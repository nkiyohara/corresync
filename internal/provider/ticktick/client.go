// Package ticktick adapts one explicitly authorized TickTick account to the
// provider-neutral task application port. Authentication and client-secret
// resolution remain owned by the local OAuth boundary.
package ticktick

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/provider/restapi"
)

// Options selects one account-scoped TickTick task route.
type Options struct {
	APIBase  string
	Account  domain.AccountID
	ReadOnly bool
	HTTP     *http.Client
}

// Client owns one authorized, account-scoped TickTick transport.
type Client struct {
	api      *restapi.Client
	account  domain.AccountID
	timeZone string
	readOnly bool
}

// New confirms the grant with TickTick's bounded preference probe. TickTick's
// Open API exposes no account identity endpoint, so account isolation rests on
// the independently stored grant handle rather than a claimed email address.
func New(ctx context.Context, options Options) (*Client, error) {
	if err := options.Account.ValidateOpaque(); err != nil {
		return nil, fmt.Errorf("ticktick account identity: %w", err)
	}
	api, err := restapi.New(restapi.Options{BaseURL: options.APIBase, HTTP: options.HTTP})
	if err != nil {
		return nil, err
	}
	var preference struct {
		TimeZone string `json:"timeZone"`
	}
	if _, err := api.DoJSON(
		ctx, http.MethodPost, "open/v1/preference", nil, nil, &preference,
		false, nil, http.StatusOK,
	); err != nil {
		_ = api.Close()
		return nil, fmt.Errorf("confirm TickTick task access: %w", err)
	}
	if preference.TimeZone == "" || len(preference.TimeZone) > 128 {
		_ = api.Close()
		return nil, errors.New("ticktick returned no valid account time zone")
	}
	if _, err := time.LoadLocation(preference.TimeZone); err != nil {
		_ = api.Close()
		return nil, errors.New("ticktick returned an unknown IANA time zone")
	}
	return &Client{
		api: api, account: options.Account, timeZone: preference.TimeZone,
		readOnly: options.ReadOnly,
	}, nil
}

// Close releases account-scoped idle connections.
func (client *Client) Close() error {
	if client == nil || client.api == nil {
		return nil
	}
	return client.api.Close()
}

// TaskCapabilities reports only behavior documented by the Open API. In
// particular, TickTick documents neither reopen nor an atomic write condition.
func (client *Client) TaskCapabilities() application.TaskCapabilities {
	if client == nil || client.api == nil {
		return application.TaskCapabilities{}
	}
	write := !client.readOnly
	return application.TaskCapabilities{
		Read: true, CrossListRead: true, Search: true,
		Create: write, Update: write, Complete: write, Delete: write,
		Recurrence: true, Subtasks: true, Checklist: true,
		Assignments: true, Labels: true, Ordering: true,
		DateOnly: true, ZonedDateTime: true,
		SyncModes: []application.TaskSyncMode{application.TaskSyncPolling},
	}
}

// TaskDegradations makes every intentionally unrepresented provider behavior
// visible to callers instead of implying a stronger portable contract.
func (client *Client) TaskDegradations() []domain.Degradation {
	return []domain.Degradation{
		{Feature: "tasks.identity", Reason: "TickTick Open API exposes no account identity endpoint; the explicitly selected account remains isolated by its dedicated OAuth grant handle"},
		{Feature: "tasks.concurrency", Reason: "TickTick does not document an atomic task version precondition; Corresync revalidates the exact task snapshot immediately before each write"},
		{Feature: "tasks.reopen", Reason: "TickTick Open API does not document reopening a completed task"},
		{Feature: "tasks.reminders", Reason: "TickTick documents reminder strings without enough portable trigger semantics; existing reminders remain provider-owned"},
		{Feature: "tasks.field_removal", Reason: "TickTick does not document clearing existing start, due, or recurrence values; Corresync rejects those removals instead of inventing null semantics"},
		{Feature: "tasks.completed_reads", Reason: "TickTick filter and completed-task reads return at most 200 tasks and expose no continuation token"},
		{Feature: "tasks.polling", Reason: "TickTick exposes no task delta token or webhook; synchronization uses bounded full snapshots"},
		{Feature: "tasks.sync_scale", Reason: "TickTick polling stops when a complete bounded snapshot or cursor cannot be represented safely"},
		{Feature: "tasks.comments", Reason: "TickTick comments remain untouched because the canonical task contract has no comment field"},
		{Feature: "tasks.focus", Reason: "TickTick focus summaries remain untouched because the canonical task contract has no focus field"},
	}
}

func (client *Client) requireRead() error {
	if client == nil || client.api == nil || client.timeZone == "" {
		return errors.New("the TickTick task service is not enabled")
	}
	return nil
}

func (client *Client) requireWrite() error {
	if err := client.requireRead(); err != nil {
		return err
	}
	if client.readOnly {
		return errors.New("the TickTick route was authorized read-only")
	}
	return nil
}
