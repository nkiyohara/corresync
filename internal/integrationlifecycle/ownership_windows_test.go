//go:build windows

package integrationlifecycle

import (
	"os"
	"testing"
)

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
