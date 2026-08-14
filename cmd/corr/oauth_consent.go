package main

import (
	"fmt"
	"strings"

	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/microsoftcloud"
	"github.com/nkiyohara/corresync/internal/oauthlocal"
)

const (
	privacyPolicyURL = "https://corresync.org/privacy.html"
	termsOfUseURL    = "https://corresync.org/terms.html"
)

type oauthConsentRoute struct {
	provider domain.ProviderID
	route    config.OAuthClient
	services oauthlocal.Services
}

func oauthConsentLabel(route oauthConsentRoute) (string, error) {
	label := string(route.provider)
	if route.provider != domain.ProviderMicrosoftGraph {
		return label, nil
	}
	cloud, err := microsoftcloud.Resolve(route.services.MicrosoftCloud)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s (%s; %s)", label, cloud.ID, cloud.APIBase), nil
}

func accountOAuthConsents(account config.Account) ([]oauthConsentRoute, error) {
	routes := make([]oauthConsentRoute, 0, 3)
	add := func(
		provider domain.ProviderID,
		route *config.OAuthClient,
		services oauthlocal.Services,
	) {
		if route == nil {
			return
		}
		for index := range routes {
			existing := &routes[index]
			sameCloud := provider != domain.ProviderMicrosoftGraph ||
				microsoftcloud.Equivalent(
					existing.services.MicrosoftCloud,
					services.MicrosoftCloud,
				)
			if existing.provider == provider &&
				oauthClientsEqual(existing.route, *route) &&
				sameCloud {
				existing.services.Mail = existing.services.Mail || services.Mail
				existing.services.Calendar = existing.services.Calendar || services.Calendar
				existing.services.Tasks = existing.services.Tasks || services.Tasks
				existing.services.TaskWrite = existing.services.TaskWrite || services.TaskWrite
				return
			}
		}
		routes = append(routes, oauthConsentRoute{
			provider: provider, route: *route,
			services: services,
		})
	}
	if account.Mail != nil {
		switch account.Mail.Provider {
		case domain.ProviderGoogle:
			if account.Mail.Google != nil {
				client := account.Mail.Google.Client()
				add(account.Mail.Provider, &client, oauthlocal.Services{Mail: true})
			}
		case domain.ProviderMicrosoftGraph:
			if account.Mail.MicrosoftGraph != nil {
				client := account.Mail.MicrosoftGraph.Client()
				add(account.Mail.Provider, &client, oauthlocal.Services{
					Mail: true, MicrosoftCloud: account.Mail.MicrosoftGraph.MicrosoftCloud,
				})
			}
		case domain.ProviderMicrosoftOWA, domain.ProviderGoogleWeb,
			domain.ProviderJMAP, domain.ProviderIMAPSMTP,
			domain.ProviderCalDAV, domain.ProviderPOP3,
			domain.ProviderMicrosoftTasks, domain.ProviderTodoist,
			domain.ProviderGoogleTasks, domain.ProviderAppleReminders,
			domain.ProviderTickTick, domain.ProviderAnyDoMCP,
			domain.ProviderThings, domain.ProviderOmniFocus:
		}
	}
	if account.Calendar != nil {
		switch account.Calendar.Provider {
		case domain.ProviderGoogle:
			if account.Calendar.Google != nil {
				client := account.Calendar.Google.Client()
				add(account.Calendar.Provider, &client, oauthlocal.Services{Calendar: true})
			}
		case domain.ProviderMicrosoftGraph:
			if account.Calendar.MicrosoftGraph != nil {
				client := account.Calendar.MicrosoftGraph.Client()
				add(account.Calendar.Provider, &client, oauthlocal.Services{
					Calendar: true, MicrosoftCloud: account.Calendar.MicrosoftGraph.MicrosoftCloud,
				})
			}
		case domain.ProviderMicrosoftOWA, domain.ProviderGoogleWeb,
			domain.ProviderJMAP, domain.ProviderIMAPSMTP,
			domain.ProviderCalDAV, domain.ProviderPOP3,
			domain.ProviderMicrosoftTasks, domain.ProviderTodoist,
			domain.ProviderGoogleTasks, domain.ProviderAppleReminders,
			domain.ProviderTickTick, domain.ProviderAnyDoMCP,
			domain.ProviderThings, domain.ProviderOmniFocus:
		}
	}
	if account.Tasks != nil && account.Tasks.Provider == domain.ProviderMicrosoftGraph &&
		account.Tasks.MicrosoftGraph != nil {
		client := account.Tasks.MicrosoftGraph.OAuth.Client()
		add(account.Tasks.Provider, &client, oauthlocal.Services{
			Tasks: true, TaskWrite: !account.Tasks.MicrosoftGraph.ReadOnly,
			MicrosoftCloud: account.Tasks.MicrosoftGraph.OAuth.MicrosoftCloud,
		})
	}
	if account.Tasks != nil && account.Tasks.Provider == domain.ProviderTodoist &&
		account.Tasks.Todoist != nil {
		client := account.Tasks.Todoist.OAuth.Client()
		add(account.Tasks.Provider, &client, oauthlocal.Services{
			Tasks: true, TaskWrite: !account.Tasks.Todoist.ReadOnly,
		})
	}
	if account.Tasks != nil && account.Tasks.Provider == domain.ProviderGoogleTasks &&
		account.Tasks.GoogleTasks != nil {
		client := account.Tasks.GoogleTasks.OAuth.Client()
		add(account.Tasks.Provider, &client, oauthlocal.Services{
			Tasks: true, TaskWrite: !account.Tasks.GoogleTasks.ReadOnly,
		})
	}
	for _, route := range routes {
		if _, err := oauthlocal.ProviderFor(
			route.provider,
			route.services,
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
			route.services,
		)
		if err != nil {
			return err
		}
		label, err := oauthConsentLabel(route)
		if err != nil {
			return err
		}
		if _, err := view.printf(
			"   %s: %s\n",
			label,
			sanitizeCell(strings.Join(provider.Scopes, ", "), 1024),
		); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintln(
		app.stderr,
		"   A provider page opens only when no matching valid local grant is stored.\n"+
			"   Privacy: "+privacyPolicyURL+"\n"+
			"   Terms: "+termsOfUseURL,
	)
	return err
}
