//go:build windows

package integrationlifecycle

import (
	"os"
	"syscall"
	"testing"
	"time"
)

type windowsFileInfo struct {
	attributes uint32
}

func (windowsFileInfo) Name() string       { return "fixture" }
func (windowsFileInfo) Size() int64        { return 0 }
func (windowsFileInfo) Mode() os.FileMode  { return os.ModeDir | 0o777 }
func (windowsFileInfo) ModTime() time.Time { return time.Time{} }
func (windowsFileInfo) IsDir() bool        { return true }
func (info windowsFileInfo) Sys() any {
	return &syscall.Win32FileAttributeData{FileAttributes: info.attributes}
}

func TestWindowsDoesNotInterpretSynthesizedUnixModeBits(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "permissions-*.exe")
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(file.Name())
	if err != nil {
		t.Fatal(err)
	}
	if WritableByOtherUsers(info) {
		t.Fatalf("Windows synthesized mode %v was treated as an ACL write grant", info.Mode())
	}
	if !ExecutableByUser(info) {
		t.Fatalf("Windows executable was rejected from synthesized mode %v", info.Mode())
	}
}

func TestWindowsRejectsReparsePointsThatAreNotModeSymlinks(t *testing.T) {
	t.Parallel()
	junction := windowsFileInfo{attributes: syscall.FILE_ATTRIBUTE_REPARSE_POINT}
	if junction.Mode()&os.ModeSymlink != 0 {
		t.Fatal("fixture unexpectedly reports ModeSymlink")
	}
	if !IsSymlinkOrReparsePoint(junction) {
		t.Fatal("Windows reparse point was accepted")
	}
	if IsSymlinkOrReparsePoint(windowsFileInfo{}) {
		t.Fatal("ordinary Windows directory was rejected")
	}
}
