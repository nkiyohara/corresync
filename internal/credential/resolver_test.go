package credential

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nkiyohara/corresync/internal/config"
)

func TestResolverReadsExternalBackendsAndZerosOwnedBytes(t *testing.T) {
	t.Parallel()
	var helperInput string
	resolver, err := New(Options{
		Helper: []string{"synthetic-helper", "get"},
		Keyring: func(service, key string) (string, error) {
			if service != keyringService || key != "mail" {
				t.Fatalf("keyring lookup = %q %q", service, key)
			}
			return "keyring-value", nil
		},
		Run: func(_ context.Context, arguments []string, input []byte) ([]byte, error) {
			if len(arguments) != 2 || arguments[0] != "synthetic-helper" {
				t.Fatalf("helper arguments = %#v", arguments)
			}
			helperInput = string(input)
			return []byte("helper-value\n"), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		reference config.CredentialRef
		want      string
	}{
		{
			config.CredentialRef{
				Backend: config.CredentialOSKeyring, Key: "mail", Consent: true,
			},
			"keyring-value",
		},
		{
			config.CredentialRef{
				Backend: config.CredentialHelper, Key: "calendar", Consent: true,
			},
			"helper-value",
		},
	} {
		secret, resolveErr := resolver.Resolve(context.Background(), test.reference)
		if resolveErr != nil || secret.String() != test.want {
			t.Fatalf("Resolve() = %q, %v", secret.String(), resolveErr)
		}
		copied := secret.CopyBytes()
		if string(copied) != test.want {
			t.Fatalf("CopyBytes() = %q", copied)
		}
		owned := secret.value
		if err := secret.Close(); err != nil {
			t.Fatal(err)
		}
		if string(copied) != test.want {
			t.Fatal("Close() modified the caller-owned copy")
		}
		clear(copied)
		for _, value := range owned {
			if value != 0 {
				t.Fatal("Close() did not zero owned bytes")
			}
		}
	}
	if !strings.Contains(helperInput, `"operation":"get"`) ||
		!strings.Contains(helperInput, `"key":"calendar"`) {
		t.Fatalf("helper input = %q", helperInput)
	}
}

func TestResolverFailsClosed(t *testing.T) {
	t.Parallel()
	resolver, err := New(Options{
		Keyring: func(string, string) (string, error) {
			return "", errors.New("synthetic keyring failure")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, reference := range []config.CredentialRef{
		{Backend: config.CredentialOSKeyring, Key: "mail"},
		{Backend: config.CredentialOSKeyring, Key: "mail", Consent: true},
		{Backend: config.CredentialHelper, Key: "mail", Consent: true},
	} {
		if _, err := resolver.Resolve(context.Background(), reference); err == nil {
			t.Fatalf("Resolve(%+v) unexpectedly succeeded", reference)
		}
	}
}

func TestHelperEnvironmentDropsUnrelatedVariables(t *testing.T) {
	t.Parallel()
	filtered := helperEnvironment([]string{
		"PATH=/bin", "HOME=/home/example", "MAIL_PASSWORD=do-not-forward",
	})
	joined := strings.Join(filtered, "\n")
	if !strings.Contains(joined, "PATH=/bin") ||
		!strings.Contains(joined, "HOME=/home/example") ||
		strings.Contains(joined, "MAIL_PASSWORD") {
		t.Fatalf("filtered environment = %#v", filtered)
	}
}
