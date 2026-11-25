package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/buildinfo"
	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/daemonapi"
	"github.com/nkiyohara/corresync/internal/domain"
)

func TestSessionBackendReportsPendingSignInKindAndExpiryOnly(t *testing.T) {
	t.Parallel()

	deadline := time.Date(2026, 11, 25, 10, 3, 0, 0, time.UTC)
	backend := &sessionBackend{
		configuration: config.Config{
			Accounts: map[string]config.Account{
				"relay": {ID: "acc_00000000000000000000000000000001"},
				"view":  {ID: "acc_00000000000000000000000000000002"},
				"idle":  {ID: "acc_00000000000000000000000000000003"},
			},
		},
		terminalSessions: map[string]*terminalLoginSession{
			"tls1_relay": {id: "tls1_relay", account: "acc_00000000000000000000000000000001", deadline: deadline},
			"tls1_view": {
				id: "tls1_view", account: "acc_00000000000000000000000000000002",
				graphical: true, deadline: deadline.Add(time.Minute),
			},
		},
		terminalAccounts: map[domain.AccountID]string{
			"acc_00000000000000000000000000000001": "tls1_relay",
			"acc_00000000000000000000000000000002": "tls1_view",
		},
	}
	result, err := backend.SessionStatus(t.Context(), domain.Caller{})
	if err != nil {
		t.Fatalf("SessionStatus() error = %v", err)
	}
	want := []daemonapi.PendingSignIn{
		{Account: "acc_00000000000000000000000000000001", Kind: daemonapi.PendingSignInTextRelay, ExpiresAt: deadline},
		{Account: "acc_00000000000000000000000000000002", Kind: daemonapi.PendingSignInLoginView, ExpiresAt: deadline.Add(time.Minute)},
	}
	if len(result.PendingSignIns) != len(want) {
		t.Fatalf("PendingSignIns = %+v, want %+v", result.PendingSignIns, want)
	}
	for index := range want {
		got := result.PendingSignIns[index]
		if got.Account != want[index].Account || got.Kind != want[index].Kind || !got.ExpiresAt.Equal(want[index].ExpiresAt) {
			t.Fatalf("PendingSignIns[%d] = %+v, want %+v", index, got, want[index])
		}
	}
	encoded, err := json.Marshal(result.PendingSignIns)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "tls1_") {
		t.Fatalf("pending sign-ins expose a session ID: %s", encoded)
	}

	backend.terminalSessions = map[string]*terminalLoginSession{}
	backend.terminalAccounts = map[domain.AccountID]string{}
	result, err = backend.SessionStatus(t.Context(), domain.Caller{})
	if err != nil {
		t.Fatalf("SessionStatus() error = %v", err)
	}
	if result.PendingSignIns != nil {
		t.Fatalf("PendingSignIns without a sign-in = %+v", result.PendingSignIns)
	}
}

func TestAuthStatusShowsPendingSignIn(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 11, 25, 10, 0, 30, 0, time.UTC)
	services := testAuthenticationStatuses(adapterTestAccountID, "work", domain.ProviderMicrosoftOWA, "", "", false)
	mail := *services.Mail
	mail.State = application.AuthenticationStatePending
	mail.Reason = application.AuthenticationReasonInteractionPending
	services.Mail = &mail
	account := daemonapi.SessionStatus{
		Account: adapterTestAccountID, Alias: "work",
		Provider:     domain.ProviderMicrosoftOWA,
		MailProvider: domain.ProviderMicrosoftOWA, State: "pending",
		Services: services,
	}
	status := daemonapi.Status{Version: "0.9.6", ProcessID: 4242}
	for _, test := range []struct {
		name     string
		pending  []daemonapi.PendingSignIn
		wantLine string
		wantJSON string
	}{
		{
			name: "forwarded login view",
			pending: []daemonapi.PendingSignIn{{
				Account: adapterTestAccountID, Kind: daemonapi.PendingSignInLoginView,
				ExpiresAt: now.Add(2*time.Minute + 10*time.Second),
			}},
			wantLine: "Sign-in in progress (forwarded login view), expires in 3 min",
			wantJSON: `"pendingSignIn":{"kind":"login_view","expiresAt":"2026-11-25T10:02:40Z"}`,
		},
		{
			name: "text relay",
			pending: []daemonapi.PendingSignIn{{
				Account: adapterTestAccountID, Kind: daemonapi.PendingSignInTextRelay,
				ExpiresAt: now.Add(20 * time.Second),
			}},
			wantLine: "Sign-in in progress (text relay in a terminal), expires in less than a minute",
			wantJSON: `"pendingSignIn":{"kind":"text_relay","expiresAt":"2026-11-25T10:00:50Z"}`,
		},
		{name: "daemon before protocol 31", wantLine: "sign-in in progress"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			report := newAuthStatusReport(status, daemonapi.SessionStatusResult{
				Accounts: []daemonapi.SessionStatus{account}, PendingSignIns: test.pending,
			}, "")
			var stdout bytes.Buffer
			app := newRuntime(t.Context(), "", &stdout, &bytes.Buffer{}, buildinfo.Current())
			if err := writeAuthStatus(app, report, now); err != nil {
				t.Fatalf("writeAuthStatus() error = %v", err)
			}
			output := stdout.String()
			if !strings.Contains(output, test.wantLine) {
				t.Fatalf("auth status output is missing %q:\n%s", test.wantLine, output)
			}
			if test.pending == nil && strings.Contains(output, "Sign-in in progress (") {
				t.Fatalf("auth status shows a pending sign-in it was not given:\n%s", output)
			}
			if test.pending != nil && !strings.Contains(output, "corr auth login --account work") {
				t.Fatalf("auth status does not say how to start again:\n%s", output)
			}
			encoded, err := json.Marshal(report)
			if err != nil {
				t.Fatal(err)
			}
			if test.wantJSON != "" && !strings.Contains(string(encoded), test.wantJSON) {
				t.Fatalf("auth status JSON = %s, want %s", encoded, test.wantJSON)
			}
			if test.wantJSON == "" && strings.Contains(string(encoded), "pendingSignIn") {
				t.Fatalf("auth status JSON has a pending sign-in: %s", encoded)
			}
		})
	}
}

func TestExpiryLabelRoundsUpToMinutes(t *testing.T) {
	t.Parallel()

	for remaining, want := range map[time.Duration]string{
		-time.Second:                "expiring now",
		0:                           "expiring now",
		59 * time.Second:            "expires in less than a minute",
		time.Minute:                 "expires in 1 min",
		2*time.Minute + time.Second: "expires in 3 min",
		10 * time.Minute:            "expires in 10 min",
	} {
		if got := expiryLabel(remaining); got != want {
			t.Errorf("expiryLabel(%s) = %q, want %q", remaining, got, want)
		}
	}
}
