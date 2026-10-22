package loginui

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

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
