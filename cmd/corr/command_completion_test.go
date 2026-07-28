package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/alecthomas/kong"
	kongcompletion "github.com/jotaen/kong-completion"
	"github.com/posener/complete"
)

func TestCompletionScriptsAreRelocatableAndCurrent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		shell string
		file  string
		want  string
	}{
		{shell: "bash", file: "corr.bash", want: "complete -o default"},
		{shell: "zsh", file: "_corr", want: "bashcompinit"},
		{shell: "fish", file: "corr.fish", want: "command corr"},
	}
	for _, test := range tests {
		t.Run(test.shell, func(t *testing.T) {
			t.Parallel()
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			if code := run(context.Background(), []string{"completion", test.shell}, &stdout, &stderr); code != 0 {
				t.Fatalf("run() code = %d, stderr = %q", code, stderr.String())
			}
			if !strings.Contains(stdout.String(), test.want) {
				t.Fatalf("completion output = %q, want %q", stdout.String(), test.want)
			}
			if strings.Contains(stdout.String(), filepath.Clean(os.Args[0])) {
				t.Fatalf("completion output embeds executable path: %q", stdout.String())
			}
			committedPath := filepath.Join("..", "..", "completions", test.file)
			committed, err := os.ReadFile(committedPath) // #nosec G304 -- fixed repository fixture path.
			if err != nil {
				t.Fatalf("read committed completion: %v", err)
			}
			if string(committed) != stdout.String() {
				t.Fatalf("%s is stale; regenerate it with `corr completion %s`", committedPath, test.shell)
			}
		})
	}
}

func TestCompletionInstallDetectsShellAndIsIdempotent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("SHELL", "/usr/bin/fish")

	runInstall := func() string {
		t.Helper()
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		if code := run(t.Context(), []string{"completion", "install"}, &stdout, &stderr); code != 0 {
			t.Fatalf("run() code = %d, stderr = %q", code, stderr.String())
		}
		return stdout.String()
	}

	if output := runInstall(); !strings.Contains(output, "Installed fish completion") {
		t.Fatalf("first install output = %q", output)
	}
	path := filepath.Join(home, "config", "fish", "completions", "corr.fish")
	first, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if output := runInstall(); !strings.Contains(output, "Found current fish completion") {
		t.Fatalf("second install output = %q", output)
	}
	second, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !first.ModTime().Equal(second.ModTime()) || first.Size() != second.Size() {
		t.Fatalf("idempotent install rewrote %s", path)
	}
}

func TestCompletionInstallProtectsDifferentExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "completions", "corr")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("user customization\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := installCompletion(path, []byte("generated\n"), false); err == nil {
		t.Fatal("installCompletion() overwrote a different file without --force")
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "user customization\n" { // #nosec G304 -- test path is below t.TempDir.
		t.Fatalf("protected completion = %q, %v", got, err)
	}
	if _, err := installCompletion(path, []byte("generated\n"), true); err != nil {
		t.Fatalf("forced install error = %v", err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "generated\n" { // #nosec G304 -- test path is below t.TempDir.
		t.Fatalf("forced completion = %q, %v", got, err)
	}
}

func TestCompletionInstallRejectsSymlinkTarget(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("creating symlinks may require elevated Windows privileges")
	}
	root := t.TempDir()
	target := filepath.Join(root, "target")
	link := filepath.Join(root, "completion")
	if err := os.WriteFile(target, []byte("target\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := installCompletion(link, []byte("generated\n"), true); err == nil {
		t.Fatal("installCompletion() followed a symlink target")
	}
}

func TestCompletionInstallRejectsRelativeBaseOverride(t *testing.T) {
	lookup := func(name string) (string, bool) {
		values := map[string]string{
			"HOME":          t.TempDir(),
			"XDG_DATA_HOME": "relative/data",
		}
		value, exists := values[name]
		return value, exists
	}
	if _, err := completionInstallPath("bash", lookup); err == nil {
		t.Fatal("completionInstallPath() accepted relative XDG_DATA_HOME")
	}
}

func TestCompletionModelPredictsNestedCommandsAndEnums(t *testing.T) {
	t.Parallel()

	parser, err := kong.New(&cli{}, kong.Name(commandName))
	if err != nil {
		t.Fatalf("create parser: %v", err)
	}
	command, err := kongcompletion.Command(parser)
	if err != nil {
		t.Fatalf("create completion model: %v", err)
	}

	tests := []struct {
		name string
		args complete.Args
		want string
	}{
		{
			name: "nested command",
			args: complete.Args{
				All:           []string{"mcp", "se"},
				Completed:     []string{"mcp"},
				Last:          "se",
				LastCompleted: "mcp",
			},
			want: "serve",
		},
		{
			name: "enum",
			args: complete.Args{
				All:           []string{"completion", "b"},
				Completed:     []string{"completion"},
				Last:          "b",
				LastCompleted: "completion",
			},
			want: "bash",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			predictions := command.Predict(test.args)
			if !containsString(predictions, test.want) {
				t.Fatalf("predictions = %#v, want %q", predictions, test.want)
			}
		})
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
