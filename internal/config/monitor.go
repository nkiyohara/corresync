package config

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/nkiyohara/corresync/internal/domain"
)

const (
	defaultMonitorPoll      = time.Minute
	defaultMonitorDebounce  = 30 * time.Second
	defaultMonitorRetention = 30 * 24 * time.Hour
	defaultMonitorRate      = 30
	defaultRunnerTimeout    = 2 * time.Minute
)

var (
	monitorDomainPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]{0,251}[a-z0-9])?$`)
	quietTimePattern     = regexp.MustCompile(`^(?:[01][0-9]|2[0-3]):[0-5][0-9]$`)
)

var monitorFields = []string{
	"account",
	"event_id",
	"has_attachments",
	"importance",
	"received_at",
	"sender",
	"subject",
	"trust",
}

// NewMonitor returns bounded defaults for an explicit enable operation.
func NewMonitor(mode domain.MonitorMode) Monitor {
	monitor := Monitor{
		Mode:          mode,
		PollInterval:  Duration(defaultMonitorPoll),
		Debounce:      Duration(defaultMonitorDebounce),
		Retention:     Duration(defaultMonitorRetention),
		RateLimitHour: defaultMonitorRate,
	}
	if mode == domain.MonitorNotify {
		monitor.Notification = &Notification{
			Adapter: "desktop",
			Fields: []string{
				"account", "event_id", "sender", "subject", "received_at", "trust",
			},
		}
	}
	return monitor
}

// NewRunner returns an explicit runner disclosure with conservative defaults.
func NewRunner(command string, arguments, fields []string, egress string, approved bool) Runner {
	return Runner{
		Command: command, Arguments: arguments, Egress: egress,
		ApproveRemote: approved, Fields: fields, Timeout: Duration(defaultRunnerTimeout),
	}
}

func (monitor Monitor) validate() error {
	if err := monitor.Mode.Validate(); err != nil {
		return err
	}
	if monitor.Mode == domain.MonitorOff {
		return errors.New("store disabled monitoring by omitting the monitor table")
	}
	if duration := time.Duration(monitor.PollInterval); duration < 15*time.Second ||
		duration > time.Hour {
		return errors.New("poll_interval must be between 15 seconds and 1 hour")
	}
	if duration := time.Duration(monitor.Debounce); duration < 0 ||
		duration > 15*time.Minute {
		return errors.New("debounce must be between 0 and 15 minutes")
	}
	if duration := time.Duration(monitor.Retention); duration < time.Hour ||
		duration > 90*24*time.Hour {
		return errors.New("retention must be between 1 hour and 90 days")
	}
	if monitor.RateLimitHour < 1 || monitor.RateLimitHour > 1000 {
		return errors.New("rate_limit_hour must be between 1 and 1000")
	}
	if monitor.QuietHours != nil {
		if err := monitor.QuietHours.validate(); err != nil {
			return err
		}
	}
	if err := monitor.Filter.validate(); err != nil {
		return err
	}
	switch monitor.Mode {
	case domain.MonitorOff:
		return errors.New("store disabled monitoring by omitting the monitor table")
	case domain.MonitorNotify:
		if monitor.Notification == nil {
			return errors.New("notify mode requires a notification adapter")
		}
		if err := monitor.Notification.validate(); err != nil {
			return err
		}
		if monitor.Runner != nil {
			return errors.New("notify mode cannot configure a runner")
		}
	case domain.MonitorQueue:
		if monitor.Notification != nil || monitor.Runner != nil {
			return errors.New("queue mode cannot configure a notification or runner")
		}
	case domain.MonitorAgent:
		if monitor.Notification != nil {
			return errors.New("agent mode cannot configure a notification adapter")
		}
		if monitor.Runner == nil {
			return errors.New("agent mode requires an explicit runner")
		}
		if err := monitor.Runner.validate(); err != nil {
			return err
		}
	}
	return nil
}

func (filter MonitorFilter) validate() error {
	if len(filter.SenderDomains) > 32 || len(filter.SubjectContains) > 32 {
		return errors.New("monitor filters may contain at most 32 values")
	}
	if hasDuplicates(filter.SenderDomains) || hasDuplicates(filter.SubjectContains) {
		return errors.New("monitor filter values must be unique")
	}
	for _, value := range filter.SenderDomains {
		if value != strings.ToLower(value) || len(value) > 253 ||
			!monitorDomainPattern.MatchString(value) || !strings.Contains(value, ".") {
			return fmt.Errorf("invalid sender domain %q", value)
		}
	}
	for _, value := range filter.SubjectContains {
		if value == "" || len(value) > 128 || strings.TrimSpace(value) != value ||
			strings.ContainsAny(value, "\r\n\x00") {
			return errors.New("subject filter must be 1 through 128 safe characters")
		}
	}
	return nil
}

func (quiet QuietHours) validate() error {
	if !quietTimePattern.MatchString(quiet.Start) ||
		!quietTimePattern.MatchString(quiet.End) ||
		quiet.Start == quiet.End {
		return errors.New("quiet hours require distinct HH:MM start and end values")
	}
	if len(quiet.TimeZone) > 128 || strings.TrimSpace(quiet.TimeZone) != quiet.TimeZone ||
		strings.ContainsAny(quiet.TimeZone, "\r\n\x00") {
		return errors.New("invalid quiet-hours time zone")
	}
	if _, err := time.LoadLocation(quiet.TimeZone); err != nil {
		return fmt.Errorf("load quiet-hours time zone: %w", err)
	}
	return nil
}

func (notification Notification) validate() error {
	if notification.Adapter != "desktop" {
		return errors.New("notification adapter must be desktop")
	}
	if len(notification.Fields) == 0 {
		return errors.New("notification fields must not be empty")
	}
	if err := validateMonitorFields(notification.Fields); err != nil {
		return err
	}
	if !slices.Contains(notification.Fields, "trust") {
		return errors.New("notification fields must include trust")
	}
	return nil
}

func (runner Runner) validate() error {
	if runner.Command == "" || !filepath.IsAbs(runner.Command) ||
		strings.TrimSpace(runner.Command) != runner.Command ||
		strings.ContainsAny(runner.Command, "\r\n\x00") {
		return errors.New("runner command must be an absolute safe path")
	}
	if len(runner.Arguments) > 32 {
		return errors.New("runner may have at most 32 arguments")
	}
	for _, argument := range runner.Arguments {
		if len(argument) > 4096 || strings.ContainsAny(argument, "\r\n\x00") {
			return errors.New("runner argument is malformed")
		}
	}
	switch runner.Egress {
	case "local":
		if runner.ApproveRemote {
			return errors.New("local runner egress cannot approve remote disclosure")
		}
	case "remote":
		if !runner.ApproveRemote {
			return errors.New("remote runner egress requires approve_remote")
		}
	default:
		return errors.New("runner egress must be local or remote")
	}
	if len(runner.Fields) == 0 {
		return errors.New("runner fields must not be empty")
	}
	if err := validateMonitorFields(runner.Fields); err != nil {
		return err
	}
	if !slices.Contains(runner.Fields, "event_id") ||
		!slices.Contains(runner.Fields, "trust") {
		return errors.New("runner fields must include event_id and trust")
	}
	if duration := time.Duration(runner.Timeout); duration < time.Second ||
		duration > 5*time.Minute {
		return errors.New("runner timeout must be between 1 second and 5 minutes")
	}
	return nil
}

func validateMonitorFields(fields []string) error {
	if len(fields) > len(monitorFields) || hasDuplicates(fields) {
		return errors.New("monitor fields must be unique supported metadata fields")
	}
	for _, field := range fields {
		if !slices.Contains(monitorFields, field) {
			return fmt.Errorf("unsupported monitor field %q", field)
		}
	}
	return nil
}

func hasDuplicates(values []string) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			return true
		}
		seen[value] = struct{}{}
	}
	return false
}
