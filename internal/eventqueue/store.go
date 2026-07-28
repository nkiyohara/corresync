// Package eventqueue implements the private, account-isolated monitor cursor
// and event outbox.
package eventqueue

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/paths"
)

const (
	stateSchemaVersion = 2
	maximumStateBytes  = 32 << 20
	maximumQueueEvents = 10_000
	maximumSeenObjects = 20_000
)

type seenObject struct {
	Count      int       `json:"count"`
	LastSeenAt time.Time `json:"lastSeenAt"`
}

type persistedState struct {
	SchemaVersion      int                        `json:"schemaVersion"`
	Initialized        bool                       `json:"initialized"`
	Cursor             string                     `json:"cursor,omitempty"`
	LastScanAt         *time.Time                 `json:"lastScanAt,omitempty"`
	LastError          string                     `json:"lastError,omitempty"`
	Seen               map[string]seenObject      `json:"seen"`
	Events             []application.MonitorEvent `json:"events"`
	ScanFailures       int                        `json:"scanFailures"`
	DispatchFailures   int                        `json:"dispatchFailures"`
	Dispatches         []time.Time                `json:"dispatches,omitempty"`
	LastDispatchAt     *time.Time                 `json:"lastDispatchAt,omitempty"`
	CircuitOpenUntil   *time.Time                 `json:"circuitOpenUntil,omitempty"`
	LastAcknowledgedAt *time.Time                 `json:"lastAcknowledgedAt,omitempty"`
}

// Store serializes all queue operations and atomically replaces each account
// snapshot. The path resolver is injectable only for synthetic tests.
type Store struct {
	mu      sync.Mutex
	resolve func(domain.AccountID) (string, error)
}

// New returns a store rooted under the platform account state directory.
func New() *Store {
	return &Store{resolve: func(account domain.AccountID) (string, error) {
		directory, err := paths.AccountStateDir(account)
		if err != nil {
			return "", err
		}
		return filepath.Join(directory, "monitor", "state.json"), nil
	}}
}

// NewAt returns a synthetic store rooted at directory.
func NewAt(directory string) *Store {
	return &Store{resolve: func(account domain.AccountID) (string, error) {
		if err := account.ValidateOpaque(); err != nil {
			return "", err
		}
		return filepath.Join(directory, string(account), "state.json"), nil
	}}
}

func (store *Store) Status(
	ctx context.Context,
	account domain.AccountID,
) (application.MonitorQueueStatus, error) {
	if err := ctx.Err(); err != nil {
		return application.MonitorQueueStatus{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	state, _, err := store.load(account)
	if err != nil {
		return application.MonitorQueueStatus{}, err
	}
	return status(state, time.Now().UTC()), nil
}

func status(state persistedState, now time.Time) application.MonitorQueueStatus {
	result := application.MonitorQueueStatus{
		Initialized: state.Initialized, Cursor: state.Cursor,
		LastScanAt: state.LastScanAt, LastError: state.LastError,
		Queued: len(state.Events), DispatchFailures: state.DispatchFailures,
		ScanFailures:       state.ScanFailures,
		LastDispatchAt:     state.LastDispatchAt,
		CircuitOpenUntil:   state.CircuitOpenUntil,
		LastAcknowledgedAt: state.LastAcknowledgedAt,
	}
	hourAgo := now.Add(-time.Hour)
	for _, dispatched := range state.Dispatches {
		if !dispatched.Before(hourAgo) {
			result.DispatchedLastHour++
		}
	}
	for index := range state.Events {
		event := state.Events[index]
		switch event.State {
		case "acknowledged":
			result.Acknowledged++
		case "dispatched":
			result.Dispatched++
		case "pending":
			result.Pending++
			if result.OldestPendingAt == nil || event.DetectedAt.Before(*result.OldestPendingAt) {
				detected := event.DetectedAt
				result.OldestPendingAt = &detected
			}
		}
	}
	return result
}

func (store *Store) List(
	ctx context.Context,
	input application.MonitorEventListInput,
) (application.MonitorEventPage, error) {
	if err := input.Validate(); err != nil {
		return application.MonitorEventPage{}, err
	}
	if err := ctx.Err(); err != nil {
		return application.MonitorEventPage{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	state, _, err := store.load(input.Account)
	if err != nil {
		return application.MonitorEventPage{}, err
	}
	events := make([]application.MonitorEvent, 0, len(state.Events))
	for _, event := range state.Events {
		if (input.State == "" || event.State == input.State) &&
			(input.Delivery == "" || event.Delivery == input.Delivery) {
			events = append(events, event)
		}
	}
	sort.Slice(events, func(left, right int) bool {
		if !events[left].DetectedAt.Equal(events[right].DetectedAt) {
			return events[left].DetectedAt.After(events[right].DetectedAt)
		}
		return events[left].ID < events[right].ID
	})
	total := len(events)
	start := min(input.Offset, total)
	end := min(start+input.Limit, total)
	pageEvents := append([]application.MonitorEvent(nil), events[start:end]...)
	return application.MonitorEventPage{
		Events: pageEvents, Offset: input.Offset, Limit: input.Limit,
		Total: total, HasMore: end < total,
	}, nil
}

func (store *Store) Acknowledge(
	ctx context.Context,
	input application.MonitorAcknowledgeInput,
	now time.Time,
) (application.MonitorEvent, error) {
	if err := input.Validate(); err != nil {
		return application.MonitorEvent{}, err
	}
	if err := ctx.Err(); err != nil {
		return application.MonitorEvent{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	state, path, err := store.load(input.Account)
	if err != nil {
		return application.MonitorEvent{}, err
	}
	for index := range state.Events {
		if state.Events[index].ID != input.EventID {
			continue
		}
		if state.Events[index].State != "acknowledged" {
			acknowledged := now.UTC()
			state.Events[index].State = "acknowledged"
			state.Events[index].AcknowledgedAt = &acknowledged
			state.LastAcknowledgedAt = &acknowledged
			if err := store.save(path, state); err != nil {
				return application.MonitorEvent{}, err
			}
		}
		return state.Events[index], nil
	}
	return application.MonitorEvent{}, errors.New("monitor event was not found in this account queue")
}

func (store *Store) Purge(ctx context.Context, account domain.AccountID) (int, error) {
	if err := account.ValidateOpaque(); err != nil {
		return 0, err
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	state, path, err := store.load(account)
	if err != nil {
		return 0, err
	}
	count := len(state.Events)
	state.Events = nil
	if err := store.save(path, state); err != nil {
		return 0, err
	}
	return count, nil
}

func (store *Store) CommitScan(
	ctx context.Context,
	scan application.MonitorScan,
) (application.MonitorScanResult, error) {
	if err := validateScan(scan); err != nil {
		return application.MonitorScanResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return application.MonitorScanResult{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	state, path, err := store.load(scan.Account)
	if err != nil {
		return application.MonitorScanResult{}, err
	}
	if scan.Bootstrap != !state.Initialized {
		return application.MonitorScanResult{}, errors.New("monitor bootstrap state changed; retry the scan")
	}
	prune(&state, scan.RetainAfter)
	result := application.MonitorScanResult{}
	for _, detection := range scan.Detections {
		key := sourceKey(detection.Provider, detection.SourceObjectID)
		seen, duplicate := state.Seen[key]
		seen.Count++
		seen.LastSeenAt = scan.ObservedAt.UTC()
		state.Seen[key] = seen
		if duplicate {
			result.Duplicates++
			for index := range state.Events {
				if state.Events[index].ID == eventID(scan.Account, detection.Provider, detection.SourceObjectID) {
					state.Events[index].DeliveryCount = seen.Count
					break
				}
			}
			continue
		}
		if scan.Bootstrap || !detection.Matched {
			continue
		}
		event := monitorEvent(detection, scan.Delivery, scan.ObservedAt)
		if scan.Delivery != "" {
			if len(state.Events) >= maximumQueueEvents {
				return application.MonitorScanResult{}, errors.New(
					"monitor queue reached its 10000-event safety bound; acknowledge and purge events",
				)
			}
			state.Events = append(state.Events, event)
		}
		result.Events = append(result.Events, event)
	}
	if len(state.Seen) > maximumSeenObjects {
		return application.MonitorScanResult{}, errors.New(
			"monitor deduplication window reached its safety bound; shorten retention or rescan",
		)
	}
	scanned := scan.ObservedAt.UTC()
	state.SchemaVersion = stateSchemaVersion
	state.Initialized = true
	state.Cursor = scan.Cursor
	state.LastScanAt = &scanned
	state.ScanFailures = 0
	if state.DispatchFailures == 0 {
		state.LastError = ""
	}
	if err := store.save(path, state); err != nil {
		return application.MonitorScanResult{}, err
	}
	return result, nil
}

func (store *Store) MarkDispatch(
	ctx context.Context,
	account domain.AccountID,
	delivery string,
	eventIDs []string,
	now time.Time,
) error {
	if err := account.ValidateOpaque(); err != nil {
		return err
	}
	if len(eventIDs) == 0 || len(eventIDs) > 100 {
		return errors.New("dispatch batch must contain 1 through 100 events")
	}
	if delivery != application.MonitorDeliveryNotification &&
		delivery != application.MonitorDeliveryRunner {
		return errors.New("dispatch requires a notification or runner delivery kind")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	state, path, err := store.load(account)
	if err != nil {
		return err
	}
	pending := make(map[string]struct{}, len(eventIDs))
	for _, id := range eventIDs {
		if _, duplicate := pending[id]; duplicate {
			return errors.New("dispatch batch contains a duplicate event")
		}
		pending[id] = struct{}{}
	}
	dispatched := now.UTC()
	for index := range state.Events {
		if _, exists := pending[state.Events[index].ID]; !exists {
			continue
		}
		if state.Events[index].Delivery != delivery {
			return errors.New("dispatch batch crossed its delivery boundary")
		}
		switch state.Events[index].State {
		case "pending":
			state.Events[index].State = "dispatched"
		case "acknowledged":
			// The runner already received this event. Preserve an
			// acknowledgement that raced with the out-of-process call while
			// still recording that the dispatch completed.
		default:
			return errors.New("dispatch batch contains an already-dispatched event")
		}
		state.Events[index].DispatchedAt = &dispatched
		delete(pending, state.Events[index].ID)
	}
	if len(pending) != 0 {
		return errors.New("dispatch batch contains an event outside this account queue")
	}
	state.DispatchFailures = 0
	state.LastError = ""
	state.CircuitOpenUntil = nil
	state.LastDispatchAt = &dispatched
	pruneDispatches(&state, dispatched.Add(-time.Hour))
	if len(state.Dispatches)+len(eventIDs) >
		application.MaxMonitorDispatchesPerHour {
		return errors.New("dispatch history reached its hourly safety bound")
	}
	for range len(eventIDs) {
		state.Dispatches = append(state.Dispatches, dispatched)
	}
	return store.save(path, state)
}

func (store *Store) MarkScanFailure(
	ctx context.Context,
	account domain.AccountID,
	_ time.Time,
	reason string,
) error {
	if err := account.ValidateOpaque(); err != nil {
		return err
	}
	if reason == "" || len(reason) > 64 || strings.ContainsAny(reason, "\r\n\x00") {
		return errors.New("scan failure reason must be a bounded machine code")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	state, path, err := store.load(account)
	if err != nil {
		return err
	}
	state.ScanFailures++
	state.LastError = reason
	return store.save(path, state)
}

func (store *Store) MarkDispatchFailure(
	ctx context.Context,
	account domain.AccountID,
	now time.Time,
	reason string,
) error {
	if err := account.ValidateOpaque(); err != nil {
		return err
	}
	if reason == "" || len(reason) > 64 || strings.ContainsAny(reason, "\r\n\x00") {
		return errors.New("dispatch failure reason must be a bounded machine code")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	state, path, err := store.load(account)
	if err != nil {
		return err
	}
	state.DispatchFailures++
	state.LastError = reason
	if state.DispatchFailures >= 3 {
		until := now.UTC().Add(15 * time.Minute)
		state.CircuitOpenUntil = &until
	}
	return store.save(path, state)
}

func validateScan(scan application.MonitorScan) error {
	if err := scan.Account.ValidateOpaque(); err != nil {
		return err
	}
	if !validMonitorCursor(scan.Cursor) {
		return errors.New("monitor cursor must be a SHA-256 hex string")
	}
	if scan.ObservedAt.IsZero() || scan.RetainAfter.IsZero() ||
		!scan.RetainAfter.Before(scan.ObservedAt) {
		return errors.New("monitor scan requires a valid observation and retention window")
	}
	if len(scan.Detections) > 1000 {
		return errors.New("monitor scan exceeds the 1000-object recovery bound")
	}
	switch scan.Delivery {
	case "", application.MonitorDeliveryQueue,
		application.MonitorDeliveryNotification,
		application.MonitorDeliveryRunner:
	default:
		return errors.New("monitor scan has an invalid delivery kind")
	}
	for _, detection := range scan.Detections {
		if detection.Account != scan.Account {
			return errors.New("monitor detection crossed its account boundary")
		}
		if err := domain.AccountAlias(detection.AccountAlias).Validate(); err != nil {
			return err
		}
		if err := detection.Provider.Validate(); err != nil {
			return err
		}
		if detection.SourceObjectID == "" || len(detection.SourceObjectID) > 4096 ||
			strings.ContainsAny(detection.SourceObjectID, "\r\n\x00") {
			return errors.New("monitor source object ID is malformed")
		}
		if len(detection.Sender.Name) > 512 ||
			len(detection.Sender.Address) > 320 ||
			len(detection.Subject) > 2048 ||
			len(detection.ReceivedAt) > 128 ||
			len(detection.Importance) > 32 ||
			strings.ContainsAny(
				detection.Sender.Name+detection.Sender.Address+
					detection.Subject+detection.ReceivedAt+
					detection.Importance,
				"\x00",
			) {
			return errors.New("monitor detection metadata exceeds its bounds")
		}
		switch detection.FilterReason {
		case "matched", "sender_domain", "subject", "importance", "self_message":
		default:
			return errors.New("unknown monitor filter result")
		}
		if detection.Matched != (detection.FilterReason == "matched") {
			return errors.New("monitor match and filter result disagree")
		}
	}
	return nil
}

func validMonitorCursor(cursor string) bool {
	if len(cursor) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(cursor)
	return err == nil
}

func monitorEvent(
	detection application.MonitorDetection,
	delivery string,
	detectedAt time.Time,
) application.MonitorEvent {
	return application.MonitorEvent{
		ID:      eventID(detection.Account, detection.Provider, detection.SourceObjectID),
		Account: detection.Account, AccountAlias: detection.AccountAlias,
		Provider: detection.Provider, SourceObjectID: detection.SourceObjectID,
		Sender: detection.Sender, Subject: detection.Subject,
		ReceivedAt: detection.ReceivedAt, Importance: detection.Importance,
		HasAttachments: detection.HasAttachments,
		Trust:          application.MonitorTrustMarker, Delivery: delivery,
		State:         "pending",
		DeliveryCount: 1, DetectedAt: detectedAt.UTC(),
	}
}

func sourceKey(provider domain.ProviderID, sourceObjectID string) string {
	digest := sha256.Sum256([]byte(string(provider) + "\x00" + sourceObjectID))
	return hex.EncodeToString(digest[:])
}

func eventID(
	account domain.AccountID,
	provider domain.ProviderID,
	sourceObjectID string,
) string {
	digest := sha256.Sum256(
		[]byte(string(account) + "\x00" + string(provider) + "\x00" + sourceObjectID),
	)
	return "evt_" + base64.RawURLEncoding.EncodeToString(digest[:])
}

func prune(state *persistedState, retainAfter time.Time) {
	for key, seen := range state.Seen {
		if seen.LastSeenAt.Before(retainAfter) {
			delete(state.Seen, key)
		}
	}
	retained := state.Events[:0]
	for _, event := range state.Events {
		if event.State != "acknowledged" || !event.DetectedAt.Before(retainAfter) {
			retained = append(retained, event)
		}
	}
	state.Events = retained
	pruneDispatches(state, retainAfter)
}

func pruneDispatches(state *persistedState, retainAfter time.Time) {
	retained := state.Dispatches[:0]
	for _, dispatched := range state.Dispatches {
		if !dispatched.Before(retainAfter) {
			retained = append(retained, dispatched)
		}
	}
	state.Dispatches = retained
}

func (store *Store) load(account domain.AccountID) (persistedState, string, error) {
	if err := account.ValidateOpaque(); err != nil {
		return persistedState{}, "", err
	}
	path, err := store.resolve(account)
	if err != nil {
		return persistedState{}, "", err
	}
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return persistedState{}, "", errors.New(
			"monitor state path exists and is not a regular file",
		)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return persistedState{}, "", fmt.Errorf("inspect monitor state: %w", err)
	}
	file, err := os.Open(path) // #nosec G304 -- path is account-ID-derived local state.
	if errors.Is(err, os.ErrNotExist) {
		return persistedState{
			SchemaVersion: stateSchemaVersion,
			Seen:          make(map[string]seenObject),
		}, path, nil
	}
	if err != nil {
		return persistedState{}, "", fmt.Errorf("open monitor state: %w", err)
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return persistedState{}, "", fmt.Errorf("inspect monitor state: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > maximumStateBytes {
		return persistedState{}, "", errors.New("monitor state is not a bounded regular file")
	}
	var state persistedState
	decoder := json.NewDecoder(io.LimitReader(file, maximumStateBytes+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return persistedState{}, "", fmt.Errorf("decode monitor state: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return persistedState{}, "", errors.New("monitor state contains trailing data")
	}
	if (state.SchemaVersion != 1 && state.SchemaVersion != stateSchemaVersion) ||
		state.Seen == nil ||
		len(state.Events) > maximumQueueEvents || len(state.Seen) > maximumSeenObjects {
		return persistedState{}, "", errors.New("monitor state failed structural validation")
	}
	if state.SchemaVersion == 1 {
		for index := range state.Events {
			if state.Events[index].Delivery == "" {
				// Legacy outbox entries predate automatic notification
				// persistence. Keep them local-only rather than guessing a
				// newly configured external sink.
				state.Events[index].Delivery = application.MonitorDeliveryQueue
			}
		}
		state.SchemaVersion = stateSchemaVersion
	}
	if state.Initialized {
		if len(state.Cursor) != sha256.Size*2 {
			return persistedState{}, "", errors.New("monitor state cursor is invalid")
		}
		if _, err := hex.DecodeString(state.Cursor); err != nil {
			return persistedState{}, "", errors.New("monitor state cursor is invalid")
		}
	}
	if state.ScanFailures < 0 || state.DispatchFailures < 0 ||
		len(state.Dispatches) > application.MaxMonitorDispatchesPerHour ||
		len(state.LastError) > 64 ||
		strings.ContainsAny(state.LastError, "\r\n\x00") {
		return persistedState{}, "", errors.New("monitor state counters are invalid")
	}
	for key, seen := range state.Seen {
		if len(key) != sha256.Size*2 || seen.Count < 1 || seen.LastSeenAt.IsZero() {
			return persistedState{}, "", errors.New("monitor state deduplication record is invalid")
		}
		if _, err := hex.DecodeString(key); err != nil {
			return persistedState{}, "", errors.New("monitor state deduplication record is invalid")
		}
	}
	for _, event := range state.Events {
		if err := event.Validate(account); err != nil ||
			event.ID != eventID(account, event.Provider, event.SourceObjectID) {
			return persistedState{}, "", errors.New("monitor state event is invalid")
		}
	}
	return state, path, nil
}

func (store *Store) save(path string, state persistedState) error {
	encoded, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encode monitor state: %w", err)
	}
	if len(encoded) > maximumStateBytes {
		return errors.New("monitor state exceeds its size bound")
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create monitor state directory: %w", err)
	}
	if err := os.Chmod(directory, 0o700); err != nil { // #nosec G302 -- owner-only state.
		return fmt.Errorf("protect monitor state directory: %w", err)
	}
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return errors.New("monitor state path exists and is not a regular file")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect monitor state path: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".state-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary monitor state: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := io.Copy(temporary, bytes.NewReader(encoded)); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write monitor state: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync monitor state: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close monitor state: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace monitor state: %w", err)
	}
	return os.Chmod(path, 0o600)
}
