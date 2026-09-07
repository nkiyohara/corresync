package jmap

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"

	"github.com/nkiyohara/corresync/internal/application"
)

type setError struct {
	Type        string `json:"type"`
	Description string `json:"description"`
}

type setResponse struct {
	AccountID    string              `json:"accountId"`
	OldState     string              `json:"oldState"`
	NewState     string              `json:"newState"`
	Created      map[string]email    `json:"created"`
	Updated      map[string]any      `json:"updated"`
	Destroyed    []string            `json:"destroyed"`
	NotCreated   map[string]setError `json:"notCreated"`
	NotUpdated   map[string]setError `json:"notUpdated"`
	NotDestroyed map[string]setError `json:"notDestroyed"`
}

type identity struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

type submission struct {
	ID      string `json:"id"`
	EmailID string `json:"emailId"`
}

func (client *Client) CreateMailDraft(
	ctx context.Context,
	input application.MailDraftInput,
) (application.MailDraft, error) {
	if err := client.requireWrite("draft creation"); err != nil {
		return application.MailDraft{}, err
	}
	return client.createDraft(ctx, input)
}

func (client *Client) SendMail(
	ctx context.Context,
	input application.MailSendInput,
) (application.MailSendResult, error) {
	if err := client.requireWrite("submission"); err != nil {
		return application.MailSendResult{}, err
	}
	if !client.observed.Submission {
		return application.MailSendResult{}, errors.New(
			"JMAP submission is unavailable for this account",
		)
	}
	draftInput := application.MailDraftInput{
		Account: input.Account, To: input.To, CC: input.CC, BCC: input.BCC,
		Subject: input.Subject, Body: input.Body, BodyFormat: input.BodyFormat,
		ComposeMode: input.ComposeMode, ReferenceMessageID: input.ReferenceMessageID,
		ReferenceChangeKey: input.ReferenceChangeKey,
		Attachments:        append([]application.MailFileAttachment(nil), input.Attachments...),
	}
	sender, err := client.defaultIdentity(ctx)
	if err != nil {
		return application.MailSendResult{}, err
	}
	draft, err := client.createDraftFrom(ctx, draftInput, &sender)
	if err != nil {
		return application.MailSendResult{}, err
	}
	var response struct {
		AccountID  string                `json:"accountId"`
		OldState   string                `json:"oldState"`
		NewState   string                `json:"newState"`
		Created    map[string]submission `json:"created"`
		NotCreated map[string]setError   `json:"notCreated"`
	}
	var implicit setResponse
	err = client.callSubmission(
		ctx,
		map[string]any{
			"accountId": client.accountID,
			"create": map[string]any{
				"send": map[string]any{
					"identityId": sender.ID,
					"emailId":    draft.ID,
				},
			},
			"onSuccessUpdateEmail": map[string]any{
				"#send": map[string]any{"keywords/$draft": nil},
			},
		},
		&response,
		&implicit,
	)
	if err != nil {
		return application.MailSendResult{}, jmapDraftSubmissionError(err)
	}
	if response.AccountID != client.accountID ||
		len(response.Created) != 0 && len(response.NotCreated) != 0 {
		return application.MailSendResult{}, jmapDraftSubmissionError(
			errors.New("JMAP submission response has inconsistent account or creation results"),
		)
	}
	if failure, exists := response.NotCreated["send"]; exists {
		return application.MailSendResult{}, jmapDraftSubmissionError(
			fmt.Errorf(
				"JMAP submission failed: %s",
				sanitizeProviderError(methodError(failure)),
			),
		)
	}
	created, exists := response.Created["send"]
	if !exists || created.ID == "" || len(response.Created) != 1 ||
		created.EmailID != "" && created.EmailID != draft.ID {
		return application.MailSendResult{}, fmt.Errorf(
			"%w: JMAP submission response did not confirm the email",
			application.ErrWriteOutcomeUnknown,
		)
	}
	_, updated := implicit.Updated[draft.ID]
	if implicit.AccountID != client.accountID || implicit.NewState == "" ||
		!updated || len(implicit.Updated) != 1 ||
		len(implicit.Created) != 0 || len(implicit.Destroyed) != 0 ||
		len(implicit.NotCreated) != 0 || len(implicit.NotUpdated) != 0 ||
		len(implicit.NotDestroyed) != 0 {
		return application.MailSendResult{}, jmapDraftSubmissionError(
			errors.New("JMAP submission did not confirm the exact draft update"),
		)
	}
	return application.MailSendResult{ID: draft.ID, ChangeKey: implicit.NewState}, nil
}

func jmapDraftSubmissionError(err error) error {
	return fmt.Errorf(
		"%w: JMAP retained the reviewed draft but submission was not confirmed: %w",
		application.ErrWriteOutcomeUnknown,
		err,
	)
}

func (client *Client) MoveMail(
	ctx context.Context,
	input application.MailMoveInput,
) (application.MailMoveResult, error) {
	if err := client.requireWrite("move"); err != nil {
		return application.MailMoveResult{}, err
	}
	destination, err := client.resolveMailbox(ctx, input.Destination)
	if err != nil {
		return application.MailMoveResult{}, err
	}
	item, err := client.requireEmailState(ctx, input.MessageID, input.ChangeKey)
	if err != nil {
		return application.MailMoveResult{}, err
	}
	mailboxes := map[string]bool{destination: true}
	if item.MailboxIDs[destination] && len(item.MailboxIDs) == 1 {
		return application.MailMoveResult{
			ID: input.MessageID, ChangeKey: input.ChangeKey,
		}, nil
	}
	state, err := client.updateEmail(ctx, input.MessageID, input.ChangeKey, map[string]any{
		"mailboxIds": mailboxes,
	})
	if err != nil {
		return application.MailMoveResult{}, err
	}
	return application.MailMoveResult{ID: input.MessageID, ChangeKey: state}, nil
}

func (client *Client) SetMailReadState(
	ctx context.Context,
	input application.MailReadStateInput,
) (application.MailReadStateResult, error) {
	if err := client.requireWrite("read-state update"); err != nil {
		return application.MailReadStateResult{}, err
	}
	if _, err := client.requireEmailState(ctx, input.MessageID, input.ChangeKey); err != nil {
		return application.MailReadStateResult{}, err
	}
	var value any = true
	if input.State == application.MailReadStateUnread {
		value = nil
	}
	state, err := client.updateEmail(ctx, input.MessageID, input.ChangeKey, map[string]any{
		"keywords/$seen": value,
	})
	if err != nil {
		return application.MailReadStateResult{}, err
	}
	return application.MailReadStateResult{
		ID: input.MessageID, ChangeKey: state, State: input.State,
	}, nil
}

func (client *Client) DeleteMail(
	ctx context.Context,
	input application.MailDeleteInput,
) error {
	if err := client.requireWrite("delete"); err != nil {
		return err
	}
	if _, err := client.requireEmailState(ctx, input.MessageID, input.ChangeKey); err != nil {
		return err
	}
	var response setResponse
	if err := client.callWrite(ctx, []string{mailCapability}, "Email/set", map[string]any{
		"accountId": client.accountID,
		"ifInState": input.ChangeKey,
		"destroy":   []string{input.MessageID},
	}, &response); err != nil {
		return err
	}
	if failure, exists := response.NotDestroyed[input.MessageID]; exists {
		return fmt.Errorf("JMAP delete failed: %s", sanitizeProviderError(methodError(failure)))
	}
	if len(response.Destroyed) != 1 || response.Destroyed[0] != input.MessageID {
		return fmt.Errorf(
			"%w: JMAP did not confirm email deletion",
			application.ErrWriteOutcomeUnknown,
		)
	}
	return nil
}

func (client *Client) createDraft(
	ctx context.Context,
	input application.MailDraftInput,
) (application.MailDraft, error) {
	return client.createDraftFrom(ctx, input, nil)
}

func (client *Client) createDraftFrom(
	ctx context.Context,
	input application.MailDraftInput,
	sender *identity,
) (application.MailDraft, error) {
	mailboxes, err := client.getMailboxes(ctx)
	if err != nil {
		return application.MailDraft{}, err
	}
	draftsID, err := mailboxByRole(mailboxes.List, "drafts")
	if err != nil {
		return application.MailDraft{}, err
	}
	composition, err := client.composition(ctx, input)
	if err != nil {
		return application.MailDraft{}, err
	}
	create := map[string]any{
		"mailboxIds": map[string]bool{draftsID: true},
		"keywords":   map[string]bool{"$draft": true, "$seen": true},
		"to":         composition.To,
		"cc":         composition.CC,
		"bcc":        composition.BCC,
		"subject":    composition.Subject,
	}
	if sender != nil {
		create["from"] = []emailAddress{{Name: sender.Name, Email: sender.Email}}
	}
	if len(composition.InReplyTo) != 0 {
		create["inReplyTo"] = composition.InReplyTo
	}
	if len(composition.References) != 0 {
		create["references"] = composition.References
	}
	bodyPart := map[string]any{
		"partId": "body",
		"type":   composition.BodyType,
	}
	create["bodyValues"] = map[string]any{
		"body": map[string]string{"value": composition.Body},
	}
	parts := []any{bodyPart}
	uploadedParts := 0
	for _, attachment := range input.Attachments {
		uploaded, err := client.upload(
			ctx,
			attachment.Name,
			attachment.ContentType,
			attachment.Content,
		)
		if err != nil {
			if uploadedParts != 0 {
				return application.MailDraft{}, jmapUploadedDraftError(err)
			}
			return application.MailDraft{}, err
		}
		uploadedParts++
		contentType := uploaded.Type
		if contentType == "" {
			contentType = attachment.ContentType
		}
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		parts = append(parts, map[string]any{
			"blobId":      uploaded.BlobID,
			"type":        contentType,
			"name":        attachment.Name,
			"size":        uploaded.Size,
			"disposition": "attachment",
		})
	}
	if len(parts) == 1 {
		create["bodyStructure"] = bodyPart
	} else {
		create["bodyStructure"] = map[string]any{
			"type": "multipart/mixed", "subParts": parts,
		}
	}
	var response setResponse
	if err := client.callWrite(ctx, []string{mailCapability}, "Email/set", map[string]any{
		"accountId": client.accountID,
		"create":    map[string]any{"draft": create},
	}, &response); err != nil {
		if uploadedParts != 0 {
			return application.MailDraft{}, jmapUploadedDraftError(err)
		}
		return application.MailDraft{}, err
	}
	if failure, exists := response.NotCreated["draft"]; exists {
		err := fmt.Errorf(
			"JMAP draft creation failed: %s",
			sanitizeProviderError(methodError(failure)),
		)
		if uploadedParts != 0 {
			return application.MailDraft{}, jmapUploadedDraftError(err)
		}
		return application.MailDraft{}, err
	}
	created, exists := response.Created["draft"]
	if !exists || created.ID == "" || response.NewState == "" {
		return application.MailDraft{}, fmt.Errorf(
			"%w: JMAP did not confirm draft creation",
			application.ErrWriteOutcomeUnknown,
		)
	}
	return application.MailDraft{
		ID: created.ID, ChangeKey: response.NewState,
	}, nil
}

func jmapUploadedDraftError(err error) error {
	return fmt.Errorf(
		"%w: JMAP retained uploaded attachment blobs but did not confirm the draft: %w",
		application.ErrWriteOutcomeUnknown,
		err,
	)
}

type composition struct {
	To         []emailAddress
	CC         []emailAddress
	BCC        []emailAddress
	Subject    string
	Body       string
	BodyType   string
	InReplyTo  []string
	References []string
}

func (client *Client) composition(
	ctx context.Context,
	input application.MailDraftInput,
) (composition, error) {
	result := composition{
		To: addresses(input.To), CC: addresses(input.CC), BCC: addresses(input.BCC),
		Subject: input.Subject, Body: input.Body, BodyType: "text/plain",
	}
	if input.EffectiveBodyFormat() == application.MailBodyHTML {
		result.BodyType = "text/html"
	}
	mode := input.EffectiveComposeMode()
	if mode == application.MailComposeNew {
		return result, nil
	}
	response, err := client.getEmails(ctx, []string{input.ReferenceMessageID}, true)
	if err != nil {
		return composition{}, err
	}
	if response.State != input.ReferenceChangeKey ||
		len(response.List) != 1 ||
		len(response.NotFound) != 0 {
		return composition{}, errors.New("JMAP reference email changed or was not found")
	}
	source := response.List[0]
	result.InReplyTo = append([]string(nil), source.MessageID...)
	result.References = append(append([]string(nil), source.References...), source.MessageID...)
	if result.Subject == "" {
		prefix := "Re: "
		if mode == application.MailComposeForward {
			prefix = "Fwd: "
		}
		if strings.HasPrefix(strings.ToLower(source.Subject), strings.ToLower(prefix)) {
			result.Subject = source.Subject
		} else {
			result.Subject = prefix + source.Subject
		}
	}
	switch mode {
	case application.MailComposeNew:
		return result, nil
	case application.MailComposeReply:
		result.To, err = uniqueDerivedAddresses(
			jmapReplyTarget(source),
			client.username,
		)
	case application.MailComposeReplyAll:
		result.To, err = uniqueDerivedAddresses(
			append(
				append([]emailAddress{}, jmapReplyTarget(source)...),
				source.To...,
			),
			client.username,
		)
		if err == nil {
			result.CC, err = uniqueDerivedAddresses(source.CC, client.username)
			result.CC = jmapAddressesExcluding(result.CC, result.To)
		}
	case application.MailComposeForward:
		sourceText, bodyErr := boundedBodyText(source)
		if bodyErr != nil {
			return composition{}, bodyErr
		}
		if sourceText != "" {
			if result.Body != "" {
				result.Body += "\n\n"
			}
			result.Body += "---------- Forwarded message ----------\n" + sourceText
		}
	default:
		return composition{}, fmt.Errorf("unsupported JMAP compose mode %q", mode)
	}
	if err != nil {
		return composition{}, err
	}
	if mode == application.MailComposeReply ||
		mode == application.MailComposeReplyAll {
		count := len(result.To) + len(result.CC)
		if count == 0 {
			return composition{}, errors.New(
				"JMAP reference email has no valid reply recipient",
			)
		}
		if count > application.MaxMailRecipients {
			return composition{}, fmt.Errorf(
				"JMAP reply has %d recipients; maximum is %d",
				count,
				application.MaxMailRecipients,
			)
		}
	}
	if len(result.Body) > application.MaxMailDraftBodyBytes {
		return composition{}, fmt.Errorf(
			"composed JMAP body exceeds %d bytes",
			application.MaxMailDraftBodyBytes,
		)
	}
	return result, nil
}

func jmapReplyTarget(source email) []emailAddress {
	if len(source.ReplyTo) != 0 {
		return source.ReplyTo
	}
	return source.From
}

func (client *Client) requireEmailState(
	ctx context.Context,
	id, state string,
) (email, error) {
	response, err := client.getEmails(ctx, []string{id}, false)
	if err != nil {
		return email{}, err
	}
	if response.State != state || len(response.List) != 1 || len(response.NotFound) != 0 {
		return email{}, errors.New("JMAP email changed or was not found")
	}
	return response.List[0], nil
}

func (client *Client) updateEmail(
	ctx context.Context,
	id, state string,
	patch map[string]any,
) (string, error) {
	var response setResponse
	if err := client.callWrite(ctx, []string{mailCapability}, "Email/set", map[string]any{
		"accountId": client.accountID,
		"ifInState": state,
		"update":    map[string]any{id: patch},
	}, &response); err != nil {
		return "", err
	}
	if failure, exists := response.NotUpdated[id]; exists {
		return "", fmt.Errorf("JMAP update failed: %s", sanitizeProviderError(methodError(failure)))
	}
	if _, exists := response.Updated[id]; !exists || response.NewState == "" {
		return "", fmt.Errorf(
			"%w: JMAP did not confirm email update",
			application.ErrWriteOutcomeUnknown,
		)
	}
	return response.NewState, nil
}

func (client *Client) defaultIdentity(ctx context.Context) (identity, error) {
	var response getResponse[identity]
	if err := client.call(
		ctx,
		[]string{submissionCapability},
		"Identity/get",
		map[string]any{
			"accountId":  client.accountID,
			"properties": []string{"id", "name", "email"},
		},
		&response,
	); err != nil {
		return identity{}, err
	}
	if response.AccountID != client.accountID || len(response.NotFound) != 0 {
		return identity{}, errors.New("JMAP identity response does not match the selected account")
	}
	if len(response.List) == 0 {
		return identity{}, errors.New("JMAP submission has no identity")
	}
	selected := response.List[0]
	for _, item := range response.List {
		if strings.EqualFold(item.Email, client.username) {
			selected = item
			break
		}
	}
	if selected.ID == "" || strings.ContainsAny(selected.Name, "\r\n\x00") {
		return identity{}, errors.New("JMAP selected identity is malformed")
	}
	parsed, err := mail.ParseAddress(selected.Email)
	if err != nil || parsed.Address != selected.Email ||
		strings.ContainsAny(selected.Email, "\r\n\x00") {
		return identity{}, errors.New("JMAP selected identity has a malformed From address")
	}
	// A domain-wide identity still needs one concrete sender address. Use the
	// configured username only if it is a valid address in that same domain.
	if domainName, wildcard := strings.CutPrefix(selected.Email, "*@"); wildcard {
		username, parseErr := mail.ParseAddress(client.username)
		_, usernameDomain, found := strings.Cut(client.username, "@")
		if parseErr != nil || username.Address != client.username ||
			!found || !strings.EqualFold(usernameDomain, domainName) ||
			strings.HasPrefix(client.username, "*@") {
			return identity{}, errors.New("JMAP wildcard identity has no concrete configured sender address")
		}
		selected.Email = client.username
	}
	return selected, nil
}

func addresses(values []string) []emailAddress {
	result := make([]emailAddress, 0, len(values))
	for _, value := range values {
		result = append(result, emailAddress{Email: value})
	}
	return result
}

func uniqueDerivedAddresses(
	values []emailAddress,
	exclude string,
) ([]emailAddress, error) {
	seen := make(map[string]struct{}, len(values))
	result := make([]emailAddress, 0, len(values))
	for _, value := range values {
		parsed, err := mail.ParseAddress(value.Email)
		if err != nil ||
			parsed.Address != value.Email ||
			strings.ContainsAny(value.Email, "\r\n\x00") {
			return nil, errors.New(
				"JMAP reference email contains a malformed recipient address",
			)
		}
		key := strings.ToLower(value.Email)
		if strings.EqualFold(value.Email, exclude) {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	return result, nil
}

func jmapAddressesExcluding(
	values, excluded []emailAddress,
) []emailAddress {
	seen := make(map[string]struct{}, len(excluded))
	for _, value := range excluded {
		seen[strings.ToLower(value.Email)] = struct{}{}
	}
	result := values[:0]
	for _, value := range values {
		if _, exists := seen[strings.ToLower(value.Email)]; !exists {
			result = append(result, value)
		}
	}
	return result
}
