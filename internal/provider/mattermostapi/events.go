package mattermostapi

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/nkiyohara/corresync/internal/application"
)

const (
	maximumWebSocketEventBytes = 1 << 20
	maximumIgnoredEvents       = 256
	maximumReconnects          = 3
)

// Invalidation is deliberately content-free. Mattermost event bodies are
// never trusted as message state; callers converge by running SyncMessages.
type Invalidation struct {
	Event          string
	ConversationID string
	Sequence       int64
	Reset          bool
}

// EventStream is an explicitly opened, account-scoped WebSocket. Constructing
// a Client does not start it, preserving monitoring as a separate opt-in.
type EventStream struct {
	client     *Client
	connection *websocket.Conn
	last       int64
	reconnects int
}

// NewEventStream opens the supported WebSocket endpoint only on an explicit
// caller request. Authorization remains owned by the external authorizer.
func (client *Client) NewEventStream(ctx context.Context) (*EventStream, error) {
	if client == nil || client.pinned == nil || client.authorization == nil {
		return nil, errors.New("mattermost WebSocket transport is unavailable")
	}
	stream := &EventStream{client: client}
	if err := stream.connect(ctx); err != nil {
		return nil, err
	}
	return stream, nil
}

func (stream *EventStream) connect(ctx context.Context) error {
	if stream == nil || stream.client == nil || stream.client.pinned == nil {
		return errors.New("mattermost WebSocket stream is unavailable")
	}
	origin, err := url.Parse(stream.client.origin)
	if err != nil {
		return errors.New("mattermost WebSocket origin is malformed")
	}
	target := *origin
	target.Scheme = "wss"
	target.Path = "/api/v4/websocket"
	authorizationRequest, err := http.NewRequestWithContext(
		ctx, http.MethodGet,
		strings.TrimRight(stream.client.origin, "/")+"/api/v4/websocket", nil,
	)
	if err != nil {
		return errors.New("build Mattermost WebSocket authorization request")
	}
	if err := stream.client.authorization.Apply(authorizationRequest); err != nil {
		return errors.New("authorize Mattermost WebSocket request")
	}
	authorization := authorizationRequest.Header.Get("Authorization")
	if authorization == "" || len(authorization) > 64<<10 || strings.ContainsAny(authorization, "\r\n\x00") {
		return errors.New("mattermost WebSocket authorization is malformed")
	}
	header := make(http.Header)
	header.Set("Authorization", authorization)
	header.Set("User-Agent", "corresync/dev")
	dialer := websocket.Dialer{
		HandshakeTimeout:  10 * time.Second,
		EnableCompression: false,
		NetDialContext:    stream.client.pinned.DialContext,
		TLSClientConfig: &tls.Config{ // #nosec G402 -- certificate verification remains enabled.
			MinVersion: tls.VersionTLS12,
			ServerName: origin.Hostname(),
		},
	}
	connection, response, err := dialer.DialContext(ctx, target.String(), header)
	header.Del("Authorization")
	authorizationRequest.Header.Del("Authorization")
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		return errors.New("connect selected Mattermost WebSocket")
	}
	connection.SetReadLimit(maximumWebSocketEventBytes)
	stream.connection = connection
	return nil
}

// Reconnect performs a caller-controlled bounded reconnect. A reconnect never
// resumes from an event body; the next invalidation forces REST snapshot reset.
func (stream *EventStream) Reconnect(ctx context.Context) error {
	if stream == nil || stream.reconnects >= maximumReconnects {
		return errors.New("mattermost WebSocket reconnect limit reached")
	}
	if stream.connection != nil {
		_ = stream.connection.Close()
		stream.connection = nil
	}
	stream.reconnects++
	return stream.connect(ctx)
}

// Next returns the next bounded state invalidation, ignoring a bounded number
// of duplicates and irrelevant events. Sequence gaps fail safe to Reset.
func (stream *EventStream) Next(ctx context.Context) (Invalidation, error) {
	if stream == nil || stream.connection == nil {
		return Invalidation{}, errors.New("mattermost WebSocket stream is not connected")
	}
	connection := stream.connection
	cancelRead := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = connection.SetReadDeadline(time.Now())
		case <-cancelRead:
		}
	}()
	defer close(cancelRead)
	for ignored := 0; ignored < maximumIgnoredEvents; ignored++ {
		if deadline, ok := ctx.Deadline(); ok {
			if err := connection.SetReadDeadline(deadline); err != nil {
				return Invalidation{}, errors.New("set Mattermost WebSocket deadline")
			}
		}
		messageType, payload, err := connection.ReadMessage()
		if err != nil {
			if ctx.Err() != nil {
				return Invalidation{}, ctx.Err()
			}
			return Invalidation{}, errors.New("read Mattermost WebSocket event")
		}
		if messageType != websocket.TextMessage || len(payload) > maximumWebSocketEventBytes {
			return Invalidation{}, errors.New("mattermost WebSocket event is malformed or oversized")
		}
		invalidation, relevant, err := parseMattermostInvalidation(payload, stream.last)
		if err != nil {
			return Invalidation{}, err
		}
		if invalidation.Sequence > stream.last {
			stream.last = invalidation.Sequence
		}
		if relevant {
			if stream.reconnects != 0 {
				invalidation.Reset = true
				stream.reconnects = 0
			}
			return invalidation, nil
		}
	}
	return Invalidation{}, errors.New("mattermost WebSocket event flood exceeded the bounded discard limit")
}

func parseMattermostInvalidation(payload []byte, previous int64) (Invalidation, bool, error) {
	var envelope struct {
		Event     string `json:"event"`
		Sequence  int64  `json:"seq"`
		Broadcast struct {
			ChannelID string `json:"channel_id,omitempty"`
		} `json:"broadcast"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil || envelope.Event == "" ||
		len(envelope.Event) > 128 || strings.ContainsAny(envelope.Event, "\r\n\x00") ||
		envelope.Sequence < 0 {
		return Invalidation{}, false, errors.New("mattermost WebSocket event envelope is malformed")
	}
	result := Invalidation{
		Event: envelope.Event, ConversationID: envelope.Broadcast.ChannelID,
		Sequence: envelope.Sequence,
	}
	if previous != 0 && envelope.Sequence <= previous {
		return result, false, nil
	}
	if previous != 0 && envelope.Sequence != previous+1 {
		result.Reset = true
	}
	if envelope.Event == "hello" {
		return result, false, nil
	}
	if !mattermostInvalidatingEvent(envelope.Event) {
		return result, false, nil
	}
	if !validMattermostID(envelope.Broadcast.ChannelID) {
		return Invalidation{}, false, errors.New("mattermost WebSocket invalidation has no bounded conversation")
	}
	return result, true, nil
}

func mattermostInvalidatingEvent(event string) bool {
	switch event {
	case "posted", "post_edited", "post_deleted", "reaction_added", "reaction_removed",
		"channel_created", "channel_updated", "channel_deleted", "channel_restored",
		"user_added", "user_removed", "direct_added", "group_added":
		return true
	default:
		return false
	}
}

func (stream *EventStream) Close() error {
	if stream == nil || stream.connection == nil {
		return nil
	}
	deadline := time.Now().Add(time.Second)
	writeErr := stream.connection.WriteControl(
		websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
		deadline,
	)
	closeErr := stream.connection.Close()
	stream.connection = nil
	if writeErr != nil {
		return fmt.Errorf("close Mattermost WebSocket: %w", writeErr)
	}
	return closeErr
}

var _ application.MessagingPort = (*Client)(nil)
