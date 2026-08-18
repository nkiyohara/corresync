package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"slices"
	"sort"
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
// never represent a client credential, authorization code, or bearer token.
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
	ClientSecret  CredentialRef `json:"clientSecret" toml:"client_secret"`
}

// Client returns the secret-free public-client authorization.
func (route GoogleMailRoute) Client() OAuthClient {
	return OAuthClient{
		ClientID: route.ClientID, RedirectURI: route.RedirectURI,
		Authorization: route.Authorization,
	}
}

// GoogleOAuthRoute binds a user-owned Google Desktop client to one pinned API
// base. ClientSecret is only an external lookup reference; the configuration
// schema can never contain the generated credential itself.
type GoogleOAuthRoute struct {
	APIBase       string        `json:"apiBase" toml:"api_base"`
	ClientID      string        `json:"clientId" toml:"client_id"`
	RedirectURI   string        `json:"redirectUri" toml:"redirect_uri"`
	Authorization CredentialRef `json:"authorization" toml:"authorization"`
	ClientSecret  CredentialRef `json:"clientSecret" toml:"client_secret"`
}

// Client returns the secret-free public-client authorization.
func (route GoogleOAuthRoute) Client() OAuthClient {
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

// TodoistTaskRoute binds the fixed Todoist API to a BYO public OAuth client.
// The route can never represent a client secret or personal API token.
type TodoistTaskRoute struct {
	OAuth    OAuthRoute `json:"oauth" toml:"oauth"`
	ReadOnly bool       `json:"readOnly,omitempty" toml:"read_only,omitempty"`
}

// GoogleTaskRoute binds the fixed Google Tasks API to an independently
// consented user-owned Desktop OAuth grant.
type GoogleTaskRoute struct {
	OAuth    GoogleOAuthRoute `json:"oauth" toml:"oauth"`
	ReadOnly bool             `json:"readOnly,omitempty" toml:"read_only,omitempty"`
}

// TickTickOAuthRoute binds a confidential OAuth client to external references
// for both its grant and client secret. Neither secret can be represented by
// the configuration schema.
type TickTickOAuthRoute struct {
	APIBase       string        `json:"apiBase" toml:"api_base"`
	ClientID      string        `json:"clientId" toml:"client_id"`
	RedirectURI   string        `json:"redirectUri" toml:"redirect_uri"`
	Authorization CredentialRef `json:"authorization" toml:"authorization"`
	ClientSecret  CredentialRef `json:"clientSecret" toml:"client_secret"`
}

// Client returns the secret-free OAuth grant identity.
func (route TickTickOAuthRoute) Client() OAuthClient {
	return OAuthClient{
		ClientID: route.ClientID, RedirectURI: route.RedirectURI,
		Authorization: route.Authorization,
	}
}

// TickTickTaskRoute selects the independent task-only TickTick grant.
type TickTickTaskRoute struct {
	OAuth    TickTickOAuthRoute `json:"oauth" toml:"oauth"`
	ReadOnly bool               `json:"readOnly,omitempty" toml:"read_only,omitempty"`
}

// CalDAVTaskRoute points at one account-scoped CalDAV VTODO endpoint.
// TaskListPath may remain empty until authenticated principal discovery.
type CalDAVTaskRoute struct {
	Endpoint     string        `json:"endpoint" toml:"endpoint"`
	TaskListPath string        `json:"taskListPath,omitempty" toml:"task_list_path,omitempty"`
	Username     string        `json:"username" toml:"username"`
	Credential   CredentialRef `json:"credential" toml:"credential"`
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
	Google         *GoogleOAuthRoute `json:"google,omitempty" toml:"google,omitempty"`
	GoogleWeb      *WebRoute         `json:"googleWeb,omitempty" toml:"google_web,omitempty"`
	MicrosoftGraph *OAuthRoute       `json:"microsoftGraph,omitempty" toml:"microsoft_graph,omitempty"`
}

// TaskRoute selects one account-scoped task adapter. Provider-specific,
// secret-free connection settings are added by each adapter issue; the
// canonical contract deliberately cannot carry an arbitrary action or secret.
type TaskRoute struct {
	Provider       domain.ProviderID        `json:"provider" toml:"provider"`
	MicrosoftGraph *MicrosoftGraphTaskRoute `json:"microsoftGraph,omitempty" toml:"microsoft_graph,omitempty"`
	Todoist        *TodoistTaskRoute        `json:"todoist,omitempty" toml:"todoist,omitempty"`
	CalDAV         *CalDAVTaskRoute         `json:"caldav,omitempty" toml:"caldav,omitempty"`
	GoogleTasks    *GoogleTaskRoute         `json:"googleTasks,omitempty" toml:"google_tasks,omitempty"`
	TickTick       *TickTickTaskRoute       `json:"tickTick,omitempty" toml:"ticktick,omitempty"`
}

// TeamsGraphMessagingRoute binds messaging to one explicitly selected Graph
// grant and tenant/workspace. ReadOnly is a local ceiling even when the grant
// contains broader delegated permissions.
type TeamsGraphMessagingRoute struct {
	OAuth       OAuthRoute `json:"oauth" toml:"oauth"`
	WorkspaceID string     `json:"workspaceId" toml:"workspace_id"`
	ReadOnly    bool       `json:"readOnly,omitempty" toml:"read_only,omitempty"`
}

// TeamsWebMessagingRoute uses only the provider-owned visible browser. The
// workspace is persisted after explicit selection and never inferred from an
// address or reused from a different account profile.
type TeamsWebMessagingRoute struct {
	Web         WebRoute `json:"web" toml:"web"`
	WorkspaceID string   `json:"workspaceId" toml:"workspace_id"`
	ReadOnly    bool     `json:"readOnly,omitempty" toml:"read_only,omitempty"`
}

// SlackMessagingRoute references a provider-supported installation token in
// an external credential owner. The schema cannot represent the token itself.
type SlackMessagingRoute struct {
	APIBase       string        `json:"apiBase" toml:"api_base"`
	WorkspaceID   string        `json:"workspaceId" toml:"workspace_id"`
	Authorization CredentialRef `json:"authorization" toml:"authorization"`
	ReadOnly      bool          `json:"readOnly,omitempty" toml:"read_only,omitempty"`
}

// MattermostMessagingRoute binds one account to one exact approved server and
// team. Redirect and DNS/IP policy is enforced again by its runtime transport.
type MattermostMessagingRoute struct {
	Origin        string        `json:"origin" toml:"origin"`
	WorkspaceID   string        `json:"workspaceId" toml:"workspace_id"`
	Authorization CredentialRef `json:"authorization" toml:"authorization"`
	ReadOnly      bool          `json:"readOnly,omitempty" toml:"read_only,omitempty"`
}

// MessagingRoute is a closed tagged union. Exactly one route-specific payload
// must match Provider; there is no automatic fallback between transports.
type MessagingRoute struct {
	Provider   domain.MessagingProviderID `json:"provider" toml:"provider"`
	TeamsGraph *TeamsGraphMessagingRoute  `json:"teamsGraph,omitempty" toml:"teams_graph,omitempty"`
	TeamsWeb   *TeamsWebMessagingRoute    `json:"teamsWeb,omitempty" toml:"teams_web,omitempty"`
	Slack      *SlackMessagingRoute       `json:"slack,omitempty" toml:"slack,omitempty"`
	Mattermost *MattermostMessagingRoute  `json:"mattermost,omitempty" toml:"mattermost,omitempty"`
}

func (account Account) validate() error {
	if account.Mail == nil && account.Calendar == nil && account.Tasks == nil && account.Messages == nil {
		return errors.New("at least one mail, calendar, task, or messaging route is required")
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
		if (account.Tasks.Provider == domain.ProviderMicrosoftGraph ||
			account.Tasks.Provider == domain.ProviderTodoist ||
			account.Tasks.Provider == domain.ProviderGoogleTasks) && account.Address == "" {
			return errors.New("an OAuth task route requires the account email address")
		}
	}
	if account.Messages != nil {
		if err := account.Messages.validate(); err != nil {
			return fmt.Errorf("messages: %w", err)
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

type accountCredentialBinding struct {
	reference CredentialRef
	tickTick  bool
}

func validateTickTickCredentialIsolation(accounts map[string]Account) error {
	bindings := make([]accountCredentialBinding, 0, len(accounts)*2)
	add := func(reference CredentialRef, tickTick bool) {
		bindings = append(bindings, accountCredentialBinding{
			reference: reference, tickTick: tickTick,
		})
	}
	for _, account := range accounts {
		if account.Mail != nil {
			if account.Mail.Provider == domain.ProviderJMAP {
				add(account.Mail.JMAP.Credential, false)
			}
			if account.Mail.Provider == domain.ProviderIMAPSMTP {
				add(account.Mail.IMAPSMTP.Credential, false)
			}
			if account.Mail.Provider == domain.ProviderGoogle {
				add(account.Mail.Google.Authorization, false)
				add(account.Mail.Google.ClientSecret, false)
			}
			if account.Mail.Provider == domain.ProviderMicrosoftGraph {
				add(account.Mail.MicrosoftGraph.Authorization, false)
			}
		}
		if account.Calendar != nil {
			if account.Calendar.Provider == domain.ProviderCalDAV {
				add(account.Calendar.CalDAV.Credential, false)
			}
			if account.Calendar.Provider == domain.ProviderGoogle {
				add(account.Calendar.Google.Authorization, false)
				add(account.Calendar.Google.ClientSecret, false)
			}
			if account.Calendar.Provider == domain.ProviderMicrosoftGraph {
				add(account.Calendar.MicrosoftGraph.Authorization, false)
			}
		}
		if account.Tasks != nil {
			if account.Tasks.Provider == domain.ProviderMicrosoftGraph {
				add(account.Tasks.MicrosoftGraph.OAuth.Authorization, false)
			}
			if account.Tasks.Provider == domain.ProviderTodoist {
				add(account.Tasks.Todoist.OAuth.Authorization, false)
			}
			if account.Tasks.Provider == domain.ProviderCalDAV {
				add(account.Tasks.CalDAV.Credential, false)
			}
			if account.Tasks.Provider == domain.ProviderGoogleTasks {
				add(account.Tasks.GoogleTasks.OAuth.Authorization, false)
				add(account.Tasks.GoogleTasks.OAuth.ClientSecret, false)
			}
			if account.Tasks.Provider == domain.ProviderTickTick {
				add(account.Tasks.TickTick.OAuth.Authorization, true)
				add(account.Tasks.TickTick.OAuth.ClientSecret, true)
			}
		}
		if account.Messages != nil {
			if account.Messages.Slack != nil {
				add(account.Messages.Slack.Authorization, false)
			}
			if account.Messages.Mattermost != nil {
				add(account.Messages.Mattermost.Authorization, false)
			}
		}
	}
	for left := range bindings {
		for right := left + 1; right < len(bindings); right++ {
			if (bindings[left].tickTick || bindings[right].tickTick) &&
				bindings[left].reference.Backend == bindings[right].reference.Backend &&
				bindings[left].reference.Key == bindings[right].reference.Key {
				return errors.New("TickTick grant and client-secret handles must not be reused by another credential binding")
			}
		}
	}
	return nil
}

func validateMessagingCredentialIsolation(accounts map[string]Account) error {
	seen := make(map[string]string)
	aliases := make([]string, 0, len(accounts))
	for alias := range accounts {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	for _, alias := range aliases {
		route := accounts[alias].Messages
		if route == nil {
			continue
		}
		var reference *CredentialRef
		switch {
		case route.Slack != nil:
			reference = &route.Slack.Authorization
		case route.Mattermost != nil:
			reference = &route.Mattermost.Authorization
		}
		if reference == nil {
			continue
		}
		key := string(reference.Backend) + "\x00" + reference.Key
		if previous, exists := seen[key]; exists {
			return fmt.Errorf("messaging accounts %q and %q reuse one credential handle", previous, alias)
		}
		seen[key] = alias
	}
	return nil
}

func validateOAuthGrantSharing(account Account) error {
	bindings := make([]oauthGrantBinding, 0, 4)
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
	if account.Tasks != nil && account.Tasks.Provider == domain.ProviderTodoist &&
		account.Tasks.Todoist != nil {
		route := account.Tasks.Todoist.OAuth
		add(domain.ProviderTodoist, route.Client(), "")
	}
	if account.Tasks != nil && account.Tasks.Provider == domain.ProviderGoogleTasks &&
		account.Tasks.GoogleTasks != nil {
		route := account.Tasks.GoogleTasks.OAuth
		add(domain.ProviderGoogleTasks, route.Client(), "")
	}
	if account.Tasks != nil && account.Tasks.Provider == domain.ProviderTickTick &&
		account.Tasks.TickTick != nil {
		route := account.Tasks.TickTick.OAuth
		add(domain.ProviderTickTick, route.Client(), "")
	}
	if account.Messages != nil && account.Messages.TeamsGraph != nil {
		route := account.Messages.TeamsGraph.OAuth
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
		if route.MicrosoftGraph == nil || route.Todoist != nil || route.CalDAV != nil ||
			route.GoogleTasks != nil || route.TickTick != nil {
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
	case domain.ProviderTodoist:
		if route.Todoist == nil || route.MicrosoftGraph != nil || route.CalDAV != nil ||
			route.GoogleTasks != nil || route.TickTick != nil {
			return errors.New("todoist tasks require independent OAuth settings")
		}
		return route.Todoist.OAuth.validateFor(domain.ProviderTodoist)
	case domain.ProviderCalDAV:
		if route.CalDAV == nil || route.MicrosoftGraph != nil || route.Todoist != nil ||
			route.GoogleTasks != nil || route.TickTick != nil {
			return errors.New("caldav tasks require CalDAV VTODO settings")
		}
		return route.CalDAV.validate()
	case domain.ProviderGoogleTasks:
		if route.GoogleTasks == nil || route.MicrosoftGraph != nil || route.Todoist != nil ||
			route.CalDAV != nil || route.TickTick != nil {
			return errors.New("google-tasks requires independent OAuth settings")
		}
		return route.GoogleTasks.OAuth.validateFor(domain.ProviderGoogleTasks)
	case domain.ProviderTickTick:
		if route.TickTick == nil || route.MicrosoftGraph != nil || route.Todoist != nil ||
			route.CalDAV != nil || route.GoogleTasks != nil {
			return errors.New("ticktick tasks require independent confidential OAuth settings")
		}
		return route.TickTick.OAuth.validate()
	case domain.ProviderMicrosoftTasks,
		domain.ProviderAppleReminders,
		domain.ProviderAnyDoMCP,
		domain.ProviderThings,
		domain.ProviderOmniFocus:
		if route.MicrosoftGraph != nil || route.Todoist != nil || route.CalDAV != nil ||
			route.GoogleTasks != nil || route.TickTick != nil {
			return errors.New("task route contains settings for another provider")
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

func (route MessagingRoute) validate() error {
	if err := route.Provider.Validate(); err != nil {
		return err
	}
	present := countNonNil(route.TeamsGraph, route.TeamsWeb, route.Slack, route.Mattermost)
	if present != 1 {
		return errors.New("exactly one provider-specific messaging route is required")
	}
	switch route.Provider {
	case domain.MessagingProviderMicrosoftTeams:
		if route.Slack != nil || route.Mattermost != nil ||
			(route.TeamsGraph == nil) == (route.TeamsWeb == nil) {
			return errors.New("microsoft-teams requires exactly one Graph or Teams Web route")
		}
		if route.TeamsGraph != nil {
			return route.TeamsGraph.validate()
		}
		return route.TeamsWeb.validate()
	case domain.MessagingProviderSlack:
		if route.Slack == nil {
			return errors.New("slack requires Slack API settings")
		}
		return route.Slack.validate()
	case domain.MessagingProviderMattermost:
		if route.Mattermost == nil {
			return errors.New("mattermost requires Mattermost API settings")
		}
		return route.Mattermost.validate()
	default:
		return fmt.Errorf("provider %q cannot supply a messaging route", route.Provider)
	}
}

func (route MessagingRoute) Kind() domain.MessagingRouteKind {
	switch {
	case route.TeamsGraph != nil:
		return domain.MessagingRouteTeamsGraph
	case route.TeamsWeb != nil:
		return domain.MessagingRouteTeamsWeb
	case route.Slack != nil:
		return domain.MessagingRouteSlackAPI
	case route.Mattermost != nil:
		return domain.MessagingRouteMattermost
	default:
		return ""
	}
}

func (route TeamsGraphMessagingRoute) validate() error {
	if err := route.OAuth.validateFor(domain.ProviderMicrosoftGraph); err != nil {
		return err
	}
	return validateWorkspaceID(route.WorkspaceID)
}

func (route TeamsWebMessagingRoute) validate() error {
	if err := route.Web.validateFor("teams.microsoft.com"); err != nil {
		return err
	}
	return validateWorkspaceID(route.WorkspaceID)
}

func (route SlackMessagingRoute) validate() error {
	if err := validateHTTPSURL("Slack API base", route.APIBase); err != nil {
		return err
	}
	parsed, _ := url.Parse(route.APIBase)
	if (parsed.Host != "slack.com" && parsed.Host != "slack-gov.com") ||
		parsed.EscapedPath() != "/api" {
		return errors.New("slack API base must be https://slack.com/api or https://slack-gov.com/api")
	}
	if err := validateWorkspaceID(route.WorkspaceID); err != nil {
		return err
	}
	return route.Authorization.validate(false)
}

func (route MattermostMessagingRoute) validate() error {
	if err := validateOrigin(route.Origin); err != nil {
		return fmt.Errorf("mattermost origin: %w", err)
	}
	if err := validateWorkspaceID(route.WorkspaceID); err != nil {
		return err
	}
	return route.Authorization.validate(false)
}

func validateWorkspaceID(value string) error {
	return validateBoundedText("messaging workspace ID", value, 4096, false)
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
	case *GoogleOAuthRoute:
		return typed == nil
	case *OAuthRoute:
		return typed == nil
	case *WebRoute:
		return typed == nil
	case *CalDAVRoute:
		return typed == nil
	case *TeamsGraphMessagingRoute:
		return typed == nil
	case *TeamsWebMessagingRoute:
		return typed == nil
	case *SlackMessagingRoute:
		return typed == nil
	case *MattermostMessagingRoute:
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
	if err := validateHTTPSURL("JMAP session URL", route.SessionURL); err != nil {
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
	if err := validateHTTPSURL("CalDAV endpoint", route.Endpoint); err != nil {
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

func (route CalDAVTaskRoute) validate() error {
	if err := validateHTTPSURL("CalDAV endpoint", route.Endpoint); err != nil {
		return err
	}
	if err := validateBoundedText("CalDAV task list path", route.TaskListPath, 2048, true); err != nil {
		return err
	}
	if route.TaskListPath != "" && !strings.HasPrefix(route.TaskListPath, "/") {
		return errors.New("CalDAV task list path must be absolute")
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
	if err := route.Client().validate(); err != nil {
		return err
	}
	return validateGoogleClientCredential(
		route.Authorization,
		route.ClientSecret,
	)
}

func (route GoogleOAuthRoute) validateFor(provider domain.ProviderID) error {
	if err := validateHTTPSURL("API base", route.APIBase); err != nil {
		return err
	}
	if err := route.Client().validate(); err != nil {
		return err
	}
	if err := validateGoogleClientCredential(
		route.Authorization,
		route.ClientSecret,
	); err != nil {
		return err
	}
	parsed, _ := url.Parse(route.APIBase)
	switch provider { //nolint:exhaustive // This route type accepts only the two Google API IDs.
	case domain.ProviderGoogle:
		if parsed.Host != "www.googleapis.com" || parsed.RawQuery != "" ||
			parsed.EscapedPath() != "" && parsed.EscapedPath() != "/" {
			return errors.New("google API base must be https://www.googleapis.com")
		}
	case domain.ProviderGoogleTasks:
		if parsed.Host != "tasks.googleapis.com" || parsed.RawQuery != "" ||
			parsed.EscapedPath() != "" && parsed.EscapedPath() != "/" {
			return errors.New("google Tasks API base must be https://tasks.googleapis.com")
		}
	default:
		return fmt.Errorf("provider %q cannot use a Google OAuth route", provider)
	}
	return nil
}

func validateGoogleClientCredential(
	authorization CredentialRef,
	clientSecret CredentialRef,
) error {
	if err := clientSecret.validate(false); err != nil {
		return fmt.Errorf("google OAuth client credential: %w", err)
	}
	if authorization.Backend == clientSecret.Backend &&
		authorization.Key == clientSecret.Key {
		return errors.New("google OAuth grant and client credential require different handles")
	}
	return nil
}

func (route OAuthRoute) validate() error {
	if err := validateHTTPSURL("API base", route.APIBase); err != nil {
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
	case domain.ProviderMicrosoftGraph:
		if err := microsoftcloud.ValidateAPIBase(route.MicrosoftCloud, route.APIBase); err != nil {
			return err
		}
	case domain.ProviderTodoist:
		if route.MicrosoftCloud != "" || parsed.Host != "api.todoist.com" ||
			parsed.RawQuery != "" || parsed.EscapedPath() != "/api/v1" {
			return errors.New("todoist API base must be https://api.todoist.com/api/v1")
		}
		if oauthRedirectUsesEphemeralPort(route.RedirectURI) {
			return errors.New("todoist OAuth requires the fixed loopback port registered for the public client")
		}
	case domain.ProviderGoogle, domain.ProviderGoogleTasks:
		return fmt.Errorf("provider %q requires a typed Google OAuth route", provider)
	case domain.ProviderMicrosoftOWA,
		domain.ProviderGoogleWeb,
		domain.ProviderJMAP,
		domain.ProviderIMAPSMTP,
		domain.ProviderCalDAV,
		domain.ProviderMicrosoftTasks,
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

func (route TickTickOAuthRoute) validate() error {
	if err := validateHTTPSURL("TickTick API base", route.APIBase); err != nil {
		return err
	}
	parsed, _ := url.Parse(route.APIBase)
	if parsed.Host != "api.ticktick.com" || parsed.RawQuery != "" ||
		parsed.EscapedPath() != "" && parsed.EscapedPath() != "/" {
		return errors.New("ticktick API base must be https://api.ticktick.com")
	}
	if err := route.Client().validate(); err != nil {
		return err
	}
	if err := route.ClientSecret.validate(false); err != nil {
		return fmt.Errorf("ticktick OAuth client secret: %w", err)
	}
	if route.ClientSecret.Backend == route.Authorization.Backend &&
		route.ClientSecret.Key == route.Authorization.Key {
		return errors.New("ticktick grant and client secret require different credential handles")
	}
	if oauthRedirectUsesEphemeralPort(route.RedirectURI) {
		return errors.New("ticktick OAuth requires the fixed loopback port registered for the confidential client")
	}
	return nil
}

func oauthRedirectUsesEphemeralPort(raw string) bool {
	redirect, _ := url.Parse(raw)
	port, _ := strconv.ParseUint(redirect.Port(), 10, 16)
	return port == 0
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
		return fmt.Errorf("web origin must be https://%s", host)
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

func validateHTTPSURL(name, raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("%s must be a credential-free HTTPS URL", name)
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

// MessagingProvider returns the independently selected messaging adapter.
func (account Account) MessagingProvider() domain.MessagingProviderID {
	if account.Messages == nil {
		return ""
	}
	return account.Messages.Provider
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
