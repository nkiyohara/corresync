package livetest

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"unicode"
)

// Environment variables read by the live harnesses. Only an operator sets
// them, by hand, for one run. No workflow, task, or hook may set them; the
// guard in this package fails the default test run if one does.
const (
	// EnvConfirm must equal the exact route identifier. Anything else skips.
	EnvConfirm = "CORRESYNC_LIVE_CONFIRM"
	// EnvAccount is the configured account alias the run may use.
	EnvAccount = "CORRESYNC_LIVE_ACCOUNT"
	// EnvConfig is the absolute path of the config file holding that alias.
	EnvConfig = "CORRESYNC_LIVE_CONFIG"
	// EnvStateDir is Corresync's existing state-root override. The harness
	// requires it so browser profiles, audit files, and session state of a
	// live run never mix with everyday state.
	EnvStateDir = "CORRESYNC_STATE_DIR"
	// EnvWrite enables the reversible synthetic writes. It must equal
	// "<route>:<alias>" so a write opt-in can never carry over to another
	// route or account.
	EnvWrite = "CORRESYNC_LIVE_WRITE"
	// EnvCalendarName names the calendar the operator set aside for tests.
	EnvCalendarName = "CORRESYNC_LIVE_CALENDAR_NAME"
	// EnvTaskListName names the task list the operator set aside for tests.
	EnvTaskListName = "CORRESYNC_LIVE_TASK_LIST_NAME"
	// EnvRecord is an optional absolute path for the record. The file must
	// not exist yet.
	EnvRecord = "CORRESYNC_LIVE_RECORD"
	// EnvDeployment is an optional broad deployment class from a closed set.
	EnvDeployment = "CORRESYNC_LIVE_DEPLOYMENT_CLASS"
	// EnvPlatform is an optional OS version, such as "macOS 26.5.2".
	EnvPlatform = "CORRESYNC_LIVE_PLATFORM"
	// EnvBrowserKeyring is an optional browser and keyring family and version.
	EnvBrowserKeyring = "CORRESYNC_LIVE_BROWSER_KEYRING"
)

// DeploymentClasses is the closed set accepted by EnvDeployment. A free-form
// value could carry a tenant or organization name.
var DeploymentClasses = []string{
	"consumer", "work-or-school", "education", "government-cloud",
	"self-hosted", "hosted-service",
}

const unrecorded = "not recorded"

var operatorTextPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._()+-]{0,63}$`)

// OptIn is the validated opt-in for one run. Account, ConfigPath, and the
// target names steer the run but never enter the record.
type OptIn struct {
	Route          RouteInfo
	Account        string
	ConfigPath     string
	StateDir       string
	Write          bool
	CalendarName   string
	TaskListName   string
	RecordPath     string
	Deployment     string
	Platform       string
	BrowserKeyring string
}

// ParseOptIn reads the opt-in for route through lookup. It returns false
// without an error when the operator did not confirm this exact route, and an
// error when the route was confirmed but the rest of the opt-in is incomplete
// or inconsistent.
func ParseOptIn(route Route, lookup func(string) (string, bool)) (OptIn, bool, error) {
	if value, _ := lookup(EnvConfirm); value != string(route) {
		return OptIn{}, false, nil
	}
	info, known := Lookup(route)
	if !known {
		return OptIn{}, true, fmt.Errorf("live route %q is not in the catalog", route)
	}
	if info.Harness == "" {
		return OptIn{}, true, fmt.Errorf("live route %q has a manual procedure, not a harness", route)
	}
	get := func(name string) string {
		value, _ := lookup(name)
		return value
	}
	optIn := OptIn{
		Route:          info,
		Account:        get(EnvAccount),
		ConfigPath:     get(EnvConfig),
		StateDir:       get(EnvStateDir),
		CalendarName:   get(EnvCalendarName),
		TaskListName:   get(EnvTaskListName),
		RecordPath:     get(EnvRecord),
		Deployment:     get(EnvDeployment),
		Platform:       get(EnvPlatform),
		BrowserKeyring: get(EnvBrowserKeyring),
	}
	var problems []error
	if err := validateAlias(optIn.Account); err != nil {
		problems = append(problems, err)
	}
	if !filepath.IsAbs(optIn.ConfigPath) {
		problems = append(problems, fmt.Errorf("%s must be an absolute path to the config holding the alias", EnvConfig))
	}
	if !filepath.IsAbs(optIn.StateDir) {
		problems = append(problems, fmt.Errorf("%s must be an absolute path to a state directory used only for live runs", EnvStateDir))
	}
	switch write := get(EnvWrite); write {
	case "":
		if optIn.CalendarName != "" || optIn.TaskListName != "" {
			problems = append(problems, fmt.Errorf("a write target is named but %s is not set", EnvWrite))
		}
	case string(route) + ":" + optIn.Account:
		optIn.Write = true
	default:
		problems = append(problems, fmt.Errorf("%s must be exactly %q to allow writes on this route and alias", EnvWrite, string(route)+":<alias>"))
	}
	for _, target := range []struct{ name, value string }{
		{EnvCalendarName, optIn.CalendarName}, {EnvTaskListName, optIn.TaskListName},
	} {
		if target.value != "" && !singleLine(target.value, 256) {
			problems = append(problems, fmt.Errorf("%s must be one line of at most 256 bytes", target.name))
		}
	}
	if optIn.RecordPath != "" && !filepath.IsAbs(optIn.RecordPath) {
		problems = append(problems, fmt.Errorf("%s must be an absolute path", EnvRecord))
	}
	switch {
	case optIn.Deployment == "":
		optIn.Deployment = unrecorded
	case !slices.Contains(DeploymentClasses, optIn.Deployment):
		problems = append(problems, fmt.Errorf("%s must be one of %s", EnvDeployment, strings.Join(DeploymentClasses, ", ")))
	}
	for _, field := range []struct {
		name  string
		value *string
	}{{EnvPlatform, &optIn.Platform}, {EnvBrowserKeyring, &optIn.BrowserKeyring}} {
		switch {
		case *field.value == "":
			*field.value = unrecorded
		case !operatorTextPattern.MatchString(*field.value):
			problems = append(problems, fmt.Errorf("%s must be 1-64 letters, digits, spaces, or . _ ( ) + -", field.name))
		}
	}
	if len(problems) != 0 {
		return OptIn{}, true, errors.Join(problems...)
	}
	return optIn, true, nil
}

// Require is the first statement of every live harness test. It skips the
// test unless the operator confirmed this exact route, and stops it when the
// opt-in is confirmed but incomplete.
func Require(t testing.TB, route Route) OptIn {
	t.Helper()
	optIn, confirmed, err := ParseOptIn(route, os.LookupEnv)
	if !confirmed {
		t.Skipf("live route %s is opt-in; see docs/evidence/README.md", route)
	}
	if err != nil {
		t.Fatalf("live route %s opt-in is incomplete: %v", route, err)
	}
	return optIn
}

func validateAlias(alias string) error {
	if !singleLine(alias, 64) || strings.TrimSpace(alias) != alias {
		return fmt.Errorf("%s must name one configured account alias", EnvAccount)
	}
	return nil
}

func singleLine(value string, maximum int) bool {
	if value == "" || len(value) > maximum {
		return false
	}
	return !strings.ContainsFunc(value, unicode.IsControl)
}
