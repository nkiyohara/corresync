package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	runtimepkg "runtime"
	"strings"
	"testing"

	"github.com/nkiyohara/corresync/internal/buildinfo"
	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/integrationlifecycle"
)

func integrationCommandRuntime(t *testing.T) (*runtime, string, string, *bytes.Buffer) {
	t.Helper()
	home := t.TempDir()
	configPath := filepath.Join(home, ".config", "corresync", "config.toml")
	if err := config.Save(configPath, config.OutlookDefault()); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(home, "bin", "corr")
	if err := os.MkdirAll(filepath.Dir(executable), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte("fixture"), 0o700); err != nil { // #nosec G306 -- fixture must be executable.
		t.Fatal(err)
	}
	executable, err := filepath.EvalSymlinks(executable)
	if err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	app := newRuntime(t.Context(), configPath, &stdout, &bytes.Buffer{}, buildinfo.Current())
	app.userHomeDir = func() (string, error) { return home, nil }
	app.userConfigDir = func() (string, error) { return filepath.Join(home, ".config"), nil }
	app.workingDirectory = func() (string, error) { return home, nil }
	app.integrationBundleDirectory = func(string) (string, error) { return "", nil }
	app.interactiveInput = func() bool { return false }
	app.interactiveOutput = func() bool { return false }
	app.interactiveStdout = func() bool { return false }
	app.runIntegrationCommand = func(ctx context.Context, stdout, stderr io.Writer, name string, arguments ...string) error {
		return app.runCommand(ctx, stdout, stderr, name, arguments...)
	}
	app.runIntegrationDirectoryCommand = func(
		ctx context.Context,
		stdout, stderr io.Writer,
		_, name string,
		arguments ...string,
	) error {
		return app.runCommand(ctx, stdout, stderr, name, arguments...)
	}
	return app, executable, configPath, &stdout
}

func TestIntegrationsSetupAppliesAndVerifiesIndependentHosts(t *testing.T) {
	t.Parallel()
	app, executable, configPath, stdout := integrationCommandRuntime(t)
	codexRegistered := false
	var mutations []string
	app.runCommand = func(_ context.Context, output, _ io.Writer, name string, arguments ...string) error {
		joined := name + " " + strings.Join(arguments, " ")
		if name != "codex" {
			return errors.New("unexpected host command")
		}
		if len(arguments) >= 2 && arguments[0] == "mcp" && arguments[1] == "get" {
			if !codexRegistered {
				return errors.New("not found")
			}
			_, _ = io.WriteString(output, "corresync\n command: "+executable+"\n args: --config "+configPath+" mcp serve\n")
			return nil
		}
		if len(arguments) >= 2 && arguments[0] == "mcp" && arguments[1] == "add" {
			mutations = append(mutations, joined)
			codexRegistered = true
			return nil
		}
		return errors.New("unexpected command")
	}
	command := integrationsSetupCommand{integrationMutationFlags: integrationMutationFlags{
		integrationTargetFlags: integrationTargetFlags{
			Hosts: []string{"codex", "cursor"}, Name: "corresync", Executable: executable, Scope: "user",
		},
		Yes: true,
	}}
	if err := command.Run(app); err != nil {
		t.Fatal(err)
	}
	if len(mutations) != 1 || !strings.Contains(stdout.String(), "applied_reload_required") {
		t.Fatalf("mutations = %#v, stdout = %s", mutations, stdout.String())
	}
	cursorPath := filepath.Join(filepath.Dir(filepath.Dir(configPath)), "..", ".cursor", "mcp.json")
	cursorPath = filepath.Clean(cursorPath)
	data, err := os.ReadFile(cursorPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(executable)) {
		t.Fatalf("Cursor config = %s", data)
	}
}

func TestIntegrationsJSONIsPreviewOnly(t *testing.T) {
	t.Parallel()
	app, executable, _, stdout := integrationCommandRuntime(t)
	mutated := false
	app.runCommand = func(_ context.Context, _ io.Writer, _ io.Writer, _ string, arguments ...string) error {
		if len(arguments) > 1 && arguments[1] == "add" {
			mutated = true
		}
		return errors.New("not found")
	}
	command := integrationsSetupCommand{integrationMutationFlags: integrationMutationFlags{
		integrationTargetFlags: integrationTargetFlags{Hosts: []string{"codex"}, Name: "corresync", Executable: executable, Scope: "user"},
		JSON:                   true,
	}}
	if err := command.Run(app); err != nil {
		t.Fatal(err)
	}
	if mutated {
		t.Fatal("JSON preview executed a mutation")
	}
	var report integrationApplyReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.SchemaVersion != integrationlifecycle.SchemaVersion || len(report.Plans) != 1 || len(report.Results) != 0 {
		t.Fatalf("report = %+v", report)
	}
}

func TestIntegrationsMutationRequiresConfirmation(t *testing.T) {
	t.Parallel()
	app, executable, _, _ := integrationCommandRuntime(t)
	app.runCommand = func(context.Context, io.Writer, io.Writer, string, ...string) error {
		return errors.New("not found")
	}
	command := integrationsSetupCommand{integrationMutationFlags: integrationMutationFlags{
		integrationTargetFlags: integrationTargetFlags{Hosts: []string{"codex"}, Name: "corresync", Executable: executable, Scope: "user"},
	}}
	err := command.Run(app)
	if err == nil || !strings.Contains(err.Error(), "interactive confirmation") {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestIntegrationsYesReturnsErrorForBlockedHost(t *testing.T) {
	t.Parallel()
	app, executable, _, stdout := integrationCommandRuntime(t)
	app.runCommand = func(_ context.Context, output, _ io.Writer, _ string, _ ...string) error {
		_, _ = io.WriteString(output, "corresync\n command: /usr/bin/other-server\n")
		return nil
	}
	command := integrationsSetupCommand{integrationMutationFlags: integrationMutationFlags{
		integrationTargetFlags: integrationTargetFlags{
			Hosts: []string{"codex"}, Name: "corresync", Executable: executable, Scope: "user",
		},
		Yes: true,
	}}
	err := command.Run(app)
	if err == nil || !strings.Contains(err.Error(), "not Corresync-owned") ||
		!strings.Contains(stdout.String(), "blocked_before_change") {
		t.Fatalf("Run() error = %v, stdout = %s", err, stdout.String())
	}
}

func TestIntegrationsDoctorReportsUnavailableAdaptersWithoutHostExecution(t *testing.T) {
	t.Parallel()
	app, executable, _, stdout := integrationCommandRuntime(t)
	app.runCommand = func(context.Context, io.Writer, io.Writer, string, ...string) error {
		t.Fatal("doctor executed a catalog-only host")
		return nil
	}
	command := integrationsDoctorCommand{
		integrationTargetFlags: integrationTargetFlags{Hosts: []string{"roo-code"}, Name: "corresync", Executable: executable, Scope: "user"},
		JSON:                   true,
	}
	if err := command.Run(app); err != nil {
		t.Fatal(err)
	}
	var report integrationDoctorReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Hosts) != 1 || report.Hosts[0].Inspection.State != integrationlifecycle.StateUnavailable {
		t.Fatalf("report = %+v", report)
	}
}

func TestIntegrationsSetupContinuesAfterHostFailureAndRerunResumes(t *testing.T) {
	t.Parallel()
	app, executable, configPath, stdout := integrationCommandRuntime(t)
	failCodex := true
	codexRegistered := false
	app.runCommand = func(_ context.Context, output, _ io.Writer, name string, arguments ...string) error {
		if name != "codex" || len(arguments) < 2 || arguments[0] != "mcp" {
			return errors.New("unexpected host command")
		}
		switch arguments[1] {
		case "get":
			if !codexRegistered {
				return errors.New("not found")
			}
			_, _ = io.WriteString(output, "corresync\n command: "+executable+"\n args: --config "+configPath+" mcp serve\n")
			return nil
		case "add":
			if failCodex {
				return errors.New("synthetic add failure")
			}
			codexRegistered = true
			return nil
		default:
			return errors.New("unexpected codex operation")
		}
	}
	command := integrationsSetupCommand{integrationMutationFlags: integrationMutationFlags{
		integrationTargetFlags: integrationTargetFlags{
			Hosts: []string{"codex", "cursor"}, Name: "corresync", Executable: executable, Scope: "user",
		},
		Yes: true,
	}}
	err := command.Run(app)
	if err == nil || !strings.Contains(err.Error(), "codex: host command reported failure") {
		t.Fatalf("first Run() error = %v", err)
	}
	cursorPath := filepath.Join(filepath.Dir(filepath.Dir(configPath)), "..", ".cursor", "mcp.json")
	cursorPath = filepath.Clean(cursorPath)
	if data, readErr := os.ReadFile(cursorPath); readErr != nil || !bytes.Contains(data, []byte(executable)) {
		t.Fatalf("later Cursor host was not applied: %s, error %v", data, readErr)
	}
	if !strings.Contains(stdout.String(), "failed_previous_state_preserved") ||
		!strings.Contains(stdout.String(), "applied_reload_required") {
		t.Fatalf("first-run results = %s", stdout.String())
	}

	failCodex = false
	stdout.Reset()
	if err := command.Run(app); err != nil {
		t.Fatalf("resume Run() error = %v", err)
	}
	if !codexRegistered || !strings.Contains(stdout.String(), "already_current") {
		t.Fatalf("resume did not preserve Cursor and finish Codex: %s", stdout.String())
	}
}

func TestSkippedIntegrationPlansHaveExplicitPerHostResults(t *testing.T) {
	t.Parallel()
	plans := []integrationlifecycle.Plan{
		{Host: "codex", Actions: []integrationlifecycle.Action{{Kind: integrationlifecycle.ActionCommand}}},
		{Host: "cursor", Blocked: true, Reason: "conflict"},
	}
	results := resultsForSkippedPlans(plans)
	if len(results) != 2 || results[0].Status != integrationlifecycle.ResultSkipped ||
		results[1].Status != integrationlifecycle.ResultBlocked {
		t.Fatalf("results = %+v", results)
	}
}

func TestVerifyIntegrationExecutableResolvesSymlinkAndRejectsUnsafeMode(t *testing.T) {
	t.Parallel()
	if runtimepkg.GOOS == "windows" {
		t.Skip("Unix executable mode and symlink semantics")
	}
	directory := t.TempDir()
	realPath := filepath.Join(directory, "corr-real")
	if err := os.WriteFile(realPath, []byte("fixture"), 0o700); err != nil { // #nosec G306 -- fixture must be executable.
		t.Fatal(err)
	}
	linkPath := filepath.Join(directory, "corr")
	if err := os.Symlink(realPath, linkPath); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	resolved, err := verifyIntegrationExecutable(linkPath)
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(realPath)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != want {
		t.Fatalf("resolved executable = %q, want %q", resolved, want)
	}
	if err := os.Chmod(realPath, 0o722); err != nil { // #nosec G302 -- intentionally unsafe fixture.
		t.Fatal(err)
	}
	if _, err := verifyIntegrationExecutable(realPath); err == nil || !strings.Contains(err.Error(), "unsafe") {
		t.Fatalf("unsafe executable error = %v", err)
	}
}

func TestVerifyIntegrationExecutableRejectsUnsafeParent(t *testing.T) {
	t.Parallel()
	if runtimepkg.GOOS == "windows" {
		t.Skip("Unix directory mode semantics")
	}
	directory := filepath.Join(t.TempDir(), "unsafe-bin")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o777); err != nil { // #nosec G302 -- intentionally unsafe fixture.
		t.Fatal(err)
	}
	executable := filepath.Join(directory, "corr")
	if err := os.WriteFile(executable, []byte("fixture"), 0o700); err != nil { // #nosec G306 -- fixture must be executable.
		t.Fatal(err)
	}
	if _, err := verifyIntegrationExecutable(executable); err == nil || !strings.Contains(err.Error(), "parent is writable") {
		t.Fatalf("unsafe parent error = %v", err)
	}
}

func TestFindIntegrationBundleDirectoryBesideInstallationPrefix(t *testing.T) {
	t.Parallel()
	prefix := t.TempDir()
	executable := filepath.Join(prefix, "bin", "corr")
	if err := os.MkdirAll(filepath.Dir(executable), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte("fixture"), 0o700); err != nil { // #nosec G306 -- fixture must be executable.
		t.Fatal(err)
	}
	bundle := filepath.Join(prefix, "share", "corresync")
	for _, relative := range []string{
		"plugins/corresync/skills/corresync/SKILL.md",
		"plugins/corresync/.codex-plugin/plugin.json",
		"integrations/gemini-cli/corresync/gemini-extension.json",
	} {
		path := filepath.Join(bundle, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	resolved, err := findIntegrationBundleDirectory(executable)
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != want {
		t.Fatalf("bundle directory = %q, want %q", resolved, want)
	}
}
