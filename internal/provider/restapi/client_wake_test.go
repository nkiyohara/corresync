package restapi

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

// switchableNetwork models a host whose network disappears and returns. It
// counts every request that reached the transport.
type switchableNetwork struct {
	offline atomic.Pointer[error]
	calls   atomic.Int32
}

func (network *switchableNetwork) setOffline(failure error) {
	network.offline.Store(&failure)
}

func (network *switchableNetwork) setOnline() {
	network.offline.Store(nil)
}

func (network *switchableNetwork) RoundTrip(*http.Request) (*http.Response, error) {
	network.calls.Add(1)
	if failure := network.offline.Load(); failure != nil {
		return nil, *failure
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
	}, nil
}

type resetBody struct{}

func (resetBody) Read([]byte) (int, error) {
	return 0, &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}
}

func (resetBody) Close() error { return nil }

func TestClientReadRecoversAfterNetworkLossAndWakeWithoutReauthentication(t *testing.T) {
	t.Parallel()
	failures := map[string]error{
		"dial refused": &net.OpError{
			Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED,
		},
		"DNS unavailable": &net.DNSError{
			Err: "no such host", Name: "api.example.invalid", IsTemporary: true,
		},
		"network unreachable": &net.OpError{
			Op: "dial", Net: "tcp", Err: syscall.ENETUNREACH,
		},
	}
	for name, failure := range failures {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			network := &switchableNetwork{}
			client, err := New(Options{
				BaseURL: "https://api.example.invalid/v1",
				HTTP:    &http.Client{Transport: network},
			})
			if err != nil {
				t.Fatal(err)
			}
			now := time.Date(2026, time.November, 25, 23, 0, 0, 0, time.UTC)
			client.resilience.now = func() time.Time { return now }
			client.resilience.sleep = func(context.Context, time.Duration) error { return nil }
			read := func() error {
				_, callErr := client.DoJSON(
					t.Context(), http.MethodGet, "messages", nil, nil, nil,
					false, nil, http.StatusOK,
				)
				return callErr
			}

			network.setOffline(failure)
			err = read()
			if err == nil || network.calls.Load() != defaultReadAttempts {
				t.Fatalf("offline read error = %v calls = %d", err, network.calls.Load())
			}
			if _, rejected := application.ProviderAuthenticationReason(err); rejected {
				t.Fatalf("network loss was classified as authentication: %v", err)
			}
			if strings.Contains(err.Error(), "api.example.invalid") {
				t.Fatalf("offline read exposed transport detail: %v", err)
			}
			// A second read while the circuit is open must not touch the
			// provider: overdue callers after wake cannot stampede it.
			var open *CircuitOpenError
			if err := read(); !errors.As(err, &open) ||
				network.calls.Load() != defaultReadAttempts {
				t.Fatalf("open circuit read error = %v calls = %d", err, network.calls.Load())
			}

			// The machine sleeps for eight hours, wakes, and the network returns.
			now = now.Add(8 * time.Hour)
			network.setOnline()
			if err := read(); err != nil || network.calls.Load() != defaultReadAttempts+1 {
				t.Fatalf("read after wake error = %v calls = %d", err, network.calls.Load())
			}
		})
	}
}

func TestClientReadSurvivesConnectionResetMidResponse(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	client, err := New(Options{
		BaseURL: "https://api.example.invalid/v1",
		HTTP: &http.Client{Transport: roundTripperFunc(func(
			*http.Request,
		) (*http.Response, error) {
			if calls.Add(1) == 1 {
				return &http.Response{
					StatusCode: http.StatusOK, Header: make(http.Header),
					Body: resetBody{},
				}, nil
			}
			return &http.Response{
				StatusCode: http.StatusOK, Header: make(http.Header),
				Body: io.NopCloser(strings.NewReader(`{"ok":true}`)),
			}, nil
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	client.resilience.sleep = func(context.Context, time.Duration) error { return nil }
	var response struct {
		OK bool `json:"ok"`
	}
	if _, err := client.DoJSON(
		t.Context(), http.MethodGet, "messages", nil, nil, &response,
		false, nil, http.StatusOK,
	); err != nil || !response.OK || calls.Load() != 2 {
		t.Fatalf("reset read = %+v error = %v calls = %d", response, err, calls.Load())
	}
}

func TestClientWriteIsNeverReplayedAfterConnectionResetMidResponse(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	client, err := New(Options{
		BaseURL: "https://api.example.invalid/v1",
		HTTP: &http.Client{Transport: roundTripperFunc(func(
			*http.Request,
		) (*http.Response, error) {
			calls.Add(1)
			return &http.Response{
				StatusCode: http.StatusCreated, Header: make(http.Header),
				Body: resetBody{},
			}, nil
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.DoJSON(
		t.Context(), http.MethodPost, "messages", nil,
		map[string]string{"subject": "synthetic"}, nil, true, nil,
		http.StatusCreated,
	)
	if !errors.Is(err, application.ErrWriteOutcomeUnknown) || calls.Load() != 1 {
		t.Fatalf("reset write error = %v calls = %d", err, calls.Load())
	}
}

// blockingNetwork models a half-open connection: the request is accepted by
// the local stack but no response ever arrives.
func blockingNetwork(calls *atomic.Int32) http.RoundTripper {
	return roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		calls.Add(1)
		<-request.Context().Done()
		return nil, request.Context().Err()
	})
}

func TestClientHalfOpenConnectionTimesOutInsteadOfHanging(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	client, err := New(Options{
		BaseURL: "https://api.example.invalid/v1",
		HTTP: &http.Client{
			Transport: blockingNetwork(&calls),
			Timeout:   2 * time.Millisecond,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	client.resilience.sleep = func(context.Context, time.Duration) error { return nil }

	_, err = client.DoJSON(
		t.Context(), http.MethodGet, "messages", nil, nil, nil,
		false, nil, http.StatusOK,
	)
	if err == nil || calls.Load() != defaultReadAttempts {
		t.Fatalf("half-open read error = %v calls = %d", err, calls.Load())
	}
	if _, rejected := application.ProviderAuthenticationReason(err); rejected {
		t.Fatalf("read timeout was classified as authentication: %v", err)
	}

	client.resilience.Succeed()
	calls.Store(0)
	_, err = client.DoJSON(
		t.Context(), http.MethodPost, "messages", nil,
		map[string]string{"subject": "synthetic"}, nil, true, nil,
		http.StatusCreated,
	)
	if !errors.Is(err, application.ErrWriteOutcomeUnknown) || calls.Load() != 1 {
		t.Fatalf("half-open write error = %v calls = %d", err, calls.Load())
	}
}

func TestClientCallerCancellationMidFlightIsNotRetried(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	started := make(chan struct{})
	client, err := New(Options{
		BaseURL: "https://api.example.invalid/v1",
		HTTP: &http.Client{Transport: roundTripperFunc(func(
			request *http.Request,
		) (*http.Response, error) {
			if calls.Add(1) == 1 {
				close(started)
			}
			<-request.Context().Done()
			return nil, request.Context().Err()
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		<-started
		cancel()
	}()
	_, err = client.DoJSON(
		ctx, http.MethodGet, "messages", nil, nil, nil,
		false, nil, http.StatusOK,
	)
	if !errors.Is(err, context.Canceled) || calls.Load() != 1 {
		t.Fatalf("cancelled read error = %v calls = %d", err, calls.Load())
	}
	// Cancellation is the caller's decision, not provider evidence.
	if err := client.resilience.BeforeRead(); err != nil {
		t.Fatalf("caller cancellation opened the circuit: %v", err)
	}
}

// A token source that is consulted before dispatch (for example, an OAuth
// refresh after the access token expired while the machine slept) can reject
// the grant. That outcome is definitive: the provider never received the
// request, so it is neither retried nor reported as an unknown write.
func TestClientDefinitiveAuthorizationFailureBeforeDispatchIsNotRetriedOrAmbiguous(
	t *testing.T,
) {
	t.Parallel()
	for _, write := range []bool{false, true} {
		name := "read"
		if write {
			name = "write"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int32
			client, err := New(Options{
				BaseURL: "https://api.example.invalid/v1",
				HTTP: &http.Client{Transport: roundTripperFunc(func(
					*http.Request,
				) (*http.Response, error) {
					calls.Add(1)
					return nil, application.NewProviderAuthenticationFailure(
						application.AuthenticationReasonGrantRevoked,
						errors.New("synthetic private refresh response"),
					)
				})},
			})
			if err != nil {
				t.Fatal(err)
			}
			var delays atomic.Int32
			client.resilience.sleep = func(context.Context, time.Duration) error {
				delays.Add(1)
				return nil
			}
			method := http.MethodGet
			var body any
			if write {
				method = http.MethodPost
				body = map[string]string{"subject": "synthetic"}
			}
			_, err = client.DoJSON(
				t.Context(), method, "messages", nil, body, nil, write, nil,
				http.StatusOK, http.StatusCreated,
			)
			reason, rejected := application.ProviderAuthenticationReason(err)
			if !rejected || reason != application.AuthenticationReasonGrantRevoked ||
				errors.Is(err, application.ErrWriteOutcomeUnknown) ||
				calls.Load() != 1 || delays.Load() != 0 ||
				strings.Contains(err.Error(), "synthetic private refresh response") ||
				strings.Contains(err.Error(), "api.example.invalid") {
				t.Fatalf(
					"authorization failure = %v reason = %q calls = %d delays = %d",
					err, reason, calls.Load(), delays.Load(),
				)
			}
			if err := client.resilience.BeforeRead(); err != nil {
				t.Fatalf("authentication failure opened the availability circuit: %v", err)
			}
		})
	}
}
