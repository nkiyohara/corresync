//go:build live

package ticktick_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/credential"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/oauthlocal"
	"github.com/nkiyohara/corresync/internal/provider/ticktick"
)

// TestLiveTickTickReadOnly is excluded from default tests and CI. It resolves
// the confidential-client secret from a separately consented OS-keyring
// handle, requests tasks:read, and never logs projects, tasks, or credentials.
func TestLiveTickTickReadOnly(t *testing.T) {
	if os.Getenv("CORRESYNC_LIVE_CONFIRM") != "ticktick-read-only" {
		t.Skip("set CORRESYNC_LIVE_CONFIRM=ticktick-read-only to opt in")
	}
	clientID := os.Getenv("CORRESYNC_LIVE_TICKTICK_CLIENT_ID")
	redirectURI := os.Getenv("CORRESYNC_LIVE_TICKTICK_REDIRECT_URI")
	authorizationKey := os.Getenv("CORRESYNC_LIVE_TICKTICK_AUTHORIZATION_KEY")
	clientSecretKey := os.Getenv("CORRESYNC_LIVE_TICKTICK_CLIENT_SECRET_KEY")
	if clientID == "" || redirectURI == "" || authorizationKey == "" || clientSecretKey == "" {
		t.Fatal("TickTick client ID, redirect URI, authorization key, and client-secret key are required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	resolver, err := credential.New(credential.Options{})
	if err != nil {
		t.Fatal(err)
	}

	manager, err := oauthlocal.New(oauthlocal.Options{})
	if err != nil {
		t.Fatal(err)
	}
	provider, err := oauthlocal.ProviderFor(
		domain.ProviderTickTick,
		oauthlocal.Services{Tasks: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	authorization, err := manager.AuthorizeConfidential(
		ctx,
		config.OAuthClient{
			ClientID: clientID, RedirectURI: redirectURI,
			Authorization: config.CredentialRef{
				Backend: config.CredentialOSKeyring, Key: authorizationKey, Consent: true,
			},
		},
		provider,
		func(ctx context.Context) ([]byte, error) {
			secret, err := resolver.Resolve(ctx, config.CredentialRef{
				Backend: config.CredentialOSKeyring, Key: clientSecretKey, Consent: true,
			})
			if err != nil {
				return nil, err
			}
			clientSecret := secret.CopyBytes()
			if err := secret.Close(); err != nil {
				for index := range clientSecret {
					clientSecret[index] = 0
				}
				return nil, err
			}
			return clientSecret, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	const account domain.AccountID = "acc_00000000000000000000000000000114"
	client, err := ticktick.New(ctx, ticktick.Options{
		APIBase: "https://api.ticktick.com", Account: account,
		ReadOnly: true, HTTP: authorization.HTTPClient(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Error(err)
		}
	})

	lists, err := client.ListTaskLists(ctx, application.TaskListInput{
		Account: account, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(lists.Lists) == 0 {
		return
	}
	if _, err := client.ListTasks(ctx, application.TaskReadInput{
		Account: account, ListID: lists.Lists[0].ID, Limit: 10,
	}); err != nil {
		t.Fatal(err)
	}
}
