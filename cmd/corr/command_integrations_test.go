package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/nkiyohara/corresync/internal/agenthost"
	"github.com/nkiyohara/corresync/internal/buildinfo"
)

func TestIntegrationsCatalogCommandsRenderFromTypedCatalog(t *testing.T) {
	t.Parallel()

	for _, fixture := range []struct {
		name      string
		arguments []string
		contains  []string
	}{
		{
			name: "list", arguments: []string{"integrations", "list"},
			contains: []string{"Agent host catalog", "codex", "catalog_only", "Run `corr integrations show HOST`"},
		},
		{
			name: "show compatibility alias", arguments: []string{"integrations", "show", "claude"},
			contains: []string{"Claude Code", "claude-code", "local MCP + Skill + plugin", "https://code.claude.com/"},
		},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			t.Parallel()
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			if code := run(t.Context(), fixture.arguments, &stdout, &stderr); code != 0 {
				t.Fatalf("run() code = %d, stderr = %q", code, stderr.String())
			}
			for _, want := range fixture.contains {
				if !strings.Contains(stdout.String(), want) {
					t.Errorf("stdout does not contain %q: %s", want, stdout.String())
				}
			}
		})
	}
}

func TestIntegrationsCatalogJSONIsStableAndSecretFree(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run(t.Context(), []string{"integrations", "list", "--json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("run() code = %d, stderr = %q", code, stderr.String())
	}
	var report integrationCatalogReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.SchemaVersion != 1 || len(report.Hosts) < 20 {
		t.Fatalf("catalog JSON = %+v", report)
	}
	encoded := stdout.String()
	for _, forbidden := range []string{"PATH", "token", "credential", "conversation", "environment"} {
		if strings.Contains(encoded, forbidden) {
			t.Errorf("catalog JSON contains forbidden value %q", forbidden)
		}
	}
}

func TestIntegrationsDetectPreservesTypedPartialReport(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app := newRuntime(t.Context(), "", &stdout, &stderr, buildinfo.Current())
	codex, ok := app.agentHosts.Lookup("codex")
	if !ok {
		t.Fatal("built-in catalog is missing Codex")
	}
	app.detectAgentHosts = func(context.Context, agenthost.Request) (agenthost.Report, error) {
		return agenthost.Report{
			SchemaVersion: 1,
			Context:       agenthost.RuntimeContext{Kind: "local", OS: "linux", Arch: "amd64"},
			Cache:         agenthost.CacheFresh,
			Hosts: []agenthost.Detection{{
				Host: codex, Status: agenthost.StatusConfirmed,
				ConnectionStatus: agenthost.ConnectionNotInspected,
				Evidence:         []agenthost.Evidence{{Kind: agenthost.EvidenceExecutable, Source: "path", Location: "/tools/codex"}},
			}},
			Failure: &agenthost.Failure{Code: "timeout"},
		}, agenthost.ErrDetectionTimeout
	}
	command := integrationsDetectCommand{JSON: true}
	err := command.Run(app)
	if !errors.Is(err, agenthost.ErrDetectionTimeout) {
		t.Fatalf("Run() error = %v", err)
	}
	var report agenthost.Report
	if decodeErr := json.Unmarshal(stdout.Bytes(), &report); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if report.Failure == nil || report.Failure.Code != "timeout" || len(report.Hosts) != 1 {
		t.Fatalf("partial report = %+v", report)
	}
}

func TestIntegrationsShowRejectsUnknownHost(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app := newRuntime(t.Context(), "", &stdout, &stderr, buildinfo.Current())
	err := (&integrationsShowCommand{Host: "unknown"}).Run(app)
	if err == nil || !strings.Contains(err.Error(), "corr integrations list") {
		t.Fatalf("Run() error = %v", err)
	}
}
