//go:build !windows

package integrationlifecycle

import (
	"os"
	"testing"
)

func assertPermissionBits(t *testing.T, info os.FileInfo, want os.FileMode) {
	t.Helper()
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("permission bits = %o, want %o", got, want)
	}
}

func requirePermissionBits(*testing.T) {}
