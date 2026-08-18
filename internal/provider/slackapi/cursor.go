package slackapi

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/domain"
)

const (
	slackCursorConversations = "conversations"
	slackCursorMessages      = "messages"
	slackCursorThread        = "thread"
	slackCursorSearch        = "search"
)

type slackPageCursor struct {
	Version        int              `json:"version"`
	Kind           string           `json:"kind"`
	Account        domain.AccountID `json:"account"`
	WorkspaceID    string           `json:"workspaceId"`
	ConversationID string           `json:"conversationId,omitempty"`
	ThreadRootID   string           `json:"threadRootId,omitempty"`
	QuerySHA256    string           `json:"querySha256,omitempty"`
	Opaque         string           `json:"opaque,omitempty"`
	Page           int              `json:"page,omitempty"`
}

func encodeSlackPageCursor(cursor slackPageCursor) (string, error) {
	if err := cursor.validate(); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	result := "slp1_" + base64.RawURLEncoding.EncodeToString(encoded)
	if len(result) > application.MaxMessageCursorBytes {
		return "", errors.New("slack pagination cursor exceeds the configured limit")
	}
	return result, nil
}

func decodeSlackPageCursor(value string, expected slackPageCursor) (slackPageCursor, error) {
	raw, found := strings.CutPrefix(value, "slp1_")
	if !found || raw == "" || len(value) > application.MaxMessageCursorBytes {
		return slackPageCursor{}, errors.New("slack pagination cursor is malformed")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return slackPageCursor{}, errors.New("slack pagination cursor is malformed")
	}
	var cursor slackPageCursor
	decoder := json.NewDecoder(strings.NewReader(string(decoded)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cursor); err != nil {
		return slackPageCursor{}, errors.New("slack pagination cursor is malformed")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return slackPageCursor{}, errors.New("slack pagination cursor has trailing data")
	}
	if err := cursor.validate(); err != nil {
		return slackPageCursor{}, err
	}
	if cursor.Kind != expected.Kind || cursor.Account != expected.Account ||
		cursor.WorkspaceID != expected.WorkspaceID || cursor.ConversationID != expected.ConversationID ||
		cursor.ThreadRootID != expected.ThreadRootID || cursor.QuerySHA256 != expected.QuerySHA256 {
		return slackPageCursor{}, errors.New("slack pagination cursor does not match the selected route")
	}
	return cursor, nil
}

func (cursor slackPageCursor) validate() error {
	if cursor.Version != 1 || cursor.Account.ValidateOpaque() != nil ||
		!validSlackID(cursor.WorkspaceID) {
		return errors.New("slack pagination cursor identity is malformed")
	}
	switch cursor.Kind {
	case slackCursorConversations:
		if cursor.ConversationID != "" || cursor.ThreadRootID != "" ||
			cursor.QuerySHA256 != "" || cursor.Page != 0 || cursor.Opaque == "" {
			return errors.New("slack conversation cursor is malformed")
		}
	case slackCursorMessages, slackCursorThread:
		if !validSlackID(cursor.ConversationID) || cursor.QuerySHA256 != "" ||
			cursor.Page != 0 || cursor.Opaque == "" {
			return errors.New("slack message cursor is malformed")
		}
		if cursor.Kind == slackCursorThread && !validSlackTimestamp(cursor.ThreadRootID) {
			return errors.New("slack thread cursor is malformed")
		}
		if cursor.Kind == slackCursorMessages && cursor.ThreadRootID != "" {
			return errors.New("slack history cursor is malformed")
		}
	case slackCursorSearch:
		if cursor.ConversationID != "" || cursor.ThreadRootID != "" || cursor.Opaque != "" ||
			cursor.Page < 2 || cursor.Page > 100 || !validSlackQueryDigest(cursor.QuerySHA256) {
			return errors.New("slack search cursor is malformed")
		}
	default:
		return errors.New("slack pagination cursor kind is malformed")
	}
	if len(cursor.Opaque) > application.MaxMessageCursorBytes || strings.ContainsAny(cursor.Opaque, "\r\n\x00") {
		return errors.New("slack provider cursor is malformed")
	}
	return nil
}

func validSlackQueryDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func slackQueryDigest(query string) string {
	digest := sha256.Sum256([]byte(query))
	return hex.EncodeToString(digest[:])
}
