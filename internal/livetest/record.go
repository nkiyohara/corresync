package livetest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/provider/restapi"
)

// Outcome is the content-free result of one observed step.
type Outcome string

const (
	OutcomePass       Outcome = "pass"
	OutcomeDegraded   Outcome = "degraded"
	OutcomeFail       Outcome = "fail"
	OutcomeUnobserved Outcome = "unobserved"
	OutcomeSkipped    Outcome = "skipped"
)

// Cleanup states whether every synthetic item a run created is gone again.
type Cleanup string

const (
	CleanupNotApplicable Cleanup = "not applicable (no item created)"
	CleanupConfirmed     Cleanup = "confirmed"
	CleanupUnconfirmed   Cleanup = "NOT CONFIRMED"
)

// Fixed step notes. Error notes come only from Classify.
const (
	NotePartial           = "partial"
	NoteEmpty             = "nothing-to-read"
	NoteNotConfigured     = "service-not-configured"
	NoteNoReversibleWrite = "no-reversible-write"
	NoteWritesNotEnabled  = "writes-not-enabled"
	NoteTargetNotNamed    = "target-not-named"
	NoteTargetNotFound    = "target-not-found"
	NoteTargetAmbiguous   = "target-ambiguous"
	NoteTargetReadOnly    = "target-read-only"
	NoteNotCreated        = "item-not-created"
	NoteStillPresent      = "item-still-present"
	NoteAbsenceUnverified = "absence-unverified"
	NoteMissingResult     = "missing-result"
)

// Step is one bounded operation. It holds no provider value: the operation is
// a harness constant, the note a fixed code, and the count a number of items.
type Step struct {
	Operation string
	Outcome   Outcome
	Count     int
	HasCount  bool
	Duration  time.Duration
	Note      string
}

// Record is a content-free observation in the format of
// docs/evidence/README.md.
type Record struct {
	Commit         string
	Date           time.Time
	GOOS           string
	GOARCH         string
	Platform       string
	BrowserKeyring string
	Route          RouteInfo
	Deployment     string
	Write          bool
	Capabilities   []string
	Degradations   []string
	Steps          []Step
	Cleanup        Cleanup
}

// NewRecord starts a record for one validated opt-in at commit.
func NewRecord(optIn OptIn, commit string, now time.Time) *Record {
	return &Record{
		Commit:         commit,
		Date:           now.UTC(),
		GOOS:           runtime.GOOS,
		GOARCH:         runtime.GOARCH,
		Platform:       optIn.Platform,
		BrowserKeyring: optIn.BrowserKeyring,
		Route:          optIn.Route,
		Deployment:     optIn.Deployment,
		Write:          optIn.Write,
		Cleanup:        CleanupNotApplicable,
	}
}

// Observe records one step from its start time, item count, and error. A nil
// error passes, a partial result degrades, and any error fails with only its
// class recorded.
func (record *Record) Observe(operation string, started time.Time, count int, partial bool, err error) Outcome {
	step := Step{
		Operation: operation, Outcome: OutcomePass,
		Count: count, HasCount: err == nil && count >= 0,
		Duration: time.Since(started),
	}
	switch {
	case err != nil:
		step.Outcome, step.Note = OutcomeFail, Classify(err)
	case partial:
		step.Outcome, step.Note = OutcomeDegraded, NotePartial
	}
	record.Steps = append(record.Steps, step)
	return step.Outcome
}

// Mark records a step that did not run or whose outcome the harness decided.
func (record *Record) Mark(operation string, outcome Outcome, note string) {
	record.Steps = append(record.Steps, Step{Operation: operation, Outcome: outcome, Note: note})
}

// AddCapabilities records the names of the capabilities observed after
// sign-in. Values are fixed field names, never provider text.
func (record *Record) AddCapabilities(capabilities domain.Capabilities) {
	for _, capability := range []struct {
		name string
		on   bool
	}{
		{"mail", capabilities.Mail}, {"calendar", capabilities.Calendar},
		{"tasks", capabilities.Tasks}, {"messages", capabilities.Messages},
		{"folders", capabilities.Folders}, {"labels", capabilities.Labels},
		{"push", capabilities.Push}, {"free-busy", capabilities.FreeBusy},
		{"incremental-sync", capabilities.IncrementalSync},
		{"scheduled-send", capabilities.ScheduledSend},
		{"shared-mailboxes", capabilities.SharedMailboxes},
		{"shared-calendars", capabilities.SharedCalendars},
		{"attachment-reads", capabilities.AttachmentReads},
		{"attachment-writes", capabilities.AttachmentWrites},
		{"draft-send", capabilities.DraftSend},
	} {
		if capability.on {
			record.Capabilities = append(record.Capabilities, capability.name)
		}
	}
	switch capabilities.OnlineMeeting {
	case "teams", "google-meet", "provider":
		record.Capabilities = append(record.Capabilities, "online-meeting:"+capabilities.OnlineMeeting)
	}
}

var featurePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,95}$`)

// AddDegradations records degradation feature codes and their lossy flag.
// The free-text reason is never recorded.
func (record *Record) AddDegradations(degradations []domain.Degradation) {
	for _, degradation := range degradations {
		feature := degradation.Feature
		if !featurePattern.MatchString(feature) {
			feature = "unrecognized-feature"
		}
		if degradation.Lossy {
			feature += " (lossy)"
		}
		if !slices.Contains(record.Degradations, feature) {
			record.Degradations = append(record.Degradations, feature)
		}
	}
}

// Result summarizes every step. A failure or unconfirmed cleanup fails the
// run, a degraded step degrades it, and a run in which nothing passed is
// unobserved rather than passed.
func (record *Record) Result() Outcome {
	result := OutcomeUnobserved
	for _, step := range record.Steps {
		switch step.Outcome {
		case OutcomeFail:
			return OutcomeFail
		case OutcomePass:
			if result == OutcomeUnobserved {
				result = OutcomePass
			}
		case OutcomeDegraded:
			result = OutcomeDegraded
		case OutcomeUnobserved, OutcomeSkipped:
		}
	}
	if record.Cleanup == CleanupUnconfirmed {
		return OutcomeFail
	}
	return result
}

var (
	commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
	codePattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9_.:-]{0,95}$`)
)

// Validate rejects a record that could carry anything other than fixed codes,
// counts, durations, and the commit.
func (record *Record) Validate() error {
	if !commitPattern.MatchString(record.Commit) {
		return errors.New("record commit must be a full lowercase hexadecimal SHA-1")
	}
	if record.Platform != unrecorded && !operatorTextPattern.MatchString(record.Platform) ||
		record.BrowserKeyring != unrecorded && !operatorTextPattern.MatchString(record.BrowserKeyring) {
		return errors.New("record platform text is malformed")
	}
	if record.Deployment != unrecorded && !slices.Contains(DeploymentClasses, record.Deployment) {
		return errors.New("record deployment class is not in the closed set")
	}
	if _, known := Lookup(record.Route.ID); !known {
		return errors.New("record route is not in the catalog")
	}
	for _, value := range append(append([]string(nil), record.Capabilities...), record.Degradations...) {
		if !featurePattern.MatchString(strings.TrimSuffix(value, " (lossy)")) {
			return errors.New("record capability or degradation is malformed")
		}
	}
	for _, step := range record.Steps {
		if !codePattern.MatchString(step.Operation) || step.Note != "" && !codePattern.MatchString(step.Note) {
			return errors.New("record step is not a fixed code")
		}
		switch step.Outcome {
		case OutcomePass, OutcomeDegraded, OutcomeFail, OutcomeUnobserved, OutcomeSkipped:
		default:
			return errors.New("record step outcome is unknown")
		}
	}
	switch record.Cleanup {
	case CleanupNotApplicable, CleanupConfirmed, CleanupUnconfirmed:
	default:
		return errors.New("record cleanup state is unknown")
	}
	return nil
}

// Markdown renders the record in the docs/evidence/README.md format.
func (record *Record) Markdown() (string, error) {
	if err := record.Validate(); err != nil {
		return "", err
	}
	var out strings.Builder
	line := func(format string, values ...any) {
		_, _ = fmt.Fprintf(&out, format+"\n\n", values...)
	}
	line("# %s live observation", record.Route.Name)
	out.WriteString("<!-- markdownlint-disable MD013 -->\n\n")
	line("Commit: `%s`", record.Commit)
	line("Release candidate or local build: local build from a clean checkout of that commit")
	line("Observation date: %s", record.Date.Format(time.DateOnly))
	line("OS and architecture: %s %s (%s)", record.GOOS, record.GOARCH, record.Platform)
	line("Browser/keyring family and version: %s", record.BrowserKeyring)
	line("Provider ID and broad deployment class: `%s`, %s",
		strings.Join(record.Route.Providers(), "`, `"), record.Deployment)
	operations := make([]string, 0, len(record.Steps))
	for _, step := range record.Steps {
		if step.Outcome != OutcomeSkipped {
			operations = append(operations, step.Operation)
		}
	}
	line("Capability or operation: %s", orNone(strings.Join(operations, ", ")))
	line("Content-free result stage: %s; cleanup %s", record.Result(), record.Cleanup)
	line("Harness: `%s` (`%s`), route `%s`", record.Route.Harness, record.Route.Test, record.Route.ID)
	scope := "read-only"
	if record.Write {
		scope = "reads and reversible synthetic writes"
	}
	line("Scope: %s", scope)
	line("Observed capabilities: %s", orNone(strings.Join(record.Capabilities, ", ")))
	line("Reported degradations: %s", orNone(strings.Join(record.Degradations, ", ")))
	out.WriteString("| Operation | Outcome | Items | Milliseconds | Note |\n")
	out.WriteString("| --- | --- | --- | --- | --- |\n")
	for _, step := range record.Steps {
		count, duration := "", ""
		if step.HasCount {
			count = strconv.Itoa(step.Count)
		}
		if step.Duration > 0 {
			duration = strconv.FormatInt(step.Duration.Round(10*time.Millisecond).Milliseconds(), 10)
		}
		_, _ = fmt.Fprintf(&out, "| `%s` | %s | %s | %s | %s |\n",
			step.Operation, step.Outcome, count, duration, step.Note)
	}
	out.WriteString("<!-- markdownlint-enable MD013 -->\n")
	return out.String(), nil
}

// SuggestedFileName is the docs/evidence/ file name for this record.
func (record *Record) SuggestedFileName() string {
	return fmt.Sprintf("%s-%s-%s-%s.md",
		record.Route.ID, record.GOOS, record.GOARCH, record.Date.Format(time.DateOnly))
}

// WriteFile writes the rendered record to a new private file.
func (record *Record) WriteFile(path string) error {
	rendered, err := record.Markdown()
	if err != nil {
		return err
	}
	// #nosec G304 -- the operator supplied this absolute output path.
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := file.WriteString(rendered)
	return errors.Join(writeErr, file.Close())
}

// Classify reduces an error to a fixed class. Provider error text can carry
// mailbox or tenant details, so it is never recorded.
func Classify(err error) string {
	var authentication *application.AuthenticationActionError
	var status *restapi.StatusError
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, application.ErrDenied):
		return "policy-denied"
	case errors.Is(err, application.ErrWriteOutcomeUnknown):
		return "write-outcome-unknown"
	case errors.As(err, &authentication):
		code := "authentication-required"
		if candidate := code + ":" + string(authentication.Action.Code); codePattern.MatchString(candidate) {
			code = candidate
		}
		return code
	case errors.As(err, &status) && status.Status >= 100 && status.Status <= 599:
		return fmt.Sprintf("http-%d", status.Status)
	default:
		return "error"
	}
}

// SourceRevision returns the checked-out commit and refuses a working tree
// with modified tracked files, because the record must name exactly the code
// that ran.
func SourceRevision(ctx context.Context) (string, error) {
	head, err := exec.CommandContext(ctx, "git", "rev-parse", "HEAD").Output()
	if err != nil {
		return "", fmt.Errorf("read the checked-out commit: %w", err)
	}
	commit := string(bytes.TrimSpace(head))
	if !commitPattern.MatchString(commit) {
		return "", errors.New("the checked-out commit is not a full SHA-1")
	}
	status, err := exec.CommandContext(ctx, "git", "status", "--porcelain", "--untracked-files=no").Output()
	if err != nil {
		return "", fmt.Errorf("inspect the working tree: %w", err)
	}
	if len(bytes.TrimSpace(status)) != 0 {
		return "", errors.New("tracked files are modified; commit or discard them so the record names the code that ran")
	}
	return commit, nil
}

func orNone(value string) string {
	if value == "" {
		return "none"
	}
	return value
}
