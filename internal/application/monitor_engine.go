package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nkiyohara/corresync/internal/domain"
)

const (
	monitorRecoveryLimit = 1000
	monitorPageSize      = 100
)

var ErrMonitorRecoveryOverflow = errors.New(
	"monitor cursor recovery exceeded 1000 inbox messages; " +
		"the inspected window was committed and older uninspected messages were not emitted",
)

// MonitorEngine performs one bounded poll. Scheduling and authenticated
// session ownership stay in the daemon adapter.
type MonitorEngine struct {
	store    MonitorEventStore
	audit    AuditRecorder
	notifier MonitorNotifier
	runner   MonitorRunner
	now      func() time.Time
}

func NewMonitorEngine(
	store MonitorEventStore,
	audit AuditRecorder,
	notifier MonitorNotifier,
	runner MonitorRunner,
) (*MonitorEngine, error) {
	if store == nil || audit == nil {
		return nil, errors.New("monitor store and audit recorder are required")
	}
	return &MonitorEngine{
		store: store, audit: audit, notifier: notifier, runner: runner,
		now: func() time.Time { return time.Now().UTC() },
	}, nil
}

// Poll recovers from the previous durable cursor, filters metadata, commits the
// cursor and deduplication state atomically, then releases only configured
// fields to the selected sink.
func (engine *MonitorEngine) Poll(
	ctx context.Context,
	policy MonitorPolicy,
	mail *MailService,
) error {
	if !policy.Mode.Collects() {
		return nil
	}
	if mail == nil {
		return errors.New("monitoring requires an authenticated mail service")
	}
	state, err := engine.store.Status(ctx, policy.Account)
	if err != nil {
		return err
	}
	var pendingDrainErr error
	if state.Initialized {
		pendingDrainErr = engine.releasePending(ctx, policy)
		state, err = engine.store.Status(ctx, policy.Account)
		if err != nil {
			return errors.Join(pendingDrainErr, err)
		}
	}
	detections, cursor, recoveryOverflow, err := engine.scanMailbox(
		ctx,
		policy,
		mail,
		state,
	)
	if err != nil {
		storeErr := engine.store.MarkScanFailure(
			context.WithoutCancel(ctx),
			policy.Account,
			engine.now(),
			"cursor_recovery_failed",
		)
		auditErr := engine.recordPipeline(
			ctx,
			policy,
			"detection",
			"cursor_recovery",
			nil,
			"",
			"failed",
			0,
		)
		return errors.Join(pendingDrainErr, err, storeErr, auditErr)
	}
	now := engine.now()
	if !state.Initialized {
		if err := engine.recordPipeline(
			ctx, policy, "detection", "baseline", nil, "", "completed", len(detections),
		); err != nil {
			return err
		}
	} else {
		for _, detection := range detections {
			if err := engine.recordPipeline(
				ctx, policy, "detection", "observed", nil, "", "completed", 1,
			); err != nil {
				return err
			}
			if err := engine.recordPipeline(
				ctx, policy, "filter", detection.FilterReason, nil, "", "completed", 1,
			); err != nil {
				return err
			}
		}
	}
	result, err := engine.store.CommitScan(ctx, MonitorScan{
		Account: policy.Account, Cursor: cursor, Bootstrap: !state.Initialized,
		Delivery: monitorDelivery(policy.Mode), ObservedAt: now,
		RecoveryOverflow: recoveryOverflow,
		RetainAfter:      now.Add(-policy.Retention), Detections: detections,
	})
	if err != nil {
		stage := "detection"
		destination := ""
		if policy.Mode.Queues() {
			stage = "queue"
			destination = "local_queue"
		}
		auditErr := engine.recordPipeline(
			ctx, policy, stage, "matched", nil, destination, "failed",
			len(detections),
		)
		storeErr := engine.store.MarkScanFailure(
			context.WithoutCancel(ctx),
			policy.Account,
			now,
			"scan_commit_failed",
		)
		return errors.Join(pendingDrainErr, err, storeErr, auditErr)
	}
	if !state.Initialized {
		return pendingDrainErr
	}
	if policy.Mode.Queues() {
		if len(result.Events) > 0 {
			if err := engine.recordPipeline(
				ctx, policy, "queue", "matched", nil, "local_queue", "completed",
				len(result.Events),
			); err != nil {
				return err
			}
		}
	}
	var overflowErr error
	if recoveryOverflow {
		overflowErr = errors.Join(
			ErrMonitorRecoveryOverflow,
			engine.recordPipeline(
				ctx,
				policy,
				"detection",
				"cursor_recovery_overflow",
				nil,
				"",
				"failed",
				len(detections),
			),
		)
	}
	switch policy.Mode {
	case domain.MonitorOff, domain.MonitorQueue:
		return errors.Join(pendingDrainErr, overflowErr)
	case domain.MonitorNotify:
		return errors.Join(
			pendingDrainErr,
			overflowErr,
			engine.notifyPending(ctx, policy),
		)
	case domain.MonitorAgent:
		return errors.Join(
			pendingDrainErr,
			overflowErr,
			engine.dispatchPending(ctx, policy),
		)
	}
	return errors.Join(pendingDrainErr, overflowErr)
}

func (engine *MonitorEngine) releasePending(
	ctx context.Context,
	policy MonitorPolicy,
) error {
	switch policy.Mode {
	case domain.MonitorNotify:
		return engine.notifyPending(ctx, policy)
	case domain.MonitorAgent:
		return engine.dispatchPending(ctx, policy)
	case domain.MonitorOff, domain.MonitorQueue:
		return nil
	default:
		return errors.New("monitor policy has an unknown delivery mode")
	}
}

func (engine *MonitorEngine) scanMailbox(
	ctx context.Context,
	policy MonitorPolicy,
	mail *MailService,
	state MonitorQueueStatus,
) ([]MonitorDetection, string, bool, error) {
	first, firstCursor, firstOverflow, err := engine.readMailboxWindow(
		ctx,
		policy,
		mail,
		state,
	)
	if err != nil {
		return nil, "", false, err
	}
	second, secondCursor, secondOverflow, err := engine.readMailboxWindow(
		ctx,
		policy,
		mail,
		state,
	)
	if err != nil {
		return nil, "", false, err
	}
	if firstCursor != secondCursor || firstOverflow != secondOverflow ||
		!sameMonitorWindow(first, second) {
		return nil, "", false, errors.New(
			"mailbox changed during cursor recovery; no cursor was advanced",
		)
	}
	detections := make([]MonitorDetection, 0, len(second))
	for _, message := range second {
		matched, reason := matchesMonitorPolicy(policy, message)
		detections = append(detections, MonitorDetection{
			Account: policy.Account, AccountAlias: policy.Alias,
			Provider:       message.Provenance.Provider,
			SourceObjectID: message.ID,
			Sender: MailAddress{
				Name:    boundedMonitorText(message.From.Name, 512),
				Address: boundedMonitorText(message.From.Address, 320),
			},
			Subject:        boundedMonitorText(message.Subject, 2048),
			ReceivedAt:     boundedMonitorText(message.ReceivedAt, 128),
			Importance:     boundedMonitorText(message.Importance, 32),
			HasAttachments: message.HasAttachments,
			Matched:        matched, FilterReason: reason,
		})
	}
	return detections, secondCursor, secondOverflow, nil
}

func (engine *MonitorEngine) readMailboxWindow(
	ctx context.Context,
	policy MonitorPolicy,
	mail *MailService,
	state MonitorQueueStatus,
) ([]MailSummary, string, bool, error) {
	caller := domain.Caller{Surface: "daemon", Instance: "monitor"}
	messages := make([]MailSummary, 0, monitorPageSize)
	newCursor := state.Cursor
	recovered := !state.Initialized
	reachedEnd := false
	for offset := 0; offset < monitorRecoveryLimit; offset += monitorPageSize {
		page, err := mail.List(ctx, MailListInput{
			Account: policy.Account,
			Folder:  MailFolder{Kind: MailFolderDistinguished, ID: "inbox"},
			Offset:  offset, Limit: monitorPageSize, TimeZone: "UTC",
		}, caller)
		if err != nil {
			return nil, "", false, err
		}
		if offset == 0 {
			newCursor = ""
			if len(page.Messages) > 0 {
				newCursor = monitorCursor(
					page.Messages[0].Provenance.Provider,
					page.Messages[0].ID,
				)
			}
		}
		for _, message := range page.Messages {
			messageCursor := monitorCursor(message.Provenance.Provider, message.ID)
			if state.Initialized && messageCursor == state.Cursor {
				recovered = true
				break
			}
			messages = append(messages, message)
		}
		if recovered {
			break
		}
		if page.IncludesLastItem || len(page.Messages) == 0 {
			reachedEnd = true
			break
		}
	}
	// Only exhausting the complete bounded window without reaching the end is
	// an overflow. A deleted cursor in a short or empty mailbox is a complete
	// inspection and therefore a normal re-baseline.
	recoveryOverflow := state.Initialized &&
		!recovered &&
		!reachedEnd &&
		len(messages) >= monitorRecoveryLimit
	if newCursor == "" {
		empty := sha256.Sum256([]byte("empty:" + string(policy.Account)))
		newCursor = hex.EncodeToString(empty[:])
	}
	return messages, newCursor, recoveryOverflow, nil
}

func sameMonitorWindow(left, right []MailSummary) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].ID != right[index].ID ||
			left[index].ChangeKey != right[index].ChangeKey ||
			left[index].Provenance.Provider != right[index].Provenance.Provider {
			return false
		}
	}
	return true
}

func matchesMonitorPolicy(
	policy MonitorPolicy,
	message MailSummary,
) (bool, string) {
	if policy.Address != "" &&
		strings.EqualFold(message.From.Address, policy.Address) {
		return false, "self_message"
	}
	if len(policy.SenderDomains) > 0 {
		at := strings.LastIndexByte(message.From.Address, '@')
		domainPart := ""
		if at >= 0 {
			domainPart = strings.ToLower(message.From.Address[at+1:])
		}
		matched := false
		for _, allowed := range policy.SenderDomains {
			if domainPart == allowed || strings.HasSuffix(domainPart, "."+allowed) {
				matched = true
				break
			}
		}
		if !matched {
			return false, "sender_domain"
		}
	}
	if len(policy.SubjectContains) > 0 {
		subject := strings.ToLower(message.Subject)
		matched := false
		for _, fragment := range policy.SubjectContains {
			if strings.Contains(subject, strings.ToLower(fragment)) {
				matched = true
				break
			}
		}
		if !matched {
			return false, "subject"
		}
	}
	if policy.ImportantOnly && !strings.EqualFold(message.Importance, "high") {
		return false, "importance"
	}
	return true, "matched"
}

func (engine *MonitorEngine) notifyPending(
	ctx context.Context,
	policy MonitorPolicy,
) error {
	state, err := engine.store.Status(ctx, policy.Account)
	if err != nil {
		return err
	}
	page, err := engine.store.List(ctx, MonitorEventListInput{
		Account:  policy.Account,
		State:    "pending",
		Delivery: MonitorDeliveryNotification,
		Limit:    MaxMonitorEventsPage,
	})
	if err != nil || page.Total == 0 {
		return err
	}
	if engine.notifier == nil {
		failureErr := engine.store.MarkDispatchFailure(
			context.WithoutCancel(ctx),
			policy.Account,
			engine.now(),
			"notification_unavailable",
		)
		auditErr := engine.recordPipeline(
			ctx, policy, "notification", "matched",
			policy.NotificationFields, policy.NotificationTarget,
			"failed", page.Total,
		)
		return errors.Join(
			errors.New("configured notification adapter is unavailable"),
			failureErr,
			auditErr,
		)
	}
	if inQuietHours(policy, engine.now()) {
		return engine.recordPipeline(
			ctx, policy, "notification", "matched",
			policy.NotificationFields, policy.NotificationTarget,
			"quiet_hours", page.Total,
		)
	}
	if blocked := dispatchBlockReason(policy, state, engine.now()); blocked != "" {
		return engine.recordPipeline(
			ctx, policy, "notification", "matched",
			policy.NotificationFields, policy.NotificationTarget,
			blocked, page.Total,
		)
	}
	remaining := policy.RateLimitHour - state.DispatchedLastHour
	releaseCount := min(remaining, len(page.Events))
	released := 0
	for _, event := range page.Events[:releaseCount] {
		release := MonitorRelease{
			Destination: policy.NotificationTarget,
			Fields:      append([]string(nil), policy.NotificationFields...),
			Event:       releaseEvent(event, policy.NotificationFields),
		}
		if err := engine.notifier.Notify(ctx, release); err != nil {
			failureErr := engine.store.MarkDispatchFailure(
				context.WithoutCancel(ctx), policy.Account, engine.now(),
				"notification_failed",
			)
			auditErr := engine.recordPipeline(
				ctx, policy, "notification", "matched",
				policy.NotificationFields, policy.NotificationTarget,
				"failed", 1,
			)
			return errors.Join(
				err,
				failureErr,
				auditErr,
			)
		}
		if err := engine.store.MarkDispatch(
			context.WithoutCancel(ctx),
			policy.Account,
			MonitorDeliveryNotification,
			[]string{event.ID},
			engine.now(),
		); err != nil {
			auditErr := engine.recordPipeline(
				ctx, policy, "notification", "matched",
				policy.NotificationFields, policy.NotificationTarget,
				"failed", 1,
			)
			return errors.Join(err, auditErr)
		}
		released++
		if err := engine.recordPipeline(
			ctx, policy, "notification", "matched",
			policy.NotificationFields, policy.NotificationTarget,
			"completed", 1,
		); err != nil {
			return err
		}
	}
	if released < page.Total {
		result := "batch_limited"
		if remaining <= released {
			result = "rate_limited"
		}
		return engine.recordPipeline(
			ctx, policy, "notification", "matched",
			policy.NotificationFields, policy.NotificationTarget,
			result, page.Total-released,
		)
	}
	return nil
}

func (engine *MonitorEngine) dispatchPending(
	ctx context.Context,
	policy MonitorPolicy,
) error {
	if engine.runner == nil {
		return errors.New("configured monitor runner is unavailable")
	}
	now := engine.now()
	state, err := engine.store.Status(ctx, policy.Account)
	if err != nil {
		return err
	}
	if inQuietHours(policy, now) {
		return engine.recordPipeline(
			ctx, policy, "runner", "matched", policy.RunnerFields,
			policy.RunnerTarget, "quiet_hours", state.Pending,
		)
	}
	if blocked := dispatchBlockReason(policy, state, now); blocked != "" {
		return engine.recordPipeline(
			ctx, policy, "runner", "matched", policy.RunnerFields,
			policy.RunnerTarget, blocked, state.Pending,
		)
	}
	remaining := policy.RateLimitHour - state.DispatchedLastHour
	limit := min(remaining, MaxMonitorEventsPage)
	page, err := engine.store.List(ctx, MonitorEventListInput{
		Account: policy.Account, State: "pending",
		Delivery: MonitorDeliveryRunner, Limit: limit,
	})
	if err != nil || len(page.Events) == 0 {
		return err
	}
	events := make([]map[string]any, 0, len(page.Events))
	ids := make([]string, 0, len(page.Events))
	for _, event := range page.Events {
		events = append(events, releaseEvent(event, policy.RunnerFields))
		ids = append(ids, event.ID)
	}
	request := MonitorRunnerRequest{
		SchemaVersion: 1, Account: policy.Account,
		Trust: MonitorTrustMarker, AllowedEffects: []string{"read"},
		Destination: policy.RunnerTarget, Egress: policy.RunnerEgress,
		Fields: append([]string(nil), policy.RunnerFields...), Events: events,
	}
	if err := engine.runner.Run(ctx, request); err != nil {
		_ = engine.store.MarkDispatchFailure(
			context.WithoutCancel(ctx), policy.Account, now, "runner_failed",
		)
		_ = engine.recordPipeline(
			ctx, policy, "runner", "matched", policy.RunnerFields,
			policy.RunnerTarget, "failed", len(events),
		)
		return err
	}
	if err := engine.store.MarkDispatch(
		context.WithoutCancel(ctx),
		policy.Account,
		MonitorDeliveryRunner,
		ids,
		now,
	); err != nil {
		return err
	}
	return engine.recordPipeline(
		ctx, policy, "runner", "matched", policy.RunnerFields,
		policy.RunnerTarget, "completed", len(events),
	)
}

func monitorDelivery(mode domain.MonitorMode) string {
	switch mode {
	case domain.MonitorOff:
		return ""
	case domain.MonitorNotify:
		return MonitorDeliveryNotification
	case domain.MonitorQueue:
		return MonitorDeliveryQueue
	case domain.MonitorAgent:
		return MonitorDeliveryRunner
	}
	return ""
}

func dispatchAllowed(
	policy MonitorPolicy,
	status MonitorQueueStatus,
	now time.Time,
) bool {
	return dispatchBlockReason(policy, status, now) == ""
}

func dispatchBlockReason(
	policy MonitorPolicy,
	status MonitorQueueStatus,
	now time.Time,
) string {
	if status.CircuitOpenUntil != nil && now.Before(*status.CircuitOpenUntil) {
		return "circuit_open"
	}
	if status.DispatchedLastHour >= policy.RateLimitHour {
		return "rate_limited"
	}
	if status.LastDispatchAt != nil &&
		now.Before(status.LastDispatchAt.Add(policy.Debounce)) {
		return "debounced"
	}
	return ""
}

func inQuietHours(policy MonitorPolicy, now time.Time) bool {
	if policy.QuietStart == "" {
		return false
	}
	location, err := time.LoadLocation(policy.QuietTimeZone)
	if err != nil {
		return true
	}
	local := now.In(location).Format("15:04")
	if policy.QuietStart < policy.QuietEnd {
		return local >= policy.QuietStart && local < policy.QuietEnd
	}
	return local >= policy.QuietStart || local < policy.QuietEnd
}

func releaseEvent(event MonitorEvent, fields []string) map[string]any {
	released := make(map[string]any, len(fields))
	for _, field := range fields {
		switch field {
		case "account":
			released[field] = event.Account
		case "event_id":
			released[field] = event.ID
		case "sender":
			released[field] = event.Sender
		case "subject":
			released[field] = event.Subject
		case "received_at":
			released[field] = event.ReceivedAt
		case "importance":
			released[field] = event.Importance
		case "has_attachments":
			released[field] = event.HasAttachments
		case "trust":
			released[field] = event.Trust
		}
	}
	return released
}

func boundedMonitorText(value string, maximum int) string {
	value = strings.ReplaceAll(value, "\x00", "")
	if len(value) <= maximum {
		return value
	}
	end := maximum
	for end > 0 && !utf8.ValidString(value[:end]) {
		end--
	}
	return value[:end]
}

func monitorCursor(provider domain.ProviderID, sourceObjectID string) string {
	digest := sha256.Sum256([]byte(string(provider) + "\x00" + sourceObjectID))
	return hex.EncodeToString(digest[:])
}

func (engine *MonitorEngine) recordPipeline(
	ctx context.Context,
	policy MonitorPolicy,
	stage string,
	filter string,
	fields []string,
	destination string,
	result string,
	count int,
) error {
	sortedFields := append([]string(nil), fields...)
	sort.Strings(sortedFields)
	auditDestination := monitorAuditDestination(destination)
	effect := domain.EffectRead
	switch stage {
	case "queue":
		effect = domain.EffectReversibleWrite
	case "notification", "runner":
		effect = domain.EffectExternalWrite
	}
	operation, err := domain.NewOperation(
		"monitor."+stage,
		effect,
		policy.Account,
		struct {
			Filter      string   `json:"filter,omitempty"`
			Fields      []string `json:"fields,omitempty"`
			Destination string   `json:"destination,omitempty"`
			Result      string   `json:"result"`
			Count       int      `json:"count"`
		}{filter, sortedFields, auditDestination, result, count},
	)
	if err != nil {
		return fmt.Errorf("create monitor audit operation: %w", err)
	}
	outcome := AuditOutcomeSuccess
	if result == "failed" {
		outcome = AuditOutcomeFailure
	}
	return engine.audit.Record(context.WithoutCancel(ctx), AuditEvent{
		Phase: AuditPhaseExecuted, Outcome: outcome, Reason: result,
		Caller:    domain.Caller{Surface: "daemon", Instance: "monitor"},
		Operation: operation.View(),
		Monitor: &MonitorAudit{
			Stage: stage, Filter: filter, Fields: sortedFields,
			Destination: auditDestination, Result: result, Count: count,
		},
	})
}

func monitorAuditDestination(destination string) string {
	if destination == "" || destination == "desktop" || destination == "local_queue" {
		return destination
	}
	digest := sha256.Sum256([]byte(destination))
	return "runner_" + hex.EncodeToString(digest[:8])
}
