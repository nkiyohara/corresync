//go:build live

package todoist_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/oauthlocal"
	"github.com/nkiyohara/corresync/internal/provider/todoist"
)

// TestLiveTodoistReadOnly is excluded from default tests and CI. It uses one
// explicitly selected public client and OS-keyring authorization, requests
// data:read only, and never logs projects, task fields, plan data, or cursors.
func TestLiveTodoistReadOnly(t *testing.T) {
	if os.Getenv("CORRESYNC_LIVE_CONFIRM") != "todoist-read-only" {
		t.Skip("set CORRESYNC_LIVE_CONFIRM=todoist-read-only to opt in")
	}
	address := os.Getenv("CORRESYNC_LIVE_TODOIST_ADDRESS")
	clientID := os.Getenv("CORRESYNC_LIVE_TODOIST_CLIENT_ID")
	redirectURI := os.Getenv("CORRESYNC_LIVE_TODOIST_REDIRECT_URI")
	authorizationKey := os.Getenv("CORRESYNC_LIVE_TODOIST_AUTHORIZATION_KEY")
	if address == "" || clientID == "" || redirectURI == "" || authorizationKey == "" {
		t.Fatal("Todoist address, client ID, redirect URI, and authorization key are required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	manager, err := oauthlocal.New(oauthlocal.Options{})
	if err != nil {
		t.Fatal(err)
	}
	provider, err := oauthlocal.ProviderFor(
		domain.ProviderTodoist,
		oauthlocal.Services{Tasks: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	authorization, err := manager.Authorize(ctx, config.OAuthClient{
		ClientID: clientID, RedirectURI: redirectURI,
		Authorization: config.CredentialRef{
			Backend: config.CredentialOSKeyring, Key: authorizationKey, Consent: true,
		},
	}, provider)
	if err != nil {
		t.Fatal(err)
	}
	client, err := todoist.New(ctx, todoist.Options{
		APIBase: "https://api.todoist.com/api/v1", Address: address,
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

	const account domain.AccountID = "acc_00000000000000000000000000000001"
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
