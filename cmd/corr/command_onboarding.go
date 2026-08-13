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
)

type onboardingRoutePlan struct {
	mail     *application.ProviderCandidate
	calendar *application.ProviderCandidate
	label    string
}

func runGuidedAccountSetup(app *runtime, createdConfig bool) error {
	if err := writeOnboardingWelcome(app); err != nil {
		return err
	}
	added := 0
	for {
		account, completed, err := runAccountRegistrationWizard(app)
		if err != nil {
			if added > 0 {
				return fmt.Errorf(
					"guided setup stopped after %d completed account(s); those accounts remain configured: %w",
					added,
					err,
				)
			}
			return err
		}
		if !completed {
			return writeOnboardingCancelled(app, added, createdConfig)
		}
		added++
		if err := runOnboardingAccountHandoff(app, account); err != nil {
			return fmt.Errorf(
				"account %q remains configured, but its post-add action failed: %w",
				account.Alias,
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
					return err
				}
				return writeOnboardingNextSteps(app, added)
			}
			if action == onboardingAdd {
				break
			}
			if err := (&accountListCommand{}).Run(app); err != nil {
				return err
			}
		}
	}
}

func runAccountRegistrationWizard(
	app *runtime,
) (application.AccountView, bool, error) {
	accounts, discoverer, err := app.accountServices()
	if err != nil {
		return application.AccountView{}, false, err
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
		return application.AccountView{}, false, err
	}
	discovery, err := discoverer.Discover(app.context, address)
	if err != nil {
		return application.AccountView{}, false, err
	}
	if err := writeOnboardingDiscovery(app, discovery); err != nil {
		return application.AccountView{}, false, err
	}
	plans := onboardingRoutePlans(discovery)
	if len(plans) == 0 {
		if onboardingDiscoveredPendingGoogle(discovery) {
			return application.AccountView{}, false, googleOAuthPendingError(
				"Gmail or Google Calendar was found.",
			)
		}
		return application.AccountView{}, false, errors.New(
			"discovery found no available route; use `corr account add ADDRESS --help` for explicit advanced setup",
		)
	}
	plan, selected, err := selectOnboardingRoutePlan(app, plans)
	if err != nil || !selected {
		return application.AccountView{}, false, err
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
		return application.AccountView{}, false, err
	}
	catalog, err := accounts.List(app.context)
	if err != nil {
		return application.AccountView{}, false, err
	}
	makeDefault, selected, err := chooseOnboardingDefault(app, catalog, alias)
	if err != nil || !selected {
		return application.AccountView{}, false, err
	}
	routing, selected, err := configureOnboardingRoutes(
		app,
		plan,
		discovery,
		alias,
	)
	if err != nil || !selected {
		return application.AccountView{}, false, err
	}
	selectedCandidate := onboardingSelectedCandidate(plan)
	mailRoute, calendarRoute, _, err := routing.routes(
		selectedCandidate,
		discovery,
	)
	if err != nil {
		return application.AccountView{}, false, err
	}
	input := application.AccountAddInput{
		Alias: alias, Address: discovery.Address,
		Mail: mailRoute, Calendar: calendarRoute, Default: makeDefault,
	}
	review, err := accounts.ReviewAdd(app.context, input)
	if err != nil {
		return application.AccountView{}, false, err
	}
	if err := writeOnboardingAccountReview(app, review); err != nil {
		return application.AccountView{}, false, err
	}
	confirmed, err := runOnboardingConfirm(
		app,
		"Add "+alias+" to this device?",
		"This writes only secret-free local route metadata. Authentication remains a separate action.",
		"Add account",
		"Cancel this account",
	)
	if err != nil || !confirmed {
		return application.AccountView{}, false, err
	}
	var account application.AccountView
	err = runSettingsAccountMutation(app, func() error {
		var addErr error
		account, addErr = accounts.Add(app.context, input)
		return addErr
	})
	if err != nil {
		return application.AccountView{}, false, err
	}
	if err := writeOnboardingAccountAdded(app, account); err != nil {
		return application.AccountView{}, false, err
	}
	return account, true, nil
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
			plans = append(plans, newOnboardingRoutePlan(mail, calDAV))
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
		case domain.ProviderGoogleWeb, domain.ProviderPOP3:
		}
	}
	return plans
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
		options = append(options, huh.NewOption(plan.label, index))
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
		case domain.ProviderGoogleWeb, domain.ProviderPOP3:
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
	if settingsInputExhausted(app) {
		return false, nil
	}
	return confirmed, nil
}

func runOnboardingAccountHandoff(
	app *runtime,
	account application.AccountView,
) error {
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
				huh.NewOption("Run an opt-in provider connection check", "doctor_online"),
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
			err = (&doctorCommand{Account: account.Alias, Online: true}).Run(app)
		case "doctor":
			err = (&doctorCommand{Account: account.Alias}).Run(app)
		}
		if err != nil {
			return err
		}
	}
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
			domain.ProviderPOP3:
		}
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

func writeOnboardingNextSteps(app *runtime, added int) error {
	view := newConsoleView(app, app.stdout, true)
	_, err := view.printf(
		"\n%s  %s\n   %s\n   %s\n   %s\n",
		view.success(),
		view.strong(fmt.Sprintf("Account setup complete · %d added", added)),
		view.command("Connect an agent: corr mcp setup --help"),
		view.command("Review accounts: corr account list"),
		view.command("Resume safely at any time: corr setup"),
	)
	return err
}
