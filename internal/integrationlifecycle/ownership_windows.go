//go:build windows

package integrationlifecycle

import (
	"os"
	"syscall"
)

func ownedByCurrentUser(os.FileInfo) bool { return true }

// IsSymlinkOrReparsePoint rejects all Windows reparse points, including
// directory junctions that Go intentionally does not expose as ModeSymlink.
func IsSymlinkOrReparsePoint(info os.FileInfo) bool {
	attributes, ok := info.Sys().(*syscall.Win32FileAttributeData)
	return info.Mode()&os.ModeSymlink != 0 ||
		(ok && attributes.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0)
}

// WritableByOtherUsers relies on Windows ACL enforcement. Go's synthesized
// permission bits do not describe ACL write access.
func WritableByOtherUsers(os.FileInfo) bool { return false }

// ExecutableByUser relies on Windows executable-file handling. Go's
// synthesized permission bits do not describe executability.
func ExecutableByUser(os.FileInfo) bool { return true }

// OwnedByCurrentUserOrRoot relies on Windows ACL enforcement.
func OwnedByCurrentUserOrRoot(os.FileInfo) bool { return true }

func syncDirectory(string) error { return nil }
