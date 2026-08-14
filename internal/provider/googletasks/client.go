// Package googletasks adapts one explicitly authorized Google Tasks account
// to Corresync's provider-neutral task port. OAuth remains owned by oauthlocal;
// this package receives only an already authorized account-scoped transport.
package googletasks

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/provider/restapi"
)

const defaultIdentityBase = "https://openidconnect.googleapis.com"

// Options selects one independently consented Google Tasks route. IdentityBase
// exists only for synthetic contract tests; production always uses Google's
// pinned OpenID Connect endpoint.
type Options struct {
	APIBase      string
	IdentityBase string
	Address      string
	Account      domain.AccountID
	ReadOnly     bool
	HTTP         *http.Client
	Now          func() time.Time
}

// Client owns one authorized, account-scoped Google Tasks transport.
type Client struct {
	api      *restapi.Client
	account  domain.AccountID
	readOnly bool
	now      func() time.Time
}

// New confirms the OpenID email and performs a bounded, read-only task-list
// probe before exposing capabilities. It cannot start authorization.
func New(ctx context.Context, options Options) (*Client, error) {
	parsed, err := mail.ParseAddress(options.Address)
	if err != nil || parsed.Address != options.Address || parsed.Name != "" {
		return nil, errors.New("google Tasks account address must be one bare address")
	}
	if err := options.Account.ValidateOpaque(); err != nil {
		return nil, fmt.Errorf("google Tasks account identity: %w", err)
	}
	identityBase := options.IdentityBase
	if identityBase == "" {
		identityBase = defaultIdentityBase
	}
	identity, err := restapi.New(restapi.Options{BaseURL: identityBase, HTTP: options.HTTP})
	if err != nil {
		return nil, err
	}
	var user struct {
		Subject       string `json:"sub"`
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
	}
	if _, err := identity.DoJSON(
		ctx, http.MethodGet, "v1/userinfo", nil, nil, &user,
		false, nil, http.StatusOK,
	); err != nil {
		_ = identity.Close()
		return nil, fmt.Errorf("confirm Google Tasks identity: %w", err)
	}
	_ = identity.Close()
	if !validProviderID(user.Subject) || !user.EmailVerified ||
		!strings.EqualFold(user.Email, options.Address) {
		return nil, errors.New("google Tasks grant identity does not match the configured account")
	}
	api, err := restapi.New(restapi.Options{BaseURL: options.APIBase, HTTP: options.HTTP})
	if err != nil {
		return nil, err
	}
	var probe taskListPage
	if _, err := api.DoJSON(
		ctx, http.MethodGet, "tasks/v1/users/@me/lists",
		queryValues("maxResults", "1"), nil, &probe,
		false, nil, http.StatusOK,
	); err != nil {
		_ = api.Close()
		return nil, fmt.Errorf("confirm Google Tasks access: %w", err)
	}
	if len(probe.Items) > 1 || probe.NextPageToken != "" && !validPageToken(probe.NextPageToken) {
		_ = api.Close()
		return nil, errors.New("google Tasks returned an invalid task-list probe")
	}
	if len(probe.Items) == 1 && !validTaskList(probe.Items[0]) {
		_ = api.Close()
		return nil, errors.New("google Tasks returned an invalid task-list probe")
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	return &Client{
		api: api, account: options.Account, readOnly: options.ReadOnly, now: now,
	}, nil
}

// Close releases account-scoped idle connections.
func (client *Client) Close() error {
	if client == nil {
		return nil
	}
	return client.api.Close()
}

// TaskCapabilities reports only the fields documented by the public Tasks API.
func (client *Client) TaskCapabilities() application.TaskCapabilities {
	if client == nil || client.api == nil {
		return application.TaskCapabilities{}
	}
	write := !client.readOnly
	return application.TaskCapabilities{
		Read: true, Create: write, Update: write, Complete: write,
		Reopen: write, Delete: write, OptimisticConcurrency: true,
		Subtasks: true, Ordering: true, DateOnly: true,
		SyncModes: []application.TaskSyncMode{application.TaskSyncPolling},
	}
}

// TaskDegradations publishes provider semantics that must not be inferred from
// Google branding or silently coerced into richer canonical fields.
func (client *Client) TaskDegradations() []domain.Degradation {
	return []domain.Degradation{
		{Feature: "tasks.due_time", Reason: "Google Tasks stores only the due date and discards the time component"},
		{Feature: "tasks.start", Reason: "Google Tasks does not expose a task start value"},
		{Feature: "tasks.priority", Reason: "Google Tasks does not expose task priority"},
		{Feature: "tasks.reminders", Reason: "Google Tasks reminders are not exposed by the public Tasks API"},
		{Feature: "tasks.recurrence", Reason: "Google Tasks recurrence is not exposed by the public Tasks API"},
		{Feature: "tasks.search", Reason: "Google Tasks exposes no server-side task search"},
		{Feature: "tasks.incremental_sync", Reason: "Google Tasks has no delta or webhook contract; Corresync performs bounded updatedMin polling"},
		{Feature: "tasks.assigned_writes", Reason: "assigned-task source metadata is output-only and provider restrictions are enforced before each write"},
		{Feature: "tasks.linked_sources", Reason: "Gmail, Chat, Docs, and web links are output-only and cannot be replaced through the Tasks API"},
		{Feature: "tasks.default_list", Reason: "the task-list API does not identify a default list"},
	}
}

func (client *Client) requireRead() error {
	if client == nil || client.api == nil {
		return errors.New("the Google Tasks service is not enabled")
	}
	return nil
}

func (client *Client) requireWrite() error {
	if err := client.requireRead(); err != nil {
		return err
	}
	if client.readOnly {
		return errors.New("the Google Tasks route was authorized read-only")
	}
	return nil
}
