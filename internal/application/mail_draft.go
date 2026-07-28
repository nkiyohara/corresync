package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"unicode/utf8"

	"github.com/nkiyohara/corresync/internal/approval"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/policy"
)

const (
	MaxMailRecipients           = 500
	MaxMailSubjectBytes         = 998
	MaxMailDraftBodyBytes       = 1 << 20
	MaxMailAttachments          = 10
	MaxMailAttachmentBytes      = 2 << 20
	MaxMailAttachmentTotalBytes = 3 << 20
	mailDraftPreviewRunes       = 500
)

// MailComposeMode selects a closed composition shape. Response modes keep the
// exact source item version in the reviewed operation.
type MailComposeMode string

const (
	MailComposeNew      MailComposeMode = "new"
	MailComposeReply    MailComposeMode = "reply"
	MailComposeReplyAll MailComposeMode = "reply_all"
	MailComposeForward  MailComposeMode = "forward"
)

// MailBodyFormat is deliberately limited to the two Exchange body formats.
type MailBodyFormat string

const (
	MailBodyText MailBodyFormat = "text"
	MailBodyHTML MailBodyFormat = "html"
)

// MailFileAttachment is one bounded file attachment embedded in the immutable
// operation. Content is encoded as base64 by encoding/json and is never shown
// in previews or audit records.
type MailFileAttachment struct {
	Name        string `json:"name"`
	ContentType string `json:"contentType,omitempty"`
	Content     []byte `json:"content"`
}

// MailAttachmentReview identifies attachment content without exposing it.
type MailAttachmentReview struct {
	Name        string `json:"name"`
	ContentType string `json:"contentType,omitempty"`
	Bytes       int    `json:"bytes"`
	SHA256      string `json:"sha256"`
}

// MailDraftInput creates a save-only new-message or response draft. It never
// sends mail.
type MailDraftInput struct {
	Account            domain.AccountID     `json:"account"`
	To                 []string             `json:"to,omitempty"`
	CC                 []string             `json:"cc,omitempty"`
	BCC                []string             `json:"bcc,omitempty"`
	Subject            string               `json:"subject,omitempty"`
	Body               string               `json:"body,omitempty"`
	BodyFormat         MailBodyFormat       `json:"bodyFormat,omitempty"`
	ComposeMode        MailComposeMode      `json:"composeMode,omitempty"`
	ReferenceMessageID string               `json:"referenceMessageId,omitempty"`
	ReferenceChangeKey string               `json:"referenceChangeKey,omitempty"`
	Attachments        []MailFileAttachment `json:"attachments,omitempty"`
}

// MailDraft identifies the saved draft returned by OWA.
type MailDraft struct {
	ID         string            `json:"id"`
	ChangeKey  string            `json:"changeKey,omitempty"`
	Provenance domain.Provenance `json:"provenance,omitempty"`
}

// MailReview is safe to show before saving or sending an exact composition.
type MailReview struct {
	To                 []string               `json:"to,omitempty"`
	CC                 []string               `json:"cc,omitempty"`
	BCC                []string               `json:"bcc,omitempty"`
	Subject            string                 `json:"subject,omitempty"`
	BodyPreview        string                 `json:"bodyPreview,omitempty"`
	BodyBytes          int                    `json:"bodyBytes"`
	BodySHA256         string                 `json:"bodySha256"`
	BodyFormat         MailBodyFormat         `json:"bodyFormat"`
	ComposeMode        MailComposeMode        `json:"composeMode"`
	ReferenceMessageID string                 `json:"referenceMessageId,omitempty"`
	ReferenceChangeKey string                 `json:"referenceChangeKey,omitempty"`
	Attachments        []MailAttachmentReview `json:"attachments,omitempty"`
}

// MailDraftAccess represents a completed save or an approval preview.
type MailDraftAccess struct {
	Status  string            `json:"status"`
	Draft   *MailDraft        `json:"draft,omitempty"`
	Review  MailReview        `json:"review"`
	Preview *approval.Preview `json:"preview,omitempty"`
}

// MailDraftWriter is the narrow OWA port for save-only drafts.
type MailDraftWriter interface {
	CreateMailDraft(context.Context, MailDraftInput) (MailDraft, error)
}

// CreateDraft prepares a save-only draft and executes it when policy allows.
func (service *MailService) CreateDraft(
	ctx context.Context,
	input MailDraftInput,
	caller domain.Caller,
) (MailDraftAccess, error) {
	if err := input.Validate(service.maxRecipients); err != nil {
		return MailDraftAccess{}, err
	}
	review := input.Review()
	operation, err := domain.NewTargetedOperation(
		"mail.create_draft",
		domain.EffectReversibleWrite,
		input.Account,
		configuredMailboxTarget(),
		input,
	)
	if err != nil {
		return MailDraftAccess{}, fmt.Errorf("create mail draft operation: %w", err)
	}
	prepared, err := service.guard.Prepare(ctx, operation, caller)
	if err != nil {
		return MailDraftAccess{}, err
	}
	switch prepared.Decision.Verdict {
	case policy.VerdictAllow:
		draft, err := service.executeDraft(ctx, input, caller, operation)
		if err != nil {
			return MailDraftAccess{}, err
		}
		return MailDraftAccess{Status: "completed", Draft: &draft, Review: review}, nil
	case policy.VerdictPreview:
		return MailDraftAccess{Status: "approval_required", Review: review, Preview: prepared.Preview}, nil
	case policy.VerdictDeny:
		return MailDraftAccess{}, errors.New("mail draft operation was denied")
	default:
		return MailDraftAccess{}, errors.New("mail draft operation received an unknown policy verdict")
	}
}

// CommitDraft consumes a caller-bound preview and saves its immutable draft.
func (service *MailService) CommitDraft(
	ctx context.Context,
	token string,
	caller domain.Caller,
) (MailDraftAccess, error) {
	operation, err := service.guard.CommitFor(
		ctx, token, caller, "mail.create_draft", domain.EffectReversibleWrite,
	)
	if err != nil {
		return MailDraftAccess{}, err
	}
	var input MailDraftInput
	if err := operation.DecodePayload(&input); err != nil {
		return MailDraftAccess{}, err
	}
	if err := input.Validate(service.maxRecipients); err != nil {
		return MailDraftAccess{}, err
	}
	draft, err := service.executeDraft(ctx, input, caller, operation)
	if err != nil {
		return MailDraftAccess{}, err
	}
	return MailDraftAccess{Status: "completed", Draft: &draft, Review: input.Review()}, nil
}

func (service *MailService) executeDraft(
	ctx context.Context,
	input MailDraftInput,
	caller domain.Caller,
	operation domain.Operation,
) (MailDraft, error) {
	if err := service.validateExecutionAccount(operation); err != nil {
		return MailDraft{}, err
	}
	draft, callErr := service.draftWriter.CreateMailDraft(ctx, input)
	outcome := AuditOutcomeSuccess
	reason := "completed"
	if callErr != nil {
		outcome = AuditOutcomeFailure
		reason = "transport_error"
		if errors.Is(callErr, ErrWriteOutcomeUnknown) {
			outcome = AuditOutcomeUnknown
			reason = "outcome_unknown"
		}
	}
	auditErr := service.guard.audit.Record(context.WithoutCancel(ctx), AuditEvent{
		Phase:     AuditPhaseExecuted,
		Outcome:   outcome,
		Reason:    reason,
		Caller:    caller,
		Operation: operation.View(),
	})
	if callErr != nil || auditErr != nil {
		return MailDraft{}, errors.Join(callErr, auditErr)
	}
	if service.provenance.AccountID != "" {
		draft.Provenance = service.mailProvenance(draft.ID)
	}
	return draft, nil
}

// Validate enforces configured and absolute recipient/content bounds.
func (input MailDraftInput) Validate(maxRecipients int) error {
	if err := input.Account.Validate(); err != nil {
		return err
	}
	if maxRecipients < 1 || maxRecipients > MaxMailRecipients {
		return errors.New("invalid mail recipient limit")
	}
	count := len(input.To) + len(input.CC) + len(input.BCC)
	if count > maxRecipients {
		return fmt.Errorf("mail draft has %d recipients; maximum is %d", count, maxRecipients)
	}
	mode := input.EffectiveComposeMode()
	switch mode {
	case MailComposeNew:
		if input.ReferenceMessageID != "" || input.ReferenceChangeKey != "" {
			return errors.New("new mail must not include a reference message")
		}
	case MailComposeReply, MailComposeReplyAll:
		if err := validateOpaqueValue("reference message ID", input.ReferenceMessageID); err != nil {
			return err
		}
		if err := validateOpaqueValue("reference message change key", input.ReferenceChangeKey); err != nil {
			return err
		}
		if count != 0 {
			return errors.New("reply recipients are derived from the reference message")
		}
	case MailComposeForward:
		if err := validateOpaqueValue("reference message ID", input.ReferenceMessageID); err != nil {
			return err
		}
		if err := validateOpaqueValue("reference message change key", input.ReferenceChangeKey); err != nil {
			return err
		}
		if count == 0 {
			return errors.New("forward requires at least one recipient")
		}
	default:
		return fmt.Errorf("unsupported mail compose mode %q", input.ComposeMode)
	}
	format := input.EffectiveBodyFormat()
	if format != MailBodyText && format != MailBodyHTML {
		return fmt.Errorf("unsupported mail body format %q", input.BodyFormat)
	}
	for _, recipient := range append(append(append([]string{}, input.To...), input.CC...), input.BCC...) {
		if err := validateMailAddress(recipient); err != nil {
			return err
		}
	}
	if !utf8.ValidString(input.Subject) || len(input.Subject) > MaxMailSubjectBytes ||
		strings.ContainsAny(input.Subject, "\r\n\x00") {
		return errors.New("mail subject is malformed or too large")
	}
	if !utf8.ValidString(input.Body) || len(input.Body) > MaxMailDraftBodyBytes ||
		strings.ContainsRune(input.Body, '\x00') {
		return errors.New("mail draft body is malformed or too large")
	}
	if len(input.Attachments) > MaxMailAttachments {
		return fmt.Errorf("mail draft has %d attachments; maximum is %d", len(input.Attachments), MaxMailAttachments)
	}
	totalAttachmentBytes := 0
	seenAttachmentNames := make(map[string]struct{}, len(input.Attachments))
	for _, attachment := range input.Attachments {
		if !utf8.ValidString(attachment.Name) || attachment.Name == "" || len(attachment.Name) > 255 ||
			strings.TrimSpace(attachment.Name) != attachment.Name || strings.ContainsAny(attachment.Name, "/\\\r\n\x00") {
			return errors.New("mail attachment name is malformed")
		}
		if _, exists := seenAttachmentNames[strings.ToLower(attachment.Name)]; exists {
			return fmt.Errorf("mail attachment %q appears more than once", attachment.Name)
		}
		seenAttachmentNames[strings.ToLower(attachment.Name)] = struct{}{}
		if len(attachment.Content) == 0 || len(attachment.Content) > MaxMailAttachmentBytes {
			return fmt.Errorf("mail attachment %q must contain between 1 and %d bytes", attachment.Name, MaxMailAttachmentBytes)
		}
		if len(attachment.ContentType) > 255 || strings.TrimSpace(attachment.ContentType) != attachment.ContentType ||
			strings.ContainsAny(attachment.ContentType, "\r\n\x00") {
			return fmt.Errorf("mail attachment %q content type is malformed", attachment.Name)
		}
		totalAttachmentBytes += len(attachment.Content)
	}
	if totalAttachmentBytes > MaxMailAttachmentTotalBytes {
		return fmt.Errorf("mail attachments total %d bytes; maximum is %d", totalAttachmentBytes, MaxMailAttachmentTotalBytes)
	}
	if count == 0 && input.Subject == "" && input.Body == "" {
		if mode == MailComposeNew && len(input.Attachments) == 0 {
			return errors.New("mail draft must contain a recipient, subject, body, or attachment")
		}
	}
	return nil
}

func validateMailAddress(value string) error {
	if value == "" || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\r\n\x00") {
		return errors.New("mail recipient address is malformed")
	}
	parsed, err := mail.ParseAddress(value)
	if err != nil || parsed.Address != value {
		return fmt.Errorf("mail recipient %q must be a bare email address", value)
	}
	return nil
}

// Review summarizes content while binding the full body through SHA-256.
func (input MailDraftInput) Review() MailReview {
	preview := input.Body
	if utf8.RuneCountInString(preview) > mailDraftPreviewRunes {
		runes := []rune(preview)
		preview = string(runes[:mailDraftPreviewRunes-1]) + "…"
	}
	digest := sha256.Sum256([]byte(input.Body))
	attachments := make([]MailAttachmentReview, 0, len(input.Attachments))
	for _, attachment := range input.Attachments {
		attachmentDigest := sha256.Sum256(attachment.Content)
		attachments = append(attachments, MailAttachmentReview{
			Name: attachment.Name, ContentType: attachment.ContentType,
			Bytes: len(attachment.Content), SHA256: hex.EncodeToString(attachmentDigest[:]),
		})
	}
	return MailReview{
		To: append([]string(nil), input.To...), CC: append([]string(nil), input.CC...),
		BCC: append([]string(nil), input.BCC...), Subject: input.Subject,
		BodyPreview: preview, BodyBytes: len(input.Body), BodySHA256: hex.EncodeToString(digest[:]),
		BodyFormat: input.EffectiveBodyFormat(), ComposeMode: input.EffectiveComposeMode(),
		ReferenceMessageID: input.ReferenceMessageID, ReferenceChangeKey: input.ReferenceChangeKey,
		Attachments: attachments,
	}
}

// EffectiveComposeMode applies the backward-compatible new-message default.
func (input MailDraftInput) EffectiveComposeMode() MailComposeMode {
	if input.ComposeMode == "" {
		return MailComposeNew
	}
	return input.ComposeMode
}

// EffectiveBodyFormat applies the backward-compatible plain-text default.
func (input MailDraftInput) EffectiveBodyFormat() MailBodyFormat {
	if input.BodyFormat == "" {
		return MailBodyText
	}
	return input.BodyFormat
}
