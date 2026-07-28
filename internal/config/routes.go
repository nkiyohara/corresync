package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"

	"github.com/nkiyohara/corresync/internal/domain"
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

// OAuthRoute identifies a local public client and an OS-keyring grant. It can
// never represent a client secret, authorization code, or bearer token.
type OAuthRoute struct {
	APIBase       string        `json:"apiBase" toml:"api_base"`
	ClientID      string        `json:"clientId" toml:"client_id"`
	RedirectURI   string        `json:"redirectUri" toml:"redirect_uri"`
	Authorization CredentialRef `json:"authorization" toml:"authorization"`
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
	GoogleAPI      *OAuthRoute       `json:"googleApi,omitempty" toml:"google_api,omitempty"`
	GoogleWeb      *WebRoute         `json:"googleWeb,omitempty" toml:"google_web,omitempty"`
	MicrosoftGraph *OAuthRoute       `json:"microsoftGraph,omitempty" toml:"microsoft_graph,omitempty"`
}

// CalendarRoute is the corresponding closed calendar tagged union.
type CalendarRoute struct {
	Provider       domain.ProviderID `json:"provider" toml:"provider"`
	OutlookWeb     *OutlookWebRoute  `json:"outlookWeb,omitempty" toml:"outlook_web,omitempty"`
	CalDAV         *CalDAVRoute      `json:"caldav,omitempty" toml:"caldav,omitempty"`
	GoogleAPI      *OAuthRoute       `json:"googleApi,omitempty" toml:"google_api,omitempty"`
	GoogleWeb      *WebRoute         `json:"googleWeb,omitempty" toml:"google_web,omitempty"`
	MicrosoftGraph *OAuthRoute       `json:"microsoftGraph,omitempty" toml:"microsoft_graph,omitempty"`
}

func (account Account) validate() error {
	if account.Mail == nil && account.Calendar == nil {
		return errors.New("at least one mail or calendar route is required")
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

func (route MailRoute) validate() error {
	if err := route.Provider.Validate(); err != nil {
		return err
	}
	present := countNonNil(
		route.OutlookWeb, route.JMAP, route.IMAPSMTP, route.GoogleAPI,
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
	case domain.ProviderGoogleAPI:
		if route.GoogleAPI == nil {
			return errors.New("google-api requires google_api settings")
		}
		return route.GoogleAPI.validateFor(domain.ProviderGoogleAPI)
	case domain.ProviderGoogleWeb:
		if route.GoogleWeb == nil {
			return errors.New("google-web requires google_web settings")
		}
		return route.GoogleWeb.validate()
	case domain.ProviderMicrosoftGraph:
		if route.MicrosoftGraph == nil {
			return errors.New("microsoft-graph requires microsoft_graph settings")
		}
		return route.MicrosoftGraph.validateFor(domain.ProviderMicrosoftGraph)
	case domain.ProviderCalDAV, domain.ProviderPOP3:
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
		route.OutlookWeb, route.CalDAV, route.GoogleAPI, route.GoogleWeb,
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
	case domain.ProviderGoogleAPI:
		if route.GoogleAPI == nil {
			return errors.New("google-api requires google_api settings")
		}
		return route.GoogleAPI.validateFor(domain.ProviderGoogleAPI)
	case domain.ProviderGoogleWeb:
		if route.GoogleWeb == nil {
			return errors.New("google-web requires google_web settings")
		}
		return route.GoogleWeb.validate()
	case domain.ProviderMicrosoftGraph:
		if route.MicrosoftGraph == nil {
			return errors.New("microsoft-graph requires microsoft_graph settings")
		}
		return route.MicrosoftGraph.validateFor(domain.ProviderMicrosoftGraph)
	case domain.ProviderJMAP, domain.ProviderIMAPSMTP, domain.ProviderPOP3:
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

func (route OAuthRoute) validate() error {
	if err := validateHTTPSURL("API base", route.APIBase, true); err != nil {
		return err
	}
	if err := validateBoundedText("OAuth client ID", route.ClientID, 512, false); err != nil {
		return err
	}
	redirect, err := url.Parse(route.RedirectURI)
	if err != nil || redirect.Scheme != "http" || redirect.Hostname() != "127.0.0.1" ||
		redirect.User != nil || redirect.Fragment != "" || redirect.RawQuery != "" ||
		redirect.Port() == "" {
		return errors.New("OAuth redirect URI must use an explicit loopback port")
	}
	if route.Authorization.Backend != CredentialOSKeyring {
		return errors.New("OAuth authorization must use the OS keyring")
	}
	return route.Authorization.validate(true)
}

func (route OAuthRoute) validateFor(provider domain.ProviderID) error {
	if err := route.validate(); err != nil {
		return err
	}
	parsed, _ := url.Parse(route.APIBase)
	switch provider {
	case domain.ProviderGoogleAPI:
		if parsed.Host != "www.googleapis.com" || parsed.RawQuery != "" ||
			parsed.EscapedPath() != "" && parsed.EscapedPath() != "/" {
			return errors.New("google API base must be https://www.googleapis.com")
		}
	case domain.ProviderMicrosoftGraph:
		if parsed.Host != "graph.microsoft.com" || parsed.RawQuery != "" ||
			strings.TrimSuffix(parsed.EscapedPath(), "/") != "/v1.0" {
			return errors.New(
				"microsoft Graph API base must be https://graph.microsoft.com/v1.0",
			)
		}
	case domain.ProviderMicrosoftOWA,
		domain.ProviderGoogleWeb,
		domain.ProviderJMAP,
		domain.ProviderIMAPSMTP,
		domain.ProviderCalDAV,
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

// PrimaryProvider preserves compact status output by preferring the mail route.
// Callers that route operations must use MailProvider or CalendarProvider.
func (account Account) PrimaryProvider() domain.ProviderID {
	if provider := account.MailProvider(); provider != "" {
		return provider
	}
	return account.CalendarProvider()
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
