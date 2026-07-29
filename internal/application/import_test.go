package application

import (
	"context"
	"testing"

	"github.com/nkiyohara/corresync/internal/domain"
)

type importScannerStub struct {
	calls int
	plan  ImportPlan
}

func (stub *importScannerStub) Scan(
	context.Context,
	ImportScanInput,
) (ImportPlan, error) {
	stub.calls++
	return stub.plan, nil
}

func (*importScannerStub) Purge(context.Context, domain.AccountID) error {
	return nil
}

func TestImportServiceRejectsUnapprovedOrRelativeSourceBeforeScanner(t *testing.T) {
	t.Parallel()
	stub := &importScannerStub{}
	service, err := NewImportService(stub)
	if err != nil {
		t.Fatal(err)
	}
	valid := ImportScanInput{
		Account: "acc_00000000000000000000000000000001",
		Source:  "/synthetic/archive.mbox",
		Format:  ImportFormatMBOX,
	}
	for _, input := range []ImportScanInput{
		valid,
		func() ImportScanInput {
			value := valid
			value.PrivacyApproved = true
			value.Source = "relative/archive.mbox"
			return value
		}(),
		func() ImportScanInput {
			value := valid
			value.PrivacyApproved = true
			value.Format = "unknown"
			return value
		}(),
	} {
		if _, err := service.Scan(t.Context(), input); err == nil {
			t.Fatalf("Scan(%#v) succeeded", input)
		}
	}
	if stub.calls != 0 {
		t.Fatalf("scanner calls = %d", stub.calls)
	}
}

func TestImportPlanRejectsInstructionTrustOrInconsistentCounts(t *testing.T) {
	t.Parallel()
	plan := ImportPlan{
		Version: 1,
		ID:      "imp_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Account: "acc_00000000000000000000000000000001",
		Source:  "/synthetic/archive.eml", Format: ImportFormatEML,
		ContentTrust: "agent_instructions",
	}
	if err := plan.Validate(); err == nil {
		t.Fatal("instruction-bearing trust label was accepted")
	}
	plan.ContentTrust = "untrusted_data"
	plan.StagedItems = 1
	if err := plan.Validate(); err == nil {
		t.Fatal("inconsistent item counts were accepted")
	}
}
