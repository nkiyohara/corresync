package main

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/approval"
	"github.com/nkiyohara/corresync/internal/audit"
	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/daemonapi"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/paths"
	"github.com/nkiyohara/corresync/internal/savedquerystore"
	"github.com/nkiyohara/corresync/internal/settingsstore"
)

// daemonMCPBackend forwards the MCP application boundary to the sole local
// session owner. Provider credentials never enter the MCP stdio process.
type daemonMCPBackend struct {
	*daemonapi.Client
	app             *runtime
	defaultAccount  domain.AccountID
	configuration   config.Config
	accounts        *application.AccountService
	settings        *application.SettingsService
	discovery       *application.AccountDiscoveryService
	guard           *application.Guard
	recorder        *audit.FileRecorder
	accountMu       sync.RWMutex
	mutationMu      sync.Mutex
	accountMutation func(
		context.Context,
		domain.Caller,
		func(context.Context) (application.AccountView, error),
	) (application.AccountView, error)
	settingsMutation func(
		context.Context,
		domain.Caller,
		func(context.Context) (application.SettingsView, error),
	) (application.SettingsView, error)
}

func newDaemonMCPBackend(app *runtime) (*daemonMCPBackend, error) {
	configuration, configPath, err := app.loadConfig()
	if err != nil {
		return nil, err
	}
	client, status, err := app.openDaemonForMCP(app.context)
	if err != nil {
		return nil, err
	}
	accounts, discoverer, err := app.accountServices()
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	settings, err := application.NewSettingsService(settingsstore.Store{ConfigPath: configPath})
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	approvals, err := approval.NewStore(approval.Options{})
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	auditPath, err := paths.AuditPath()
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	recorder, err := audit.NewFileRecorder(auditPath, audit.Options{})
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	rules := configuration.Policy.Rules()
	rules.PreviewReversibleWrites = true
	guard, err := application.NewGuard(rules, approvals, recorder)
	if err != nil {
		_ = recorder.Close()
		_ = client.Close()
		return nil, err
	}
	return &daemonMCPBackend{
		Client: client, app: app,
		defaultAccount: status.DefaultAccount, configuration: configuration,
		accounts: accounts, settings: settings,
		discovery: discoverer, guard: guard, recorder: recorder,
	}, nil
}

func (backend *daemonMCPBackend) Close() error {
	var clientErr error
	if backend.Client != nil {
		clientErr = backend.Client.Close()
	}
	var recorderErr error
	if backend.recorder != nil {
		recorderErr = backend.recorder.Close()
	}
	return errors.Join(clientErr, recorderErr)
}

func (backend *daemonMCPBackend) DefaultAccount() domain.AccountID {
	_ = backend.refreshAccountSnapshot()
	backend.accountMu.RLock()
	defer backend.accountMu.RUnlock()
	return backend.defaultAccount
}

func (backend *daemonMCPBackend) refreshAccountSnapshot() error {
	if backend.app == nil {
		return nil
	}
	configuration, _, err := backend.app.loadConfig()
	if err != nil {
		return err
	}
	if len(configuration.Accounts) == 0 {
		backend.accountMu.Lock()
		backend.configuration = configuration
		backend.defaultAccount = ""
		backend.accountMu.Unlock()
		return nil
	}
	account, exists := configuration.Accounts[configuration.DefaultAccount]
	if !exists {
		return errors.New("configured default account disappeared")
	}
	backend.accountMu.Lock()
	backend.configuration = configuration
	backend.defaultAccount = account.ID
	backend.accountMu.Unlock()
	return nil
}

func (backend *daemonMCPBackend) DiscoverAccounts(
	ctx context.Context,
	address string,
) (application.AccountDiscoveryResult, error) {
	return backend.discovery.Discover(ctx, address)
}

func (backend *daemonMCPBackend) ListAccounts(
	ctx context.Context,
) (application.AccountCatalog, error) {
	return backend.accounts.List(ctx)
}

func (backend *daemonMCPBackend) ShowAccount(
	ctx context.Context,
	reference string,
) (application.AccountView, error) {
	return backend.accounts.Show(ctx, reference)
}

func (backend *daemonMCPBackend) ShowSettings(
	ctx context.Context,
) (application.SettingsView, error) {
	return backend.settings.Show(ctx)
}

func (backend *daemonMCPBackend) PreviewSettingsUpdate(
	ctx context.Context,
	input application.SettingsUpdateInput,
	caller domain.Caller,
) (application.SettingsChangeAccess, error) {
	review, err := backend.settings.Review(ctx, input)
	if err != nil {
		return application.SettingsChangeAccess{}, err
	}
	operation, err := domain.NewOperation(
		"settings.update",
		domain.EffectReversibleWrite,
		backend.DefaultAccount(),
		review,
	)
	if err != nil {
		return application.SettingsChangeAccess{}, err
	}
	preparation, err := backend.guard.Prepare(ctx, operation, caller)
	if err != nil {
		return application.SettingsChangeAccess{}, err
	}
	if preparation.Preview == nil {
		return application.SettingsChangeAccess{}, errors.New(
			"settings update policy did not issue the required preview",
		)
	}
	return application.SettingsChangeAccess{
		Status: "approval_required", Review: &review,
		Preview: preparation.Preview,
	}, nil
}

func (backend *daemonMCPBackend) CommitSettingsUpdate(
	ctx context.Context,
	token string,
	caller domain.Caller,
) (application.SettingsChangeAccess, error) {
	operation, err := backend.guard.CommitFor(
		ctx,
		token,
		caller,
		"settings.update",
		domain.EffectReversibleWrite,
	)
	if err != nil {
		return application.SettingsChangeAccess{}, err
	}
	var review application.SettingsChangeReview
	if err := operation.DecodePayload(&review); err != nil {
		return application.SettingsChangeAccess{}, err
	}
	settings, err := backend.executeSettingsMutation(
		ctx,
		caller,
		func(callContext context.Context) (application.SettingsView, error) {
			return backend.settings.Apply(callContext, review)
		},
	)
	auditErr := backend.guard.RecordExecution(ctx, operation, caller, err)
	if err != nil {
		return application.SettingsChangeAccess{}, errors.Join(err, auditErr)
	}
	if auditErr != nil {
		return application.SettingsChangeAccess{}, auditErr
	}
	return application.SettingsChangeAccess{
		Status: "completed", Settings: &settings,
	}, nil
}

func (backend *daemonMCPBackend) PreviewAccountAdd(
	ctx context.Context,
	input application.AccountAddInput,
	caller domain.Caller,
) (application.AccountChangeAccess, error) {
	plan, review, err := backend.accounts.PlanAdd(ctx, input)
	if err != nil {
		return application.AccountChangeAccess{}, err
	}
	operation, err := domain.NewOperation(
		"account.add",
		domain.EffectReversibleWrite,
		plan.Account,
		plan,
	)
	if err != nil {
		return application.AccountChangeAccess{}, err
	}
	preparation, err := backend.guard.Prepare(ctx, operation, caller)
	if err != nil {
		return application.AccountChangeAccess{}, err
	}
	if preparation.Preview == nil {
		return application.AccountChangeAccess{}, errors.New(
			"account addition policy did not issue the required preview",
		)
	}
	review.RestartsSessions = true
	return application.AccountChangeAccess{
		Status: "approval_required", Review: &review,
		Preview: preparation.Preview,
	}, nil
}

func (backend *daemonMCPBackend) CommitAccountAdd(
	ctx context.Context,
	token string,
	caller domain.Caller,
) (application.AccountChangeAccess, error) {
	operation, err := backend.guard.CommitFor(
		ctx,
		token,
		caller,
		"account.add",
		domain.EffectReversibleWrite,
	)
	if err != nil {
		return application.AccountChangeAccess{}, err
	}
	var plan application.AccountAddPlan
	if err := operation.DecodePayload(&plan); err != nil {
		return application.AccountChangeAccess{}, err
	}
	if operation.Account() != plan.Account {
		return application.AccountChangeAccess{}, errors.New(
			"approved account addition no longer matches its stable identity",
		)
	}
	account, err := backend.executeAccountMutation(
		ctx,
		caller,
		func(callContext context.Context) (application.AccountView, error) {
			return backend.accounts.AddPlanned(callContext, plan)
		},
	)
	auditErr := backend.guard.RecordExecution(ctx, operation, caller, err)
	if err != nil {
		return application.AccountChangeAccess{}, errors.Join(err, auditErr)
	}
	if auditErr != nil {
		return application.AccountChangeAccess{}, auditErr
	}
	return application.AccountChangeAccess{
		Status: "completed", Account: &account,
	}, nil
}

func (backend *daemonMCPBackend) PreviewAccountRename(
	ctx context.Context,
	input application.AccountRenameInput,
	caller domain.Caller,
) (application.AccountChangeAccess, error) {
	review, err := backend.accounts.ReviewRename(ctx, input)
	if err != nil {
		return application.AccountChangeAccess{}, err
	}
	normalized := input
	normalized.Account = string(review.Account)
	operation, err := domain.NewOperation(
		"account.rename",
		domain.EffectReversibleWrite,
		review.Account,
		normalized,
	)
	if err != nil {
		return application.AccountChangeAccess{}, err
	}
	preparation, err := backend.guard.Prepare(ctx, operation, caller)
	if err != nil {
		return application.AccountChangeAccess{}, err
	}
	if preparation.Preview == nil {
		return application.AccountChangeAccess{}, errors.New(
			"account rename policy did not issue the required preview",
		)
	}
	review.RestartsSessions = true
	return application.AccountChangeAccess{
		Status: "approval_required", Review: &review,
		Preview: preparation.Preview,
	}, nil
}

func (backend *daemonMCPBackend) CommitAccountRename(
	ctx context.Context,
	token string,
	caller domain.Caller,
) (application.AccountChangeAccess, error) {
	operation, err := backend.guard.CommitFor(
		ctx,
		token,
		caller,
		"account.rename",
		domain.EffectReversibleWrite,
	)
	if err != nil {
		return application.AccountChangeAccess{}, err
	}
	var input application.AccountRenameInput
	if err := operation.DecodePayload(&input); err != nil {
		return application.AccountChangeAccess{}, err
	}
	if input.Account != string(operation.Account()) {
		return application.AccountChangeAccess{}, errors.New(
			"approved account rename no longer matches its account",
		)
	}
	account, err := backend.executeAccountMutation(
		ctx,
		caller,
		func(callContext context.Context) (application.AccountView, error) {
			return backend.accounts.Rename(callContext, input)
		},
	)
	auditErr := backend.guard.RecordExecution(ctx, operation, caller, err)
	if err != nil {
		return application.AccountChangeAccess{}, errors.Join(err, auditErr)
	}
	if auditErr != nil {
		return application.AccountChangeAccess{}, auditErr
	}
	return application.AccountChangeAccess{
		Status: "completed", Account: &account,
	}, nil
}

func (backend *daemonMCPBackend) PreviewAccountRemove(
	ctx context.Context,
	input application.AccountRemoveInput,
	caller domain.Caller,
) (application.AccountChangeAccess, error) {
	review, err := backend.accounts.ReviewRemove(ctx, input)
	if err != nil {
		return application.AccountChangeAccess{}, err
	}
	normalized := input
	normalized.Account = string(review.Account)
	if review.ReplacementAccount != "" {
		normalized.ReplacementDefault = string(review.ReplacementAccount)
	}
	operation, err := domain.NewOperation(
		"account.remove",
		domain.EffectDestructiveWrite,
		review.Account,
		normalized,
	)
	if err != nil {
		return application.AccountChangeAccess{}, err
	}
	preparation, err := backend.guard.Prepare(ctx, operation, caller)
	if err != nil {
		return application.AccountChangeAccess{}, err
	}
	if preparation.Preview == nil {
		return application.AccountChangeAccess{}, errors.New(
			"account removal policy did not issue the required preview",
		)
	}
	review.RestartsSessions = true
	return application.AccountChangeAccess{
		Status: "approval_required", Review: &review,
		Preview: preparation.Preview,
	}, nil
}

func (backend *daemonMCPBackend) CommitAccountRemove(
	ctx context.Context,
	token string,
	caller domain.Caller,
) (application.AccountChangeAccess, error) {
	operation, err := backend.guard.CommitFor(
		ctx,
		token,
		caller,
		"account.remove",
		domain.EffectDestructiveWrite,
	)
	if err != nil {
		return application.AccountChangeAccess{}, err
	}
	var input application.AccountRemoveInput
	if err := operation.DecodePayload(&input); err != nil {
		return application.AccountChangeAccess{}, err
	}
	if input.Account != string(operation.Account()) {
		return application.AccountChangeAccess{}, errors.New(
			"approved account removal no longer matches its account",
		)
	}
	account, err := backend.executeAccountMutation(
		ctx,
		caller,
		func(callContext context.Context) (application.AccountView, error) {
			return backend.accounts.Remove(callContext, input)
		},
	)
	auditErr := backend.guard.RecordExecution(ctx, operation, caller, err)
	if err != nil {
		return application.AccountChangeAccess{}, errors.Join(err, auditErr)
	}
	if auditErr != nil {
		return application.AccountChangeAccess{}, auditErr
	}
	return application.AccountChangeAccess{
		Status: "completed", Account: &account,
	}, nil
}

func (backend *daemonMCPBackend) executeAccountMutation(
	ctx context.Context,
	caller domain.Caller,
	change func(context.Context) (application.AccountView, error),
) (application.AccountView, error) {
	if backend.accountMutation != nil {
		return backend.accountMutation(ctx, caller, change)
	}
	return backend.commitAccountMutation(ctx, caller, change)
}

func (backend *daemonMCPBackend) executeSettingsMutation(
	ctx context.Context,
	caller domain.Caller,
	change func(context.Context) (application.SettingsView, error),
) (application.SettingsView, error) {
	if backend.settingsMutation != nil {
		return backend.settingsMutation(ctx, caller, change)
	}
	var settings application.SettingsView
	err := backend.commitConfigurationMutation(
		ctx,
		caller,
		"settings",
		func(callContext context.Context) error {
			var changeErr error
			settings, changeErr = change(callContext)
			return changeErr
		},
	)
	return settings, err
}

func (backend *daemonMCPBackend) commitAccountMutation(
	ctx context.Context,
	caller domain.Caller,
	change func(context.Context) (application.AccountView, error),
) (application.AccountView, error) {
	var account application.AccountView
	err := backend.commitConfigurationMutation(
		ctx,
		caller,
		"account",
		func(callContext context.Context) error {
			var changeErr error
			account, changeErr = change(callContext)
			return changeErr
		},
	)
	return account, err
}

func (backend *daemonMCPBackend) commitConfigurationMutation(
	ctx context.Context,
	caller domain.Caller,
	kind string,
	change func(context.Context) error,
) error {
	backend.mutationMu.Lock()
	defer backend.mutationMu.Unlock()
	if backend.app == nil || backend.Client == nil {
		return fmt.Errorf(
			"%s mutation coordinator is unavailable",
			kind,
		)
	}
	configPath, err := backend.app.resolvedConfigPath()
	if err != nil {
		return err
	}
	owner, err := backend.InspectOwner(ctx, caller)
	if err != nil {
		return fmt.Errorf("inspect session owner before %s mutation: %w", kind, err)
	}
	shutdownContext, cancelShutdown := context.WithTimeout(
		ctx,
		daemonControlTimeout,
	)
	shutdownErr := backend.ShutdownOwner(
		shutdownContext,
		caller,
		owner,
	)
	cancelShutdown()
	transitionContext, cancelTransition := context.WithTimeout(
		context.WithoutCancel(ctx),
		daemonReplacementTimeout,
	)
	defer cancelTransition()
	_, replaced, waitErr := waitForDaemonExit(
		transitionContext,
		backend.app,
		backend.Client,
		owner.Status(),
		daemonReplacementTimeout,
	)
	if waitErr != nil {
		return errors.Join(
			fmt.Errorf("stop session owner for %s mutation: %w", kind, waitErr),
			shutdownErr,
		)
	}
	if replaced {
		return fmt.Errorf(
			"session owner changed concurrently; review the %s mutation again",
			kind,
		)
	}

	changeErr := ctx.Err()
	if changeErr == nil {
		changeErr = change(ctx)
	}
	restartContext, cancelRestart := context.WithTimeout(
		context.WithoutCancel(ctx),
		daemonReplacementTimeout+daemonStartupTimeout,
	)
	defer cancelRestart()
	restartErr := backend.restartAfterConfigurationMutation(
		restartContext,
		configPath,
	)
	if changeErr != nil {
		return errors.Join(changeErr, restartErr)
	}
	if restartErr != nil {
		return fmt.Errorf(
			"%s configuration changed but restart failed; inspect `corr config show` and restart the daemon: %w",
			kind, restartErr,
		)
	}
	return nil
}

func (backend *daemonMCPBackend) restartAfterConfigurationMutation(
	ctx context.Context,
	configPath string,
) error {
	if err := backend.app.startDaemon(ctx, configPath); err != nil {
		return fmt.Errorf("start updated session owner: %w", err)
	}
	status, err := waitForDaemon(
		ctx,
		backend.app,
		backend.Client,
		daemonStartupTimeout,
	)
	if err != nil {
		return err
	}
	digest, err := config.Fingerprint(configPath)
	if err != nil {
		return err
	}
	if err := backend.app.validateDaemonStatus(status, digest); err != nil {
		return err
	}
	configuration, err := config.Load(configPath)
	if err != nil {
		return err
	}
	backend.accountMu.Lock()
	backend.configuration = configuration
	backend.defaultAccount = status.DefaultAccount
	backend.accountMu.Unlock()
	return nil
}

func (backend *daemonMCPBackend) ResolveAccount(reference string) (domain.AccountID, error) {
	if err := backend.refreshAccountSnapshot(); err != nil {
		return "", err
	}
	backend.accountMu.RLock()
	defer backend.accountMu.RUnlock()
	_, account, err := backend.configuration.ResolveAccount(reference)
	if err != nil {
		return "", err
	}
	return account.ID, nil
}

func (backend *daemonMCPBackend) savedQueries() (*application.SavedQueryService, error) {
	return application.NewSavedQueryService(savedquerystore.New(), backend.Client)
}

func (backend *daemonMCPBackend) ListSavedQueries(
	ctx context.Context,
	account domain.AccountID,
	caller domain.Caller,
) (application.SavedQueryCatalog, error) {
	if err := caller.Validate(); err != nil {
		return application.SavedQueryCatalog{}, err
	}
	service, err := backend.savedQueries()
	if err != nil {
		return application.SavedQueryCatalog{}, err
	}
	return service.List(ctx, account)
}

func (backend *daemonMCPBackend) GetSavedQuery(
	ctx context.Context,
	input application.SavedQueryDeleteInput,
	caller domain.Caller,
) (application.SavedQueryDefinition, error) {
	if err := caller.Validate(); err != nil {
		return application.SavedQueryDefinition{}, err
	}
	service, err := backend.savedQueries()
	if err != nil {
		return application.SavedQueryDefinition{}, err
	}
	return service.Get(ctx, input)
}

func (backend *daemonMCPBackend) RunSavedQuery(
	ctx context.Context,
	input application.SavedQueryRunInput,
	caller domain.Caller,
) (application.SavedQueryExecution, error) {
	service, err := backend.savedQueries()
	if err != nil {
		return application.SavedQueryExecution{}, err
	}
	return service.Run(ctx, input, caller)
}

func (backend *daemonMCPBackend) PreviewSavedQuerySave(
	ctx context.Context,
	input application.SavedQuerySaveInput,
	caller domain.Caller,
) (application.SavedQueryChangeAccess, error) {
	if err := caller.Validate(); err != nil {
		return application.SavedQueryChangeAccess{}, err
	}
	service, err := backend.savedQueries()
	if err != nil {
		return application.SavedQueryChangeAccess{}, err
	}
	review, err := service.ReviewSave(ctx, input)
	if err != nil {
		return application.SavedQueryChangeAccess{}, err
	}
	preview, err := backend.prepareSavedQueryOperation(
		ctx,
		"saved_query.save",
		domain.EffectReversibleWrite,
		review.Account,
		review,
		caller,
	)
	if err != nil {
		return application.SavedQueryChangeAccess{}, err
	}
	return application.SavedQueryChangeAccess{
		Status: "approval_required", Review: &review, Preview: preview,
	}, nil
}

func (backend *daemonMCPBackend) CommitSavedQuerySave(
	ctx context.Context,
	token string,
	caller domain.Caller,
) (application.SavedQueryChangeAccess, error) {
	operation, err := backend.guard.CommitFor(
		ctx,
		token,
		caller,
		"saved_query.save",
		domain.EffectReversibleWrite,
	)
	if err != nil {
		return application.SavedQueryChangeAccess{}, err
	}
	var review application.SavedQueryChangeReview
	if err := operation.DecodePayload(&review); err != nil {
		return application.SavedQueryChangeAccess{}, err
	}
	if review.Account != operation.Account() {
		return application.SavedQueryChangeAccess{}, errors.New(
			"approved saved query no longer matches its account",
		)
	}
	service, err := backend.savedQueries()
	if err != nil {
		return application.SavedQueryChangeAccess{}, err
	}
	query, executionErr := service.ApplySave(ctx, review)
	auditErr := backend.guard.RecordExecution(ctx, operation, caller, executionErr)
	if executionErr != nil || auditErr != nil {
		return application.SavedQueryChangeAccess{}, errors.Join(executionErr, auditErr)
	}
	return application.SavedQueryChangeAccess{
		Status: "completed", Query: &query,
	}, nil
}

func (backend *daemonMCPBackend) PreviewSavedQueryDelete(
	ctx context.Context,
	input application.SavedQueryDeleteInput,
	caller domain.Caller,
) (application.SavedQueryChangeAccess, error) {
	if err := caller.Validate(); err != nil {
		return application.SavedQueryChangeAccess{}, err
	}
	service, err := backend.savedQueries()
	if err != nil {
		return application.SavedQueryChangeAccess{}, err
	}
	review, err := service.ReviewDelete(ctx, input)
	if err != nil {
		return application.SavedQueryChangeAccess{}, err
	}
	preview, err := backend.prepareSavedQueryOperation(
		ctx,
		"saved_query.delete",
		domain.EffectDestructiveWrite,
		review.Account,
		review,
		caller,
	)
	if err != nil {
		return application.SavedQueryChangeAccess{}, err
	}
	return application.SavedQueryChangeAccess{
		Status: "approval_required", Review: &review, Preview: preview,
	}, nil
}

func (backend *daemonMCPBackend) CommitSavedQueryDelete(
	ctx context.Context,
	token string,
	caller domain.Caller,
) (application.SavedQueryChangeAccess, error) {
	operation, err := backend.guard.CommitFor(
		ctx,
		token,
		caller,
		"saved_query.delete",
		domain.EffectDestructiveWrite,
	)
	if err != nil {
		return application.SavedQueryChangeAccess{}, err
	}
	var review application.SavedQueryChangeReview
	if err := operation.DecodePayload(&review); err != nil {
		return application.SavedQueryChangeAccess{}, err
	}
	if review.Account != operation.Account() {
		return application.SavedQueryChangeAccess{}, errors.New(
			"approved saved query deletion no longer matches its account",
		)
	}
	service, err := backend.savedQueries()
	if err != nil {
		return application.SavedQueryChangeAccess{}, err
	}
	executionErr := service.ApplyDelete(ctx, review)
	auditErr := backend.guard.RecordExecution(ctx, operation, caller, executionErr)
	if executionErr != nil || auditErr != nil {
		return application.SavedQueryChangeAccess{}, errors.Join(executionErr, auditErr)
	}
	return application.SavedQueryChangeAccess{Status: "completed"}, nil
}

func (backend *daemonMCPBackend) PreviewSavedQueryPurge(
	ctx context.Context,
	input application.SavedQueryPurgeInput,
	caller domain.Caller,
) (application.SavedQueryPurgeAccess, error) {
	if err := caller.Validate(); err != nil {
		return application.SavedQueryPurgeAccess{}, err
	}
	service, err := backend.savedQueries()
	if err != nil {
		return application.SavedQueryPurgeAccess{}, err
	}
	review, err := service.ReviewPurge(ctx, input)
	if err != nil {
		return application.SavedQueryPurgeAccess{}, err
	}
	preview, err := backend.prepareSavedQueryOperation(
		ctx,
		"saved_query.purge",
		domain.EffectDestructiveWrite,
		review.Account,
		review,
		caller,
	)
	if err != nil {
		return application.SavedQueryPurgeAccess{}, err
	}
	return application.SavedQueryPurgeAccess{
		Status: "approval_required", Review: &review, Preview: preview,
	}, nil
}

func (backend *daemonMCPBackend) CommitSavedQueryPurge(
	ctx context.Context,
	token string,
	caller domain.Caller,
) (application.SavedQueryPurgeAccess, error) {
	operation, err := backend.guard.CommitFor(
		ctx,
		token,
		caller,
		"saved_query.purge",
		domain.EffectDestructiveWrite,
	)
	if err != nil {
		return application.SavedQueryPurgeAccess{}, err
	}
	var review application.SavedQueryPurgeReview
	if err := operation.DecodePayload(&review); err != nil {
		return application.SavedQueryPurgeAccess{}, err
	}
	if review.Account != operation.Account() {
		return application.SavedQueryPurgeAccess{}, errors.New(
			"approved saved query purge no longer matches its account",
		)
	}
	service, err := backend.savedQueries()
	if err != nil {
		return application.SavedQueryPurgeAccess{}, err
	}
	executionErr := service.ApplyPurge(ctx, review)
	auditErr := backend.guard.RecordExecution(ctx, operation, caller, executionErr)
	if executionErr != nil || auditErr != nil {
		return application.SavedQueryPurgeAccess{}, errors.Join(executionErr, auditErr)
	}
	return application.SavedQueryPurgeAccess{Status: "completed", Purged: true}, nil
}

func (backend *daemonMCPBackend) prepareSavedQueryOperation(
	ctx context.Context,
	name string,
	effect domain.Effect,
	account domain.AccountID,
	payload any,
	caller domain.Caller,
) (*approval.Preview, error) {
	operation, err := domain.NewOperation(name, effect, account, payload)
	if err != nil {
		return nil, err
	}
	preparation, err := backend.guard.Prepare(ctx, operation, caller)
	if err != nil {
		return nil, err
	}
	if preparation.Preview == nil {
		return nil, errors.New("saved query policy did not issue the required preview")
	}
	return preparation.Preview, nil
}
