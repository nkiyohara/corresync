package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/domain"
)

// signInVerifier asks the local daemon whether an account's current
// connection settings already completed a sign-in started from the CLI.
type signInVerifier interface {
	SignInVerified(context.Context, domain.AccountID, domain.Caller) (bool, error)
}

// signInFirstTimeNotice is printed when the account's current connection
// settings were never signed in to from the CLI.
const signInFirstTimeNotice = "This is the first sign-in with these connection settings. Check that the page above is the one you expect."

// reviewSignInDestination prints where `corr auth login` will sign in, from the
// same source as the MCP approval prompt, and says when it is the first
// sign-in with the account's current connection settings. It never stops the
// sign-in: the person reads the page it names and can press Ctrl-C.
func reviewSignInDestination(
	app *runtime,
	verifier signInVerifier,
	alias string,
	configured config.Account,
) error {
	routes, routeErr := signInDestinations(configured)
	if _, err := writeSignInDestination(app.stderr, app.textWidth(app.stderr), alias, configured, routes, routeErr); err != nil {
		return err
	}
	verified, verifyErr := verifier.SignInVerified(app.context, configured.ID, app.caller())
	if verifyErr == nil && verified {
		return nil
	}
	_, err := fmt.Fprintln(app.stderr, wrapText(signInFirstTimeNotice, app.textWidth(app.stderr), ""))
	return err
}

// writeSignInDestination writes the destination notice and returns the hosts
// it named.
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
