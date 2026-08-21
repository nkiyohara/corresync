package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nkiyohara/corresync/internal/buildinfo"
	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/feedback"
	"github.com/nkiyohara/corresync/internal/paths"
)

func TestRunShowsHelpWithoutArguments(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run(context.Background(), nil, &stdout, &stderr); code != 0 {
		t.Fatalf("run() code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Local-first, guarded mail, calendar, and tasks") {
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

func TestAccountAddHelpUsesConventionalProtocolFlagNames(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run(
		context.Background(),
		[]string{"account", "add", "--help"},
		&stdout,
		&stderr,
	); code != 0 {
		t.Fatalf("account add help code = %d, stderr = %q", code, stderr.String())
	}
	for _, flag := range []string{
		"--oauth-client-id",
		"--oauth-redirect-uri",
		"--approve-oauth",
		"--task-provider",
		"--microsoft-cloud",
		"--approve-task-oauth",
		"--task-read-only",
		"--imap-tls",
		"--smtp-tls",
		"--caldav-endpoint",
	} {
		if !strings.Contains(stdout.String(), flag) {
			t.Errorf("account add help is missing %q:\n%s", flag, stdout.String())
		}
	}
	for _, legacy := range []string{"--o-auth", "--imaptls", "--smtptls", "--cal-dav"} {
		if strings.Contains(stdout.String(), legacy) {
			t.Errorf("account add help retained malformed flag %q:\n%s", legacy, stdout.String())
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
		"settings",
		"config",
		"doctor",
		"auth",
		"mail",
		"calendar",
		"daemon",
		"integrations",
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
		if !strings.HasPrefix(stdout.String(), "corr dev ") {
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

func TestPendingMessagingCommandsStayHiddenButParseable(t *testing.T) {
	t.Parallel()

	var root bytes.Buffer
	var rootErr bytes.Buffer
	if code := run(context.Background(), nil, &root, &rootErr); code != 0 {
		t.Fatalf("root help code = %d, stderr = %q", code, rootErr.String())
	}
	if strings.Contains(root.String(), "messages") {
		t.Fatalf("pending messaging command was advertised in root help:\n%s", root.String())
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run(context.Background(), []string{"messages", "--help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("message help code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Read and manage provider-neutral messaging") {
		t.Fatalf("message command is not parseable by exact name: %q", stdout.String())
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
		t.Fatalf("corr update did not select the default update action: %q", stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), []string{"update", "check", "--help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("update check help code = %d, stderr=%q", code, stderr.String())
	}
	normalizedHelp := strings.Join(strings.Fields(stdout.String()), " ")
	if !strings.Contains(normalizedHelp, "Use check to report the latest release in the configured channel without installing") {
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
	configuration, err := config.Load(path)
	if err != nil {
		t.Fatalf("load initialized config: %v", err)
	}
	if configuration.DefaultAccount != "" || len(configuration.Accounts) != 0 {
		t.Fatalf("config init selected a provider route: %+v", configuration)
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

func TestRunWithCrashBoundaryPersistsOnlySanitizedEvidence(t *testing.T) {
	stateDirectory := t.TempDir()
	t.Setenv("CORRESYNC_STATE_DIR", stateDirectory)
	privateConfig := filepath.Join(t.TempDir(), "person@example.test", "config.toml")
	secret := "Bearer synthetic-private-token"
	var stderr bytes.Buffer
	code := runWithCrashBoundary(
		context.Background(),
		[]string{"--config", privateConfig, "daemon", "serve"},
		&bytes.Buffer{},
		&stderr,
		buildinfo.Info{
			Version: "v0.9.0-rc.3", Commit: "0123456789abcdef",
			BuildDate: "2026-08-21T12:00:00Z", GoVersion: "go1.26.0",
			OS: "linux", Arch: "amd64",
		},
		func(context.Context, []string, io.Writer, io.Writer) int {
			panic(secret)
		},
	)
	if code != 1 || strings.Contains(stderr.String(), secret) {
		t.Fatalf("panic result = code %d, stderr %q", code, stderr.String())
	}
	path, err := paths.FeedbackCrashPath()
	if err != nil {
		t.Fatal(err)
	}
	record, err := (feedback.CrashStore{Path: path}).Load()
	if err != nil {
		t.Fatalf("load crash record: %v", err)
	}
	if record.ProcessRole != "daemon" || record.Boundary != "process" || len(record.Frames) == 0 {
		t.Fatalf("crash record = %+v", record)
	}
	encoded, err := os.ReadFile(path) // #nosec G304 -- test-owned state path.
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{secret, privateConfig, "person@example.test"} {
		if bytes.Contains(encoded, []byte(forbidden)) {
			t.Fatalf("crash record retained %q: %s", forbidden, encoded)
		}
	}
}

func TestCrashProcessRoleUsesOnlyServeProcesses(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		arguments []string
		want      string
	}{
		"daemon serve":  {[]string{"daemon", "serve"}, "daemon"},
		"daemon status": {[]string{"daemon", "status"}, "cli"},
		"mcp serve":     {[]string{"--config", "/private", "mcp", "serve"}, "mcp"},
		"ordinary CLI":  {[]string{"mail", "list"}, "cli"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := crashProcessRole(test.arguments); got != test.want {
				t.Fatalf("crashProcessRole(%q) = %q, want %q", test.arguments, got, test.want)
			}
		})
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
