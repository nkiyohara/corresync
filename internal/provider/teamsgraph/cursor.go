package teamsgraph

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"strconv"
	"strings"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/domain"
)

const (
	graphCursorConversations = "conversations"
	graphCursorMessages      = "messages"
	graphCursorSearch        = "search"
	graphStageChats          = "chats"
	graphStageChannels       = "channels"
)

type graphPageCursor struct {
	Version        int              `json:"version"`
	Kind           string           `json:"kind"`
	Stage          string           `json:"stage,omitempty"`
	Account        domain.AccountID `json:"account"`
	WorkspaceID    string           `json:"workspaceId"`
	ConversationID string           `json:"conversationId,omitempty"`
	ThreadRootID   string           `json:"threadRootId,omitempty"`
	QuerySHA256    string           `json:"querySha256,omitempty"`
	NextLink       string           `json:"nextLink,omitempty"`
	TeamIndex      int              `json:"teamIndex,omitempty"`
	TeamID         string           `json:"teamId,omitempty"`
	Offset         int              `json:"offset,omitempty"`
}

func encodeGraphCursor(cursor graphPageCursor) (string, error) {
	if err := cursor.validate(); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	result := "tgp1_" + base64.RawURLEncoding.EncodeToString(encoded)
	if len(result) > application.MaxMessageCursorBytes {
		return "", errors.New("the Teams Graph pagination cursor exceeds the configured limit")
	}
	return result, nil
}

func decodeGraphCursor(value string, expected graphPageCursor) (graphPageCursor, error) {
	raw, found := strings.CutPrefix(value, "tgp1_")
	if !found || raw == "" || len(value) > application.MaxMessageCursorBytes {
		return graphPageCursor{}, errors.New("the Teams Graph pagination cursor is malformed")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return graphPageCursor{}, errors.New("the Teams Graph pagination cursor is malformed")
	}
	var cursor graphPageCursor
	decoder := json.NewDecoder(strings.NewReader(string(decoded)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cursor); err != nil {
		return graphPageCursor{}, errors.New("the Teams Graph pagination cursor is malformed")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return graphPageCursor{}, errors.New("the Teams Graph pagination cursor has trailing data")
	}
	if err := cursor.validate(); err != nil {
		return graphPageCursor{}, err
	}
	if cursor.Kind != expected.Kind || cursor.Account != expected.Account ||
		cursor.WorkspaceID != expected.WorkspaceID || cursor.ConversationID != expected.ConversationID ||
		cursor.ThreadRootID != expected.ThreadRootID || cursor.QuerySHA256 != expected.QuerySHA256 {
		return graphPageCursor{}, errors.New("the Teams Graph pagination cursor does not match the selected route")
	}
	return cursor, nil
}

func (cursor graphPageCursor) validate() error {
	if cursor.Version != 1 || cursor.Account.ValidateOpaque() != nil ||
		!validGraphOpaque(cursor.WorkspaceID) {
		return errors.New("the Teams Graph pagination cursor identity is malformed")
	}
	if len(cursor.NextLink) > application.MaxMessageCursorBytes ||
		strings.ContainsAny(cursor.NextLink, "\r\n\x00") {
		return errors.New("the Teams Graph provider continuation is malformed")
	}
	switch cursor.Kind {
	case graphCursorConversations:
		if cursor.ConversationID != "" || cursor.ThreadRootID != "" ||
			cursor.QuerySHA256 != "" || cursor.Offset != 0 {
			return errors.New("the Teams Graph conversation cursor is malformed")
		}
		switch cursor.Stage {
		case graphStageChats:
			if cursor.NextLink == "" || cursor.TeamIndex != 0 || cursor.TeamID != "" {
				return errors.New("the Teams Graph chat cursor is malformed")
			}
		case graphStageChannels:
			if cursor.TeamIndex < 0 || cursor.TeamIndex > maximumGraphItems ||
				(cursor.NextLink == "") != (cursor.TeamID == "") {
				return errors.New("the Teams Graph channel cursor is malformed")
			}
		default:
			return errors.New("the Teams Graph conversation cursor stage is malformed")
		}
	case graphCursorMessages:
		if cursor.Stage != "" || cursor.NextLink == "" || cursor.TeamIndex != 0 ||
			cursor.TeamID != "" || cursor.Offset != 0 || cursor.QuerySHA256 != "" {
			return errors.New("the Teams Graph message cursor is malformed")
		}
		if _, err := decodeGraphConversationID(cursor.ConversationID); err != nil {
			return errors.New("the Teams Graph message cursor conversation is malformed")
		}
	case graphCursorSearch:
		decoded, err := hex.DecodeString(cursor.QuerySHA256)
		if cursor.Stage != "" || cursor.NextLink != "" || cursor.TeamIndex != 0 ||
			cursor.TeamID != "" || cursor.ConversationID != "" || cursor.ThreadRootID != "" ||
			cursor.Offset < 1 || cursor.Offset > 10_000 || err != nil || len(decoded) != sha256.Size {
			return errors.New("the Teams Graph search cursor is malformed")
		}
	default:
		return errors.New("the Teams Graph pagination cursor kind is malformed")
	}
	return nil
}

func graphQueryDigest(query string) string {
	digest := sha256.Sum256([]byte(query))
	return hex.EncodeToString(digest[:])
}

func (client *Client) continuation(value, resource string, fixed url.Values) (url.Values, error) {
	if client == nil || client.apiBase == nil || value == "" ||
		len(value) > application.MaxMessageCursorBytes || strings.ContainsAny(value, "\r\n\x00") {
		return nil, errors.New("the Teams Graph continuation is malformed")
	}
	target, err := url.Parse(value)
	if err != nil || target.Scheme != client.apiBase.Scheme || target.Host != client.apiBase.Host ||
		target.User != nil || target.Fragment != "" {
		return nil, errors.New("the Teams Graph continuation escaped the configured origin")
	}
	basePath := strings.TrimSuffix(client.apiBase.EscapedPath(), "/") + "/"
	if target.EscapedPath() != basePath+resource {
		return nil, errors.New("the Teams Graph continuation escaped the selected collection")
	}
	query, err := url.ParseQuery(target.RawQuery)
	if err != nil {
		return nil, errors.New("the Teams Graph continuation query is malformed")
	}
	result := cloneGraphQuery(fixed)
	for name, values := range query {
		if name == "$skiptoken" {
			if len(values) != 1 || values[0] == "" || len(values[0]) > 4096 ||
				strings.ContainsAny(values[0], "\r\n\x00") {
				return nil, errors.New("the Teams Graph continuation token is malformed")
			}
			result.Set(name, values[0])
			continue
		}
		if expected, exists := fixed[name]; !exists || len(values) != len(expected) ||
			strings.Join(values, "\x00") != strings.Join(expected, "\x00") {
			return nil, errors.New("the Teams Graph continuation changed the selected query")
		}
	}
	if result.Get("$skiptoken") == "" {
		return nil, errors.New("the Teams Graph continuation omitted its token")
	}
	return result, nil
}

func graphTop(limit, maximum int) string { return strconv.Itoa(min(limit, maximum)) }

func cloneGraphQuery(source url.Values) url.Values {
	result := make(url.Values, len(source))
	for name, values := range source {
		result[name] = append([]string(nil), values...)
	}
	return result
}
