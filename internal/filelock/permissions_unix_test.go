//go:build !windows

package filelock

import (
	"os"
	"testing"
)

func assertDirectoryMode(t *testing.T, info os.FileInfo, want os.FileMode) {
	t.Helper()
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("directory mode = %o, want %o", got, want)
	}
}
