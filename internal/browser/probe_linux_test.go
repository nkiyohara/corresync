//go:build linux

package browser

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestProbeClassifiesLinuxSandboxStartupFailure(t *testing.T) {
	t.Parallel()
	executable := filepath.Join(t.TempDir(), "synthetic-chrome")
	contents := "#!/bin/sh\n" +
		"echo 'Failed to move to new namespace: Operation not permitted' >&2\n" +
		"exit 1\n"
	// #nosec G306 -- the owner-only test fixture must be executable.
	if err := os.WriteFile(executable, []byte(contents), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Probe(t.Context(), executable); !errors.Is(err, ErrLinuxSandboxUnavailable) {
		t.Fatalf("Probe() error = %v", err)
	}
}
