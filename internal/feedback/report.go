package feedback

import (
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	manualReportVersion = 2
	maximumReportBytes  = 16 << 10
)

var (
	versionPattern = regexp.MustCompile(
		`^(?:dev|v?[0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.-]+)?)$`,
	)
	commitPattern    = regexp.MustCompile(`^(?:none|[0-9a-f]{7,64})$`)
	goVersionPattern = regexp.MustCompile(`^go[0-9]+\.[0-9]+(?:\.[0-9]+)?(?:[a-z0-9.-]+)?$`)
)

// Build contains bounded public build metadata.
type Build struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildDate string `json:"build_date"`
	GoVersion string `json:"go_version"`
	Platform  string `json:"platform"`
}

// ConfigStatus describes validation without exposing configuration values.
type ConfigStatus struct {
	Status        string `json:"status"`
	Reason        string `json:"reason,omitempty"`
	SchemaVersion int    `json:"schema_version,omitempty"`
}

// Provider summarizes configured service routes without account identifiers.
type Provider struct {
	ID           string   `json:"id"`
	Capabilities []string `json:"capabilities"`
}

// LastErrorStatus is either omitted by choice, absent, degraded, or sanitized.
type LastErrorStatus struct {
	Status  string        `json:"status"`
	Reason  string        `json:"reason,omitempty"`
	ID      string        `json:"id,omitempty"`
	Command *CommandShape `json:"command,omitempty"`
	Classes []string      `json:"classes,omitempty"`
}

// LastCrashStatus is either omitted by choice, absent, degraded, or a
// content-free panic record.
type LastCrashStatus struct {
	Status      string       `json:"status"`
	Reason      string       `json:"reason,omitempty"`
	ID          string       `json:"id,omitempty"`
	RecordedAt  string       `json:"recorded_at,omitempty"`
	ProcessRole string       `json:"process_role,omitempty"`
	Boundary    string       `json:"boundary,omitempty"`
	Build       *Build       `json:"build,omitempty"`
	Frames      []CrashFrame `json:"frames,omitempty"`
}

// Input consists only of values already reduced to public or allowlisted data.
type Input struct {
	Build          Build
	InstallMethod  string
	Config         ConfigStatus
	Providers      []Provider
	ProviderReason string
	LastError      LastErrorStatus
	LastCrash      LastCrashStatus
}

// Report is the deterministic, complete review artifact.
type Report struct {
	SchemaVersion int              `json:"schema_version"`
	Privacy       PrivacyStatement `json:"privacy"`
	Build         Build            `json:"build"`
	Installation  SectionValue     `json:"installation"`
	Config        ConfigStatus     `json:"config"`
	Providers     ProviderSection  `json:"providers"`
	LastError     LastErrorStatus  `json:"last_error"`
	LastCrash     LastCrashStatus  `json:"last_crash"`
}

// PrivacyStatement documents exactly what producing the report did.
type PrivacyStatement struct {
	Generation             string `json:"generation"`
	AutomaticUpload        bool   `json:"automatic_upload"`
	ContentIncluded        bool   `json:"mail_or_calendar_content_included"`
	TaskContentIncluded    bool   `json:"task_content_included"`
	MessageContentIncluded bool   `json:"message_content_included"`
	RawPanicIncluded       bool   `json:"raw_panic_included"`
}

// SectionValue reports one bounded scalar collection result.
type SectionValue struct {
	Status string `json:"status"`
	Value  string `json:"value,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// ProviderSection reports a provider aggregate or its collection degradation.
type ProviderSection struct {
	Status string     `json:"status"`
	Reason string     `json:"reason,omitempty"`
	Items  []Provider `json:"items"`
}

// Generate serializes a stable report. Invalid section inputs are replaced by
// visible degradation markers rather than reflected into output.
func Generate(input Input) ([]byte, error) {
	report := Report{
		SchemaVersion: manualReportVersion,
		Privacy: PrivacyStatement{
			Generation:      "local-only",
			AutomaticUpload: false,
			ContentIncluded: false,
		},
		Build: sanitizeBuild(input.Build),
		Installation: sanitizeSectionValue(
			SectionValue{Status: "ok", Value: input.InstallMethod},
		),
		Config:    sanitizeConfig(input.Config),
		Providers: sanitizeProviders(input.Providers, input.ProviderReason),
		LastError: sanitizeLastError(input.LastError),
		LastCrash: sanitizeLastCrash(input.LastCrash),
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return nil, err
	}
	encoded = append(encoded, '\n')
	if len(encoded) > maximumReportBytes {
		return nil, errors.New("redacted feedback report exceeds its size limit")
	}
	return encoded, nil
}

func sanitizeBuild(build Build) Build {
	return Build{
		Version:   sanitizePattern(build.Version, versionPattern),
		Commit:    sanitizePattern(build.Commit, commitPattern),
		BuildDate: sanitizeBuildDate(build.BuildDate),
		GoVersion: sanitizePattern(build.GoVersion, goVersionPattern),
		Platform:  sanitizePlatform(build.Platform),
	}
}

func sanitizePattern(value string, pattern *regexp.Regexp) string {
	if pattern.MatchString(value) {
		return value
	}
	return "unavailable"
}

func sanitizeBuildDate(value string) string {
	if value == "unknown" {
		return value
	}
	if _, err := time.Parse(time.RFC3339, value); err == nil {
		return value
	}
	return "unavailable"
}

func sanitizePlatform(value string) string {
	goos, goarch, found := strings.Cut(value, "/")
	if !found {
		return "unavailable"
	}
	allowedOS := map[string]struct{}{
		"aix": {}, "darwin": {}, "dragonfly": {}, "freebsd": {}, "illumos": {},
		"linux": {}, "netbsd": {}, "openbsd": {}, "plan9": {}, "solaris": {},
		"windows": {},
	}
	allowedArch := map[string]struct{}{
		"386": {}, "amd64": {}, "arm": {}, "arm64": {}, "loong64": {},
		"mips": {}, "mips64": {}, "mips64le": {}, "mipsle": {}, "ppc64": {},
		"ppc64le": {}, "riscv64": {}, "s390x": {}, "wasm": {},
	}
	if _, ok := allowedOS[goos]; !ok {
		return "unavailable"
	}
	if _, ok := allowedArch[goarch]; !ok {
		return "unavailable"
	}
	return value
}

func sanitizeSectionValue(section SectionValue) SectionValue {
	if section.Status == "ok" && allowedInstallMethod(section.Value) {
		return section
	}
	return SectionValue{Status: "degraded", Reason: "collection_failed"}
}

func sanitizeConfig(status ConfigStatus) ConfigStatus {
	switch status.Status {
	case "ok":
		if status.SchemaVersion > 0 && status.SchemaVersion <= 1_000 {
			return ConfigStatus{Status: "ok", SchemaVersion: status.SchemaVersion}
		}
	case "degraded":
		if allowedReason(status.Reason) {
			return ConfigStatus{Status: "degraded", Reason: status.Reason}
		}
	}
	return ConfigStatus{Status: "degraded", Reason: "collection_failed"}
}

func sanitizeProviders(providers []Provider, collectionReason string) ProviderSection {
	if collectionReason != "" {
		if !allowedReason(collectionReason) {
			collectionReason = "collection_failed"
		}
		return ProviderSection{
			Status: "degraded",
			Reason: collectionReason,
			Items:  []Provider{},
		}
	}
	if len(providers) > 16 {
		return ProviderSection{
			Status: "degraded",
			Reason: "collection_bounded",
			Items:  []Provider{},
		}
	}
	sanitized := make([]Provider, len(providers))
	for index, provider := range providers {
		sanitized[index] = Provider{
			ID:           provider.ID,
			Capabilities: append([]string(nil), provider.Capabilities...),
		}
	}
	sort.Slice(sanitized, func(left, right int) bool {
		return sanitized[left].ID < sanitized[right].ID
	})
	previous := ""
	for index := range sanitized {
		provider := &sanitized[index]
		if !allowedProvider(provider.ID) ||
			provider.ID <= previous ||
			len(provider.Capabilities) == 0 ||
			len(provider.Capabilities) > 8 {
			return ProviderSection{
				Status: "degraded",
				Reason: "collection_failed",
				Items:  []Provider{},
			}
		}
		previous = provider.ID
		sort.Strings(provider.Capabilities)
		previousCapability := ""
		for _, capability := range provider.Capabilities {
			if !allowedCapability(capability) || capability <= previousCapability {
				return ProviderSection{
					Status: "degraded",
					Reason: "collection_failed",
					Items:  []Provider{},
				}
			}
			previousCapability = capability
		}
	}
	return ProviderSection{Status: "ok", Items: sanitized}
}

func sanitizeLastError(status LastErrorStatus) LastErrorStatus {
	switch status.Status {
	case "not-requested", "absent":
		return LastErrorStatus{Status: status.Status}
	case "degraded":
		if allowedReason(status.Reason) {
			return LastErrorStatus{Status: "degraded", Reason: status.Reason}
		}
	case "ok":
		if status.Command == nil {
			break
		}
		record := ErrorRecord{
			Version: recordVersion,
			ID:      status.ID,
			Command: *status.Command,
			Classes: status.Classes,
		}
		if record.validate() == nil {
			return LastErrorStatus{
				Status:  "ok",
				ID:      record.ID,
				Command: &record.Command,
				Classes: record.Classes,
			}
		}
	}
	return LastErrorStatus{Status: "degraded", Reason: "collection_failed"}
}

func sanitizeLastCrash(status LastCrashStatus) LastCrashStatus {
	switch status.Status {
	case "not-requested", "absent":
		return LastCrashStatus{Status: status.Status}
	case "degraded":
		if allowedReason(status.Reason) {
			return LastCrashStatus{Status: "degraded", Reason: status.Reason}
		}
	case "ok":
		if status.Build == nil {
			break
		}
		recordedAt, err := time.Parse(time.RFC3339, status.RecordedAt)
		if err != nil {
			break
		}
		record := CrashRecord{
			Version: crashRecordVersion, ID: status.ID, RecordedAt: recordedAt,
			ProcessRole: status.ProcessRole, Boundary: status.Boundary,
			Build: *status.Build, Frames: append([]CrashFrame(nil), status.Frames...),
		}
		if record.validate() == nil {
			return LastCrashStatus{
				Status: "ok", ID: record.ID, RecordedAt: record.RecordedAt.Format(time.RFC3339),
				ProcessRole: record.ProcessRole, Boundary: record.Boundary,
				Build: &record.Build, Frames: record.Frames,
			}
		}
	}
	return LastCrashStatus{Status: "degraded", Reason: "collection_failed"}
}

func allowedReason(reason string) bool {
	switch reason {
	case "collection_bounded", "collection_failed", "config_invalid", "config_missing",
		"diagnostic_invalid", "state_unavailable":
		return true
	default:
		return false
	}
}

func allowedCapability(capability string) bool {
	switch capability {
	case "calendar", "mail", "tasks":
		return true
	default:
		return false
	}
}

func allowedInstallMethod(method string) bool {
	switch method {
	case "apk", "deb", "direct", "homebrew", "rpm", "scoop", "winget":
		return true
	default:
		return false
	}
}

func allowedProvider(provider string) bool {
	switch provider {
	case "anydo-mcp", "apple-reminders", "caldav", "google", "google-tasks",
		"google-web", "imap-smtp", "jmap", "microsoft-graph", "microsoft-owa",
		"microsoft-web-tasks", "omnifocus", "things", "ticktick", "todoist":
		return true
	default:
		return false
	}
}
