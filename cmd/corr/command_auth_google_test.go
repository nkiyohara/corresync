package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nkiyohara/corresync/internal/buildinfo"
)

func TestGoogleClientImportStoresOnlyCredentialAndPrintsNoSecret(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "client.json")
	contents := `{"installed":{"client_id":"synthetic.apps.googleusercontent.com","project_id":"synthetic","auth_uri":"https://accounts.google.com/o/oauth2/auth","token_uri":"https://oauth2.googleapis.com/token","auth_provider_x509_cert_url":"https://www.googleapis.com/oauth2/v1/certs","client_secret":"never-print-this","redirect_uris":["http://localhost"]}}`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stored bytes.Buffer
	app := newRuntime(t.Context(), "", &stdout, &bytes.Buffer{}, buildinfo.Info{})
	command := googleClientImportCommand{
		File: path,
		Key:  "personal-google-client",
		store: func(key string, value []byte) error {
			if key != "personal-google-client" {
				t.Fatalf("credential key = %q", key)
			}
			stored.Write(value)
			return nil
		},
	}
	if err := command.Run(app); err != nil {
		t.Fatal(err)
	}
	if stored.String() != "never-print-this" {
		t.Fatalf("stored credential = %q", stored.String())
	}
	if strings.Contains(stdout.String(), "never-print-this") ||
		!strings.Contains(stdout.String(), "personal-google-client") ||
		!strings.Contains(stdout.String(), "synthetic.apps.googleusercontent.com") {
		t.Fatalf("import output = %q", stdout.String())
	}
}

func TestGoogleClientImportReplacesOnlyWhenExplicitlyRequested(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "client.json")
	contents := `{"installed":{"client_id":"synthetic.apps.googleusercontent.com","project_id":"synthetic","auth_uri":"https://accounts.google.com/o/oauth2/auth","token_uri":"https://oauth2.googleapis.com/token","auth_provider_x509_cert_url":"https://www.googleapis.com/oauth2/v1/certs","client_secret":"replace-only-value","redirect_uris":["http://localhost"]}}`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	app := newRuntime(t.Context(), "", &bytes.Buffer{}, &bytes.Buffer{}, buildinfo.Info{})
	command := googleClientImportCommand{
		File: path, Key: "personal-google-client", Replace: true,
		store: func(string, []byte) error {
			t.Fatal("create-only store used for explicit replacement")
			return nil
		},
		replaceStore: func(key string, value []byte) error {
			if key != "personal-google-client" || string(value) != "replace-only-value" {
				t.Fatalf("replacement = %q %q", key, value)
			}
			return nil
		},
	}
	if err := command.Run(app); err != nil {
		t.Fatal(err)
	}
}
