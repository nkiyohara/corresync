package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nkiyohara/owa-bridge/internal/buildinfo"
	"github.com/nkiyohara/owa-bridge/internal/config"
	"github.com/nkiyohara/owa-bridge/internal/daemonapi"
	"github.com/nkiyohara/owa-bridge/internal/localipc"
)

type lifecycleTestDaemon struct {
	stop      func()
	stopped   <-chan struct{}
	shutdowns *atomic.Int32
}

func TestOpenDaemonReplacesOutdatedOwner(t *testing.T) {
	t.Parallel()

	for _, protocolVersion := range []int{
		daemonapi.ProtocolVersion,
		daemonapi.ProtocolVersion - 1,
	} {
		t.Run(fmt.Sprintf("protocol-%d", protocolVersion), func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			configPath := filepath.Join(root, "config.toml")
			if err := config.Save(configPath, config.Default()); err != nil {
				t.Fatalf("config.Save() error = %v", err)
			}
			configDigest, err := config.Fingerprint(configPath)
			if err != nil {
				t.Fatalf("config.Fingerprint() error = %v", err)
			}
			endpoint, err := localipc.ResolveInState(
				configPath,
				filepath.Join(root, "state"),
			)
			if err != nil {
				t.Fatalf("ResolveInState() error = %v", err)
			}
			previous := startLifecycleTestDaemon(
				t.Context(),
				t,
				endpoint,
				protocolVersion,
				"0.4.1",
				123,
				configDigest,
			)
			t.Cleanup(previous.stop)

			var starts atomic.Int32
			var replacement lifecycleTestDaemon
			app := newRuntime(
				t.Context(),
				configPath,
				&bytes.Buffer{},
				&bytes.Buffer{},
				buildinfo.Current(),
			)
			app.endpoint = func(string) (localipc.Endpoint, error) { return endpoint, nil }
			app.startDaemon = func(ctx context.Context, path string) error {
				if path != configPath {
					return fmt.Errorf("replacement config path = %q, want %q", path, configPath)
				}
				starts.Add(1)
				replacement = startLifecycleTestDaemon(
					ctx,
					t,
					endpoint,
					daemonapi.ProtocolVersion,
					app.info.Version,
					456,
					configDigest,
				)
				return nil
			}

			client, status, err := app.openDaemon(t.Context())
			if err != nil {
				t.Fatalf("openDaemon() error = %v", err)
			}
			t.Cleanup(func() { _ = client.Close() })
			if replacement.stop != nil {
				t.Cleanup(replacement.stop)
			}
			if status.Version != app.info.Version ||
				status.ProtocolVersion != daemonapi.ProtocolVersion ||
				status.ProcessID != 456 {
				t.Fatalf("replacement status = %+v", status)
			}
			if starts.Load() != 1 {
				t.Fatalf("replacement starts = %d, want 1", starts.Load())
			}
			select {
			case <-previous.stopped:
			case <-time.After(time.Second):
				t.Fatal("outdated daemon did not stop")
			}
		})
	}
}

func TestOpenDaemonDoesNotApplyChangedConfigDuringReplacement(t *testing.T) {
	t.Parallel()

	for _, protocolVersion := range []int{
		daemonapi.ProtocolVersion,
		daemonapi.ProtocolVersion - 1,
	} {
		t.Run(fmt.Sprintf("protocol-%d", protocolVersion), func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			configPath := filepath.Join(root, "config.toml")
			if err := config.Save(configPath, config.Default()); err != nil {
				t.Fatalf("config.Save() error = %v", err)
			}
			endpoint, err := localipc.ResolveInState(
				configPath,
				filepath.Join(root, "state"),
			)
			if err != nil {
				t.Fatalf("ResolveInState() error = %v", err)
			}
			previous := startLifecycleTestDaemon(
				t.Context(),
				t,
				endpoint,
				protocolVersion,
				"0.4.1",
				123,
				strings.Repeat("b", 64),
			)
			t.Cleanup(previous.stop)

			var starts atomic.Int32
			app := newRuntime(
				t.Context(),
				configPath,
				&bytes.Buffer{},
				&bytes.Buffer{},
				buildinfo.Current(),
			)
			app.endpoint = func(string) (localipc.Endpoint, error) { return endpoint, nil }
			app.startDaemon = func(context.Context, string) error {
				starts.Add(1)
				return errors.New("replacement must not start")
			}

			client, _, err := app.openDaemon(t.Context())
			if client != nil {
				_ = client.Close()
			}
			if err == nil || !strings.Contains(err.Error(), "stale configuration") {
				t.Fatalf("openDaemon() error = %v, want stale configuration", err)
			}
			if starts.Load() != 0 {
				t.Fatalf("replacement starts = %d, want 0", starts.Load())
			}
			select {
			case <-previous.stopped:
				t.Fatal("daemon stopped despite a changed configuration")
			default:
			}
		})
	}
}

func TestReplaceDaemonDoesNotStopNewGeneration(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	configPath := filepath.Join(root, "config.toml")
	if err := config.Save(configPath, config.Default()); err != nil {
		t.Fatalf("config.Save() error = %v", err)
	}
	configDigest, err := config.Fingerprint(configPath)
	if err != nil {
		t.Fatalf("config.Fingerprint() error = %v", err)
	}
	endpoint, err := localipc.ResolveInState(configPath, filepath.Join(root, "state"))
	if err != nil {
		t.Fatalf("ResolveInState() error = %v", err)
	}
	previous := startLifecycleTestDaemon(
		t.Context(),
		t,
		endpoint,
		daemonapi.ProtocolVersion,
		"0.4.1",
		123,
		configDigest,
	)
	t.Cleanup(previous.stop)

	firstClient, err := daemonapi.NewClient(endpoint)
	if err != nil {
		t.Fatalf("NewClient(first) error = %v", err)
	}
	t.Cleanup(func() { _ = firstClient.Close() })
	secondClient, err := daemonapi.NewClient(endpoint)
	if err != nil {
		t.Fatalf("NewClient(second) error = %v", err)
	}
	t.Cleanup(func() { _ = secondClient.Close() })
	callerApp := newRuntime(
		t.Context(),
		configPath,
		&bytes.Buffer{},
		&bytes.Buffer{},
		buildinfo.Current(),
	)
	firstOwner, err := firstClient.InspectOwner(t.Context(), callerApp.caller())
	if err != nil {
		t.Fatalf("InspectOwner(first) error = %v", err)
	}
	secondOwner, err := secondClient.InspectOwner(t.Context(), callerApp.caller())
	if err != nil {
		t.Fatalf("InspectOwner(second) error = %v", err)
	}

	var starts atomic.Int32
	var replacement lifecycleTestDaemon
	var replacementMu sync.Mutex
	callerApp.startDaemon = func(ctx context.Context, _ string) error {
		replacementMu.Lock()
		defer replacementMu.Unlock()
		if replacement.stop == nil {
			starts.Add(1)
			replacement = startLifecycleTestDaemon(
				ctx,
				t,
				endpoint,
				daemonapi.ProtocolVersion,
				callerApp.info.Version,
				456,
				configDigest,
			)
		}
		return nil
	}

	firstStatus, err := callerApp.replaceDaemon(
		t.Context(),
		firstClient,
		firstOwner,
		configPath,
		configDigest,
	)
	if err != nil {
		t.Fatalf("replaceDaemon(first) error = %v", err)
	}
	t.Cleanup(replacement.stop)
	secondStatus, err := callerApp.replaceDaemon(
		t.Context(),
		secondClient,
		secondOwner,
		configPath,
		configDigest,
	)
	if err != nil {
		t.Fatalf("replaceDaemon(second) error = %v", err)
	}
	if firstStatus.ProcessID != 456 || secondStatus.ProcessID != 456 {
		t.Fatalf("replacement statuses = %+v and %+v", firstStatus, secondStatus)
	}
	if starts.Load() != 1 {
		t.Fatalf("replacement starts = %d, want 1", starts.Load())
	}
	if replacement.shutdowns.Load() != 0 {
		t.Fatalf(
			"delayed updater stopped replacement %d time(s)",
			replacement.shutdowns.Load(),
		)
	}
}

func TestWaitForDaemonPreservesLastFailure(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	endpoint, err := localipc.ResolveInState(
		filepath.Join(root, "config.toml"),
		filepath.Join(root, "state"),
	)
	if err != nil {
		t.Fatalf("ResolveInState() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(endpoint.CredentialPath), 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(endpoint.CredentialPath, []byte("invalid\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	client, err := daemonapi.NewClient(endpoint)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	app := newRuntime(
		t.Context(),
		filepath.Join(root, "config.toml"),
		&bytes.Buffer{},
		&bytes.Buffer{},
		buildinfo.Current(),
	)
	ctx, cancel := context.WithTimeout(t.Context(), 80*time.Millisecond)
	defer cancel()
	_, err = waitForDaemon(ctx, app, client, time.Second)
	if err == nil || !strings.Contains(err.Error(), "invalid IPC credential") {
		t.Fatalf("waitForDaemon() error = %v, want credential cause", err)
	}
}

func startLifecycleTestDaemon(
	ctx context.Context,
	t *testing.T,
	endpoint localipc.Endpoint,
	protocolVersion int,
	version string,
	processID int,
	configDigest string,
) lifecycleTestDaemon {
	t.Helper()

	listener, err := localipc.Listen(endpoint)
	if err != nil {
		t.Fatalf("localipc.Listen() error = %v", err)
	}
	credential, err := localipc.IssueCredential(endpoint)
	if err != nil {
		_ = listener.Close()
		t.Fatalf("IssueCredential() error = %v", err)
	}

	type requestEnvelope struct {
		Version int             `json:"version"`
		ID      string          `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}
	type responseError struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	type responseEnvelope struct {
		Version int             `json:"version"`
		ID      string          `json:"id,omitempty"`
		Result  json.RawMessage `json:"result,omitempty"`
		Error   *responseError  `json:"error,omitempty"`
	}

	var stopOnce sync.Once
	stopped := make(chan struct{})
	shutdowns := &atomic.Int32{}
	serveDone := make(chan error, 1)
	var server *http.Server
	stop := func() {
		stopOnce.Do(func() {
			shutdownCtx, cancel := context.WithTimeout(ctx, time.Second)
			_ = server.Shutdown(shutdownCtx)
			cancel()
			_ = listener.Close()
			_ = credential.Close()
			<-serveDone
			close(stopped)
		})
	}
	server = &http.Server{
		ReadHeaderTimeout: time.Second,
		Handler: http.HandlerFunc(func(
			writer http.ResponseWriter,
			request *http.Request,
		) {
			if request.Header.Get("Authorization") != "Bearer "+credential.Value() {
				writer.Header().Set("Content-Type", "application/json")
				writer.WriteHeader(http.StatusUnauthorized)
				if err := json.NewEncoder(writer).Encode(responseEnvelope{
					Version: protocolVersion,
					Error: &responseError{
						Code:    "unauthorized",
						Message: "daemon authorization failed",
					},
				}); err != nil {
					t.Errorf("write lifecycle authorization response: %v", err)
				}
				return
			}
			var envelope requestEnvelope
			if err := json.NewDecoder(request.Body).Decode(&envelope); err != nil {
				t.Errorf("decode lifecycle request: %v", err)
				writer.WriteHeader(http.StatusBadRequest)
				return
			}
			response := responseEnvelope{Version: protocolVersion, ID: envelope.ID}
			statusCode := http.StatusOK
			shutdown := false
			var responseErr error
			switch {
			case envelope.Version != protocolVersion:
				statusCode = http.StatusBadRequest
				response.Error = &responseError{
					Code:    "invalid_request",
					Message: fmt.Sprintf("unsupported daemon protocol version %d", envelope.Version),
				}
			case envelope.Method == "status":
				encoded, encodeErr := json.Marshal(daemonapi.Status{
					ProtocolVersion: protocolVersion,
					Version:         version,
					ProcessID:       processID,
					StartedAt:       time.Unix(1, 0).UTC(),
					DefaultAccount:  "work",
					ConfigDigest:    configDigest,
				})
				response.Result = encoded
				responseErr = encodeErr
			case envelope.Method == "session.status":
				encoded, encodeErr := json.Marshal(daemonapi.SessionStatusResult{
					Accounts: []daemonapi.SessionStatus{{
						Account: "work",
						State:   "signed_out",
					}},
				})
				response.Result = encoded
				responseErr = encodeErr
			case envelope.Method == "shutdown":
				response.Result = json.RawMessage(`{"stopping":true}`)
				shutdown = true
				shutdowns.Add(1)
			default:
				response.Error = &responseError{
					Code:    "operation_failed",
					Message: "unsupported lifecycle test method",
				}
			}
			if responseErr != nil {
				t.Errorf("encode lifecycle response: %v", responseErr)
				writer.WriteHeader(http.StatusInternalServerError)
				return
			}
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(statusCode)
			if err := json.NewEncoder(writer).Encode(response); err != nil {
				t.Errorf("write lifecycle response: %v", err)
			}
			if shutdown {
				go stop()
			}
		}),
	}
	go func() { serveDone <- server.Serve(listener) }()
	return lifecycleTestDaemon{stop: stop, stopped: stopped, shutdowns: shutdowns}
}
