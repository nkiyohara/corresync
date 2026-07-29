package audit

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/domain"
)

type fixedClock struct{ now time.Time }

func (clock fixedClock) Now() time.Time { return clock.now }

func testEvent(t *testing.T) Event {
	t.Helper()
	operation, err := domain.NewOperation("mail.search", domain.EffectRead, "work", struct {
		Query string `json:"query"`
	}{Query: "synthetic"})
	if err != nil {
		t.Fatalf("NewOperation() error = %v", err)
	}
	return Event{
		Phase:     PhaseExecuted,
		Outcome:   OutcomeSuccess,
		Reason:    "completed",
		Caller:    domain.Caller{Surface: "cli", Instance: "process-1"},
		Operation: operation.View(),
	}
}

func TestFileRecorderWritesContentFreeJSONL(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "audit", "events.jsonl")
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	recorder, err := NewFileRecorder(path, Options{Clock: fixedClock{now: now}})
	if err != nil {
		t.Fatalf("NewFileRecorder() error = %v", err)
	}
	event := testEvent(t)
	if err := recorder.Record(context.Background(), event); err != nil {
		t.Fatalf("Record() error = %v", err)
	}
	if err := recorder.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	data, err := os.ReadFile(path) // #nosec G304 -- path is confined to t.TempDir.
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	for _, forbidden := range []string{"synthetic", "token", "password", "subject", "recipient"} {
		if strings.Contains(strings.ToLower(string(data)), forbidden) {
			t.Fatalf("audit log contains forbidden content %q: %s", forbidden, data)
		}
	}
	var got Event
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if got.SchemaVersion != 1 || got.ID == "" || !got.Timestamp.Equal(now) {
		t.Fatalf("incomplete recorded event: %+v", got)
	}
	if runtime.GOOS != "windows" {
		info, statErr := os.Stat(path)
		if statErr != nil {
			t.Fatalf("Stat() error = %v", statErr)
		}
		if gotPerm := info.Mode().Perm(); gotPerm != 0o600 {
			t.Fatalf("audit permissions = %o, want 600", gotPerm)
		}
	}
}

func TestFileRecorderSerializesConcurrentEvents(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "events.jsonl")
	recorder, err := NewFileRecorder(path, Options{})
	if err != nil {
		t.Fatalf("NewFileRecorder() error = %v", err)
	}
	t.Cleanup(func() {
		if closeErr := recorder.Close(); closeErr != nil {
			t.Errorf("Close() error = %v", closeErr)
		}
	})

	var wait sync.WaitGroup
	event := testEvent(t)
	for range 32 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if recordErr := recorder.Record(context.Background(), event); recordErr != nil {
				t.Errorf("Record() error = %v", recordErr)
			}
		}()
	}
	wait.Wait()

	file, err := os.Open(path) // #nosec G304 -- path is confined to t.TempDir.
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if closeErr := file.Close(); closeErr != nil {
			t.Errorf("Close() error = %v", closeErr)
		}
	})
	scanner := bufio.NewScanner(file)
	count := 0
	for scanner.Scan() {
		var event Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatalf("invalid JSON line: %v", err)
		}
		count++
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if count != 32 {
		t.Fatalf("event lines = %d, want 32", count)
	}
}

func TestFileRecorderAuditsMonitorDisclosureWithoutMailboxContent(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "events.jsonl")
	recorder, err := NewFileRecorder(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	event := testEvent(t)
	event.Monitor = &application.MonitorAudit{
		Stage: "runner", Filter: "matched",
		Fields:      []string{"event_id", "subject", "trust"},
		Destination: "runner_0123456789abcdef",
		Result:      "completed", Count: 2,
	}
	if err := recorder.Record(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path) // #nosec G304 -- synthetic private path.
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"destination":"runner_0123456789abcdef"`) ||
		!strings.Contains(string(data), `"fields":["event_id","subject","trust"]`) {
		t.Fatalf("monitor disclosure was not audited: %s", data)
	}
	for _, forbidden := range []string{"attacker@example", "ignore previous", "/home/person"} {
		if strings.Contains(strings.ToLower(string(data)), forbidden) {
			t.Fatalf("monitor audit leaked %q: %s", forbidden, data)
		}
	}
}

func TestFileRecorderRejectsInvalidEventsAndPaths(t *testing.T) {
	t.Parallel()

	directoryPath := filepath.Join(t.TempDir(), "events.jsonl")
	if err := os.Mkdir(directoryPath, 0o700); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	if _, err := NewFileRecorder(directoryPath, Options{}); err == nil {
		t.Fatal("NewFileRecorder() unexpectedly accepted directory path")
	}

	recorder, err := NewFileRecorder(filepath.Join(t.TempDir(), "events.jsonl"), Options{})
	if err != nil {
		t.Fatalf("NewFileRecorder() error = %v", err)
	}
	t.Cleanup(func() {
		if closeErr := recorder.Close(); closeErr != nil {
			t.Errorf("Close() error = %v", closeErr)
		}
	})
	for _, event := range []Event{
		{},
		func() Event { event := testEvent(t); event.Phase = "future"; return event }(),
		func() Event { event := testEvent(t); event.Outcome = "future"; return event }(),
		func() Event { event := testEvent(t); event.Reason = "message\nbody"; return event }(),
	} {
		if err := recorder.Record(context.Background(), event); err == nil {
			t.Fatalf("Record() unexpectedly accepted event: %+v", event)
		}
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := recorder.Record(cancelled, testEvent(t)); err == nil {
		t.Fatal("Record() unexpectedly accepted cancelled context")
	}
}
