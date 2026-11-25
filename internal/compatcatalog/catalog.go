// Package compatcatalog holds the canonical, public provider compatibility
// catalog. Each entry records what a user can do with one provider route and
// service today, which deterministic tests back that statement, and which live
// observation, if any, exists. An entry is a documentation claim checked
// against source-owned release gates; it is never a capability observation.
package compatcatalog

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"slices"
	"strings"
)

// SchemaVersion is the only catalog document version this package accepts.
const SchemaVersion = 1

//go:embed catalog.json
var embedded []byte

// Service is the closed set of product surfaces an entry can describe.
type Service string

const (
	ServiceMail     Service = "mail"
	ServiceCalendar Service = "calendar"
	ServiceTasks    Service = "tasks"
	ServiceMessages Service = "messages"
	// ServiceBoundary covers product boundaries that are not one provider
	// service, such as the CLI, local IPC, MCP clients, or distribution.
	ServiceBoundary Service = "boundary"
)

// Services lists every accepted service in rendering order.
func Services() []Service {
	return []Service{ServiceMail, ServiceCalendar, ServiceTasks, ServiceMessages, ServiceBoundary}
}

// Availability is what a user can do with a route in the current source.
type Availability string

const (
	// AvailabilityAvailable needs only an explicit sign-in.
	AvailabilityAvailable Availability = "available"
	// AvailabilityAvailableWithSetup needs an OAuth client the user registers
	// or a credential the user stores outside Corresync first.
	AvailabilityAvailableWithSetup Availability = "available-with-setup"
	// AvailabilityPreview is a source-owned preview that is reachable while
	// release evidence, including live observation, is still missing.
	AvailabilityPreview Availability = "preview"
	// AvailabilityDisabled is implemented code that a release gate keeps
	// unreachable.
	AvailabilityDisabled Availability = "disabled"
	// AvailabilityPlanned is a reserved identifier with a tracking issue and
	// no adapter.
	AvailabilityPlanned Availability = "planned"
	// AvailabilityUnsupported is not offered and has no accepted plan.
	AvailabilityUnsupported Availability = "unsupported"
)

// Availabilities lists every accepted availability state.
func Availabilities() []Availability {
	return []Availability{
		AvailabilityAvailable, AvailabilityAvailableWithSetup, AvailabilityPreview,
		AvailabilityDisabled, AvailabilityPlanned, AvailabilityUnsupported,
	}
}

// Description is the plain-language meaning used in generated documentation.
func (availability Availability) Description() string {
	switch availability {
	case AvailabilityAvailable:
		return "usable after an explicit sign-in"
	case AvailabilityAvailableWithSetup:
		return "usable after you register your own OAuth client or store a credential outside Corresync"
	case AvailabilityPreview:
		return "reachable as a preview; release evidence, including live observation, is still missing"
	case AvailabilityDisabled:
		return "code exists, but a release gate keeps it off"
	case AvailabilityPlanned:
		return "reserved identifier with a tracking issue; no adapter yet"
	case AvailabilityUnsupported:
		return "not offered and not planned"
	default:
		return ""
	}
}

// Reachable reports whether a user can select the route in this build.
func (availability Availability) Reachable() bool {
	switch availability {
	case AvailabilityAvailable, AvailabilityAvailableWithSetup, AvailabilityPreview:
		return true
	case AvailabilityDisabled, AvailabilityPlanned, AvailabilityUnsupported:
		return false
	default:
		return false
	}
}

// Authentication is the closed set of sign-in ownership models.
type Authentication string

const (
	AuthenticationNone               Authentication = "none"
	AuthenticationBrowserSession     Authentication = "browser-session"
	AuthenticationUserOAuthClient    Authentication = "user-oauth-client"
	AuthenticationManagedOAuthClient Authentication = "managed-oauth-client"
	AuthenticationExternalCredential Authentication = "external-credential"
	AuthenticationOSPermission       Authentication = "os-permission"
	AuthenticationLocalAutomation    Authentication = "local-automation"
	AuthenticationUpstreamMCP        Authentication = "upstream-mcp"
)

// Authentications lists every accepted authentication kind.
func Authentications() []Authentication {
	return []Authentication{
		AuthenticationNone, AuthenticationBrowserSession, AuthenticationUserOAuthClient,
		AuthenticationManagedOAuthClient, AuthenticationExternalCredential, AuthenticationOSPermission,
		AuthenticationLocalAutomation, AuthenticationUpstreamMCP,
	}
}

// Description is the plain-language meaning used in generated documentation.
func (authentication Authentication) Description() string {
	switch authentication {
	case AuthenticationNone:
		return "no provider authentication"
	case AuthenticationBrowserSession:
		return "visible, browser-owned provider sign-in"
	case AuthenticationUserOAuthClient:
		return "OAuth through a client you register with the provider"
	case AuthenticationManagedOAuthClient:
		return "OAuth through a Corresync-owned client"
	case AuthenticationExternalCredential:
		return "a credential you keep in the OS keyring or an explicitly configured helper"
	case AuthenticationOSPermission:
		return "a local operating-system permission"
	case AuthenticationLocalAutomation:
		return "documented local application automation"
	case AuthenticationUpstreamMCP:
		return "the provider's own remote MCP server"
	default:
		return ""
	}
}

// LiveState is the closed set of live-evidence states.
type LiveState string

const (
	// LiveObserved links a content-free, commit-bound record under
	// docs/evidence/.
	LiveObserved LiveState = "live-observed"
	// LiveHistorical is an earlier note that did not record the exact commit.
	LiveHistorical LiveState = "historical-not-commit-bound"
	// LiveNotRun means no authorized live observation exists.
	LiveNotRun LiveState = "not-run"
	// LiveRejected means the provider refused the observed route.
	LiveRejected LiveState = "rejected"
)

// LiveStates lists every accepted live-evidence state.
func LiveStates() []LiveState {
	return []LiveState{LiveObserved, LiveHistorical, LiveNotRun, LiveRejected}
}

// Description is the plain-language meaning used in generated documentation.
func (state LiveState) Description() string {
	switch state {
	case LiveObserved:
		return "a content-free record tied to the exact tested commit"
	case LiveHistorical:
		return "an earlier note without an exact commit; context only, not evidence"
	case LiveNotRun:
		return "no authorized live observation exists"
	case LiveRejected:
		return "the provider refused the observed route"
	default:
		return ""
	}
}

// Catalog is the decoded, validated catalog document.
type Catalog struct {
	SchemaVersion int     `json:"schemaVersion"`
	Entries       []Entry `json:"entries"`
}

// Entry is one provider route and service, or one product boundary.
type Entry struct {
	Provider       string                `json:"provider"`
	ProviderName   string                `json:"providerName"`
	Route          string                `json:"route"`
	Service        Service               `json:"service"`
	Availability   Availability          `json:"availability"`
	Authentication Authentication        `json:"authentication"`
	Deterministic  DeterministicEvidence `json:"deterministicEvidence"`
	Live           LiveEvidence          `json:"liveEvidence"`
	Notes          string                `json:"notes,omitempty"`
	Issue          int                   `json:"issue,omitempty"`
	TaskContract   *TaskContract         `json:"taskContract,omitempty"`
}

// DeterministicEvidence names the repository tests that back an entry.
type DeterministicEvidence struct {
	Summary string `json:"summary"`
	// Tests are repository-relative test files or package directories.
	Tests []string `json:"tests,omitempty"`
}

// LiveEvidence records the live-observation state.
type LiveEvidence struct {
	State   LiveState `json:"state"`
	Summary string    `json:"summary"`
	// Record is a repository-relative docs/evidence/*.md path, required only
	// for live-observed entries.
	Record string `json:"record,omitempty"`
}

// TaskContract preserves the task development matrix columns.
type TaskContract struct {
	CRUDState          string `json:"crudState"`
	ReminderRecurrence string `json:"reminderRecurrence"`
	HierarchyMetadata  string `json:"hierarchyMetadata"`
	TimeContract       string `json:"timeContract"`
	Sync               string `json:"sync"`
}

// BoundaryProvider is the provider identifier of every boundary entry.
const BoundaryProvider = "corresync"

// Default decodes and validates the embedded catalog.
func Default() (Catalog, error) {
	return Decode(embedded)
}

// Decode strictly decodes and validates one catalog document.
func Decode(data []byte) (Catalog, error) {
	if len(data) > 1<<20 {
		return Catalog{}, errors.New("compatibility catalog exceeds 1 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var catalog Catalog
	if err := decoder.Decode(&catalog); err != nil {
		return Catalog{}, fmt.Errorf("decode compatibility catalog: %w", err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return Catalog{}, errors.New("compatibility catalog has trailing data")
	}
	if err := catalog.Validate(); err != nil {
		return Catalog{}, err
	}
	return catalog, nil
}

var (
	providerPattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`)
	routePattern    = regexp.MustCompile(`^[a-z][a-z0-9]*(?:_[a-z0-9]+)*$`)
	testPathPattern = regexp.MustCompile(`^(?:cmd|internal|tools|web|site|testdata)(?:/[A-Za-z0-9._-]+)+$`)
	recordPattern   = regexp.MustCompile(`^docs/evidence/[a-z0-9][a-z0-9.-]*\.md$`)
)

// Validate checks closed sets, uniqueness, public-only text, and links.
func (catalog Catalog) Validate() error {
	if catalog.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported compatibility catalog schema version %d", catalog.SchemaVersion)
	}
	if len(catalog.Entries) == 0 || len(catalog.Entries) > 512 {
		return errors.New("compatibility catalog must contain 1 to 512 entries")
	}
	seen := make(map[string]struct{}, len(catalog.Entries))
	names := make(map[string]string)
	for index := range catalog.Entries {
		entry := &catalog.Entries[index]
		if err := entry.validate(); err != nil {
			return fmt.Errorf("compatibility catalog entry %d (%s/%s/%s): %w",
				index, entry.Provider, entry.Service, entry.Route, err)
		}
		key := entry.Key()
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("duplicate compatibility catalog entry %s", key)
		}
		seen[key] = struct{}{}
		if previous, ok := names[entry.Provider]; ok && previous != entry.ProviderName &&
			entry.Service != ServiceBoundary {
			return fmt.Errorf("provider %q has inconsistent display names", entry.Provider)
		}
		if entry.Service != ServiceBoundary {
			names[entry.Provider] = entry.ProviderName
		}
	}
	return nil
}

// Key is the unique provider/service/route identity of an entry.
func (entry Entry) Key() string {
	return entry.Provider + "/" + string(entry.Service) + "/" + entry.Route
}

func (entry Entry) validate() error {
	if len(entry.Provider) > 64 || !providerPattern.MatchString(entry.Provider) {
		return errors.New("invalid provider identifier")
	}
	if len(entry.Route) > 64 || !routePattern.MatchString(entry.Route) {
		return errors.New("invalid route identifier")
	}
	if !slices.Contains(Services(), entry.Service) {
		return fmt.Errorf("unknown service %q", entry.Service)
	}
	if !slices.Contains(Availabilities(), entry.Availability) {
		return fmt.Errorf("unknown availability %q", entry.Availability)
	}
	if !slices.Contains(Authentications(), entry.Authentication) {
		return fmt.Errorf("unknown authentication %q", entry.Authentication)
	}
	if !slices.Contains(LiveStates(), entry.Live.State) {
		return fmt.Errorf("unknown live evidence state %q", entry.Live.State)
	}
	if (entry.Service == ServiceBoundary) != (entry.Provider == BoundaryProvider) {
		return fmt.Errorf("boundary entries and only boundary entries use provider %q", BoundaryProvider)
	}
	if (entry.Service == ServiceTasks && entry.Provider != BoundaryProvider) != (entry.TaskContract != nil) {
		return errors.New("task entries and only task entries carry a task contract")
	}
	for _, field := range []struct{ name, value string }{
		{"provider name", entry.ProviderName},
		{"deterministic summary", entry.Deterministic.Summary},
		{"live evidence summary", entry.Live.Summary},
	} {
		if err := validateText(field.name, field.value, true); err != nil {
			return err
		}
	}
	if err := validateText("notes", entry.Notes, false); err != nil {
		return err
	}
	if contract := entry.TaskContract; contract != nil {
		for _, field := range []struct{ name, value string }{
			{"task CRUD/state", contract.CRUDState},
			{"task reminder/recurrence", contract.ReminderRecurrence},
			{"task hierarchy/metadata", contract.HierarchyMetadata},
			{"task time contract", contract.TimeContract},
			{"task sync", contract.Sync},
		} {
			if err := validateText(field.name, field.value, true); err != nil {
				return err
			}
		}
	}
	if entry.Issue < 0 || entry.Issue > 1_000_000 {
		return errors.New("issue number is out of range")
	}
	if entry.Availability == AvailabilityPlanned && entry.Issue == 0 {
		return errors.New("a planned entry needs a tracking issue")
	}
	if len(entry.Deterministic.Tests) > 16 {
		return errors.New("too many deterministic test references")
	}
	if entry.Availability.Reachable() && len(entry.Deterministic.Tests) == 0 {
		return errors.New("a reachable entry needs at least one deterministic test reference")
	}
	testSeen := make(map[string]struct{}, len(entry.Deterministic.Tests))
	for _, test := range entry.Deterministic.Tests {
		if err := validateTestPath(test); err != nil {
			return err
		}
		if _, duplicate := testSeen[test]; duplicate {
			return fmt.Errorf("duplicate test reference %q", test)
		}
		testSeen[test] = struct{}{}
	}
	switch entry.Live.State {
	case LiveObserved:
		if !recordPattern.MatchString(entry.Live.Record) || path.Clean(entry.Live.Record) != entry.Live.Record {
			return errors.New("a live-observed entry needs a docs/evidence/*.md record")
		}
	case LiveHistorical, LiveNotRun, LiveRejected:
		if entry.Live.Record != "" {
			return errors.New("only a live-observed entry may link an evidence record")
		}
	}
	return nil
}

func validateTestPath(test string) error {
	if len(test) > 256 || !testPathPattern.MatchString(test) || path.Clean(test) != test ||
		strings.Contains(test, "/../") || strings.Contains(test, "/./") {
		return fmt.Errorf("invalid test reference %q", test)
	}
	if IsTestFileName(path.Base(test)) || path.Ext(test) == "" {
		return nil
	}
	return fmt.Errorf("test reference %q is neither a test file nor a package directory", test)
}

// IsTestFileName reports whether a file name follows a recognized test
// naming convention.
func IsTestFileName(name string) bool {
	return strings.HasSuffix(name, "_test.go") ||
		strings.HasSuffix(name, ".test.mjs") ||
		strings.HasSuffix(name, ".test.cjs")
}

// validateText keeps catalog text single-line, table-safe, and free of
// endpoints, addresses, and markup so the public catalog cannot carry
// account data or secrets.
func validateText(field, value string, required bool) error {
	if value == "" {
		if required {
			return fmt.Errorf("%s must not be empty", field)
		}
		return nil
	}
	if len(value) > 600 {
		return fmt.Errorf("%s exceeds 600 bytes", field)
	}
	if strings.TrimSpace(value) != value {
		return fmt.Errorf("%s must not start or end with whitespace", field)
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return fmt.Errorf("%s contains a control character", field)
		}
	}
	for _, forbidden := range []string{"|", "<", ">", "://", "@", "\\"} {
		if strings.Contains(value, forbidden) {
			return fmt.Errorf("%s must not contain %q", field, forbidden)
		}
	}
	return nil
}
