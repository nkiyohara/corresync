package restapi

import (
	"context"
	"crypto/x509"
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

func TestClientBoundsErrorsAndRejectsRedirects(t *testing.T) {
	t.Parallel()
	server := httptest.NewTLSServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		switch request.URL.Path {
		case "/api/ok":
			_, _ = io.WriteString(writer, `{"ok":true}`)
		case "/api/fail":
			writer.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(
				writer,
				`{"error":{"code":"synthetic_code","message":"private provider text"}}`,
			)
		case "/api/redirect":
			http.Redirect(writer, request, "/api/ok", http.StatusTemporaryRedirect)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig.RootCAs = roots
	client, err := New(Options{
		BaseURL: server.URL + "/api",
		HTTP:    &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		OK bool `json:"ok"`
	}
	if _, err := client.DoJSON(
		t.Context(), http.MethodGet, "ok", nil, nil, &decoded, false, nil,
		http.StatusOK,
	); err != nil || !decoded.OK {
		t.Fatalf("DoJSON() = %#v, %v", decoded, err)
	}
	if _, err := client.DoJSON(
		t.Context(), http.MethodGet, "fail", nil, nil, nil, false, nil,
		http.StatusOK,
	); err == nil ||
		!strings.Contains(err.Error(), "synthetic_code") ||
		strings.Contains(err.Error(), "private provider text") {
		t.Fatalf("bounded provider error = %v", err)
	}
	if _, err := client.DoJSON(
		t.Context(), http.MethodGet, "redirect", nil, nil, nil, false, nil,
		http.StatusOK,
	); err == nil || !strings.Contains(err.Error(), "redirects are not accepted") {
		t.Fatalf("redirect error = %v", err)
	}
}

func TestClientMarksAmbiguousWriteTransportFailure(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	client, err := New(Options{
		BaseURL: "https://api.example.invalid/v1",
		HTTP: &http.Client{Transport: roundTripperFunc(func(
			*http.Request,
		) (*http.Response, error) {
			calls.Add(1)
			return nil, errors.New("private synthetic transport failure")
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.DoJSON(
		context.Background(),
		http.MethodPost,
		"messages",
		nil,
		map[string]string{"subject": "synthetic"},
		nil,
		true,
		nil,
		http.StatusCreated,
	)
	if !errors.Is(err, application.ErrWriteOutcomeUnknown) || calls.Load() != 1 ||
		strings.Contains(err.Error(), "private synthetic transport failure") {
		t.Fatalf("write transport error = %v calls = %d", err, calls.Load())
	}
}

func TestClientRetriesOnlyReadsWithBoundedBackoffAndReplayableBody(t *testing.T) {
	var calls atomic.Int32
	client, err := New(Options{
		BaseURL: "https://api.example.invalid/v1",
		HTTP: &http.Client{Transport: roundTripperFunc(func(
			request *http.Request,
		) (*http.Response, error) {
			call := calls.Add(1)
			body, readErr := io.ReadAll(request.Body)
			if readErr != nil || string(body) != `{"query":"synthetic"}` {
				t.Fatalf("attempt %d body = %q error = %v", call, body, readErr)
			}
			if call < defaultReadAttempts {
				return nil, errors.New("synthetic transient failure")
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
			}, nil
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	var delays []time.Duration
	client.resilience.sleep = func(_ context.Context, delay time.Duration) error {
		delays = append(delays, delay)
		return nil
	}
	var response struct {
		OK bool `json:"ok"`
	}
	_, err = client.DoJSON(
		t.Context(), http.MethodPost, "search", nil,
		map[string]string{"query": "synthetic"}, &response,
		false, nil, http.StatusOK,
	)
	if err != nil || !response.OK || calls.Load() != defaultReadAttempts {
		t.Fatalf("resilient read response = %+v calls = %d error = %v", response, calls.Load(), err)
	}
	wantDelays := []time.Duration{100 * time.Millisecond, 400 * time.Millisecond}
	if len(delays) != len(wantDelays) {
		t.Fatalf("read retry delays = %v", delays)
	}
	for index := range delays {
		if delays[index] != wantDelays[index] {
			t.Fatalf("read retry delays = %v, want %v", delays, wantDelays)
		}
	}
}

func TestClientNeverAcceptsExhaustedTransientReadStatus(t *testing.T) {
	var calls atomic.Int32
	client, err := New(Options{
		BaseURL: "https://api.example.invalid/v1",
		HTTP: &http.Client{Transport: roundTripperFunc(func(
			*http.Request,
		) (*http.Response, error) {
			calls.Add(1)
			return &http.Response{
				StatusCode: http.StatusServiceUnavailable,
				Header:     make(http.Header),
				Body: io.NopCloser(strings.NewReader(
					`{"error":{"code":"temporarily_unavailable"}}`,
				)),
			}, nil
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	client.resilience.sleep = func(context.Context, time.Duration) error { return nil }
	result, err := client.DoJSON(
		t.Context(), http.MethodGet, "messages", nil, nil, nil,
		false, nil, http.StatusOK, http.StatusServiceUnavailable,
	)
	if err == nil || result.Status != 0 || calls.Load() != defaultReadAttempts {
		t.Fatalf("exhausted transient result = %+v calls = %d error = %v", result, calls.Load(), err)
	}
}

func TestClientCircuitIsAccountLocalAndRecoversAfterBoundedDelay(t *testing.T) {
	var calls atomic.Int32
	client, err := New(Options{
		BaseURL: "https://api.example.invalid/v1",
		HTTP: &http.Client{Transport: roundTripperFunc(func(
			*http.Request,
		) (*http.Response, error) {
			if calls.Add(1) <= defaultReadAttempts {
				return nil, errors.New("synthetic provider outage")
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
			}, nil
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.August, 14, 22, 0, 0, 0, time.UTC)
	client.resilience.now = func() time.Time { return now }
	client.resilience.sleep = func(context.Context, time.Duration) error { return nil }
	read := func() error {
		_, callErr := client.DoJSON(
			t.Context(), http.MethodGet, "messages", nil, nil, nil,
			false, nil, http.StatusOK,
		)
		return callErr
	}
	if err := read(); err == nil || calls.Load() != defaultReadAttempts ||
		strings.Contains(err.Error(), "synthetic provider outage") {
		t.Fatalf("exhausted read error = %v calls = %d", err, calls.Load())
	}
	if err := read(); err == nil {
		t.Fatal("open circuit allowed another request")
	} else {
		var open *CircuitOpenError
		if !errors.As(err, &open) || open.Throttled ||
			open.RetryAfter != transientCircuitDelay {
			t.Fatalf("open circuit error = %#v (%v)", open, err)
		}
	}
	if calls.Load() != defaultReadAttempts {
		t.Fatalf("open circuit made a provider call: %d", calls.Load())
	}
	now = now.Add(transientCircuitDelay)
	if err := read(); err != nil || calls.Load() != defaultReadAttempts+1 {
		t.Fatalf("recovered read error = %v calls = %d", err, calls.Load())
	}
}

func TestClientRateLimitOpensCircuitWithoutHidingProviderResponse(t *testing.T) {
	var calls atomic.Int32
	client, err := New(Options{
		BaseURL: "https://api.example.invalid/v1",
		HTTP: &http.Client{Transport: roundTripperFunc(func(
			*http.Request,
		) (*http.Response, error) {
			calls.Add(1)
			return &http.Response{
				StatusCode: http.StatusTooManyRequests,
				Header:     http.Header{"Retry-After": {"60"}},
				Body:       io.NopCloser(strings.NewReader(`{"error":{"code":"rate_limited"}}`)),
			}, nil
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.August, 14, 22, 0, 0, 0, time.UTC)
	client.resilience.now = func() time.Time { return now }
	result, err := client.DoJSON(
		t.Context(), http.MethodGet, "messages", nil, nil, nil,
		false, nil, http.StatusOK, http.StatusTooManyRequests,
	)
	if err != nil || result.Status != http.StatusTooManyRequests {
		t.Fatalf("rate-limit result = %+v error = %v", result, err)
	}
	_, err = client.DoJSON(
		t.Context(), http.MethodGet, "messages", nil, nil, nil,
		false, nil, http.StatusOK,
	)
	var open *CircuitOpenError
	if !errors.As(err, &open) || !open.Throttled || open.RetryAfter != time.Minute || calls.Load() != 1 {
		t.Fatalf("throttle circuit error = %#v (%v), calls = %d", open, err, calls.Load())
	}
}

func TestRetryAfterDurationAcceptsDeltaAndHTTPDateWithinBounds(t *testing.T) {
	now := time.Date(2026, time.August, 14, 22, 0, 0, 0, time.UTC)
	tests := []struct {
		value string
		want  time.Duration
	}{
		{value: "60", want: time.Minute},
		{value: now.Add(90 * time.Second).Format(http.TimeFormat), want: 90 * time.Second},
		{value: now.Add(time.Hour).Format(http.TimeFormat), want: maximumThrottleCircuit},
		{value: now.Format(http.TimeFormat), want: 0},
		{value: "private-invalid-value", want: 0},
	}
	for _, test := range tests {
		if got := retryAfterDuration(test.value, now); got != test.want {
			t.Errorf("retryAfterDuration(%q) = %s, want %s", test.value, got, test.want)
		}
	}
}

func TestClientClassifiesExplicitUnauthorizedResponse(t *testing.T) {
	t.Parallel()
	client, err := New(Options{
		BaseURL: "https://api.example.invalid/v1",
		HTTP: &http.Client{Transport: roundTripperFunc(func(
			*http.Request,
		) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusUnauthorized,
				Header:     make(http.Header),
				Body: io.NopCloser(strings.NewReader(
					`{"error":{"code":"invalid_token","message":"private detail"}}`,
				)),
			}, nil
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.DoJSON(
		t.Context(),
		http.MethodGet,
		"messages",
		nil,
		nil,
		nil,
		false,
		nil,
		http.StatusOK,
	)
	reason, ok := application.ProviderAuthenticationReason(err)
	if !ok || reason != application.AuthenticationReasonCredentialRejected ||
		strings.Contains(err.Error(), "private detail") {
		t.Fatalf("unauthorized error = %v, reason = %q", err, reason)
	}
}

func TestClientMarksUnverifiableSuccessfulWriteResponseAsAmbiguous(
	t *testing.T,
) {
	t.Parallel()
	tests := []struct {
		name   string
		body   string
		status int
		json   bool
	}{
		{
			name: "malformed JSON", body: `{`,
			status: http.StatusOK, json: true,
		},
		{
			name:   "oversized response",
			body:   strings.Repeat("x", maximumResponseBytes+1),
			status: http.StatusOK,
		},
		{name: "unexpected success", status: http.StatusAccepted},
		{
			name:   "server failure",
			body:   `{"error":{"code":"private_provider_detail"}}`,
			status: http.StatusServiceUnavailable,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			client, err := New(Options{
				BaseURL: "https://api.example.invalid/v1",
				HTTP: &http.Client{Transport: roundTripperFunc(func(
					*http.Request,
				) (*http.Response, error) {
					return &http.Response{
						StatusCode: test.status,
						Header:     make(http.Header),
						Body:       io.NopCloser(strings.NewReader(test.body)),
					}, nil
				})},
			})
			if err != nil {
				t.Fatal(err)
			}
			var response struct {
				ID string `json:"id"`
			}
			var target any
			if test.json {
				target = &response
			}
			_, err = client.DoJSON(
				t.Context(),
				http.MethodPost,
				"messages",
				nil,
				map[string]string{"subject": "synthetic"},
				target,
				true,
				nil,
				http.StatusOK,
			)
			if !errors.Is(err, application.ErrWriteOutcomeUnknown) {
				t.Fatalf("write response error = %v", err)
			}
			if strings.Contains(err.Error(), "private_provider_detail") {
				t.Fatalf("write response exposed provider detail: %v", err)
			}
		})
	}
}

func TestClientKeepsExplicitWriteRejectionDefinite(t *testing.T) {
	t.Parallel()
	client, err := New(Options{
		BaseURL: "https://api.example.invalid/v1",
		HTTP: &http.Client{Transport: roundTripperFunc(func(
			*http.Request,
		) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusBadRequest,
				Header:     make(http.Header),
				Body: io.NopCloser(strings.NewReader(
					`{"error":{"code":"invalid_request"}}`,
				)),
			}, nil
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.DoJSON(
		t.Context(),
		http.MethodPost,
		"messages",
		nil,
		map[string]string{"subject": "synthetic"},
		nil,
		true,
		nil,
		http.StatusOK,
	)
	if err == nil || errors.Is(err, application.ErrWriteOutcomeUnknown) {
		t.Fatalf("explicit write rejection error = %v", err)
	}
}

func TestClientRejectsDecodedDotSegments(t *testing.T) {
	t.Parallel()
	client, err := New(Options{
		BaseURL: "https://api.example.invalid/v1",
		HTTP:    &http.Client{},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, resource := range []string{
		"me/messages/.",
		"me/messages/..",
		"me/messages/%2e",
		"me/messages/%2E%2E",
	} {
		if _, err := client.target(resource, nil); err == nil ||
			!strings.Contains(err.Error(), "dot segment") {
			t.Errorf("target(%q) error = %v", resource, err)
		}
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (function roundTripperFunc) RoundTrip(
	request *http.Request,
) (*http.Response, error) {
	return function(request)
}
