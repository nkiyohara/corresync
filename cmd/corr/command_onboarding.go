package main

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"charm.land/huh/v2"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/googleoauthclient"
	"github.com/nkiyohara/corresync/internal/rollout"
)

const (
	onboardingContinue = "continue"
	onboardingAdd      = "add"
	onboardingReview   = "review"
	onboardingCancel   = "__cancel_onboarding__"

	onboardingPresetICloud = "icloud"

	iCloudAccountManagementURL = "https://account.apple.com/account/manage"
	iCloudKeyringService       = "corresync"
)

type onboardingRoutePlan struct {
	mail     *application.ProviderCandidate
	calendar *application.ProviderCandidate
	label    string
	preset   string
	// manual marks a route the person chose by hand rather than one that
	// discovery found; its calendar starts unselected.
	manual bool
}

type onboardingService string

const (
	onboardingServiceMail           onboardingService = "mail"
	onboardingServiceCalendar       onboardingService = "calendar"
	onboardingServiceMicrosoftTasks onboardingService = "microsoft_tasks"
	onboardingServiceGoogleTasks    onboardingService = "google_tasks"
	onboardingServiceMessaging      onboardingService = "messaging"
)

type onboardingServiceSelection struct {
	mail         bool
	calendar     bool
	taskProvider domain.ProviderID
	messaging    bool
}

type onboardingRegistration struct {
	account          application.AccountView
	preset           string
	credential       onboardingCredentialReference
	googleClientPath string
	googleClientKey  string
}

type onboardingAccountProgress struct {
	added     int
	proceed   bool
	cancelled bool
}

func runOnboardingAccountPhase(
	app *runtime,
	createdConfig bool,
) (onboardingAccountProgress, error) {
	accounts, _, err := app.accountServices()
	if err != nil {
		return onboardingAccountProgress{}, err
	}
	catalog, err := accounts.List(app.context)
	if err != nil {
		return onboardingAccountProgress{}, err
	}
	added := 0
	addAccount := len(catalog.Accounts) == 0
	for !addAccount {
		action, selected, selectErr := runSettingsSelect(
			app,
			"Add another account?",
			"Your accounts stay as they are unless you change them.",
			[]huh.Option[string]{
				huh.NewOption("No, continue setup", onboardingContinue).
					Selected(settingsAccessible(app)),
				huh.NewOption("Add another account", onboardingAdd),
				huh.NewOption("Show my accounts", onboardingReview),
			},
		)
		if selectErr != nil {
			return onboardingAccountProgress{}, selectErr
		}
		if !selected {
			return onboardingAccountProgress{cancelled: true}, nil
		}
		switch action {
		case onboardingContinue:
			return onboardingAccountProgress{proceed: true}, nil
		case onboardingAdd:
			addAccount = true
		case onboardingReview:
			if err := (&accountListCommand{}).Run(app); err != nil {
				return onboardingAccountProgress{}, err
			}
		}
	}

	for {
		registration, completed, err := runAccountRegistrationWizard(app)
		if err != nil {
			if added > 0 {
				return onboardingAccountProgress{added: added}, fmt.Errorf(
					"setup stopped after %d completed account(s); those accounts remain configured: %w",
					added,
					err,
				)
			}
			return onboardingAccountProgress{}, err
		}
		if !completed {
			if err := writeOnboardingCancelled(app, added, createdConfig); err != nil {
				return onboardingAccountProgress{added: added}, err
			}
			return onboardingAccountProgress{added: added, cancelled: true}, nil
		}
		added++
		if err := runOnboardingAccountHandoff(app, registration); err != nil {
			return onboardingAccountProgress{added: added}, fmt.Errorf(
				"account %q remains configured, but its post-add action failed: %w",
				registration.account.Alias,
				err,
			)
		}
		for {
			action, selected, err := runSettingsSelect(
				app,
				"What next?",
				"Accounts you added stay added if you stop now.",
				[]huh.Option[string]{
					huh.NewOption("Add another account", onboardingAdd),
					huh.NewOption("Show my accounts", onboardingReview),
					huh.NewOption("Continue setup", onboardingContinue).
						Selected(true),
				},
			)
			if err != nil || !selected || action == onboardingContinue {
				if err != nil {
					return onboardingAccountProgress{added: added}, err
				}
				if !selected {
					return onboardingAccountProgress{added: added, cancelled: true}, nil
				}
				return onboardingAccountProgress{added: added, proceed: true}, nil
			}
			if action == onboardingAdd {
				break
			}
			if err := (&accountListCommand{}).Run(app); err != nil {
				return onboardingAccountProgress{added: added}, err
			}
		}
	}
}

func runAccountRegistrationWizard(
	app *runtime,
) (onboardingRegistration, bool, error) {
	accounts, discoverer, err := app.accountServices()
	if err != nil {
		return onboardingRegistration{}, false, err
	}
	address := ""
	selected, err := runSettingsInput(
		app,
		"Email address",
		"Used to find your mail server. You sign in later.",
		&address,
		254,
		validateSettingsEmailAddress,
	)
	if err != nil || !selected {
		return onboardingRegistration{}, false, err
	}
	if err := writeOnboardingAnswer(app, "Account", address); err != nil {
		return onboardingRegistration{}, false, err
	}
	discovery, err := discoverer.Discover(app.context, address)
	if err != nil {
		return onboardingRegistration{}, false, err
	}
	plan, selected, err := chooseOnboardingRoutePlan(app, discovery)
	if err != nil || !selected {
		return onboardingRegistration{}, false, err
	}
	services, selected, err := selectOnboardingServices(app, plan)
	if err != nil || !selected {
		return onboardingRegistration{}, false, err
	}

	alias := discovery.Address[:strings.LastIndexByte(discovery.Address, '@')]
	selected, err = runSettingsInput(
		app,
		"Name for this account",
		"A short name you type to pick this account, such as work or personal.",
		&alias,
		64,
		func(value string) error { return domain.AccountAlias(value).Validate() },
	)
	if err != nil || !selected {
		return onboardingRegistration{}, false, err
	}
	if err := writeOnboardingAnswer(app, "Name", alias); err != nil {
		return onboardingRegistration{}, false, err
	}
	catalog, err := accounts.List(app.context)
	if err != nil {
		return onboardingRegistration{}, false, err
	}
	makeDefault, selected, err := chooseOnboardingDefault(app, catalog, alias)
	if err != nil || !selected {
		return onboardingRegistration{}, false, err
	}
	routing, selected, err := configureOnboardingRoutes(
		app,
		plan.withServices(services),
		discovery,
		alias,
	)
	if err != nil || !selected {
		return onboardingRegistration{}, false, err
	}
	routing, selected, err = configureOnboardingTaskRoute(
		app, routing, plan, services, alias,
	)
	if err != nil || !selected {
		return onboardingRegistration{}, false, err
	}
	routing, messagingRoute, selected, err := configureOnboardingMessagingRoute(
		app, routing, plan, services,
	)
	if err != nil || !selected {
		return onboardingRegistration{}, false, err
	}
	var mailRoute *application.AccountMailRouteInput
	var calendarRoute *application.AccountCalendarRouteInput
	selectedPlan := plan.withServices(services)
	if selectedPlan.mail != nil || selectedPlan.calendar != nil {
		selectedCandidate := onboardingSelectedCandidate(selectedPlan)
		mailRoute, calendarRoute, _, err = routing.routes(
			selectedCandidate,
			discovery,
		)
	}
	if err != nil {
		return onboardingRegistration{}, false, err
	}
	taskRoute, err := routing.taskRoute()
	if err != nil {
		return onboardingRegistration{}, false, err
	}
	input := application.AccountAddInput{
		Alias: alias, Address: discovery.Address,
		Mail: mailRoute, Calendar: calendarRoute, Tasks: taskRoute,
		Messages: messagingRoute, Default: makeDefault,
	}
	review, err := accounts.ReviewAdd(app.context, input)
	if err != nil {
		return onboardingRegistration{}, false, err
	}
	if err := writeOnboardingAccountReview(app, review); err != nil {
		return onboardingRegistration{}, false, err
	}
	confirmed, err := runOnboardingConfirm(
		app,
		"Add "+alias+" to this computer?",
		"This saves the settings above. No password is saved and nothing signs in yet.",
		"Add account",
		"Cancel this account",
	)
	if err != nil || !confirmed {
		return onboardingRegistration{}, false, err
	}
	var account application.AccountView
	err = runSettingsAccountMutation(app, func() error {
		var addErr error
		account, addErr = accounts.Add(app.context, input)
		return addErr
	})
	if err != nil {
		return onboardingRegistration{}, false, err
	}
	if err := writeOnboardingAccountAdded(app, account); err != nil {
		return onboardingRegistration{}, false, err
	}
	return onboardingRegistration{
		account:          account,
		preset:           plan.preset,
		credential:       onboardingHandoffCredential(routing),
		googleClientPath: routing.onboardingGoogleClientPath,
		googleClientKey:  onboardingGoogleClientKey(routing),
	}, true, nil
}

func onboardingGoogleClientKey(routing accountAddCommand) string {
	if routing.OAuthClientSecretKey != "" {
		return routing.OAuthClientSecretKey
	}
	return routing.TaskOAuthSecretKey
}

func onboardingHandoffCredential(
	routing accountAddCommand,
) onboardingCredentialReference {
	if routing.CredentialKey != "" {
		return onboardingCredentialReference{
			backend: routing.CredentialBackend,
			key:     routing.CredentialKey,
		}
	}
	return onboardingCredentialReference{
		backend: routing.CalendarCredentialBackend,
		key:     routing.CalendarCredentialKey,
	}
}

func onboardingRoutePlans(
	discovery application.AccountDiscoveryResult,
) []onboardingRoutePlan {
	available := make(map[domain.ProviderID]*application.ProviderCandidate)
	for index := range discovery.Candidates {
		candidate := &discovery.Candidates[index]
		if candidate.Available {
			available[candidate.Provider] = candidate
		}
	}
	plans := make([]onboardingRoutePlan, 0, len(available)+2)
	calDAV := available[domain.ProviderCalDAV]
	for _, mailProvider := range []domain.ProviderID{
		domain.ProviderIMAPSMTP,
		domain.ProviderJMAP,
	} {
		if mail := available[mailProvider]; mail != nil && calDAV != nil {
			plan := newOnboardingRoutePlan(mail, calDAV)
			if isICloudOnboardingPlan(discovery, plan) {
				plan.preset = onboardingPresetICloud
				plan.label = "iCloud Mail and Calendar · needs an app-specific password"
			}
			plans = append(plans, plan)
		}
	}
	for _, candidate := range discovery.Candidates {
		if !candidate.Available {
			continue
		}
		copy := candidate
		switch candidate.Provider {
		case domain.ProviderMicrosoftOWA, domain.ProviderMicrosoftGraph,
			domain.ProviderGoogle:
			plans = append(plans, newOnboardingRoutePlan(&copy, &copy))
		case domain.ProviderJMAP, domain.ProviderIMAPSMTP:
			plans = append(plans, newOnboardingRoutePlan(&copy, nil))
		case domain.ProviderCalDAV:
			plans = append(plans, newOnboardingRoutePlan(nil, &copy))
		case domain.ProviderGoogleWeb, domain.ProviderPOP3,
			domain.ProviderMicrosoftTasks, domain.ProviderTodoist,
			domain.ProviderGoogleTasks, domain.ProviderAppleReminders,
			domain.ProviderTickTick, domain.ProviderAnyDoMCP,
			domain.ProviderThings, domain.ProviderOmniFocus:
		}
	}
	return plans
}

func isICloudOnboardingPlan(
	discovery application.AccountDiscoveryResult,
	plan onboardingRoutePlan,
) bool {
	if plan.mail == nil || plan.mail.Provider != domain.ProviderIMAPSMTP ||
		plan.calendar == nil || plan.calendar.Provider != domain.ProviderCalDAV {
		return false
	}
	mailEndpoints := map[string]bool{}
	for _, endpoint := range plan.mail.Endpoints {
		mailEndpoints[endpoint.Kind+"="+strings.ToLower(endpoint.Value)] = true
	}
	calendarEndpoint := false
	for _, endpoint := range plan.calendar.Endpoints {
		if endpoint.Kind == "caldav" &&
			strings.EqualFold(endpoint.Value, "https://caldav.icloud.com:443/") {
			calendarEndpoint = true
		}
	}
	if !mailEndpoints["imap=imap.mail.me.com:993"] ||
		!mailEndpoints["smtp=smtp.mail.me.com:587"] || !calendarEndpoint {
		return false
	}
	if discovery.Domain == "icloud.com" || discovery.Domain == "mac.com" ||
		discovery.Domain == "me.com" {
		return true
	}
	// A custom domain is identified only by the complete credential-free Apple
	// endpoint set. A suffix or a single generic standards record is insufficient.
	return candidateHasDiscoveryEvidence(*plan.mail, "srv_imaps") &&
		candidateHasDiscoveryEvidence(*plan.mail, "srv_submission") &&
		candidateHasDiscoveryEvidence(*plan.calendar, "srv_caldavs")
}

func candidateHasDiscoveryEvidence(
	candidate application.ProviderCandidate,
	source string,
) bool {
	for _, evidence := range candidate.Evidence {
		if evidence.Source == source {
			return true
		}
	}
	return false
}

func newOnboardingRoutePlan(
	mailCandidate *application.ProviderCandidate,
	calendarCandidate *application.ProviderCandidate,
) onboardingRoutePlan {
	// Mail and calendar together is the usual case, so only a single
	// service is spelled out.
	parts := make([]string, 0, 3)
	switch {
	case mailCandidate != nil && calendarCandidate != nil &&
		mailCandidate.Provider == calendarCandidate.Provider:
		parts = append(parts, providerDisplayName(mailCandidate.Provider))
	case mailCandidate != nil && calendarCandidate != nil:
		parts = append(parts, providerDisplayName(mailCandidate.Provider)+" + "+
			providerDisplayName(calendarCandidate.Provider))
	case mailCandidate != nil:
		parts = append(parts, providerDisplayName(mailCandidate.Provider), "mail only")
	case calendarCandidate != nil:
		parts = append(parts, providerDisplayName(calendarCandidate.Provider), "calendar only")
	}
	parts = append(parts, onboardingSignInSummary(mailCandidate, calendarCandidate))
	return onboardingRoutePlan{
		mail: mailCandidate, calendar: calendarCandidate,
		label: strings.Join(parts, " · "),
	}
}

// onboardingSignInSummary says in a few words what signing in to a route will
// need, so a person can compare routes without knowing the protocols.
func onboardingSignInSummary(
	candidates ...*application.ProviderCandidate,
) string {
	for _, candidate := range candidates {
		if candidate != nil &&
			candidate.Authentication == application.DiscoveryExternalCredential {
			return "needs a saved password"
		}
	}
	for _, candidate := range candidates {
		if candidate == nil || candidate.Authentication != application.DiscoveryExplicitOAuth {
			continue
		}
		if candidate.Provider == domain.ProviderGoogle {
			return "needs your own OAuth client"
		}
		return "needs your own app registration"
	}
	return "sign in with your browser"
}

const (
	onboardingRouteOther = "other"
	onboardingRouteStop  = "stop"
	onboardingRoutePlanN = "plan:"
	onboardingRouteHand  = "manual:"
)

// chooseOnboardingRoutePlan asks how to connect. Discovered routes come first,
// in discovery order; when there are none, or the person asks for something
// else, the routes Corresync can set up by hand are offered instead of ending
// the wizard. A chosen route that needs something prepared first is explained
// before any of its questions are asked.
func chooseOnboardingRoutePlan(
	app *runtime,
	discovery application.AccountDiscoveryResult,
) (onboardingRoutePlan, bool, error) {
	plans := onboardingRoutePlans(discovery)
	manual := len(plans) == 0
	for {
		var plan onboardingRoutePlan
		var choice string
		var selected bool
		var err error
		if len(plans) == 1 && !manual && !onboardingPlanRequiresSelection(plans[0]) {
			plan, choice, selected = plans[0], onboardingRoutePlanN+"0", true
		} else {
			choice, selected, err = selectOnboardingRoute(app, discovery, plans, manual)
			if err != nil || !selected {
				return onboardingRoutePlan{}, false, err
			}
		}
		switch {
		case choice == onboardingRouteStop:
			return onboardingRoutePlan{}, false, nil
		case choice == onboardingRouteOther:
			manual = true
			continue
		case strings.HasPrefix(choice, onboardingRoutePlanN):
			index, parseErr := strconv.Atoi(strings.TrimPrefix(choice, onboardingRoutePlanN))
			if parseErr != nil || index < 0 || index >= len(plans) {
				return onboardingRoutePlan{}, false, errors.New("unknown connection choice")
			}
			plan = plans[index]
		case strings.HasPrefix(choice, onboardingRouteHand):
			manualPlan, known := manualOnboardingRoutePlan(
				domain.ProviderID(strings.TrimPrefix(choice, onboardingRouteHand)),
				discovery.Domain,
			)
			if !known {
				return onboardingRoutePlan{}, false, errors.New("unknown connection choice")
			}
			plan = manualPlan
		}
		next, selected, err := confirmOnboardingPrerequisite(app, plan, plans)
		if err != nil || !selected {
			return onboardingRoutePlan{}, false, err
		}
		switch next {
		case onboardingRouteStop:
			return onboardingRoutePlan{}, false, nil
		case onboardingRouteOther:
			manual = true
			continue
		case onboardingRouteOutlookWeb:
			if owa, found := onboardingOutlookWebPlan(plans); found {
				plan = owa
			}
		}
		if err := writeOnboardingAnswer(app, "Connection", plan.name()); err != nil {
			return onboardingRoutePlan{}, false, err
		}
		return plan, true, nil
	}
}

func selectOnboardingRoute(
	app *runtime,
	discovery application.AccountDiscoveryResult,
	plans []onboardingRoutePlan,
	manual bool,
) (string, bool, error) {
	options := make([]huh.Option[string], 0, len(plans)+8)
	for index, plan := range plans {
		label := "Also possible: " + plan.label
		if index == 0 {
			label = "Recommended: " + plan.label
		}
		options = append(options, huh.NewOption(label, onboardingRoutePlanN+strconv.Itoa(index)).
			Selected(plan.preset == onboardingPresetICloud))
	}
	title := "How should Corresync connect?"
	description := "Corresync found these ways to connect. You'll confirm it works when you sign in."
	if manual {
		for _, route := range manualOnboardingRoutes(discovery, plans) {
			options = append(options, huh.NewOption(
				route.label,
				onboardingRouteHand+string(route.provider),
			))
		}
		options = append(options, huh.NewOption(
			"Stop here and come back later", onboardingRouteStop,
		))
		if len(plans) == 0 {
			title = "Choose how to connect"
			description = "Corresync couldn't find how to connect to " +
				sanitizeCell(discovery.Domain, 253) +
				". Pick the kind of server your provider uses; each choice says what it asks for."
		} else {
			description = "Pick one of the ways Corresync found, or set up a server yourself."
		}
	} else {
		options = append(options, huh.NewOption(
			"Something else: choose the server type yourself", onboardingRouteOther,
		))
	}
	return runOnboardingSelect(app, title, description, options, onboardingCancel)
}

type onboardingManualRoute struct {
	provider domain.ProviderID
	label    string
}

// manualOnboardingRoutes lists the routes `corr account add` accepts without a
// discovered candidate. Outlook Web is left out when discovery recognized the
// domain as Microsoft but did not offer Outlook Web: those domains use a
// sign-in that Corresync does not support.
func manualOnboardingRoutes(
	discovery application.AccountDiscoveryResult,
	plans []onboardingRoutePlan,
) []onboardingManualRoute {
	discovered := map[domain.ProviderID]bool{}
	for _, plan := range plans {
		if plan.mail != nil {
			discovered[plan.mail.Provider] = true
		}
		if plan.calendar != nil {
			discovered[plan.calendar.Provider] = true
		}
	}
	microsoftWithoutOutlookWeb := false
	for _, candidate := range discovery.Candidates {
		if candidate.Provider == domain.ProviderMicrosoftGraph {
			microsoftWithoutOutlookWeb = true
		}
	}
	if discovered[domain.ProviderMicrosoftOWA] {
		microsoftWithoutOutlookWeb = false
	}
	routes := []onboardingManualRoute{
		{domain.ProviderIMAPSMTP, "IMAP/SMTP: most mail servers (server names, saved password)"},
		{domain.ProviderJMAP, "JMAP: Fastmail and other JMAP servers (URL, saved password)"},
		{domain.ProviderCalDAV, "CalDAV: calendar only (URL, saved password)"},
		{domain.ProviderMicrosoftOWA, "Outlook Web: Microsoft 365 work or school (browser sign-in)"},
		{domain.ProviderMicrosoftGraph, "Microsoft Graph: any Microsoft account (your app registration)"},
	}
	if rollout.GoogleBYOOAuthEnabled {
		routes = append(routes, onboardingManualRoute{
			domain.ProviderGoogle, "Google: Gmail or Workspace (your own Google OAuth client)",
		})
	}
	result := make([]onboardingManualRoute, 0, len(routes))
	for _, route := range routes {
		if route.provider == domain.ProviderMicrosoftOWA && microsoftWithoutOutlookWeb {
			continue
		}
		if discovered[route.provider] {
			continue
		}
		result = append(result, route)
	}
	return result
}

// manualOnboardingRoutePlan builds the same candidate `corr account add
// --provider` builds for a route discovery did not find. It carries no
// endpoints, so the wizard asks for each one.
func manualOnboardingRoutePlan(
	provider domain.ProviderID,
	domainName string,
) (onboardingRoutePlan, bool) {
	candidate := func(provider domain.ProviderID) *application.ProviderCandidate {
		authentication := application.DiscoveryExternalCredential
		switch provider { //nolint:exhaustive // only the manual routes below reach this.
		case domain.ProviderMicrosoftOWA:
			authentication = application.DiscoveryBrowserFirstParty
		case domain.ProviderMicrosoftGraph, domain.ProviderGoogle:
			authentication = application.DiscoveryExplicitOAuth
		}
		return &application.ProviderCandidate{
			Provider:                  provider,
			Authentication:            authentication,
			RequiresExplicitSelection: true,
			Available:                 true,
			Evidence: []application.DiscoveryEvidence{
				{Source: "manual_override", Detail: domainName},
			},
		}
	}
	var plan onboardingRoutePlan
	switch provider { //nolint:exhaustive // every other provider is not offered by hand.
	case domain.ProviderIMAPSMTP, domain.ProviderJMAP:
		plan = newOnboardingRoutePlan(candidate(provider), candidate(domain.ProviderCalDAV))
		plan.label = providerDisplayName(provider) + " · " + onboardingSignInSummary(plan.mail)
	case domain.ProviderCalDAV:
		plan = newOnboardingRoutePlan(nil, candidate(provider))
	case domain.ProviderMicrosoftOWA, domain.ProviderMicrosoftGraph:
		shared := candidate(provider)
		plan = newOnboardingRoutePlan(shared, shared)
	case domain.ProviderGoogle:
		if !rollout.GoogleBYOOAuthEnabled {
			return onboardingRoutePlan{}, false
		}
		shared := candidate(provider)
		plan = newOnboardingRoutePlan(shared, shared)
	default:
		return onboardingRoutePlan{}, false
	}
	plan.manual = true
	return plan, true
}

const onboardingRouteOutlookWeb = "outlook_web"

func onboardingOutlookWebPlan(plans []onboardingRoutePlan) (onboardingRoutePlan, bool) {
	for _, plan := range plans {
		if plan.mail != nil && plan.mail.Provider == domain.ProviderMicrosoftOWA {
			return plan, true
		}
	}
	return onboardingRoutePlan{}, false
}

type onboardingPrerequisite struct {
	title       string
	description string
	ready       string
}

const (
	microsoftAppRegistrationGuide = "https://learn.microsoft.com/entra/identity-platform/quickstart-register-app"
	googleOAuthClientGuide        = "https://corresync.org/google-oauth.html"
	standardsSetupGuide           = "https://corresync.org/providers.html#standards-title"
)

// onboardingPlanPrerequisite describes what a person must prepare before the
// route's questions make sense. A route that signs in through the provider's
// own browser page needs nothing.
func onboardingPlanPrerequisite(plan onboardingRoutePlan) (onboardingPrerequisite, bool) {
	switch {
	case plan.preset == onboardingPresetICloud:
		return onboardingPrerequisite{
			title: "iCloud needs an app-specific password",
			description: "Apple requires two-factor authentication before you can create one. " +
				"After the account is added, Corresync can open Apple's page and save the " +
				"password through your system's own prompt.",
			ready: "Continue",
		}, true
	case plan.usesProvider(domain.ProviderMicrosoftGraph):
		return onboardingPrerequisite{
			title: "Microsoft Graph needs your own app registration",
			description: "Register a public client app in Microsoft Entra (free) and copy " +
				"its Application (client) ID. How to register: " + microsoftAppRegistrationGuide,
			ready: "I have a client ID, continue",
		}, true
	case plan.usesGoogle():
		return onboardingPrerequisite{
			title: "Google needs your own OAuth client",
			description: "Gmail and Google Calendar need a Desktop OAuth client from a Google " +
				"Cloud project you own; download its JSON file, then continue. Guide: " +
				googleOAuthClientGuide,
			ready: "I have the JSON file, continue",
		}, true
	case plan.usesProvider(domain.ProviderIMAPSMTP), plan.usesProvider(domain.ProviderJMAP),
		plan.usesProvider(domain.ProviderCalDAV):
		return onboardingPrerequisite{
			title: "This server needs a saved password",
			description: "Corresync never asks for your password. Save it (often an app " +
				"password from your provider) in this computer's keyring or a credential " +
				"helper before you sign in; you give only the entry's name here. Guide: " +
				standardsSetupGuide,
			ready: "Continue",
		}, true
	}
	return onboardingPrerequisite{}, false
}

func confirmOnboardingPrerequisite(
	app *runtime,
	plan onboardingRoutePlan,
	plans []onboardingRoutePlan,
) (string, bool, error) {
	prerequisite, needed := onboardingPlanPrerequisite(plan)
	if !needed {
		return onboardingContinue, true, nil
	}
	options := []huh.Option[string]{
		huh.NewOption(prerequisite.ready, onboardingContinue),
	}
	if _, found := onboardingOutlookWebPlan(plans); found &&
		plan.usesProvider(domain.ProviderMicrosoftGraph) {
		options = append(options, huh.NewOption(
			"Use Outlook Web instead: sign in with your browser, nothing to prepare",
			onboardingRouteOutlookWeb,
		))
	}
	options = append(options,
		huh.NewOption("Use another way to connect", onboardingRouteOther),
		huh.NewOption("Stop here and come back later", onboardingRouteStop),
	)
	return runOnboardingSelect(
		app, prerequisite.title, prerequisite.description, options, onboardingCancel,
	)
}

func (plan onboardingRoutePlan) name() string {
	name, _, _ := strings.Cut(plan.label, " · ")
	return name
}

func selectOnboardingServices(
	app *runtime,
	plan onboardingRoutePlan,
) (onboardingServiceSelection, bool, error) {
	messagingEnabled := rollout.MessagingCatalogEnabled()
	description := "Choose what Corresync should use for this account."
	if plan.usesMicrosoft() && !messagingEnabled {
		description += " Teams messaging is not available yet."
	}
	values, selected, err := runSettingsMultiSelect(
		app,
		"Services to configure",
		description,
		onboardingServiceOptions(plan, messagingEnabled),
		validateOnboardingServices,
	)
	if err != nil || !selected {
		return onboardingServiceSelection{}, false, err
	}
	if err := validateOnboardingServices(values); err != nil {
		return onboardingServiceSelection{}, false, err
	}
	result := onboardingServiceSelection{}
	names := make([]string, 0, len(values))
	for _, service := range values {
		switch service {
		case onboardingServiceMail:
			result.mail = true
			names = append(names, "Mail")
		case onboardingServiceCalendar:
			result.calendar = true
			names = append(names, "Calendar")
		case onboardingServiceMicrosoftTasks:
			result.taskProvider = domain.ProviderMicrosoftGraph
			names = append(names, "Microsoft To Do")
		case onboardingServiceGoogleTasks:
			result.taskProvider = domain.ProviderGoogleTasks
			names = append(names, "Google Tasks")
		case onboardingServiceMessaging:
			result.messaging = true
			names = append(names, "Teams messaging")
		}
	}
	if err := writeOnboardingAnswer(app, "Services", strings.Join(names, ", ")); err != nil {
		return onboardingServiceSelection{}, false, err
	}
	return result, true, nil
}

func validateOnboardingServices(values []onboardingService) error {
	if len(values) == 0 {
		return inputProblem("Select at least one service.")
	}
	taskServices := 0
	for _, service := range values {
		if service == onboardingServiceMicrosoftTasks || service == onboardingServiceGoogleTasks {
			taskServices++
		}
	}
	if taskServices > 1 {
		return inputProblem("Select only one task service per account.")
	}
	return nil
}

func onboardingServiceOptions(
	plan onboardingRoutePlan,
	messagingEnabled bool,
) []huh.Option[onboardingService] {
	options := make([]huh.Option[onboardingService], 0, 4)
	if plan.mail != nil {
		options = append(options, huh.NewOption(
			"Mail ("+providerDisplayName(plan.mail.Provider)+")",
			onboardingServiceMail,
		).Selected(true))
	}
	if plan.calendar != nil {
		options = append(options, huh.NewOption(
			"Calendar ("+providerDisplayName(plan.calendar.Provider)+")",
			onboardingServiceCalendar,
		).Selected(!plan.manual || plan.mail == nil))
	}
	if plan.usesMicrosoft() {
		options = append(options, huh.NewOption(
			"Microsoft To Do (needs your own Microsoft app registration)",
			onboardingServiceMicrosoftTasks,
		))
		if messagingEnabled {
			options = append(options, huh.NewOption(
				"Teams messaging (preview; you choose Teams Web or Graph next)",
				onboardingServiceMessaging,
			))
		}
	}
	if plan.usesGoogle() && rollout.GoogleBYOOAuthEnabled {
		options = append(options, huh.NewOption(
			"Google Tasks (signs in separately with your Google OAuth client)",
			onboardingServiceGoogleTasks,
		))
	}
	return options
}

func (plan onboardingRoutePlan) withServices(
	services onboardingServiceSelection,
) onboardingRoutePlan {
	selected := plan
	if !services.mail {
		selected.mail = nil
	}
	if !services.calendar {
		selected.calendar = nil
	}
	return selected
}

func (plan onboardingRoutePlan) usesMicrosoft() bool {
	return plan.usesProvider(domain.ProviderMicrosoftOWA) ||
		plan.usesProvider(domain.ProviderMicrosoftGraph)
}

func (plan onboardingRoutePlan) usesGoogle() bool {
	return plan.usesProvider(domain.ProviderGoogle)
}

func (plan onboardingRoutePlan) usesProvider(provider domain.ProviderID) bool {
	return plan.mail != nil && plan.mail.Provider == provider ||
		plan.calendar != nil && plan.calendar.Provider == provider
}

func runOnboardingSelect[T comparable](
	app *runtime,
	title string,
	description string,
	options []huh.Option[T],
	cancelValue T,
) (T, bool, error) {
	if settingsAccessible(app) {
		options = append(
			options,
			huh.NewOption("Cancel this account", cancelValue).Selected(true),
		)
	}
	value, selected, err := runSettingsSelect(app, title, description, options)
	if err != nil || !selected || value == cancelValue {
		var zero T
		return zero, false, err
	}
	return value, true, nil
}

func onboardingPlanRequiresSelection(plan onboardingRoutePlan) bool {
	return plan.mail != nil && plan.mail.RequiresExplicitSelection ||
		plan.calendar != nil && plan.calendar.RequiresExplicitSelection
}

func chooseOnboardingDefault(
	app *runtime,
	catalog application.AccountCatalog,
	alias string,
) (bool, bool, error) {
	if len(catalog.Accounts) == 0 {
		return true, true, nil
	}
	current := "the current account"
	for _, account := range catalog.Accounts {
		if account.IsDefault {
			current = account.Alias
			break
		}
	}
	choice, selected, err := runOnboardingSelect(
		app,
		"Default account",
		"Commands use the default account when you leave out --account.",
		[]huh.Option[string]{
			huh.NewOption("Keep "+current+" as default", "keep"),
			huh.NewOption("Make "+alias+" the default", "new"),
		},
		onboardingCancel,
	)
	return choice == "new", selected, err
}

func configureOnboardingRoutes(
	app *runtime,
	plan onboardingRoutePlan,
	discovery application.AccountDiscoveryResult,
	alias string,
) (accountAddCommand, bool, error) {
	command := accountAddCommand{
		Address: discovery.Address, Alias: alias,
		Provider: "none", CalendarProvider: "none",
		CredentialBackend: "os-keyring",
	}
	if plan.preset == onboardingPresetICloud {
		return configureICloudOnboardingRoutes(
			app, command, discovery, alias, plan.mail != nil, plan.calendar != nil,
		)
	}
	if plan.mail != nil {
		command.Provider = string(plan.mail.Provider)
	}
	if plan.calendar != nil {
		command.CalendarProvider = string(plan.calendar.Provider)
	}
	providers := map[domain.ProviderID]bool{}
	if plan.mail != nil {
		providers[plan.mail.Provider] = true
	}
	if plan.calendar != nil {
		providers[plan.calendar.Provider] = true
	}
	for _, provider := range []domain.ProviderID{
		domain.ProviderMicrosoftOWA,
		domain.ProviderJMAP,
		domain.ProviderIMAPSMTP,
		domain.ProviderCalDAV,
		domain.ProviderMicrosoftGraph,
		domain.ProviderGoogle,
		domain.ProviderGoogleWeb,
		domain.ProviderPOP3,
	} {
		if !providers[provider] {
			continue
		}
		var selected bool
		var err error
		switch provider {
		case domain.ProviderMicrosoftOWA:
			command.Origin, selected, err = onboardingHTTPSEndpoint(
				app, "Outlook Web address",
				"The address where you open Outlook in a browser. Microsoft 365 uses https://outlook.cloud.microsoft.",
				candidateEndpoint(*plan.mailOrCalendar(provider), "origin"),
				"https://outlook.cloud.microsoft",
			)
		case domain.ProviderJMAP:
			command.SessionURL, selected, err = onboardingHTTPSEndpoint(
				app, "JMAP session URL",
				"From your provider's setup instructions, for example https://mail.example.com/.well-known/jmap.",
				candidateHTTPSEndpoint(*plan.mail, "jmap"),
				"https://",
			)
			if err == nil && selected {
				selected, err = configureOnboardingMailCredential(app, &command, alias, "JMAP")
			}
		case domain.ProviderIMAPSMTP:
			selected, err = configureOnboardingIMAP(app, &command, *plan.mail, alias)
		case domain.ProviderCalDAV:
			command.CalDAVEndpoint, selected, err = onboardingHTTPSEndpoint(
				app, "CalDAV server URL",
				"From your provider's setup instructions. It starts with https://.",
				candidateHTTPSEndpoint(*plan.calendar, "caldav"),
				"https://",
			)
			if err == nil && selected {
				selected, err = configureOnboardingCalendarCredential(app, &command, alias)
			}
		case domain.ProviderMicrosoftGraph:
			selected, err = configureOnboardingOAuth(
				app, &command, alias, domain.ProviderMicrosoftGraph,
			)
		case domain.ProviderGoogle:
			selected, err = configureOnboardingOAuth(
				app, &command, alias, domain.ProviderGoogle,
			)
		case domain.ProviderGoogleWeb, domain.ProviderPOP3,
			domain.ProviderMicrosoftTasks, domain.ProviderTodoist,
			domain.ProviderGoogleTasks, domain.ProviderAppleReminders,
			domain.ProviderTickTick, domain.ProviderAnyDoMCP,
			domain.ProviderThings, domain.ProviderOmniFocus:
			return accountAddCommand{}, false, fmt.Errorf(
				"provider %q is not available for guided setup",
				provider,
			)
		}
		if err != nil || !selected {
			return accountAddCommand{}, false, err
		}
	}
	return command, true, nil
}

func configureOnboardingTaskRoute(
	app *runtime,
	command accountAddCommand,
	plan onboardingRoutePlan,
	services onboardingServiceSelection,
	alias string,
) (accountAddCommand, bool, error) {
	provider := services.taskProvider
	if provider == "" {
		return command, true, nil
	}
	oauthProvider := domain.ProviderMicrosoftGraph
	authorizationSuffix := "microsoft-tasks"
	switch provider { //nolint:exhaustive // every other provider follows the explicit unsupported path.
	case domain.ProviderMicrosoftGraph:
		if !plan.usesMicrosoft() {
			return accountAddCommand{}, false, errors.New(
				"the selected connection route does not offer Microsoft To Do",
			)
		}
	case domain.ProviderGoogleTasks:
		if !plan.usesGoogle() {
			return accountAddCommand{}, false, errors.New(
				"the selected connection route does not offer Google Tasks",
			)
		}
		oauthProvider = domain.ProviderGoogle
		authorizationSuffix = "google-tasks"
	default:
		return accountAddCommand{}, false, errors.New(
			"the selected guided task service is unsupported",
		)
	}

	taskOAuth := command
	canReuseClient := plan.usesProvider(oauthProvider) &&
		command.onboardingOAuthProvider == oauthProvider &&
		command.OAuthClientID != ""
	if !canReuseClient {
		taskOAuth = accountAddCommand{}
		selected, err := configureOnboardingOAuth(app, &taskOAuth, alias, oauthProvider)
		if err != nil || !selected {
			return accountAddCommand{}, false, err
		}
	}
	command.TaskProvider = string(provider)
	command.TaskOAuthClientID = taskOAuth.OAuthClientID
	command.TaskOAuthRedirectURI = taskOAuth.OAuthRedirectURI
	command.TaskAuthorizationKey = alias + "-" + authorizationSuffix
	command.ApproveTaskOAuth = true
	if provider == domain.ProviderGoogleTasks {
		command.TaskOAuthSecretBackend = taskOAuth.OAuthClientSecretBackend
		command.TaskOAuthSecretKey = taskOAuth.OAuthClientSecretKey
		command.ApproveTaskOAuthSecret = taskOAuth.ApproveOAuthClientSecret
		if command.onboardingGoogleClientPath == "" {
			command.onboardingGoogleClientPath = taskOAuth.onboardingGoogleClientPath
		}
	}
	return command, true, nil
}

func configureOnboardingMessagingRoute(
	app *runtime,
	command accountAddCommand,
	plan onboardingRoutePlan,
	services onboardingServiceSelection,
) (accountAddCommand, *application.AccountMessagingRouteInput, bool, error) {
	if !services.messaging {
		return command, nil, true, nil
	}
	if !plan.usesMicrosoft() {
		return accountAddCommand{}, nil, false, errors.New(
			"the selected connection route does not offer guided messaging",
		)
	}
	if !rollout.MessagingCatalogEnabled() {
		return accountAddCommand{}, nil, false, rollout.ErrMessagingPending
	}
	route, selected, err := runOnboardingSelect(
		app,
		"Teams connection (preview)",
		"Teams support is a preview not yet tested with live accounts. Microsoft Graph is used only if you pick it here.",
		[]huh.Option[domain.MessagingRouteKind]{
			huh.NewOption(
				"Teams Web · sign in with your browser",
				domain.MessagingRouteTeamsWeb,
			).Selected(true),
			huh.NewOption(
				"Microsoft Graph · needs your own Microsoft app registration",
				domain.MessagingRouteTeamsGraph,
			),
		},
		domain.MessagingRouteKind(onboardingCancel),
	)
	if err != nil || !selected {
		return accountAddCommand{}, nil, false, err
	}
	if err := rollout.RequireMessaging(domain.MessagingProviderMicrosoftTeams, route); err != nil {
		return accountAddCommand{}, nil, false, err
	}
	workspaceID := ""
	selected, err = runSettingsInput(
		app,
		"Teams tenant or workspace ID",
		"Your Teams tenant ID. Corresync does not guess it from your email address.",
		&workspaceID,
		4096,
		validateOnboardingMessagingWorkspace,
	)
	if err != nil || !selected {
		return accountAddCommand{}, nil, false, err
	}
	result := &application.AccountMessagingRouteInput{
		Provider: domain.MessagingProviderMicrosoftTeams,
	}
	if route == domain.MessagingRouteTeamsWeb {
		result.TeamsWeb = &application.AccountTeamsWebMessagingInput{
			Web:         application.AccountWebInput{Origin: "https://teams.microsoft.com"},
			WorkspaceID: workspaceID,
		}
		return command, result, true, nil
	}
	messagingOAuth := command
	if command.OAuthClientID == "" ||
		command.onboardingOAuthProvider != domain.ProviderMicrosoftGraph {
		messagingOAuth = accountAddCommand{MicrosoftCloud: command.MicrosoftCloud}
		selected, err = configureOnboardingOAuth(
			app, &messagingOAuth, command.Alias, domain.ProviderMicrosoftGraph,
		)
		if err != nil || !selected {
			return accountAddCommand{}, nil, false, err
		}
	}
	oauth, err := messagingOAuth.oauthRoute(
		domain.ProviderMicrosoftGraph,
		onboardingCandidateForProvider(plan, domain.ProviderMicrosoftGraph),
		"https://graph.microsoft.com/v1.0",
		false,
	)
	if err != nil {
		return accountAddCommand{}, nil, false, err
	}
	result.TeamsGraph = &application.AccountTeamsGraphMessagingInput{
		OAuth: *oauth, WorkspaceID: workspaceID,
	}
	return command, result, true, nil
}

func validateOnboardingMessagingWorkspace(value string) error {
	if value == "" || len(value) > 4096 || strings.TrimSpace(value) != value ||
		strings.ContainsAny(value, "\r\n\x00") {
		return inputProblem("Type the tenant or workspace ID on one line, with no spaces at either end.")
	}
	return nil
}

func configureICloudOnboardingRoutes(
	app *runtime,
	command accountAddCommand,
	discovery application.AccountDiscoveryResult,
	alias string,
	mail bool,
	calendar bool,
) (accountAddCommand, bool, error) {
	calendarUsername := discovery.Address
	if calendar {
		description := "Keep this address unless you sign in to your Apple Account with another email."
		if mail {
			description = "Mail signs in as " + sanitizeCell(discovery.Address, 254) +
				". " + description
		}
		selected, err := runSettingsInput(
			app,
			"Apple Account email for Calendar",
			description,
			&calendarUsername,
			254,
			validateSettingsEmailAddress,
		)
		if err != nil || !selected {
			return accountAddCommand{}, false, err
		}
	}
	service := "iCloud Mail"
	credentialKey := alias + "-icloud-mail"
	if calendar {
		service = "iCloud Calendar"
		credentialKey = alias + "-icloud-calendar"
	}
	if mail && calendar {
		service = "iCloud Mail and Calendar"
		credentialKey = alias + "-icloud"
	}
	reference, selected, err := configureOnboardingCredentialReference(
		app,
		service,
		credentialKey,
	)
	if err != nil || !selected {
		return accountAddCommand{}, false, err
	}
	if mail {
		command.Provider = string(domain.ProviderIMAPSMTP)
		command.IMAPHost = "imap.mail.me.com"
		command.IMAPPort = 993
		command.IMAPTLS = "implicit"
		command.SMTPHost = "smtp.mail.me.com"
		command.SMTPPort = 587
		command.SMTPTLS = "starttls"
		command.Username = discovery.Address
		command.CredentialBackend = reference.backend
		command.CredentialKey = reference.key
		command.ApproveCredential = true
	}
	if calendar {
		command.CalendarProvider = string(domain.ProviderCalDAV)
		command.CalDAVEndpoint = "https://caldav.icloud.com:443/"
		command.CalendarUsername = calendarUsername
		command.CalendarCredentialBackend = reference.backend
		command.CalendarCredentialKey = reference.key
		command.ApproveCalendarCredential = true
	}
	return command, true, nil
}

func (plan onboardingRoutePlan) mailOrCalendar(
	provider domain.ProviderID,
) *application.ProviderCandidate {
	if plan.mail != nil && plan.mail.Provider == provider {
		return plan.mail
	}
	return plan.calendar
}

func onboardingSelectedCandidate(
	plan onboardingRoutePlan,
) application.ProviderCandidate {
	if plan.mail != nil {
		return *plan.mail
	}
	return *plan.calendar
}

func onboardingCandidateForProvider(
	plan onboardingRoutePlan,
	provider domain.ProviderID,
) application.ProviderCandidate {
	if plan.mail != nil && plan.mail.Provider == provider {
		return *plan.mail
	}
	if plan.calendar != nil && plan.calendar.Provider == provider {
		return *plan.calendar
	}
	return application.ProviderCandidate{Provider: provider}
}

func onboardingHTTPSEndpoint(
	app *runtime,
	title string,
	description string,
	discovered string,
	prefill string,
) (string, bool, error) {
	if discovered != "" {
		return discovered, true, nil
	}
	value := prefill
	selected, err := runSettingsInput(
		app, title, description, &value, 2048, validateOnboardingHTTPSURL,
	)
	if err == nil && selected {
		err = writeOnboardingAnswer(app, title, value)
	}
	return value, selected, err
}

func validateOnboardingHTTPSURL(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery ||
		parsed.Fragment != "" || strings.HasSuffix(parsed.Host, ":") {
		return inputProblem(
			"Type an https:// address without a user name, ? or #. Example: https://mail.example.com/dav/",
		)
	}
	return nil
}

func configureOnboardingMailCredential(
	app *runtime,
	command *accountAddCommand,
	alias string,
	service string,
) (bool, error) {
	reference, selected, err := configureOnboardingCredentialReference(
		app, service, alias+"-mail",
	)
	if err != nil || !selected {
		return false, err
	}
	command.CredentialBackend = reference.backend
	command.CredentialKey = reference.key
	command.ApproveCredential = true
	return true, nil
}

func configureOnboardingCalendarCredential(
	app *runtime,
	command *accountAddCommand,
	alias string,
) (bool, error) {
	reference, selected, err := configureOnboardingCredentialReference(
		app, "Calendar", alias+"-calendar",
	)
	if err != nil || !selected {
		return false, err
	}
	command.CalendarCredentialBackend = reference.backend
	command.CalendarCredentialKey = reference.key
	command.ApproveCalendarCredential = true
	return true, nil
}

type onboardingCredentialReference struct {
	backend string
	key     string
}

func configureOnboardingCredentialReference(
	app *runtime,
	service string,
	defaultKey string,
) (onboardingCredentialReference, bool, error) {
	secret := "password"
	if strings.Contains(service, "OAuth client") {
		secret = "client secret"
	}
	backend, selected, err := chooseOnboardingCredentialBackend(
		app, "Where is the "+service+" "+secret+" saved?",
	)
	if err != nil || !selected {
		return onboardingCredentialReference{}, false, err
	}
	key := defaultKey
	selected, err = runSettingsInput(
		app,
		service+" "+secret+" entry name",
		"The name of the keyring or helper entry that holds the "+secret+". Corresync stores only this name.",
		&key,
		256,
		validateOnboardingCredentialKey,
	)
	if err != nil || !selected {
		return onboardingCredentialReference{}, false, err
	}
	consent, err := runOnboardingConfirm(
		app,
		"Use that saved "+service+" "+secret+" when this account signs in?",
		"The "+secret+" stays in the keyring or helper; the settings file keeps only its name.",
		"Allow",
		"Cancel this account",
	)
	if err != nil || !consent {
		return onboardingCredentialReference{}, false, err
	}
	return onboardingCredentialReference{backend: backend, key: key}, true, nil
}

func chooseOnboardingCredentialBackend(
	app *runtime,
	title string,
) (string, bool, error) {
	return runOnboardingSelect(
		app,
		title,
		"You can save it now or at any time before you sign in.",
		[]huh.Option[string]{
			huh.NewOption("This computer's keyring (recommended)", "os-keyring"),
			huh.NewOption("A credential helper program you set up", "helper"),
		},
		onboardingCancel,
	)
}

func validateOnboardingCredentialKey(value string) error {
	if value == "" || len(value) > 256 || strings.ContainsAny(value, "\r\n\x00") {
		return inputProblem("Type the entry name on one line, up to 256 characters.")
	}
	return nil
}

func configureOnboardingIMAP(
	app *runtime,
	command *accountAddCommand,
	candidate application.ProviderCandidate,
	alias string,
) (bool, error) {
	var err error
	var selected bool
	command.IMAPHost, command.IMAPPort, command.IMAPTLS, selected, err =
		configureOnboardingTLSEndpoint(
			app, "IMAP", candidateEndpoint(candidate, "imap"), 993, "implicit",
		)
	if err != nil || !selected {
		return false, err
	}
	command.SMTPHost, command.SMTPPort, command.SMTPTLS, selected, err =
		configureOnboardingTLSEndpoint(
			app, "SMTP", candidateEndpoint(candidate, "smtp"), 587, "starttls",
		)
	if err != nil || !selected {
		return false, err
	}
	return configureOnboardingMailCredential(app, command, alias, "Mail")
}

func configureOnboardingTLSEndpoint(
	app *runtime,
	service string,
	discovered string,
	defaultPort uint16,
	defaultMode string,
) (string, uint16, string, bool, error) {
	if discovered != "" {
		endpoint, err := accountTLSEndpoint("", 0, "", discovered, defaultMode)
		if err != nil {
			return "", 0, "", false, err
		}
		return endpoint.Host, endpoint.Port, endpoint.Mode, true, nil
	}
	host := ""
	selected, err := runSettingsInput(
		app,
		service+" host",
		"The server name from your provider, such as mail.example.com. IP addresses are not accepted.",
		&host,
		253,
		validateOnboardingHostname,
	)
	if err != nil || !selected {
		return "", 0, "", false, err
	}
	port := strconv.Itoa(int(defaultPort))
	selected, err = runSettingsInput(
		app,
		service+" port",
		"The port number from your provider.",
		&port,
		5,
		func(value string) error {
			parsed, parseErr := strconv.ParseUint(value, 10, 16)
			if parseErr != nil || parsed == 0 {
				return inputProblem("Type a port number from 1 to 65535.")
			}
			return nil
		},
	)
	if err != nil || !selected {
		return "", 0, "", false, err
	}
	mode, selected, err := runOnboardingSelect(
		app,
		service+" TLS mode",
		"Your provider says which one to use. Both are encrypted.",
		[]huh.Option[string]{
			huh.NewOption("Implicit TLS", "implicit").Selected(defaultMode == "implicit"),
			huh.NewOption("STARTTLS", "starttls").Selected(defaultMode == "starttls"),
		},
		onboardingCancel,
	)
	if err != nil || !selected {
		return "", 0, "", false, err
	}
	parsedPort, _ := strconv.ParseUint(port, 10, 16)
	if err := writeOnboardingAnswer(
		app, service+" server", net.JoinHostPort(host, port)+" ("+mode+")",
	); err != nil {
		return "", 0, "", false, err
	}
	return host, uint16(parsedPort), mode, true, nil
}

func validateOnboardingHostname(value string) error {
	if value == "" || strings.TrimSpace(value) != value ||
		strings.ContainsAny(value, "/:@\r\n\x00") || net.ParseIP(value) != nil ||
		!validOnboardingDNSName(value) {
		return inputProblem("Type a server name such as mail.example.com, without a port or user name.")
	}
	return nil
}

func validOnboardingDNSName(value string) bool {
	if len(value) > 253 || strings.HasPrefix(value, ".") || strings.HasSuffix(value, ".") {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if character != '-' && (character < 'a' || character > 'z') &&
				(character < 'A' || character > 'Z') &&
				(character < '0' || character > '9') {
				return false
			}
		}
	}
	return true
}

func configureOnboardingOAuth(
	app *runtime,
	command *accountAddCommand,
	alias string,
	provider domain.ProviderID,
) (bool, error) {
	name := "Microsoft"
	clientTitle := "Microsoft application (client) ID"
	keySuffix := "microsoft"
	switch provider {
	case domain.ProviderMicrosoftGraph:
	case domain.ProviderGoogle:
		name = "Google"
		clientTitle = "Google client ID"
		keySuffix = "google"
	case domain.ProviderMicrosoftOWA, domain.ProviderGoogleWeb,
		domain.ProviderJMAP, domain.ProviderIMAPSMTP, domain.ProviderCalDAV,
		domain.ProviderPOP3, domain.ProviderMicrosoftTasks,
		domain.ProviderTodoist, domain.ProviderGoogleTasks,
		domain.ProviderAppleReminders, domain.ProviderTickTick,
		domain.ProviderAnyDoMCP, domain.ProviderThings,
		domain.ProviderOmniFocus:
		return false, fmt.Errorf("provider %q has no guided OAuth setup", provider)
	default:
		return false, fmt.Errorf("provider %q has no guided OAuth setup", provider)
	}
	clientID := ""
	var selected bool
	var err error
	var clientCredential onboardingCredentialReference
	if provider == domain.ProviderGoogle {
		source, sourceSelected, sourceErr := runOnboardingSelect(
			app,
			"Google Desktop OAuth client",
			"Use the Desktop OAuth client from your own Google Cloud project. Guide: "+googleOAuthClientGuide,
			[]huh.Option[string]{
				huh.NewOption("Use the downloaded JSON file (recommended)", "import").Selected(true),
				huh.NewOption("Use a client already saved in the keyring or a helper", "existing"),
			},
			onboardingCancel,
		)
		if sourceErr != nil || !sourceSelected {
			return false, sourceErr
		}
		if source == "import" {
			path := ""
			selected, err = runSettingsInput(
				app,
				"Downloaded Google client JSON",
				"The full path of the JSON file you downloaded for the Desktop app.",
				&path,
				4096,
				validateOnboardingPath,
			)
			if err != nil || !selected {
				return false, err
			}
			client, parseErr := googleoauthclient.ParseFile(path)
			if parseErr != nil {
				return false, withHint(
					fmt.Errorf("read the Google client file: %w", parseErr),
					"download the Desktop app JSON again ("+googleOAuthClientGuide+"), then run corr setup",
				)
			}
			clientID = client.ID
			client.Close()
			clientCredential = onboardingCredentialReference{
				backend: "os-keyring",
				key:     alias + "-google-client",
			}
			command.onboardingGoogleClientPath = path
		} else {
			selected, err = runSettingsInput(
				app,
				"Google client ID",
				"The client_id of the Desktop app in your Google Cloud project.",
				&clientID,
				512,
				validateOnboardingOpaqueInput,
			)
			if err != nil || !selected {
				return false, err
			}
			clientCredential, selected, err = configureOnboardingCredentialReference(
				app,
				"Google Desktop OAuth client",
				alias+"-google-client",
			)
			if err != nil || !selected {
				return false, err
			}
		}
	} else {
		selected, err = runSettingsInput(
			app,
			clientTitle,
			"The Application (client) ID on the Overview page of your app registration. How to register: "+microsoftAppRegistrationGuide,
			&clientID,
			512,
			validateOnboardingOpaqueInput,
		)
		if err != nil || !selected {
			return false, err
		}
	}
	redirectURI := "http://127.0.0.1:0/callback"
	if provider == domain.ProviderGoogle {
		redirectURI = "http://127.0.0.1:0"
	}
	selected, err = runSettingsInput(
		app,
		"Redirect URI",
		"The redirect address registered for your app. Port 0 lets Corresync pick a free port when you sign in.",
		&redirectURI,
		2048,
		validateOnboardingLoopbackRedirect,
	)
	if err != nil || !selected {
		return false, err
	}
	clientLabel := "app registration"
	if provider == domain.ProviderGoogle {
		clientLabel = "OAuth client"
	}
	consent, err := runOnboardingConfirm(
		app,
		"Sign in with this "+name+" "+clientLabel+" later?",
		"Nothing opens now. You sign in after the account is added.",
		"Use "+clientLabel,
		"Cancel this account",
	)
	if err != nil || !consent {
		return false, err
	}
	command.OAuthClientID = clientID
	command.OAuthRedirectURI = redirectURI
	command.AuthorizationKey = alias + "-" + keySuffix
	command.ApproveOAuth = true
	if provider == domain.ProviderGoogle {
		command.OAuthClientSecretBackend = clientCredential.backend
		command.OAuthClientSecretKey = clientCredential.key
		command.ApproveOAuthClientSecret = true
	}
	command.onboardingOAuthProvider = provider
	return true, nil
}

func validateOnboardingOpaqueInput(value string) error {
	if value == "" || len(value) > 512 || strings.TrimSpace(value) != value ||
		strings.ContainsAny(value, "\r\n\x00") {
		return inputProblem("Type one line of up to 512 characters, with no spaces at either end.")
	}
	return nil
}

func validateOnboardingPath(value string) error {
	if value == "" || len(value) > 4096 || strings.ContainsAny(value, "\r\n\x00") {
		return inputProblem("Type the path of one file on this computer.")
	}
	return nil
}

func validateOnboardingLoopbackRedirect(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		parsed.Port() == "" {
		return inputProblem("Type an http://127.0.0.1 address with a port. Example: http://127.0.0.1:0/callback")
	}
	if _, err := strconv.ParseUint(parsed.Port(), 10, 16); err != nil {
		return inputProblem("Use a port from 0 to 65535. Example: http://127.0.0.1:0/callback")
	}
	return nil
}

func runOnboardingConfirm(
	app *runtime,
	title string,
	description string,
	affirmative string,
	negative string,
) (bool, error) {
	confirmed := false
	restoreFallback := prepareAccessibleFieldFallback(app, "n\n")
	defer restoreFallback()
	form := settingsForm(app, huh.NewConfirm().
		Title(title).
		Description(settingsHelpText(app, description, "Esc cancels.")).
		Affirmative(affirmative).
		Negative(negative).
		Value(&confirmed))
	if err := form.RunWithContext(app.context); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return false, nil
		}
		return false, fmt.Errorf("run onboarding confirmation: %w", err)
	}
	if err := app.context.Err(); err != nil {
		return false, err
	}
	if settingsInputExhausted(app) {
		return false, nil
	}
	return confirmed, nil
}

func runOnboardingAccountHandoff(
	app *runtime,
	registration onboardingRegistration,
) error {
	if registration.preset == onboardingPresetICloud {
		return runICloudAccountHandoff(app, registration)
	}
	if registration.googleClientPath != "" {
		return runGoogleAccountHandoff(app, registration)
	}
	account := registration.account
	authentication := accountAuthenticationKind(account)
	for {
		options := make([]huh.Option[string], 0, 4)
		switch authentication {
		case application.DiscoveryBrowserFirstParty,
			application.DiscoveryExplicitOAuth:
			options = append(options,
				huh.NewOption("Sign in now", "login"),
				huh.NewOption("Sign in now with text only, in this terminal", "login_terminal"),
			)
		case application.DiscoveryExternalCredential:
			options = append(options,
				huh.NewOption("Sign in now with the saved password", "login"),
				huh.NewOption("Test the connection without reading mail", "doctor_online"),
			)
		}
		options = append(options,
			huh.NewOption("Check the setup on this computer", "doctor"),
			huh.NewOption("Later", "finish").
				Selected(settingsAccessible(app)),
		)
		action, selected, err := runSettingsSelect(
			app,
			"Sign in to "+account.Alias+" now?",
			onboardingHandoffDescription(authentication),
			options,
		)
		if err != nil || !selected || action == "finish" {
			return err
		}
		switch action {
		case "login":
			err = (&loginCommand{Account: account.Alias}).Run(app)
		case "login_terminal":
			err = runSettingsTerminalLogin(app, account.Alias)
		case "doctor_online":
			err = (&doctorCommand{
				Account: account.Alias,
				Online:  true, ConnectionOnly: authentication == application.DiscoveryExternalCredential,
			}).Run(app)
		case "doctor":
			err = (&doctorCommand{Account: account.Alias}).Run(app)
		}
		if err != nil {
			return err
		}
	}
}

func runGoogleAccountHandoff(
	app *runtime,
	registration onboardingRegistration,
) error {
	if err := validateOnboardingPath(registration.googleClientPath); err != nil {
		return errors.New("the Google client file path is missing; run corr setup again")
	}
	if err := validateOnboardingCredentialKey(registration.googleClientKey); err != nil {
		return errors.New("the keyring entry name for the Google client is missing; run corr setup again")
	}
	clientPath := registration.googleClientPath
	for {
		options := make([]huh.Option[string], 0, 5)
		if clientPath != "" {
			options = append(options, huh.NewOption(
				"Save the downloaded client in this computer's keyring (recommended)",
				"import",
			).Selected(true))
			options = append(options, huh.NewOption(
				"Later", "finish",
			))
		} else {
			options = append(options,
				huh.NewOption("Sign in now in your browser", "login"),
				huh.NewOption("Check the setup on this computer", "doctor"),
				huh.NewOption("Later", "finish"),
			)
		}
		action, selected, err := runSettingsSelect(
			app,
			"Finish connecting "+registration.account.Alias,
			"The client file is copied only into this computer's keyring. Signing in to Google is a separate step.",
			options,
		)
		if err != nil || !selected || action == "finish" {
			return err
		}
		switch action {
		case "import":
			err = (&googleClientImportCommand{
				File: clientPath,
				Key:  registration.googleClientKey,
			}).Run(app)
			if err == nil {
				clientPath = ""
			}
		case "login":
			err = (&loginCommand{Account: registration.account.Alias}).Run(app)
		case "doctor":
			err = (&doctorCommand{Account: registration.account.Alias}).Run(app)
		}
		if err != nil {
			return err
		}
	}
}

func runICloudAccountHandoff(
	app *runtime,
	registration onboardingRegistration,
) error {
	account := registration.account
	credential := registration.credential
	if err := validateOnboardingCredentialKey(credential.key); err != nil {
		return errors.New("the keyring entry name for the iCloud password is missing; run corr setup again")
	}
	for {
		options := []huh.Option[string]{
			huh.NewOption("Open Apple's app-specific password page", "apple_password"),
		}
		if credential.backend == "os-keyring" {
			options = append(
				options,
				huh.NewOption("Save the password with your system's own prompt", "enroll"),
			)
		} else {
			options = append(
				options,
				huh.NewOption("Show how to save it with your credential helper", "helper"),
			)
		}
		options = append(options,
			huh.NewOption("Sign in now with the saved password", "login"),
			huh.NewOption("Test the connection without reading mail", "doctor_online"),
			huh.NewOption("Check the setup on this computer", "doctor"),
			huh.NewOption("Later", "finish").
				Selected(settingsAccessible(app)),
		)
		action, selected, err := runSettingsSelect(
			app,
			"Finish connecting "+account.Alias,
			"Create an app-specific password on Apple's page, then save it. Corresync never sees the password you type into the system prompt.",
			options,
		)
		if err != nil || !selected || action == "finish" {
			return err
		}
		switch action {
		case "apple_password":
			err = openOnboardingURL(app, iCloudAccountManagementURL)
		case "enroll":
			err = runICloudCredentialEnrollment(app, credential)
		case "helper":
			err = writeICloudHelperHandoff(app, credential.key)
		case "login":
			err = (&loginCommand{Account: account.Alias}).Run(app)
		case "doctor_online":
			err = (&doctorCommand{
				Account: account.Alias, Online: true, ConnectionOnly: true,
			}).Run(app)
		case "doctor":
			err = (&doctorCommand{Account: account.Alias}).Run(app)
		}
		if err != nil {
			return err
		}
	}
}

func openOnboardingURL(app *runtime, target string) error {
	parsed, err := url.Parse(target)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil ||
		parsed.Host != "account.apple.com" || parsed.RawQuery != "" ||
		parsed.Fragment != "" {
		return errors.New("refusing to open an unexpected web address")
	}
	name, arguments, err := feedbackBrowserCommand(app.info.OS, parsed.String())
	if err != nil {
		return err
	}
	return app.runCommand(app.context, app.stdout, app.stderr, name, arguments...)
}

func runICloudCredentialEnrollment(
	app *runtime,
	credential onboardingCredentialReference,
) error {
	if credential.backend != "os-keyring" {
		return errors.New("this password is saved with your credential helper, not the system prompt")
	}
	name, arguments, err := iCloudCredentialEnrollmentCommand(
		app.info.OS,
		credential.key,
	)
	if err != nil {
		return err
	}
	if err := writeICloudCredentialPrompt(app); err != nil {
		return err
	}
	if err := app.runCommand(
		app.context,
		app.stdout,
		app.stderr,
		name,
		arguments...,
	); err != nil {
		return fmt.Errorf("the system password prompt did not finish: %w", err)
	}
	return writeICloudCredentialStored(app)
}

func iCloudCredentialEnrollmentCommand(
	goos string,
	key string,
) (string, []string, error) {
	if err := validateOnboardingCredentialKey(key); err != nil {
		return "", nil, err
	}
	switch goos {
	case "darwin":
		return "/usr/bin/security", []string{
			"add-generic-password", "-U",
			"-s", iCloudKeyringService,
			"-a", key,
			"-l", "Corresync iCloud",
			"-w",
		}, nil
	case "linux":
		return "secret-tool", []string{
			"store", "--label=Corresync iCloud",
			"service", iCloudKeyringService,
			"username", key,
		}, nil
	case "windows":
		return "cmdkey.exe", []string{
			"/generic:" + iCloudKeyringService + ":" + key,
			"/user:" + key,
		}, nil
	default:
		return "", nil, fmt.Errorf(
			"saving a password with the system prompt is not supported on %s; choose a credential helper instead",
			goos,
		)
	}
}

func writeICloudCredentialPrompt(app *runtime) error {
	view := newConsoleView(app, app.stdout, true)
	_, err := view.printf(
		"\n%s  %s\n   %s\n   %s\n",
		view.info(),
		view.strong("Save the app-specific password"),
		view.muted("Paste the password into your system's prompt that opens next."),
		view.muted("Corresync never sees it: it is not in the settings file, the command line, or any log."),
	)
	return err
}

func writeICloudCredentialStored(app *runtime) error {
	view := newConsoleView(app, app.stdout, true)
	_, err := view.printf(
		"%s  %s\n   %s\n",
		view.success(),
		view.strong("Password saved"),
		view.muted("Your system saved it; Corresync only learned that the prompt finished."),
	)
	return err
}

func writeICloudHelperHandoff(app *runtime, key string) error {
	view := newConsoleView(app, app.stdout, true)
	_, err := view.printf(
		"\n%s  %s\n   %s\n   %s\n",
		view.info(),
		view.strong("Save it with your credential helper"),
		view.muted("Use the helper's own tool to save it under the name "+sanitizeCell(key, 256)+"."),
		view.muted("Corresync asks the helper for it by that name when the account signs in."),
	)
	return err
}

func accountAuthenticationKind(
	account application.AccountView,
) application.DiscoveryAuthentication {
	for _, route := range []*application.AccountRouteView{account.Mail, account.Calendar} {
		if route == nil {
			continue
		}
		switch route.Provider {
		case domain.ProviderJMAP, domain.ProviderIMAPSMTP, domain.ProviderCalDAV:
			return application.DiscoveryExternalCredential
		case domain.ProviderGoogle, domain.ProviderMicrosoftGraph:
			return application.DiscoveryExplicitOAuth
		case domain.ProviderMicrosoftOWA, domain.ProviderGoogleWeb,
			domain.ProviderPOP3, domain.ProviderMicrosoftTasks,
			domain.ProviderTodoist, domain.ProviderGoogleTasks,
			domain.ProviderAppleReminders, domain.ProviderTickTick,
			domain.ProviderAnyDoMCP, domain.ProviderThings,
			domain.ProviderOmniFocus:
		}
	}
	if account.Tasks != nil && (account.Tasks.Provider == domain.ProviderMicrosoftGraph ||
		account.Tasks.Provider == domain.ProviderTodoist ||
		account.Tasks.Provider == domain.ProviderGoogleTasks ||
		account.Tasks.Provider == domain.ProviderTickTick) {
		return application.DiscoveryExplicitOAuth
	}
	if account.Messages != nil &&
		account.Messages.Route == domain.MessagingRouteTeamsGraph {
		return application.DiscoveryExplicitOAuth
	}
	return application.DiscoveryBrowserFirstParty
}

func onboardingHandoffDescription(
	authentication application.DiscoveryAuthentication,
) string {
	if authentication == application.DiscoveryExternalCredential {
		return "Save the password in the keyring or helper first, then sign in. You can also do this later."
	}
	if authentication == application.DiscoveryExplicitOAuth {
		return "Sign in opens the provider's page in your browser and asks you to allow access."
	}
	return "Sign in opens Outlook in a browser. On a computer without a display, it shows a login view you open over SSH."
}

func providerDisplayName(provider domain.ProviderID) string {
	switch provider {
	case domain.ProviderMicrosoftOWA:
		return "Outlook Web"
	case domain.ProviderMicrosoftGraph:
		return "Microsoft Graph"
	case domain.ProviderGoogle:
		return "Gmail / Google"
	case domain.ProviderJMAP:
		return "JMAP"
	case domain.ProviderIMAPSMTP:
		return "IMAP / SMTP"
	case domain.ProviderCalDAV:
		return "CalDAV"
	case domain.ProviderGoogleWeb:
		return "Google Web"
	case domain.ProviderPOP3:
		return "POP3"
	case domain.ProviderMicrosoftTasks:
		return "Microsoft To Do (web)"
	case domain.ProviderTodoist:
		return "Todoist"
	case domain.ProviderGoogleTasks:
		return "Google Tasks"
	case domain.ProviderAppleReminders:
		return "Apple Reminders"
	case domain.ProviderTickTick:
		return "TickTick"
	case domain.ProviderAnyDoMCP:
		return "Any.do MCP"
	case domain.ProviderThings:
		return "Things"
	case domain.ProviderOmniFocus:
		return "OmniFocus"
	default:
		return string(provider)
	}
}

func writeOnboardingAccountReview(
	app *runtime,
	review application.AccountChangeReview,
) error {
	view := newConsoleView(app, app.stdout, true)
	if _, err := view.printf(
		"\n%s  %s\n\n  %-16s %s\n  %-16s %s\n  %-16s %s\n  %-16s %s\n  %-16s %s\n  %-16s %s\n  %-16s %s\n",
		view.info(),
		view.strong("Review account before adding"),
		"Name", sanitizeCell(review.Alias, 64),
		"Address", sanitizeCell(review.Address, 254),
		"Mail", onboardingProviderValue(review.MailProvider),
		"Calendar", onboardingProviderValue(review.CalendarProvider),
		"Tasks", onboardingProviderValue(review.TaskProvider),
		"Messages", onboardingMessagingProviderValue(review.MessagingProvider),
		"Default", yesNo(review.MakesDefault),
	); err != nil {
		return err
	}
	for _, route := range []struct {
		service string
		view    *application.AccountRouteView
	}{
		{service: "Mail", view: review.Mail},
		{service: "Calendar", view: review.Calendar},
		{service: "Tasks", view: review.Tasks},
	} {
		if route.view == nil {
			continue
		}
		if route.view.Identity != "" {
			if _, err := view.printf(
				"  %-16s %s\n",
				sanitizeCell(route.service+" sign-in", 16),
				sanitizeCell(route.view.Identity, 320),
			); err != nil {
				return err
			}
		}
		for _, endpoint := range route.view.Endpoints {
			label := sanitizeCell(route.service+" "+endpoint.Kind, 16)
			if _, err := view.printf(
				"  %-16s %s\n",
				label,
				sanitizeCell(endpoint.Value, 2048),
			); err != nil {
				return err
			}
		}
	}
	for _, credential := range review.Credentials {
		if _, err := view.printf(
			"  %-16s %s · %s entry %s\n",
			"Saved secret",
			credential.Service,
			credential.Backend, sanitizeCell(credential.Key, 256),
		); err != nil {
			return err
		}
	}
	if review.Messages != nil {
		if _, err := view.printf(
			"  %-16s %s · %s\n  %-16s %s\n",
			"Messaging route", review.Messages.Provider, review.Messages.Route,
			"Workspace", sanitizeCell(review.Messages.WorkspaceID, 4096),
		); err != nil {
			return err
		}
	}
	_, err := view.printf(
		"\n   %s\n",
		view.muted("Nothing has signed in yet. You sign in after the account is added."),
	)
	return err
}

func onboardingProviderValue(provider domain.ProviderID) string {
	if provider == "" {
		return "not configured"
	}
	return providerDisplayName(provider)
}

func onboardingMessagingProviderValue(provider domain.MessagingProviderID) string {
	if provider == "" {
		return "not configured"
	}
	return string(provider)
}

// writeOnboardingWelcome is the one line shown above the first question.
func writeOnboardingWelcome(app *runtime, accounts []application.AccountView) error {
	view := newConsoleView(app, app.stdout, true)
	text := "Add your email account. Corresync finds how to connect; you sign in at the end."
	if len(accounts) > 0 {
		names := make([]string, 0, len(accounts))
		for _, account := range accounts {
			names = append(names, sanitizeCell(account.Alias, 64))
		}
		text = "Accounts on this computer: " + strings.Join(names, ", ")
	}
	wrapped := wrapText(text, app.textWidth(app.stdout), "   ")
	_, err := view.printf(
		"\n%s  %s\n\n",
		view.info(),
		view.strong(strings.TrimPrefix(wrapped, "   ")),
	)
	return err
}

// writeOnboardingAnswer leaves a one-line record of an answered question, so
// the screen shows what was chosen after the prompt itself has gone.
func writeOnboardingAnswer(app *runtime, label string, value string) error {
	if settingsAccessible(app) {
		// Line-oriented prompts already leave the answer on screen.
		return nil
	}
	view := newConsoleView(app, app.stdout, true)
	_, err := view.printf(
		"  %s %s\n",
		view.muted(label+":"),
		sanitizeCell(value, 2048),
	)
	return err
}

func writeOnboardingAccountAdded(
	app *runtime,
	account application.AccountView,
) error {
	view := newConsoleView(app, app.stdout, true)
	_, err := view.printf(
		"\n%s  %s\n",
		view.success(),
		view.strong("Added "+sanitizeCell(account.Alias, 64)+". It is not signed in yet."),
	)
	return err
}

func writeOnboardingCancelled(
	app *runtime,
	added int,
	createdConfig bool,
) error {
	view := newConsoleView(app, app.stdout, true)
	detail := "No account was added this time."
	if added > 0 {
		detail = fmt.Sprintf("The %d account(s) you added are kept.", added)
	}
	text := detail + " Run corr setup again when you're ready."
	if createdConfig {
		if path, err := app.resolvedConfigPath(); err == nil {
			text += "\nSettings file: " + sanitizeCell(path, 2048)
		}
	}
	_, err := view.printf(
		"\n%s  %s\n%s\n",
		view.info(), view.strong("Setup stopped"),
		view.muted(wrapText(text, app.textWidth(app.stdout), "   ")),
	)
	return err
}
