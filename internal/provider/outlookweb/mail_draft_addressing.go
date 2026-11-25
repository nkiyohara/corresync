package outlookweb

import (
	"context"
	"strings"

	"github.com/nkiyohara/corresync/internal/application"
)

// draftAddressing reads back the visible addressing Exchange resolved for one
// exact saved draft version, using the same exact-identity GetItem path as the
// saved-draft send snapshot. It never requests Bcc from the draft or from the
// reference message. A failed read leaves the addressing unresolved; it never
// turns a confirmed save into an error or an empty recipient list.
func (client *Client) draftAddressing(
	ctx context.Context,
	input application.MailDraftInput,
	draft application.MailDraft,
) *application.MailDraftAddressing {
	if draft.ChangeKey == "" {
		return nil
	}
	item, err := client.getExactItem(ctx, draft.ID, draft.ChangeKey, draftAddressingShape(), "draft")
	if err != nil || !item.IsDraft {
		return nil
	}
	addressing := &application.MailDraftAddressing{
		To:      recipientAddresses(item.ToRecipients),
		CC:      recipientAddresses(item.CCRecipients),
		Subject: item.Subject,
	}
	mode := input.EffectiveComposeMode()
	if mode != application.MailComposeReply && mode != application.MailComposeReplyAll {
		return addressing
	}
	source, err := client.getExactItem(
		ctx, input.ReferenceMessageID, input.ReferenceChangeKey, sourceParticipantsShape(), "reference message",
	)
	if err != nil {
		return addressing
	}
	self := owaSelfAddresses(client.mailbox, item.From, source.ReceivedBy)
	if len(self) == 0 {
		return addressing
	}
	participants := make([]string, 0, len(source.ToRecipients)+len(source.CCRecipients))
	for _, address := range append(
		recipientAddresses(source.ToRecipients), recipientAddresses(source.CCRecipients)...,
	) {
		if _, own := self[strings.ToLower(address)]; !own {
			participants = append(participants, address)
		}
	}
	addressing.SourceParticipants = participants
	addressing.SourceParticipantsKnown = true
	return addressing
}

func draftAddressingShape() itemResponseShape {
	return exactItemShape(
		"",
		"item:Subject",
		"message:ToRecipients",
		"message:CcRecipients",
		"message:From",
		"item:IsDraft",
	)
}

// sourceParticipantsShape requests only the reference message's visible
// recipients and the receiving mailbox. Bcc is deliberately not requested.
func sourceParticipantsShape() itemResponseShape {
	return exactItemShape(
		"",
		"message:ToRecipients",
		"message:CcRecipients",
		"message:ReceivedBy",
	)
}

// owaSelfAddresses collects the account's own addresses that Exchange
// reported. Without any, source participants stay unknown instead of
// counting the account itself as an omitted participant.
func owaSelfAddresses(explicit string, candidates ...recipient) map[string]struct{} {
	self := make(map[string]struct{}, len(candidates)+1)
	if explicit != "" {
		self[strings.ToLower(explicit)] = struct{}{}
	}
	for _, candidate := range candidates {
		if address := candidate.Mailbox.EmailAddress; address != "" {
			self[strings.ToLower(address)] = struct{}{}
		}
	}
	return self
}
