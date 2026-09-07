package browser

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

func TestTeamsConversationScriptRequiresRenderedIdentity(t *testing.T) {
	ctx := teamsScriptTestContext(t)
	cases := []struct {
		name    string
		html    string
		chat    string
		team    string
		channel string
		valid   bool
	}{
		{name: "matching", html: `<header data-tid="chat-pane-header" data-chat-id="chat-A">Conversation A</header>`, chat: "chat-A", valid: true},
		{name: "stale", html: `<header data-tid="chat-pane-header" data-chat-id="chat-A">Conversation A</header>`, chat: "chat-B"},
		{name: "missing", html: `<header data-tid="chat-pane-header">Conversation A</header>`, chat: "chat-A"},
		{name: "hidden-stale", html: `<header data-tid="chat-pane-header" data-chat-id="chat-A" hidden>Old</header><header data-tid="chat-pane-header" data-chat-id="chat-B">Current</header>`, chat: "chat-B", valid: true},
		{name: "ambiguous", html: `<header data-tid="chat-pane-header" data-chat-id="chat-A">First</header><header data-tid="chat-pane-header" data-chat-id="chat-A">Second</header>`, chat: "chat-A"},
		{name: "channel", html: `<header data-tid="channel-header" data-team-id="team-A" data-channel-id="channel-A">Channel A</header>`, team: "team-A", channel: "channel-A", valid: true},
		{name: "wrong-team", html: `<header data-tid="channel-header" data-team-id="team-A" data-channel-id="channel-A">Channel A</header>`, team: "team-B", channel: "channel-A"},
		{name: "conflicting-team", html: `<header data-tid="channel-header" data-team-id="team-A" data-group-id="team-B" data-channel-id="channel-A">Channel A</header>`, team: "team-A", channel: "channel-A"},
		{name: "conflicting-ancestor", html: `<main data-chat-id="chat-B"><header data-tid="chat-pane-header" data-chat-id="chat-A">Conversation A</header></main>`, chat: "chat-A"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			expression, err := teamsCallExpression(teamsCurrentConversationScript, test.chat, test.team, test.channel)
			if err != nil {
				t.Fatal(err)
			}
			var snapshot teamsConversationSnapshot
			evaluateTeamsFixture(t, ctx, test.html, expression, &snapshot)
			if !test.valid {
				if snapshot.State != "unknown" || len(snapshot.Rows) != 0 {
					t.Fatalf("unproven identity returned data: %#v", snapshot)
				}
				return
			}
			if snapshot.State != "rows" || len(snapshot.Rows) != 1 {
				t.Fatalf("matching identity = %#v", snapshot)
			}
			row := snapshot.Rows[0]
			if row.ChatID != test.chat || row.TeamID != test.team || row.ChannelID != test.channel {
				t.Fatalf("observed identity = %#v", row)
			}
		})
	}
}

func TestTeamsMessageScriptRequiresConversationAndThreadEvidence(t *testing.T) {
	ctx := teamsScriptTestContext(t)
	cases := []struct {
		name    string
		pane    string
		row     string
		root    string
		message string
		valid   bool
	}{
		{name: "matching", pane: `data-chat-id="chat-A"`, message: "message-A", valid: true},
		{name: "stale", pane: `data-chat-id="chat-B"`, message: "message-A"},
		{name: "missing", message: "message-A"},
		{name: "foreign-row", pane: `data-chat-id="chat-A"`, row: `data-chat-id="chat-B"`, message: "message-A"},
		{name: "missing-thread", pane: `data-chat-id="chat-A"`, root: "root-A", message: "message-A"},
		{name: "wrong-thread", pane: `data-chat-id="chat-A"`, row: `data-parent-message-id="root-B"`, root: "root-A", message: "message-A"},
		{name: "matching-thread", pane: `data-chat-id="chat-A"`, row: `data-parent-message-id="root-A"`, root: "root-A", message: "message-A", valid: true},
		{name: "root-message", pane: `data-chat-id="chat-A"`, root: "root-A", message: "root-A", valid: true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			fixture := fmt.Sprintf(`<main %s><section data-tid="message-pane"><article data-message-id="%s" %s data-author-id="actor-A"><div data-tid="message-body">Synthetic message</div><time datetime="2026-01-01T00:00:00Z">time</time></article></section></main>`, test.pane, test.message, test.row)
			expression, err := teamsCallExpression(teamsMessageSnapshotScript, true, "chat-A", "", "", test.root, test.message)
			if err != nil {
				t.Fatal(err)
			}
			var snapshot teamsMessageSnapshot
			evaluateTeamsFixture(t, ctx, fixture, expression, &snapshot)
			if !test.valid {
				if snapshot.State != "unknown" || len(snapshot.Rows) != 0 {
					t.Fatalf("unproven route returned data: %#v", snapshot)
				}
				return
			}
			if snapshot.State != "rows" || len(snapshot.Rows) != 1 || snapshot.Rows[0].ChatID != "chat-A" {
				t.Fatalf("matching route = %#v", snapshot)
			}
			if test.name == "root-message" && snapshot.Rows[0].ThreadRootID != "" {
				t.Fatal("script synthesized a parent for the root message")
			}
		})
	}
}

func TestTeamsScriptsDistinguishUnknownCountsFromObservedZero(t *testing.T) {
	ctx := teamsScriptTestContext(t)
	cases := []struct {
		name      string
		attribute string
		known     bool
		count     int
	}{
		{name: "missing"},
		{name: "empty", attribute: `=""`},
		{name: "whitespace", attribute: `=" "`},
		{name: "malformed", attribute: `="unknown"`},
		{name: "negative", attribute: `="-1"`},
		{name: "fraction", attribute: `="0.5"`},
		{name: "overflow", attribute: `="1000001"`},
		{name: "zero", attribute: `="0"`, known: true},
		{name: "positive", attribute: `="42"`, known: true, count: 42},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			memberAttribute, reactionAttribute := "", ""
			if test.attribute != "" {
				memberAttribute = "data-member-count" + test.attribute
				reactionAttribute = "data-count" + test.attribute
			}
			fixture := fmt.Sprintf(`<main data-chat-id="chat-A"><header data-tid="chat-pane-header" %s>Conversation A</header><section data-tid="message-pane"><article data-message-id="message-A" data-author-id="actor-A"><div data-tid="message-body">Synthetic message</div><time datetime="2026-01-01T00:00:00Z">time</time><button data-reaction-type="like" %s>Like</button></article></section></main><button data-tid="app-bar-chat">Chat</button><div data-tid="chat-list"><a data-tid="chat-list-item" data-chat-id="chat-A" href="https://teams.microsoft.com/l/chat/chat-A/conversations" %s>Conversation A</a></div>`, memberAttribute, reactionAttribute, memberAttribute)
			for _, script := range []struct {
				name, function string
				arguments      []any
			}{
				{name: "current", function: teamsCurrentConversationScript, arguments: []any{"chat-A", "", ""}},
				{name: "list", function: teamsConversationSnapshotScript, arguments: []any{"chat"}},
			} {
				expression, err := teamsCallExpression(script.function, script.arguments...)
				if err != nil {
					t.Fatal(err)
				}
				var snapshot teamsConversationSnapshot
				evaluateTeamsFixture(t, ctx, fixture, expression, &snapshot)
				if snapshot.State != "rows" || len(snapshot.Rows) != 1 {
					t.Fatalf("%s snapshot = %#v", script.name, snapshot)
				}
				row := snapshot.Rows[0]
				if row.MemberCountKnown != test.known || row.MemberCount != test.count {
					t.Fatalf("%s count = %d, known = %t", script.name, row.MemberCount, row.MemberCountKnown)
				}
			}
			expression, err := teamsCallExpression(teamsMessageSnapshotScript, true, "chat-A", "", "", "", "message-A")
			if err != nil {
				t.Fatal(err)
			}
			var snapshot teamsMessageSnapshot
			evaluateTeamsFixture(t, ctx, fixture, expression, &snapshot)
			if snapshot.State != "rows" || len(snapshot.Rows) != 1 || len(snapshot.Rows[0].Reactions) != 1 {
				t.Fatalf("message snapshot = %#v", snapshot)
			}
			reaction := snapshot.Rows[0].Reactions[0]
			if reaction.CountKnown != test.known || reaction.Count != test.count {
				t.Fatalf("reaction count = %#v", reaction)
			}
		})
	}
}

func teamsScriptTestContext(t *testing.T) context.Context {
	t.Helper()
	executable, err := ResolveExecutable("")
	if err != nil {
		t.Skipf("Chromium unavailable: %v", err)
	}
	timeoutContext, cancelTimeout := context.WithTimeout(t.Context(), 30*time.Second)
	allocator, cancelAllocator := chromedp.NewExecAllocator(timeoutContext, allocatorOptions(executable, t.TempDir(), true)...)
	ctx, cancelBrowser := chromedp.NewContext(allocator)
	t.Cleanup(func() { cancelBrowser(); cancelAllocator(); cancelTimeout() })
	return ctx
}

func evaluateTeamsFixture(t *testing.T, ctx context.Context, html, expression string, output any) {
	t.Helper()
	// A synthetic loopback origin keeps URL parsing realistic without any
	// navigation or request to Teams. Fixture links are never activated.
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(writer, html)
	}))
	defer server.Close()
	if err := chromedp.Run(ctx, chromedp.Navigate(server.URL), chromedp.Evaluate(expression, output, teamsAwaitPromise)); err != nil {
		t.Fatal(err)
	}
}
