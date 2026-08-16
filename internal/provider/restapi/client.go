// Package restapi provides a bounded, redirect-rejecting HTTP boundary shared
// by explicit API provider adapters. It contains transport mechanics only, not
// provider semantics or application policy.
package restapi

import (
	"bytes"
	"context"
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
)

const (
	maximumRequestBytes  = 4 << 20
	maximumResponseBytes = 8 << 20
	defaultReadAttempts  = 3
)

// ErrPrecondition indicates a server-enforced version condition failed.
var ErrPrecondition = errors.New("API write precondition failed")

var errRedirectRejected = errors.New("API redirects are not accepted")

// StatusError is a bounded, content-free HTTP failure. Provider adapters may
// branch on a documented status such as an expired delta token without
// parsing an error string or exposing a response body.
type StatusError struct {
	Status int
	Code   string
}

func (failure *StatusError) Error() string {
	if failure.Code != "" {
		return fmt.Sprintf("API returned HTTP %d (%s)", failure.Status, failure.Code)
	}
	return fmt.Sprintf("API returned HTTP %d", failure.Status)
}

// IsStatus reports whether err contains the selected HTTP response status.
func IsStatus(err error, status int) bool {
	var failure *StatusError
	return errors.As(err, &failure) && failure.Status == status
}

// Client owns one API origin and an already authorized HTTP client.
type Client struct {
	base       *url.URL
	http       *http.Client
	resilience *readResilience
}

// Options configures one account-scoped API transport.
type Options struct {
	BaseURL string
	HTTP    *http.Client
}

// New validates a credential-free HTTPS base without contacting it.
func New(options Options) (*Client, error) {
	base, err := url.Parse(options.BaseURL)
	if err != nil || base.Scheme != "https" || base.Hostname() == "" ||
		base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, errors.New("API base must be a credential-free HTTPS URL")
	}
	if options.HTTP == nil {
		return nil, errors.New("authorized API HTTP client is required")
	}
	httpClient := *options.HTTP
	if httpClient.Timeout == 0 {
		httpClient.Timeout = 30 * time.Second
	}
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return errRedirectRejected
	}
	return &Client{
		base: base, http: &httpClient,
		resilience: newReadResilience(),
	}, nil
}

// Result is one bounded provider response.
type Result struct {
	Status int
	Header http.Header
	Body   []byte
}

// DoJSON executes one JSON request and decodes a bounded JSON response.
func (client *Client) DoJSON(
	ctx context.Context,
	method, resource string,
	query url.Values,
	requestBody any,
	responseBody any,
	write bool,
	headers http.Header,
	accepted ...int,
) (Result, error) {
	var body io.Reader
	if requestBody != nil {
		encoded, err := json.Marshal(requestBody)
		if err != nil {
			return Result{}, err
		}
		if len(encoded) > maximumRequestBytes {
			return Result{}, errors.New("API JSON request exceeds the configured limit")
		}
		body = bytes.NewReader(encoded)
	}
	result, err := client.Do(
		ctx, method, resource, query, body, write, headers, accepted...,
	)
	if err != nil {
		return Result{}, err
	}
	if err := decodeJSONResult(result, responseBody, write); err != nil {
		return result, err
	}
	return result, nil
}

// DoForm executes one bounded application/x-www-form-urlencoded request and
// decodes a bounded JSON response. It exists for provider APIs whose documented
// write boundary is a form body (for example, a command batch) rather than a
// JSON entity.
func (client *Client) DoForm(
	ctx context.Context,
	method, resource string,
	query, form url.Values,
	responseBody any,
	write bool,
	headers http.Header,
	accepted ...int,
) (Result, error) {
	encoded := form.Encode()
	if len(encoded) > maximumRequestBytes {
		return Result{}, errors.New("API form request exceeds the configured limit")
	}
	requestHeaders := headers.Clone()
	if requestHeaders == nil {
		requestHeaders = make(http.Header)
	}
	requestHeaders.Set("Content-Type", "application/x-www-form-urlencoded")
	result, err := client.Do(
		ctx, method, resource, query, strings.NewReader(encoded), write,
		requestHeaders, accepted...,
	)
	if err != nil {
		return Result{}, err
	}
	if err := decodeJSONResult(result, responseBody, write); err != nil {
		return result, err
	}
	return result, nil
}

func decodeJSONResult(result Result, destination any, write bool) error {
	if destination == nil || len(result.Body) == 0 {
		return nil
	}
	if err := json.Unmarshal(result.Body, destination); err != nil {
		if write {
			return fmt.Errorf(
				"%w: API write returned malformed JSON",
				application.ErrWriteOutcomeUnknown,
			)
		}
		return errors.New("API returned malformed JSON")
	}
	return nil
}

// Do executes one bounded request. Transport failures for writes are reported
// as outcome-unknown because the remote service may have committed them.
func (client *Client) Do(
	ctx context.Context,
	method, resource string,
	query url.Values,
	body io.Reader,
	write bool,
	headers http.Header,
	accepted ...int,
) (Result, error) {
	target, err := client.target(resource, query)
	if err != nil {
		return Result{}, err
	}
	payload, err := boundedRequestBody(body)
	if err != nil {
		return Result{}, err
	}
	if !write {
		if err := client.resilience.BeforeRead(); err != nil {
			return Result{}, err
		}
	}
	attempts := 1
	if !write {
		attempts = defaultReadAttempts
	}
	for attempt := 1; attempt <= attempts; attempt++ {
		result, callErr := client.doOnce(
			ctx, method, target, payload, headers, write,
		)
		if callErr != nil {
			if write {
				return Result{}, callErr
			}
			var transient *transientReadError
			if !errors.As(callErr, &transient) {
				return Result{}, callErr
			}
			if attempt < attempts && ctx.Err() == nil {
				if sleepErr := client.resilience.Backoff(ctx, attempt); sleepErr != nil {
					return Result{}, sleepErr
				}
				continue
			}
			if ctx.Err() == nil {
				client.resilience.OpenTransient()
			}
			return Result{}, callErr
		}
		if !write && retryableReadStatus(result.Status) {
			retryAfter := retryAfterDuration(
				result.Header.Get("Retry-After"),
				client.resilience.Now(),
			)
			if retryAfter > 0 {
				client.resilience.OpenThrottle(retryAfter)
				return Result{}, apiStatusError(result.Status, result.Body)
			}
			if attempt < attempts && ctx.Err() == nil {
				if sleepErr := client.resilience.Backoff(ctx, attempt); sleepErr != nil {
					return Result{}, sleepErr
				}
				continue
			}
			client.resilience.OpenTransient()
			return Result{}, apiStatusError(result.Status, result.Body)
		}
		if !write && result.Status == http.StatusTooManyRequests {
			client.resilience.OpenThrottle(
				retryAfterDuration(
					result.Header.Get("Retry-After"),
					client.resilience.Now(),
				),
			)
		} else if !write {
			client.resilience.Succeed()
		}
		return classifyResult(result, write, accepted...)
	}
	return Result{}, errors.New("API read exhausted its bounded attempts")
}

func (client *Client) doOnce(
	ctx context.Context,
	method string,
	target *url.URL,
	payload []byte,
	headers http.Header,
	write bool,
) (Result, error) {
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	request, err := http.NewRequestWithContext(ctx, method, target.String(), body)
	if err != nil {
		return Result{}, err
	}
	for name, values := range headers {
		for _, value := range values {
			request.Header.Add(name, value)
		}
	}
	if body != nil && request.Header.Get("Content-Type") == "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.http.Do(request)
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		if write {
			return Result{}, fmt.Errorf(
				"%w: API write transport failed after dispatch",
				application.ErrWriteOutcomeUnknown,
			)
		}
		if errors.Is(err, errRedirectRejected) || ctx.Err() != nil {
			return Result{}, err
		}
		return Result{}, &transientReadError{cause: err}
	}
	content, readErr := io.ReadAll(
		io.LimitReader(response.Body, maximumResponseBytes+1),
	)
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil {
		if write {
			return Result{}, fmt.Errorf(
				"%w: API write response could not be verified",
				application.ErrWriteOutcomeUnknown,
			)
		}
		return Result{}, &transientReadError{cause: errors.Join(readErr, closeErr)}
	}
	if len(content) > maximumResponseBytes {
		if write {
			return Result{}, fmt.Errorf(
				"%w: API write response exceeds the configured limit",
				application.ErrWriteOutcomeUnknown,
			)
		}
		return Result{}, errors.New("API response exceeds the configured limit")
	}
	return Result{
		Status: response.StatusCode,
		Header: response.Header.Clone(),
		Body:   content,
	}, nil
}

type transientReadError struct{ cause error }

func (failure *transientReadError) Error() string {
	return "transient API read failure"
}

func (failure *transientReadError) Unwrap() error { return failure.cause }

func classifyResult(result Result, write bool, accepted ...int) (Result, error) {
	for _, status := range accepted {
		if result.Status == status {
			return result, nil
		}
	}
	if result.Status == http.StatusUnauthorized {
		return Result{}, application.NewProviderAuthenticationFailure(
			application.AuthenticationReasonCredentialRejected,
			apiStatusError(result.Status, result.Body),
		)
	}
	if result.Status == http.StatusPreconditionFailed ||
		result.Status == http.StatusConflict {
		return Result{}, ErrPrecondition
	}
	statusErr := apiStatusError(result.Status, result.Body)
	if write &&
		(result.Status >= http.StatusInternalServerError ||
			result.Status >= http.StatusOK && result.Status < http.StatusMultipleChoices) {
		return Result{}, fmt.Errorf(
			"%w: API write result could not be verified",
			application.ErrWriteOutcomeUnknown,
		)
	}
	return Result{}, statusErr
}

func boundedRequestBody(body io.Reader) ([]byte, error) {
	if body == nil {
		return nil, nil
	}
	payload, err := io.ReadAll(io.LimitReader(body, maximumRequestBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read API request: %w", err)
	}
	if len(payload) > maximumRequestBytes {
		return nil, errors.New("API request exceeds the configured limit")
	}
	return payload, nil
}

func retryableReadStatus(status int) bool {
	switch status {
	case http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func (client *Client) target(resource string, query url.Values) (*url.URL, error) {
	if resource == "" || strings.HasPrefix(resource, "//") ||
		strings.ContainsAny(resource, "\r\n\x00") {
		return nil, errors.New("API resource path is malformed")
	}
	raw := strings.TrimRight(client.base.String(), "/") + "/" +
		strings.TrimLeft(resource, "/")
	target, err := url.Parse(raw)
	if err != nil ||
		target.Scheme != client.base.Scheme ||
		target.Host != client.base.Host ||
		target.User != nil ||
		target.Fragment != "" {
		return nil, errors.New("API resource escaped the configured origin")
	}
	for _, segment := range strings.Split(target.Path, "/") {
		if segment == "." || segment == ".." {
			return nil, errors.New("API resource path contains a dot segment")
		}
	}
	basePath := strings.TrimSuffix(client.base.EscapedPath(), "/") + "/"
	if !strings.HasPrefix(target.EscapedPath(), basePath) {
		return nil, errors.New("API resource escaped the configured base path")
	}
	target.RawQuery = query.Encode()
	return target, nil
}

func apiStatusError(status int, body []byte) error {
	code := ""
	var envelope struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(body, &envelope) == nil && len(envelope.Error) != 0 {
		var structured struct {
			Code json.RawMessage `json:"code"`
		}
		if json.Unmarshal(envelope.Error, &structured) == nil {
			var text string
			if json.Unmarshal(structured.Code, &text) == nil {
				code = text
			} else {
				var number int
				if json.Unmarshal(structured.Code, &number) == nil {
					code = strconv.Itoa(number)
				}
			}
		}
	}
	if code != "" && len(code) <= 128 &&
		!strings.ContainsAny(code, "\r\n\x00") {
		return &StatusError{Status: status, Code: code}
	}
	return &StatusError{Status: status}
}

// Close releases idle account-scoped network connections.
func (client *Client) Close() error {
	if client != nil && client.http != nil {
		client.http.CloseIdleConnections()
	}
	return nil
}
