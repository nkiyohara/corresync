package outlookweb

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/domain"
)

const (
	defaultTimeout          = 30 * time.Second
	defaultMaxRequestBytes  = 8 << 20
	defaultMaxResponseBytes = 16 << 20
	defaultReadAttempts     = 3
	loginTimeoutStatus      = 440
)

var (
	ErrSessionExpired = errors.New("outlook web session expired")
	ErrResponseTooBig = errors.New("outlook web response exceeds configured limit")
)

// Authorizer applies current browser-owned credentials to an exact-origin
// request without exposing those credentials to the protocol client.
type Authorizer interface {
	Apply(*http.Request) error
}

// Options configures the hardened OWA HTTP boundary.
type Options struct {
	Origin           string
	Mailbox          string
	Authorizer       Authorizer
	HTTPClient       *http.Client
	UserAgent        string
	MaxRequestBytes  int
	MaxResponseBytes int
	ReadAttempts     int
}

// Client calls only registered OWA actions against one exact HTTPS origin.
type Client struct {
	origin           *url.URL
	mailbox          string
	authorizer       Authorizer
	http             *http.Client
	userAgent        string
	maxRequestBytes  int
	maxResponseBytes int
	readAttempts     int
	random           io.Reader
	sleep            func(context.Context, time.Duration) error
}

// HTTPError contains safe response metadata and never includes a response body.
type HTTPError struct {
	StatusCode int
	RequestID  string
}

func (failure *HTTPError) Error() string {
	if failure.RequestID == "" {
		return fmt.Sprintf("outlook web returned HTTP %d", failure.StatusCode)
	}
	return fmt.Sprintf("outlook web returned HTTP %d (request %s)", failure.StatusCode, failure.RequestID)
}

// NewClient validates and clones all mutable transport dependencies.
func NewClient(options Options) (*Client, error) {
	origin, err := parseOrigin(options.Origin)
	if err != nil {
		return nil, err
	}
	if options.Authorizer == nil {
		return nil, errors.New("OWA authorizer is required")
	}
	if options.MaxRequestBytes == 0 {
		options.MaxRequestBytes = defaultMaxRequestBytes
	}
	if options.MaxResponseBytes == 0 {
		options.MaxResponseBytes = defaultMaxResponseBytes
	}
	if options.ReadAttempts == 0 {
		options.ReadAttempts = defaultReadAttempts
	}
	if options.MaxRequestBytes < 1 || options.MaxRequestBytes > 64<<20 {
		return nil, errors.New("max OWA request bytes must be between 1 and 64 MiB")
	}
	if options.MaxResponseBytes < 1 || options.MaxResponseBytes > 64<<20 {
		return nil, errors.New("max OWA response bytes must be between 1 and 64 MiB")
	}
	if options.ReadAttempts < 1 || options.ReadAttempts > 5 {
		return nil, errors.New("OWA read attempts must be between 1 and 5")
	}
	if options.UserAgent == "" {
		options.UserAgent = "corresync/dev"
	}
	if err := validateExplicitMailbox(options.Mailbox); err != nil {
		return nil, err
	}
	if strings.ContainsAny(options.UserAgent, "\r\n\x00") {
		return nil, errors.New("OWA user agent contains a forbidden character")
	}

	return &Client{
		origin:           origin,
		mailbox:          options.Mailbox,
		authorizer:       options.Authorizer,
		http:             hardenedHTTPClient(options.HTTPClient),
		userAgent:        options.UserAgent,
		maxRequestBytes:  options.MaxRequestBytes,
		maxResponseBytes: options.MaxResponseBytes,
		readAttempts:     options.ReadAttempts,
		random:           rand.Reader,
		sleep:            sleepContext,
	}, nil
}

// Call executes one registered action and decodes its bounded JSON response.
func (client *Client) Call(ctx context.Context, action Action, requestBody, responseBody any) error {
	if !action.valid() {
		return errors.New("unregistered OWA action")
	}
	payload, err := json.Marshal(requestBody)
	if err != nil {
		return fmt.Errorf("encode OWA request: %w", err)
	}
	if len(payload) > client.maxRequestBytes {
		return fmt.Errorf("OWA request exceeds %d bytes", client.maxRequestBytes)
	}

	attempts := 1
	if action.Effect() == domain.EffectRead || action.Effect() == domain.EffectSensitiveRead {
		attempts = client.readAttempts
	}
	for attempt := 1; attempt <= attempts; attempt++ {
		response, callErr := client.callOnce(ctx, action, payload)
		if callErr != nil {
			if _, authenticationFailure := application.ProviderAuthenticationReason(callErr); authenticationFailure {
				return callErr
			}
			if attempt < attempts && ctx.Err() == nil {
				if sleepErr := client.sleep(ctx, retryDelay(attempt, nil)); sleepErr != nil {
					return sleepErr
				}
				continue
			}
			return callErr
		}

		body, readErr := readLimited(response.Body, client.maxResponseBytes)
		closeErr := response.Body.Close()
		if response.StatusCode == http.StatusUnauthorized ||
			response.StatusCode == http.StatusForbidden ||
			response.StatusCode == loginTimeoutStatus {
			return ErrSessionExpired
		}
		if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
			if attempt < attempts && retryableStatus(response.StatusCode) {
				if sleepErr := client.sleep(ctx, retryDelay(attempt, response)); sleepErr != nil {
					return sleepErr
				}
				continue
			}
			return classifyPostRequestError(action, &HTTPError{
				StatusCode: response.StatusCode,
				RequestID:  safeRequestID(response.Header.Get("request-id")),
			})
		}
		if readErr != nil {
			return classifyPostRequestError(action, readErr)
		}
		if closeErr != nil {
			return classifyPostRequestError(action, fmt.Errorf("close OWA response: %w", closeErr))
		}
		if responseBody == nil || len(body) == 0 {
			return nil
		}
		if err := json.Unmarshal(body, responseBody); err != nil {
			return classifyPostRequestError(action, fmt.Errorf("decode OWA response JSON: %w", err))
		}
		return nil
	}
	return errors.New("OWA call exhausted attempts")
}

func (client *Client) callOnce(ctx context.Context, action Action, payload []byte) (*http.Response, error) {
	requestID, err := newRequestID(client.random)
	if err != nil {
		return nil, err
	}
	serviceURL := *client.origin
	serviceURL.Path = "/owa/service.svc"
	query := make(url.Values)
	query.Set("action", action.Name())
	serviceURL.RawQuery = query.Encode()

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, serviceURL.String(), bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build OWA request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Action", action.Name())
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	request.Header.Set("User-Agent", client.userAgent)
	request.Header.Set("X-OWA-ActionName", action.Name()+"Action")
	request.Header.Set("X-Requested-With", "XMLHttpRequest")
	request.Header.Set("client-request-id", requestID)
	request.Header.Set("return-client-request-id", "true")
	if err := client.authorizer.Apply(request); err != nil {
		return nil, application.NewProviderAuthenticationFailure(
			application.AuthenticationReasonInteractionRequired,
			errors.New("outlook web browser authorization is unavailable"),
		)
	}
	if client.mailbox != "" {
		request.Header.Set("X-AnchorMailbox", client.mailbox)
		request.Header.Set("X-OWA-ExplicitLogonUser", client.mailbox)
	}

	response, err := client.http.Do(request)
	if err != nil {
		return nil, classifyPostRequestError(action, fmt.Errorf("execute OWA request: %w", err))
	}
	return response, nil
}

func validateExplicitMailbox(value string) error {
	if value == "" {
		return nil
	}
	if len(value) > 254 || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\r\n\x00") {
		return errors.New("explicit OWA mailbox must be a bare SMTP address")
	}
	local, domain, found := strings.Cut(value, "@")
	if !found || local == "" || domain == "" || strings.ContainsAny(value, " <>\t") {
		return errors.New("explicit OWA mailbox must be a bare SMTP address")
	}
	return nil
}

func classifyPostRequestError(action Action, failure error) error {
	if action.Effect() == domain.EffectRead || action.Effect() == domain.EffectSensitiveRead {
		return failure
	}
	return errors.Join(application.ErrWriteOutcomeUnknown, failure)
}

func hardenedHTTPClient(provided *http.Client) *http.Client {
	if provided != nil {
		clone := *provided
		clone.CheckRedirect = rejectRedirect
		if clone.Timeout == 0 {
			clone.Timeout = defaultTimeout
		}
		return &clone
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	} else {
		transport.TLSClientConfig = transport.TLSClientConfig.Clone()
		transport.TLSClientConfig.MinVersion = tls.VersionTLS12
	}
	transport.ResponseHeaderTimeout = 15 * time.Second
	return &http.Client{
		Transport:     transport,
		Timeout:       defaultTimeout,
		CheckRedirect: rejectRedirect,
	}
}

func rejectRedirect(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }

func parseOrigin(raw string) (*url.URL, error) {
	origin, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse OWA origin: %w", err)
	}
	if origin.Scheme != "https" || origin.Host == "" || origin.User != nil ||
		origin.Path != "" && origin.Path != "/" || origin.RawQuery != "" || origin.Fragment != "" {
		return nil, errors.New("OWA origin must be an HTTPS origin without user information, path, query, or fragment")
	}
	origin.Path = ""
	origin.Host = strings.ToLower(origin.Host)
	return origin, nil
}

func readLimited(reader io.Reader, maximum int) ([]byte, error) {
	limited := io.LimitReader(reader, int64(maximum)+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("read OWA response: %w", err)
	}
	if len(body) > maximum {
		return nil, ErrResponseTooBig
	}
	return body, nil
}

func retryableStatus(status int) bool {
	switch status {
	case http.StatusTooManyRequests,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func retryDelay(attempt int, response *http.Response) time.Duration {
	if response != nil {
		if seconds, err := strconv.Atoi(response.Header.Get("Retry-After")); err == nil && seconds >= 0 {
			delay := time.Duration(seconds) * time.Second
			if delay > 30*time.Second {
				return 30 * time.Second
			}
			return delay
		}
	}
	return time.Duration(attempt*attempt) * 100 * time.Millisecond
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func newRequestID(random io.Reader) (string, error) {
	bytes := make([]byte, 16)
	if _, err := io.ReadFull(random, bytes); err != nil {
		return "", fmt.Errorf("generate OWA request ID: %w", err)
	}
	bytes[6] = bytes[6]&0x0f | 0x40
	bytes[8] = bytes[8]&0x3f | 0x80
	encoded := hex.EncodeToString(bytes)
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" +
		encoded[16:20] + "-" + encoded[20:32], nil
}

func safeRequestID(value string) string {
	if len(value) > 128 || strings.ContainsAny(value, "\r\n\x00") {
		return ""
	}
	return value
}
