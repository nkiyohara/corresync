package googleoauthclient

import (
	"bytes"
	"strings"
	"testing"
)

const validDesktopClient = `{
  "installed": {
    "client_id": "synthetic.apps.googleusercontent.com",
    "project_id": "synthetic-project",
    "auth_uri": "https://accounts.google.com/o/oauth2/auth",
    "token_uri": "https://oauth2.googleapis.com/token",
    "auth_provider_x509_cert_url": "https://www.googleapis.com/oauth2/v1/certs",
    "client_secret": "synthetic-client-credential",
    "redirect_uris": ["http://localhost", "http://127.0.0.1"]
  }
}`

func TestParseAcceptsOnlyBoundedGoogleDesktopClient(t *testing.T) {
	t.Parallel()
	client, err := Parse(strings.NewReader(validDesktopClient))
	if err != nil {
		t.Fatal(err)
	}
	if client.ID != "synthetic.apps.googleusercontent.com" ||
		string(client.Secret) != "synthetic-client-credential" {
		t.Fatalf("client = %+v", client)
	}
	secret := client.Secret
	client.Close()
	if client.Secret != nil {
		t.Fatal("Close retained the credential")
	}
	if !bytes.Equal(secret, make([]byte, len(secret))) {
		t.Fatal("Close did not overwrite the owned credential bytes")
	}
}

func TestParseRejectsOtherClientTypesEndpointsAndRedirects(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"web application":        strings.Replace(validDesktopClient, `"installed"`, `"web"`, 1),
		"unknown field":          strings.Replace(validDesktopClient, `"project_id":`, `"unexpected": true, "project_id":`, 1),
		"authorization endpoint": strings.Replace(validDesktopClient, "https://accounts.google.com/o/oauth2/auth", "https://attacker.test/auth", 1),
		"token endpoint":         strings.Replace(validDesktopClient, "https://oauth2.googleapis.com/token", "https://attacker.test/token", 1),
		"remote redirect":        strings.Replace(validDesktopClient, "http://localhost", "https://attacker.test/callback", 1),
		"redirect query":         strings.Replace(validDesktopClient, "http://localhost", "http://localhost?code=leak", 1),
		"missing credential":     strings.Replace(validDesktopClient, "synthetic-client-credential", "", 1),
		"trailing document":      validDesktopClient + `{}`,
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if client, err := Parse(strings.NewReader(input)); err == nil {
				client.Close()
				t.Fatal("unsafe client document was accepted")
			}
		})
	}
}

func TestParseRejectsOversizedDocumentBeforeDecoding(t *testing.T) {
	t.Parallel()
	input := strings.NewReader(strings.Repeat("x", maximumDocumentBytes+1))
	if _, err := Parse(input); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("oversized document error = %v", err)
	}
}
