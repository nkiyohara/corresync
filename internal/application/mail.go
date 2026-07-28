package application

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/nkiyohara/owa-bridge/internal/domain"
	"github.com/nkiyohara/owa-bridge/internal/policy"
)

const MaxMailPageSize = 100

// MailFolderKind distinguishes named folders from discovered opaque IDs.
type MailFolderKind string

const (
	MailFolderDistinguished MailFolderKind = "distinguished"
	MailFolderOpaque        MailFolderKind = "opaque"
)

// MailFolder is a protocol-independent folder selection.
type MailFolder struct {
	Kind MailFolderKind `json:"kind"`
	ID   string         `json:"id"`
}

// MailListInput is shared exactly by CLI, MCP, policy, and the transport port.
type MailListInput struct {
	Account  domain.AccountID `json:"account"`
	Folder   MailFolder       `json:"folder"`
	Offset   int              `json:"offset"`
	Limit    int              `json:"limit"`
	TimeZone string           `json:"timeZone"`
}

// MailAddress is listing metadata, not a complete contact.
type MailAddress struct {
	Name    string `json:"name,omitempty"`
	Address string `json:"address,omitempty"`
}

// MailSummary intentionally excludes the message body and attachment content.
type MailSummary struct {
	ID             string            `json:"id"`
	ChangeKey      string            `json:"changeKey,omitempty"`
	Subject        string            `json:"subject,omitempty"`
	From           MailAddress       `json:"from,omitempty"`
	ReceivedAt     string            `json:"receivedAt,omitempty"`
	Importance     string            `json:"importance,omitempty"`
	IsRead         bool              `json:"isRead"`
	HasAttachments bool              `json:"hasAttachments"`
	Provenance     domain.Provenance `json:"provenance,omitempty"`
}

// MailPage is the stable output contract exposed by both adapters.
type MailPage struct {
	Messages         []MailSummary `json:"messages"`
	TotalItemsInView int           `json:"totalItemsInView"`
	IncludesLastItem bool          `json:"includesLastItem"`
}

// MailReader is the application port implemented by the OWA adapter.
type MailReader interface {
	ListMessages(context.Context, MailListInput) (MailPage, error)
}

// MailSearcher performs a bounded, read-only mailbox search.
type MailSearcher interface {
	SearchMessages(context.Context, MailSearchInput) (MailPage, error)
}

// MailPort combines the metadata and explicit-body reads required by one mail
// service without exposing transport-specific operations.
type MailPort interface {
	MailReader
	MailSearcher
	MailFolderReader
	MailBodyReader
	MailAttachmentReader
	MailDraftWriter
	MailSender
	MailMover
	MailReadStateWriter
	MailDeleter
}

// MailOptions applies configured limits at the application boundary.
type MailOptions struct {
	MaxRecipients int
	Provenance    domain.Provenance
}

// MailService applies policy and audit around mail use cases.
type MailService struct {
	guard            *Guard
	reader           MailReader
	searcher         MailSearcher
	folderReader     MailFolderReader
	bodyReader       MailBodyReader
	attachmentReader MailAttachmentReader
	draftWriter      MailDraftWriter
	sender           MailSender
	mover            MailMover
	readState        MailReadStateWriter
	deleter          MailDeleter
	maxRecipients    int
	provenance       domain.Provenance
}

// NewMailService requires the shared guard and a transport port.
func NewMailService(guard *Guard, reader MailPort, options MailOptions) (*MailService, error) {
	if guard == nil {
		return nil, errors.New("mail guard is required")
	}
	if reader == nil {
		return nil, errors.New("mail reader is required")
	}
	if options.MaxRecipients < 1 || options.MaxRecipients > MaxMailRecipients {
		return nil, fmt.Errorf("max mail recipients must be between 1 and %d", MaxMailRecipients)
	}
	return &MailService{
		guard: guard, reader: reader, searcher: reader, folderReader: reader,
		bodyReader: reader, attachmentReader: reader, draftWriter: reader,
		sender: reader, mover: reader, readState: reader, deleter: reader,
		maxRecipients: options.MaxRecipients, provenance: options.Provenance,
	}, nil
}

// List returns metadata only through the shared policy and audit boundary.
func (service *MailService) List(
	ctx context.Context,
	input MailListInput,
	caller domain.Caller,
) (MailPage, error) {
	if err := input.Validate(); err != nil {
		return MailPage{}, err
	}
	operation, err := domain.NewOperation("mail.list", domain.EffectRead, input.Account, input)
	if err != nil {
		return MailPage{}, fmt.Errorf("create mail list operation: %w", err)
	}
	prepared, err := service.guard.Prepare(ctx, operation, caller)
	if err != nil {
		return MailPage{}, err
	}
	if prepared.Decision.Verdict != policy.VerdictAllow {
		return MailPage{}, errors.New("mail list operation was not allowed for immediate execution")
	}

	page, callErr := service.reader.ListMessages(ctx, input)
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
		return MailPage{}, errors.Join(callErr, auditErr)
	}
	service.applyMailPage(&page)
	return page, nil
}

// Validate rejects malformed application input before policy or network access.
func (input MailListInput) Validate() error {
	if err := input.Account.Validate(); err != nil {
		return err
	}
	if err := validateMessageFolder(input.Folder); err != nil {
		return err
	}
	if input.Offset < 0 {
		return errors.New("mail offset must not be negative")
	}
	if input.Limit < 1 || input.Limit > MaxMailPageSize {
		return fmt.Errorf("mail limit must be between 1 and %d", MaxMailPageSize)
	}
	if len(input.TimeZone) > 128 || strings.TrimSpace(input.TimeZone) != input.TimeZone ||
		strings.ContainsAny(input.TimeZone, "\r\n\x00") {
		return errors.New("invalid mail time zone")
	}
	return nil
}

func validateMessageFolder(folder MailFolder) error {
	switch folder.Kind {
	case MailFolderDistinguished:
		switch strings.ToLower(folder.ID) {
		case "inbox", "archive", "deleteditems", "drafts", "sentitems":
		default:
			return fmt.Errorf("unsupported distinguished mail folder %q", folder.ID)
		}
	case MailFolderOpaque:
		if err := validateOpaqueValue("mail folder ID", folder.ID); err != nil {
			return err
		}
	default:
		return errors.New("mail folder kind is required")
	}
	return nil
}

func validateOpaqueValue(name, value string) error {
	if value == "" {
		return fmt.Errorf("%s must not be empty", name)
	}
	if len(value) > 4096 || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\r\n\x00") {
		return fmt.Errorf("%s is malformed", name)
	}
	return nil
}

func configuredMailboxTarget() domain.TargetRef {
	return domain.TargetRef{Kind: domain.TargetMailbox, ID: "configured-mailbox"}
}

func (service *MailService) mailProvenance(sourceObjectID string) domain.Provenance {
	provenance := service.provenance
	if provenance.AccountID == "" {
		return domain.Provenance{}
	}
	provenance.SourceObjectID = sourceObjectID
	return provenance
}

func (service *MailService) applyMailPage(page *MailPage) {
	if service.provenance.AccountID == "" {
		return
	}
	for index := range page.Messages {
		page.Messages[index].Provenance = service.mailProvenance(page.Messages[index].ID)
	}
}
