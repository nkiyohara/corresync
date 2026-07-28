package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/nkiyohara/owa-bridge/internal/buildinfo"
	"github.com/nkiyohara/owa-bridge/internal/config"
	"github.com/nkiyohara/owa-bridge/internal/daemonapi"
	"github.com/nkiyohara/owa-bridge/internal/localipc"
)

func TestDoctorOfflineProducesContentFreeHealthyReport(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	executable := filepath.Join(root, "chromium")
	// #nosec G306 -- the owner-only test fixture must be executable.
	if err := os.WriteFile(executable, []byte("synthetic executable"), 0o700); err != nil {
		t.Fatal(err)
	}
	configuration := config.Default()
	configuration.Browser.Executable = executable
	configPath := filepath.Join(root, "config.toml")
	if err := config.Save(configPath, configuration); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	app := newRuntime(context.Background(), configPath, &stdout, &bytes.Buffer{}, buildinfo.Current())
	app.endpoint = func(path string) (localipc.Endpoint, error) {
		return localipc.ResolveInState(path, filepath.Join(root, "state"))
	}
	command := doctorCommand{JSON: true}
	if err := command.Run(app); err != nil {
		t.Fatalf("doctor.Run() error = %v", err)
	}

	var report doctorReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("decode doctor output: %v", err)
	}
	if !report.Healthy || report.Online ||
		report.Account != string(configuration.Accounts[configuration.DefaultAccount].ID) {
		t.Fatalf("unexpected doctor report: %+v", report)
	}
	if len(report.Checks) != 7 ||
		report.Checks[2].Name != "update" ||
		report.Checks[5].Name != "daemon" ||
		report.Checks[5].Status != "skip" ||
		report.Checks[6].Status != "skip" {
		t.Fatalf("unexpected doctor checks: %+v", report.Checks)
	}
}

func TestDoctorOfflineRejectsIncompatibleRunningDaemon(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name     string
		protocol int
		version  string
	}{
		{name: "protocol", protocol: daemonapi.ProtocolVersion - 1, version: "0.5.0"},
		{name: "binary", protocol: daemonapi.ProtocolVersion, version: "0.5.0"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			executable := filepath.Join(root, "chromium")
			// #nosec G306 -- the owner-only test fixture must be executable.
			if err := os.WriteFile(executable, []byte("synthetic executable"), 0o700); err != nil {
				t.Fatal(err)
			}
			configuration := config.Default()
			configuration.Browser.Executable = executable
			configPath := filepath.Join(root, "config.toml")
			if err := config.Save(configPath, configuration); err != nil {
				t.Fatal(err)
			}
			configDigest, err := config.Fingerprint(configPath)
			if err != nil {
				t.Fatal(err)
			}
			endpoint, err := localipc.ResolveInState(configPath, filepath.Join(root, "state"))
			if err != nil {
				t.Fatal(err)
			}
			previous := startLifecycleTestDaemon(
				t.Context(),
				t,
				endpoint,
				test.protocol,
				test.version,
				321,
				configDigest,
			)
			t.Cleanup(previous.stop)

			var stdout bytes.Buffer
			app := newRuntime(
				t.Context(),
				configPath,
				&stdout,
				&bytes.Buffer{},
				buildinfo.Current(),
			)
			app.endpoint = func(string) (localipc.Endpoint, error) { return endpoint, nil }
			command := doctorCommand{JSON: true}
			if err := command.Run(app); err == nil {
				t.Fatal("doctor unexpectedly accepted an incompatible daemon")
			}
			var report doctorReport
			if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
				t.Fatalf("decode doctor output: %v", err)
			}
			if report.Healthy {
				t.Fatalf("doctor report is unexpectedly healthy: %+v", report)
			}
			for _, check := range report.Checks {
				if check.Name == "daemon" && check.Status == "fail" {
					return
				}
			}
			t.Fatalf("doctor report lacks a daemon failure: %+v", report.Checks)
		})
	}
}

func TestDoctorReportsInvalidConfigBeforeOnlineWork(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	app := newRuntime(
		context.Background(),
		filepath.Join(t.TempDir(), "missing.toml"),
		&stdout,
		&bytes.Buffer{},
		buildinfo.Current(),
	)
	command := doctorCommand{Online: true, JSON: true}
	if err := command.Run(app); err == nil {
		t.Fatal("doctor.Run() unexpectedly accepted a missing config")
	}
	var report doctorReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("decode doctor output: %v", err)
	}
	if report.Healthy || len(report.Checks) != 1 || report.Checks[0].Name != "config" {
		t.Fatalf("unexpected doctor failure report: %+v", report)
	}
}
