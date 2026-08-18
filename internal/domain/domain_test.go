package domain

import (
	"strings"
	"testing"
)

func TestEffectClassification(t *testing.T) {
	t.Parallel()

	tests := []struct {
		effect  Effect
		valid   bool
		isWrite bool
	}{
		{EffectRead, true, false},
		{EffectSensitiveRead, true, false},
		{EffectReversibleWrite, true, true},
		{EffectExternalWrite, true, true},
		{EffectDestructiveWrite, true, true},
		{Effect("future_effect"), false, false},
	}

	for _, test := range tests {
		t.Run(string(test.effect), func(t *testing.T) {
			t.Parallel()
			if got := test.effect.Validate() == nil; got != test.valid {
				t.Fatalf("Validate() success = %v, want %v", got, test.valid)
			}
			if got := test.effect.IsWrite(); got != test.isWrite {
				t.Fatalf("IsWrite() = %v, want %v", got, test.isWrite)
			}
		})
	}
}

func TestNewOperationSnapshotsPayload(t *testing.T) {
	t.Parallel()

	payload := map[string]string{"subject": "Original"}
	operation, err := NewOperation("mail.send", EffectExternalWrite, "work", payload)
	if err != nil {
		t.Fatalf("NewOperation() error = %v", err)
	}
	payload["subject"] = "Modified"

	var decoded map[string]string
	if err := operation.DecodePayload(&decoded); err != nil {
		t.Fatalf("DecodePayload() error = %v", err)
	}
	if decoded["subject"] != "Original" {
		t.Fatalf("payload was not snapshotted: %#v", decoded)
	}

	view := operation.View()
	if view.Name != "mail.send" || view.Effect != EffectExternalWrite || view.Account != "work" {
		t.Fatalf("unexpected view: %+v", view)
	}
	if len(view.Digest) != 2*32 {
		t.Fatalf("digest length = %d, want 64", len(view.Digest))
	}
}

func TestNewOperationRejectsInvalidBoundaries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		effect  Effect
		account AccountID
		payload any
	}{
		{"Mail.Send", EffectExternalWrite, "work", nil},
		{"mail.send", Effect("unknown"), "work", nil},
		{"mail.send", EffectExternalWrite, "", nil},
		{"mail.send", EffectExternalWrite, " work", nil},
		{"mail.send", EffectExternalWrite, "work\nother", nil},
		{"mail.send", EffectExternalWrite, "work", func() {}},
		{"mail.send", EffectExternalWrite, "work", strings.Repeat("x", maximumPayloadBytes+1)},
	}

	for _, test := range tests {
		if _, err := NewOperation(test.name, test.effect, test.account, test.payload); err == nil {
			t.Fatalf("NewOperation(%q, %q, %q) unexpectedly succeeded", test.name, test.effect, test.account)
		}
	}
}

func TestTargetedOperationDigestBindsExactTarget(t *testing.T) {
	t.Parallel()

	payload := map[string]string{"subject": "Quarterly plan"}
	workCalendar, err := NewTargetedOperation(
		"calendar.create",
		EffectExternalWrite,
		"acc_00112233445566778899aabbccddeeff",
		TargetRef{Kind: TargetCalendar, ID: "calendar-primary"},
		payload,
	)
	if err != nil {
		t.Fatal(err)
	}
	sharedCalendar, err := NewTargetedOperation(
		"calendar.create",
		EffectExternalWrite,
		"acc_00112233445566778899aabbccddeeff",
		TargetRef{Kind: TargetCalendar, ID: "calendar-shared"},
		payload,
	)
	if err != nil {
		t.Fatal(err)
	}
	otherAccount, err := NewTargetedOperation(
		"calendar.create",
		EffectExternalWrite,
		"acc_ffeeddccbbaa99887766554433221100",
		TargetRef{Kind: TargetCalendar, ID: "calendar-primary"},
		payload,
	)
	if err != nil {
		t.Fatal(err)
	}
	workView := workCalendar.View()
	if workView.Target == nil ||
		workView.Target.Kind != TargetCalendar ||
		workView.Target.ID != "calendar-primary" {
		t.Fatalf("targeted view = %+v", workView)
	}
	if workView.Digest == sharedCalendar.View().Digest {
		t.Fatal("different calendars produced the same operation digest")
	}
	if workView.Digest == otherAccount.View().Digest {
		t.Fatal("different accounts produced the same operation digest")
	}
}

func TestMessagingTargetKindsRemainClosedAndBounded(t *testing.T) {
	t.Parallel()

	valid := []TargetRef{
		{Kind: TargetWorkspace, ID: "workspace-1"},
		{Kind: TargetConversation, ID: "11:workspace-114:conversation-1"},
		{Kind: TargetMessage, ID: "11:workspace-114:conversation-19:message-1"},
	}
	for _, target := range valid {
		if err := target.Validate(); err != nil {
			t.Fatalf("TargetRef(%+v).Validate() error = %v", target, err)
		}
	}
	if err := (TargetRef{Kind: TargetMessage, ID: strings.Repeat("x", 3*4096+17)}).Validate(); err == nil {
		t.Fatal("oversized messaging target unexpectedly succeeded")
	}
}

func TestTaskTargetHasASeparateCompositeBound(t *testing.T) {
	t.Parallel()

	if err := (TargetRef{
		Kind: TargetTask,
		ID:   strings.Repeat("x", maximumTargetIDBytes),
	}).Validate(); err != nil {
		t.Fatalf("maximum task target rejected: %v", err)
	}
	if err := (TargetRef{
		Kind: TargetMailbox,
		ID:   strings.Repeat("x", 4097),
	}).Validate(); err == nil {
		t.Fatal("task composite bound leaked into mailbox targets")
	}
}

func TestGeneratedAccountIDsAreOpaqueAndUnique(t *testing.T) {
	t.Parallel()

	first, err := NewAccountID()
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewAccountID()
	if err != nil {
		t.Fatal(err)
	}
	if err := first.ValidateOpaque(); err != nil {
		t.Fatalf("generated account ID rejected: %v", err)
	}
	if first == second {
		t.Fatalf("generated duplicate account ID %q", first)
	}
	for _, invalid := range []AccountID{
		"work",
		"acc_work",
		"acc_00112233445566778899AABBCCDDEEFF",
		"acc_00112233445566778899aabbccddee",
	} {
		if err := invalid.ValidateOpaque(); err == nil {
			t.Fatalf("non-opaque account ID %q accepted", invalid)
		}
	}
}

func TestAccountAliasRejectsOpaqueAccountIDForm(t *testing.T) {
	t.Parallel()

	if err := AccountAlias("personal").Validate(); err != nil {
		t.Fatalf("ordinary alias rejected: %v", err)
	}
	if err := AccountAlias(
		"acc_00112233445566778899aabbccddeeff",
	).Validate(); err == nil {
		t.Fatal("opaque account ID form was accepted as an alias")
	}
}

func TestCapabilitiesAndProvenanceValidation(t *testing.T) {
	t.Parallel()

	if err := (Capabilities{Mail: true, Calendar: true, OnlineMeeting: "teams"}).Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (Capabilities{OnlineMeeting: "arbitrary-provider-value"}).Validate(); err == nil {
		t.Fatal("open-ended online meeting capability accepted")
	}
	if err := (Provenance{
		AccountID:      "acc_00112233445566778899aabbccddeeff",
		Provider:       ProviderMicrosoftOWA,
		MailboxID:      "primary",
		SourceObjectID: "opaque-item",
	}).Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (Provenance{
		AccountID:  "acc_00112233445566778899aabbccddeeff",
		Provider:   ProviderMicrosoftOWA,
		MailboxID:  "primary",
		CalendarID: "calendar",
	}).Validate(); err == nil {
		t.Fatal("ambiguous provenance accepted")
	}
}

func TestCallerValidation(t *testing.T) {
	t.Parallel()

	if err := (Caller{Surface: "mcp", Instance: "codex:session-1"}).Validate(); err != nil {
		t.Fatalf("valid caller rejected: %v", err)
	}
	for _, caller := range []Caller{
		{},
		{Surface: "mcp", Instance: ""},
		{Surface: "mcp\n", Instance: "session"},
		{Surface: "cli", Instance: " session"},
	} {
		if err := caller.Validate(); err == nil {
			t.Fatalf("invalid caller unexpectedly accepted: %+v", caller)
		}
	}
}
