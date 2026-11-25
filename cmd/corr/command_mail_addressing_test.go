package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/nkiyohara/corresync/internal/application"
)

func TestMailHelpListsEveryCompositionMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		arguments []string
		want      []string
	}{
		{
			arguments: []string{"mail", "--help"},
			want: []string{
				"Save a new message, reply, reply-all, or forward as a draft.",
				"--mode reply addresses only the sender (or Reply-To), and --mode reply-all keeps everyone else on To and Cc",
			},
		},
		{
			arguments: []string{"mail", "draft", "--help"},
			want: []string{
				"new or forward (the recipients you give)", "reply (sender or Reply-To only)",
				"reply-all (sender plus the other To/Cc)",
				"Replies take their recipients from the original message",
			},
		},
		{
			arguments: []string{"mail", "send", "--help"},
			want: []string{
				"new or forward: you give the recipients", "reply (sender or Reply-To only)",
				"reply-all (sender plus the other To/Cc)", "use corr mail draft, then send-draft",
			},
		},
	}
	for _, test := range tests {
		t.Run(strings.Join(test.arguments, " "), func(t *testing.T) {
			t.Parallel()
			var stdout, stderr bytes.Buffer
			if code := run(context.Background(), test.arguments, &stdout, &stderr); code != 0 {
				t.Fatalf("code = %d, stderr = %q", code, stderr.String())
			}
			help := strings.Join(strings.Fields(stdout.String()), " ")
			for _, want := range test.want {
				if !strings.Contains(help, want) {
					t.Errorf("help is missing %q:\n%s", want, stdout.String())
				}
			}
		})
	}
}

func TestWriteSavedDraftShowsResolvedAddressing(t *testing.T) {
	t.Parallel()

	count := func(value int) *int { return &value }
	resolved := func(to, cc []string, omitted *int) *application.MailDraftAddressingReview {
		return &application.MailDraftAddressingReview{
			Status: application.MailAddressingResolved,
			Resolved: &application.MailResolvedAddressing{
				To: to, CC: cc, Subject: "Re: Plan\x1b[2J", SourceParticipantsNotIncluded: omitted,
			},
		}
	}
	tests := []struct {
		name       string
		mode       application.MailComposeMode
		addressing *application.MailDraftAddressingReview
		want       []string
		absent     []string
	}{
		{
			name: "reply that drops participants", mode: application.MailComposeReply,
			addressing: resolved([]string{"alice@example.invalid"}, []string{}, count(2)),
			want: []string{
				"Mode: reply", "Original message: source-1 (change key source-version-1)",
				"To: alice@example.invalid", "Cc: (none)", "Subject: Re: Plan",
				"Note: 2 To/Cc recipients of the original message are not on this draft",
				"--mode reply-all",
			},
			absent: []string{"\x1b"},
		},
		{
			name: "reply-all keeps everyone", mode: application.MailComposeReplyAll,
			addressing: resolved(
				[]string{"alice@example.invalid", "bob@example.invalid"},
				[]string{"carol@example.invalid"}, count(0),
			),
			want: []string{
				"To: alice@example.invalid, bob@example.invalid",
				"Cc: carol@example.invalid",
			},
			absent: []string{"Note:"},
		},
		{
			name: "unknown source participants", mode: application.MailComposeReply,
			addressing: resolved([]string{"alice@example.invalid"}, []string{}, nil),
			want:       []string{"To: alice@example.invalid"},
			absent:     []string{"Note:"},
		},
		{
			name: "unresolved route", mode: application.MailComposeReply,
			addressing: &application.MailDraftAddressingReview{Status: application.MailAddressingUnresolved},
			want:       []string{"did not report the saved To, Cc, and subject (unknown, not empty)"},
			absent:     []string{" To:", "Cc: (none)"},
		},
		{
			name: "older owner without addressing", mode: application.MailComposeReply,
			want:   []string{"unknown, not empty"},
			absent: []string{" To:"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var output bytes.Buffer
			err := writeSavedDraft(&output, application.MailDraftAccess{
				Status: "completed", Draft: &application.MailDraft{ID: "draft-1"},
				Review: application.MailReview{
					ComposeMode: test.mode, ReferenceMessageID: "source-1",
					ReferenceChangeKey: "source-version-1",
				},
				Addressing: test.addressing,
			})
			if err != nil {
				t.Fatal(err)
			}
			text := strings.Join(strings.Fields(output.String()), " ")
			for _, want := range test.want {
				if !strings.Contains(text, want) {
					t.Errorf("output is missing %q:\n%s", want, output.String())
				}
			}
			for _, absent := range test.absent {
				if strings.Contains(text, absent) || strings.Contains(output.String(), absent) {
					t.Errorf("output unexpectedly contains %q:\n%s", absent, output.String())
				}
			}
		})
	}
}
