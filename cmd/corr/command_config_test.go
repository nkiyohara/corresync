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
)

func TestConfigGetAndSetTypedValues(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.toml")
	if err := config.Save(path, config.OutlookDefault()); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	app := newRuntime(t.Context(), path, &stdout, &bytes.Buffer{}, buildinfo.Current())

	set := configSetCommand{Key: "policy.max_recipients", Value: "42", JSON: true}
	if err := set.Run(app); err != nil {
		t.Fatalf("config set error = %v", err)
	}
	var updated struct {
		Key     string `json:"key"`
		Value   int    `json:"value"`
		Updated bool   `json:"updated"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &updated); err != nil {
		t.Fatalf("decode set output: %v", err)
	}
	if updated.Key != set.Key || updated.Value != 42 || !updated.Updated {
		t.Fatalf("unexpected set output: %+v", updated)
	}

	stdout.Reset()
	get := configGetCommand{Key: set.Key, JSON: true}
	if err := get.Run(app); err != nil {
		t.Fatalf("config get error = %v", err)
	}
	var read struct {
		Value int `json:"value"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &read); err != nil || read.Value != 42 {
		t.Fatalf("config get output = %s, %v", stdout.String(), err)
	}
	loaded, err := config.Load(path)
	if err != nil || loaded.Policy.MaxRecipients != 42 {
		t.Fatalf("saved config = %+v, %v", loaded, err)
	}
}

func TestConfigGetAndSetAutomaticInstall(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.toml")
	if err := config.Save(path, config.OutlookDefault()); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	app := newRuntime(t.Context(), path, &stdout, &bytes.Buffer{}, buildinfo.Current())
	if err := (&configSetCommand{
		Key: "updates.auto_install", Value: "true", JSON: true,
	}).Run(app); err != nil {
		t.Fatalf("config set auto-install: %v", err)
	}
	loaded, err := config.Load(path)
	if err != nil || !loaded.Updates.AutoInstall {
		t.Fatalf("saved auto-install config = %+v, %v", loaded.Updates, err)
	}
	value, err := getConfigValue(loaded, "updates.auto_install")
	if err != nil || value != true {
		t.Fatalf("get updates.auto_install = %v, %v", value, err)
	}
}

func TestConfigGetAndSetUpdateChannel(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.toml")
	if err := config.Save(path, config.OutlookDefault()); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	app := newRuntime(t.Context(), path, &stdout, &bytes.Buffer{}, buildinfo.Current())
	if err := (&configSetCommand{
		Key: "updates.channel", Value: "preview", JSON: true,
	}).Run(app); err != nil {
		t.Fatalf("config set update channel: %v", err)
	}
	loaded, err := config.Load(path)
	if err != nil || loaded.Updates.Channel != config.UpdateChannelPreview {
		t.Fatalf("saved update channel = %+v, %v", loaded.Updates, err)
	}
	value, err := getConfigValue(loaded, "updates.channel")
	if err != nil || value != config.UpdateChannelPreview {
		t.Fatalf("get updates.channel = %v, %v", value, err)
	}
	before, err := config.Fingerprint(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := (&configSetCommand{
		Key: "updates.channel", Value: "nightly", JSON: true,
	}).Run(app); err == nil {
		t.Fatal("unsupported update channel was accepted")
	}
	after, err := config.Fingerprint(path)
	if err != nil || after != before {
		t.Fatalf("invalid channel modified config: before=%s after=%s err=%v", before, after, err)
	}
}

func TestConfigSetSupportsDottedAccountAliases(t *testing.T) {
	t.Parallel()

	configuration := config.OutlookDefault()
	if err := setConfigValue(
		&configuration,
		"accounts.shared.finance.origin",
		"https://outlook.cloud.microsoft",
	); err != nil {
		t.Fatalf("setConfigValue() error = %v", err)
	}
	value, err := getConfigValue(configuration, "accounts.shared.finance.origin")
	if err != nil || value != "https://outlook.cloud.microsoft" {
		t.Fatalf("getConfigValue() = %v, %v", value, err)
	}
}

func TestConfigSetRejectsInvalidValueWithoutWriting(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.toml")
	if err := config.Save(path, config.OutlookDefault()); err != nil {
		t.Fatal(err)
	}
	before, err := config.Fingerprint(path)
	if err != nil {
		t.Fatal(err)
	}
	app := newRuntime(t.Context(), path, &bytes.Buffer{}, &bytes.Buffer{}, buildinfo.Current())
	command := configSetCommand{Key: "policy.max_recipients", Value: "0"}
	if err := command.Run(app); err == nil {
		t.Fatal("config set unexpectedly accepted an unsafe value")
	}
	after, err := config.Fingerprint(path)
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("invalid config set changed the file")
	}
}

func TestConfigEditValidatesBeforePreservingEditedTOML(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.toml")
	if err := config.Save(path, config.OutlookDefault()); err != nil {
		t.Fatal(err)
	}
	app := newRuntime(t.Context(), path, &bytes.Buffer{}, &bytes.Buffer{}, buildinfo.Current())
	app.runCommand = func(
		_ context.Context,
		_, _ io.Writer,
		name string,
		arguments ...string,
	) error {
		if name != "synthetic-editor" || len(arguments) != 2 || arguments[0] != "--wait" {
			t.Fatalf("editor command = %q %q", name, arguments)
		}
		editPath := arguments[1]
		contents, err := os.ReadFile(editPath) // #nosec G304 -- private test path.
		if err != nil {
			return err
		}
		return os.WriteFile( // #nosec G703 -- private test path supplied by the command under test.
			editPath,
			append([]byte("# preserved comment\n"), contents...),
			0o600,
		)
	}
	command := configEditCommand{Editor: "synthetic-editor --wait"}
	if err := command.Run(app); err != nil {
		t.Fatalf("config edit error = %v", err)
	}
	edited, err := os.ReadFile(path) // #nosec G304 -- private test path.
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(edited), "# preserved comment\n") {
		t.Fatalf("edited config did not preserve TOML: %s", edited)
	}
}

func TestConfigEditRejectsInvalidTOMLWithoutChangingOriginal(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.toml")
	if err := config.Save(path, config.OutlookDefault()); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path) // #nosec G304 -- private test path.
	if err != nil {
		t.Fatal(err)
	}
	app := newRuntime(t.Context(), path, &bytes.Buffer{}, &bytes.Buffer{}, buildinfo.Current())
	app.runCommand = func(
		_ context.Context,
		_ io.Writer,
		_ io.Writer,
		_ string,
		arguments ...string,
	) error {
		return os.WriteFile(arguments[len(arguments)-1], []byte("invalid = ["), 0o600)
	}
	command := configEditCommand{Editor: "synthetic-editor"}
	if err := command.Run(app); err == nil {
		t.Fatal("config edit unexpectedly accepted invalid TOML")
	}
	after, err := os.ReadFile(path) // #nosec G304 -- private test path.
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("invalid config edit changed the original")
	}
}
