package eventqueue

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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
	purgedState, _, err := store.load(testAccountA)
	if err != nil {
		t.Fatal(err)
	}
	if len(purgedState.Seen) != 0 {
		t.Fatalf("Purge() retained %d deduplication records", len(purgedState.Seen))
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

func TestMarkDispatchEnforcesTheMaximumHourlyHistory(t *testing.T) {
	t.Parallel()
	store := NewAt(t.TempDir())
	now := time.Now().UTC()
	if _, err := store.CommitScan(
		t.Context(),
		testScan(testAccountA, now.Add(-time.Minute), true, "baseline", nil),
	); err != nil {
		t.Fatal(err)
	}
	detections := make([]application.MonitorDetection, 0, application.MaxMonitorDispatchesPerHour)
	for index := range application.MaxMonitorDispatchesPerHour {
		detections = append(
			detections,
			testDetection(testAccountA, "message-"+time.Duration(index).String(), true),
		)
	}
	scan := testScan(testAccountA, now, false, "full", detections)
	scan.Delivery = application.MonitorDeliveryRunner
	result, err := store.CommitScan(t.Context(), scan)
	if err != nil {
		t.Fatal(err)
	}
	for offset := 0; offset < len(result.Events); offset += 100 {
		ids := make([]string, 0, 100)
		for _, event := range result.Events[offset : offset+100] {
			ids = append(ids, event.ID)
		}
		if err := store.MarkDispatch(
			t.Context(),
			testAccountA,
			application.MonitorDeliveryRunner,
			ids,
			now,
		); err != nil {
			t.Fatalf("MarkDispatch() error = %v", err)
		}
	}
	status, err := store.Status(t.Context(), testAccountA)
	if err != nil {
		t.Fatal(err)
	}
	if status.DispatchedLastHour != application.MaxMonitorDispatchesPerHour {
		t.Fatalf(
			"DispatchedLastHour = %d, want %d",
			status.DispatchedLastHour,
			application.MaxMonitorDispatchesPerHour,
		)
	}
	extraScan := testScan(
		testAccountA,
		now.Add(time.Minute),
		false,
		"extra",
		[]application.MonitorDetection{testDetection(testAccountA, "extra", true)},
	)
	extraScan.Delivery = application.MonitorDeliveryRunner
	extra, err := store.CommitScan(t.Context(), extraScan)
	if err != nil || len(extra.Events) != 1 {
		t.Fatalf("extra CommitScan() = %+v, %v", extra, err)
	}
	if err := store.MarkDispatch(
		t.Context(),
		testAccountA,
		application.MonitorDeliveryRunner,
		[]string{extra.Events[0].ID},
		now,
	); err == nil {
		t.Fatal("MarkDispatch() exceeded the hourly history bound")
	}
}

func TestNotificationOutboxKeepsCursorMonotonicAndEventsPending(t *testing.T) {
	t.Parallel()
	store := NewAt(t.TempDir())
	start := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	if _, err := store.CommitScan(
		t.Context(),
		testScan(testAccountA, start, true, "baseline", nil),
	); err != nil {
		t.Fatal(err)
	}
	next := testScan(
		testAccountA,
		start.Add(time.Minute),
		false,
		"next",
		[]application.MonitorDetection{testDetection(testAccountA, "message", true)},
	)
	next.Delivery = application.MonitorDeliveryNotification
	next.RecoveryOverflow = true
	first, err := store.CommitScan(t.Context(), next)
	if err != nil || len(first.Events) != 1 {
		t.Fatalf("first CommitScan() = %+v, %v", first, err)
	}
	status, err := store.Status(t.Context(), testAccountA)
	if err != nil {
		t.Fatal(err)
	}
	if status.Cursor != next.Cursor || status.Pending != 1 ||
		status.RecoveryOverflows != 1 ||
		status.LastRecoveryOverflow == nil {
		t.Fatalf("status = %+v, want committed cursor and pending event", status)
	}
	page, err := store.List(t.Context(), application.MonitorEventListInput{
		Account:  testAccountA,
		State:    "pending",
		Delivery: application.MonitorDeliveryNotification,
		Limit:    10,
	})
	if err != nil || len(page.Events) != 1 {
		t.Fatalf("notification pending page = %+v, %v", page, err)
	}
}

func TestLegacyEventsMigrateToLocalOnlyDelivery(t *testing.T) {
	t.Parallel()
	store := NewAt(t.TempDir())
	start := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
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
			"next",
			[]application.MonitorDetection{
				testDetection(testAccountA, "legacy-message", true),
			},
		),
	); err != nil {
		t.Fatal(err)
	}
	state, path, err := store.load(testAccountA)
	if err != nil {
		t.Fatal(err)
	}
	state.SchemaVersion = 1
	state.Events[0].Delivery = ""
	if err := store.save(path, state); err != nil {
		t.Fatal(err)
	}
	page, err := NewAt(filepath.Dir(filepath.Dir(path))).List(
		t.Context(),
		application.MonitorEventListInput{
			Account:  testAccountA,
			Delivery: application.MonitorDeliveryQueue,
			Limit:    10,
		},
	)
	if err != nil || len(page.Events) != 1 ||
		page.Events[0].Delivery != application.MonitorDeliveryQueue {
		t.Fatalf("migrated page = %+v, %v", page, err)
	}
}

func TestTerminalEventsExpireAndYieldCapacity(t *testing.T) {
	t.Parallel()
	cutoff := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	old := cutoff.Add(-time.Minute)
	recent := cutoff.Add(time.Minute)
	state := persistedState{Events: []application.MonitorEvent{
		{ID: "pending", State: "pending", DetectedAt: old},
		{ID: "old-dispatched", State: "dispatched", DispatchedAt: &old},
		{ID: "recent-dispatched", State: "dispatched", DispatchedAt: &recent},
		{ID: "old-acknowledged", State: "acknowledged", AcknowledgedAt: &old},
	}}
	prune(&state, cutoff)
	if len(state.Events) != 2 ||
		state.Events[0].ID != "pending" ||
		state.Events[1].ID != "recent-dispatched" {
		t.Fatalf("retained terminal events = %+v", state.Events)
	}

	state.Events = make([]application.MonitorEvent, maximumQueueEvents)
	for index := range state.Events {
		state.Events[index].ID = "pending"
		state.Events[index].State = "pending"
	}
	state.Events[10] = application.MonitorEvent{
		ID: "newer-terminal", State: "dispatched", DispatchedAt: &recent,
	}
	state.Events[20] = application.MonitorEvent{
		ID: "oldest-terminal", State: "acknowledged", AcknowledgedAt: &old,
	}
	if !makeEventCapacity(&state) || len(state.Events) != maximumQueueEvents-1 {
		t.Fatalf("terminal event did not yield capacity: %d", len(state.Events))
	}
	for _, event := range state.Events {
		if event.ID == "oldest-terminal" {
			t.Fatal("capacity eviction retained the oldest terminal event")
		}
	}
}

func TestDeduplicationTracksOnlyMatchesAndEvictsOldestUnqueuedEntry(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	store := NewAt(t.TempDir())
	scan := testScan(
		testAccountA,
		start,
		true,
		"baseline",
		[]application.MonitorDetection{
			testDetection(testAccountA, "matched", true),
			testDetection(testAccountA, "unmatched", false),
		},
	)
	if _, err := store.CommitScan(t.Context(), scan); err != nil {
		t.Fatal(err)
	}
	state, _, err := store.load(testAccountA)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Seen) != 1 {
		t.Fatalf("deduplication records = %d, want only the match", len(state.Seen))
	}

	protectedDetection := testDetection(testAccountA, "protected", true)
	protectedKey := sourceKey(
		protectedDetection.Provider,
		protectedDetection.SourceObjectID,
	)
	state.Seen = make(map[string]seenObject, maximumSeenObjects)
	for index := range maximumSeenObjects - 1 {
		key := fmt.Sprintf("%064x", index)
		state.Seen[key] = seenObject{
			Count:      1,
			LastSeenAt: start.Add(time.Duration(index) * time.Second),
		}
	}
	state.Seen[protectedKey] = seenObject{
		Count:      1,
		LastSeenAt: start.Add(-time.Hour),
	}
	state.Events = []application.MonitorEvent{{
		Provider:       protectedDetection.Provider,
		SourceObjectID: protectedDetection.SourceObjectID,
		State:          "pending",
	}}
	if !makeSeenCapacity(&state) || len(state.Seen) != maximumSeenObjects-1 {
		t.Fatalf("makeSeenCapacity() retained %d records", len(state.Seen))
	}
	if _, exists := state.Seen[protectedKey]; !exists {
		t.Fatal("deduplication eviction removed a pending event's identity")
	}
}

func TestRetentionKeepsDeduplicationForQueuedEvents(t *testing.T) {
	t.Parallel()
	store := NewAt(t.TempDir())
	start := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	if _, err := store.CommitScan(
		t.Context(),
		testScan(testAccountA, start, true, "baseline", nil),
	); err != nil {
		t.Fatal(err)
	}
	detection := testDetection(testAccountA, "pending", true)
	if _, err := store.CommitScan(
		t.Context(),
		testScan(
			testAccountA,
			start.Add(time.Minute),
			false,
			"first",
			[]application.MonitorDetection{detection},
		),
	); err != nil {
		t.Fatal(err)
	}
	repeated := testScan(
		testAccountA,
		start.Add(2*time.Hour),
		false,
		"repeated",
		[]application.MonitorDetection{detection},
	)
	repeated.RetainAfter = start.Add(time.Hour)
	result, err := store.CommitScan(t.Context(), repeated)
	if err != nil {
		t.Fatal(err)
	}
	if result.Duplicates != 1 || len(result.Events) != 0 {
		t.Fatalf("repeated result = %+v, want one duplicate and no new event", result)
	}
	state, _, err := store.load(testAccountA)
	if err != nil {
		t.Fatal(err)
	}
	key := sourceKey(detection.Provider, detection.SourceObjectID)
	if len(state.Events) != 1 || len(state.Seen) != 1 {
		t.Fatalf(
			"retained queue state has %d events and %d seen records",
			len(state.Events),
			len(state.Seen),
		)
	}
	if seen, exists := state.Seen[key]; !exists || seen.Count != 2 {
		t.Fatalf("protected deduplication record = %+v, exists=%t", seen, exists)
	}
}

func TestMarkDispatchPreservesConcurrentAcknowledgement(t *testing.T) {
	t.Parallel()
	store := NewAt(t.TempDir())
	start := time.Now().UTC().Add(-3 * time.Minute).Truncate(time.Second)
	if _, err := store.CommitScan(
		t.Context(),
		testScan(testAccountA, start, true, "baseline", nil),
	); err != nil {
		t.Fatal(err)
	}
	dispatchScan := testScan(
		testAccountA,
		start.Add(time.Minute),
		false,
		"next",
		[]application.MonitorDetection{testDetection(testAccountA, "message", true)},
	)
	dispatchScan.Delivery = application.MonitorDeliveryRunner
	result, err := store.CommitScan(t.Context(), dispatchScan)
	if err != nil || len(result.Events) != 1 {
		t.Fatalf("CommitScan() = %+v, %v", result, err)
	}
	input := application.MonitorAcknowledgeInput{
		Account: testAccountA,
		EventID: result.Events[0].ID,
	}
	if _, err := store.Acknowledge(
		t.Context(),
		input,
		start.Add(2*time.Minute),
	); err != nil {
		t.Fatal(err)
	}
	dispatchedAt := start.Add(3 * time.Minute)
	if err := store.MarkDispatch(
		t.Context(),
		testAccountA,
		application.MonitorDeliveryRunner,
		[]string{input.EventID},
		dispatchedAt,
	); err != nil {
		t.Fatalf("MarkDispatch() error = %v", err)
	}
	page, err := store.List(t.Context(), application.MonitorEventListInput{
		Account: testAccountA,
		State:   "acknowledged",
		Limit:   10,
	})
	if err != nil || len(page.Events) != 1 ||
		page.Events[0].DispatchedAt == nil ||
		!page.Events[0].DispatchedAt.Equal(dispatchedAt) {
		t.Fatalf("acknowledged events = %+v, %v", page.Events, err)
	}
	status, err := store.Status(t.Context(), testAccountA)
	if err != nil {
		t.Fatal(err)
	}
	if status.Acknowledged != 1 || status.Dispatched != 0 ||
		status.DispatchedLastHour != 1 {
		t.Fatalf("status = %+v", status)
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
		func(detection *application.MonitorDetection) {
			detection.ReceivedAt = "2026-07-28T12:00:00Z\x00"
		},
		func(detection *application.MonitorDetection) {
			detection.Importance = "high\x00"
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
		Delivery:   application.MonitorDeliveryQueue,
		ObservedAt: now, RetainAfter: now.Add(-24 * time.Hour),
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
