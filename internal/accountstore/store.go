// Package accountstore adapts secret-free local configuration and state to the
// account lifecycle application ports.
package accountstore

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/paths"
)

// Store persists account definitions in one explicit config file.
type Store struct {
	ConfigPath string
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
			IsDefault: alias == configuration.DefaultAccount,
		})
	}
	return application.AccountCatalog{Accounts: accounts}, nil
}

// AddAccount atomically rejects stale alias/ID conflicts before saving.
func (store Store) AddAccount(
	ctx context.Context,
	account application.AccountRegistration,
) error {
	return config.Update(ctx, store.ConfigPath, func(configuration *config.Config) error {
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
		configuration.Accounts[account.Alias] = config.Account{
			ID: account.ID, Address: account.Address,
			Mail: mail, Calendar: calendar,
		}
		if account.IsDefault {
			configuration.DefaultAccount = account.Alias
		}
		return nil
	})
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
	case domain.ProviderMicrosoftGraph,
		domain.ProviderGoogleAPI,
		domain.ProviderGoogleWeb,
		domain.ProviderCalDAV,
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
	case domain.ProviderMicrosoftGraph,
		domain.ProviderGoogleAPI,
		domain.ProviderGoogleWeb,
		domain.ProviderJMAP,
		domain.ProviderIMAPSMTP,
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
	case domain.ProviderMicrosoftGraph,
		domain.ProviderGoogleAPI,
		domain.ProviderGoogleWeb,
		domain.ProviderCalDAV,
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
	case domain.ProviderMicrosoftGraph,
		domain.ProviderGoogleAPI,
		domain.ProviderGoogleWeb,
		domain.ProviderJMAP,
		domain.ProviderIMAPSMTP,
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
	replacementDefault string,
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
			if replacementDefault == "" || replacementDefault == alias {
				return errors.New("removing the default account requires a different replacement")
			}
			if _, exists := configuration.Accounts[replacementDefault]; !exists {
				return fmt.Errorf(
					"replacement default account %q is not configured",
					replacementDefault,
				)
			}
			configuration.DefaultAccount = replacementDefault
		} else if replacementDefault != "" {
			return errors.New("replacement default is valid only for the default account")
		}
		delete(configuration.Accounts, alias)
		return nil
	})
}

// PurgeAccountState removes only Corresync-owned state derived from the stable
// opaque account ID. It refuses symlinked roots rather than following them.
func (Store) PurgeAccountState(_ context.Context, accountID domain.AccountID) error {
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
	return nil
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
