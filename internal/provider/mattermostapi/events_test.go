package mattermostapi

import "testing"

func TestMattermostInvalidationsDeduplicateAndRecoverGaps(t *testing.T) {
	channelID := "channelid00000000000000000"
	first, relevant, err := parseMattermostInvalidation([]byte(
		`{"event":"posted","seq":7,"broadcast":{"channel_id":"`+channelID+`"},"data":{"post":"untrusted"}}`,
	), 0)
	if err != nil || !relevant || first.Reset || first.ConversationID != channelID {
		t.Fatalf("first invalidation = %#v, %v, %v", first, relevant, err)
	}
	_, relevant, err = parseMattermostInvalidation([]byte(
		`{"event":"posted","seq":7,"broadcast":{"channel_id":"`+channelID+`"}}`,
	), 7)
	if err != nil || relevant {
		t.Fatalf("duplicate = relevant %v, error %v", relevant, err)
	}
	gap, relevant, err := parseMattermostInvalidation([]byte(
		`{"event":"post_deleted","seq":9,"broadcast":{"channel_id":"`+channelID+`"}}`,
	), 7)
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
		if _, _, err := parseMattermostInvalidation([]byte(payload), 0); err == nil {
			t.Fatalf("parseMattermostInvalidation(%s) succeeded", payload)
		}
	}
}
