//go:build linux || darwin

package localipc

import (
	"path/filepath"
	"strings"
	"testing"
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
