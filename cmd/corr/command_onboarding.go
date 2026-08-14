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
}

type onboardingRegistration struct {
	account    application.AccountView
	preset     string
	credential onboardingCredentialReference
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
			"Continue setup",
			"Existing accounts stay unchanged unless you explicitly add or manage one.",
			[]huh.Option[string]{
				huh.NewOption("Continue to agent setup", onboardingContinue).
					Selected(settingsAccessible(app)),
				huh.NewOption("Add another account", onboardingAdd),
				huh.NewOption("Review configured accounts", onboardingReview),
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
					"guided setup stopped after %d completed account(s); those accounts remain configured: %w",
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
				"What would you like to do next?",
				"Completed accounts stay configured if a later step is cancelled.",
				[]huh.Option[string]{
					huh.NewOption("Add another account", onboardingAdd),
					huh.NewOption("Review configured accounts", onboardingReview),
					huh.NewOption("Continue to agent setup", onboardingContinue).
						Selected(settingsAccessible(app)),
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
		"Used only for credential-free discovery and the local account route. No sign-in starts.",
		&address,
		254,
		validateSettingsEmailAddress,
	)
	if err != nil || !selected {
		return onboardingRegistration{}, false, err
	}
	discovery, err := discoverer.Discover(app.context, address)
	if err != nil {
		return onboardingRegistration{}, false, err
	}
	if err := writeOnboardingDiscovery(app, discovery); err != nil {
		return onboardingRegistration{}, false, err
	}
	plans := onboardingRoutePlans(discovery)
	if len(plans) == 0 {
		if onboardingDiscoveredPendingGoogle(discovery) {
			return onboardingRegistration{}, false, googleOAuthPendingError(
				"Gmail or Google Calendar was found.",
			)
		}
		return onboardingRegistration{}, false, errors.New(
			"discovery found no available route; use `corr account add ADDRESS --help` for explicit advanced setup",
		)
	}
	plan, selected, err := selectOnboardingRoutePlan(app, plans)
	if err != nil || !selected {
		return onboardingRegistration{}, false, err
	}

	alias := discovery.Address[:strings.LastIndexByte(discovery.Address, '@')]
	selected, err = runSettingsInput(
		app,
		"Local account name",
		"A short editable name used to select this account; it is not a login or secret.",
		&alias,
		64,
		func(value string) error { return domain.AccountAlias(value).Validate() },
	)
	if err != nil || !selected {
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
		plan,
		discovery,
		alias,
	)
	if err != nil || !selected {
		return onboardingRegistration{}, false, err
	}
	selectedCandidate := onboardingSelectedCandidate(plan)
	mailRoute, calendarRoute, _, err := routing.routes(
		selectedCandidate,
		discovery,
	)
	if err != nil {
		return onboardingRegistration{}, false, err
	}
	input := application.AccountAddInput{
		Alias: alias, Address: discovery.Address,
		Mail: mailRoute, Calendar: calendarRoute, Default: makeDefault,
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
		"Add "+alias+" to this device?",
		"This writes only secret-free local route metadata. Authentication remains a separate action.",
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
		account: account,
		preset:  plan.preset,
		credential: onboardingCredentialReference{
			backend: routing.CredentialBackend,
			key:     routing.CredentialKey,
		},
	}, true, nil
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
				plan.label = "iCloud Mail + Calendar · app-specific password · " +
					fmt.Sprintf("%d/100 evidence", onboardingPlanConfidence(mail, calDAV))
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

func onboardingDiscoveredPendingGoogle(
	discovery application.AccountDiscoveryResult,
) bool {
	if rollout.GoogleOAuthApproved {
		return false
	}
	for _, candidate := range discovery.Candidates {
		if candidate.Provider == domain.ProviderGoogle {
			return true
		}
	}
	return false
}

func newOnboardingRoutePlan(
	mailCandidate *application.ProviderCandidate,
	calendarCandidate *application.ProviderCandidate,
) onboardingRoutePlan {
	parts := make([]string, 0, 2)
	if mailCandidate != nil {
		parts = append(parts, providerDisplayName(mailCandidate.Provider)+" Mail")
	}
	if calendarCandidate != nil {
		parts = append(parts, providerDisplayName(calendarCandidate.Provider)+" Calendar")
	}
	authentication := onboardingAuthentication(mailCandidate, calendarCandidate)
	confidence := onboardingPlanConfidence(mailCandidate, calendarCandidate)
	return onboardingRoutePlan{
		mail: mailCandidate, calendar: calendarCandidate,
		label: strings.Join(parts, " + ") + " · " + authentication +
			fmt.Sprintf(" · %d/100 evidence", confidence),
	}
}

func onboardingAuthentication(
	candidates ...*application.ProviderCandidate,
) string {
	for _, candidate := range candidates {
		if candidate != nil &&
			candidate.Authentication == application.DiscoveryExternalCredential {
			return "external credential"
		}
	}
	for _, candidate := range candidates {
		if candidate != nil && candidate.Authentication == application.DiscoveryExplicitOAuth {
			return "provider authorization"
		}
	}
	return "provider-owned browser sign-in"
}

func onboardingPlanConfidence(candidates ...*application.ProviderCandidate) int {
	confidence := 100
	for _, candidate := range candidates {
		if candidate != nil && candidate.Confidence < confidence {
			confidence = candidate.Confidence
		}
	}
	return confidence
}

func selectOnboardingRoutePlan(
	app *runtime,
	plans []onboardingRoutePlan,
) (onboardingRoutePlan, bool, error) {
	if len(plans) == 1 && !onboardingPlanRequiresSelection(plans[0]) {
		return plans[0], true, nil
	}
	options := make([]huh.Option[int], 0, len(plans))
	for index, plan := range plans {
		options = append(
			options,
			huh.NewOption(plan.label, index).Selected(plan.preset == onboardingPresetICloud),
		)
	}
	index, selected, err := runOnboardingSelect(
		app,
		"Mail and calendar route",
		"Choose the services you recognize. No browser or credential access starts here.",
		options,
		-1,
	)
	if err != nil || !selected {
		return onboardingRoutePlan{}, false, err
	}
	return plans[index], true, nil
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
		"The default is used only when a command omits --account.",
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
		return configureICloudOnboardingRoutes(app, command, discovery, alias)
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
				app, "Outlook Web origin", "Provider-owned HTTPS origin.",
				candidateEndpoint(*plan.mailOrCalendar(provider), "origin"),
			)
		case domain.ProviderJMAP:
			command.SessionURL, selected, err = onboardingHTTPSEndpoint(
				app, "JMAP session URL", "Credential-free discovery resource.",
				candidateHTTPSEndpoint(*plan.mail, "jmap"),
			)
			if err == nil && selected {
				selected, err = configureOnboardingMailCredential(app, &command, alias, "JMAP")
			}
		case domain.ProviderIMAPSMTP:
			selected, err = configureOnboardingIMAP(app, &command, *plan.mail, alias)
		case domain.ProviderCalDAV:
			command.CalDAVEndpoint, selected, err = onboardingHTTPSEndpoint(
				app, "CalDAV endpoint", "HTTPS discovery endpoint for your calendar service.",
				candidateHTTPSEndpoint(*plan.calendar, "caldav"),
			)
			if err == nil && selected {
				selected, err = configureOnboardingCalendarCredential(app, &command, alias)
			}
		case domain.ProviderMicrosoftGraph:
			selected, err = configureOnboardingOAuth(app, &command, alias)
		case domain.ProviderGoogle:
			return accountAddCommand{}, false, googleOAuthPendingError(
				"Gmail or Google Calendar was selected.",
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

func configureICloudOnboardingRoutes(
	app *runtime,
	command accountAddCommand,
	discovery application.AccountDiscoveryResult,
	alias string,
) (accountAddCommand, bool, error) {
	if err := writeICloudOnboardingGuidance(app, discovery.Address); err != nil {
		return accountAddCommand{}, false, err
	}
	calendarUsername := discovery.Address
	selected, err := runSettingsInput(
		app,
		"Apple Account email for Calendar",
		"Keep the discovered address unless your Apple Account sign-in uses another email.",
		&calendarUsername,
		254,
		validateSettingsEmailAddress,
	)
	if err != nil || !selected {
		return accountAddCommand{}, false, err
	}
	reference, selected, err := configureOnboardingCredentialReference(
		app,
		"iCloud Mail and Calendar",
		alias+"-icloud",
	)
	if err != nil || !selected {
		return accountAddCommand{}, false, err
	}
	command.Provider = string(domain.ProviderIMAPSMTP)
	command.CalendarProvider = string(domain.ProviderCalDAV)
	command.IMAPHost = "imap.mail.me.com"
	command.IMAPPort = 993
	command.IMAPTLS = "implicit"
	command.SMTPHost = "smtp.mail.me.com"
	command.SMTPPort = 587
	command.SMTPTLS = "starttls"
	command.CalDAVEndpoint = "https://caldav.icloud.com:443/"
	command.Username = discovery.Address
	command.CalendarUsername = calendarUsername
	command.CredentialBackend = reference.backend
	command.CredentialKey = reference.key
	command.ApproveCredential = true
	command.CalendarCredentialBackend = reference.backend
	command.CalendarCredentialKey = reference.key
	command.ApproveCalendarCredential = true
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

func onboardingHTTPSEndpoint(
	app *runtime,
	title string,
	description string,
	discovered string,
) (string, bool, error) {
	if discovered != "" {
		return discovered, true, nil
	}
	value := "https://"
	selected, err := runSettingsInput(
		app, title, description, &value, 2048, validateOnboardingHTTPSURL,
	)
	return value, selected, err
}

func validateOnboardingHTTPSURL(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery ||
		parsed.Fragment != "" || strings.HasSuffix(parsed.Host, ":") {
		return errors.New("enter one credential-free HTTPS URL without a query or fragment")
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
	backend, selected, err := chooseOnboardingCredentialBackend(app, service+" credential")
	if err != nil || !selected {
		return onboardingCredentialReference{}, false, err
	}
	key := defaultKey
	selected, err = runSettingsInput(
		app,
		service+" credential handle",
		"Name of an existing external secret. Corresync never asks for or stores its value.",
		&key,
		256,
		validateOnboardingCredentialKey,
	)
	if err != nil || !selected {
		return onboardingCredentialReference{}, false, err
	}
	consent, err := runOnboardingConfirm(
		app,
		"Allow this account to use that external "+service+" credential?",
		"The handle is stored in config; the secret remains in the selected external store.",
		"Allow reference",
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
		"Choose where a credential you control is already stored.",
		[]huh.Option[string]{
			huh.NewOption("OS keyring (recommended)", "os-keyring"),
			huh.NewOption("Approved credential helper", "helper"),
		},
		onboardingCancel,
	)
}

func validateOnboardingCredentialKey(value string) error {
	if value == "" || len(value) > 256 || strings.ContainsAny(value, "\r\n\x00") {
		return errors.New("enter a bounded external credential handle")
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
			app, "SMTP Submission", candidateEndpoint(candidate, "smtp"), 587, "starttls",
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
		"TLS server hostname from your provider; IP literals are not accepted.",
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
		"Encrypted provider endpoint port.",
		&port,
		5,
		func(value string) error {
			parsed, parseErr := strconv.ParseUint(value, 10, 16)
			if parseErr != nil || parsed == 0 {
				return errors.New("enter a port from 1 through 65535")
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
		"Both modes require valid TLS; plaintext is never available.",
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
	return host, uint16(parsedPort), mode, true, nil
}

func validateOnboardingHostname(value string) error {
	if value == "" || strings.TrimSpace(value) != value ||
		strings.ContainsAny(value, "/:@\r\n\x00") || net.ParseIP(value) != nil ||
		!validOnboardingDNSName(value) {
		return errors.New("enter one DNS hostname")
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
) (bool, error) {
	clientID := ""
	selected, err := runSettingsInput(
		app,
		"Microsoft public-client ID",
		"Use a public OAuth client registration you are authorized to operate; no client secret is accepted.",
		&clientID,
		512,
		validateOnboardingOpaqueInput,
	)
	if err != nil || !selected {
		return false, err
	}
	redirectURI := "http://127.0.0.1:0/callback"
	selected, err = runSettingsInput(
		app,
		"Registered loopback redirect URI",
		"Must use plain HTTP on 127.0.0.1 with a registered path and no query or fragment.",
		&redirectURI,
		2048,
		validateOnboardingLoopbackRedirect,
	)
	if err != nil || !selected {
		return false, err
	}
	consent, err := runOnboardingConfirm(
		app,
		"Use this public client for an explicit Microsoft authorization later?",
		"Discovery still does not open a browser. Sign-in is offered only after the account is added.",
		"Use public client",
		"Cancel this account",
	)
	if err != nil || !consent {
		return false, err
	}
	command.OAuthClientID = clientID
	command.OAuthRedirectURI = redirectURI
	command.AuthorizationKey = alias + "-graph"
	command.ApproveOAuth = true
	return true, nil
}

func validateOnboardingOpaqueInput(value string) error {
	if value == "" || len(value) > 512 || strings.TrimSpace(value) != value ||
		strings.ContainsAny(value, "\r\n\x00") {
		return errors.New("enter one bounded value")
	}
	return nil
}

func validateOnboardingLoopbackRedirect(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		parsed.Port() == "" {
		return errors.New("enter a registered http://127.0.0.1 loopback URI with an explicit port")
	}
	if _, err := strconv.ParseUint(parsed.Port(), 10, 16); err != nil {
		return errors.New("enter a loopback URI with a port from 0 through 65535")
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
		Description(description+" Esc cancels.").
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
	account := registration.account
	authentication := accountAuthenticationKind(account)
	for {
		options := make([]huh.Option[string], 0, 4)
		switch authentication {
		case application.DiscoveryBrowserFirstParty,
			application.DiscoveryExplicitOAuth:
			options = append(options,
				huh.NewOption("Sign in now in the provider-owned browser", "login"),
				huh.NewOption("Sign in now through the text-only browser relay", "login_terminal"),
			)
		case application.DiscoveryExternalCredential:
			options = append(options,
				huh.NewOption("Authenticate with the external credential now", "login"),
				huh.NewOption("Run a content-free connection check", "doctor_online"),
			)
		}
		options = append(options,
			huh.NewOption("Run local setup checks", "doctor"),
			huh.NewOption("Finish this account later", "finish").
				Selected(settingsAccessible(app)),
		)
		action, selected, err := runSettingsSelect(
			app,
			"Account added · "+account.Alias,
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

func runICloudAccountHandoff(
	app *runtime,
	registration onboardingRegistration,
) error {
	account := registration.account
	credential := registration.credential
	if err := validateOnboardingCredentialKey(credential.key); err != nil {
		return errors.New("iCloud credential handoff is missing its reviewed handle")
	}
	for {
		options := []huh.Option[string]{
			huh.NewOption("Open Apple's app-specific password page", "apple_password"),
		}
		if credential.backend == "os-keyring" {
			options = append(
				options,
				huh.NewOption("Store it with the OS credential prompt", "enroll"),
			)
		} else {
			options = append(
				options,
				huh.NewOption("Show the approved-helper handoff", "helper"),
			)
		}
		options = append(options,
			huh.NewOption("Authenticate Mail and Calendar now", "login"),
			huh.NewOption("Run a content-free connection check", "doctor_online"),
			huh.NewOption("Run local setup checks", "doctor"),
			huh.NewOption("Finish this account later", "finish").
				Selected(settingsAccessible(app)),
		)
		action, selected, err := runSettingsSelect(
			app,
			"Secure iCloud handoff · "+account.Alias,
			"Create one app-specific password after 2FA, then let an external credential owner store it. Corresync never reads the prompt.",
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
		return errors.New("refusing to open an unexpected onboarding URL")
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
		return errors.New("credential enrollment is owned by the configured helper")
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
		return fmt.Errorf("OS credential prompt failed: %w", err)
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
			"OS credential enrollment is unsupported on %s; select an approved helper instead",
			goos,
		)
	}
}

func writeICloudCredentialPrompt(app *runtime) error {
	view := newConsoleView(app, app.stdout, true)
	_, err := view.printf(
		"\n%s  %s\n   %s\n   %s\n",
		view.info(),
		view.strong("OS credential prompt"),
		view.muted("Paste the Apple app-specific password into the operating system's prompt."),
		view.muted("Its value is absent from Corresync config, arguments, environment, output, and logs."),
	)
	return err
}

func writeICloudCredentialStored(app *runtime) error {
	view := newConsoleView(app, app.stdout, true)
	_, err := view.printf(
		"%s  %s\n   %s\n",
		view.success(),
		view.strong("External credential stored"),
		view.muted("The OS-owned store completed the prompt; Corresync received only its exit status."),
	)
	return err
}

func writeICloudHelperHandoff(app *runtime, key string) error {
	view := newConsoleView(app, app.stdout, true)
	_, err := view.printf(
		"\n%s  %s\n   %s\n   %s\n",
		view.info(),
		view.strong("Approved credential helper"),
		view.muted("Use the helper's own enrollment UI for the reviewed handle "+sanitizeCell(key, 256)+"."),
		view.muted("The helper must return that credential only for its existing version 1 get request."),
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
	return application.DiscoveryBrowserFirstParty
}

func onboardingHandoffDescription(
	authentication application.DiscoveryAuthentication,
) string {
	if authentication == application.DiscoveryExternalCredential {
		return "The route references only an external credential handle. Store its secret outside Corresync before checking the provider, or finish later."
	}
	return "Authentication is still off. Starting it is a separate explicit action owned by the provider."
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

func writeOnboardingWelcome(app *runtime) error {
	view := newConsoleView(app, app.stdout, true)
	_, err := view.printf(
		"\n%s  %s\n   %s\n   %s\n\n",
		view.info(),
		view.strong("Guided account setup"),
		view.muted("Discover a route, review secret-free local settings, then choose authentication separately."),
		view.muted("Esc cancels the current account; accounts already completed remain configured."),
	)
	return err
}

func writeICloudOnboardingGuidance(app *runtime, address string) error {
	view := newConsoleView(app, app.stdout, true)
	_, err := view.printf(
		"\n%s  %s\n   %s\n   %s\n   %s\n\n",
		view.info(),
		view.strong("iCloud Mail + Calendar"),
		view.muted("Apple requires two-factor authentication before you can create an app-specific password."),
		view.muted("Mail uses "+sanitizeCell(address, 254)+" as the full username; Calendar can use a different Apple Account email."),
		view.muted("Nothing is opened and no credential is requested until you explicitly choose the post-add handoff."),
	)
	return err
}

func writeOnboardingDiscovery(
	app *runtime,
	discovery application.AccountDiscoveryResult,
) error {
	view := newConsoleView(app, app.stdout, true)
	if _, err := view.printf(
		"\n%s  %s\n   %s\n",
		view.info(),
		view.strong("Provider candidates for "+sanitizeCell(discovery.Domain, 253)),
		view.muted("Evidence is credential-free and does not prove account permissions."),
	); err != nil {
		return err
	}
	for _, candidate := range discovery.Candidates {
		availability := "available"
		if !candidate.Available {
			availability = "not available in this build"
			if candidate.Provider == domain.ProviderGoogle && !rollout.GoogleOAuthApproved {
				availability = "coming soon · production OAuth approval pending"
			}
		}
		if _, err := view.printf(
			"   %-20s %3d/100 · %s\n",
			sanitizeCell(providerDisplayName(candidate.Provider), 20),
			candidate.Confidence,
			view.muted(availability),
		); err != nil {
			return err
		}
	}
	return nil
}

func writeOnboardingAccountReview(
	app *runtime,
	review application.AccountChangeReview,
) error {
	view := newConsoleView(app, app.stdout, true)
	if _, err := view.printf(
		"\n%s  %s\n\n  %-16s %s\n  %-16s %s\n  %-16s %s\n  %-16s %s\n  %-16s %t\n",
		view.info(),
		view.strong("Review account before adding"),
		"Local name", sanitizeCell(review.Alias, 64),
		"Address", sanitizeCell(review.Address, 254),
		"Mail", onboardingProviderValue(review.MailProvider),
		"Calendar", onboardingProviderValue(review.CalendarProvider),
		"Default", review.MakesDefault,
	); err != nil {
		return err
	}
	for _, route := range []struct {
		service string
		view    *application.AccountRouteView
	}{
		{service: "Mail", view: review.Mail},
		{service: "Calendar", view: review.Calendar},
	} {
		if route.view == nil {
			continue
		}
		if route.view.Identity != "" {
			if _, err := view.printf(
				"  %-18s %s\n",
				sanitizeCell(route.service+" identity", 18),
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
			"  %-16s %s · %s · approved handle reference\n",
			"Credential",
			credential.Service,
			credential.Backend,
		); err != nil {
			return err
		}
	}
	_, err := view.printf(
		"\n   %s\n   %s\n   %s\n",
		view.muted("No browser, credential value, provider API, or administrator flow has been opened."),
		view.muted("Capabilities are observed only after sign-in; technical routes remain available in account details."),
		view.command("After adding: corr account show "+shellSingleQuote(review.Alias)),
	)
	return err
}

func onboardingProviderValue(provider domain.ProviderID) string {
	if provider == "" {
		return "not configured"
	}
	return providerDisplayName(provider)
}

func writeOnboardingAccountAdded(
	app *runtime,
	account application.AccountView,
) error {
	view := newConsoleView(app, app.stdout, true)
	_, err := view.printf(
		"\n%s  %s\n   %s\n",
		view.success(),
		view.strong("Account "+sanitizeCell(account.Alias, 64)+" added"),
		view.muted("The secret-free route is durable; authentication and monitoring are still off."),
	)
	return err
}

func writeOnboardingCancelled(
	app *runtime,
	added int,
	createdConfig bool,
) error {
	view := newConsoleView(app, app.stdout, true)
	detail := fmt.Sprintf("No current account was added. %d earlier account(s) remain configured.", added)
	if added == 0 && createdConfig {
		detail = "No account was added. The new empty, secret-free configuration remains available."
	}
	_, err := view.printf(
		"\n%s  %s\n   %s\n",
		view.info(), view.strong("Guided setup stopped"), view.muted(detail),
	)
	return err
}
