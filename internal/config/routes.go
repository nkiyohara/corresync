package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/microsoftcloud"
)

// Credentials configures the one optional external helper. Secrets are read
// from its stdout on demand and never represented by this schema.
type Credentials struct {
	Helper []string `json:"helper,omitempty" toml:"helper,omitempty"`
}

// CredentialBackend names an external secret owner.
type CredentialBackend string

const (
	CredentialOSKeyring CredentialBackend = "os-keyring"
	CredentialHelper    CredentialBackend = "helper"
)

// CredentialRef is a non-secret lookup key plus the prior human-consent bit
// recorded by an explicit CLI account configuration action.
type CredentialRef struct {
	Backend CredentialBackend `json:"backend" toml:"backend"`
	Key     string            `json:"key" toml:"key"`
	Consent bool              `json:"consent" toml:"consent"`
}

// TLSMode selects a fail-closed encrypted transport.
type TLSMode string

const (
	TLSImplicit TLSMode = "implicit"
	TLSStartTLS TLSMode = "starttls"
)

// TLSEndpoint is one validated host and port with no embedded credential.
type TLSEndpoint struct {
	Host string  `json:"host" toml:"host"`
	Port uint16  `json:"port" toml:"port"`
	Mode TLSMode `json:"mode" toml:"mode"`
}

// OutlookWebRoute uses a dedicated first-party browser profile.
type OutlookWebRoute struct {
	Origin  string `json:"origin" toml:"origin"`
	Mailbox string `json:"mailbox,omitempty" toml:"mailbox,omitempty"`
}

// JMAPRoute points at an RFC 8620 session resource.
type JMAPRoute struct {
	SessionURL string        `json:"sessionUrl" toml:"session_url"`
	Username   string        `json:"username" toml:"username"`
	Credential CredentialRef `json:"credential" toml:"credential"`
}

// IMAPSMTPRoute keeps receive and submission endpoints distinct.
type IMAPSMTPRoute struct {
	IMAP       TLSEndpoint   `json:"imap" toml:"imap"`
	SMTP       TLSEndpoint   `json:"smtp" toml:"smtp"`
	Username   string        `json:"username" toml:"username"`
	Mailbox    string        `json:"mailbox,omitempty" toml:"mailbox,omitempty"`
	Credential CredentialRef `json:"credential" toml:"credential"`
}

// CalDAVRoute points at one account-scoped CalDAV endpoint. CalendarPath may
// remain empty until authenticated principal discovery.
type CalDAVRoute struct {
	Endpoint     string        `json:"endpoint" toml:"endpoint"`
	CalendarPath string        `json:"calendarPath,omitempty" toml:"calendar_path,omitempty"`
	Username     string        `json:"username" toml:"username"`
	Credential   CredentialRef `json:"credential" toml:"credential"`
}

// OAuthClient identifies a local public client and an OS-keyring grant. It can
// never represent a client secret, authorization code, or bearer token.
type OAuthClient struct {
	ClientID      string        `json:"clientId" toml:"client_id"`
	RedirectURI   string        `json:"redirectUri" toml:"redirect_uri"`
	Authorization CredentialRef `json:"authorization" toml:"authorization"`
}

// GoogleMailRoute binds Gmail's fixed API endpoint to one explicit desktop
// OAuth public client. The endpoint is product policy, not mutable
// configuration, so a Google grant cannot be redirected to another host.
type GoogleMailRoute struct {
	Username      string        `json:"username" toml:"username"`
	Mailbox       string        `json:"mailbox,omitempty" toml:"mailbox,omitempty"`
	ClientID      string        `json:"clientId" toml:"client_id"`
	RedirectURI   string        `json:"redirectUri" toml:"redirect_uri"`
	Authorization CredentialRef `json:"authorization" toml:"authorization"`
}

// Client returns the secret-free public-client authorization.
func (route GoogleMailRoute) Client() OAuthClient {
	return OAuthClient{
		ClientID: route.ClientID, RedirectURI: route.RedirectURI,
		Authorization: route.Authorization,
	}
}

// OAuthRoute adds one pinned HTTPS API base to a public-client authorization.
type OAuthRoute struct {
	APIBase        string            `json:"apiBase" toml:"api_base"`
	MicrosoftCloud microsoftcloud.ID `json:"microsoftCloud,omitempty" toml:"microsoft_cloud,omitempty"`
	ClientID       string            `json:"clientId" toml:"client_id"`
	RedirectURI    string            `json:"redirectUri" toml:"redirect_uri"`
	Authorization  CredentialRef     `json:"authorization" toml:"authorization"`
}

// MicrosoftGraphTaskRoute binds To Do to an independently consented Graph
// public-client grant. ReadOnly selects Tasks.Read instead of Tasks.ReadWrite.
type MicrosoftGraphTaskRoute struct {
	OAuth    OAuthRoute `json:"oauth" toml:"oauth"`
	ReadOnly bool       `json:"readOnly,omitempty" toml:"read_only,omitempty"`
}

// Client returns the secret-free public-client portion of an API route.
func (route OAuthRoute) Client() OAuthClient {
	return OAuthClient{
		ClientID: route.ClientID, RedirectURI: route.RedirectURI,
		Authorization: route.Authorization,
	}
}

// WebRoute identifies a provider-owned interactive browser origin.
type WebRoute struct {
	Origin string `json:"origin" toml:"origin"`
}

// MailRoute is a closed tagged union. Exactly the provider-matching payload is
// present; unknown provider-specific properties are rejected by strict TOML.
type MailRoute struct {
	Provider       domain.ProviderID `json:"provider" toml:"provider"`
	OutlookWeb     *OutlookWebRoute  `json:"outlookWeb,omitempty" toml:"outlook_web,omitempty"`
	JMAP           *JMAPRoute        `json:"jmap,omitempty" toml:"jmap,omitempty"`
	IMAPSMTP       *IMAPSMTPRoute    `json:"imapSmtp,omitempty" toml:"imap_smtp,omitempty"`
	Google         *GoogleMailRoute  `json:"google,omitempty" toml:"google,omitempty"`
	GoogleWeb      *WebRoute         `json:"googleWeb,omitempty" toml:"google_web,omitempty"`
	MicrosoftGraph *OAuthRoute       `json:"microsoftGraph,omitempty" toml:"microsoft_graph,omitempty"`
}

// CalendarRoute is the corresponding closed calendar tagged union.
type CalendarRoute struct {
	Provider       domain.ProviderID `json:"provider" toml:"provider"`
	OutlookWeb     *OutlookWebRoute  `json:"outlookWeb,omitempty" toml:"outlook_web,omitempty"`
	CalDAV         *CalDAVRoute      `json:"caldav,omitempty" toml:"caldav,omitempty"`
	Google         *OAuthRoute       `json:"google,omitempty" toml:"google,omitempty"`
	GoogleWeb      *WebRoute         `json:"googleWeb,omitempty" toml:"google_web,omitempty"`
	MicrosoftGraph *OAuthRoute       `json:"microsoftGraph,omitempty" toml:"microsoft_graph,omitempty"`
}

// TaskRoute selects one account-scoped task adapter. Provider-specific,
// secret-free connection settings are added by each adapter issue; the
// canonical contract deliberately cannot carry an arbitrary action or secret.
type TaskRoute struct {
	Provider       domain.ProviderID        `json:"provider" toml:"provider"`
	MicrosoftGraph *MicrosoftGraphTaskRoute `json:"microsoftGraph,omitempty" toml:"microsoft_graph,omitempty"`
}

func (account Account) validate() error {
	if account.Mail == nil && account.Calendar == nil && account.Tasks == nil {
		return errors.New("at least one mail, calendar, or task route is required")
	}
	if account.Mail != nil {
		if err := account.Mail.validate(); err != nil {
			return fmt.Errorf("mail: %w", err)
		}
	}
	if account.Calendar != nil {
		if err := account.Calendar.validate(); err != nil {
			return fmt.Errorf("calendar: %w", err)
		}
	}
	if account.Tasks != nil {
		if err := account.Tasks.validate(); err != nil {
			return fmt.Errorf("tasks: %w", err)
		}
		if account.Tasks.Provider == domain.ProviderMicrosoftGraph && account.Address == "" {
			return errors.New("a Microsoft Graph task route requires the account email address")
		}
	}
	if err := validateOAuthGrantSharing(account); err != nil {
		return err
	}
	googleMail := account.Mail != nil &&
		account.Mail.Provider == domain.ProviderGoogle &&
		account.Mail.Google != nil
	googleCalendar := account.Calendar != nil &&
		account.Calendar.Provider == domain.ProviderGoogle &&
		account.Calendar.Google != nil
	if googleMail || googleCalendar {
		if account.Address == "" {
			return errors.New("google routes require the account email address")
		}
		if googleMail &&
			!strings.EqualFold(account.Mail.Google.Username, account.Address) {
			return errors.New(
				"google mail username must match the account email address",
			)
		}
	}
	if account.Monitor != nil {
		if err := account.Monitor.validate(); err != nil {
			return fmt.Errorf("monitor: %w", err)
		}
		if account.Mail == nil && account.Monitor.Mode.Collects() {
			return errors.New("monitoring requires a mail route")
		}
	}
	return nil
}

type oauthGrantBinding struct {
	provider      domain.ProviderID
	clientID      string
	redirectURI   string
	authorization CredentialRef
	cloud         microsoftcloud.ID
}

func validateOAuthGrantSharing(account Account) error {
	bindings := make([]oauthGrantBinding, 0, 3)
	add := func(provider domain.ProviderID, client OAuthClient, cloud microsoftcloud.ID) {
		bindings = append(bindings, oauthGrantBinding{
			provider: provider, clientID: client.ClientID,
			redirectURI: client.RedirectURI, authorization: client.Authorization,
			cloud: cloud,
		})
	}
	if account.Mail != nil {
		if account.Mail.Provider == domain.ProviderGoogle && account.Mail.Google != nil {
			add(domain.ProviderGoogle, account.Mail.Google.Client(), "")
		}
		if account.Mail.Provider == domain.ProviderMicrosoftGraph && account.Mail.MicrosoftGraph != nil {
			add(domain.ProviderMicrosoftGraph, account.Mail.MicrosoftGraph.Client(), account.Mail.MicrosoftGraph.MicrosoftCloud)
		}
	}
	if account.Calendar != nil {
		if account.Calendar.Provider == domain.ProviderGoogle && account.Calendar.Google != nil {
			add(domain.ProviderGoogle, account.Calendar.Google.Client(), "")
		}
		if account.Calendar.Provider == domain.ProviderMicrosoftGraph && account.Calendar.MicrosoftGraph != nil {
			add(domain.ProviderMicrosoftGraph, account.Calendar.MicrosoftGraph.Client(), account.Calendar.MicrosoftGraph.MicrosoftCloud)
		}
	}
	if account.Tasks != nil && account.Tasks.Provider == domain.ProviderMicrosoftGraph &&
		account.Tasks.MicrosoftGraph != nil {
		route := account.Tasks.MicrosoftGraph.OAuth
		add(domain.ProviderMicrosoftGraph, route.Client(), route.MicrosoftCloud)
	}
	for left := range bindings {
		for right := left + 1; right < len(bindings); right++ {
			if bindings[left].authorization.Backend == bindings[right].authorization.Backend &&
				bindings[left].authorization.Key == bindings[right].authorization.Key &&
				!sameOAuthGrant(bindings[left], bindings[right]) {
				return errors.New("one OAuth authorization handle cannot identify different provider or public-client grants")
			}
		}
	}
	return nil
}

func sameOAuthGrant(left, right oauthGrantBinding) bool {
	if left.provider != right.provider || left.clientID != right.clientID ||
		left.redirectURI != right.redirectURI || left.authorization != right.authorization {
		return false
	}
	return left.provider != domain.ProviderMicrosoftGraph ||
		microsoftcloud.Equivalent(left.cloud, right.cloud)
}

func (route TaskRoute) validate() error {
	if err := route.Provider.Validate(); err != nil {
		return err
	}
	switch route.Provider {
	case domain.ProviderMicrosoftGraph:
		if route.MicrosoftGraph == nil {
			return errors.New("microsoft-graph tasks require independent OAuth settings")
		}
		if err := route.MicrosoftGraph.OAuth.validateFor(domain.ProviderMicrosoftGraph); err != nil {
			return err
		}
		profile, err := microsoftcloud.Resolve(route.MicrosoftGraph.OAuth.MicrosoftCloud)
		if err != nil {
			return err
		}
		if !profile.TasksAvailable {
			return errors.New("the Microsoft To Do API is unavailable in the selected Microsoft cloud")
		}
		return nil
	case domain.ProviderMicrosoftTasks,
		domain.ProviderTodoist,
		domain.ProviderCalDAV,
		domain.ProviderGoogleTasks,
		domain.ProviderAppleReminders,
		domain.ProviderTickTick,
		domain.ProviderAnyDoMCP,
		domain.ProviderThings,
		domain.ProviderOmniFocus:
		if route.MicrosoftGraph != nil {
			return errors.New("non-Graph task route cannot contain Microsoft Graph settings")
		}
		return nil
	case domain.ProviderMicrosoftOWA,
		domain.ProviderGoogle,
		domain.ProviderGoogleWeb,
		domain.ProviderJMAP,
		domain.ProviderIMAPSMTP,
		domain.ProviderPOP3:
		return fmt.Errorf("provider %q cannot supply a task route", route.Provider)
	default:
		return fmt.Errorf("provider %q cannot supply a task route", route.Provider)
	}
}

func (route MailRoute) validate() error {
	if err := route.Provider.Validate(); err != nil {
		return err
	}
	present := countNonNil(
		route.OutlookWeb, route.JMAP, route.IMAPSMTP, route.Google,
		route.GoogleWeb, route.MicrosoftGraph,
	)
	if present != 1 {
		return errors.New("exactly one provider-specific mail route is required")
	}
	switch route.Provider {
	case domain.ProviderMicrosoftOWA:
		if route.OutlookWeb == nil {
			return errors.New("microsoft-owa requires outlook_web")
		}
		return route.OutlookWeb.validate()
	case domain.ProviderJMAP:
		if route.JMAP == nil {
			return errors.New("jmap requires jmap settings")
		}
		return route.JMAP.validate()
	case domain.ProviderIMAPSMTP:
		if route.IMAPSMTP == nil {
			return errors.New("imap-smtp requires imap_smtp settings")
		}
		return route.IMAPSMTP.validate()
	case domain.ProviderGoogle:
		if route.Google == nil {
			return errors.New("google requires google settings")
		}
		return route.Google.validate()
	case domain.ProviderGoogleWeb:
		if route.GoogleWeb == nil {
			return errors.New("google-web requires google_web settings")
		}
		return route.GoogleWeb.validateFor("mail.google.com")
	case domain.ProviderMicrosoftGraph:
		if route.MicrosoftGraph == nil {
			return errors.New("microsoft-graph requires microsoft_graph settings")
		}
		return route.MicrosoftGraph.validateFor(domain.ProviderMicrosoftGraph)
	case domain.ProviderCalDAV, domain.ProviderPOP3,
		domain.ProviderMicrosoftTasks, domain.ProviderTodoist,
		domain.ProviderGoogleTasks, domain.ProviderAppleReminders,
		domain.ProviderTickTick, domain.ProviderAnyDoMCP,
		domain.ProviderThings, domain.ProviderOmniFocus:
		return fmt.Errorf("provider %q cannot supply a mail route", route.Provider)
	default:
		return fmt.Errorf("provider %q cannot supply a mail route", route.Provider)
	}
}

func (route CalendarRoute) validate() error {
	if err := route.Provider.Validate(); err != nil {
		return err
	}
	present := countNonNil(
		route.OutlookWeb, route.CalDAV, route.Google, route.GoogleWeb,
		route.MicrosoftGraph,
	)
	if present != 1 {
		return errors.New("exactly one provider-specific calendar route is required")
	}
	switch route.Provider {
	case domain.ProviderMicrosoftOWA:
		if route.OutlookWeb == nil {
			return errors.New("microsoft-owa requires outlook_web")
		}
		return route.OutlookWeb.validate()
	case domain.ProviderCalDAV:
		if route.CalDAV == nil {
			return errors.New("caldav requires caldav settings")
		}
		return route.CalDAV.validate()
	case domain.ProviderGoogle:
		if route.Google == nil {
			return errors.New("google requires google settings")
		}
		return route.Google.validateFor(domain.ProviderGoogle)
	case domain.ProviderGoogleWeb:
		if route.GoogleWeb == nil {
			return errors.New("google-web requires google_web settings")
		}
		return route.GoogleWeb.validateFor("calendar.google.com")
	case domain.ProviderMicrosoftGraph:
		if route.MicrosoftGraph == nil {
			return errors.New("microsoft-graph requires microsoft_graph settings")
		}
		return route.MicrosoftGraph.validateFor(domain.ProviderMicrosoftGraph)
	case domain.ProviderJMAP, domain.ProviderIMAPSMTP, domain.ProviderPOP3,
		domain.ProviderMicrosoftTasks, domain.ProviderTodoist,
		domain.ProviderGoogleTasks, domain.ProviderAppleReminders,
		domain.ProviderTickTick, domain.ProviderAnyDoMCP,
		domain.ProviderThings, domain.ProviderOmniFocus:
		return fmt.Errorf("provider %q cannot supply a calendar route", route.Provider)
	default:
		return fmt.Errorf("provider %q cannot supply a calendar route", route.Provider)
	}
}

func countNonNil(values ...any) int {
	count := 0
	for _, value := range values {
		if !isNilPointer(value) {
			count++
		}
	}
	return count
}

func isNilPointer(value any) bool {
	switch typed := value.(type) {
	case *OutlookWebRoute:
		return typed == nil
	case *JMAPRoute:
		return typed == nil
	case *IMAPSMTPRoute:
		return typed == nil
	case *GoogleMailRoute:
		return typed == nil
	case *OAuthRoute:
		return typed == nil
	case *WebRoute:
		return typed == nil
	case *CalDAVRoute:
		return typed == nil
	default:
		panic("unsupported route pointer")
	}
}

func (route OutlookWebRoute) validate() error {
	if err := validateOrigin(route.Origin); err != nil {
		return err
	}
	return validateMailbox(route.Mailbox)
}

func (route JMAPRoute) validate() error {
	if err := validateHTTPSURL("JMAP session URL", route.SessionURL, true); err != nil {
		return err
	}
	if err := validateUsername(route.Username); err != nil {
		return err
	}
	return route.Credential.validate(false)
}

func (route IMAPSMTPRoute) validate() error {
	if err := route.IMAP.validate("IMAP"); err != nil {
		return err
	}
	if err := route.SMTP.validate("SMTP"); err != nil {
		return err
	}
	if err := validateUsername(route.Username); err != nil {
		return err
	}
	if err := validateMailbox(route.Mailbox); err != nil {
		return err
	}
	return route.Credential.validate(false)
}

func (route CalDAVRoute) validate() error {
	if err := validateHTTPSURL("CalDAV endpoint", route.Endpoint, true); err != nil {
		return err
	}
	if err := validateBoundedText("CalDAV calendar path", route.CalendarPath, 2048, true); err != nil {
		return err
	}
	if route.CalendarPath != "" && !strings.HasPrefix(route.CalendarPath, "/") {
		return errors.New("CalDAV calendar path must be absolute")
	}
	if err := validateUsername(route.Username); err != nil {
		return err
	}
	return route.Credential.validate(false)
}

func (route OAuthClient) validate() error {
	if err := validateBoundedText("OAuth client ID", route.ClientID, 512, false); err != nil {
		return err
	}
	redirect, err := url.Parse(route.RedirectURI)
	if err != nil || redirect.Scheme != "http" || redirect.Hostname() != "127.0.0.1" ||
		redirect.User != nil || redirect.Fragment != "" || redirect.RawQuery != "" ||
		redirect.Port() == "" {
		return errors.New("OAuth redirect URI must use an explicit loopback port")
	}
	port, err := strconv.ParseUint(redirect.Port(), 10, 16)
	if err != nil || port > 65535 {
		return errors.New("OAuth redirect URI has an invalid loopback port")
	}
	if route.Authorization.Backend != CredentialOSKeyring {
		return errors.New("OAuth authorization must use the OS keyring")
	}
	return route.Authorization.validate(true)
}

func (route GoogleMailRoute) validate() error {
	if err := validateUsername(route.Username); err != nil {
		return err
	}
	if err := validateMailbox(route.Mailbox); err != nil {
		return err
	}
	return route.Client().validate()
}

func (route OAuthRoute) validate() error {
	if err := validateHTTPSURL("API base", route.APIBase, true); err != nil {
		return err
	}
	return route.Client().validate()
}

func (route OAuthRoute) validateFor(provider domain.ProviderID) error {
	if err := route.validate(); err != nil {
		return err
	}
	parsed, _ := url.Parse(route.APIBase)
	switch provider {
	case domain.ProviderGoogle:
		if route.MicrosoftCloud != "" {
			return errors.New("google OAuth route cannot select a Microsoft cloud")
		}
		if parsed.Host != "www.googleapis.com" || parsed.RawQuery != "" ||
			parsed.EscapedPath() != "" && parsed.EscapedPath() != "/" {
			return errors.New("google API base must be https://www.googleapis.com")
		}
	case domain.ProviderMicrosoftGraph:
		if err := microsoftcloud.ValidateAPIBase(route.MicrosoftCloud, route.APIBase); err != nil {
			return err
		}
	case domain.ProviderMicrosoftOWA,
		domain.ProviderGoogleWeb,
		domain.ProviderJMAP,
		domain.ProviderIMAPSMTP,
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
		return fmt.Errorf("provider %q has no OAuth API base policy", provider)
	default:
		return fmt.Errorf("unknown OAuth API provider %q", provider)
	}
	return nil
}

func (route WebRoute) validate() error {
	return validateOrigin(route.Origin)
}

func (route WebRoute) validateFor(host string) error {
	if err := route.validate(); err != nil {
		return err
	}
	parsed, _ := url.Parse(route.Origin)
	if parsed.Host != host {
		return fmt.Errorf("google Web origin must be https://%s", host)
	}
	return nil
}

func (endpoint TLSEndpoint) validate(name string) error {
	if endpoint.Host == "" || len(endpoint.Host) > 253 ||
		strings.TrimSpace(endpoint.Host) != endpoint.Host ||
		strings.ContainsAny(endpoint.Host, "\r\n\x00/@") ||
		net.ParseIP(endpoint.Host) == nil && !validDNSName(endpoint.Host) {
		return fmt.Errorf("%s host is invalid", name)
	}
	if endpoint.Port == 0 {
		return fmt.Errorf("%s port is required", name)
	}
	switch endpoint.Mode {
	case TLSImplicit, TLSStartTLS:
	default:
		return fmt.Errorf("%s TLS mode must be implicit or starttls", name)
	}
	return nil
}

func (reference CredentialRef) validate(oauth bool) error {
	switch reference.Backend {
	case CredentialOSKeyring:
	case CredentialHelper:
		if oauth {
			return errors.New("OAuth authorization cannot use a credential helper")
		}
	default:
		return errors.New("credential backend must be os-keyring or helper")
	}
	if err := validateBoundedText("credential key", reference.Key, 256, false); err != nil {
		return err
	}
	if !reference.Consent {
		return errors.New("credential use requires explicit prior consent")
	}
	return nil
}

func (credentials Credentials) validate() error {
	if len(credentials.Helper) > 16 {
		return errors.New("credential helper has too many arguments")
	}
	if len(credentials.Helper) > 0 && !filepath.IsAbs(credentials.Helper[0]) {
		return errors.New("credential helper executable must use an absolute path")
	}
	for _, argument := range credentials.Helper {
		if err := validateBoundedText("credential helper argument", argument, 4096, false); err != nil {
			return err
		}
	}
	return nil
}

func validateUsername(value string) error {
	return validateBoundedText("provider username", value, 320, false)
}

func validateBoundedText(name, value string, maximum int, allowEmpty bool) error {
	if !allowEmpty && value == "" {
		return fmt.Errorf("%s is required", name)
	}
	if len(value) > maximum || strings.TrimSpace(value) != value ||
		strings.ContainsAny(value, "\r\n\x00") {
		return fmt.Errorf("%s is malformed", name)
	}
	return nil
}

func validateHTTPSURL(name, raw string, allowPath bool) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("%s must be a credential-free HTTPS URL", name)
	}
	if !allowPath && parsed.Path != "" && parsed.Path != "/" {
		return fmt.Errorf("%s must not include a path", name)
	}
	return nil
}

func validDNSName(value string) bool {
	if len(value) > 253 || strings.HasPrefix(value, ".") || strings.HasSuffix(value, ".") {
		return false
	}
	labels := strings.Split(value, ".")
	return slices.IndexFunc(labels, func(label string) bool {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return true
		}
		for _, character := range label {
			if character != '-' && (character < 'a' || character > 'z') &&
				(character < 'A' || character > 'Z') &&
				(character < '0' || character > '9') {
				return true
			}
		}
		return false
	}) < 0
}

// MailProvider returns the selected mail adapter, if any.
func (account Account) MailProvider() domain.ProviderID {
	if account.Mail == nil {
		return ""
	}
	return account.Mail.Provider
}

// CalendarProvider returns the selected calendar adapter, if any.
func (account Account) CalendarProvider() domain.ProviderID {
	if account.Calendar == nil {
		return ""
	}
	return account.Calendar.Provider
}

// TaskProvider returns the selected task adapter, if any.
func (account Account) TaskProvider() domain.ProviderID {
	if account.Tasks == nil {
		return ""
	}
	return account.Tasks.Provider
}

// PrimaryProvider preserves compact status output by preferring mail, then
// calendar, then tasks. Callers routing operations must use the typed accessor.
func (account Account) PrimaryProvider() domain.ProviderID {
	if provider := account.MailProvider(); provider != "" {
		return provider
	}
	if provider := account.CalendarProvider(); provider != "" {
		return provider
	}
	return account.TaskProvider()
}

// OutlookWeb returns the shared browser settings when every configured route
// is Outlook Web and uses the same origin and mailbox.
func (account Account) OutlookWeb() (OutlookWebRoute, bool) {
	var selected *OutlookWebRoute
	for _, route := range []*OutlookWebRoute{
		func() *OutlookWebRoute {
			if account.Mail != nil {
				return account.Mail.OutlookWeb
			}
			return nil
		}(),
		func() *OutlookWebRoute {
			if account.Calendar != nil {
				return account.Calendar.OutlookWeb
			}
			return nil
		}(),
	} {
		if route == nil {
			continue
		}
		if selected != nil && *selected != *route {
			return OutlookWebRoute{}, false
		}
		copy := *route
		selected = &copy
	}
	if selected == nil {
		return OutlookWebRoute{}, false
	}
	return *selected, true
}
