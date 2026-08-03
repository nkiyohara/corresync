//go:build linux || darwin

package localipc

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

const endpointOwnerHelperEnvironment = "CORRESYNC_ENDPOINT_OWNER_TEST_HELPER"

func TestStopEndpointOwnerSignalsPinnedSameUserPeer(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "config.toml")
	stateDirectory := filepath.Join(root, "state")
	readyPath := filepath.Join(root, "ready")

	// #nosec G204,G702 -- execute the current test binary with a fixed helper selector.
	command := exec.CommandContext(
		t.Context(), os.Args[0], "-test.run=^TestEndpointOwnerSignalHelper$",
	)
	command.Env = append(os.Environ(),
		endpointOwnerHelperEnvironment+"=1",
		"CORRESYNC_ENDPOINT_OWNER_CONFIG="+configPath,
		"CORRESYNC_ENDPOINT_OWNER_STATE="+stateDirectory,
		"CORRESYNC_ENDPOINT_OWNER_READY="+readyPath,
	)
	if err := command.Start(); err != nil {
		t.Fatalf("start endpoint-owner helper: %v", err)
	}
	defer func() {
		if command.Process != nil {
			_ = command.Process.Kill()
		}
	}()

	deadline := time.Now().Add(3 * time.Second)
	for {
		encoded, err := os.ReadFile(readyPath) // #nosec G304 -- path is below t.TempDir.
		if err == nil {
			processID, parseErr := strconv.Atoi(string(encoded))
			if parseErr != nil || processID != command.Process.Pid {
				t.Fatalf("helper identity = %q, want PID %d", encoded, command.Process.Pid)
			}
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read helper readiness: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatal("endpoint-owner helper did not become ready")
		}
		time.Sleep(10 * time.Millisecond)
	}

	endpoint, err := ResolveInState(configPath, stateDirectory)
	if err != nil {
		t.Fatalf("ResolveInState() error = %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	processID, err := StopEndpointOwner(ctx, endpoint)
	if err != nil {
		t.Fatalf("StopEndpointOwner() error = %v", err)
	}
	if processID != command.Process.Pid {
		t.Fatalf("StopEndpointOwner() PID = %d, want %d", processID, command.Process.Pid)
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("endpoint-owner helper exit: %v", err)
	}
	command.Process = nil
	active, err := EndpointActive(endpoint)
	if err != nil || active {
		t.Fatalf("EndpointActive() after stop = %t, %v", active, err)
	}
}

func TestEndpointOwnerSignalHelper(t *testing.T) {
	if os.Getenv(endpointOwnerHelperEnvironment) != "1" {
		t.Skip("helper process only")
	}
	configPath := os.Getenv("CORRESYNC_ENDPOINT_OWNER_CONFIG")
	stateDirectory := os.Getenv("CORRESYNC_ENDPOINT_OWNER_STATE")
	readyPath := os.Getenv("CORRESYNC_ENDPOINT_OWNER_READY")
	endpoint, err := ResolveInState(configPath, stateDirectory)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := Listen(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM)
	defer signal.Stop(signals)
	// #nosec G703 -- the parent supplies a path confined to its t.TempDir.
	if err := os.WriteFile(
		readyPath,
		[]byte(strconv.Itoa(os.Getpid())),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	select {
	case <-signals:
	case <-time.After(5 * time.Second):
		t.Fatal("helper did not receive termination")
	}
}
