package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/nkiyohara/corresync/internal/approval"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/policy"
)

// ErrExactDraftSendUnavailable reports a provider route that cannot make the
// reviewed draft version an atomic precondition of submission.
var ErrExactDraftSendUnavailable = errors.New("exact-version draft send is unavailable")

// MailDraftSendInput identifies one exact saved draft version. The draft
// content is read from the selected account; callers never reconstruct it.
type MailDraftSendInput struct {
	Account        domain.AccountID `json:"account"`
	DraftID        string           `json:"draftId"`
	DraftChangeKey string           `json:"draftChangeKey"`
}

// MailDraftAttachmentSnapshot is bounded metadata covered by the immutable
// preview. The provider's version precondition binds the attachment bytes.
type MailDraftAttachmentSnapshot struct {
	Name        string `json:"name,omitempty"`
	ContentType string `json:"contentType,omitempty"`
	Bytes       int    `json:"bytes"`
	Inline      bool   `json:"inline"`
}

// MailDraftSnapshot is the provider-neutral content of one versioned draft.
// It is held only long enough to construct the approval review.
type MailDraftSnapshot struct {
	ID          string                        `json:"id"`
	ChangeKey   string                        `json:"changeKey"`
	To          []string                      `json:"to,omitempty"`
	CC          []string                      `json:"cc,omitempty"`
	BCC         []string                      `json:"bcc,omitempty"`
	Subject     string                        `json:"subject,omitempty"`
	Body        string                        `json:"body,omitempty"`
	BodyFormat  MailBodyFormat                `json:"bodyFormat"`
	Attachments []MailDraftAttachmentSnapshot `json:"attachments,omitempty"`
}

// MailDraftSendReview exposes bounded content while binding the complete
// normalized body by hash and every attachment by versioned metadata.
type MailDraftSendReview struct {
	DraftID        string                        `json:"draftId"`
	DraftChangeKey string                        `json:"draftChangeKey"`
	To             []string                      `json:"to,omitempty"`
	CC             []string                      `json:"cc,omitempty"`
	BCC            []string                      `json:"bcc,omitempty"`
	Subject        string                        `json:"subject,omitempty"`
	BodyPreview    string                        `json:"bodyPreview,omitempty"`
	BodyBytes      int                           `json:"bodyBytes"`
	BodySHA256     string                        `json:"bodySha256"`
	BodyFormat     MailBodyFormat                `json:"bodyFormat"`
	Attachments    []MailDraftAttachmentSnapshot `json:"attachments,omitempty"`
}

// MailDraftSendAccess is either the exact approval preview or a completed
// provider-native transition from Drafts to Sent.
type MailDraftSendAccess struct {
	Status  string              `json:"status"`
	Sent    *MailSendResult     `json:"sent,omitempty"`
	Review  MailDraftSendReview `json:"review"`
	Preview *approval.Preview   `json:"preview,omitempty"`
}

// MailDraftSender is implemented only by adapters whose remote contract can
// bind submission to the exact version inspected for the preview.
type MailDraftSender interface {
	GetMailDraftSnapshot(context.Context, MailDraftSendInput) (MailDraftSnapshot, error)
	SendMailDraft(context.Context, MailDraftSendInput) (MailSendResult, error)
}

type mailDraftSendPayload struct {
	Input  MailDraftSendInput  `json:"input"`
	Review MailDraftSendReview `json:"review"`
}

// SendDraft reads one exact saved draft and prepares its mandatory external
// write preview. It never submits the draft.
func (service *MailService) SendDraft(
	ctx context.Context,
	input MailDraftSendInput,
	caller domain.Caller,
) (MailDraftSendAccess, error) {
	if err := input.Validate(); err != nil {
		return MailDraftSendAccess{}, err
	}
	preflight, err := domain.NewTargetedOperation(
		"mail.send_draft",
		domain.EffectExternalWrite,
		input.Account,
		configuredMailboxTarget(),
		input,
	)
	if err != nil {
		return MailDraftSendAccess{}, fmt.Errorf("create saved draft send preflight: %w", err)
	}
	if err := service.guard.requirePreviewPreparation(ctx, preflight, caller); err != nil {
		return MailDraftSendAccess{}, err
	}
	if err := service.validateRoutedAccount(input.Account); err != nil {
		return MailDraftSendAccess{}, err
	}
	if service.draftSender == nil {
		return MailDraftSendAccess{}, ErrExactDraftSendUnavailable
	}
	snapshot, err := service.draftSender.GetMailDraftSnapshot(ctx, input)
	if err != nil {
		return MailDraftSendAccess{}, err
	}
	if err := snapshot.Validate(input, service.maxRecipients); err != nil {
		return MailDraftSendAccess{}, fmt.Errorf("validate saved draft snapshot: %w", err)
	}
	review := snapshot.Review()
	payload := mailDraftSendPayload{Input: input, Review: review}
	operation, err := domain.NewTargetedOperation(
		"mail.send_draft",
		domain.EffectExternalWrite,
		input.Account,
		configuredMailboxTarget(),
		payload,
	)
	if err != nil {
		return MailDraftSendAccess{}, fmt.Errorf("create saved draft send operation: %w", err)
	}
	prepared, err := service.guard.Prepare(ctx, operation, caller)
	if err != nil {
		return MailDraftSendAccess{}, err
	}
	switch prepared.Decision.Verdict {
	case policy.VerdictPreview:
		return MailDraftSendAccess{
			Status: "approval_required", Review: review, Preview: prepared.Preview,
		}, nil
	case policy.VerdictDeny:
		return MailDraftSendAccess{}, errors.New("saved draft send operation was denied")
	case policy.VerdictAllow:
		return MailDraftSendAccess{}, errors.New(
			"saved draft send policy attempted to bypass mandatory preview",
		)
	default:
		return MailDraftSendAccess{}, errors.New(
			"saved draft send operation received an unknown policy verdict",
		)
	}
}

// CommitSendDraft consumes one caller-bound preview and asks the provider to
// submit exactly the reviewed draft version once.
func (service *MailService) CommitSendDraft(
	ctx context.Context,
	token string,
	caller domain.Caller,
) (MailDraftSendAccess, error) {
	operation, err := service.guard.CommitFor(
		ctx, token, caller, "mail.send_draft", domain.EffectExternalWrite,
	)
	if err != nil {
		return MailDraftSendAccess{}, err
	}
	var payload mailDraftSendPayload
	if err := operation.DecodePayload(&payload); err != nil {
		return MailDraftSendAccess{}, err
	}
	if err := payload.Input.Validate(); err != nil {
		return MailDraftSendAccess{}, err
	}
	if err := payload.Review.Validate(payload.Input, service.maxRecipients); err != nil {
		return MailDraftSendAccess{}, err
	}
	if service.draftSender == nil {
		return MailDraftSendAccess{}, ErrExactDraftSendUnavailable
	}
	sent, err := service.executeSendDraft(ctx, payload.Input, caller, operation)
	if err != nil {
		return MailDraftSendAccess{}, err
	}
	return MailDraftSendAccess{
		Status: "sent", Sent: &sent, Review: payload.Review,
	}, nil
}

func (service *MailService) executeSendDraft(
	ctx context.Context,
	input MailDraftSendInput,
	caller domain.Caller,
	operation domain.Operation,
) (MailSendResult, error) {
	if err := service.validateExecutionAccount(operation); err != nil {
		return MailSendResult{}, err
	}
	sent, callErr := service.draftSender.SendMailDraft(ctx, input)
	outcome, reason := mailWriteAuditOutcome(callErr)
	auditErr := service.guard.audit.Record(context.WithoutCancel(ctx), AuditEvent{
		Phase: AuditPhaseExecuted, Outcome: outcome, Reason: reason,
		Caller: caller, Operation: operation.View(),
	})
	if callErr != nil || auditErr != nil {
		return MailSendResult{}, errors.Join(callErr, auditErr)
	}
	if service.provenance.AccountID != "" {
		sent.Provenance = service.mailProvenance(sent.ID)
	}
	return sent, nil
}

func (input MailDraftSendInput) Validate() error {
	if err := input.Account.Validate(); err != nil {
		return err
	}
	if err := validateOpaqueValue("draft ID", input.DraftID); err != nil {
		return err
	}
	return validateOpaqueValue("draft change key", input.DraftChangeKey)
}

func (snapshot MailDraftSnapshot) Validate(
	input MailDraftSendInput,
	maxRecipients int,
) error {
	if snapshot.ID != input.DraftID || snapshot.ChangeKey != input.DraftChangeKey {
		return errors.New("saved draft changed or returned a different identity")
	}
	composition := MailSendInput{
		Account: input.Account, To: snapshot.To, CC: snapshot.CC, BCC: snapshot.BCC,
		Subject: snapshot.Subject, Body: snapshot.Body, BodyFormat: snapshot.BodyFormat,
	}
	if err := composition.Validate(maxRecipients); err != nil {
		return err
	}
	return validateDraftAttachmentSnapshots(snapshot.Attachments)
}

func validateDraftAttachmentSnapshots(attachments []MailDraftAttachmentSnapshot) error {
	if len(attachments) > MaxMailAttachments {
		return fmt.Errorf(
			"saved draft has %d attachments; maximum is %d",
			len(attachments),
			MaxMailAttachments,
		)
	}
	total := 0
	for _, attachment := range attachments {
		if !utf8.ValidString(attachment.Name) || len(attachment.Name) > 255 ||
			strings.ContainsAny(attachment.Name, "\r\n\x00") ||
			len(attachment.ContentType) > 255 ||
			strings.TrimSpace(attachment.ContentType) != attachment.ContentType ||
			strings.ContainsAny(attachment.ContentType, "\r\n\x00") ||
			attachment.Bytes < 0 || attachment.Bytes > MaxMailAttachmentBytes {
			return errors.New("saved draft attachment metadata is malformed or too large")
		}
		total += attachment.Bytes
	}
	if total > MaxMailAttachmentTotalBytes {
		return fmt.Errorf(
			"saved draft attachments total %d bytes; maximum is %d",
			total,
			MaxMailAttachmentTotalBytes,
		)
	}
	return nil
}

func (snapshot MailDraftSnapshot) Review() MailDraftSendReview {
	bodyPreview, bodySHA256 := reviewMailBody(snapshot.Body)
	return MailDraftSendReview{
		DraftID: snapshot.ID, DraftChangeKey: snapshot.ChangeKey,
		To:      append([]string(nil), snapshot.To...),
		CC:      append([]string(nil), snapshot.CC...),
		BCC:     append([]string(nil), snapshot.BCC...),
		Subject: snapshot.Subject, BodyPreview: bodyPreview,
		BodyBytes: len(snapshot.Body), BodySHA256: bodySHA256,
		BodyFormat:  snapshot.BodyFormat,
		Attachments: append([]MailDraftAttachmentSnapshot(nil), snapshot.Attachments...),
	}
}

func (review MailDraftSendReview) Validate(
	input MailDraftSendInput,
	maxRecipients int,
) error {
	if review.DraftID != input.DraftID || review.DraftChangeKey != input.DraftChangeKey {
		return errors.New("saved draft review identity does not match its input")
	}
	composition := MailSendInput{
		Account: input.Account, To: review.To, CC: review.CC, BCC: review.BCC,
		Subject: review.Subject, Body: review.BodyPreview, BodyFormat: review.BodyFormat,
	}
	if err := composition.Validate(maxRecipients); err != nil {
		return err
	}
	if review.BodyBytes < 0 || review.BodyBytes > MaxMailDraftBodyBytes ||
		!utf8.ValidString(review.BodyPreview) ||
		utf8.RuneCountInString(review.BodyPreview) > mailDraftPreviewRunes ||
		strings.ContainsRune(review.BodyPreview, '\x00') {
		return errors.New("saved draft body review has an invalid size")
	}
	if len(review.BodySHA256) != 2*sha256.Size {
		return errors.New("saved draft body review has an invalid SHA-256 digest")
	}
	if _, err := hex.DecodeString(review.BodySHA256); err != nil {
		return errors.New("saved draft body review has an invalid SHA-256 digest")
	}
	return validateDraftAttachmentSnapshots(review.Attachments)
}
