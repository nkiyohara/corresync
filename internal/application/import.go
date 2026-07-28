package application

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/nkiyohara/corresync/internal/domain"
)

const (
	MaxImportPlanItems    = 10_000
	MaxImportSourceBytes  = int64(512 << 20)
	MaxImportItemBytes    = 16 << 20
	MaxImportDesktopHints = 256
)

// ImportFormat is a closed read-only archive/configuration scanner selection.
type ImportFormat string

const (
	ImportFormatAuto        ImportFormat = "auto"
	ImportFormatMixed       ImportFormat = "mixed"
	ImportFormatMBOX        ImportFormat = "mbox"
	ImportFormatMaildir     ImportFormat = "maildir"
	ImportFormatEML         ImportFormat = "eml"
	ImportFormatICS         ImportFormat = "ics"
	ImportFormatVCF         ImportFormat = "vcf"
	ImportFormatPST         ImportFormat = "pst"
	ImportFormatOLM         ImportFormat = "olm"
	ImportFormatThunderbird ImportFormat = "thunderbird"
)

// ImportScanInput names one explicit local source and account-owned staging
// boundary. PrivacyApproved is human consent to read that path, not consent to
// authenticate or upload.
type ImportScanInput struct {
	Account         domain.AccountID `json:"account"`
	Source          string           `json:"source"`
	Format          ImportFormat     `json:"format"`
	PrivacyApproved bool             `json:"privacyApproved"`
}

// ImportSourceProvenance retains where one opaque staged item came from.
type ImportSourceProvenance struct {
	Path    string       `json:"path"`
	Format  ImportFormat `json:"format"`
	Ordinal int          `json:"ordinal"`
}

// ImportItem is content-free staged metadata. ObjectSHA256 names a private
// content-addressed object; the raw object is never returned through this API.
type ImportItem struct {
	Kind         string                 `json:"kind"`
	ObjectSHA256 string                 `json:"objectSha256"`
	DedupeKey    string                 `json:"dedupeKey"`
	Status       string                 `json:"status"`
	MessageID    string                 `json:"messageId,omitempty"`
	OriginalDate string                 `json:"originalDate,omitempty"`
	Flags        []string               `json:"flags,omitempty"`
	Folder       string                 `json:"folder,omitempty"`
	CalendarUID  string                 `json:"calendarUid,omitempty"`
	RecurrenceID string                 `json:"recurrenceId,omitempty"`
	ContactUID   string                 `json:"contactUid,omitempty"`
	Source       ImportSourceProvenance `json:"source"`
	Degradations []domain.Degradation   `json:"degradations,omitempty"`
}

// ImportDesktopHint is a sanitized account/config observation. It intentionally
// has no credential, cookie, token, or source-config copy.
type ImportDesktopHint struct {
	Application string `json:"application"`
	AccountType string `json:"accountType,omitempty"`
	Host        string `json:"host,omitempty"`
	Identity    string `json:"identity,omitempty"`
}

// ImportDecisionGate records an intentionally unsupported archive format
// before a parser implementation or dependency has been approved.
type ImportDecisionGate struct {
	Format ImportFormat `json:"format"`
	Reason string       `json:"reason"`
	Gates  []string     `json:"gates"`
}

// ImportPlan is a deterministic, local-only scan result. ContentTrust prevents
// callers from treating imported text as instructions.
type ImportPlan struct {
	Version        int                  `json:"version"`
	ID             string               `json:"id"`
	Account        domain.AccountID     `json:"account"`
	Source         string               `json:"source"`
	Format         ImportFormat         `json:"format"`
	ContentTrust   string               `json:"contentTrust"`
	ExistingPlan   bool                 `json:"existingPlan"`
	Items          []ImportItem         `json:"items"`
	DesktopHints   []ImportDesktopHint  `json:"desktopHints,omitempty"`
	DecisionGates  []ImportDecisionGate `json:"decisionGates,omitempty"`
	Degradations   []domain.Degradation `json:"degradations,omitempty"`
	StagedItems    int                  `json:"stagedItems"`
	DuplicateItems int                  `json:"duplicateItems"`
	Conflicts      int                  `json:"conflicts"`
	BytesRead      int64                `json:"bytesRead"`
}

// ImportScanner stages one deterministic read-only plan.
type ImportScanner interface {
	Scan(context.Context, ImportScanInput) (ImportPlan, error)
	Purge(context.Context, domain.AccountID) error
}

// ImportService keeps validation independent from the filesystem adapter.
type ImportService struct {
	scanner ImportScanner
}

func NewImportService(scanner ImportScanner) (*ImportService, error) {
	if scanner == nil {
		return nil, errors.New("import scanner is required")
	}
	return &ImportService{scanner: scanner}, nil
}

// Scan validates the explicit privacy boundary before any scanner call.
func (service *ImportService) Scan(
	ctx context.Context,
	input ImportScanInput,
) (ImportPlan, error) {
	if err := input.Validate(); err != nil {
		return ImportPlan{}, err
	}
	plan, err := service.scanner.Scan(ctx, input)
	if err != nil {
		return ImportPlan{}, err
	}
	if err := plan.Validate(); err != nil {
		return ImportPlan{}, fmt.Errorf("validate import plan: %w", err)
	}
	return plan, nil
}

// Purge removes only the selected account's Corresync-owned import staging.
func (service *ImportService) Purge(
	ctx context.Context,
	account domain.AccountID,
) error {
	if err := account.ValidateOpaque(); err != nil {
		return err
	}
	return service.scanner.Purge(ctx, account)
}

func (input ImportScanInput) Validate() error {
	if err := input.Account.ValidateOpaque(); err != nil {
		return err
	}
	if !input.PrivacyApproved {
		return errors.New(
			"import scanning requires explicit approval to read the selected local path",
		)
	}
	if input.Source == "" || !filepath.IsAbs(input.Source) ||
		filepath.Clean(input.Source) != input.Source ||
		len(input.Source) > 4096 ||
		strings.ContainsAny(input.Source, "\r\n\x00") {
		return errors.New("import source must be one clean absolute local path")
	}
	switch input.Format {
	case ImportFormatAuto,
		ImportFormatMixed,
		ImportFormatMBOX,
		ImportFormatMaildir,
		ImportFormatEML,
		ImportFormatICS,
		ImportFormatVCF,
		ImportFormatPST,
		ImportFormatOLM,
		ImportFormatThunderbird:
		return nil
	default:
		return fmt.Errorf("unsupported import format %q", input.Format)
	}
}

func (plan ImportPlan) Validate() error {
	if plan.Version != 1 || len(plan.ID) != 68 ||
		!strings.HasPrefix(plan.ID, "imp_") ||
		!validImportDigest(strings.TrimPrefix(plan.ID, "imp_")) {
		return errors.New("import plan identity is invalid")
	}
	if err := plan.Account.ValidateOpaque(); err != nil {
		return err
	}
	if plan.Source == "" || !filepath.IsAbs(plan.Source) ||
		filepath.Clean(plan.Source) != plan.Source ||
		len(plan.Source) > 4096 ||
		strings.ContainsAny(plan.Source, "\r\n\x00") {
		return errors.New("import plan source is malformed")
	}
	if plan.ContentTrust != "untrusted_data" {
		return errors.New("import plan content trust is invalid")
	}
	if !validImportPlanFormat(plan.Format) {
		return errors.New("import plan format is invalid")
	}
	if len(plan.Items) > MaxImportPlanItems ||
		len(plan.DesktopHints) > MaxImportDesktopHints ||
		len(plan.DecisionGates) > 8 ||
		len(plan.Degradations) > 64 {
		return errors.New("import plan is unbounded")
	}
	if plan.StagedItems < 0 || plan.DuplicateItems < 0 ||
		plan.Conflicts < 0 ||
		plan.StagedItems+plan.DuplicateItems != len(plan.Items) ||
		plan.Conflicts > plan.StagedItems ||
		plan.BytesRead < 0 || plan.BytesRead > MaxImportSourceBytes {
		return errors.New("import plan counts are inconsistent")
	}
	stagedItems := 0
	duplicateItems := 0
	conflicts := 0
	for _, item := range plan.Items {
		if err := validateImportItem(item); err != nil {
			return err
		}
		switch item.Status {
		case "staged":
			stagedItems++
		case "duplicate":
			duplicateItems++
		case "conflict":
			stagedItems++
			conflicts++
		}
	}
	if plan.StagedItems != stagedItems ||
		plan.DuplicateItems != duplicateItems ||
		plan.Conflicts != conflicts {
		return errors.New("import plan status counts are inconsistent")
	}
	for _, degradation := range plan.Degradations {
		if err := degradation.Validate(); err != nil {
			return err
		}
	}
	for _, hint := range plan.DesktopHints {
		if hint.Application == "" || len(hint.Application) > 64 ||
			len(hint.AccountType) > 64 || len(hint.Host) > 253 ||
			len(hint.Identity) > 320 ||
			strings.ContainsAny(
				hint.Application+hint.AccountType+hint.Host+hint.Identity,
				"\r\n\x00",
			) {
			return errors.New("import desktop hint is malformed")
		}
	}
	for _, gate := range plan.DecisionGates {
		if gate.Format != ImportFormatPST && gate.Format != ImportFormatOLM ||
			gate.Reason == "" || len(gate.Reason) > 512 ||
			strings.ContainsAny(gate.Reason, "\r\n\x00") ||
			len(gate.Gates) == 0 || len(gate.Gates) > 8 {
			return errors.New("import decision gate is malformed")
		}
		for _, decision := range gate.Gates {
			if decision == "" || len(decision) > 128 ||
				strings.ContainsAny(decision, "\r\n\x00") {
				return errors.New("import decision gate is malformed")
			}
		}
	}
	return nil
}

func validateImportItem(item ImportItem) error {
	switch item.Kind {
	case "mail", "event", "contact":
	default:
		return errors.New("import item kind is invalid")
	}
	if !validImportDigest(item.ObjectSHA256) ||
		!validImportDigest(item.DedupeKey) {
		return errors.New("import item digest is invalid")
	}
	switch item.Status {
	case "staged", "duplicate", "conflict":
	default:
		return errors.New("import item status is invalid")
	}
	if item.Source.Path == "" || len(item.Source.Path) > 4096 ||
		!filepath.IsAbs(item.Source.Path) ||
		filepath.Clean(item.Source.Path) != item.Source.Path ||
		strings.ContainsAny(item.Source.Path, "\r\n\x00") ||
		!validImportItemFormat(item.Source.Format) ||
		item.Source.Ordinal < 1 {
		return errors.New("import item provenance is invalid")
	}
	if len(item.Flags) > 32 || len(item.Degradations) > 16 {
		return errors.New("import item metadata is unbounded")
	}
	for _, degradation := range item.Degradations {
		if err := degradation.Validate(); err != nil {
			return err
		}
	}
	for _, value := range []string{
		item.MessageID,
		item.OriginalDate,
		item.Folder,
		item.CalendarUID,
		item.RecurrenceID,
		item.ContactUID,
	} {
		if len(value) > 4096 || strings.ContainsAny(value, "\r\n\x00") {
			return errors.New("import item metadata is malformed")
		}
	}
	for _, flag := range item.Flags {
		if flag == "" || len(flag) > 64 ||
			strings.ContainsAny(flag, "\r\n\x00") {
			return errors.New("import item flag is malformed")
		}
	}
	return nil
}

func validImportPlanFormat(format ImportFormat) bool {
	switch format {
	case ImportFormatMixed,
		ImportFormatMBOX,
		ImportFormatMaildir,
		ImportFormatEML,
		ImportFormatICS,
		ImportFormatVCF,
		ImportFormatPST,
		ImportFormatOLM,
		ImportFormatThunderbird:
		return true
	case ImportFormatAuto:
		return false
	default:
		return false
	}
}

func validImportItemFormat(format ImportFormat) bool {
	switch format {
	case ImportFormatMBOX,
		ImportFormatMaildir,
		ImportFormatEML,
		ImportFormatICS,
		ImportFormatVCF:
		return true
	case ImportFormatAuto,
		ImportFormatMixed,
		ImportFormatPST,
		ImportFormatOLM,
		ImportFormatThunderbird:
		return false
	default:
		return false
	}
}

func validImportDigest(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
