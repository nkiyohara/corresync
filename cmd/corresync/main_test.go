package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nkiyohara/corresync/internal/buildinfo"
)

func TestRunShowsHelpWithoutArguments(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run(context.Background(), nil, &stdout, &stderr); code != 0 {
		t.Fatalf("run() code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Local-first, guarded mail and calendar") {
		t.Fatalf("help output did not contain description: %q", stdout.String())
	}
}

func TestRunShowsCommandGroupHelp(t *testing.T) {
	t.Parallel()

	for _, arguments := range [][]string{
		{"mcp", "--help"},
		{"mcp", "setup", "--help"},
		{"mail", "--help"},
	} {
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		if code := run(context.Background(), arguments, &stdout, &stderr); code != 0 {
			t.Errorf("run(%q) code = %d, stderr = %q", arguments, code, stderr.String())
		}
		if !strings.Contains(stdout.String(), "Usage:") {
			t.Errorf("run(%q) stdout = %q, want usage", arguments, stdout.String())
		}
	}
}

func TestRootHelpCommandDescriptionsMatchCommandHelp(t *testing.T) {
	t.Parallel()

	var root bytes.Buffer
	var rootErr bytes.Buffer
	if code := run(context.Background(), nil, &root, &rootErr); code != 0 {
		t.Fatalf("root help code = %d, stderr = %q", code, rootErr.String())
	}
	commands := []string{
		"config",
		"doctor",
		"auth",
		"mail",
		"calendar",
		"daemon",
		"mcp",
		"update",
		"completion",
		"version",
	}
	for _, command := range commands {
		description := rootCommandDescription(t, root.String(), command)
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		if code := run(context.Background(), []string{command, "--help"}, &stdout, &stderr); code != 0 {
			t.Fatalf("%s help code = %d, stderr = %q", command, code, stderr.String())
		}
		if !strings.Contains(stdout.String(), description+".") {
			t.Errorf(
				"%s root description %q is not present in command help:\n%s",
				command,
				description,
				stdout.String(),
			)
		}
	}
}

func TestRunSupportsConventionalVersionAndHelpForms(t *testing.T) {
	t.Parallel()

	for _, arguments := range [][]string{{"--version"}, {"-V"}} {
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		if code := run(context.Background(), arguments, &stdout, &stderr); code != 0 {
			t.Fatalf("run(%q) code = %d, stderr = %q", arguments, code, stderr.String())
		}
		if !strings.HasPrefix(stdout.String(), "corresync dev ") {
			t.Fatalf("run(%q) stdout = %q", arguments, stdout.String())
		}
	}
	for _, arguments := range [][]string{{"help"}, {"help", "config"}} {
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		if code := run(context.Background(), arguments, &stdout, &stderr); code != 0 {
			t.Fatalf("run(%q) code = %d, stderr = %q", arguments, code, stderr.String())
		}
		if !strings.Contains(stdout.String(), "Usage:") {
			t.Fatalf("run(%q) stdout = %q, want help", arguments, stdout.String())
		}
	}
}

func TestLegacyRootLoginHelpRemainsAvailable(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run(context.Background(), []string{"login", "--help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("legacy login help code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Open the interactive provider sign-in") {
		t.Fatalf("legacy login help is incomplete: %q", stdout.String())
	}
}

func TestUpdateDefaultsToActionAndKeepsCheckOnlySubcommand(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run(context.Background(), []string{"update", "--json"}, &stdout, &stderr); code != 1 {
		t.Fatalf("development update code = %d, want 1; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "development builds cannot self-update") {
		t.Fatalf("corresync update did not select the default update action: %q", stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), []string{"update", "check", "--help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("update check help code = %d, stderr=%q", code, stderr.String())
	}
	normalizedHelp := strings.Join(strings.Fields(stdout.String()), " ")
	if !strings.Contains(normalizedHelp, "Use check to report the latest stable release without installing") {
		t.Fatalf("update check help is ambiguous: %q", stdout.String())
	}
}

func TestRunInitializesAndValidatesConfig(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "nested", "config.toml")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	arguments := []string{"--config", path, "config", "init", "--json"}
	if code := run(context.Background(), arguments, &stdout, &stderr); code != 0 {
		t.Fatalf("config init code = %d, stderr = %q", code, stderr.String())
	}
	var initialized struct {
		Created bool   `json:"created"`
		Path    string `json:"path"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &initialized); err != nil {
		t.Fatalf("decode config init output: %v", err)
	}
	if !initialized.Created || initialized.Path != path {
		t.Fatalf("unexpected config init output: %+v", initialized)
	}

	stdout.Reset()
	stderr.Reset()
	arguments = []string{"--config", path, "config", "validate", "--json"}
	if code := run(context.Background(), arguments, &stdout, &stderr); code != 0 {
		t.Fatalf("config validate code = %d, stderr = %q", code, stderr.String())
	}
	var validated struct {
		Valid bool `json:"valid"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &validated); err != nil {
		t.Fatalf("decode config validate output: %v", err)
	}
	if !validated.Valid {
		t.Fatalf("unexpected config validate output: %s", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), []string{"--config", path, "config", "init"}, &stdout, &stderr); code != 1 {
		t.Fatalf("second config init code = %d, want 1", code)
	}
}

func TestRunPrintsVersionAsJSON(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run(context.Background(), []string{"version", "--json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("run() code = %d, stderr = %q", code, stderr.String())
	}

	var info buildinfo.Info
	if err := json.Unmarshal(stdout.Bytes(), &info); err != nil {
		t.Fatalf("decode version output: %v", err)
	}
	if info.Version == "" || info.Commit == "" || info.GoVersion == "" {
		t.Fatalf("incomplete build info: %+v", info)
	}
}

func TestRunRejectsUnknownCommand(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run(context.Background(), []string{"unknown"}, &stdout, &stderr); code != 2 {
		t.Fatalf("run() code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "unexpected argument") {
		t.Fatalf("stderr did not explain parse error: %q", stderr.String())
	}
}

func rootCommandDescription(t *testing.T, help, command string) string {
	t.Helper()
	for _, line := range strings.Split(help, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == command {
			return strings.Join(fields[1:], " ")
		}
	}
	t.Fatalf("root help is missing command %q:\n%s", command, help)
	return ""
}
