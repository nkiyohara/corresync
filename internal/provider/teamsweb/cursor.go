package teamsweb

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
	"github.com/nkiyohara/corresync/internal/provider/teamscontract"
)

const (
	webCursorConversations = "conversations"
	webCursorMessages      = "messages"
	webCursorSearch        = "search"
)

type pageCursor struct {
	Version        int              `json:"version"`
	Kind           string           `json:"kind"`
	Account        domain.AccountID `json:"account"`
	WorkspaceID    string           `json:"workspaceId"`
	ConversationID string           `json:"conversationId,omitempty"`
	ThreadRootID   string           `json:"threadRootId,omitempty"`
	QuerySHA256    string           `json:"querySha256,omitempty"`
	Opaque         string           `json:"opaque"`
}

func encodeCursor(cursor pageCursor) (string, error) {
	if err := cursor.validate(); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	value := "twp1_" + base64.RawURLEncoding.EncodeToString(encoded)
	if len(value) > application.MaxMessageCursorBytes {
		return "", errors.New("the Teams Web pagination cursor exceeds the configured limit")
	}
	return value, nil
}

func decodeCursor(value string, expected pageCursor) (pageCursor, error) {
	raw, found := strings.CutPrefix(value, "twp1_")
	if !found || raw == "" || len(value) > application.MaxMessageCursorBytes {
		return pageCursor{}, errors.New("the Teams Web pagination cursor is malformed")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || base64.RawURLEncoding.EncodeToString(decoded) != raw {
		return pageCursor{}, errors.New("the Teams Web pagination cursor is malformed")
	}
	var cursor pageCursor
	decoder := json.NewDecoder(strings.NewReader(string(decoded)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cursor); err != nil {
		return pageCursor{}, errors.New("the Teams Web pagination cursor is malformed")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return pageCursor{}, errors.New("the Teams Web pagination cursor has trailing data")
	}
	if err := cursor.validate(); err != nil {
		return pageCursor{}, err
	}
	if cursor.Kind != expected.Kind || cursor.Account != expected.Account ||
		cursor.WorkspaceID != expected.WorkspaceID ||
		cursor.ConversationID != expected.ConversationID ||
		cursor.ThreadRootID != expected.ThreadRootID || cursor.QuerySHA256 != expected.QuerySHA256 {
		return pageCursor{}, errors.New("the Teams Web pagination cursor does not match the selected route")
	}
	return cursor, nil
}

func (cursor pageCursor) validate() error {
	if cursor.Version != 1 || cursor.Account.ValidateOpaque() != nil ||
		!teamscontract.ValidOpaque(cursor.WorkspaceID) || cursor.Opaque == "" ||
		len(cursor.Opaque) > 4096 || strings.ContainsAny(cursor.Opaque, "\r\n\x00") {
		return errors.New("the Teams Web pagination cursor identity is malformed")
	}
	switch cursor.Kind {
	case webCursorConversations:
		if cursor.ConversationID != "" || cursor.ThreadRootID != "" || cursor.QuerySHA256 != "" {
			return errors.New("the Teams Web conversation cursor is malformed")
		}
	case webCursorMessages:
		if validateConversationID(cursor.ConversationID) != nil || cursor.QuerySHA256 != "" {
			return errors.New("the Teams Web message cursor is malformed")
		}
	case webCursorSearch:
		decoded, err := hex.DecodeString(cursor.QuerySHA256)
		if cursor.ThreadRootID != "" || err != nil || len(decoded) != sha256.Size {
			return errors.New("the Teams Web search cursor is malformed")
		}
		if cursor.ConversationID != "" && validateConversationID(cursor.ConversationID) != nil {
			return errors.New("the Teams Web search conversation is malformed")
		}
	default:
		return errors.New("the Teams Web pagination cursor kind is malformed")
	}
	return nil
}

func queryDigest(query string) string {
	digest := sha256.Sum256([]byte(query))
	return hex.EncodeToString(digest[:])
}

func unwrapPageCursor(value string, expected pageCursor) (string, error) {
	if value == "" {
		return "", nil
	}
	cursor, err := decodeCursor(value, expected)
	if err != nil {
		return "", err
	}
	return cursor.Opaque, nil
}

func wrapPageCursor(opaque string, cursor pageCursor) (string, error) {
	if opaque == "" {
		return "", nil
	}
	cursor.Version = 1
	cursor.Opaque = opaque
	return encodeCursor(cursor)
}
