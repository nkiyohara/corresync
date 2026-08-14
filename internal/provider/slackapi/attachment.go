package slackapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/nkiyohara/corresync/internal/application"
)

func (client *Client) getSlackAttachment(
	ctx context.Context,
	input application.MessageAttachmentGetInput,
) (application.MessageAttachmentContent, error) {
	message, err := client.getSlackMessage(
		ctx, input.ConversationID, input.ThreadRootID, input.MessageID,
	)
	if err != nil {
		return application.MessageAttachmentContent{}, err
	}
	var selected *slackFile
	for index := range message.Files {
		if message.Files[index].ID == input.AttachmentID {
			selected = &message.Files[index]
			break
		}
	}
	if selected == nil {
		return application.MessageAttachmentContent{}, errors.New("slack did not return the selected attachment")
	}
	if selected.Size < 0 || selected.Size > application.MaxMessageAttachmentBytes {
		return application.MessageAttachmentContent{}, errors.New("slack attachment exceeds the configured download limit")
	}
	rawURL := selected.URLPrivateDownload
	if rawURL == "" {
		rawURL = selected.URLPrivate
	}
	target, err := client.validateSlackFileURL(rawURL)
	if err != nil {
		return application.MessageAttachmentContent{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return application.MessageAttachmentContent{}, err
	}
	response, err := client.files.Do(request)
	if err != nil {
		return application.MessageAttachmentContent{}, errors.New("download Slack attachment")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusUnauthorized {
		return application.MessageAttachmentContent{}, application.NewProviderAuthenticationFailure(
			application.AuthenticationReasonCredentialRejected,
			errors.New("slack attachment authorization was rejected"),
		)
	}
	if response.StatusCode == http.StatusTooManyRequests {
		retry, parseErr := strconv.Atoi(response.Header.Get("Retry-After"))
		if parseErr != nil || retry < 1 || time.Duration(retry)*time.Second > maximumRetryAfter {
			return application.MessageAttachmentContent{}, errors.New("slack returned a malformed file rate-limit interval")
		}
		return application.MessageAttachmentContent{}, &RateLimitError{RetryAfter: time.Duration(retry) * time.Second}
	}
	if response.StatusCode != http.StatusOK {
		return application.MessageAttachmentContent{}, fmt.Errorf("slack file endpoint returned HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, application.MaxMessageAttachmentBytes+1))
	if err != nil {
		return application.MessageAttachmentContent{}, errors.New("read Slack attachment")
	}
	if len(data) > application.MaxMessageAttachmentBytes || int64(len(data)) != selected.Size {
		return application.MessageAttachmentContent{}, errors.New("slack attachment body does not match its bounded metadata")
	}
	name := selected.Name
	if name == "" {
		name = selected.Title
	}
	return application.MessageAttachmentContent{
		Metadata: application.MessageAttachment{
			ID: selected.ID, Name: boundedSlackText(name, 4096),
			MediaType: boundedSlackText(selected.MediaType, 256), Size: selected.Size,
			Downloadable: true,
		},
		Data: data,
	}, nil
}

func (client *Client) validateSlackFileURL(raw string) (*url.URL, error) {
	if raw == "" || len(raw) > 8192 || strings.ContainsAny(raw, "\r\n\x00") {
		return nil, errors.New("slack attachment URL is malformed")
	}
	target, err := url.Parse(raw)
	if err != nil || target.Scheme != "https" || target.User != nil ||
		target.RawQuery != "" || target.Fragment != "" || target.Host == "" {
		return nil, errors.New("slack attachment URL is not one credential-free HTTPS URL")
	}
	allowed := target.Host == client.apiHost
	switch client.apiHost {
	case "slack.com":
		allowed = allowed || target.Host == "files.slack.com"
	case "slack-gov.com":
		allowed = allowed || target.Host == "files.slack-gov.com"
	}
	if !allowed || !strings.HasPrefix(target.EscapedPath(), "/files-pri/") {
		return nil, errors.New("slack attachment URL escaped the selected provider authority")
	}
	return target, nil
}
