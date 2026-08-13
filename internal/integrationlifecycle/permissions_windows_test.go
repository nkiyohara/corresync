//go:build windows

package integrationlifecycle

import (
	"os"
	"testing"
)

// Windows reports synthesized Unix permission bits that do not describe the
// ACLs enforced by the filesystem. The production ownership checks have the
// same platform boundary in ownership_windows.go.
func assertPermissionBits(*testing.T, os.FileInfo, os.FileMode) {}

func requirePermissionBits(t *testing.T) {
	t.Helper()
	t.Skip("Unix permission-bit semantics are unavailable on Windows")
}
