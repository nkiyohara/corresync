//go:build windows

package integrationlifecycle

import "os"

func ownedByCurrentUser(os.FileInfo) bool { return true }

// WritableByOtherUsers relies on Windows ACL enforcement. Go's synthesized
// permission bits do not describe ACL write access.
func WritableByOtherUsers(os.FileInfo) bool { return false }

// ExecutableByUser relies on Windows executable-file handling. Go's
// synthesized permission bits do not describe executability.
func ExecutableByUser(os.FileInfo) bool { return true }

// OwnedByCurrentUserOrRoot relies on Windows ACL enforcement.
func OwnedByCurrentUserOrRoot(os.FileInfo) bool { return true }

func syncDirectory(string) error { return nil }
