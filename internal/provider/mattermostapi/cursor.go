package mattermostapi

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

type mattermostCursorKind string

const (
	mattermostCursorConversations mattermostCursorKind = "conversations"
	mattermostCursorMessages      mattermostCursorKind = "messages"
	mattermostCursorThread        mattermostCursorKind = "thread"
	mattermostCursorSearch        mattermostCursorKind = "search"
	mattermostCursorSync          mattermostCursorKind = "sync"
)

type mattermostCursor struct {
	Version        int                  `json:"version"`
	Kind           mattermostCursorKind `json:"kind"`
	Account        domain.AccountID     `json:"account"`
	WorkspaceID    string               `json:"workspaceId"`
	ConversationID string               `json:"conversationId,omitempty"`
	ThreadRootID   string               `json:"threadRootId,omitempty"`
	QuerySHA256    string               `json:"querySha256,omitempty"`
	SnapshotSHA256 string               `json:"snapshotSha256,omitempty"`
	Offset         int                  `json:"offset,omitempty"`
	Page           int                  `json:"page,omitempty"`
	Before         string               `json:"before,omitempty"`
}

func encodeMattermostCursor(cursor mattermostCursor) (string, error) {
	if err := cursor.validate(); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	result := "mmc1_" + base64.RawURLEncoding.EncodeToString(encoded)
	if len(result) > application.MaxMessageCursorBytes {
		return "", errors.New("mattermost cursor exceeds the configured limit")
	}
	return result, nil
}

func decodeMattermostCursor(value string, expected mattermostCursor) (mattermostCursor, error) {
	raw, found := strings.CutPrefix(value, "mmc1_")
	if !found || raw == "" || len(value) > application.MaxMessageCursorBytes {
		return mattermostCursor{}, errors.New("mattermost cursor is malformed")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return mattermostCursor{}, errors.New("mattermost cursor is malformed")
	}
	var cursor mattermostCursor
	decoder := json.NewDecoder(strings.NewReader(string(decoded)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cursor); err != nil {
		return mattermostCursor{}, errors.New("mattermost cursor is malformed")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return mattermostCursor{}, errors.New("mattermost cursor has trailing data")
	}
	if err := cursor.validate(); err != nil {
		return mattermostCursor{}, err
	}
	if cursor.Kind != expected.Kind || cursor.Account != expected.Account ||
		cursor.WorkspaceID != expected.WorkspaceID ||
		cursor.ConversationID != expected.ConversationID ||
		cursor.ThreadRootID != expected.ThreadRootID ||
		cursor.QuerySHA256 != expected.QuerySHA256 {
		return mattermostCursor{}, errors.New("mattermost cursor does not match the selected route")
	}
	return cursor, nil
}

func (cursor mattermostCursor) validate() error {
	if cursor.Version != 1 {
		return errors.New("unsupported Mattermost cursor version")
	}
	switch cursor.Kind {
	case mattermostCursorConversations, mattermostCursorMessages, mattermostCursorThread,
		mattermostCursorSearch, mattermostCursorSync:
	default:
		return errors.New("unsupported Mattermost cursor kind")
	}
	if err := cursor.Account.ValidateOpaque(); err != nil || cursor.WorkspaceID == "" ||
		len(cursor.WorkspaceID) > 4096 || strings.ContainsAny(cursor.WorkspaceID, "\r\n\x00") {
		return errors.New("mattermost cursor route is malformed")
	}
	for _, value := range []string{cursor.ConversationID, cursor.ThreadRootID, cursor.Before} {
		if value != "" && !validMattermostID(value) {
			return errors.New("mattermost cursor identity is malformed")
		}
	}
	for _, digest := range []string{cursor.QuerySHA256, cursor.SnapshotSHA256} {
		if digest != "" && (len(digest) != 64 || !hexDigest(digest)) {
			return errors.New("mattermost cursor digest is malformed")
		}
	}
	if cursor.Offset < 0 || cursor.Offset > maximumMattermostItems || cursor.Page < 0 || cursor.Page > 1_000_000 {
		return errors.New("mattermost cursor position is malformed")
	}
	return nil
}

func mattermostDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func hexDigest(value string) bool {
	_, err := hex.DecodeString(value)
	return err == nil
}
