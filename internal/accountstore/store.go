// Package accountstore adapts secret-free local configuration and state to the
// account lifecycle application ports.
package accountstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/paths"
)

// Store persists account definitions in one explicit config file.
type Store struct {
	ConfigPath               string
	DeleteOAuthAuthorization func(string) error
}

// ListAccounts returns a detached account catalog.
func (store Store) ListAccounts(context.Context) (application.AccountCatalog, error) {
	configuration, err := config.Load(store.ConfigPath)
	if err != nil {
		return application.AccountCatalog{}, err
	}
	accounts := make([]application.AccountView, 0, len(configuration.Accounts))
	for alias, account := range configuration.Accounts {
		accounts = append(accounts, application.AccountView{
			ID: account.ID, Alias: alias, Address: account.Address,
			Mail:      mailRouteView(account.Mail),
			Calendar:  calendarRouteView(account.Calendar),
			Tasks:     taskRouteView(account.Tasks),
			IsDefault: alias == configuration.DefaultAccount,
		})
	}
	return application.AccountCatalog{Accounts: accounts}, nil
}

func taskRouteView(route *config.TaskRoute) *application.AccountRouteView {
	if route == nil {
		return nil
	}
	if route.Provider == domain.ProviderMicrosoftGraph && route.MicrosoftGraph != nil {
		return oauthRouteView(route.Provider, &route.MicrosoftGraph.OAuth)
	}
	return application.TaskRouteView(route.Provider)
}

// ListCredentialBindings returns private handle ownership for application
// validation. It is never surfaced by account read views.
func (store Store) ListCredentialBindings(
	ctx context.Context,
) ([]application.AccountCredentialBinding, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	configuration, err := config.Load(store.ConfigPath)
	if err != nil {
		return nil, err
	}
	aliases := make([]string, 0, len(configuration.Accounts))
	for alias := range configuration.Accounts {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	var bindings []application.AccountCredentialBinding
	for _, alias := range aliases {
		account := configuration.Accounts[alias]
		for _, reference := range accountCredentialReferences(account) {
			bindings = append(bindings, application.AccountCredentialBinding{
				Account: account.ID,
				Backend: string(reference.Backend),
				Key:     reference.Key,
			})
		}
	}
	return bindings, nil
}

// AddAccount atomically rejects stale alias/ID conflicts before saving.
func (store Store) AddAccount(
	ctx context.Context,
	account application.AccountRegistration,
) error {
	return config.Update(ctx, store.ConfigPath, func(configuration *config.Config) error {
		if configuration.Accounts == nil {
			configuration.Accounts = make(map[string]config.Account)
		}
		if len(configuration.Accounts) >= 32 {
			return errors.New("at most 32 accounts are supported")
		}
		if _, exists := configuration.Accounts[account.Alias]; exists {
			return fmt.Errorf("account alias %q already exists", account.Alias)
		}
		for alias, existing := range configuration.Accounts {
			if existing.ID == account.ID {
				return fmt.Errorf("account ID already belongs to alias %q", alias)
			}
		}
		mail, err := mailRouteConfig(account.Mail)
		if err != nil {
			return err
		}
		calendar, err := calendarRouteConfig(account.Calendar)
		if err != nil {
			return err
		}
		tasks, err := taskRouteConfig(account.Tasks)
		if err != nil {
			return err
		}
		candidate := config.Account{
			ID: account.ID, Address: account.Address,
			Mail: mail, Calendar: calendar, Tasks: tasks,
		}
		for _, requested := range accountCredentialReferences(candidate) {
			for alias, existing := range configuration.Accounts {
				for _, reference := range accountCredentialReferences(existing) {
					if requested.Backend == reference.Backend &&
						requested.Key == reference.Key {
						return fmt.Errorf(
							"credential handle %q in backend %q already belongs to account %q",
							requested.Key,
							requested.Backend,
							alias,
						)
					}
				}
			}
		}
		configuration.Accounts[account.Alias] = candidate
		if account.IsDefault {
			configuration.DefaultAccount = account.Alias
		}
		return nil
	})
}

func taskRouteConfig(
	route *application.AccountTaskRouteInput,
) (*config.TaskRoute, error) {
	if route == nil {
		return nil, nil
	}
	if err := route.Provider.Validate(); err != nil {
		return nil, err
	}
	result := &config.TaskRoute{Provider: route.Provider}
	if route.MicrosoftGraph != nil {
		oauth, err := oauthRouteConfig(&route.MicrosoftGraph.OAuth)
		if err != nil {
			return nil, err
		}
		result.MicrosoftGraph = &config.MicrosoftGraphTaskRoute{
			OAuth: *oauth, ReadOnly: route.MicrosoftGraph.ReadOnly,
		}
	}
	return result, nil
}

func accountCredentialReferences(account config.Account) []config.CredentialRef {
	references := make([]config.CredentialRef, 0, 2)
	if account.Mail != nil {
		switch account.Mail.Provider {
		case domain.ProviderJMAP:
			if account.Mail.JMAP != nil {
				references = append(references, account.Mail.JMAP.Credential)
			}
		case domain.ProviderIMAPSMTP:
			if account.Mail.IMAPSMTP != nil {
				references = append(references, account.Mail.IMAPSMTP.Credential)
			}
		case domain.ProviderGoogle:
			if account.Mail.Google != nil {
				references = append(references, account.Mail.Google.Authorization)
			}
		case domain.ProviderMicrosoftGraph:
			if account.Mail.MicrosoftGraph != nil {
				references = append(
					references,
					account.Mail.MicrosoftGraph.Authorization,
				)
			}
		case domain.ProviderMicrosoftOWA, domain.ProviderGoogleWeb,
			domain.ProviderCalDAV, domain.ProviderPOP3,
			domain.ProviderMicrosoftTasks, domain.ProviderTodoist,
			domain.ProviderGoogleTasks, domain.ProviderAppleReminders,
			domain.ProviderTickTick, domain.ProviderAnyDoMCP,
			domain.ProviderThings, domain.ProviderOmniFocus:
		}
	}
	if account.Calendar != nil {
		switch account.Calendar.Provider {
		case domain.ProviderCalDAV:
			if account.Calendar.CalDAV != nil {
				references = append(references, account.Calendar.CalDAV.Credential)
			}
		case domain.ProviderGoogle:
			if account.Calendar.Google != nil {
				references = append(
					references,
					account.Calendar.Google.Authorization,
				)
			}
		case domain.ProviderMicrosoftGraph:
			if account.Calendar.MicrosoftGraph != nil {
				references = append(
					references,
					account.Calendar.MicrosoftGraph.Authorization,
				)
			}
		case domain.ProviderMicrosoftOWA, domain.ProviderGoogleWeb,
			domain.ProviderJMAP, domain.ProviderIMAPSMTP, domain.ProviderPOP3,
			domain.ProviderMicrosoftTasks, domain.ProviderTodoist,
			domain.ProviderGoogleTasks, domain.ProviderAppleReminders,
			domain.ProviderTickTick, domain.ProviderAnyDoMCP,
			domain.ProviderThings, domain.ProviderOmniFocus:
		}
	}
	if account.Tasks != nil && account.Tasks.MicrosoftGraph != nil {
		references = append(
			references,
			account.Tasks.MicrosoftGraph.OAuth.Authorization,
		)
	}
	return references
}

func mailRouteView(route *config.MailRoute) *application.AccountRouteView {
	if route == nil {
		return nil
	}
	switch route.Provider {
	case domain.ProviderMicrosoftOWA:
		return outlookWebRouteView(route.Provider, route.OutlookWeb)
	case domain.ProviderJMAP:
		if route.JMAP == nil {
			return &application.AccountRouteView{Provider: route.Provider}
		}
		return &application.AccountRouteView{
			Provider: route.Provider,
			Endpoints: []application.DiscoveredEndpoint{
				{Kind: "session", Value: route.JMAP.SessionURL},
			},
			Identity: route.JMAP.Username,
			Credential: &application.AccountCredentialView{
				Configured: true,
				Backend:    string(route.JMAP.Credential.Backend),
				Consented:  route.JMAP.Credential.Consent,
			},
		}
	case domain.ProviderIMAPSMTP:
		if route.IMAPSMTP == nil {
			return &application.AccountRouteView{Provider: route.Provider}
		}
		return &application.AccountRouteView{
			Provider: route.Provider,
			Endpoints: []application.DiscoveredEndpoint{
				{
					Kind: "imap",
					Value: fmt.Sprintf(
						"%s://%s:%d",
						route.IMAPSMTP.IMAP.Mode,
						route.IMAPSMTP.IMAP.Host,
						route.IMAPSMTP.IMAP.Port,
					),
				},
				{
					Kind: "smtp",
					Value: fmt.Sprintf(
						"%s://%s:%d",
						route.IMAPSMTP.SMTP.Mode,
						route.IMAPSMTP.SMTP.Host,
						route.IMAPSMTP.SMTP.Port,
					),
				},
			},
			Identity: route.IMAPSMTP.Username,
			Credential: &application.AccountCredentialView{
				Configured: true,
				Backend:    string(route.IMAPSMTP.Credential.Backend),
				Consented:  route.IMAPSMTP.Credential.Consent,
			},
		}
	case domain.ProviderGoogle:
		return googleMailRouteView(route.Provider, route.Google)
	case domain.ProviderGoogleWeb:
		return webRouteView(route.Provider, route.GoogleWeb)
	case domain.ProviderMicrosoftGraph:
		return oauthRouteView(route.Provider, route.MicrosoftGraph)
	case
		domain.ProviderCalDAV,
		domain.ProviderMicrosoftTasks,
		domain.ProviderTodoist,
		domain.ProviderGoogleTasks,
		domain.ProviderAppleReminders,
		domain.ProviderTickTick,
		domain.ProviderAnyDoMCP,
		domain.ProviderThings,
		domain.ProviderOmniFocus,
		domain.ProviderPOP3:
		return &application.AccountRouteView{
			Provider: route.Provider,
			Endpoints: []application.DiscoveredEndpoint{
				{Kind: "configured", Value: "provider-specific"},
			},
		}
	default:
		return &application.AccountRouteView{Provider: route.Provider}
	}
}

func calendarRouteView(route *config.CalendarRoute) *application.AccountRouteView {
	if route == nil {
		return nil
	}
	switch route.Provider {
	case domain.ProviderMicrosoftOWA:
		return outlookWebRouteView(route.Provider, route.OutlookWeb)
	case domain.ProviderCalDAV:
		if route.CalDAV == nil {
			return &application.AccountRouteView{Provider: route.Provider}
		}
		endpoints := []application.DiscoveredEndpoint{
			{Kind: "endpoint", Value: route.CalDAV.Endpoint},
		}
		if route.CalDAV.CalendarPath != "" {
			endpoints = append(endpoints, application.DiscoveredEndpoint{
				Kind: "calendar", Value: route.CalDAV.CalendarPath,
			})
		}
		return &application.AccountRouteView{
			Provider: route.Provider, Endpoints: endpoints,
			Identity: route.CalDAV.Username,
			Credential: &application.AccountCredentialView{
				Configured: true,
				Backend:    string(route.CalDAV.Credential.Backend),
				Consented:  route.CalDAV.Credential.Consent,
			},
		}
	case domain.ProviderGoogle:
		return oauthRouteView(route.Provider, route.Google)
	case domain.ProviderGoogleWeb:
		return webRouteView(route.Provider, route.GoogleWeb)
	case domain.ProviderMicrosoftGraph:
		return oauthRouteView(route.Provider, route.MicrosoftGraph)
	case
		domain.ProviderJMAP,
		domain.ProviderIMAPSMTP,
		domain.ProviderMicrosoftTasks,
		domain.ProviderTodoist,
		domain.ProviderGoogleTasks,
		domain.ProviderAppleReminders,
		domain.ProviderTickTick,
		domain.ProviderAnyDoMCP,
		domain.ProviderThings,
		domain.ProviderOmniFocus,
		domain.ProviderPOP3:
		return &application.AccountRouteView{
			Provider: route.Provider,
			Endpoints: []application.DiscoveredEndpoint{
				{Kind: "configured", Value: "provider-specific"},
			},
		}
	default:
		return &application.AccountRouteView{Provider: route.Provider}
	}
}

func oauthRouteView(
	provider domain.ProviderID,
	route *config.OAuthRoute,
) *application.AccountRouteView {
	if route == nil {
		return &application.AccountRouteView{Provider: provider}
	}
	return &application.AccountRouteView{
		Provider: provider,
		Endpoints: []application.DiscoveredEndpoint{
			{Kind: "api", Value: route.APIBase},
		},
		Credential: &application.AccountCredentialView{
			Configured: true,
			Backend:    string(route.Authorization.Backend),
			Consented:  route.Authorization.Consent,
		},
	}
}

func googleMailRouteView(
	provider domain.ProviderID,
	route *config.GoogleMailRoute,
) *application.AccountRouteView {
	if route == nil {
		return &application.AccountRouteView{Provider: provider}
	}
	return &application.AccountRouteView{
		Provider: provider,
		Endpoints: []application.DiscoveredEndpoint{
			{Kind: "imap", Value: "implicit://imap.gmail.com:993"},
			{Kind: "smtp", Value: "starttls://smtp.gmail.com:587"},
		},
		Identity: route.Username,
		Credential: &application.AccountCredentialView{
			Configured: true,
			Backend:    string(route.Authorization.Backend),
			Consented:  route.Authorization.Consent,
		},
	}
}

func webRouteView(
	provider domain.ProviderID,
	route *config.WebRoute,
) *application.AccountRouteView {
	if route == nil {
		return &application.AccountRouteView{Provider: provider}
	}
	return &application.AccountRouteView{
		Provider: provider,
		Endpoints: []application.DiscoveredEndpoint{
			{Kind: "origin", Value: route.Origin},
		},
	}
}

func outlookWebRouteView(
	provider domain.ProviderID,
	route *config.OutlookWebRoute,
) *application.AccountRouteView {
	if route == nil {
		return &application.AccountRouteView{Provider: provider}
	}
	return &application.AccountRouteView{
		Provider: provider,
		Endpoints: []application.DiscoveredEndpoint{
			{Kind: "origin", Value: route.Origin},
		},
		Identity: route.Mailbox,
	}
}

func mailRouteConfig(
	route *application.AccountMailRouteInput,
) (*config.MailRoute, error) {
	if route == nil {
		return nil, nil
	}
	switch route.Provider {
	case domain.ProviderMicrosoftOWA:
		if route.OutlookWeb == nil {
			return nil, errors.New("outlook web mail settings are missing")
		}
		return &config.MailRoute{
			Provider: route.Provider,
			OutlookWeb: &config.OutlookWebRoute{
				Origin: route.OutlookWeb.Origin, Mailbox: route.OutlookWeb.Mailbox,
			},
		}, nil
	case domain.ProviderJMAP:
		if route.JMAP == nil {
			return nil, errors.New("JMAP mail settings are missing")
		}
		return &config.MailRoute{
			Provider: route.Provider,
			JMAP: &config.JMAPRoute{
				SessionURL: route.JMAP.SessionURL,
				Username:   route.JMAP.Username,
				Credential: config.CredentialRef{
					Backend: config.CredentialBackend(route.JMAP.Credential.Backend),
					Key:     route.JMAP.Credential.Key,
					Consent: route.JMAP.Credential.Consent,
				},
			},
		}, nil
	case domain.ProviderIMAPSMTP:
		if route.IMAPSMTP == nil {
			return nil, errors.New("IMAP/SMTP mail settings are missing")
		}
		return &config.MailRoute{
			Provider: route.Provider,
			IMAPSMTP: &config.IMAPSMTPRoute{
				IMAP: config.TLSEndpoint{
					Host: route.IMAPSMTP.IMAP.Host,
					Port: route.IMAPSMTP.IMAP.Port,
					Mode: config.TLSMode(route.IMAPSMTP.IMAP.Mode),
				},
				SMTP: config.TLSEndpoint{
					Host: route.IMAPSMTP.SMTP.Host,
					Port: route.IMAPSMTP.SMTP.Port,
					Mode: config.TLSMode(route.IMAPSMTP.SMTP.Mode),
				},
				Username: route.IMAPSMTP.Username,
				Mailbox:  route.IMAPSMTP.Mailbox,
				Credential: config.CredentialRef{
					Backend: config.CredentialBackend(route.IMAPSMTP.Credential.Backend),
					Key:     route.IMAPSMTP.Credential.Key,
					Consent: route.IMAPSMTP.Credential.Consent,
				},
			},
		}, nil
	case domain.ProviderGoogle:
		if route.Google == nil {
			return nil, errors.New("google mail settings are missing")
		}
		return &config.MailRoute{
			Provider: route.Provider,
			Google: &config.GoogleMailRoute{
				Username:    route.Google.Username,
				Mailbox:     route.Google.Mailbox,
				ClientID:    route.Google.ClientID,
				RedirectURI: route.Google.RedirectURI,
				Authorization: config.CredentialRef{
					Backend: config.CredentialBackend(
						route.Google.Authorization.Backend,
					),
					Key:     route.Google.Authorization.Key,
					Consent: route.Google.Authorization.Consent,
				},
			},
		}, nil
	case domain.ProviderGoogleWeb:
		if route.GoogleWeb == nil {
			return nil, errors.New("google Web mail settings are missing")
		}
		return &config.MailRoute{
			Provider:  route.Provider,
			GoogleWeb: &config.WebRoute{Origin: route.GoogleWeb.Origin},
		}, nil
	case domain.ProviderMicrosoftGraph:
		oauth, err := oauthRouteConfig(route.MicrosoftGraph)
		if err != nil {
			return nil, err
		}
		return &config.MailRoute{
			Provider: route.Provider, MicrosoftGraph: oauth,
		}, nil
	case
		domain.ProviderCalDAV,
		domain.ProviderMicrosoftTasks,
		domain.ProviderTodoist,
		domain.ProviderGoogleTasks,
		domain.ProviderAppleReminders,
		domain.ProviderTickTick,
		domain.ProviderAnyDoMCP,
		domain.ProviderThings,
		domain.ProviderOmniFocus,
		domain.ProviderPOP3:
		return nil, fmt.Errorf("mail provider %q is not accepted by account add", route.Provider)
	default:
		return nil, fmt.Errorf("unknown mail provider %q", route.Provider)
	}
}

func calendarRouteConfig(
	route *application.AccountCalendarRouteInput,
) (*config.CalendarRoute, error) {
	if route == nil {
		return nil, nil
	}
	switch route.Provider {
	case domain.ProviderMicrosoftOWA:
		if route.OutlookWeb == nil {
			return nil, errors.New("outlook Web calendar settings are missing")
		}
		return &config.CalendarRoute{
			Provider: route.Provider,
			OutlookWeb: &config.OutlookWebRoute{
				Origin: route.OutlookWeb.Origin, Mailbox: route.OutlookWeb.Mailbox,
			},
		}, nil
	case domain.ProviderCalDAV:
		if route.CalDAV == nil {
			return nil, errors.New("CalDAV calendar settings are missing")
		}
		return &config.CalendarRoute{
			Provider: route.Provider,
			CalDAV: &config.CalDAVRoute{
				Endpoint: route.CalDAV.Endpoint, CalendarPath: route.CalDAV.CalendarPath,
				Username: route.CalDAV.Username,
				Credential: config.CredentialRef{
					Backend: config.CredentialBackend(route.CalDAV.Credential.Backend),
					Key:     route.CalDAV.Credential.Key,
					Consent: route.CalDAV.Credential.Consent,
				},
			},
		}, nil
	case domain.ProviderGoogle:
		oauth, err := oauthRouteConfig(route.Google)
		if err != nil {
			return nil, err
		}
		return &config.CalendarRoute{Provider: route.Provider, Google: oauth}, nil
	case domain.ProviderGoogleWeb:
		if route.GoogleWeb == nil {
			return nil, errors.New("google Web calendar settings are missing")
		}
		return &config.CalendarRoute{
			Provider:  route.Provider,
			GoogleWeb: &config.WebRoute{Origin: route.GoogleWeb.Origin},
		}, nil
	case domain.ProviderMicrosoftGraph:
		oauth, err := oauthRouteConfig(route.MicrosoftGraph)
		if err != nil {
			return nil, err
		}
		return &config.CalendarRoute{
			Provider: route.Provider, MicrosoftGraph: oauth,
		}, nil
	case
		domain.ProviderJMAP,
		domain.ProviderIMAPSMTP,
		domain.ProviderMicrosoftTasks,
		domain.ProviderTodoist,
		domain.ProviderGoogleTasks,
		domain.ProviderAppleReminders,
		domain.ProviderTickTick,
		domain.ProviderAnyDoMCP,
		domain.ProviderThings,
		domain.ProviderOmniFocus,
		domain.ProviderPOP3:
		return nil, fmt.Errorf(
			"calendar provider %q is not accepted by account add",
			route.Provider,
		)
	default:
		return nil, fmt.Errorf(
			"calendar provider %q is not accepted by account add",
			route.Provider,
		)
	}
}

func oauthRouteConfig(
	route *application.AccountOAuthInput,
) (*config.OAuthRoute, error) {
	if route == nil {
		return nil, errors.New("OAuth route settings are missing")
	}
	return &config.OAuthRoute{
		APIBase: route.APIBase, MicrosoftCloud: route.MicrosoftCloud,
		ClientID:    route.ClientID,
		RedirectURI: route.RedirectURI,
		Authorization: config.CredentialRef{
			Backend: config.CredentialBackend(route.Authorization.Backend),
			Key:     route.Authorization.Key,
			Consent: route.Authorization.Consent,
		},
	}, nil
}

// RenameAccount atomically changes only the mutable alias.
func (store Store) RenameAccount(
	ctx context.Context,
	accountID domain.AccountID,
	newAlias string,
) error {
	return config.Update(ctx, store.ConfigPath, func(configuration *config.Config) error {
		if _, exists := configuration.Accounts[newAlias]; exists {
			return fmt.Errorf("account alias %q already exists", newAlias)
		}
		oldAlias, account, exists := configuration.AccountByID(accountID)
		if !exists {
			return fmt.Errorf("account ID %q is not configured", accountID)
		}
		delete(configuration.Accounts, oldAlias)
		configuration.Accounts[newAlias] = account
		if configuration.DefaultAccount == oldAlias {
			configuration.DefaultAccount = newAlias
		}
		return nil
	})
}

// RemoveAccount atomically deletes one route and updates the default.
func (store Store) RemoveAccount(
	ctx context.Context,
	accountID domain.AccountID,
	replacementDefault domain.AccountID,
) error {
	return config.Update(ctx, store.ConfigPath, func(configuration *config.Config) error {
		alias, _, exists := configuration.AccountByID(accountID)
		if !exists {
			return fmt.Errorf("account ID %q is not configured", accountID)
		}
		if len(configuration.Accounts) == 1 {
			return errors.New("cannot remove the only configured account")
		}
		if alias == configuration.DefaultAccount {
			if replacementDefault == "" || replacementDefault == accountID {
				return errors.New("removing the default account requires a different replacement")
			}
			replacementAlias, _, exists := configuration.AccountByID(replacementDefault)
			if !exists {
				return fmt.Errorf(
					"replacement default account ID %q is not configured",
					replacementDefault,
				)
			}
			configuration.DefaultAccount = replacementAlias
		} else if replacementDefault != "" {
			return errors.New("replacement default is valid only for the default account")
		}
		delete(configuration.Accounts, alias)
		return nil
	})
}

// PurgeAccountState removes only Corresync-owned state derived from the stable
// opaque account ID. It refuses symlinked roots rather than following them.
func (store Store) PurgeAccountState(
	ctx context.Context,
	accountID domain.AccountID,
) error {
	if err := accountID.ValidateOpaque(); err != nil {
		return err
	}
	profile, err := paths.ProfileDir(accountID)
	if err != nil {
		return err
	}
	accountState, err := paths.AccountStateDir(accountID)
	if err != nil {
		return err
	}
	for _, target := range []string{profile, accountState} {
		if err := removeOwnedTree(target); err != nil {
			return err
		}
	}
	if store.ConfigPath == "" || store.DeleteOAuthAuthorization == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	configuration, err := config.Load(store.ConfigPath)
	if err != nil {
		return fmt.Errorf("load OAuth authorization ownership: %w", err)
	}
	_, account, exists := configuration.AccountByID(accountID)
	if !exists {
		return fmt.Errorf("account ID %q is not configured", accountID)
	}
	targetKeys := accountOAuthAuthorizationKeys(account)
	sharedKeys := make(map[string]struct{})
	for _, other := range configuration.Accounts {
		if other.ID == accountID {
			continue
		}
		for _, key := range accountOAuthAuthorizationKeys(other) {
			sharedKeys[key] = struct{}{}
		}
	}
	for _, key := range targetKeys {
		if _, shared := sharedKeys[key]; shared {
			continue
		}
		if err := store.DeleteOAuthAuthorization(key); err != nil {
			return fmt.Errorf("delete account OAuth authorization: %w", err)
		}
	}
	return nil
}

func accountOAuthAuthorizationKeys(account config.Account) []string {
	keys := make([]string, 0, 3)
	if account.Mail != nil {
		if account.Mail.Google != nil {
			keys = append(keys, account.Mail.Google.Authorization.Key)
		}
		if account.Mail.MicrosoftGraph != nil {
			keys = append(keys, account.Mail.MicrosoftGraph.Authorization.Key)
		}
	}
	if account.Calendar != nil {
		for _, route := range []*config.OAuthRoute{
			account.Calendar.Google,
			account.Calendar.MicrosoftGraph,
		} {
			if route != nil {
				keys = append(keys, route.Authorization.Key)
			}
		}
	}
	if account.Tasks != nil && account.Tasks.MicrosoftGraph != nil {
		keys = append(keys, account.Tasks.MicrosoftGraph.OAuth.Authorization.Key)
	}
	slices.Sort(keys)
	return slices.Compact(keys)
}

func removeOwnedTree(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect account state: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("account state path %q is not a directory", path)
	}
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("remove account state: %w", err)
	}
	return nil
}
