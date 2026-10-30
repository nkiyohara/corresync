package main

import (
	"net/url"
	"testing"
)

func TestValidSetupDeepLink(t *testing.T) {
	const prompt = "Set up Corresync.\nReview permissions first."
	encoded := url.QueryEscape(prompt)
	for _, raw := range []string{"codex://new?prompt=" + encoded, "claude://code/new?q=" + encoded} {
		if !validSetupDeepLink(raw, prompt) {
			t.Errorf("rejected composer link %q", raw)
		}
	}
	for _, raw := range []string{
		"codex://new?prompt=" + encoded + "&submit=true",
		"codex://new?prompt=" + encoded + "&prompt=" + encoded,
		"codex://new?prompt=" + encoded + "&path=/tmp",
		"codex://new?prompt=" + encoded + "#fragment",
		"codex://user@new?prompt=" + encoded,
		"codex://new?prompt=changed",
		"claude://code/new?q=" + encoded + "&folder=/tmp",
		"claude://claude.ai/new?q=" + encoded,
		"claude://code/new?q=%zz",
		"other://new?prompt=" + encoded,
		"codex://new?prompt=",
	} {
		if validSetupDeepLink(raw, prompt) {
			t.Errorf("accepted unsafe or changed link %q", raw)
		}
	}
	if validSetupDeepLink("codex://new?prompt=", "") {
		t.Error("accepted empty request")
	}
}
