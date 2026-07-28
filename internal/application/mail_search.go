package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/policy"
)

const (
	// MaxMailSearchPageSize bounds every provider search adapter.
	MaxMailSearchPageSize = 50
	// MaxMailSearchQueryBytes prevents an agent from turning a provider query
	// into an unbounded request payload while retaining useful searches.
	MaxMailSearchQueryBytes = 1024
)

// MailSearchInput is a read-only search contract. Query uses the selected
// provider's user-facing language; it is not an arbitrary protocol action.
type MailSearchInput struct {
	Account  domain.AccountID `json:"account"`
	Folder   MailFolder       `json:"folder"`
	Query    string           `json:"query"`
	Offset   int              `json:"offset"`
	Limit    int              `json:"limit"`
	TimeZone string           `json:"timeZone"`
}

// Search returns bounded message metadata through policy and audit. Audit
// events retain only the operation digest, never the private search query.
func (service *MailService) Search(
	ctx context.Context,
	input MailSearchInput,
	caller domain.Caller,
) (MailPage, error) {
	if err := input.Validate(); err != nil {
		return MailPage{}, err
	}
	operation, err := domain.NewOperation("mail.search", domain.EffectRead, input.Account, input)
	if err != nil {
		return MailPage{}, fmt.Errorf("create mail search operation: %w", err)
	}
	prepared, err := service.guard.Prepare(ctx, operation, caller)
	if err != nil {
		return MailPage{}, err
	}
	if prepared.Decision.Verdict != policy.VerdictAllow {
		return MailPage{}, errors.New("mail search operation was not allowed for immediate execution")
	}

	page, callErr := service.searcher.SearchMessages(ctx, input)
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

// Validate rejects malformed search input before policy or network access.
func (input MailSearchInput) Validate() error {
	if err := (MailListInput{
		Account: input.Account, Folder: input.Folder, Offset: input.Offset,
		Limit: input.Limit, TimeZone: input.TimeZone,
	}).Validate(); err != nil {
		return err
	}
	if input.Limit > MaxMailSearchPageSize {
		return fmt.Errorf("mail search limit must be between 1 and %d", MaxMailSearchPageSize)
	}
	if input.Query == "" || strings.TrimSpace(input.Query) != input.Query {
		return errors.New("mail search query must not be empty or have surrounding whitespace")
	}
	if len(input.Query) > MaxMailSearchQueryBytes || !utf8.ValidString(input.Query) ||
		strings.ContainsAny(input.Query, "\r\n\x00") {
		return fmt.Errorf("mail search query must be valid UTF-8 without CR, LF, or NUL and at most %d bytes", MaxMailSearchQueryBytes)
	}
	return nil
}
