//go:build windows

package filelock

import (
	"os"
	"syscall"
	"testing"
	"time"
)

type reparseFileInfo struct {
	attributes uint32
}

func (reparseFileInfo) Name() string       { return "fixture" }
func (reparseFileInfo) Size() int64        { return 0 }
func (reparseFileInfo) Mode() os.FileMode  { return os.ModeDir | 0o777 }
func (reparseFileInfo) ModTime() time.Time { return time.Time{} }
func (reparseFileInfo) IsDir() bool        { return true }
func (info reparseFileInfo) Sys() any {
	return &syscall.Win32FileAttributeData{FileAttributes: info.attributes}
}

func TestIsLinkLikeRejectsWindowsReparsePoint(t *testing.T) {
	t.Parallel()
	junction := reparseFileInfo{attributes: syscall.FILE_ATTRIBUTE_REPARSE_POINT}
	if junction.Mode()&os.ModeSymlink != 0 {
		t.Fatal("fixture unexpectedly reports ModeSymlink")
	}
	if !isLinkLike(junction) {
		t.Fatal("Windows reparse point was accepted")
	}
	if isLinkLike(reparseFileInfo{}) {
		t.Fatal("ordinary Windows directory was rejected")
	}
}
