package main

import (
	"fmt"
	"strings"

	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/oauthlocal"
)

type oauthConsentRoute struct {
	provider domain.ProviderID
	route    config.OAuthRoute
	mail     bool
	calendar bool
}

func accountOAuthConsents(account config.Account) ([]oauthConsentRoute, error) {
	routes := make([]oauthConsentRoute, 0, 2)
	add := func(
		provider domain.ProviderID,
		route *config.OAuthRoute,
		mail, calendar bool,
	) {
		if route == nil {
			return
		}
		for index := range routes {
			existing := &routes[index]
			if existing.provider == provider &&
				oauthRoutesEqual(existing.route, *route) {
				existing.mail = existing.mail || mail
				existing.calendar = existing.calendar || calendar
				return
			}
		}
		routes = append(routes, oauthConsentRoute{
			provider: provider, route: *route,
			mail: mail, calendar: calendar,
		})
	}
	if account.Mail != nil {
		switch account.Mail.Provider {
		case domain.ProviderGoogleAPI:
			add(account.Mail.Provider, account.Mail.GoogleAPI, true, false)
		case domain.ProviderMicrosoftGraph:
			add(account.Mail.Provider, account.Mail.MicrosoftGraph, true, false)
		case domain.ProviderMicrosoftOWA, domain.ProviderGoogleWeb,
			domain.ProviderJMAP, domain.ProviderIMAPSMTP,
			domain.ProviderCalDAV, domain.ProviderPOP3:
		}
	}
	if account.Calendar != nil {
		switch account.Calendar.Provider {
		case domain.ProviderGoogleAPI:
			add(account.Calendar.Provider, account.Calendar.GoogleAPI, false, true)
		case domain.ProviderMicrosoftGraph:
			add(account.Calendar.Provider, account.Calendar.MicrosoftGraph, false, true)
		case domain.ProviderMicrosoftOWA, domain.ProviderGoogleWeb,
			domain.ProviderJMAP, domain.ProviderIMAPSMTP,
			domain.ProviderCalDAV, domain.ProviderPOP3:
		}
	}
	for _, route := range routes {
		if _, err := oauthlocal.ProviderFor(
			route.provider,
			route.mail,
			route.calendar,
		); err != nil {
			return nil, err
		}
	}
	return routes, nil
}

func writeOAuthConsentNotice(app *runtime, account config.Account) error {
	routes, err := accountOAuthConsents(account)
	if err != nil || len(routes) == 0 {
		return err
	}
	view := newConsoleView(app, app.stderr, app.interactiveOutput())
	if _, err := view.printf(
		"%s  %s\n",
		view.warning(),
		view.strong("OAuth consent review"),
	); err != nil {
		return err
	}
	for _, route := range routes {
		provider, err := oauthlocal.ProviderFor(
			route.provider,
			route.mail,
			route.calendar,
		)
		if err != nil {
			return err
		}
		if _, err := view.printf(
			"   %s: %s\n",
			route.provider,
			sanitizeCell(strings.Join(provider.Scopes, ", "), 1024),
		); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintln(
		app.stderr,
		"   A provider page opens only when no matching valid local grant is stored.",
	)
	return err
}
