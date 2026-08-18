package credential

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/nkiyohara/corresync/internal/config"
)

func TestStoreOSKeyringRequiresExplicitReplacement(t *testing.T) {
	t.Parallel()
	setCalls := 0
	set := func(service, key, value string) error {
		setCalls++
		if service != keyringService || key != "google-client" || value != "synthetic-value" {
			t.Fatalf("keyring set = %q %q %q", service, key, value)
		}
		return nil
	}
	missing := func(service, key string) (string, error) {
		if service != keyringService || key != "google-client" {
			t.Fatalf("keyring get = %q %q", service, key)
		}
		return "", keyring.ErrNotFound
	}
	if err := storeOSKeyring(
		"google-client", []byte("synthetic-value"), false, missing, set,
	); err != nil {
		t.Fatal(err)
	}
	if setCalls != 1 {
		t.Fatalf("create set calls = %d", setCalls)
	}

	existing := func(string, string) (string, error) {
		return "existing-value", nil
	}
	err := storeOSKeyring(
		"google-client", []byte("synthetic-value"), false, existing, set,
	)
	if err == nil || !strings.Contains(err.Error(), "--replace") {
		t.Fatalf("existing handle error = %v", err)
	}
	if setCalls != 1 {
		t.Fatalf("existing handle set calls = %d", setCalls)
	}

	unexpectedGet := func(string, string) (string, error) {
		t.Fatal("explicit replacement inspected the old credential")
		return "", nil
	}
	if err := storeOSKeyring(
		"google-client", []byte("synthetic-value"), true, unexpectedGet, set,
	); err != nil {
		t.Fatal(err)
	}
	if setCalls != 2 {
		t.Fatalf("replacement set calls = %d", setCalls)
	}
}

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
