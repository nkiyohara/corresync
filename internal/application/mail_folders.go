package application

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/policy"
)

// MailFolderTraversal controls whether FindFolder returns direct children or
// the bounded recursive hierarchy below the selected parent.
type MailFolderTraversal string

const (
	MailFolderTraversalShallow MailFolderTraversal = "shallow"
	MailFolderTraversalDeep    MailFolderTraversal = "deep"
)

// MailFolderListInput selects one bounded page of the mailbox folder
// hierarchy. The parent can be a well-known folder or an opaque discovered ID.
type MailFolderListInput struct {
	Account   domain.AccountID    `json:"account"`
	Parent    MailFolder          `json:"parent"`
	Traversal MailFolderTraversal `json:"traversal"`
	Offset    int                 `json:"offset"`
	Limit     int                 `json:"limit"`
	TimeZone  string              `json:"timeZone"`
}

// MailFolderSummary contains folder metadata only. It never includes item
// content, permissions, rules, delegates, or mailbox identities.
type MailFolderSummary struct {
	ID               string            `json:"id"`
	ChangeKey        string            `json:"changeKey,omitempty"`
	ParentID         string            `json:"parentId,omitempty"`
	DisplayName      string            `json:"displayName,omitempty"`
	Class            string            `json:"class,omitempty"`
	DistinguishedID  string            `json:"distinguishedId,omitempty"`
	ChildFolderCount int               `json:"childFolderCount"`
	TotalItemCount   int               `json:"totalItemCount"`
	UnreadItemCount  int               `json:"unreadItemCount"`
	Provenance       domain.Provenance `json:"provenance,omitempty"`
}

// MailFolderPage is the stable folder-discovery result shared by CLI and MCP.
type MailFolderPage struct {
	Folders          []MailFolderSummary `json:"folders"`
	TotalFolders     int                 `json:"totalFolders"`
	IncludesLastItem bool                `json:"includesLastItem"`
}

// MailFolderReader is implemented by each isolated mail adapter.
type MailFolderReader interface {
	ListMailFolders(context.Context, MailFolderListInput) (MailFolderPage, error)
}

// ListFolders returns bounded folder metadata through the same guard and audit
// boundary as message listing.
func (service *MailService) ListFolders(
	ctx context.Context,
	input MailFolderListInput,
	caller domain.Caller,
) (MailFolderPage, error) {
	if err := input.Validate(); err != nil {
		return MailFolderPage{}, err
	}
	operation, err := domain.NewOperation("mail.folders.list", domain.EffectRead, input.Account, input)
	if err != nil {
		return MailFolderPage{}, fmt.Errorf("create mail folder list operation: %w", err)
	}
	prepared, err := service.guard.Prepare(ctx, operation, caller)
	if err != nil {
		return MailFolderPage{}, err
	}
	if prepared.Decision.Verdict != policy.VerdictAllow {
		return MailFolderPage{}, errors.New("mail folder list operation was not allowed for immediate execution")
	}

	page, callErr := service.folderReader.ListMailFolders(ctx, input)
	outcome := AuditOutcomeSuccess
	reason := "completed"
	if callErr != nil {
		outcome = AuditOutcomeFailure
		reason = "transport_error"
	}
	auditErr := service.guard.audit.Record(context.WithoutCancel(ctx), AuditEvent{
		Phase:     AuditPhaseExecuted,
		Outcome:   outcome,
		Reason:    reason,
		Caller:    caller,
		Operation: operation.View(),
	})
	if callErr != nil || auditErr != nil {
		return MailFolderPage{}, errors.Join(callErr, auditErr)
	}
	if service.provenance.AccountID != "" {
		for index := range page.Folders {
			page.Folders[index].Provenance = service.mailProvenance(page.Folders[index].ID)
		}
	}
	return page, nil
}

// Validate rejects unbounded hierarchy reads and ambiguous folder selectors.
func (input MailFolderListInput) Validate() error {
	if err := input.Account.Validate(); err != nil {
		return err
	}
	switch input.Parent.Kind {
	case MailFolderDistinguished:
		switch strings.ToLower(input.Parent.ID) {
		case "msgfolderroot", "inbox", "archive", "deleteditems", "drafts", "sentitems":
		default:
			return fmt.Errorf("unsupported distinguished mail folder parent %q", input.Parent.ID)
		}
	case MailFolderOpaque:
		if err := validateOpaqueValue("mail folder parent ID", input.Parent.ID); err != nil {
			return err
		}
	default:
		return errors.New("mail folder parent kind is required")
	}
	if input.Traversal != MailFolderTraversalShallow && input.Traversal != MailFolderTraversalDeep {
		return errors.New("mail folder traversal must be shallow or deep")
	}
	if input.Offset < 0 {
		return errors.New("mail folder offset must not be negative")
	}
	if input.Limit < 1 || input.Limit > MaxMailPageSize {
		return fmt.Errorf("mail folder limit must be between 1 and %d", MaxMailPageSize)
	}
	if len(input.TimeZone) > 128 || strings.TrimSpace(input.TimeZone) != input.TimeZone ||
		strings.ContainsAny(input.TimeZone, "\r\n\x00") {
		return errors.New("invalid mail folder time zone")
	}
	return nil
}
