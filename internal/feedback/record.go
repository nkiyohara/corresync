// Package feedback builds bounded diagnostics that contain only explicitly
// allowlisted, content-free fields.
package feedback

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"net"
	"regexp"
	"sort"
	"strings"
)

const recordVersion = 1

const (
	maximumCommandBytes = 256
	maximumArguments    = 256
	maximumFlags        = 32
	maximumErrorDepth   = 16
)

var (
	commandShapePattern = regexp.MustCompile(
		`^corr(?: [a-z][a-z0-9-]{0,63}| <[a-z][a-z0-9-]{0,63}>)*$`,
	)
	longFlagPattern  = regexp.MustCompile(`^--[a-z][a-z0-9-]{0,63}$`)
	shortFlagPattern = regexp.MustCompile(`^-[A-Za-z0-9]$`)
	errorIDPattern   = regexp.MustCompile(`^local-[0-9a-f]{24}$`)
)

// ErrorRecord is the complete persisted diagnostic state. It deliberately has
// no timestamp, raw error, argument value, account identifier, or path.
type ErrorRecord struct {
	Version int          `json:"version"`
	ID      string       `json:"id"`
	Command CommandShape `json:"command"`
	Classes []string     `json:"classes"`
}

// CommandShape preserves useful command structure without argument values.
type CommandShape struct {
	Path            string   `json:"path"`
	Flags           []string `json:"flags"`
	ValuesRedacted  bool     `json:"valuesRedacted"`
	ArgumentsCapped bool     `json:"argumentsCapped,omitempty"`
}

// NewErrorRecord reduces an execution failure to a deterministic local record.
func NewErrorRecord(err error, command string, arguments []string) ErrorRecord {
	shape := commandShape(command, arguments)
	record := ErrorRecord{
		Version: recordVersion,
		Command: shape,
		Classes: classifyError(err),
	}
	record.ID = recordID(record)
	return record
}

func commandShape(command string, arguments []string) CommandShape {
	path := "corr"
	candidate := "corr " + strings.TrimSpace(command)
	if len(candidate) <= maximumCommandBytes && commandShapePattern.MatchString(candidate) {
		path = candidate
	}
	shape := CommandShape{
		Path:           path,
		Flags:          make([]string, 0, 8),
		ValuesRedacted: true,
	}
	seen := make(map[string]struct{})
	for index, argument := range arguments {
		if index >= maximumArguments {
			shape.ArgumentsCapped = true
			break
		}
		if argument == "--" {
			break
		}
		name, _, _ := strings.Cut(argument, "=")
		if !longFlagPattern.MatchString(name) && !shortFlagPattern.MatchString(name) {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		if len(shape.Flags) == maximumFlags {
			shape.ArgumentsCapped = true
			break
		}
		seen[name] = struct{}{}
		shape.Flags = append(shape.Flags, name)
	}
	sort.Strings(shape.Flags)
	return shape
}

func classifyError(err error) []string {
	classes := map[string]struct{}{"operation_failed": {}}
	queue := []error{err}
	for inspected := 0; len(queue) > 0 && inspected < maximumErrorDepth; inspected++ {
		current := queue[0]
		queue = queue[1:]
		if current == nil {
			continue
		}
		switch {
		case errors.Is(current, context.Canceled):
			classes["canceled"] = struct{}{}
		case errors.Is(current, context.DeadlineExceeded):
			classes["timeout"] = struct{}{}
		case errors.Is(current, fs.ErrNotExist):
			classes["not_found"] = struct{}{}
		case errors.Is(current, fs.ErrPermission):
			classes["permission_denied"] = struct{}{}
		default:
			// The queue explicitly walks each wrapper with a hard bound.
			if _, networkFailure := current.(net.Error); networkFailure { //nolint:errorlint
				classes["network_error"] = struct{}{}
			}
		}
		// This manual traversal is intentionally bounded and supports joined errors.
		switch wrapped := current.(type) { //nolint:errorlint
		case interface{ Unwrap() []error }:
			queue = append(queue, wrapped.Unwrap()...)
		case interface{ Unwrap() error }:
			queue = append(queue, wrapped.Unwrap())
		}
	}
	result := make([]string, 0, len(classes))
	for class := range classes {
		result = append(result, class)
	}
	sort.Strings(result)
	return result
}

func recordID(record ErrorRecord) string {
	var canonical strings.Builder
	canonical.WriteString(record.Command.Path)
	canonical.WriteByte(0)
	for _, flag := range record.Command.Flags {
		canonical.WriteString(flag)
		canonical.WriteByte(0)
	}
	for _, class := range record.Classes {
		canonical.WriteString(class)
		canonical.WriteByte(0)
	}
	if record.Command.ArgumentsCapped {
		canonical.WriteByte(1)
	}
	digest := sha256.Sum256([]byte(canonical.String()))
	return "local-" + hex.EncodeToString(digest[:12])
}

func (record ErrorRecord) validate() error {
	if record.Version != recordVersion {
		return errors.New("unsupported diagnostic record version")
	}
	if !errorIDPattern.MatchString(record.ID) {
		return errors.New("invalid local error identifier")
	}
	if !commandShapePattern.MatchString(record.Command.Path) ||
		len(record.Command.Path) > maximumCommandBytes ||
		!record.Command.ValuesRedacted ||
		len(record.Command.Flags) > maximumFlags {
		return errors.New("invalid redacted command shape")
	}
	previous := ""
	for _, flag := range record.Command.Flags {
		if (!longFlagPattern.MatchString(flag) && !shortFlagPattern.MatchString(flag)) ||
			flag <= previous {
			return errors.New("invalid redacted command flags")
		}
		previous = flag
	}
	if len(record.Classes) == 0 || len(record.Classes) > 8 {
		return errors.New("invalid sanitized error classes")
	}
	allowedClasses := map[string]struct{}{
		"canceled":          {},
		"network_error":     {},
		"not_found":         {},
		"operation_failed":  {},
		"permission_denied": {},
		"timeout":           {},
	}
	previous = ""
	for _, class := range record.Classes {
		if _, allowed := allowedClasses[class]; !allowed || class <= previous {
			return errors.New("invalid sanitized error class")
		}
		previous = class
	}
	if record.ID != recordID(record) {
		return errors.New("local error identifier does not match record")
	}
	return nil
}
