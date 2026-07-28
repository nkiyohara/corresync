package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type completionCommand struct {
	Action string `arg:"" enum:"bash,zsh,fish,install" help:"Shell to generate, or install to detect and configure the current shell."`
	Shell  string `default:"auto" enum:"auto,bash,zsh,fish" help:"Shell override for the install action."`
	Force  bool   `help:"Replace a different existing file during the install action."`
}

type completionInstallCommand struct {
	Shell string
	Force bool
}

var completionScripts = map[string]string{
	"bash": `# bash completion for corresync
# The installed command resolves itself from PATH for relocatable archives.
complete -o default -o bashdefault -C corresync corresync
`,
	"zsh": `#compdef corresync
# zsh completion for Corresync through its bash-compatible completion protocol.
autoload -U +X bashcompinit && bashcompinit
complete -o default -o bashdefault -C corresync corresync
`,
	"fish": `# fish completion for Corresync
function __corresync_complete
    set -lx COMP_LINE (commandline -cp)
    test -z (commandline -ct); and set COMP_LINE "$COMP_LINE "
    command corresync
end
complete -f -c corresync -a "(__corresync_complete)"
`,
}

func (command *completionCommand) Run(app *runtime) error {
	if command.Action == "install" {
		return (&completionInstallCommand{
			Shell: command.Shell,
			Force: command.Force,
		}).Run(app)
	}
	if command.Shell != "auto" || command.Force {
		return errors.New("--shell and --force apply only to `corresync completion install`")
	}
	script, exists := completionScripts[command.Action]
	if !exists {
		return errors.New("unsupported completion shell")
	}
	_, err := fmt.Fprint(app.stdout, script)
	return err
}

func (command *completionInstallCommand) Run(app *runtime) error {
	shell := command.Shell
	if shell == "auto" {
		var err error
		shell, err = detectCompletionShell(app.lookupEnv)
		if err != nil {
			return err
		}
	}
	destination, err := completionInstallPath(shell, app.lookupEnv)
	if err != nil {
		return err
	}
	script := []byte(completionScripts[shell])
	status, err := installCompletion(destination, script, command.Force)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(app.stdout, "%s %s completion at %s.\n", status, shell, destination); err != nil {
		return err
	}
	if shell == "zsh" && !pathListContains(lookupCompletionEnv(app.lookupEnv, "FPATH"), filepath.Dir(destination)) {
		_, err = fmt.Fprintf(
			app.stdout,
			"Add this before compinit in your zsh startup file: fpath=(%s $fpath)\n",
			shellSingleQuote(filepath.Dir(destination)),
		)
	}
	return err
}

func detectCompletionShell(lookup func(string) (string, bool)) (string, error) {
	value := lookupCompletionEnv(lookup, "SHELL")
	name := strings.TrimSuffix(strings.ToLower(filepath.Base(value)), ".exe")
	if _, supported := completionScripts[name]; supported {
		return name, nil
	}
	if value == "" {
		return "", errors.New("cannot detect a shell because SHELL is unset; pass --shell bash, zsh, or fish")
	}
	return "", fmt.Errorf("unsupported shell %q; pass --shell bash, zsh, or fish", value)
}

func completionInstallPath(
	shell string,
	lookup func(string) (string, bool),
) (string, error) {
	home := lookupCompletionEnv(lookup, "HOME")
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
	}
	if !filepath.IsAbs(home) {
		return "", errors.New("completion home directory must be absolute")
	}
	switch shell {
	case "bash":
		dataHome := lookupCompletionEnv(lookup, "XDG_DATA_HOME")
		if dataHome == "" {
			dataHome = filepath.Join(home, ".local", "share")
		} else if !filepath.IsAbs(dataHome) {
			return "", errors.New("XDG_DATA_HOME must be absolute")
		}
		return filepath.Join(dataHome, "bash-completion", "completions", "corresync"), nil
	case "fish":
		configHome := lookupCompletionEnv(lookup, "XDG_CONFIG_HOME")
		if configHome == "" {
			configHome = filepath.Join(home, ".config")
		} else if !filepath.IsAbs(configHome) {
			return "", errors.New("XDG_CONFIG_HOME must be absolute")
		}
		return filepath.Join(configHome, "fish", "completions", "corresync.fish"), nil
	case "zsh":
		zshHome := lookupCompletionEnv(lookup, "ZDOTDIR")
		if zshHome == "" {
			zshHome = home
		} else if !filepath.IsAbs(zshHome) {
			return "", errors.New("ZDOTDIR must be absolute")
		}
		return filepath.Join(zshHome, ".zfunc", "_corresync"), nil
	default:
		return "", fmt.Errorf("unsupported shell %q", shell)
	}
}

func installCompletion(path string, contents []byte, force bool) (string, error) {
	replace := false
	info, err := os.Lstat(path)
	switch {
	case err == nil:
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("completion target %q is not a regular file", path)
		}
		existing, readErr := os.ReadFile(path) // #nosec G304 -- user-selected completion target.
		if readErr != nil {
			return "", fmt.Errorf("read existing completion: %w", readErr)
		}
		if bytes.Equal(existing, contents) {
			return "Found current", nil
		}
		if !force {
			return "", fmt.Errorf("completion target %q differs; review it or rerun with --force", path)
		}
		replace = true
	case errors.Is(err, os.ErrNotExist):
	default:
		return "", fmt.Errorf("inspect completion target: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return "", fmt.Errorf("create completion directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".corresync-completion-*")
	if err != nil {
		return "", fmt.Errorf("create temporary completion: %w", err)
	}
	temporaryPath := temporary.Name()
	cleanup := func() { _ = os.Remove(temporaryPath) }
	defer cleanup()
	if err := temporary.Chmod(0o644); err != nil {
		_ = temporary.Close()
		return "", fmt.Errorf("set completion permissions: %w", err)
	}
	if _, err := temporary.Write(contents); err != nil {
		_ = temporary.Close()
		return "", fmt.Errorf("write completion: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return "", fmt.Errorf("sync completion: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return "", fmt.Errorf("close completion: %w", err)
	}
	var backupPath string
	if replace {
		backup, backupErr := os.CreateTemp(filepath.Dir(path), ".corresync-completion-backup-*")
		if backupErr != nil {
			return "", fmt.Errorf("reserve completion backup: %w", backupErr)
		}
		backupPath = backup.Name()
		if closeErr := backup.Close(); closeErr != nil {
			return "", fmt.Errorf("close completion backup: %w", closeErr)
		}
		if removeErr := os.Remove(backupPath); removeErr != nil {
			return "", fmt.Errorf("prepare completion backup: %w", removeErr)
		}
		if renameErr := os.Rename(path, backupPath); renameErr != nil {
			return "", fmt.Errorf("back up existing completion: %w", renameErr)
		}
	}
	// Link the complete temporary file into place without replacing a path
	// created after the earlier Lstat. This keeps symlink races fail-closed on
	// both a fresh install and an explicit replacement.
	if err := os.Link(temporaryPath, path); err != nil {
		if backupPath != "" {
			_ = os.Rename(backupPath, path)
		}
		return "", fmt.Errorf("install completion: %w", err)
	}
	if backupPath != "" {
		_ = os.Remove(backupPath)
	}
	return "Installed", nil
}

func lookupCompletionEnv(
	lookup func(string) (string, bool),
	name string,
) string {
	if value, exists := lookup(name); exists {
		return strings.TrimSpace(value)
	}
	return ""
}

func pathListContains(list, want string) bool {
	for _, candidate := range filepath.SplitList(list) {
		if filepath.Clean(candidate) == filepath.Clean(want) {
			return true
		}
	}
	return false
}

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
