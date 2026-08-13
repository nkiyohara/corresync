//go:build windows

package filelock

import (
	"os"
	"testing"
)

// Windows reports synthesized Unix permission bits that do not describe the
// ACLs enforced by the filesystem.
func assertDirectoryMode(*testing.T, os.FileInfo, os.FileMode) {}
