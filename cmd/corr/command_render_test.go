package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"reflect"
	runtimepkg "runtime"
	"strings"
	"testing"
)

func TestRenderedCommandRoundTripsLiteralPaths(t *testing.T) {
	t.Parallel()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// These are valid Windows and Unix path components, including shell syntax.
	values := []string{"plain", "space name", "cash$HOME", "$(printf AUDIT)", "tick`echo AUDIT`", "semi;colon", "amp&ersand", "per%PATH%cent", "bang!", "paren(s)", "single'quote", `C:\path with spaces\corr.exe`}
	if runtimepkg.GOOS != "windows" {
		values = append(values, "", `double"quote`, "line\nbreak", "tab\tvalue", "wild*?[x]")
	}
	arguments := append([]string{"-test.run=^TestRenderedCommandArgvHelper$", "--"}, values...)
	rendered := formatCommand(executable, arguments)
	var command *exec.Cmd
	if runtimepkg.GOOS == "windows" {
		shell, err := exec.LookPath("pwsh")
		if err != nil {
			shell, err = exec.LookPath("powershell")
		}
		if err != nil {
			t.Fatal("PowerShell is required for the Windows rendering contract")
		}
		command = exec.CommandContext(t.Context(), shell, "-NoProfile", "-NonInteractive", "-Command", rendered) // #nosec G204 -- Executes this test binary with synthetic literal arguments to verify quoting.
	} else {
		command = exec.CommandContext(t.Context(), "/bin/sh", "-c", rendered) // #nosec G204 -- Executes this test binary with synthetic literal arguments to verify quoting.
	}
	command.Env = append(os.Environ(), "CORRESYNC_RENDER_ARGV_HELPER=1")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("run rendered command: %v, output=%s", err, output)
	}
	var got []string
	if err := json.Unmarshal(output, &got); err != nil {
		t.Fatalf("decode helper: %v, output=%s", err, output)
	}
	if !reflect.DeepEqual(got, values) {
		t.Fatalf("argv=%q, want=%q", got, values)
	}
}

func TestRenderedCommandArgvHelper(_ *testing.T) {
	if os.Getenv("CORRESYNC_RENDER_ARGV_HELPER") != "1" {
		return
	}
	for index, value := range os.Args {
		if value == "--" {
			_ = json.NewEncoder(os.Stdout).Encode(os.Args[index+1:])
			os.Exit(0)
		}
	}
	os.Exit(2)
}

func TestWindowsRenderingIsExplicitPowerShell(t *testing.T) {
	got := formatCommandForOS("windows", `C:\O'Brien\corr.exe`, []string{"mcp", "serve", "$HOME;echo"})
	if !strings.HasPrefix(got, "& 'C:\\O''Brien\\corr.exe' ") || !strings.HasSuffix(got, "'$HOME;echo'") {
		t.Fatalf("PowerShell rendering=%s", got)
	}
}
