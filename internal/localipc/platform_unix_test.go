//go:build linux || darwin

package localipc

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestPlatformEndpointFallsBackFromLongTemporaryDirectory(t *testing.T) {
	t.Parallel()

	id := strings.Repeat("a", 32)
	address, runtimeDirectory, lockPath, err := platformEndpointInTemp(
		filepath.Join("/private/var/folders", strings.Repeat("x", 64), "T"),
		id,
		501,
		"corresync",
	)
	if err != nil {
		t.Fatalf("platformEndpointInTemp() error = %v", err)
	}
	wantRuntimeDirectory := "/tmp/corresync-501"
	if runtimeDirectory != wantRuntimeDirectory {
		t.Fatalf("runtime directory = %q, want %q", runtimeDirectory, wantRuntimeDirectory)
	}
	if address != filepath.Join(wantRuntimeDirectory, id+".sock") ||
		lockPath != filepath.Join(wantRuntimeDirectory, id+".lock") {
		t.Fatalf("unexpected fallback endpoint: address=%q lock=%q", address, lockPath)
	}
	if len(address) > maximumUnixSocketPath {
		t.Fatalf("fallback socket path is %d bytes", len(address))
	}
}

func TestPlatformEndpointKeepsShortTemporaryDirectory(t *testing.T) {
	t.Parallel()

	id := strings.Repeat("b", 32)
	address, runtimeDirectory, _, err := platformEndpointInTemp("/short", id, 42, "corresync")
	if err != nil {
		t.Fatalf("platformEndpointInTemp() error = %v", err)
	}
	if runtimeDirectory != "/short/corresync-42" ||
		address != filepath.Join(runtimeDirectory, id+".sock") {
		t.Fatalf("short endpoint unexpectedly changed: address=%q runtime=%q", address, runtimeDirectory)
	}
}

func TestPlatformEndpointIsIndependentOfRuntimeEnvironment(t *testing.T) {
	firstXDG := shortTemporaryDirectory(t, "corresync-xdg-first-")
	firstTemp := shortTemporaryDirectory(t, "corresync-tmp-first-")
	t.Setenv("XDG_RUNTIME_DIR", firstXDG)
	t.Setenv("TMPDIR", firstTemp)

	id := strings.Repeat("c", 32)
	firstAddress, firstDirectory, firstLock, err := platformEndpoint(id, "corresync")
	if err != nil {
		t.Fatalf("platformEndpoint(first) error = %v", err)
	}

	secondXDG := shortTemporaryDirectory(t, "corresync-xdg-second-")
	secondTemp := shortTemporaryDirectory(t, "corresync-tmp-second-")
	t.Setenv("XDG_RUNTIME_DIR", secondXDG)
	t.Setenv("TMPDIR", secondTemp)
	secondAddress, secondDirectory, secondLock, err := platformEndpoint(id, "corresync")
	if err != nil {
		t.Fatalf("platformEndpoint(second) error = %v", err)
	}

	if firstAddress != secondAddress ||
		firstDirectory != secondDirectory ||
		firstLock != secondLock {
		t.Fatalf(
			"runtime environment split one namespace: first=(%q, %q, %q) second=(%q, %q, %q)",
			firstAddress,
			firstDirectory,
			firstLock,
			secondAddress,
			secondDirectory,
			secondLock,
		)
	}
	if firstDirectory != filepath.Join(fallbackUnixTempDir, "corresync-"+strconv.Itoa(os.Geteuid())) {
		t.Fatalf("canonical runtime directory = %q", firstDirectory)
	}
}

func TestPreviousEndpointRetainsV085XDGRuntimeLocation(t *testing.T) {
	runtimeBase := shortTemporaryDirectory(t, "corresync-previous-xdg-")
	t.Setenv("XDG_RUNTIME_DIR", runtimeBase)
	configPath := filepath.Join(t.TempDir(), "config.toml")
	stateDirectory := filepath.Join(t.TempDir(), "state")
	current, err := ResolveInState(configPath, stateDirectory)
	if err != nil {
		t.Fatalf("ResolveInState() error = %v", err)
	}
	previous, err := ResolvePreviousInState(configPath, stateDirectory)
	if err != nil {
		t.Fatalf("ResolvePreviousInState() error = %v", err)
	}
	wantDirectory := filepath.Join(runtimeBase, "corresync")
	found := false
	for _, endpoint := range previous {
		if endpoint.Address == current.Address {
			t.Fatalf("previous endpoints included canonical address %q", current.Address)
		}
		if endpoint.runtimeDir == wantDirectory {
			found = true
			if endpoint.CredentialPath != current.CredentialPath ||
				endpoint.ID != current.ID {
				t.Fatalf("previous endpoint changed namespace: %+v current=%+v", endpoint, current)
			}
		}
	}
	if !found {
		t.Fatalf("previous endpoints = %+v, want runtime directory %q", previous, wantDirectory)
	}
}

func TestLegacyPlatformEndpointRetainsXDGRuntimeLocation(t *testing.T) {
	runtimeBase := shortTemporaryDirectory(t, "owa-legacy-xdg-")
	t.Setenv("XDG_RUNTIME_DIR", runtimeBase)
	address, runtimeDirectory, lockPath, err := legacyPlatformEndpoint(
		strings.Repeat("d", 32),
		"owa-bridge",
	)
	if err != nil {
		t.Fatalf("legacyPlatformEndpoint() error = %v", err)
	}
	wantDirectory := filepath.Join(runtimeBase, "owa-bridge")
	if runtimeDirectory != wantDirectory ||
		address != filepath.Join(wantDirectory, strings.Repeat("d", 32)+".sock") ||
		lockPath != filepath.Join(wantDirectory, strings.Repeat("d", 32)+".lock") {
		t.Fatalf(
			"legacy endpoint = (%q, %q, %q), want directory %q",
			address,
			runtimeDirectory,
			lockPath,
			wantDirectory,
		)
	}
}

func TestEndpointActiveRequiresHeldSingleton(t *testing.T) {
	directory := privateRuntimeDirectory(t)
	endpoint := endpointForDirectory(directory)
	active, err := EndpointActive(endpoint)
	if err != nil || active {
		t.Fatalf("EndpointActive(absent) = %t, %v", active, err)
	}
	listener, err := Listen(endpoint)
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	active, err = EndpointActive(endpoint)
	if err != nil || !active {
		t.Fatalf("EndpointActive(listening) = %t, %v", active, err)
	}
	if err := listener.Close(); err != nil {
		t.Fatalf("listener.Close() error = %v", err)
	}
	active, err = EndpointActive(endpoint)
	if err != nil || active {
		t.Fatalf("EndpointActive(closed) = %t, %v", active, err)
	}
}

func TestDialContextRejectsUnsafeSocketNodes(t *testing.T) {
	t.Parallel()

	tests := map[string]func(string) error{
		"symlink": func(path string) error {
			return os.Symlink(filepath.Join(filepath.Dir(path), "missing"), path)
		},
		"regular file": func(path string) error {
			return os.WriteFile(path, []byte("synthetic"), 0o600)
		},
		"FIFO": func(path string) error {
			return unix.Mkfifo(path, 0o600)
		},
	}
	for name, create := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			endpoint, lock := endpointWithHeldLock(t)
			defer unlockAndClose(t, lock)
			if err := create(endpoint.Address); err != nil {
				t.Fatalf("create unsafe node: %v", err)
			}

			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			if connection, err := DialContext(ctx, endpoint); err == nil {
				_ = connection.Close()
				t.Fatal("DialContext() accepted unsafe socket node")
			}
		})
	}
}

func TestDialContextRejectsPermissiveRuntimeDirectory(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	if err := os.Chmod(directory, 0o755); err != nil { // #nosec G302 -- testing rejection.
		t.Fatalf("Chmod() error = %v", err)
	}
	endpoint := endpointForDirectory(directory)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if connection, err := DialContext(ctx, endpoint); err == nil {
		_ = connection.Close()
		t.Fatal("DialContext() accepted permissive runtime directory")
	}
}

func TestValidateUnixNodeRejectsWrongOwner(t *testing.T) {
	t.Parallel()

	uid, err := currentEffectiveUID()
	if err != nil {
		t.Fatalf("currentEffectiveUID() error = %v", err)
	}
	stat := unix.Stat_t{Mode: unix.S_IFSOCK | 0o600, Uid: uid + 1}
	if err := validateUnixNode(stat, uid, unix.S_IFSOCK, "endpoint socket"); err == nil {
		t.Fatal("validateUnixNode() accepted a node owned by another user")
	}
}

func TestDialContextRejectsSocketWithoutSingletonOwner(t *testing.T) {
	t.Parallel()

	directory := privateRuntimeDirectory(t)
	endpoint := endpointForDirectory(directory)
	uid, err := currentEffectiveUID()
	if err != nil {
		t.Fatalf("currentEffectiveUID() error = %v", err)
	}
	lock, err := openSingletonLock(endpoint.lockPath, uid)
	if err != nil {
		t.Fatalf("openSingletonLock() error = %v", err)
	}
	t.Cleanup(func() { _ = lock.Close() })
	squatter, err := net.ListenUnix(
		"unix",
		&net.UnixAddr{Name: endpoint.Address, Net: "unix"},
	)
	if err != nil {
		t.Fatalf("ListenUnix() error = %v", err)
	}
	t.Cleanup(func() { _ = squatter.Close() })
	if err := os.Chmod(endpoint.Address, 0o600); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if connection, err := DialContext(ctx, endpoint); err == nil {
		_ = connection.Close()
		t.Fatal("DialContext() accepted a socket without an active singleton owner")
	}
}

func TestDialContextRejectsSocketReplacementDuringConnect(t *testing.T) {
	t.Parallel()

	directory := privateRuntimeDirectory(t)
	endpoint := endpointForDirectory(directory)
	listener, err := Listen(endpoint)
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	var squatter *net.UnixListener
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	connection, err := dialAuthenticatedUnix(
		ctx,
		endpoint,
		func(ctx context.Context, address string) (net.Conn, error) {
			original := address + ".original"
			if renameErr := os.Rename(address, original); renameErr != nil {
				return nil, renameErr
			}
			var listenErr error
			squatter, listenErr = net.ListenUnix(
				"unix",
				&net.UnixAddr{Name: address, Net: "unix"},
			)
			if listenErr != nil {
				return nil, listenErr
			}
			if chmodErr := os.Chmod(address, 0o600); chmodErr != nil {
				return nil, chmodErr
			}
			dialer := net.Dialer{}
			return dialer.DialContext(ctx, "unix", address)
		},
	)
	if connection != nil {
		_ = connection.Close()
	}
	if squatter != nil {
		t.Cleanup(func() { _ = squatter.Close() })
	}
	if err == nil || !strings.Contains(err.Error(), "socket changed") {
		t.Fatalf("dialAuthenticatedUnix() error = %v, want socket replacement rejection", err)
	}
}

func endpointWithHeldLock(t *testing.T) (Endpoint, *os.File) {
	t.Helper()

	directory := privateRuntimeDirectory(t)
	endpoint := endpointForDirectory(directory)
	uid, err := currentEffectiveUID()
	if err != nil {
		t.Fatalf("currentEffectiveUID() error = %v", err)
	}
	lock, err := openSingletonLock(endpoint.lockPath, uid)
	if err != nil {
		t.Fatalf("openSingletonLock() error = %v", err)
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = lock.Close()
		t.Fatalf("Flock() error = %v", err)
	}
	return endpoint, lock
}

func privateRuntimeDirectory(t *testing.T) string {
	t.Helper()

	// macOS runner TempDir paths can exceed sockaddr_un.sun_path before the
	// synthetic socket filename is appended. The production resolver already
	// has a tested short-path fallback; keep these node-attack tests focused on
	// ownership and replacement by using an owner-only short temporary path.
	directory := shortTemporaryDirectory(t, "corr-ipc-")
	if socketPath := filepath.Join(directory, "synthetic.sock"); len(socketPath) > maximumUnixSocketPath {
		t.Fatalf("short test socket path is %d bytes: %q", len(socketPath), socketPath)
	}
	if err := os.Chmod(directory, 0o700); err != nil { // #nosec G302 -- owner-only directory.
		t.Fatalf("Chmod() error = %v", err)
	}
	return directory
}

func shortTemporaryDirectory(t *testing.T, pattern string) string {
	t.Helper()

	directory, err := os.MkdirTemp("/tmp", pattern)
	if err != nil {
		t.Fatalf("MkdirTemp() error = %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(directory); err != nil {
			t.Errorf("RemoveAll() error = %v", err)
		}
	})
	return directory
}

func endpointForDirectory(directory string) Endpoint {
	return Endpoint{
		ID:         "synthetic",
		Address:    filepath.Join(directory, "synthetic.sock"),
		lockPath:   filepath.Join(directory, "synthetic.lock"),
		runtimeDir: directory,
	}
}

func unlockAndClose(t *testing.T, lock *os.File) {
	t.Helper()

	if err := errors.Join(
		unix.Flock(int(lock.Fd()), unix.LOCK_UN),
		lock.Close(),
	); err != nil {
		t.Errorf("release lock: %v", err)
	}
}
