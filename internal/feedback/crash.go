package feedback

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	crashRecordVersion = 1
	maximumCrashFrames = 32
)

var (
	crashIDPattern       = regexp.MustCompile(`^local-crash-[0-9a-f]{24}$`)
	crashFunctionPattern = regexp.MustCompile(`^[A-Za-z0-9_./*()-]{1,192}$`)
)

// CrashRecord is one replace-in-place, content-free panic record. Panic values,
// errors, arguments, request data, identifiers, and filesystem paths are not
// representable.
type CrashRecord struct {
	Version     int          `json:"version"`
	ID          string       `json:"id"`
	RecordedAt  time.Time    `json:"recorded_at"`
	ProcessRole string       `json:"process_role"`
	Boundary    string       `json:"boundary"`
	Build       Build        `json:"build"`
	Frames      []CrashFrame `json:"frames"`
}

// CrashFrame retains only a compiled source symbol and line number. Source
// paths and runtime argument values are deliberately omitted.
type CrashFrame struct {
	Function string `json:"function"`
	Line     int    `json:"line"`
}

// NewCrashRecord reduces a recovered panic stack to a strict allowlist.
func NewCrashRecord(
	build Build,
	processRole string,
	boundary string,
	recordedAt time.Time,
	callers []uintptr,
) CrashRecord {
	record := CrashRecord{
		Version:     crashRecordVersion,
		RecordedAt:  recordedAt.UTC().Truncate(time.Second),
		ProcessRole: sanitizeProcessRole(processRole),
		Boundary:    sanitizeCrashBoundary(boundary),
		Build:       sanitizeBuild(build),
		Frames:      crashFrames(callers),
	}
	record.ID = crashRecordID(record)
	return record
}

func crashFrames(callers []uintptr) []CrashFrame {
	frames := runtime.CallersFrames(callers)
	result := make([]CrashFrame, 0, maximumCrashFrames)
	for len(result) < maximumCrashFrames {
		frame, more := frames.Next()
		function := strings.TrimSpace(frame.Function)
		if generic := strings.IndexByte(function, '['); generic >= 0 {
			function = function[:generic]
		}
		if allowedCrashFunction(function) && frame.Line > 0 && frame.Line <= 10_000_000 {
			result = append(result, CrashFrame{Function: function, Line: frame.Line})
		}
		if !more {
			break
		}
	}
	return result
}

func allowedCrashFunction(function string) bool {
	return crashFunctionPattern.MatchString(function) &&
		(strings.HasPrefix(function, "main.") ||
			strings.HasPrefix(function, "github.com/nkiyohara/corresync/"))
}

func sanitizeProcessRole(role string) string {
	switch role {
	case "cli", "daemon", "mcp":
		return role
	default:
		return "cli"
	}
}

func sanitizeCrashBoundary(boundary string) string {
	switch boundary {
	case "process", "daemon_request", "daemon_server", "monitor", "background_work":
		return boundary
	default:
		return "process"
	}
}

func crashRecordID(record CrashRecord) string {
	digest := sha256.New()
	_, _ = digest.Write([]byte(record.ProcessRole))
	_, _ = digest.Write([]byte{0})
	_, _ = digest.Write([]byte(record.Boundary))
	_, _ = digest.Write([]byte{0})
	_, _ = digest.Write([]byte(record.Build.Version))
	_, _ = digest.Write([]byte{0})
	_, _ = digest.Write([]byte(record.Build.Commit))
	for _, frame := range record.Frames {
		_, _ = digest.Write([]byte{0})
		_, _ = digest.Write([]byte(frame.Function))
		_, _ = digest.Write([]byte{0})
		_, _ = digest.Write([]byte(strconv.Itoa(frame.Line)))
	}
	return "local-crash-" + hex.EncodeToString(digest.Sum(nil)[:12])
}

func (record CrashRecord) validate() error {
	if record.Version != crashRecordVersion || !crashIDPattern.MatchString(record.ID) ||
		record.RecordedAt.IsZero() || record.RecordedAt.Location() != time.UTC ||
		!record.RecordedAt.Equal(record.RecordedAt.Truncate(time.Second)) ||
		record.ProcessRole != sanitizeProcessRole(record.ProcessRole) ||
		record.Boundary != sanitizeCrashBoundary(record.Boundary) ||
		len(record.Frames) == 0 || len(record.Frames) > maximumCrashFrames {
		return errors.New("invalid local crash record")
	}
	sanitizedBuild := sanitizeBuild(record.Build)
	if sanitizedBuild != record.Build {
		return errors.New("invalid local crash build")
	}
	for _, frame := range record.Frames {
		if !allowedCrashFunction(frame.Function) || frame.Line < 1 || frame.Line > 10_000_000 {
			return errors.New("invalid local crash frame")
		}
	}
	if record.ID != crashRecordID(record) {
		return errors.New("invalid local crash identifier")
	}
	return nil
}
