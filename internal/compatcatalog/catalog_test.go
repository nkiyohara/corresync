package compatcatalog

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDefaultCatalogIsValid(t *testing.T) {
	t.Parallel()

	catalog := defaultCatalog(t)
	if len(catalog.Entries) < 40 {
		t.Fatalf("catalog has %d entries", len(catalog.Entries))
	}
	for _, service := range Services() {
		found := false
		for _, entry := range catalog.Entries {
			found = found || entry.Service == service
		}
		if !found {
			t.Errorf("no entry for service %q", service)
		}
	}
}

func TestEveryClosedValueHasADescription(t *testing.T) {
	t.Parallel()

	for _, value := range Availabilities() {
		if value.Description() == "" {
			t.Errorf("availability %q has no description", value)
		}
	}
	for _, value := range Authentications() {
		if value.Description() == "" {
			t.Errorf("authentication %q has no description", value)
		}
	}
	for _, value := range LiveStates() {
		if value.Description() == "" {
			t.Errorf("live state %q has no description", value)
		}
	}
}

func validEntry() map[string]any {
	return map[string]any{
		"provider":       "imap-smtp",
		"providerName":   "IMAP and SMTP",
		"route":          "imap_smtp",
		"service":        "mail",
		"availability":   "available-with-setup",
		"authentication": "external-credential",
		"deterministicEvidence": map[string]any{
			"summary": "Synthetic contracts",
			"tests":   []string{"internal/provider/imapmail"},
		},
		"liveEvidence": map[string]any{"state": "not-run", "summary": "Not run"},
	}
}

func encode(t *testing.T, entries ...map[string]any) []byte {
	t.Helper()
	data, err := json.Marshal(map[string]any{"schemaVersion": SchemaVersion, "entries": entries})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestDecodeAcceptsMinimalEntry(t *testing.T) {
	t.Parallel()

	if _, err := Decode(encode(t, validEntry())); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeRejectsInvalidCatalogs(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		mutate func(map[string]any)
		want   string
	}{
		{"unknown field", func(entry map[string]any) { entry["token"] = "x" }, "unknown field"},
		{"unknown availability", func(entry map[string]any) { entry["availability"] = "beta" }, "unknown availability"},
		{"unknown service", func(entry map[string]any) { entry["service"] = "chat" }, "unknown service"},
		{"unknown authentication", func(entry map[string]any) { entry["authentication"] = "password" }, "unknown authentication"},
		{"unknown live state", func(entry map[string]any) {
			entry["liveEvidence"] = map[string]any{"state": "passed", "summary": "x"}
		}, "unknown live evidence state"},
		{"observed without record", func(entry map[string]any) {
			entry["liveEvidence"] = map[string]any{"state": "live-observed", "summary": "x"}
		}, "docs/evidence"},
		{"record outside evidence", func(entry map[string]any) {
			entry["liveEvidence"] = map[string]any{"state": "live-observed", "summary": "x", "record": "docs/../x.md"}
		}, "docs/evidence"},
		{"record on unobserved entry", func(entry map[string]any) {
			entry["liveEvidence"] = map[string]any{"state": "not-run", "summary": "x", "record": "docs/evidence/a.md"}
		}, "only a live-observed"},
		{"endpoint in text", func(entry map[string]any) { entry["notes"] = "see https://example.invalid" }, "://"},
		{"address in text", func(entry map[string]any) { entry["notes"] = "user@example.invalid" }, "@"},
		{"table pipe in text", func(entry map[string]any) { entry["providerName"] = "a | b" }, "|"},
		{"html in text", func(entry map[string]any) { entry["notes"] = "<b>" }, "<"},
		{"reachable without tests", func(entry map[string]any) {
			entry["deterministicEvidence"] = map[string]any{"summary": "x"}
		}, "at least one"},
		{"test path traversal", func(entry map[string]any) {
			entry["deterministicEvidence"] = map[string]any{"summary": "x", "tests": []string{"internal/../etc"}}
		}, "invalid test reference"},
		{"non-test file", func(entry map[string]any) {
			entry["deterministicEvidence"] = map[string]any{"summary": "x", "tests": []string{"internal/x/client.go"}}
		}, "neither a test file"},
		{"planned without issue", func(entry map[string]any) {
			entry["availability"] = "planned"
			entry["deterministicEvidence"] = map[string]any{"summary": "x"}
		}, "tracking issue"},
		{"boundary with provider", func(entry map[string]any) { entry["service"] = "boundary" }, "boundary"},
		{"task without contract", func(entry map[string]any) { entry["service"] = "tasks" }, "task contract"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			entry := validEntry()
			test.mutate(entry)
			_, err := Decode(encode(t, entry))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Decode() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestDecodeRejectsDuplicatesVersionsAndTrailingData(t *testing.T) {
	t.Parallel()

	if _, err := Decode(encode(t, validEntry(), validEntry())); err == nil ||
		!strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate error = %v", err)
	}
	if _, err := Decode(append(encode(t, validEntry()), []byte(" {}")...)); err == nil ||
		!strings.Contains(err.Error(), "trailing") {
		t.Fatalf("trailing error = %v", err)
	}
	if _, err := Decode([]byte(`{"schemaVersion":2,"entries":[]}`)); err == nil ||
		!strings.Contains(err.Error(), "schema version") {
		t.Fatalf("version error = %v", err)
	}
}
