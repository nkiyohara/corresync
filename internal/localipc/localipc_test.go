package localipc

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDeriveEndpointNamespacesConfigAndState(t *testing.T) {
	t.Parallel()

	first, err := deriveEndpoint(
		"/synthetic/config-a.toml", "/synthetic/state", namespaceVersion, "corresync",
	)
	if err != nil {
		t.Fatalf("deriveEndpoint() error = %v", err)
	}
	second, err := deriveEndpoint(
		"/synthetic/config-b.toml", "/synthetic/state", namespaceVersion, "corresync",
	)
	if err != nil {
		t.Fatalf("deriveEndpoint() error = %v", err)
	}
	if first.ID == second.ID || first.Address == second.Address || first.CredentialPath == second.CredentialPath {
		t.Fatalf("distinct configs collided: first=%+v second=%+v", first, second)
	}
}

func TestCredentialLifecycleRejectsNonRegularTarget(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	endpoint := Endpoint{CredentialPath: filepath.Join(directory, "credential")}
	credential, err := IssueCredential(endpoint)
	if err != nil {
		t.Fatalf("IssueCredential() error = %v", err)
	}
	loaded, err := LoadCredential(endpoint)
	if err != nil || loaded != credential.Value() {
		t.Fatalf("LoadCredential() = %q, %v", loaded, err)
	}
	if err := credential.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if _, err := LoadCredential(endpoint); !errors.Is(err, os.ErrNotExist) ||
		!errors.Is(err, ErrCredentialMissing) {
		t.Fatalf("LoadCredential() error = %v, want not exist", err)
	}
	if err := os.Mkdir(endpoint.CredentialPath, 0o700); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	if _, err := IssueCredential(endpoint); err == nil {
		t.Fatal("IssueCredential() accepted a directory target")
	}
}

func TestCredentialLoadFailuresAreBoundedAndClassified(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "sensitive-name.credential")
	endpoint := Endpoint{CredentialPath: path}

	_, err := LoadCredential(endpoint)
	if reason, ok := CredentialLoadFailure(err); !ok || reason != CredentialMissing {
		t.Fatalf("missing credential failure = %q, %v", reason, err)
	}
	if strings.Contains(err.Error(), path) {
		t.Fatalf("missing credential exposed path: %v", err)
	}

	if err := os.WriteFile(path, []byte("secret-invalid-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = LoadCredential(endpoint)
	if reason, ok := CredentialLoadFailure(err); !ok || reason != CredentialMalformed {
		t.Fatalf("malformed credential failure = %q, %v", reason, err)
	}
	if strings.Contains(err.Error(), path) || strings.Contains(err.Error(), "secret-invalid-value") {
		t.Fatalf("malformed credential exposed sensitive data: %v", err)
	}

	permissionErr := credentialLoadFailure(
		CredentialUnavailable,
		&os.PathError{Op: "open", Path: path, Err: os.ErrPermission},
	)
	if reason, ok := CredentialLoadFailure(permissionErr); !ok ||
		reason != CredentialPermissionDenied || strings.Contains(permissionErr.Error(), path) {
		t.Fatalf("permission credential failure = %q, %v", reason, permissionErr)
	}
}

func TestListenerIsSingletonAndDialable(t *testing.T) {
	t.Parallel()

	endpoint, err := deriveEndpoint(
		filepath.Join(t.TempDir(), "config.toml"), filepath.Join(t.TempDir(), "state"),
		namespaceVersion, "corresync",
	)
	if err != nil {
		t.Fatalf("deriveEndpoint() error = %v", err)
	}
	listener, err := Listen(endpoint)
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	if _, err := Listen(endpoint); err == nil {
		t.Fatal("second Listen() unexpectedly succeeded")
	}

	accepted := make(chan error, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr == nil {
			acceptErr = connection.Close()
		}
		accepted <- acceptErr
	}()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	connection, err := DialContext(ctx, endpoint)
	if err != nil {
		t.Fatalf("DialContext() error = %v", err)
	}
	if err := connection.Close(); err != nil {
		t.Fatalf("connection.Close() error = %v", err)
	}
	select {
	case err := <-accepted:
		if err != nil && !errors.Is(err, net.ErrClosed) {
			t.Fatalf("Accept() error = %v", err)
		}
	case <-ctx.Done():
		t.Fatal("Accept() did not complete")
	}
}
