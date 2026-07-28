package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/credential"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/provider/jmap"
)

func TestSessionBackendOnlyResolvesJMAPCredentialForExplicitCLILogin(t *testing.T) {
	t.Parallel()

	const accountID domain.AccountID = "acc_00000000000000000000000000000001"
	keyringReads := 0
	resolver, err := credential.New(credential.Options{
		Keyring: func(service, key string) (string, error) {
			keyringReads++
			if service != "corresync" || key != "jmap-work" {
				t.Fatalf("keyring request = %q, %q", service, key)
			}
			return "synthetic-secret", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	configuration := config.Default()
	configuration.Accounts["work"] = config.Account{
		ID: accountID,
		Mail: &config.MailRoute{
			Provider: domain.ProviderJMAP,
			JMAP: &config.JMAPRoute{
				SessionURL: "https://jmap.example.invalid/session",
				Username:   "reader@example.invalid",
				Credential: config.CredentialRef{
					Backend: config.CredentialOSKeyring,
					Key:     "jmap-work",
					Consent: true,
				},
			},
		},
	}
	factoryCalls := 0
	var observedPassword []byte
	factoryError := errors.New("synthetic factory stop")
	backend := &sessionBackend{
		configuration: configuration,
		credentials:   resolver,
		accounts:      make(map[domain.AccountID]sessionAccount),
		previews:      make(map[string]sessionPreview),
		newJMAP: func(_ context.Context, options jmap.Options) (*jmap.Client, error) {
			factoryCalls++
			observedPassword = options.Password
			return nil, factoryError
		},
	}
	mcpCaller := domain.Caller{Surface: "mcp", Instance: "synthetic-client"}
	cliCaller := domain.Caller{Surface: "cli", Instance: "synthetic-process"}

	_, err = backend.ListMail(t.Context(), application.MailListInput{
		Account: accountID,
		Folder: application.MailFolder{
			Kind: application.MailFolderDistinguished,
			ID:   "inbox",
		},
		Limit: 25,
	}, mcpCaller)
	if err == nil || !strings.Contains(err.Error(), "corr auth login") {
		t.Fatalf("ListMail() error = %v", err)
	}
	if keyringReads != 0 || factoryCalls != 0 {
		t.Fatalf(
			"ordinary MCP read touched authentication: keyring=%d factory=%d",
			keyringReads,
			factoryCalls,
		)
	}

	_, err = backend.Login(t.Context(), accountID, mcpCaller)
	if err == nil || !strings.Contains(err.Error(), "explicit local CLI") {
		t.Fatalf("MCP Login() error = %v", err)
	}
	if keyringReads != 0 || factoryCalls != 0 {
		t.Fatalf(
			"MCP login touched authentication: keyring=%d factory=%d",
			keyringReads,
			factoryCalls,
		)
	}

	_, err = backend.Login(t.Context(), accountID, cliCaller)
	if !errors.Is(err, factoryError) {
		t.Fatalf("CLI Login() error = %v", err)
	}
	if keyringReads != 1 || factoryCalls != 1 {
		t.Fatalf(
			"explicit CLI login did not resolve once: keyring=%d factory=%d",
			keyringReads,
			factoryCalls,
		)
	}
	for index, value := range observedPassword {
		if value != 0 {
			t.Fatalf("temporary credential byte %d was not zeroed", index)
		}
	}
}
