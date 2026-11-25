package loginui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nkiyohara/corresync/internal/application"
)

func TestLoginUIBoundaries(t *testing.T) {
	calls := 0
	server, err := New(t.Context(), 0, func(_ context.Context, action *application.GraphicalLoginAction) (Result, error) {
		calls++
		if action != nil && action.Type == "cancel" {
			return Result{Status: "cancelled"}, nil
		}
		return Result{Status: "pending"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.listener.Close() })
	host := "127.0.0.1:" + strconv.Itoa(server.Port())
	request := func(hostname, origin, token, controller, body string) int {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "http://"+hostname+"/action", strings.NewReader(body))
		req.Header.Set("Origin", origin)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Login-Controller", controller)
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		server.ServeHTTP(response, req)
		if response.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("cache allowed")
		}
		return response.Code
	}
	controller := strings.Repeat("a", 32)
	for _, test := range []struct {
		name, host, origin, token, controller, body string
		want                                        int
	}{
		{"host", "evil.invalid", "http://evil.invalid", server.token, controller, `{"type":"refresh"}`, 403},
		{"origin", host, "https://evil.invalid", server.token, controller, `{"type":"refresh"}`, 403},
		{"token", host, "http://" + host, "wrong", controller, `{"type":"refresh"}`, 401},
		{"controller", host, "http://" + host, server.token, "bad", `{"type":"refresh"}`, 403},
		{"unknown field", host, "http://" + host, server.token, controller, `{"type":"refresh","url":"https://example.invalid"}`, 400},
		{"overflow", host, "http://" + host, server.token, controller, `{"type":"text","text":"` + strings.Repeat("x", 9000) + `"}`, 400},
		{"initial input", host, "http://" + host, server.token, controller, `{"type":"click","x":1,"y":1}`, 409},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := request(test.host, test.origin, test.token, test.controller, test.body); got != test.want {
				t.Fatalf("status %d want %d", got, test.want)
			}
		})
	}
	if calls != 0 {
		t.Fatal("unauthorized interaction")
	}
	if got := request(host, "http://"+host, server.token, controller, `{"type":"refresh"}`); got != 200 {
		t.Fatal(got)
	}
	if got := request(host, "http://"+host, server.token, strings.Repeat("b", 32), `{"type":"refresh"}`); got != 409 {
		t.Fatal(got)
	}
	if got := request(host, "http://"+host, server.token, controller, `{"type":"cancel"}`); got != 200 {
		t.Fatal(got)
	}
	if got := request(host, "http://"+host, server.token, controller, `{"type":"refresh"}`); got != 401 {
		t.Fatal(got)
	}
	if calls != 2 {
		t.Fatal(calls)
	}
}

func TestLoginUIExpiryCancelsOnlyStartedInteraction(t *testing.T) {
	for _, started := range []bool{false, true} {
		calls := 0
		server, err := New(t.Context(), 0, func(_ context.Context, action *application.GraphicalLoginAction) (Result, error) {
			calls++
			if action == nil || action.Type != "cancel" {
				t.Error("unexpected input")
			}
			return Result{}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		server.started = started
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if err := server.Run(ctx); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		if (calls == 1) != started || server.token != "" {
			t.Fatalf("calls=%d token retained=%v", calls, server.token != "")
		}
	}
}

func newTestRequester(t *testing.T, server *Server) func(body string) int {
	t.Helper()
	host := "127.0.0.1:" + strconv.Itoa(server.Port())
	return func(body string) int {
		request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "http://"+host+"/action", strings.NewReader(body))
		request.Header.Set("Origin", "http://"+host)
		request.Header.Set("Authorization", "Bearer "+server.token)
		request.Header.Set("X-Login-Controller", strings.Repeat("a", 32))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		return response.Code
	}
}

// A login that never started cannot be recovered from the page, so the server
// ends with the reason and sends no cancellation for an interaction that
// does not exist.
func TestLoginUIFailedStartEndsWithItsReason(t *testing.T) {
	failure := errors.New("synthetic start failure")
	calls := 0
	server, err := New(t.Context(), 0, func(context.Context, *application.GraphicalLoginAction) (Result, error) {
		calls++
		return Result{}, failure
	})
	if err != nil {
		t.Fatal(err)
	}
	request := newTestRequester(t, server)
	if got := request(`{"type":"refresh"}`); got != http.StatusBadGateway {
		t.Fatal(got)
	}
	if err := server.Run(t.Context()); !errors.Is(err, failure) {
		t.Fatalf("Run() = %v", err)
	}
	if calls != 1 || server.Outcome() != "" {
		t.Fatalf("calls=%d outcome=%q", calls, server.Outcome())
	}
}

func TestLoginUIEndsAfterRepeatedUnreadableScreens(t *testing.T) {
	failure := errors.New("synthetic expired session")
	var actions []string
	failing := false
	server, err := New(t.Context(), 0, func(_ context.Context, action *application.GraphicalLoginAction) (Result, error) {
		if action == nil {
			return Result{Status: "pending"}, nil
		}
		actions = append(actions, action.Type)
		if failing && action.Type == "refresh" {
			return Result{}, failure
		}
		return Result{Status: "pending"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	request := newTestRequester(t, server)
	if got := request(`{"type":"refresh"}`); got != http.StatusOK {
		t.Fatal(got)
	}
	// Failures separated by a good read never add up.
	failing = true
	for range maxUnreadableScreens - 1 {
		if got := request(`{"type":"refresh"}`); got != http.StatusBadGateway {
			t.Fatal(got)
		}
	}
	failing = false
	if got := request(`{"type":"refresh"}`); got != http.StatusOK {
		t.Fatal(got)
	}
	failing = true
	for range maxUnreadableScreens {
		if got := request(`{"type":"refresh"}`); got != http.StatusBadGateway {
			t.Fatal(got)
		}
	}
	if got := request(`{"type":"refresh"}`); got != http.StatusUnauthorized {
		t.Fatalf("ended view still accepts requests: %d", got)
	}
	if err := server.Run(t.Context()); !errors.Is(err, failure) {
		t.Fatalf("Run() = %v", err)
	}
	// The remote login is cancelled exactly once, by the give-up path.
	if cancels := strings.Count(strings.Join(actions, ","), "cancel"); cancels != 1 || actions[len(actions)-1] != "cancel" {
		t.Fatalf("actions = %v", actions)
	}
}

type pageAnswer struct {
	code   int
	state  string
	result pageResult
}

// newPageRequester posts one action as the paired tab. shown is the frame the
// tab claims to show; zero sends no X-Login-Frame header, like a fresh tab.
func newPageRequester(t *testing.T, server *Server) func(body string, shown uint64) pageAnswer {
	t.Helper()
	host := "127.0.0.1:" + strconv.Itoa(server.Port())
	return func(body string, shown uint64) pageAnswer {
		t.Helper()
		request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "http://"+host+"/action", strings.NewReader(body))
		request.Header.Set("Origin", "http://"+host)
		request.Header.Set("Authorization", "Bearer "+server.token)
		request.Header.Set("X-Login-Controller", strings.Repeat("a", 32))
		request.Header.Set("Content-Type", "application/json")
		if shown != 0 {
			request.Header.Set("X-Login-Frame", strconv.FormatUint(shown, 10))
		}
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		answer := pageAnswer{code: response.Code, state: response.Header().Get("X-Login-State")}
		if response.Code == http.StatusOK {
			if err := json.Unmarshal(response.Body.Bytes(), &answer.result); err != nil {
				t.Fatal(err)
			}
		}
		return answer
	}
}

func syntheticFrame(jpeg, origin string) *application.GraphicalLoginFrame {
	return &application.GraphicalLoginFrame{JPEG: []byte(jpeg), Origin: origin, Width: application.GraphicalLoginWidth, Height: application.GraphicalLoginHeight}
}

// An unchanged screen is not sent to the page again; a changed picture or a
// changed origin is, and a tab that shows no picture (a reload) always gets one.
func TestLoginUISendsAFrameOnlyWhenItChanged(t *testing.T) {
	var next Result
	server, err := New(t.Context(), 0, func(context.Context, *application.GraphicalLoginAction) (Result, error) {
		return next, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	request := newPageRequester(t, server)
	first := syntheticFrame("picture-1", "https://login.example")
	for _, step := range []struct {
		name      string
		next      Result
		shown     uint64
		wantID    uint64
		wantFrame string
		unchanged bool
	}{
		{"first frame", Result{Status: "pending", Frame: first}, 0, 1, "picture-1", false},
		{"same frame again", Result{Status: "pending", Frame: syntheticFrame("picture-1", "https://login.example")}, 1, 1, "", true},
		{"reloaded tab", Result{Status: "pending", Frame: syntheticFrame("picture-1", "https://login.example")}, 0, 1, "picture-1", false},
		{"stale tab", Result{Status: "pending", Frame: syntheticFrame("picture-1", "https://login.example")}, 7, 1, "picture-1", false},
		{"page navigating", Result{Status: "pending"}, 1, 0, "", false},
		{"helper says unchanged", Result{Status: "pending", Unchanged: true}, 1, 1, "", true},
		{"helper says unchanged to a reloaded tab", Result{Status: "pending", Unchanged: true}, 0, 1, "picture-1", false},
		{"new picture", Result{Status: "pending", Frame: syntheticFrame("picture-2", "https://login.example")}, 1, 2, "picture-2", false},
		{"same picture on another origin", Result{Status: "pending", Frame: syntheticFrame("picture-2", "https://other.example")}, 2, 3, "picture-2", false},
	} {
		next = step.next
		answer := request(`{"type":"refresh"}`, step.shown)
		got := answer.result
		if answer.code != http.StatusOK || got.FrameID != step.wantID || got.Unchanged != step.unchanged ||
			(step.wantFrame == "") != (got.Frame == nil) || got.Frame != nil && string(got.Frame.JPEG) != step.wantFrame {
			t.Fatalf("%s: answer = %d %+v", step.name, answer.code, got)
		}
	}
	if got := request(`{"type":"refresh"}`, 1); got.result.Frame == nil || got.result.Frame.Origin != "https://other.example" {
		t.Fatalf("the origin label must follow the picture: %+v", got.result)
	}
	if got := request(`{"type":"refresh"}`, 0); got.code != http.StatusOK {
		t.Fatal(got.code)
	}
	if got := newTestRequesterWithFrame(t, server, "not-a-number"); got != http.StatusBadRequest {
		t.Fatalf("invalid frame header = %d", got)
	}
}

func newTestRequesterWithFrame(t *testing.T, server *Server, frame string) int {
	t.Helper()
	host := "127.0.0.1:" + strconv.Itoa(server.Port())
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "http://"+host+"/action", strings.NewReader(`{"type":"refresh"}`))
	request.Header.Set("Origin", "http://"+host)
	request.Header.Set("Authorization", "Bearer "+server.token)
	request.Header.Set("X-Login-Controller", strings.Repeat("a", 32))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Login-Frame", frame)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	return response.Code
}

// The page shows the time left. It comes from the view's own deadline, or
// from the remote helper when that ends sooner.
func TestLoginUIReportsRemainingLifetime(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	remote := 0
	server, err := New(ctx, 0, func(context.Context, *application.GraphicalLoginAction) (Result, error) {
		return Result{Status: "pending", ExpiresIn: remote}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	request := newPageRequester(t, server)
	if got := request(`{"type":"refresh"}`, 0).result.ExpiresIn; got < 110 || got > 120 {
		t.Fatalf("expiresIn = %d", got)
	}
	remote = 11 * 60 // Out of bounds: ignored.
	if got := request(`{"type":"refresh"}`, 0).result.ExpiresIn; got < 110 || got > 120 {
		t.Fatalf("expiresIn = %d", got)
	}
	remote = 30
	if got := request(`{"type":"refresh"}`, 0).result.ExpiresIn; got < 29 || got > 30 {
		t.Fatalf("expiresIn = %d", got)
	}
	remote = 90 // Never extends the sooner deadline.
	if got := request(`{"type":"refresh"}`, 0).result.ExpiresIn; got < 29 || got > 30 {
		t.Fatalf("expiresIn = %d", got)
	}
}

// A pointer move that fails is dropped: it is not reported as possibly lost
// input and does not count towards giving up on an unreadable screen.
func TestLoginUIDropsAFailedMove(t *testing.T) {
	failure := errors.New("synthetic move failure")
	var actions []string
	server, err := New(t.Context(), 0, func(_ context.Context, action *application.GraphicalLoginAction) (Result, error) {
		if action == nil {
			return Result{Status: "pending"}, nil
		}
		actions = append(actions, action.Type)
		if action.Type != "cancel" {
			return Result{}, failure
		}
		return Result{Status: "cancelled"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	request := newPageRequester(t, server)
	request(`{"type":"refresh"}`, 0)
	for range maxUnreadableScreens + 1 {
		if got := request(`{"type":"move","x":10,"y":20}`, 0); got.code != http.StatusBadGateway || got.state != "dropped" {
			t.Fatalf("move answer = %+v", got)
		}
	}
	if got := request(`{"type":"click","x":10,"y":20}`, 0); got.state != "input-unknown" {
		t.Fatalf("click answer = %+v", got)
	}
	if got := request(`{"type":"move","x":10,"y":20,"text":"x"}`, 0); got.code != http.StatusBadRequest {
		t.Fatalf("move with text = %+v", got)
	}
	if strings.Contains(strings.Join(actions, ","), "cancel") {
		t.Fatalf("failed moves ended the login: %v", actions)
	}
}
