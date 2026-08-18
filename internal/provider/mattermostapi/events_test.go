package mattermostapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestMattermostInvalidationsDeduplicateAndRecoverGaps(t *testing.T) {
	channelID := "channelid00000000000000000"
	first, relevant, err := parseMattermostInvalidation([]byte(
		`{"event":"posted","seq":7,"broadcast":{"channel_id":"`+channelID+`"},"data":{"post":"untrusted"}}`,
	), 0, false)
	if err != nil || !relevant || first.Reset || first.ConversationID != channelID {
		t.Fatalf("first invalidation = %#v, %v, %v", first, relevant, err)
	}
	_, relevant, err = parseMattermostInvalidation([]byte(
		`{"event":"posted","seq":7,"broadcast":{"channel_id":"`+channelID+`"}}`,
	), 7, true)
	if err != nil || relevant {
		t.Fatalf("duplicate = relevant %v, error %v", relevant, err)
	}
	gap, relevant, err := parseMattermostInvalidation([]byte(
		`{"event":"post_deleted","seq":9,"broadcast":{"channel_id":"`+channelID+`"}}`,
	), 7, true)
	if err != nil || !relevant || !gap.Reset {
		t.Fatalf("gap = %#v, %v, %v", gap, relevant, err)
	}
}

func TestMattermostInvalidationsRejectUnboundedRouting(t *testing.T) {
	for _, payload := range []string{
		`{"event":"posted","seq":1,"broadcast":{}}`,
		`{"event":"posted","seq":-1,"broadcast":{"channel_id":"channelid00000000000000000"}}`,
		`{"event":"posted\nsecret","seq":1,"broadcast":{"channel_id":"channelid00000000000000000"}}`,
	} {
		if _, _, err := parseMattermostInvalidation([]byte(payload), 0, false); err == nil {
			t.Fatalf("parseMattermostInvalidation(%s) succeeded", payload)
		}
	}
}

func TestMattermostInvalidationResetSurvivesReconnectAndIrrelevantGap(t *testing.T) {
	t.Parallel()
	stream := EventStream{last: 42, reconnects: 1}
	stream.resetSequenceAfterReconnect()
	if stream.last != 0 || stream.sequenceSeen || !stream.pendingReset {
		t.Fatalf("reconnect state = %#v", &stream)
	}
	_, relevant := stream.recordInvalidation(Invalidation{
		Event: "typing", Sequence: 3, Reset: true,
	}, false)
	if relevant || !stream.pendingReset || stream.last != 3 {
		t.Fatalf("irrelevant gap state = %#v", &stream)
	}
	result, relevant := stream.recordInvalidation(Invalidation{
		Event: "posted", ConversationID: "channelid00000000000000000", Sequence: 4,
	}, true)
	if !relevant || !result.Reset || stream.pendingReset || stream.reconnects != 0 {
		t.Fatalf("recovered invalidation = %#v, stream=%#v", result, &stream)
	}
}

func TestMattermostTeamInvalidationsForceSnapshotReset(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		broadcast      string
		conversationID string
	}{
		{broadcast: `{}`, conversationID: ""},
		{broadcast: `{"channel_id":"channelid00000000000000000"}`, conversationID: "channelid00000000000000000"},
	} {
		invalidation, relevant, err := parseMattermostInvalidation([]byte(
			`{"event":"channel_created","seq":8,"broadcast":`+test.broadcast+`,"data":{"channel_id":"untrusted"}}`,
		), 7, true)
		if err != nil || !relevant || !invalidation.Reset ||
			invalidation.ConversationID != test.conversationID {
			t.Fatalf("team invalidation = %#v, relevant=%v, error=%v", invalidation, relevant, err)
		}
	}
}

func TestMattermostSequenceZeroStillDetectsTheFirstGap(t *testing.T) {
	t.Parallel()
	channelID := "channelid00000000000000000"
	hello, relevant, err := parseMattermostInvalidation(
		[]byte(`{"event":"hello","seq":0,"broadcast":{}}`), 0, false,
	)
	if err != nil || relevant {
		t.Fatalf("hello = %#v, relevant=%v, error=%v", hello, relevant, err)
	}
	stream := EventStream{}
	stream.recordInvalidation(hello, false)
	gap, relevant, err := parseMattermostInvalidation([]byte(
		`{"event":"posted","seq":2,"broadcast":{"channel_id":"`+channelID+`"}}`,
	), stream.last, stream.sequenceSeen)
	if err != nil || !relevant || !gap.Reset {
		t.Fatalf("first gap = %#v, relevant=%v, error=%v", gap, relevant, err)
	}
}

func TestMattermostCloseInterruptsABlockedRead(t *testing.T) {
	t.Parallel()
	upgraded := make(chan struct{})
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		connection, err := upgrader.Upgrade(writer, request, nil)
		if err != nil {
			return
		}
		close(upgraded)
		defer func() { _ = connection.Close() }()
		for {
			if _, _, err := connection.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	connection, response, err := websocket.DefaultDialer.Dial(
		"ws"+strings.TrimPrefix(server.URL, "http"), nil,
	)
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	<-upgraded
	stream := &EventStream{connection: connection}
	readDone := make(chan error, 1)
	go func() {
		_, err := stream.Next(context.Background())
		readDone <- err
	}()
	deadline := time.Now().Add(time.Second)
	for stream.readMu.TryLock() {
		stream.readMu.Unlock()
		if time.Now().After(deadline) {
			t.Fatal("Next did not start its WebSocket read")
		}
		runtime.Gosched()
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- stream.Close() }()
	select {
	case <-closeDone:
	case <-time.After(time.Second):
		t.Fatal("Close blocked behind the active WebSocket read")
	}
	select {
	case err := <-readDone:
		if err == nil {
			t.Fatal("closed WebSocket read unexpectedly succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not interrupt the active WebSocket read")
	}
}
