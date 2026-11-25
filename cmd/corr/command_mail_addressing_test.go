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
			want:      []string{"new, reply, reply-all, or forward draft", "reply-all keeps the other To/Cc"},
		},
		{
			arguments: []string{"mail", "draft", "--help"},
			want: []string{
				"new (explicit recipients)", "reply (sender or Reply-To only)",
				"reply-all (sender plus the other To/Cc)", "forward (explicit recipients)",
				"Choose from the intended audience",
			},
		},
		{
			arguments: []string{"mail", "send", "--help"},
			want: []string{
				"new or forward with explicit recipients", "reply (sender or Reply-To only)",
				"reply-all (sender plus the other To/Cc)", "require mail draft, then send-draft",
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
				"Mode: reply", "Source message: source-1 (change key source-version-1)",
				"Resolved To: alice@example.invalid", "Resolved Cc: (none)", "Resolved subject: Re: Plan",
				"Note: 2 To/Cc participant(s) of the source message are not addressed",
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
				"Resolved To: alice@example.invalid, bob@example.invalid",
				"Resolved Cc: carol@example.invalid",
			},
			absent: []string{"Note:"},
		},
		{
			name: "unknown source participants", mode: application.MailComposeReply,
			addressing: resolved([]string{"alice@example.invalid"}, []string{}, nil),
			want:       []string{"Resolved To: alice@example.invalid"},
			absent:     []string{"Note:"},
		},
		{
			name: "unresolved route", mode: application.MailComposeReply,
			addressing: &application.MailDraftAddressingReview{Status: application.MailAddressingUnresolved},
			want:       []string{"not reported by this provider route (unknown, not empty)"},
			absent:     []string{"Resolved To:", "(none)"},
		},
		{
			name: "older owner without addressing", mode: application.MailComposeReply,
			want:   []string{"unknown, not empty"},
			absent: []string{"Resolved To:"},
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
			for _, want := range test.want {
				if !strings.Contains(output.String(), want) {
					t.Errorf("output is missing %q:\n%s", want, output.String())
				}
			}
			for _, absent := range test.absent {
				if strings.Contains(output.String(), absent) {
					t.Errorf("output unexpectedly contains %q:\n%s", absent, output.String())
				}
			}
		})
	}
}
