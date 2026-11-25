// Package livetest holds the shared, network-free parts of the opt-in live
// observation harnesses: the route catalog, the environment opt-in gate, the
// content-free observation record, and the guard that keeps every harness out
// of default test runs and CI.
//
// Nothing in this package contacts a provider. The harnesses that do are
// build-tagged `live` test files; see docs/evidence/README.md for the index.
package livetest

import "github.com/nkiyohara/corresync/internal/domain"

// Route is the stable opt-in identifier of one shipped provider route.
type Route string

const (
	RouteOutlookWeb               Route = "outlook-web"
	RouteOutlookWebForwardedLogin Route = "outlook-web-forwarded-login"
	RouteGoogle                   Route = "google"
	RouteGoogleTasks              Route = "google-tasks"
	RouteMicrosoftGraph           Route = "microsoft-graph"
	RouteMicrosoftTodo            Route = "microsoft-todo"
	RouteJMAP                     Route = "jmap"
	RouteIMAPSMTP                 Route = "imap-smtp"
	RouteCalDAV                   Route = "caldav"
	RouteCalDAVTasks              Route = "caldav-tasks"
	RouteTodoist                  Route = "todoist"
	RouteTickTick                 Route = "ticktick"
	RouteTeamsGraph               Route = "teams-graph"
	RouteTeamsWeb                 Route = "teams-web"
	RouteMattermost               Route = "mattermost"
)

// RouteInfo describes one route and where its observation procedure lives.
// Exactly one of Harness or Manual is set.
type RouteInfo struct {
	ID   Route
	Name string
	// Harness is the repository-relative build-tagged test file and Test the
	// test function inside it.
	Harness string
	Test    string
	// Manual is the repository-relative document section holding the manual
	// procedure for a route a Go test cannot drive.
	Manual string

	Mail           domain.ProviderID
	Calendar       domain.ProviderID
	Tasks          domain.ProviderID
	Messaging      domain.MessagingProviderID
	MessagingRoute domain.MessagingRouteKind
	// MailWrite is true only when the route can both save a draft and delete
	// that exact draft again, so a synthetic draft can be fully cleaned up.
	MailWrite bool
}

// Providers returns the distinct provider identifiers the route exercises.
func (info RouteInfo) Providers() []string {
	providers := make([]string, 0, 4)
	seen := make(map[string]bool, 4)
	for _, provider := range []string{
		string(info.Mail), string(info.Calendar), string(info.Tasks), string(info.Messaging),
	} {
		if provider != "" && !seen[provider] {
			seen[provider] = true
			providers = append(providers, provider)
		}
	}
	return providers
}

const harnessDirectory = "cmd/corr/"

// Catalog lists every shipped route in index order.
func Catalog() []RouteInfo {
	return []RouteInfo{
		{
			ID: RouteOutlookWeb, Name: "Outlook Web mail and calendar",
			Harness: harnessDirectory + "outlook_web_live_test.go", Test: "TestLiveOutlookWebRoute",
			Mail: domain.ProviderMicrosoftOWA, Calendar: domain.ProviderMicrosoftOWA, MailWrite: true,
		},
		{
			ID: RouteOutlookWebForwardedLogin, Name: "Outlook Web forwarded login view",
			Manual: "docs/manual-test-checklist.md#forwarded-login-view-observation",
			Mail:   domain.ProviderMicrosoftOWA, Calendar: domain.ProviderMicrosoftOWA,
		},
		{
			ID: RouteGoogle, Name: "Gmail and Google Calendar API (user-owned OAuth client)",
			Harness: harnessDirectory + "google_live_test.go", Test: "TestLiveGoogleRoute",
			// Gmail cannot permanently delete a draft through this route, so a
			// synthetic draft could not be cleaned up completely.
			Mail: domain.ProviderGoogle, Calendar: domain.ProviderGoogle,
		},
		{
			ID: RouteGoogleTasks, Name: "Google Tasks API (user-owned OAuth client)",
			Harness: harnessDirectory + "google_tasks_live_test.go", Test: "TestLiveGoogleTasksRoute",
			Tasks: domain.ProviderGoogleTasks,
		},
		{
			ID: RouteMicrosoftGraph, Name: "Microsoft Graph mail and calendar",
			Harness: harnessDirectory + "microsoft_graph_live_test.go", Test: "TestLiveMicrosoftGraphRoute",
			Mail: domain.ProviderMicrosoftGraph, Calendar: domain.ProviderMicrosoftGraph, MailWrite: true,
		},
		{
			ID: RouteMicrosoftTodo, Name: "Microsoft To Do through Microsoft Graph",
			Harness: harnessDirectory + "microsoft_todo_live_test.go", Test: "TestLiveMicrosoftTodoRoute",
			Tasks: domain.ProviderMicrosoftGraph,
		},
		{
			ID: RouteJMAP, Name: "JMAP mail",
			Harness: harnessDirectory + "jmap_live_test.go", Test: "TestLiveJMAPRoute",
			Mail: domain.ProviderJMAP, MailWrite: true,
		},
		{
			ID: RouteIMAPSMTP, Name: "IMAP/SMTP mail",
			Harness: harnessDirectory + "imap_smtp_live_test.go", Test: "TestLiveIMAPSMTPRoute",
			Mail: domain.ProviderIMAPSMTP, MailWrite: true,
		},
		{
			ID: RouteCalDAV, Name: "CalDAV calendar",
			Harness: harnessDirectory + "caldav_live_test.go", Test: "TestLiveCalDAVRoute",
			Calendar: domain.ProviderCalDAV,
		},
		{
			ID: RouteCalDAVTasks, Name: "CalDAV VTODO tasks",
			Harness: harnessDirectory + "caldav_tasks_live_test.go", Test: "TestLiveCalDAVTasksRoute",
			Tasks: domain.ProviderCalDAV,
		},
		{
			ID: RouteTodoist, Name: "Todoist tasks",
			Harness: harnessDirectory + "todoist_live_test.go", Test: "TestLiveTodoistRoute",
			Tasks: domain.ProviderTodoist,
		},
		{
			ID: RouteTickTick, Name: "TickTick tasks",
			Harness: harnessDirectory + "ticktick_live_test.go", Test: "TestLiveTickTickRoute",
			Tasks: domain.ProviderTickTick,
		},
		{
			ID: RouteTeamsGraph, Name: "Microsoft Teams through Microsoft Graph (preview)",
			Harness: harnessDirectory + "teams_graph_live_test.go", Test: "TestLiveTeamsGraphRoute",
			Messaging:      domain.MessagingProviderMicrosoftTeams,
			MessagingRoute: domain.MessagingRouteTeamsGraph,
		},
		{
			ID: RouteTeamsWeb, Name: "Microsoft Teams on the web (preview)",
			Harness: harnessDirectory + "teams_web_live_test.go", Test: "TestLiveTeamsWebRoute",
			Messaging:      domain.MessagingProviderMicrosoftTeams,
			MessagingRoute: domain.MessagingRouteTeamsWeb,
		},
		{
			ID: RouteMattermost, Name: "Mattermost (preview)",
			Harness: harnessDirectory + "mattermost_live_test.go", Test: "TestLiveMattermostRoute",
			Messaging:      domain.MessagingProviderMattermost,
			MessagingRoute: domain.MessagingRouteMattermost,
		},
	}
}

// Lookup returns the catalog entry for one route.
func Lookup(route Route) (RouteInfo, bool) {
	for _, info := range Catalog() {
		if info.ID == route {
			return info, true
		}
	}
	return RouteInfo{}, false
}
