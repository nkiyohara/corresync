package main

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/approval"
	"github.com/nkiyohara/corresync/internal/buildinfo"
	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/daemonapi"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/localipc"
	"github.com/nkiyohara/corresync/internal/policy"
)

type daemonMCPAudit struct {
	events []application.AuditEvent
}

func (recorder *daemonMCPAudit) Record(
	_ context.Context,
	event application.AuditEvent,
) error {
	recorder.events = append(recorder.events, event)
	return nil
}

func daemonMCPGuard(
	t *testing.T,
	rules policy.Rules,
	recorder application.AuditRecorder,
) *application.Guard {
	t.Helper()
	approvals, err := approval.NewStore(approval.Options{})
	if err != nil {
		t.Fatal(err)
	}
	guard, err := application.NewGuard(rules, approvals, recorder)
	if err != nil {
		t.Fatal(err)
	}
	return guard
}

func TestDaemonMCPAccountLifecycleUsesCallerBoundPreviewCommit(t *testing.T) {
	app, path, _ := newAccountCommandRuntime(t, &accountDiscovererStub{})
	accounts, _, err := app.accountServices()
	if err != nil {
		t.Fatal(err)
	}
	configuration, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	recorder := &daemonMCPAudit{}
	rules := policy.DefaultRules()
	rules.PreviewReversibleWrites = true
	backend := &daemonMCPBackend{
		app: app, configuration: configuration,
		defaultAccount: configuration.Accounts[configuration.DefaultAccount].ID,
		accounts:       accounts, guard: daemonMCPGuard(t, rules, recorder),
		accountMutation: func(
			ctx context.Context,
			_ domain.Caller,
			change func(context.Context) (application.AccountView, error),
		) (application.AccountView, error) {
			return change(ctx)
		},
	}
	caller := domain.Caller{Surface: "mcp", Instance: "lifecycle-test"}
	otherCaller := domain.Caller{Surface: "mcp", Instance: "other-test"}
	addInput := application.AccountAddInput{
		Alias: "team", Address: "reader@example.invalid",
		Mail: &application.AccountMailRouteInput{
			Provider: domain.ProviderMicrosoftOWA,
			OutlookWeb: &application.AccountOutlookWebInput{
				Origin: "https://outlook.example.invalid",
			},
		},
	}
	addPreview, err := backend.PreviewAccountAdd(t.Context(), addInput, caller)
	if err != nil || addPreview.Preview == nil ||
		addPreview.Status != "approval_required" {
		t.Fatalf("add preview = %+v error = %v", addPreview, err)
	}
	if _, err := backend.CommitAccountAdd(
		t.Context(),
		addPreview.Preview.Token,
		otherCaller,
	); err == nil {
		t.Fatal("account add accepted another caller's approval")
	}
	added, err := backend.CommitAccountAdd(
		t.Context(),
		addPreview.Preview.Token,
		caller,
	)
	if err != nil || added.Account == nil || added.Account.Alias != "team" {
		t.Fatalf("add commit = %+v error = %v", added, err)
	}
	if _, err := backend.CommitAccountAdd(
		t.Context(),
		addPreview.Preview.Token,
		caller,
	); err == nil {
		t.Fatal("account add replayed an approval")
	}

	renamePreview, err := backend.PreviewAccountRename(
		t.Context(),
		application.AccountRenameInput{Account: "team", NewAlias: "office"},
		caller,
	)
	if err != nil || renamePreview.Preview == nil {
		t.Fatalf("rename preview = %+v error = %v", renamePreview, err)
	}
	if _, err := backend.CommitAccountAdd(
		t.Context(),
		renamePreview.Preview.Token,
		caller,
	); err == nil {
		t.Fatal("account add consumed a rename approval")
	}
	renamed, err := backend.CommitAccountRename(
		t.Context(),
		renamePreview.Preview.Token,
		caller,
	)
	if err != nil || renamed.Account == nil ||
		renamed.Account.Alias != "office" ||
		renamed.Account.ID != added.Account.ID {
		t.Fatalf("rename commit = %+v error = %v", renamed, err)
	}

	removePreview, err := backend.PreviewAccountRemove(
		t.Context(),
		application.AccountRemoveInput{Account: "office"},
		caller,
	)
	if err != nil || removePreview.Preview == nil ||
		removePreview.Review == nil ||
		!removePreview.Review.PurgesLocalState {
		t.Fatalf("remove preview = %+v error = %v", removePreview, err)
	}
	removed, err := backend.CommitAccountRemove(
		t.Context(),
		removePreview.Preview.Token,
		caller,
	)
	if err != nil || removed.Account == nil ||
		removed.Account.ID != added.Account.ID {
		t.Fatalf("remove commit = %+v error = %v", removed, err)
	}
	if _, err := accounts.Show(t.Context(), "office"); err == nil {
		t.Fatal("removed account remained configured")
	}
	if len(recorder.events) != 9 {
		t.Fatalf("account lifecycle audit events = %d, want 9", len(recorder.events))
	}
	for index, phase := range []application.AuditPhase{
		application.AuditPhasePrepared,
		application.AuditPhaseCommitted,
		application.AuditPhaseExecuted,
	} {
		if recorder.events[index].Phase != phase {
			t.Fatalf("audit event %d = %+v, want phase %q", index, recorder.events[index], phase)
		}
	}
}

func TestDaemonMCPAccountLifecycleCommitKeepsPreviewedOpaqueIdentity(t *testing.T) {
	app, path, _ := newAccountCommandRuntime(t, &accountDiscovererStub{})
	accounts, _, err := app.accountServices()
	if err != nil {
		t.Fatal(err)
	}
	configuration, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	originalDefault := configuration.Accounts[configuration.DefaultAccount].ID
	recorder := &daemonMCPAudit{}
	rules := policy.DefaultRules()
	rules.PreviewReversibleWrites = true
	backend := &daemonMCPBackend{
		app: app, configuration: configuration,
		defaultAccount: originalDefault,
		accounts:       accounts, guard: daemonMCPGuard(t, rules, recorder),
		accountMutation: func(
			ctx context.Context,
			_ domain.Caller,
			change func(context.Context) (application.AccountView, error),
		) (application.AccountView, error) {
			return change(ctx)
		},
	}
	added, err := accounts.Add(t.Context(), application.AccountAddInput{
		Alias: "team", Address: "reader@example.invalid",
		Mail: &application.AccountMailRouteInput{
			Provider: domain.ProviderMicrosoftOWA,
			OutlookWeb: &application.AccountOutlookWebInput{
				Origin: "https://outlook.example.invalid",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	caller := domain.Caller{Surface: "mcp", Instance: "identity-test"}
	renamePreview, err := backend.PreviewAccountRename(
		t.Context(),
		application.AccountRenameInput{Account: "team", NewAlias: "office"},
		caller,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := accounts.Rename(t.Context(), application.AccountRenameInput{
		Account: "team", NewAlias: "archive",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := accounts.Rename(t.Context(), application.AccountRenameInput{
		Account: "work", NewAlias: "team",
	}); err != nil {
		t.Fatal(err)
	}
	renamed, err := backend.CommitAccountRename(
		t.Context(),
		renamePreview.Preview.Token,
		caller,
	)
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Account == nil || renamed.Account.ID != added.ID ||
		renamed.Account.Alias != "office" {
		t.Fatalf("rename changed the wrong account: %+v", renamed)
	}

	removePreview, err := backend.PreviewAccountRemove(
		t.Context(),
		application.AccountRemoveInput{Account: "office"},
		caller,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := accounts.Rename(t.Context(), application.AccountRenameInput{
		Account: "office", NewAlias: "archive",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := accounts.Rename(t.Context(), application.AccountRenameInput{
		Account: "team", NewAlias: "office",
	}); err != nil {
		t.Fatal(err)
	}
	removed, err := backend.CommitAccountRemove(
		t.Context(),
		removePreview.Preview.Token,
		caller,
	)
	if err != nil {
		t.Fatal(err)
	}
	if removed.Account == nil || removed.Account.ID != added.ID {
		t.Fatalf("remove changed the wrong account: %+v", removed)
	}
	survivor, err := accounts.Show(t.Context(), "office")
	if err != nil {
		t.Fatal(err)
	}
	if survivor.ID != originalDefault {
		t.Fatalf("unexpected survivor: %+v", survivor)
	}
}

func TestDaemonMCPAccountLifecycleHonorsReadOnlyPolicy(t *testing.T) {
	app, path, _ := newAccountCommandRuntime(t, &accountDiscovererStub{})
	accounts, _, err := app.accountServices()
	if err != nil {
		t.Fatal(err)
	}
	configuration, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	recorder := &daemonMCPAudit{}
	backend := &daemonMCPBackend{
		app: app, configuration: configuration,
		defaultAccount: configuration.Accounts[configuration.DefaultAccount].ID,
		accounts:       accounts,
		guard: daemonMCPGuard(
			t,
			policy.Rules{
				Mode:                    policy.ModeReadOnly,
				PreviewReversibleWrites: true,
			},
			recorder,
		),
	}
	_, err = backend.PreviewAccountAdd(
		t.Context(),
		application.AccountAddInput{
			Alias: "team", Address: "reader@example.invalid",
			Mail: &application.AccountMailRouteInput{
				Provider: domain.ProviderMicrosoftOWA,
				OutlookWeb: &application.AccountOutlookWebInput{
					Origin: "https://outlook.example.invalid",
				},
			},
		},
		domain.Caller{Surface: "mcp", Instance: "read-only-test"},
	)
	if !errors.Is(err, application.ErrDenied) {
		t.Fatalf("PreviewAccountAdd() error = %v, want policy denial", err)
	}
	if len(recorder.events) != 1 ||
		recorder.events[0].Outcome != application.AuditOutcomeDenied {
		t.Fatalf("read-only audit = %+v", recorder.events)
	}
}

func TestDaemonMCPAccountMutationRestartsAroundConfigurationChange(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "config.toml")
	if err := config.Save(configPath, config.OutlookDefault()); err != nil {
		t.Fatal(err)
	}
	initialDigest, err := config.Fingerprint(configPath)
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := localipc.ResolveInState(
		configPath,
		filepath.Join(root, "state"),
	)
	if err != nil {
		t.Fatal(err)
	}
	previous := startLifecycleTestDaemon(
		t.Context(),
		t,
		endpoint,
		daemonapi.ProtocolVersion,
		"dev",
		501,
		initialDigest,
	)
	t.Cleanup(previous.stop)

	app := newRuntime(
		t.Context(),
		configPath,
		&bytes.Buffer{},
		&bytes.Buffer{},
		buildinfo.Info{Version: "dev", OS: "linux", Arch: "amd64"},
	)
	app.endpoint = func(string) (localipc.Endpoint, error) {
		return endpoint, nil
	}
	var starts atomic.Int32
	var replacement lifecycleTestDaemon
	app.startDaemon = func(ctx context.Context, path string) error {
		if path != configPath {
			t.Fatalf("restart path = %q", path)
		}
		digest, fingerprintErr := config.Fingerprint(path)
		if fingerprintErr != nil {
			return fingerprintErr
		}
		starts.Add(1)
		replacement = startLifecycleTestDaemon(
			ctx,
			t,
			endpoint,
			daemonapi.ProtocolVersion,
			"dev",
			502,
			digest,
		)
		return nil
	}
	client, err := daemonapi.NewClient(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	accounts, _, err := app.accountServices()
	if err != nil {
		t.Fatal(err)
	}
	configuration, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	backend := &daemonMCPBackend{
		Client: client, app: app, accounts: accounts,
		configuration:  configuration,
		defaultAccount: configuration.Accounts[configuration.DefaultAccount].ID,
	}
	staleBackend := &daemonMCPBackend{
		app:            app,
		configuration:  configuration,
		defaultAccount: configuration.Accounts[configuration.DefaultAccount].ID,
	}
	account, err := backend.commitAccountMutation(
		t.Context(),
		domain.Caller{Surface: "mcp", Instance: "restart-test"},
		func(ctx context.Context) (application.AccountView, error) {
			return accounts.Rename(ctx, application.AccountRenameInput{
				Account: "work", NewAlias: "office",
			})
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if replacement.stop != nil {
		t.Cleanup(replacement.stop)
	}
	if account.Alias != "office" || starts.Load() != 1 ||
		previous.shutdowns.Load() != 1 {
		t.Fatalf(
			"mutation result = %+v starts=%d shutdowns=%d",
			account,
			starts.Load(),
			previous.shutdowns.Load(),
		)
	}
	resolved, err := backend.ResolveAccount("office")
	if err != nil || resolved != account.ID {
		t.Fatalf("refreshed resolution = %q error = %v", resolved, err)
	}
	resolved, err = staleBackend.ResolveAccount("office")
	if err != nil || resolved != account.ID {
		t.Fatalf("other process resolution = %q error = %v", resolved, err)
	}
}
