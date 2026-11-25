package application

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nkiyohara/corresync/internal/approval"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/policy"
)

func replyDraftInput(mode MailComposeMode) MailDraftInput {
	return MailDraftInput{
		Account: "work", Body: "Synthetic reply", ComposeMode: mode,
		ReferenceMessageID: "source-1", ReferenceChangeKey: "source-version-1",
	}
}

func TestMailReplyModesRejectExplicitRecipientsWithModeGuidance(t *testing.T) {
	t.Parallel()

	tests := []struct {
		mode    MailComposeMode
		mutate  func(*MailDraftInput)
		want    []string
		without string
	}{
		{
			mode:   MailComposeReply,
			mutate: func(input *MailDraftInput) { input.CC = []string{"carol@example.invalid"} },
			want: []string{
				"reply recipients are derived from the reference message",
				"reply_all", "--mode reply-all", "other To/Cc participants",
			},
		},
		{
			mode:   MailComposeReply,
			mutate: func(input *MailDraftInput) { input.To = []string{"bob@example.invalid"} },
			want:   []string{"reply_all", "only the sender"},
		},
		{
			mode:    MailComposeReplyAll,
			mutate:  func(input *MailDraftInput) { input.BCC = []string{"dave@example.invalid"} },
			want:    []string{"reply_all recipients are derived from the reference message", "omit explicit recipients"},
			without: "--mode reply-all",
		},
	}
	for _, test := range tests {
		t.Run(string(test.mode), func(t *testing.T) {
			t.Parallel()
			input := replyDraftInput(test.mode)
			test.mutate(&input)
			err := input.Validate(MaxMailRecipients)
			if err == nil {
				t.Fatal("Validate() accepted explicit recipients for a derived reply")
			}
			for _, want := range test.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not contain %q", err, want)
				}
			}
			if test.without != "" && strings.Contains(err.Error(), test.without) {
				t.Errorf("error %q unexpectedly contains %q", err, test.without)
			}
		})
	}
}

func knownSource(participants ...string) func(*MailDraftAddressing) {
	return func(addressing *MailDraftAddressing) {
		addressing.SourceParticipants = participants
		addressing.SourceParticipantsKnown = true
	}
}

func TestMailDraftAddressingReview(t *testing.T) {
	t.Parallel()

	omitted := func(count int) *int { return &count }
	tests := []struct {
		name     string
		mode     MailComposeMode
		reported *MailDraftAddressing
		source   func(*MailDraftAddressing)
		status   MailAddressingStatus
		omitted  *int
	}{
		{name: "route does not report", mode: MailComposeReply, status: MailAddressingUnresolved},
		{
			name: "ordinary reply drops other participants", mode: MailComposeReply,
			reported: &MailDraftAddressing{To: []string{"alice@example.invalid"}, Subject: "Re: Plan"},
			source:   knownSource("bob@example.invalid", "Carol@Example.invalid", "alice@example.invalid"),
			status:   MailAddressingResolved, omitted: omitted(2),
		},
		{
			name: "reply-all keeps To and Cc", mode: MailComposeReplyAll,
			reported: &MailDraftAddressing{
				To: []string{"alice@example.invalid", "bob@example.invalid"},
				CC: []string{"carol@example.invalid"}, Subject: "Re: Plan",
			},
			source: knownSource("BOB@example.invalid", "carol@example.invalid"),
			status: MailAddressingResolved, omitted: omitted(0),
		},
		{
			name: "duplicate source participants count once", mode: MailComposeReply,
			reported: &MailDraftAddressing{To: []string{"alice@example.invalid"}},
			source:   knownSource("bob@example.invalid", "Bob@example.invalid"),
			status:   MailAddressingResolved, omitted: omitted(1),
		},
		{
			name: "unverified source participants stay unknown", mode: MailComposeReply,
			reported: &MailDraftAddressing{To: []string{"alice@example.invalid"}},
			status:   MailAddressingResolved,
		},
		{
			name: "new mail has no source comparison", mode: MailComposeNew,
			reported: &MailDraftAddressing{To: []string{"alice@example.invalid"}},
			source:   knownSource("bob@example.invalid"),
			status:   MailAddressingResolved,
		},
		{
			name: "forward has no source comparison", mode: MailComposeForward,
			reported: &MailDraftAddressing{To: []string{"erin@example.invalid"}, Subject: "Fwd: Plan"},
			source:   knownSource("bob@example.invalid"),
			status:   MailAddressingResolved,
		},
		{
			name: "verified empty lists stay resolved", mode: MailComposeNew,
			reported: &MailDraftAddressing{Subject: "Notes to self"},
			status:   MailAddressingResolved,
		},
		{
			name: "malformed provider address is unresolved", mode: MailComposeReply,
			reported: &MailDraftAddressing{To: []string{"alice@example.invalid\nBcc: x@example.invalid"}},
			status:   MailAddressingUnresolved,
		},
		{
			name: "malformed source participant leaves count unknown", mode: MailComposeReply,
			reported: &MailDraftAddressing{To: []string{"alice@example.invalid"}},
			source:   knownSource("bob@example.invalid", ""),
			status:   MailAddressingResolved,
		},
		{
			name: "oversized subject is unresolved", mode: MailComposeNew,
			reported: &MailDraftAddressing{Subject: strings.Repeat("s", maxReviewSubjectBytes+1)},
			status:   MailAddressingUnresolved,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if test.source != nil {
				test.source(test.reported)
			}
			review := draftAddressingReview(test.mode, test.reported)
			if review.Status != test.status {
				t.Fatalf("status = %q, want %q", review.Status, test.status)
			}
			if test.status == MailAddressingUnresolved {
				if review.Resolved != nil {
					t.Fatalf("unresolved review exposed addressing: %+v", review.Resolved)
				}
				return
			}
			resolved := review.Resolved
			if resolved == nil || resolved.To == nil || resolved.CC == nil {
				t.Fatalf("resolved review must carry non-nil lists: %+v", resolved)
			}
			switch {
			case test.omitted == nil && resolved.SourceParticipantsNotIncluded != nil:
				t.Fatalf("omitted = %d, want unknown", *resolved.SourceParticipantsNotIncluded)
			case test.omitted != nil && (resolved.SourceParticipantsNotIncluded == nil ||
				*resolved.SourceParticipantsNotIncluded != *test.omitted):
				t.Fatalf("omitted = %v, want %d", resolved.SourceParticipantsNotIncluded, *test.omitted)
			}
		})
	}
}

func TestMailDraftAccessDistinguishesUnresolvedFromEmpty(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		addressing *MailDraftAddressing
		want       string
		absent     []string
	}{
		{
			name: "unresolved", want: `"addressing":{"status":"unresolved"}`,
			absent: []string{`"resolved"`},
		},
		{
			name:       "verified empty",
			addressing: &MailDraftAddressing{Subject: "Synthetic"},
			want:       `"addressing":{"status":"resolved","resolved":{"to":[],"cc":[],"subject":"Synthetic"}}`,
		},
		{
			name: "never exposes bcc",
			addressing: &MailDraftAddressing{
				To: []string{"alice@example.invalid"}, Subject: "Synthetic",
			},
			want:   `"resolved":{"to":["alice@example.invalid"],"cc":[],"subject":"Synthetic"}`,
			absent: []string{"hidden@example.invalid"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			reader := &fakeMailReader{addressing: test.addressing}
			service, _ := testMailService(t, reader)
			input := validDraftInput()
			input.BCC = []string{"hidden@example.invalid"}
			access, err := service.CreateDraft(t.Context(), input, domain.Caller{Surface: "mcp", Instance: "addressing"})
			if err != nil {
				t.Fatalf("CreateDraft() error = %v", err)
			}
			access.Review.BCC = nil
			encoded, err := json.Marshal(access)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(encoded), test.want) {
				t.Fatalf("JSON %s does not contain %s", encoded, test.want)
			}
			for _, absent := range test.absent {
				if strings.Contains(string(encoded), absent) {
					t.Fatalf("JSON %s unexpectedly contains %s", encoded, absent)
				}
			}
			var decoded MailDraftAccess
			if err := json.Unmarshal(encoded, &decoded); err != nil {
				t.Fatal(err)
			}
			if decoded.Addressing == nil || decoded.Addressing.Status != access.Addressing.Status {
				t.Fatalf("round trip addressing = %+v", decoded.Addressing)
			}
			if resolved := decoded.Addressing.Resolved; resolved != nil && (resolved.To == nil || resolved.CC == nil) {
				t.Fatalf("round trip lost verified empty lists: %+v", resolved)
			}
		})
	}
}

func TestMailDraftAddressingIsOmittedBeforeSave(t *testing.T) {
	t.Parallel()

	store, err := approval.NewStore(approval.Options{})
	if err != nil {
		t.Fatal(err)
	}
	guard, err := NewGuard(policy.Rules{Mode: policy.ModeGuarded, PreviewReversibleWrites: true}, store, &memoryAudit{})
	if err != nil {
		t.Fatal(err)
	}
	reader := &fakeMailReader{addressing: &MailDraftAddressing{To: []string{"alice@example.invalid"}}}
	service, err := NewMailService(guard, reader, testMailOptions())
	if err != nil {
		t.Fatal(err)
	}
	caller := domain.Caller{Surface: "cli", Instance: "preview-first"}
	access, err := service.CreateDraft(t.Context(), validDraftInput(), caller)
	if err != nil {
		t.Fatalf("CreateDraft() error = %v", err)
	}
	if access.Status != "approval_required" || access.Addressing != nil || reader.calls != 0 {
		t.Fatalf("preview access = %+v calls=%d", access, reader.calls)
	}
	committed, err := service.CommitDraft(t.Context(), access.Preview.Token, caller)
	if err != nil {
		t.Fatalf("CommitDraft() error = %v", err)
	}
	if committed.Addressing == nil || committed.Addressing.Status != MailAddressingResolved ||
		committed.Addressing.Resolved.To[0] != "alice@example.invalid" {
		t.Fatalf("committed addressing = %+v", committed.Addressing)
	}
}

// A result produced before the addressing field existed still decodes, and
// its omission means unknown rather than an empty recipient list.
func TestMailDraftAccessDecodesPreAddressingResult(t *testing.T) {
	t.Parallel()

	legacy := `{"status":"completed","draft":{"id":"draft-1","changeKey":"change-1"},` +
		`"review":{"bodyBytes":5,"bodySha256":"00","bodyFormat":"text","composeMode":"reply",` +
		`"referenceMessageId":"source-1","referenceChangeKey":"source-version-1"}}`
	var access MailDraftAccess
	if err := json.Unmarshal([]byte(legacy), &access); err != nil {
		t.Fatal(err)
	}
	if access.Status != "completed" || access.Draft == nil || access.Addressing != nil {
		t.Fatalf("legacy access = %+v", access)
	}
}
