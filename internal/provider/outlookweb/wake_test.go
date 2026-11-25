package outlookweb

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/nkiyohara/corresync/internal/application"
)

type wakeRoundTripper func(*http.Request) (*http.Response, error)

func (function wakeRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func wakeOWAClient(
	t *testing.T,
	authorizer Authorizer,
	transport http.RoundTripper,
	timeout time.Duration,
) *Client {
	t.Helper()
	client, err := NewClient(Options{
		Origin:     "https://outlook.example.invalid",
		Authorizer: authorizer,
		HTTPClient: &http.Client{Transport: transport, Timeout: timeout},
		UserAgent:  "corresync/test",
	})
	if err != nil {
		t.Fatal(err)
	}
	client.sleep = func(context.Context, time.Duration) error { return nil }
	return client
}

func TestClientNetworkLossAfterWakeIsNotSessionExpiryAndRecovers(t *testing.T) {
	t.Parallel()
	var offline atomic.Bool
	var calls atomic.Int32
	client := wakeOWAClient(t, &fakeAuthorizer{}, wakeRoundTripper(func(
		*http.Request,
	) (*http.Response, error) {
		calls.Add(1)
		if offline.Load() {
			return nil, &net.DNSError{
				Err: "no such host", Name: "outlook.example.invalid", IsTemporary: true,
			}
		}
		return &http.Response{
			StatusCode: http.StatusOK, Header: make(http.Header),
			Body: io.NopCloser(strings.NewReader(`{"ok":true}`)),
		}, nil
	}), 0)

	offline.Store(true)
	err := client.Call(t.Context(), FindItem, struct{}{}, nil)
	if err == nil || errors.Is(err, ErrSessionExpired) ||
		calls.Load() != defaultReadAttempts {
		t.Fatalf("offline read = %v, calls = %d", err, calls.Load())
	}
	if _, rejected := application.ProviderAuthenticationReason(err); rejected {
		t.Fatalf("network loss was classified as authentication: %v", err)
	}

	calls.Store(0)
	err = client.Call(t.Context(), CreateItem, struct{}{}, nil)
	if !errors.Is(err, application.ErrWriteOutcomeUnknown) || calls.Load() != 1 {
		t.Fatalf("offline write = %v, calls = %d", err, calls.Load())
	}

	offline.Store(false)
	calls.Store(0)
	if err := client.Call(t.Context(), FindItem, struct{}{}, nil); err != nil ||
		calls.Load() != 1 {
		t.Fatalf("read after reconnect = %v, calls = %d", err, calls.Load())
	}
}

func TestClientHalfOpenConnectionTimesOutWithoutHangingOrReplayingWrites(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	client := wakeOWAClient(t, &fakeAuthorizer{}, wakeRoundTripper(func(
		request *http.Request,
	) (*http.Response, error) {
		calls.Add(1)
		<-request.Context().Done()
		return nil, request.Context().Err()
	}), 2*time.Millisecond)

	err := client.Call(t.Context(), FindItem, struct{}{}, nil)
	if err == nil || errors.Is(err, ErrSessionExpired) ||
		calls.Load() != defaultReadAttempts {
		t.Fatalf("half-open read = %v, calls = %d", err, calls.Load())
	}
	calls.Store(0)
	err = client.Call(t.Context(), CreateItem, struct{}{}, nil)
	if !errors.Is(err, application.ErrWriteOutcomeUnknown) || calls.Load() != 1 {
		t.Fatalf("half-open write = %v, calls = %d", err, calls.Load())
	}
}

// When the browser that owns the session died during sleep, a write fails
// before any request leaves the process. That is a definite sign-in action,
// not an unknown write outcome.
func TestClientBrowserOwnerLossBeforeWriteIsDefiniteAndNotDispatched(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	client := wakeOWAClient(t, &failingAuthorizer{}, wakeRoundTripper(func(
		*http.Request,
	) (*http.Response, error) {
		calls.Add(1)
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
	}), 0)
	err := client.Call(t.Context(), CreateItem, struct{}{}, nil)
	reason, rejected := application.ProviderAuthenticationReason(err)
	if !rejected || reason != application.AuthenticationReasonInteractionRequired ||
		errors.Is(err, application.ErrWriteOutcomeUnknown) || calls.Load() != 0 {
		t.Fatalf("write after browser loss = %v, reason = %q, calls = %d", err, reason, calls.Load())
	}
}
