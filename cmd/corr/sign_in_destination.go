package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"charm.land/huh/v2"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/domain"
)

// signInVerifier asks the local daemon whether an account's current
// connection settings already completed a sign-in started from the CLI.
type signInVerifier interface {
	SignInVerified(context.Context, domain.AccountID, domain.Caller) (bool, error)
}

// confirmSignInDestination asks, on an interactive terminal, whether to go
// ahead with a first sign-in at the named hosts. The answer defaults to No.
// Tests replace it.
var confirmSignInDestination = func(app *runtime, alias string, hosts []string) (bool, error) {
	confirmed := false
	restoreFallback := prepareAccessibleFieldFallback(app, "n\n")
	defer restoreFallback()
	title := fmt.Sprintf("Sign in to %s?", alias)
	if len(hosts) > 0 {
		title = fmt.Sprintf("Sign in to %s at %s?", alias, strings.Join(hosts, ", "))
	}
	form := settingsForm(app, huh.NewConfirm().
		Title(title).
		Description("Choose Sign in only if this is the page you expect. Esc cancels.").
		Affirmative("Sign in").
		Negative("Cancel").
		Value(&confirmed))
	if err := form.RunWithContext(app.context); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return false, nil
		}
		return false, fmt.Errorf("confirm sign-in: %w", err)
	}
	if err := app.context.Err(); err != nil {
		return false, err
	}
	if settingsInputExhausted(app) {
		return false, nil
	}
	return confirmed, nil
}

// signInFirstTimeNotice is printed when the account's current connection
// settings were never signed in to from the CLI.
const signInFirstTimeNotice = "This is the first sign-in with these connection settings. Check that the page above is the one you expect."

// reviewSignInDestination prints where `corr auth login` will sign in, from the
// same source as the MCP approval prompt. When the current settings were
// never signed in to from the CLI and canPrompt is true, it asks for
// confirmation and returns an error on No, before anything opens.
func reviewSignInDestination(
	app *runtime,
	verifier signInVerifier,
	alias string,
	configured config.Account,
	canPrompt bool,
) error {
	routes, routeErr := signInDestinations(configured)
	hosts, err := writeSignInDestination(app.stderr, app.textWidth(app.stderr), alias, configured, routes, routeErr)
	if err != nil {
		return err
	}
	verified, verifyErr := verifier.SignInVerified(app.context, configured.ID, app.caller())
	if verifyErr == nil && verified {
		return nil
	}
	if _, err := fmt.Fprintln(app.stderr, wrapText(signInFirstTimeNotice, app.textWidth(app.stderr), "")); err != nil {
		return err
	}
	if !canPrompt {
		return nil
	}
	confirmed, err := confirmSignInDestination(app, sanitizeCell(alias, 64), hosts)
	if err != nil {
		return err
	}
	if !confirmed {
		return withHint(
			fmt.Errorf("did not sign in to %s; nothing was opened", alias),
			shellCommand("corr", "account", "show", alias),
		)
	}
	return nil
}

// writeSignInDestination writes the destination notice and returns the hosts
// it named, for the confirmation question.
func writeSignInDestination(
	writer io.Writer,
	width int,
	alias string,
	configured config.Account,
	routes []application.AccountSignInRoute,
	routeErr error,
) ([]string, error) {
	heading := "Signing in to " + sanitizeCell(alias, 64)
	switch {
	case configured.Address == "":
	case application.AccountSignInAddressDisplayable(configured.Address):
		heading += " (" + configured.Address + ")"
	default:
		heading += " (address cannot be shown in plain text)"
	}
	if _, err := fmt.Fprintln(writer, heading); err != nil {
		return nil, err
	}
	if routeErr != nil {
		text := "The sign-in page for this account cannot be shown here."
		if errors.Is(routeErr, errSignInDestinationUndisplayed) {
			text = "The sign-in page for this account contains characters that cannot be shown in plain text."
		}
		text += " Check the account's connection settings with:"
		_, err := fmt.Fprintf(writer, "%s\n    %s\n", wrapText(text, width, "  "), shellCommand("corr", "account", "show", alias))
		return nil, err
	}
	var hosts []string
	for _, group := range groupSignInDestinations(routes) {
		line := fmt.Sprintf("%s (%s): %s", providerDisplayName(group.provider), strings.Join(group.services, ", "),
			strings.Join(group.hosts, ", "))
		switch group.kind {
		case application.AccountSignInProviderAuthorization:
			if group.clientID != "" {
				line += ", OAuth client " + group.clientID
			}
			line += " (opens in your browser)"
		case application.AccountSignInStoredCredential:
			line += " (uses the saved password from your keyring)"
		case application.AccountSignInDedicatedBrowser:
		}
		if _, err := fmt.Fprintln(writer, wrapText(line, width, "  ")); err != nil {
			return nil, err
		}
		for _, host := range group.hosts {
			if !slices.Contains(hosts, host) {
				hosts = append(hosts, host)
			}
		}
	}
	return hosts, nil
}

type signInDestinationGroup struct {
	provider domain.ProviderID
	kind     application.AccountSignInRouteKind
	clientID string
	hosts    []string
	services []string
}

func groupSignInDestinations(routes []application.AccountSignInRoute) []signInDestinationGroup {
	groups := make([]signInDestinationGroup, 0, len(routes))
	for _, route := range routes {
		hosts := append([]string{route.Host}, route.AdditionalHosts...)
		matched := false
		for index := range groups {
			group := &groups[index]
			if group.provider == route.Provider && group.kind == route.Kind &&
				group.clientID == route.ClientID && slices.Equal(group.hosts, hosts) {
				group.services = append(group.services, string(route.Service))
				matched = true
				break
			}
		}
		if !matched {
			groups = append(groups, signInDestinationGroup{
				provider: route.Provider, kind: route.Kind, clientID: route.ClientID,
				hosts: hosts, services: []string{string(route.Service)},
			})
		}
	}
	return groups
}
