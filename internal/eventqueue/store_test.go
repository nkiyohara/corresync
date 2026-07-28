package eventqueue

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/domain"
)

const (
	testAccountA domain.AccountID = "acc_00000000000000000000000000000001"
	testAccountB domain.AccountID = "acc_00000000000000000000000000000002"
)

func TestCommitScanRecoversDurablyAndIdentifiesDuplicates(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	store := NewAt(root)
	start := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)

	baseline := testScan(testAccountA, start, true, "baseline", []application.MonitorDetection{
		testDetection(testAccountA, "old", true),
	})
	result, err := store.CommitScan(t.Context(), baseline)
	if err != nil {
		t.Fatalf("bootstrap CommitScan() error = %v", err)
	}
	if len(result.Events) != 0 {
		t.Fatalf("bootstrap enqueued historical events: %+v", result.Events)
	}

	next := testScan(testAccountA, start.Add(time.Minute), false, "next", []application.MonitorDetection{
		testDetection(testAccountA, "new", true),
		testDetection(testAccountA, "ignored", false),
	})
	result, err = store.CommitScan(t.Context(), next)
	if err != nil {
		t.Fatalf("new CommitScan() error = %v", err)
	}
	if len(result.Events) != 1 || result.Events[0].SourceObjectID != "new" ||
		result.Events[0].Trust != application.MonitorTrustMarker {
		t.Fatalf("new events = %+v", result.Events)
	}

	duplicate := testScan(
		testAccountA,
		start.Add(2*time.Minute),
		false,
		"duplicate",
		[]application.MonitorDetection{testDetection(testAccountA, "new", true)},
	)
	result, err = store.CommitScan(t.Context(), duplicate)
	if err != nil {
		t.Fatalf("duplicate CommitScan() error = %v", err)
	}
	if result.Duplicates != 1 || len(result.Events) != 0 {
		t.Fatalf("duplicate result = %+v", result)
	}

	reloaded := NewAt(root)
	page, err := reloaded.List(t.Context(), application.MonitorEventListInput{
		Account: testAccountA, Limit: 50,
	})
	if err != nil {
		t.Fatalf("reloaded List() error = %v", err)
	}
	if len(page.Events) != 1 || page.Events[0].DeliveryCount != 2 {
		t.Fatalf("reloaded events = %+v", page.Events)
	}
	status, err := reloaded.Status(t.Context(), testAccountA)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Initialized || status.Cursor != cursor("duplicate") ||
		status.Pending != 1 {
		t.Fatalf("reloaded status = %+v", status)
	}

	if runtime.GOOS != "windows" {
		path := filepath.Join(root, string(testAccountA), "state.json")
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("monitor state permissions = %o, want 600", info.Mode().Perm())
		}
	}
}

func TestAcknowledgeIsIdempotentAndPurgeIsAccountScoped(t *testing.T) {
	t.Parallel()
	store := NewAt(t.TempDir())
	start := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	for _, account := range []domain.AccountID{testAccountA, testAccountB} {
		if _, err := store.CommitScan(
			t.Context(),
			testScan(account, start, true, "baseline", nil),
		); err != nil {
			t.Fatal(err)
		}
		if _, err := store.CommitScan(
			t.Context(),
			testScan(
				account,
				start.Add(time.Minute),
				false,
				"next",
				[]application.MonitorDetection{testDetection(account, "message", true)},
			),
		); err != nil {
			t.Fatal(err)
		}
	}
	page, err := store.List(t.Context(), application.MonitorEventListInput{
		Account: testAccountA, Limit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	input := application.MonitorAcknowledgeInput{
		Account: testAccountA, EventID: page.Events[0].ID,
	}
	acknowledgedAt := start.Add(2 * time.Minute)
	first, err := store.Acknowledge(t.Context(), input, acknowledgedAt)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Acknowledge(
		t.Context(),
		input,
		acknowledgedAt.Add(time.Hour),
	)
	if err != nil {
		t.Fatal(err)
	}
	if first.AcknowledgedAt == nil || second.AcknowledgedAt == nil ||
		!first.AcknowledgedAt.Equal(*second.AcknowledgedAt) {
		t.Fatalf("idempotent acknowledgement changed timestamp: %+v, %+v", first, second)
	}
	purged, err := store.Purge(t.Context(), testAccountA)
	if err != nil || purged != 1 {
		t.Fatalf("Purge() = %d, %v", purged, err)
	}
	other, err := store.List(t.Context(), application.MonitorEventListInput{
		Account: testAccountB, Limit: 10,
	})
	if err != nil || len(other.Events) != 1 {
		t.Fatalf("other account queue changed: %+v, %v", other, err)
	}
}

func TestCircuitBreakerOpensAfterThreeRunnerFailures(t *testing.T) {
	t.Parallel()
	store := NewAt(t.TempDir())
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	for range 3 {
		if err := store.MarkDispatchFailure(
			t.Context(),
			testAccountA,
			now,
			"runner_failed",
		); err != nil {
			t.Fatal(err)
		}
	}
	status, err := store.Status(t.Context(), testAccountA)
	if err != nil {
		t.Fatal(err)
	}
	if status.DispatchFailures != 3 || status.CircuitOpenUntil == nil ||
		!status.CircuitOpenUntil.Equal(now.Add(15*time.Minute)) {
		t.Fatalf("status = %+v", status)
	}
}

func TestRetentionPrunesOnlyAcknowledgedEvents(t *testing.T) {
	t.Parallel()
	store := NewAt(t.TempDir())
	start := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	if _, err := store.CommitScan(
		t.Context(),
		testScan(testAccountA, start, true, "baseline", nil),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CommitScan(
		t.Context(),
		testScan(
			testAccountA,
			start.Add(time.Minute),
			false,
			"one",
			[]application.MonitorDetection{testDetection(testAccountA, "one", true)},
		),
	); err != nil {
		t.Fatal(err)
	}
	page, err := store.List(t.Context(), application.MonitorEventListInput{
		Account: testAccountA, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Acknowledge(
		t.Context(),
		application.MonitorAcknowledgeInput{
			Account: testAccountA, EventID: page.Events[0].ID,
		},
		start.Add(2*time.Minute),
	); err != nil {
		t.Fatal(err)
	}
	later := start.Add(48 * time.Hour)
	scan := testScan(testAccountA, later, false, "later", nil)
	scan.RetainAfter = later.Add(-24 * time.Hour)
	if _, err := store.CommitScan(t.Context(), scan); err != nil {
		t.Fatal(err)
	}
	page, err = store.List(t.Context(), application.MonitorEventListInput{
		Account: testAccountA, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 0 {
		t.Fatalf("expired acknowledged events remained: %+v", page.Events)
	}
}

func TestStoreRejectsSymlinkState(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink privilege is platform-specific")
	}
	t.Parallel()
	root := t.TempDir()
	directory := filepath.Join(root, string(testAccountA))
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "outside.json")
	if err := os.WriteFile(target, []byte(`{"schemaVersion":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(directory, "state.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := NewAt(root).Status(t.Context(), testAccountA); err == nil {
		t.Fatal("Status() unexpectedly followed a monitor state symlink")
	}
}

func TestCommitScanRejectsNULInNotificationMetadata(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	for _, mutate := range []func(*application.MonitorDetection){
		func(detection *application.MonitorDetection) {
			detection.Subject = "unsafe\x00subject"
		},
		func(detection *application.MonitorDetection) {
			detection.Sender.Name = "unsafe\x00sender"
		},
		func(detection *application.MonitorDetection) {
			detection.Sender.Address = "unsafe\x00@example.invalid"
		},
	} {
		detection := testDetection(testAccountA, "message", true)
		mutate(&detection)
		if _, err := NewAt(t.TempDir()).CommitScan(
			t.Context(),
			testScan(
				testAccountA,
				now,
				false,
				"unsafe",
				[]application.MonitorDetection{detection},
			),
		); err == nil {
			t.Fatal("CommitScan() accepted NUL notification metadata")
		}
	}
}

func testScan(
	account domain.AccountID,
	now time.Time,
	bootstrap bool,
	cursorValue string,
	detections []application.MonitorDetection,
) application.MonitorScan {
	return application.MonitorScan{
		Account: account, Cursor: cursor(cursorValue), Bootstrap: bootstrap,
		Queue: true, ObservedAt: now, RetainAfter: now.Add(-24 * time.Hour),
		Detections: detections,
	}
}

func testDetection(
	account domain.AccountID,
	source string,
	matched bool,
) application.MonitorDetection {
	reason := "subject"
	if matched {
		reason = "matched"
	}
	return application.MonitorDetection{
		Account: account, AccountAlias: "synthetic",
		Provider: domain.ProviderJMAP, SourceObjectID: source,
		Sender:  application.MailAddress{Address: "sender@example.invalid"},
		Subject: "Synthetic subject", ReceivedAt: "2026-07-28T12:00:00Z",
		Matched: matched, FilterReason: reason,
	}
}

func cursor(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}
