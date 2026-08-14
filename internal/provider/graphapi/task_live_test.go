//go:build live

package graphapi_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/microsoftcloud"
	"github.com/nkiyohara/corresync/internal/oauthlocal"
	"github.com/nkiyohara/corresync/internal/provider/graphapi"
)

// TestLiveMicrosoftTodoReadOnly is excluded from default tests and CI. It uses
// an explicitly selected public client and OS-keyring authorization, requests
// Tasks.Read only, and never logs task lists, titles, notes, or cursor values.
func TestLiveMicrosoftTodoReadOnly(t *testing.T) {
	if os.Getenv("CORRESYNC_LIVE_CONFIRM") != "microsoft-todo-read-only" {
		t.Skip("set CORRESYNC_LIVE_CONFIRM=microsoft-todo-read-only to opt in")
	}
	address := os.Getenv("CORRESYNC_LIVE_MICROSOFT_ADDRESS")
	clientID := os.Getenv("CORRESYNC_LIVE_MICROSOFT_CLIENT_ID")
	redirectURI := os.Getenv("CORRESYNC_LIVE_MICROSOFT_REDIRECT_URI")
	authorizationKey := os.Getenv("CORRESYNC_LIVE_MICROSOFT_AUTHORIZATION_KEY")
	if address == "" || clientID == "" || redirectURI == "" || authorizationKey == "" {
		t.Fatal("Microsoft address, client ID, redirect URI, and authorization key are required")
	}
	cloud, err := microsoftcloud.Resolve(
		microsoftcloud.ID(os.Getenv("CORRESYNC_LIVE_MICROSOFT_CLOUD")),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !cloud.TasksAvailable {
		t.Fatal("Microsoft To Do is unavailable in the selected cloud")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	manager, err := oauthlocal.New(oauthlocal.Options{})
	if err != nil {
		t.Fatal(err)
	}
	provider, err := oauthlocal.ProviderFor(
		domain.ProviderMicrosoftGraph,
		oauthlocal.Services{Tasks: true, MicrosoftCloud: cloud.ID},
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
	client, err := graphapi.New(ctx, graphapi.Options{
		APIBase: cloud.APIBase, Address: address, Tasks: true,
		HTTP: authorization.HTTPClient(),
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
