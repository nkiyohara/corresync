package livetest

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/provider/restapi"
)

func environment(values map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}

func completeOptIn(route Route) map[string]string {
	return map[string]string{
		EnvConfirm:  string(route),
		EnvAccount:  "live-test",
		EnvConfig:   "/tmp/live/config.toml",
		EnvStateDir: "/tmp/live/state",
	}
}

func TestParseOptInSkipsUnlessTheExactRouteIsConfirmed(t *testing.T) {
	t.Parallel()
	for _, confirm := range []string{"", "1", "true", "outlook-web ", "google", "microsoft-todo-read-only"} {
		values := completeOptIn(RouteOutlookWeb)
		values[EnvConfirm] = confirm
		_, confirmed, err := ParseOptIn(RouteOutlookWeb, environment(values))
		if confirmed || err != nil {
			t.Fatalf("confirm %q: confirmed=%v err=%v", confirm, confirmed, err)
		}
	}
}

func TestParseOptInRejectsIncompleteOrMismatchedOptIn(t *testing.T) {
	t.Parallel()
	cases := map[string]func(map[string]string){
		"missing alias":           func(v map[string]string) { delete(v, EnvAccount) },
		"relative config":         func(v map[string]string) { v[EnvConfig] = "config.toml" },
		"missing state dir":       func(v map[string]string) { delete(v, EnvStateDir) },
		"write for another route": func(v map[string]string) { v[EnvWrite] = "google:live-test" },
		"write for another alias": func(v map[string]string) { v[EnvWrite] = "outlook-web:other" },
		"bare write flag":         func(v map[string]string) { v[EnvWrite] = "1" },
		"target without write":    func(v map[string]string) { v[EnvCalendarName] = "Live tests" },
		"relative record":         func(v map[string]string) { v[EnvRecord] = "record.md" },
		"free-form deployment":    func(v map[string]string) { v[EnvDeployment] = "Contoso tenant" },
		"address as platform":     func(v map[string]string) { v[EnvPlatform] = "reader@example.invalid" },
		"multiline target": func(v map[string]string) {
			v[EnvWrite] = "outlook-web:live-test"
			v[EnvCalendarName] = "a\nb"
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			values := completeOptIn(RouteOutlookWeb)
			mutate(values)
			if _, confirmed, err := ParseOptIn(RouteOutlookWeb, environment(values)); !confirmed || err == nil {
				t.Fatalf("confirmed=%v err=%v", confirmed, err)
			}
		})
	}
}

func TestParseOptInRejectsManualRoute(t *testing.T) {
	t.Parallel()
	values := completeOptIn(RouteOutlookWebForwardedLogin)
	if _, _, err := ParseOptIn(RouteOutlookWebForwardedLogin, environment(values)); err == nil {
		t.Fatal("a manual route must not be accepted by a harness")
	}
}

func TestParseOptInBindsWritesToRouteAndAlias(t *testing.T) {
	t.Parallel()
	values := completeOptIn(RouteCalDAV)
	values[EnvWrite] = "caldav:live-test"
	values[EnvCalendarName] = "Corresync live tests"
	values[EnvDeployment] = "self-hosted"
	optIn, confirmed, err := ParseOptIn(RouteCalDAV, environment(values))
	if err != nil || !confirmed || !optIn.Write || optIn.Deployment != "self-hosted" {
		t.Fatalf("optIn=%+v confirmed=%v err=%v", optIn, confirmed, err)
	}
	if optIn.Platform != unrecorded || optIn.BrowserKeyring != unrecorded {
		t.Fatalf("unset operator text must render as %q", unrecorded)
	}
}

func TestCatalogIsCompleteAndConsistent(t *testing.T) {
	t.Parallel()
	seen := map[Route]bool{}
	for _, info := range Catalog() {
		if seen[info.ID] {
			t.Fatalf("duplicate route %s", info.ID)
		}
		seen[info.ID] = true
		if (info.Harness == "") == (info.Manual == "") {
			t.Fatalf("%s must have exactly one of a harness or a manual procedure", info.ID)
		}
		if info.Harness != "" && (!strings.HasSuffix(info.Harness, "_live_test.go") || !strings.HasPrefix(info.Test, "TestLive")) {
			t.Fatalf("%s harness %s/%s is misnamed", info.ID, info.Harness, info.Test)
		}
		if len(info.Providers()) == 0 {
			t.Fatalf("%s exercises no provider", info.ID)
		}
		if info.MailWrite && info.Mail == "" {
			t.Fatalf("%s allows a mail write without a mail service", info.ID)
		}
	}
	if google, _ := Lookup(RouteGoogle); google.MailWrite {
		t.Fatal("Gmail cannot permanently delete a draft, so it must stay read-only for mail")
	}
}

const testCommit = "17b9ae267ec956ae3f91e046cd2521cb0e755fee"

func sampleRecord(t *testing.T) *Record {
	t.Helper()
	values := completeOptIn(RouteOutlookWeb)
	values[EnvWrite] = "outlook-web:live-test"
	values[EnvCalendarName] = "Private calendar name"
	values[EnvDeployment] = "work-or-school"
	values[EnvPlatform] = "macOS 26.5.2"
	optIn, _, err := ParseOptIn(RouteOutlookWeb, environment(values))
	if err != nil {
		t.Fatal(err)
	}
	record := NewRecord(optIn, testCommit, time.Date(2026, 11, 25, 9, 0, 0, 0, time.UTC))
	started := time.Now().Add(-120 * time.Millisecond)
	record.Observe("session.authenticate", started, -1, false, nil)
	record.Observe("mail.messages.list", started, 7, false, nil)
	record.Observe("calendar.events.list", started, 0, true, nil)
	record.Mark("mail.draft.write", OutcomeSkipped, NoteWritesNotEnabled)
	record.AddCapabilities(domain.Capabilities{Mail: true, Calendar: true, OnlineMeeting: "teams"})
	record.AddDegradations([]domain.Degradation{
		{Feature: "mail.delete", Reason: "reader@example.invalid said no", Lossy: true},
		{Feature: "subject: Quarterly results", Reason: "free text"},
	})
	return record
}

func TestRecordMarkdownIsContentFree(t *testing.T) {
	t.Parallel()
	record := sampleRecord(t)
	rendered, err := record.Markdown()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Commit: `" + testCommit + "`",
		"Observation date: 2026-11-25",
		"`microsoft-owa`, work-or-school",
		"Harness: `cmd/corr/outlook_web_live_test.go`",
		"| `mail.messages.list` | pass | 7 |",
		"| `calendar.events.list` | degraded | 0 |",
		"online-meeting:teams",
		"mail.delete (lossy)",
		"unrecognized-feature",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("record lacks %q:\n%s", want, rendered)
		}
	}
	for _, forbidden := range []string{"live-test", "/tmp/live", "Private calendar name", "example.invalid", "Quarterly", "free text"} {
		if strings.Contains(rendered, forbidden) {
			t.Errorf("record leaks %q:\n%s", forbidden, rendered)
		}
	}
	if got := record.Result(); got != OutcomeDegraded {
		t.Fatalf("Result() = %s, want degraded", got)
	}
	if got := record.SuggestedFileName(); got != fmt.Sprintf("outlook-web-%s-%s-2026-11-25.md", record.GOOS, record.GOARCH) {
		t.Fatalf("SuggestedFileName() = %s", got)
	}
}

func TestRecordValidateRejectsFreeText(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*Record){
		"short commit":      func(r *Record) { r.Commit = "17b9ae2" },
		"free operation":    func(r *Record) { r.Steps[0].Operation = "list Inbox for reader@example.invalid" },
		"free note":         func(r *Record) { r.Steps[0].Note = "HTTP 403: Access denied for tenant Contoso" },
		"unknown outcome":   func(r *Record) { r.Steps[0].Outcome = "ok" },
		"free capability":   func(r *Record) { r.Capabilities = append(r.Capabilities, "Project Phoenix") },
		"unknown cleanup":   func(r *Record) { r.Cleanup = "probably" },
		"free deployment":   func(r *Record) { r.Deployment = "Contoso" },
		"uncatalogued path": func(r *Record) { r.Route = RouteInfo{ID: "custom"} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			record := sampleRecord(t)
			mutate(record)
			if _, err := record.Markdown(); err == nil {
				t.Fatal("Markdown() accepted a record that could carry content")
			}
		})
	}
}

func TestRecordResult(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		steps   []Outcome
		cleanup Cleanup
		want    Outcome
	}{
		{"nothing observed", []Outcome{OutcomeSkipped, OutcomeUnobserved}, CleanupNotApplicable, OutcomeUnobserved},
		{"pass", []Outcome{OutcomePass, OutcomeSkipped}, CleanupNotApplicable, OutcomePass},
		{"degraded", []Outcome{OutcomePass, OutcomeDegraded}, CleanupConfirmed, OutcomeDegraded},
		{"failure wins", []Outcome{OutcomeDegraded, OutcomeFail, OutcomePass}, CleanupConfirmed, OutcomeFail},
		{"cleanup unconfirmed fails", []Outcome{OutcomePass}, CleanupUnconfirmed, OutcomeFail},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			record := &Record{Cleanup: test.cleanup}
			for _, outcome := range test.steps {
				record.Mark("step", outcome, "")
			}
			if got := record.Result(); got != test.want {
				t.Fatalf("Result() = %s, want %s", got, test.want)
			}
		})
	}
}

func TestClassifyKeepsOnlyFixedClasses(t *testing.T) {
	t.Parallel()
	action := application.AuthenticationActionRequired{Code: "reauthentication_required"}
	cases := []struct {
		err  error
		want string
	}{
		{nil, ""},
		{fmt.Errorf("list: %w", context.DeadlineExceeded), "timeout"},
		{context.Canceled, "cancelled"},
		{fmt.Errorf("x: %w", application.ErrDenied), "policy-denied"},
		{application.ErrWriteOutcomeUnknown, "write-outcome-unknown"},
		{&application.AuthenticationActionError{Action: action}, "authentication-required:reauthentication_required"},
		{fmt.Errorf("read: %w", &restapi.StatusError{Status: 403, Code: "Tenant Contoso blocked"}), "http-403"},
		{errors.New("mailbox reader@example.invalid is full"), "error"},
	}
	for _, test := range cases {
		if got := Classify(test.err); got != test.want {
			t.Errorf("Classify(%v) = %q, want %q", test.err, got, test.want)
		}
	}
}
