package restapi

import (
	"context"
	"crypto/x509"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
	client, err := New(Options{
		BaseURL: "https://api.example.invalid/v1",
		HTTP: &http.Client{Transport: roundTripperFunc(func(
			*http.Request,
		) (*http.Response, error) {
			return nil, errors.New("synthetic transport failure")
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
	if !errors.Is(err, application.ErrWriteOutcomeUnknown) {
		t.Fatalf("write transport error = %v", err)
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
		{name: "server failure", status: http.StatusServiceUnavailable},
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
