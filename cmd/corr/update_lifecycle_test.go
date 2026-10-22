package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/nkiyohara/corresync/internal/buildinfo"
	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/daemonapi"
	"github.com/nkiyohara/corresync/internal/localipc"
)

func TestUpdateStopsOnlyAuthenticatedConfigOwner(t *testing.T) {
	for _, version := range []string{"0.9.1", "0.9.3"} {
		t.Run(version, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "config.toml")
			if err := config.Save(path, config.Default()); err != nil {
				t.Fatal(err)
			}
			digest, err := config.Fingerprint(path)
			if err != nil {
				t.Fatal(err)
			}
			endpoint, err := localipc.ResolveInState(path, filepath.Join(root, "state"))
			if err != nil {
				t.Fatal(err)
			}
			owner := startLifecycleTestDaemon(t.Context(), t, endpoint, daemonapi.ProtocolVersion, version, 123, digest)
			t.Cleanup(owner.stop)
			app := newRuntime(t.Context(), path, &bytes.Buffer{}, &bytes.Buffer{}, buildinfo.Current())
			app.info.Version = "0.9.2"
			app.endpoint = func(string) (localipc.Endpoint, error) { return endpoint, nil }
			app.previousEndpoints = func(string) ([]localipc.Endpoint, error) { return nil, nil }
			stopped, err := app.stopUpdateOwner(t.Context(), path)
			if version == "0.9.3" {
				if err == nil || stopped || owner.shutdowns.Load() != 0 {
					t.Fatalf("newer owner was modified: %v %v", stopped, err)
				}
			} else if err != nil || !stopped || owner.shutdowns.Load() != 1 {
				t.Fatalf("old owner shutdown = %v %v", stopped, err)
			}
		})
	}
}

func TestExecutableReplacementRetiresOldGeneration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corr")
	if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	generation := captureExecutableGeneration(path)
	if generation.changed() {
		t.Fatal("unchanged executable retired")
	}
	if err := os.Rename(path, path+".backup"); err != nil {
		t.Fatal(err)
	}
	if generation.changed() {
		t.Fatal("transient rename gap retired owner")
	}
	if err := os.WriteFile(path, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	if !generation.changed() {
		t.Fatal("replacement went undetected")
	}
	app := &runtime{executableGeneration: generation}
	if app.requireCurrentExecutable() == nil {
		t.Fatal("old image can restart a daemon")
	}
}

func TestAutomaticUpdateDefaultsPreserveExistingOptOut(t *testing.T) {
	app := newRuntime(context.Background(), filepath.Join(t.TempDir(), "missing.toml"), &bytes.Buffer{}, &bytes.Buffer{}, buildinfo.Current())
	settings := app.automaticUpdateSettings(t.Context(), nil)
	if !settings.autoInstall || !settings.checksEnabled {
		t.Fatal("new installation defaults are not automatic")
	}
	configuration := config.Default()
	configuration.Updates.AutoInstall = false
	if err := config.Save(app.configPath, configuration); err != nil {
		t.Fatal(err)
	}
	if app.automaticUpdateSettings(t.Context(), nil).autoInstall {
		t.Fatal("explicit opt-out was lost")
	}
}
