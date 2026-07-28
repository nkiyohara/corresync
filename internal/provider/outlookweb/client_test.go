package outlookweb

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nkiyohara/corresync/internal/application"
)

const transportSyntheticBearer = "transport-synthetic-token-0123456789abcdef"

type fakeAuthorizer struct{ calls atomic.Int32 }

func (authorizer *fakeAuthorizer) Apply(request *http.Request) error {
	authorizer.calls.Add(1)
	request.Header.Set("Authorization", "Bearer "+transportSyntheticBearer)
	return nil
}

func testClient(t *testing.T, server *httptest.Server, actionOptions func(*Options)) *Client {
	t.Helper()
	options := Options{
		Origin:     server.URL,
		Authorizer: &fakeAuthorizer{},
		HTTPClient: server.Client(),
		UserAgent:  "corresync/test",
	}
	if actionOptions != nil {
		actionOptions(&options)
	}
	client, err := NewClient(options)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	client.sleep = func(context.Context, time.Duration) error { return nil }
	return client
}

func TestClientCallsRegisteredAction(t *testing.T) {
	t.Parallel()

	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/owa/service.svc" || request.URL.Query().Get("action") != "FindItem" {
			t.Errorf("unexpected request URL: %s", request.URL)
		}
		if request.Header.Get("Action") != "FindItem" ||
			request.Header.Get("Authorization") != "Bearer "+transportSyntheticBearer ||
			request.Header.Get("X-OWA-ActionName") != "FindItemAction" ||
			request.Header.Get("client-request-id") == "" {
			t.Errorf("missing protocol headers: %v", request.Header)
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("ReadAll() error = %v", err)
		}
		if string(body) != `{"query":"synthetic"}` {
			t.Errorf("request body = %s", body)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	client := testClient(t, server, nil)
	var response struct {
		OK bool `json:"ok"`
	}
	if err := client.Call(
		context.Background(),
		FindItem,
		map[string]string{"query": "synthetic"},
		&response,
	); err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if !response.OK {
		t.Fatal("Call() did not decode response")
	}
}

func TestClientRoutesExplicitSharedMailboxWithoutChangingOrigin(t *testing.T) {
	t.Parallel()

	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("X-AnchorMailbox") != "shared@example.invalid" ||
			request.Header.Get("X-OWA-ExplicitLogonUser") != "shared@example.invalid" {
			t.Errorf("missing explicit mailbox headers: %v", request.Header)
		}
		if request.URL.Path != "/owa/service.svc" {
			t.Errorf("request escaped configured origin: %s", request.URL)
		}
		_, _ = writer.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	client := testClient(t, server, func(options *Options) {
		options.Mailbox = "shared@example.invalid"
	})
	if err := client.Call(t.Context(), FindItem, struct{}{}, &struct{}{}); err != nil {
		t.Fatalf("Call() error = %v", err)
	}
}

func TestClientRetriesReadsButNeverWrites(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		call := calls.Add(1)
		if call < 3 {
			writer.Header().Set("Retry-After", "0")
			writer.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = writer.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	client := testClient(t, server, nil)
	var response map[string]bool
	if err := client.Call(context.Background(), FindItem, struct{}{}, &response); err != nil {
		t.Fatalf("read Call() error = %v", err)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("read calls = %d, want 3", got)
	}

	calls.Store(0)
	if err := client.Call(context.Background(), CreateItem, struct{}{}, &response); !errors.Is(err, application.ErrWriteOutcomeUnknown) {
		t.Fatalf("write Call() error = %v, want unknown outcome", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("write calls = %d, want 1", got)
	}
}

func TestClientMarksAmbiguousWriteTransportFailure(t *testing.T) {
	t.Parallel()

	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	client := testClient(t, server, nil)
	server.Close()
	err := client.Call(context.Background(), CreateItem, struct{}{}, nil)
	if !errors.Is(err, application.ErrWriteOutcomeUnknown) {
		t.Fatalf("Call() error = %v, want ErrWriteOutcomeUnknown", err)
	}
}

func TestClientClassifiesSessionExpiryAndSafeHTTPError(t *testing.T) {
	t.Parallel()

	status := atomic.Int32{}
	status.Store(http.StatusUnauthorized)
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("request-id", "synthetic-request-id")
		writer.WriteHeader(int(status.Load()))
		_, _ = writer.Write([]byte(`{"private":"must not enter error"}`))
	}))
	defer server.Close()
	client := testClient(t, server, nil)

	if err := client.Call(context.Background(), FindItem, struct{}{}, nil); !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("Call() error = %v, want ErrSessionExpired", err)
	}
	status.Store(http.StatusBadRequest)
	err := client.Call(context.Background(), FindItem, struct{}{}, nil)
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusBadRequest {
		t.Fatalf("Call() error = %v, want HTTPError 400", err)
	}
	if strings.Contains(err.Error(), "private") {
		t.Fatalf("HTTPError exposed response body: %v", err)
	}
}

func TestClientBoundsBodiesAndRejectsInvalidJSON(t *testing.T) {
	t.Parallel()

	responseBody := atomic.Value{}
	responseBody.Store("not-json")
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(responseBody.Load().(string)))
	}))
	defer server.Close()
	client := testClient(t, server, func(options *Options) {
		options.MaxRequestBytes = 16
		options.MaxResponseBytes = 16
	})

	var response any
	if err := client.Call(context.Background(), FindItem, struct{}{}, &response); err == nil {
		t.Fatal("Call() unexpectedly accepted invalid JSON")
	}
	responseBody.Store(strings.Repeat("x", 17))
	if err := client.Call(context.Background(), FindItem, struct{}{}, &response); !errors.Is(err, ErrResponseTooBig) {
		t.Fatalf("Call() error = %v, want ErrResponseTooBig", err)
	}
	if err := client.Call(
		context.Background(),
		FindItem,
		map[string]string{"payload": strings.Repeat("x", 17)},
		&response,
	); err == nil {
		t.Fatal("Call() unexpectedly accepted oversized request")
	}
}

func TestClientDoesNotFollowRedirects(t *testing.T) {
	t.Parallel()

	var targetCalls atomic.Int32
	target := httptest.NewTLSServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		targetCalls.Add(1)
		if request.Header.Get("Authorization") != "" {
			t.Error("redirect target received authorization")
		}
	}))
	defer target.Close()
	source := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, target.URL, http.StatusFound)
	}))
	defer source.Close()

	client := testClient(t, source, nil)
	if err := client.Call(context.Background(), FindItem, struct{}{}, nil); err == nil {
		t.Fatal("Call() unexpectedly followed redirect")
	}
	if got := targetCalls.Load(); got != 0 {
		t.Fatalf("redirect target calls = %d, want 0", got)
	}
}

func TestClientRejectsUnregisteredActionAndInvalidOptions(t *testing.T) {
	t.Parallel()

	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("server should not be called")
	}))
	defer server.Close()
	client := testClient(t, server, nil)
	if err := client.Call(context.Background(), Action(255), struct{}{}, nil); err == nil {
		t.Fatal("Call() unexpectedly accepted unregistered action")
	}

	for _, options := range []Options{
		{},
		{Origin: "http://outlook.example", Authorizer: &fakeAuthorizer{}},
		{Origin: "https://outlook.example", Authorizer: nil},
		{Origin: "https://outlook.example", Authorizer: &fakeAuthorizer{}, ReadAttempts: 6},
		{Origin: "https://outlook.example", Authorizer: &fakeAuthorizer{}, MaxResponseBytes: 65 << 20},
	} {
		if _, err := NewClient(options); err == nil {
			t.Fatalf("NewClient(%+v) unexpectedly succeeded", options)
		}
	}
}
