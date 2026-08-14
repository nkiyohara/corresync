package application

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestCanonicalMessageJSONFixtures(t *testing.T) {
	t.Parallel()

	var send MessageSendInput
	decodeMessageContractFixture(t, "message-send-v1.json", &send)
	if send.Account != "" {
		t.Fatal("message send fixture must leave account selection to the calling surface")
	}
	send.Account = testMessageAccount
	if err := send.Validate(); err != nil {
		t.Fatalf("message send fixture validation error = %v", err)
	}
	if send.ReplyToID == "" || len(send.Attachments) != 1 || string(send.Attachments[0].Data) != "synthetic" {
		t.Fatalf("message send fixture lost canonical fields: %+v", send)
	}

	var message Message
	decodeMessageContractFixture(t, "message-v1.json", &message)
	if err := message.Validate(); err != nil {
		t.Fatalf("message fixture validation error = %v", err)
	}
	if message.Summary.ID != "message-synthetic-001" ||
		message.Summary.Provenance.AccountID != testMessageAccount ||
		message.Summary.Provenance.Route != MessagingRouteSlackAPI ||
		len(message.Mentions) != 1 || len(message.Attachments) != 1 {
		t.Fatalf("message fixture lost canonical fields: %+v", message)
	}
}

func decodeMessageContractFixture(t *testing.T, name string, destination any) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "contracts", name)) // #nosec G304 -- fixed synthetic fixture name.
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		t.Fatalf("%s contains trailing JSON: %v", name, err)
	}
}
