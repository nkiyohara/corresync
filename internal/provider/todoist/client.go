// Package todoist adapts one explicitly authorized Todoist account to the
// provider-neutral task application port. Authentication remains owned by the
// local OAuth boundary; this package never accepts a personal API token.
package todoist

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"net/url"
	"strings"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/provider/restapi"
)

// Options selects one account-scoped Todoist task route.
type Options struct {
	APIBase  string
	Address  string
	ReadOnly bool
	HTTP     *http.Client
}

type planLimits struct {
	PlanName  string `json:"plan_name"`
	Deadlines bool   `json:"deadlines"`
	Labels    bool   `json:"labels"`
	Reminders bool   `json:"reminders"`
}

// Client owns one authorized, account-scoped Todoist transport.
type Client struct {
	api      *restapi.Client
	userID   string
	readOnly bool
	plan     planLimits
}

// New confirms the delegated user identity and current plan before exposing
// capabilities. The probe is read-only and cannot start authorization.
func New(ctx context.Context, options Options) (*Client, error) {
	parsed, err := mail.ParseAddress(options.Address)
	if err != nil || parsed.Address != options.Address || parsed.Name != "" {
		return nil, errors.New("todoist account address must be one bare address")
	}
	api, err := restapi.New(restapi.Options{BaseURL: options.APIBase, HTTP: options.HTTP})
	if err != nil {
		return nil, err
	}
	var probe struct {
		User struct {
			ID    string `json:"id"`
			Email string `json:"email"`
		} `json:"user"`
		Plan struct {
			Current *planLimits `json:"current"`
		} `json:"user_plan_limits"`
	}
	resourceTypes, _ := marshalJSON([]string{"user", "user_plan_limits"})
	if _, err := api.DoForm(
		ctx, http.MethodPost, "sync", nil,
		url.Values{
			"sync_token":     {"*"},
			"resource_types": {resourceTypes},
		},
		&probe, false, nil, http.StatusOK,
	); err != nil {
		_ = api.Close()
		return nil, fmt.Errorf("confirm Todoist identity and plan: %w", err)
	}
	if !validID(probe.User.ID) || probe.User.Email == "" ||
		!strings.EqualFold(probe.User.Email, options.Address) {
		_ = api.Close()
		return nil, errors.New("todoist grant identity does not match the configured account")
	}
	if probe.Plan.Current == nil || probe.Plan.Current.PlanName == "" {
		_ = api.Close()
		return nil, errors.New("todoist did not return current plan limits")
	}
	return &Client{
		api: api, userID: probe.User.ID,
		readOnly: options.ReadOnly, plan: *probe.Plan.Current,
	}, nil
}

// Close releases account-scoped idle network connections.
func (client *Client) Close() error {
	if client == nil {
		return nil
	}
	return client.api.Close()
}

// TaskCapabilities reports only features confirmed by the identity/plan probe.
func (client *Client) TaskCapabilities() application.TaskCapabilities {
	if client == nil || client.api == nil {
		return application.TaskCapabilities{}
	}
	write := !client.readOnly
	return application.TaskCapabilities{
		Read: true, Create: write, Update: write, Complete: write,
		Reopen: write, Delete: write,
		Reminders: client.plan.Reminders, Recurrence: true, Subtasks: true,
		Assignments: true, Labels: client.plan.Labels,
		DateOnly: true, FloatingDateTime: true, ZonedDateTime: true,
		SyncModes: []application.TaskSyncMode{application.TaskSyncToken},
	}
}

// TaskDegradations describes provider semantics that are intentionally not
// normalized away, including current-plan restrictions observed at sign-in.
func (client *Client) TaskDegradations() []domain.Degradation {
	result := []domain.Degradation{
		{Feature: "tasks.search", Reason: "the Todoist adapter does not expose provider filter syntax as portable search"},
		{Feature: "tasks.completed_reads", Reason: "Todoist API v1 exposes direct get only for active tasks; Corresync does not invent an unbounded completed-archive scan"},
		{Feature: "tasks.concurrency", Reason: "Todoist does not expose an atomic task version precondition; Corresync revalidates the exact snapshot immediately before each write"},
		{Feature: "tasks.recurring_completion", Reason: "completing a recurring Todoist task archives that repeating task; advancing only one occurrence is not represented by the canonical completion result"},
		{Feature: "tasks.comments", Reason: "Todoist comments remain untouched because the canonical task contract has no comment field"},
		{Feature: "tasks.duration", Reason: "Todoist duration remains untouched because the canonical task contract has no duration field"},
		{Feature: "tasks.sections", Reason: "Todoist sections remain provider-owned because the canonical task contract has no section field"},
		{Feature: "tasks.location_reminders", Reason: "Todoist location reminders remain untouched because the canonical task contract has no location reminder field"},
		{Feature: "tasks.sync_scale", Reason: "the account-local sync cursor embeds bounded project membership and stops rather than exceed the canonical cursor limit"},
	}
	if !client.plan.Labels {
		result = append(result, domain.Degradation{
			Feature: "tasks.labels", Reason: "the current Todoist plan does not enable labels",
		})
	}
	if !client.plan.Reminders {
		result = append(result, domain.Degradation{
			Feature: "tasks.reminders", Reason: "the current Todoist plan does not enable reminders",
		})
	}
	if !client.plan.Deadlines {
		result = append(result, domain.Degradation{
			Feature: "tasks.deadlines", Reason: "the current Todoist plan does not enable deadlines",
		})
	}
	return result
}

func (client *Client) requireRead() error {
	if client == nil || client.api == nil || !validID(client.userID) {
		return errors.New("the Todoist task service is not enabled")
	}
	return nil
}

func (client *Client) requireWrite() error {
	if err := client.requireRead(); err != nil {
		return err
	}
	if client.readOnly {
		return errors.New("the Todoist route was authorized read-only")
	}
	return nil
}
