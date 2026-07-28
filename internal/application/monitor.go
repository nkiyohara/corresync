package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nkiyohara/corresync/internal/domain"
)

const (
	MaxMonitorEventsPage        = 100
	MaxMonitorDispatchesPerHour = 1000
	MonitorTrustMarker          = "untrusted_data"
	MonitorDeliveryQueue        = "queue"
	MonitorDeliveryNotification = "notification"
	MonitorDeliveryRunner       = "runner"
)

// MonitorPolicy is the secret-free, immutable account policy visible to
// status callers and consumed by the daemon-owned watcher.
type MonitorPolicy struct {
	Account            domain.AccountID   `json:"account"`
	Alias              string             `json:"alias"`
	Address            string             `json:"-"`
	Mode               domain.MonitorMode `json:"mode"`
	PollInterval       time.Duration      `json:"-"`
	Debounce           time.Duration      `json:"-"`
	Retention          time.Duration      `json:"-"`
	RateLimitHour      int                `json:"rateLimitHour"`
	SenderDomains      []string           `json:"senderDomains,omitempty"`
	SubjectContains    []string           `json:"subjectContains,omitempty"`
	ImportantOnly      bool               `json:"importantOnly,omitempty"`
	QuietStart         string             `json:"quietStart,omitempty"`
	QuietEnd           string             `json:"quietEnd,omitempty"`
	QuietTimeZone      string             `json:"quietTimeZone,omitempty"`
	NotificationTarget string             `json:"notificationTarget,omitempty"`
	NotificationFields []string           `json:"notificationFields,omitempty"`
	RunnerTarget       string             `json:"runnerTarget,omitempty"`
	RunnerEgress       string             `json:"runnerEgress,omitempty"`
	RunnerFields       []string           `json:"runnerFields,omitempty"`
}

// MonitorCatalog supplies account-scoped policy without exposing mutable
// configuration to mail, notification, or runner adapters.
type MonitorCatalog interface {
	MonitorPolicy(context.Context, domain.AccountID) (MonitorPolicy, error)
}

// MonitorQueueStatus contains only local operational metadata.
type MonitorQueueStatus struct {
	Initialized        bool       `json:"initialized"`
	Cursor             string     `json:"cursor,omitempty"`
	LastScanAt         *time.Time `json:"lastScanAt,omitempty"`
	LastError          string     `json:"lastError,omitempty"`
	Queued             int        `json:"queued"`
	Pending            int        `json:"pending"`
	Dispatched         int        `json:"dispatched"`
	Acknowledged       int        `json:"acknowledged"`
	ScanFailures       int        `json:"scanFailures"`
	DispatchFailures   int        `json:"dispatchFailures"`
	DispatchedLastHour int        `json:"dispatchedLastHour"`
	LastDispatchAt     *time.Time `json:"lastDispatchAt,omitempty"`
	CircuitOpenUntil   *time.Time `json:"circuitOpenUntil,omitempty"`
	OldestPendingAt    *time.Time `json:"oldestPendingAt,omitempty"`
	LastAcknowledgedAt *time.Time `json:"lastAcknowledgedAt,omitempty"`
}

// MonitorStatus joins immutable consent with local queue health.
type MonitorStatus struct {
	Account           domain.AccountID   `json:"account"`
	Alias             string             `json:"alias"`
	Mode              domain.MonitorMode `json:"mode"`
	CollectionEnabled bool               `json:"collectionEnabled"`
	PollInterval      string             `json:"pollInterval,omitempty"`
	Debounce          string             `json:"debounce,omitempty"`
	Retention         string             `json:"retention,omitempty"`
	RateLimitHour     int                `json:"rateLimitHour,omitempty"`
	Filter            MonitorFilterView  `json:"filter"`
	QuietHours        *MonitorQuietView  `json:"quietHours,omitempty"`
	Notification      *MonitorSinkView   `json:"notification,omitempty"`
	Runner            *MonitorSinkView   `json:"runner,omitempty"`
	Queue             MonitorQueueStatus `json:"queue"`
}

// MonitorFilterView repeats only configured metadata predicates.
type MonitorFilterView struct {
	SenderDomains   []string `json:"senderDomains,omitempty"`
	SubjectContains []string `json:"subjectContains,omitempty"`
	ImportantOnly   bool     `json:"importantOnly,omitempty"`
}

// MonitorQuietView discloses the local schedule without materializing events.
type MonitorQuietView struct {
	Start    string `json:"start"`
	End      string `json:"end"`
	TimeZone string `json:"timeZone"`
}

// MonitorSinkView discloses exactly where and which fields may leave the
// watcher boundary.
type MonitorSinkView struct {
	Destination string   `json:"destination"`
	Egress      string   `json:"egress"`
	Fields      []string `json:"fields"`
}

// MonitorEvent is metadata-first and never contains a body or attachment.
type MonitorEvent struct {
	ID             string            `json:"id"`
	Account        domain.AccountID  `json:"account"`
	AccountAlias   string            `json:"accountAlias"`
	Provider       domain.ProviderID `json:"provider"`
	SourceObjectID string            `json:"sourceObjectId"`
	Sender         MailAddress       `json:"sender,omitempty"`
	Subject        string            `json:"subject,omitempty"`
	ReceivedAt     string            `json:"receivedAt,omitempty"`
	Importance     string            `json:"importance,omitempty"`
	HasAttachments bool              `json:"hasAttachments"`
	Trust          string            `json:"trust"`
	Delivery       string            `json:"delivery"`
	State          string            `json:"state"`
	DeliveryCount  int               `json:"deliveryCount"`
	DetectedAt     time.Time         `json:"detectedAt"`
	DispatchedAt   *time.Time        `json:"dispatchedAt,omitempty"`
	AcknowledgedAt *time.Time        `json:"acknowledgedAt,omitempty"`
}

// MonitorEventPage is a stable bounded queue projection.
type MonitorEventPage struct {
	Events  []MonitorEvent `json:"events"`
	Offset  int            `json:"offset"`
	Limit   int            `json:"limit"`
	Total   int            `json:"total"`
	HasMore bool           `json:"hasMore"`
}

// MonitorEventListInput selects one account queue and an optional state.
type MonitorEventListInput struct {
	Account  domain.AccountID `json:"account"`
	State    string           `json:"state,omitempty"`
	Delivery string           `json:"delivery,omitempty"`
	Offset   int              `json:"offset"`
	Limit    int              `json:"limit"`
}

// MonitorAcknowledgeInput updates one local queue item only.
type MonitorAcknowledgeInput struct {
	Account domain.AccountID `json:"account"`
	EventID string           `json:"eventId"`
}

// MonitorDetection is one provider metadata observation. Matched records may
// be released to the configured sink; all others update deduplication only.
type MonitorDetection struct {
	Account        domain.AccountID
	AccountAlias   string
	Provider       domain.ProviderID
	SourceObjectID string
	Sender         MailAddress
	Subject        string
	ReceivedAt     string
	Importance     string
	HasAttachments bool
	Matched        bool
	FilterReason   string
}

// MonitorScan atomically advances one provider cursor with every observation.
type MonitorScan struct {
	Account     domain.AccountID
	Cursor      string
	Bootstrap   bool
	Delivery    string
	ObservedAt  time.Time
	RetainAfter time.Time
	Detections  []MonitorDetection
}

// MonitorScanResult returns only first-seen matching events. Duplicate
// deliveries still increment their stored delivery count.
type MonitorScanResult struct {
	Events     []MonitorEvent
	Duplicates int
}

// MonitorEventStore is the durable, account-isolated application port.
type MonitorEventStore interface {
	Status(context.Context, domain.AccountID) (MonitorQueueStatus, error)
	List(context.Context, MonitorEventListInput) (MonitorEventPage, error)
	Acknowledge(context.Context, MonitorAcknowledgeInput, time.Time) (MonitorEvent, error)
	Purge(context.Context, domain.AccountID) (int, error)
	CommitScan(context.Context, MonitorScan) (MonitorScanResult, error)
	MarkDispatch(
		context.Context,
		domain.AccountID,
		string,
		[]string,
		time.Time,
	) error
	MarkScanFailure(context.Context, domain.AccountID, time.Time, string) error
	MarkDispatchFailure(context.Context, domain.AccountID, time.Time, string) error
}

// MonitorNotifier emits one local notification containing only the released
// fields. Implementations receive no service, credential, or policy handle.
type MonitorNotifier interface {
	Notify(context.Context, MonitorRelease) error
}

// MonitorRunner invokes one explicitly configured process with a bounded batch
// and read-only authority declaration.
type MonitorRunner interface {
	Run(context.Context, MonitorRunnerRequest) error
}

// MonitorRelease contains one event projection and the exact disclosure list.
type MonitorRelease struct {
	Destination string         `json:"destination"`
	Fields      []string       `json:"fields"`
	Event       map[string]any `json:"event"`
}

// MonitorRunnerRequest is the entire runner input. The runner is never given
// an approval token, credential, config path, or mutable policy object.
type MonitorRunnerRequest struct {
	SchemaVersion  int              `json:"schemaVersion"`
	Account        domain.AccountID `json:"account"`
	Trust          string           `json:"trust"`
	AllowedEffects []string         `json:"allowedEffects"`
	Destination    string           `json:"destination"`
	Egress         string           `json:"egress"`
	Fields         []string         `json:"fields"`
	Events         []map[string]any `json:"events"`
}

// Validate rejects fabricated daemon status before it reaches a CLI or MCP
// adapter.
func (status MonitorStatus) Validate(expected domain.AccountID) error {
	if status.Account != expected {
		return errors.New("monitor status crossed its account boundary")
	}
	if err := status.Account.ValidateOpaque(); err != nil {
		return err
	}
	if err := domain.AccountAlias(status.Alias).Validate(); err != nil {
		return err
	}
	if err := status.Mode.Validate(); err != nil {
		return err
	}
	if status.CollectionEnabled != status.Mode.Collects() {
		return errors.New("monitor collection state disagrees with its mode")
	}
	if status.Queue.Queued < 0 || status.Queue.Queued > 10_000 ||
		status.Queue.Pending < 0 || status.Queue.Dispatched < 0 ||
		status.Queue.Acknowledged < 0 ||
		status.Queue.Pending+status.Queue.Dispatched+status.Queue.Acknowledged !=
			status.Queue.Queued ||
		status.Queue.ScanFailures < 0 ||
		status.Queue.DispatchFailures < 0 ||
		status.Queue.DispatchedLastHour < 0 ||
		status.Queue.DispatchedLastHour > MaxMonitorDispatchesPerHour {
		return errors.New("monitor queue status has invalid counts")
	}
	if !status.Mode.Collects() {
		if status.Notification != nil || status.Runner != nil {
			return errors.New("disabled monitoring exposes an active sink")
		}
		return nil
	}
	if status.RateLimitHour < 1 ||
		status.RateLimitHour > MaxMonitorDispatchesPerHour ||
		len(status.Filter.SenderDomains) > 32 ||
		len(status.Filter.SubjectContains) > 32 {
		return errors.New("monitor status has invalid policy bounds")
	}
	for _, encoded := range []string{
		status.PollInterval,
		status.Debounce,
		status.Retention,
	} {
		if _, err := time.ParseDuration(encoded); err != nil {
			return errors.New("monitor status has an invalid duration")
		}
	}
	switch status.Mode {
	case domain.MonitorOff:
		return errors.New("disabled monitor unexpectedly reached sink validation")
	case domain.MonitorNotify:
		if status.Notification == nil || status.Runner != nil {
			return errors.New("notify status has invalid sinks")
		}
		if err := status.Notification.validate("local"); err != nil {
			return err
		}
	case domain.MonitorQueue:
		if status.Notification != nil || status.Runner != nil {
			return errors.New("queue status has invalid sinks")
		}
	case domain.MonitorAgent:
		if status.Notification != nil || status.Runner == nil {
			return errors.New("agent status has invalid sinks")
		}
		if err := status.Runner.validate(""); err != nil {
			return err
		}
	}
	return nil
}

func (sink MonitorSinkView) validate(expectedEgress string) error {
	if sink.Destination == "" || len(sink.Destination) > 4096 ||
		strings.TrimSpace(sink.Destination) != sink.Destination ||
		strings.ContainsAny(sink.Destination, "\r\n\x00") {
		return errors.New("monitor sink destination is invalid")
	}
	if expectedEgress != "" {
		if sink.Egress != expectedEgress {
			return errors.New("monitor sink egress is invalid")
		}
	} else if sink.Egress != "local" && sink.Egress != "remote" {
		return errors.New("monitor runner egress is invalid")
	}
	if len(sink.Fields) == 0 || len(sink.Fields) > 8 {
		return errors.New("monitor sink fields are invalid")
	}
	seen := make(map[string]struct{}, len(sink.Fields))
	for _, field := range sink.Fields {
		switch field {
		case "account", "event_id", "has_attachments", "importance",
			"received_at", "sender", "subject", "trust":
		default:
			return errors.New("monitor sink includes an unsupported field")
		}
		if _, exists := seen[field]; exists {
			return errors.New("monitor sink includes duplicate fields")
		}
		seen[field] = struct{}{}
	}
	return nil
}

// Validate rejects malformed local event data at process boundaries.
func (event MonitorEvent) Validate(expected domain.AccountID) error {
	if event.Account != expected {
		return errors.New("monitor event crossed its account boundary")
	}
	if err := event.Account.ValidateOpaque(); err != nil {
		return err
	}
	if err := domain.AccountAlias(event.AccountAlias).Validate(); err != nil {
		return err
	}
	if err := event.Provider.Validate(); err != nil {
		return err
	}
	if err := (MonitorAcknowledgeInput{
		Account: event.Account,
		EventID: event.ID,
	}).Validate(); err != nil {
		return err
	}
	if event.SourceObjectID == "" || len(event.SourceObjectID) > 4096 ||
		strings.ContainsAny(event.SourceObjectID, "\r\n\x00") {
		return errors.New("monitor event source object ID is malformed")
	}
	if len(event.Sender.Name) > 512 || len(event.Sender.Address) > 320 ||
		len(event.Subject) > 2048 || len(event.ReceivedAt) > 128 ||
		len(event.Importance) > 32 ||
		strings.ContainsAny(
			event.Sender.Name+event.Sender.Address+event.Subject+
				event.ReceivedAt+event.Importance,
			"\x00",
		) {
		return errors.New("monitor event metadata exceeds its bounds")
	}
	if event.Trust != MonitorTrustMarker || event.DeliveryCount < 1 ||
		event.DetectedAt.IsZero() {
		return errors.New("monitor event lacks trust or delivery metadata")
	}
	if !validMonitorDelivery(event.Delivery, false) {
		return errors.New("monitor event has an invalid delivery kind")
	}
	switch event.State {
	case "pending":
		if event.DispatchedAt != nil || event.AcknowledgedAt != nil {
			return errors.New("pending event has completion metadata")
		}
	case "dispatched":
		if event.DispatchedAt == nil || event.AcknowledgedAt != nil {
			return errors.New("dispatched event has invalid completion metadata")
		}
	case "acknowledged":
		if event.AcknowledgedAt == nil {
			return errors.New("acknowledged event lacks its timestamp")
		}
	default:
		return errors.New("monitor event has an invalid state")
	}
	return nil
}

// Validate checks one daemon queue page against the exact request.
func (page MonitorEventPage) Validate(input MonitorEventListInput) error {
	if err := input.Validate(); err != nil {
		return err
	}
	if page.Offset != input.Offset || page.Limit != input.Limit ||
		page.Total < 0 || page.Total > 10_000 ||
		len(page.Events) > input.Limit || page.Total < len(page.Events) {
		return errors.New("monitor event page has invalid bounds")
	}
	expectedMore := page.Offset+len(page.Events) < page.Total
	if page.HasMore != expectedMore {
		return errors.New("monitor event page has invalid continuation metadata")
	}
	for _, event := range page.Events {
		if err := event.Validate(input.Account); err != nil {
			return err
		}
		if input.State != "" && event.State != input.State {
			return errors.New("monitor event page violated its state filter")
		}
		if input.Delivery != "" && event.Delivery != input.Delivery {
			return errors.New("monitor event page violated its delivery filter")
		}
	}
	return nil
}

// MonitorService exposes the same typed status, list, and acknowledgement use
// cases to CLI, daemon IPC, and MCP.
type MonitorService struct {
	catalog MonitorCatalog
	store   MonitorEventStore
	audit   AuditRecorder
	now     func() time.Time
}

func NewMonitorService(
	catalog MonitorCatalog,
	store MonitorEventStore,
	audit AuditRecorder,
) (*MonitorService, error) {
	if catalog == nil || store == nil || audit == nil {
		return nil, errors.New("monitor catalog, store, and audit recorder are required")
	}
	return &MonitorService{
		catalog: catalog, store: store, audit: audit,
		now: func() time.Time { return time.Now().UTC() },
	}, nil
}

func (service *MonitorService) Status(
	ctx context.Context,
	account domain.AccountID,
	caller domain.Caller,
) (MonitorStatus, error) {
	if err := account.ValidateOpaque(); err != nil {
		return MonitorStatus{}, err
	}
	policy, err := service.catalog.MonitorPolicy(ctx, account)
	if err != nil {
		return MonitorStatus{}, err
	}
	if policy.Account != account || policy.Mode.Validate() != nil {
		return MonitorStatus{}, errors.New("monitor catalog returned an invalid account policy")
	}
	operation, err := domain.NewOperation(
		"monitor.status",
		domain.EffectRead,
		account,
		struct{}{},
	)
	if err != nil {
		return MonitorStatus{}, err
	}
	if err := service.auditPrepared(ctx, operation, caller); err != nil {
		return MonitorStatus{}, err
	}
	queue, callErr := service.store.Status(ctx, account)
	status := MonitorStatus{
		Account: account, Alias: policy.Alias, Mode: policy.Mode,
		CollectionEnabled: policy.Mode.Collects(),
		RateLimitHour:     policy.RateLimitHour,
		Filter: MonitorFilterView{
			SenderDomains:   append([]string(nil), policy.SenderDomains...),
			SubjectContains: append([]string(nil), policy.SubjectContains...),
			ImportantOnly:   policy.ImportantOnly,
		},
		Queue: queue,
	}
	if policy.Mode.Collects() {
		status.PollInterval = policy.PollInterval.String()
		status.Debounce = policy.Debounce.String()
		status.Retention = policy.Retention.String()
	}
	if policy.QuietStart != "" {
		status.QuietHours = &MonitorQuietView{
			Start: policy.QuietStart, End: policy.QuietEnd,
			TimeZone: policy.QuietTimeZone,
		}
	}
	if policy.NotificationTarget != "" {
		status.Notification = &MonitorSinkView{
			Destination: policy.NotificationTarget,
			Egress:      "local",
			Fields:      append([]string(nil), policy.NotificationFields...),
		}
	}
	if policy.RunnerTarget != "" {
		status.Runner = &MonitorSinkView{
			Destination: policy.RunnerTarget,
			Egress:      policy.RunnerEgress,
			Fields:      append([]string(nil), policy.RunnerFields...),
		}
	}
	if callErr == nil {
		callErr = status.Validate(account)
	}
	auditErr := service.auditExecuted(ctx, operation, caller, callErr)
	if callErr != nil || auditErr != nil {
		return MonitorStatus{}, errors.Join(callErr, auditErr)
	}
	return status, nil
}

func (service *MonitorService) List(
	ctx context.Context,
	input MonitorEventListInput,
	caller domain.Caller,
) (MonitorEventPage, error) {
	if err := input.Validate(); err != nil {
		return MonitorEventPage{}, err
	}
	operation, err := domain.NewOperation(
		"events.list",
		domain.EffectRead,
		input.Account,
		input,
	)
	if err != nil {
		return MonitorEventPage{}, err
	}
	if err := service.auditPrepared(ctx, operation, caller); err != nil {
		return MonitorEventPage{}, err
	}
	page, callErr := service.store.List(ctx, input)
	if callErr == nil {
		callErr = page.Validate(input)
	}
	auditErr := service.auditExecuted(ctx, operation, caller, callErr)
	if callErr != nil || auditErr != nil {
		return MonitorEventPage{}, errors.Join(callErr, auditErr)
	}
	return page, nil
}

func (service *MonitorService) Acknowledge(
	ctx context.Context,
	input MonitorAcknowledgeInput,
	caller domain.Caller,
) (MonitorEvent, error) {
	if err := input.Validate(); err != nil {
		return MonitorEvent{}, err
	}
	operation, err := domain.NewTargetedOperation(
		"events.acknowledge",
		domain.EffectReversibleWrite,
		input.Account,
		domain.TargetRef{Kind: domain.TargetLocalQueue, ID: input.EventID},
		input,
	)
	if err != nil {
		return MonitorEvent{}, err
	}
	if err := service.auditPrepared(ctx, operation, caller); err != nil {
		return MonitorEvent{}, err
	}
	event, callErr := service.store.Acknowledge(ctx, input, service.now())
	if callErr == nil {
		if event.ID != input.EventID || event.State != "acknowledged" {
			callErr = errors.New("monitor store returned an invalid acknowledgement")
		} else {
			callErr = event.Validate(input.Account)
		}
	}
	auditErr := service.auditExecutedMonitor(
		ctx,
		operation,
		caller,
		callErr,
		&MonitorAudit{
			Stage: "acknowledgement", Result: monitorAuditResult(callErr), Count: 1,
		},
	)
	if callErr != nil || auditErr != nil {
		return MonitorEvent{}, errors.Join(callErr, auditErr)
	}
	return event, nil
}

// Purge is intentionally not exposed by MCP. The CLI must gather explicit
// approval before calling this local destructive operation.
func (service *MonitorService) Purge(
	ctx context.Context,
	account domain.AccountID,
	caller domain.Caller,
) (int, error) {
	if err := account.ValidateOpaque(); err != nil {
		return 0, err
	}
	operation, err := domain.NewTargetedOperation(
		"events.purge",
		domain.EffectDestructiveWrite,
		account,
		domain.TargetRef{Kind: domain.TargetLocalQueue, ID: "account-events"},
		struct{}{},
	)
	if err != nil {
		return 0, err
	}
	if err := service.auditPrepared(ctx, operation, caller); err != nil {
		return 0, err
	}
	count, callErr := service.store.Purge(ctx, account)
	auditErr := service.auditExecutedMonitor(
		ctx,
		operation,
		caller,
		callErr,
		&MonitorAudit{
			Stage: "purge", Result: monitorAuditResult(callErr), Count: count,
		},
	)
	return count, errors.Join(callErr, auditErr)
}

func (input MonitorEventListInput) Validate() error {
	if err := input.Account.ValidateOpaque(); err != nil {
		return err
	}
	switch input.State {
	case "", "pending", "dispatched", "acknowledged":
	default:
		return errors.New("event state must be pending, dispatched, or acknowledged")
	}
	if !validMonitorDelivery(input.Delivery, true) {
		return errors.New("event delivery must be queue, notification, or runner")
	}
	if input.Offset < 0 || input.Offset > 10_000 {
		return errors.New("event offset must be between 0 and 10000")
	}
	if input.Limit < 1 || input.Limit > MaxMonitorEventsPage {
		return fmt.Errorf("event limit must be between 1 and %d", MaxMonitorEventsPage)
	}
	return nil
}

func validMonitorDelivery(delivery string, allowEmpty bool) bool {
	if delivery == "" {
		return allowEmpty
	}
	switch delivery {
	case MonitorDeliveryQueue, MonitorDeliveryNotification, MonitorDeliveryRunner:
		return true
	default:
		return false
	}
}

func (input MonitorAcknowledgeInput) Validate() error {
	if err := input.Account.ValidateOpaque(); err != nil {
		return err
	}
	if !strings.HasPrefix(input.EventID, "evt_") || len(input.EventID) != 47 {
		return errors.New("invalid monitor event ID")
	}
	return nil
}

func (service *MonitorService) auditPrepared(
	ctx context.Context,
	operation domain.Operation,
	caller domain.Caller,
) error {
	if err := caller.Validate(); err != nil {
		return err
	}
	return service.audit.Record(ctx, AuditEvent{
		Phase: AuditPhasePrepared, Outcome: AuditOutcomeAllowed,
		Reason: "local_operation_validated", Caller: caller,
		Operation: operation.View(),
	})
}

func (service *MonitorService) auditExecuted(
	ctx context.Context,
	operation domain.Operation,
	caller domain.Caller,
	callErr error,
) error {
	return service.auditExecutedMonitor(ctx, operation, caller, callErr, nil)
}

func (service *MonitorService) auditExecutedMonitor(
	ctx context.Context,
	operation domain.Operation,
	caller domain.Caller,
	callErr error,
	details *MonitorAudit,
) error {
	outcome, reason := AuditOutcomeSuccess, "completed"
	if callErr != nil {
		outcome, reason = AuditOutcomeFailure, "local_store_error"
	}
	return service.audit.Record(context.WithoutCancel(ctx), AuditEvent{
		Phase: AuditPhaseExecuted, Outcome: outcome, Reason: reason,
		Caller: caller, Operation: operation.View(), Monitor: details,
	})
}

func monitorAuditResult(callErr error) string {
	if callErr != nil {
		return "failed"
	}
	return "completed"
}
