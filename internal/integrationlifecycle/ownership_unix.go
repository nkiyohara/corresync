//go:build !windows

package integrationlifecycle

import (
	"errors"
	"os"
	"syscall"
)

func ownedByCurrentUser(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	// #nosec G115 -- Unix user IDs are non-negative and represented as uint32 by Stat_t.
	return ok && stat.Uid == uint32(os.Getuid())
}

// WritableByOtherUsers reports whether Unix group or other permission bits
// allow a different user to replace or modify a reviewed path component.
func WritableByOtherUsers(info os.FileInfo) bool {
	return info.Mode().Perm()&0o022 != 0
}

// ExecutableByUser reports whether any Unix execute bit is present. Windows
// uses executable file associations instead of Unix mode bits.
func ExecutableByUser(info os.FileInfo) bool {
	return info.Mode().Perm()&0o111 != 0
}

// OwnedByCurrentUserOrRoot reports whether a local executable path component
// has a trusted owner for registration in an external host.
func OwnedByCurrentUserOrRoot(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	// #nosec G115 -- Unix user IDs are non-negative and represented as uint32 by Stat_t.
	return ok && (stat.Uid == 0 || stat.Uid == uint32(os.Getuid()))
}

func syncDirectory(path string) error {
	// #nosec G304 -- callers pass a validated directory selected below reviewed roots.
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}
