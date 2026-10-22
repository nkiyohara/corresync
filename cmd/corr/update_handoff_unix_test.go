//go:build linux || darwin

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nkiyohara/corresync/internal/updatecheck"
)

func TestUpdatedCommandPreservesArgumentsInputAndExitStatus(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corr")
	script := "#!/bin/sh\nprintf '%s|%s|%s\\n' \"$CORRESYNC_NO_UPDATE_CHECK\" \"$1\" \"$2\"\ncat\nexit 17\n"
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	} // #nosec G306 -- synthetic executable fixture.
	var stdout, stderr bytes.Buffer
	app := &runtime{executablePath: path, stdin: strings.NewReader("original input"), stdout: &stdout, stderr: &stderr}
	code := app.runUpdatedCommand(t.Context(), []string{"mail", "literal;argument"}, updatecheck.InstallResult{})
	if code != 17 || stdout.String() != "1|mail|literal;argument\noriginal input" || stderr.Len() != 0 {
		t.Fatalf("handoff = %d %q %q", code, stdout.String(), stderr.String())
	}
}
