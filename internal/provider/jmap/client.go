// Package jmap adapts the standard JMAP Mail and Submission capabilities to
// Corresync's closed application ports.
package jmap

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nkiyohara/corresync/internal/application"
)

const (
	coreCapability       = "urn:ietf:params:jmap:core"
	mailCapability       = "urn:ietf:params:jmap:mail"
	submissionCapability = "urn:ietf:params:jmap:submission"
	maximumResponseBytes = 8 << 20
	maximumSessionBytes  = 1 << 20
)

// Options identifies one explicitly configured JMAP account. Password carries
// a private copy owned and zeroed by the resulting client.
type Options struct {
	SessionURL string
	Username   string
	Password   []byte
	Client     *http.Client
}

type sessionDocument struct {
	Capabilities   map[string]json.RawMessage `json:"capabilities"`
	Accounts       map[string]sessionAccount  `json:"accounts"`
	PrimaryAccount map[string]string          `json:"primaryAccounts"`
	APIURL         string                     `json:"apiUrl"`
	DownloadURL    string                     `json:"downloadUrl"`
	UploadURL      string                     `json:"uploadUrl"`
}

type sessionAccount struct {
	AccountCapabilities map[string]json.RawMessage `json:"accountCapabilities"`
	IsReadOnly          bool                       `json:"isReadOnly"`
}

// Client is account-scoped and safe for concurrent application calls. It
// creates independent HTTP requests and owns no provider-side session cookie.
type Client struct {
	username    string
	password    []byte
	http        *http.Client
	apiURL      *url.URL
	downloadURL string
	uploadURL   string
	accountID   string
	observed    ObservedCapabilities
	close       sync.Once
}

// ObservedCapabilities reports account-specific JMAP behavior confirmed by
// the authenticated session resource.
type ObservedCapabilities struct {
	Submission bool
	ReadOnly   bool
}

// New retrieves and validates the configured JMAP session resource. The call
// is authentication, not discovery, and must only be made from explicit CLI
// login.
func New(ctx context.Context, options Options) (*Client, error) {
	if options.Username == "" || len(options.Username) > 320 ||
		strings.ContainsAny(options.Username, "\r\n\x00") {
		return nil, errors.New("JMAP username is malformed")
	}
	if len(options.Password) == 0 || len(options.Password) > 64<<10 {
		return nil, errors.New("JMAP credential is empty or too large")
	}
	sessionURL, err := validatedHTTPSURL("JMAP session URL", options.SessionURL)
	if err != nil {
		return nil, err
	}
	httpClient := options.Client
	if httpClient == nil {
		httpClient = &http.Client{
			Timeout: 30 * time.Second,
		}
	} else {
		copy := *httpClient
		httpClient = &copy
		if httpClient.Timeout == 0 {
			httpClient.Timeout = 30 * time.Second
		}
	}
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return errors.New("JMAP redirects are not accepted")
	}
	client := &Client{
		username: options.Username,
		password: append([]byte(nil), options.Password...),
		http:     httpClient,
	}
	document, err := client.fetchSession(ctx, sessionURL)
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	if _, ok := document.Capabilities[coreCapability]; !ok {
		_ = client.Close()
		return nil, errors.New("JMAP server does not advertise the core capability")
	}
	if _, ok := document.Capabilities[mailCapability]; !ok {
		_ = client.Close()
		return nil, errors.New("JMAP server does not advertise mail")
	}
	accountID := document.PrimaryAccount[mailCapability]
	account, ok := document.Accounts[accountID]
	if !ok || accountID == "" {
		_ = client.Close()
		return nil, errors.New("JMAP session has no primary mail account")
	}
	if _, ok := account.AccountCapabilities[mailCapability]; !ok {
		_ = client.Close()
		return nil, errors.New("JMAP account does not advertise mail")
	}
	_, serverSubmission := document.Capabilities[submissionCapability]
	_, accountSubmission := account.AccountCapabilities[submissionCapability]
	submission := serverSubmission &&
		accountSubmission &&
		document.PrimaryAccount[submissionCapability] == accountID
	apiURL, err := validatedHTTPSURL("JMAP API URL", document.APIURL)
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	if err := requireSameOrigin("JMAP API URL", sessionURL, apiURL); err != nil {
		_ = client.Close()
		return nil, err
	}
	downloadURL, err := validatedTemplateURL("JMAP download URL", document.DownloadURL)
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	if err := requireSameOrigin("JMAP download URL", sessionURL, downloadURL); err != nil {
		_ = client.Close()
		return nil, err
	}
	uploadURL, err := validatedTemplateURL("JMAP upload URL", document.UploadURL)
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	if err := requireSameOrigin("JMAP upload URL", sessionURL, uploadURL); err != nil {
		_ = client.Close()
		return nil, err
	}
	client.apiURL = apiURL
	client.downloadURL = document.DownloadURL
	client.uploadURL = document.UploadURL
	client.accountID = accountID
	client.observed = ObservedCapabilities{
		Submission: submission,
		ReadOnly:   account.IsReadOnly,
	}
	return client, nil
}

// ObservedCapabilities returns the immutable post-authentication capability
// snapshot without exposing provider identifiers or session data.
func (client *Client) ObservedCapabilities() ObservedCapabilities {
	if client == nil {
		return ObservedCapabilities{}
	}
	return client.observed
}

func (client *Client) requireWrite(feature string) error {
	if client.observed.ReadOnly {
		return fmt.Errorf("JMAP %s is unavailable because the account is read-only", feature)
	}
	return nil
}

func (client *Client) fetchSession(
	ctx context.Context,
	sessionURL *url.URL,
) (sessionDocument, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, sessionURL.String(), nil)
	if err != nil {
		return sessionDocument{}, fmt.Errorf("create JMAP session request: %w", err)
	}
	client.authorize(request)
	response, err := client.http.Do(request)
	if err != nil {
		return sessionDocument{}, fmt.Errorf("fetch JMAP session: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		statusErr := fmt.Errorf("JMAP session returned HTTP %d", response.StatusCode)
		if response.StatusCode == http.StatusUnauthorized {
			return sessionDocument{}, application.NewProviderAuthenticationFailure(
				application.AuthenticationReasonCredentialRejected,
				statusErr,
			)
		}
		return sessionDocument{}, statusErr
	}
	var document sessionDocument
	if err := decodeBoundedJSON(response.Body, maximumSessionBytes, &document); err != nil {
		return sessionDocument{}, fmt.Errorf("decode JMAP session: %w", err)
	}
	return document, nil
}

type requestDocument struct {
	Using       []string     `json:"using"`
	MethodCalls []methodCall `json:"methodCalls"`
}

type methodCall [3]any

type responseDocument struct {
	MethodResponses []json.RawMessage `json:"methodResponses"`
}

type methodError struct {
	Type        string `json:"type"`
	Description string `json:"description"`
}

func (client *Client) call(
	ctx context.Context,
	capabilities []string,
	method string,
	arguments any,
	result any,
) error {
	return client.callWithEffect(
		ctx,
		capabilities,
		method,
		arguments,
		result,
		false,
	)
}

func (client *Client) callWrite(
	ctx context.Context,
	capabilities []string,
	method string,
	arguments any,
	result any,
) error {
	return client.callWithEffect(
		ctx,
		capabilities,
		method,
		arguments,
		result,
		true,
	)
}

func (client *Client) callWithEffect(
	ctx context.Context,
	capabilities []string,
	method string,
	arguments any,
	result any,
	write bool,
) error {
	document := requestDocument{
		Using: append([]string{coreCapability}, capabilities...),
		MethodCalls: []methodCall{
			{method, arguments, "c1"},
		},
	}
	body, err := json.Marshal(document)
	if err != nil {
		return fmt.Errorf("encode JMAP request: %w", err)
	}
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		client.apiURL.String(),
		bytes.NewReader(body),
	)
	if err != nil {
		return fmt.Errorf("create JMAP API request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	client.authorize(request)
	response, err := client.http.Do(request)
	if err != nil {
		if write {
			return fmt.Errorf(
				"%w: call JMAP %s: %w",
				application.ErrWriteOutcomeUnknown,
				method,
				err,
			)
		}
		return fmt.Errorf("call JMAP %s: %w", method, err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		statusErr := fmt.Errorf(
			"JMAP %s returned HTTP %d",
			method,
			response.StatusCode,
		)
		if response.StatusCode == http.StatusUnauthorized {
			return application.NewProviderAuthenticationFailure(
				application.AuthenticationReasonCredentialRejected,
				statusErr,
			)
		}
		if write &&
			(response.StatusCode >= http.StatusInternalServerError ||
				response.StatusCode >= http.StatusOK &&
					response.StatusCode < http.StatusMultipleChoices) {
			return fmt.Errorf(
				"%w: %w",
				application.ErrWriteOutcomeUnknown,
				statusErr,
			)
		}
		return statusErr
	}
	var decoded responseDocument
	if err := decodeBoundedJSON(response.Body, maximumResponseBytes, &decoded); err != nil {
		return unverifiableJMAPResponse(write, method, err)
	}
	if len(decoded.MethodResponses) != 1 {
		return unverifiableJMAPResponse(
			write,
			method,
			fmt.Errorf(
				"returned %d method responses",
				len(decoded.MethodResponses),
			),
		)
	}
	var envelope []json.RawMessage
	if err := json.Unmarshal(decoded.MethodResponses[0], &envelope); err != nil || len(envelope) != 3 {
		return unverifiableJMAPResponse(
			write,
			method,
			errors.New("returned a malformed method response"),
		)
	}
	var responseMethod string
	if err := json.Unmarshal(envelope[0], &responseMethod); err != nil {
		return unverifiableJMAPResponse(write, method, err)
	}
	if responseMethod == "error" {
		var methodErr methodError
		if err := json.Unmarshal(envelope[1], &methodErr); err != nil {
			return fmt.Errorf("JMAP %s failed with a malformed error", method)
		}
		return fmt.Errorf("JMAP %s failed: %s", method, sanitizeProviderError(methodErr))
	}
	if responseMethod != method {
		return unverifiableJMAPResponse(
			write,
			method,
			fmt.Errorf("returned unexpected method %q", responseMethod),
		)
	}
	if err := json.Unmarshal(envelope[1], result); err != nil {
		return unverifiableJMAPResponse(write, method, err)
	}
	return nil
}

func unverifiableJMAPResponse(write bool, method string, err error) error {
	if write {
		return fmt.Errorf(
			"%w: verify JMAP %s response: %w",
			application.ErrWriteOutcomeUnknown,
			method,
			err,
		)
	}
	return fmt.Errorf("verify JMAP %s response: %w", method, err)
}

func (client *Client) authorize(request *http.Request) {
	request.SetBasicAuth(client.username, string(client.password))
}

func (client *Client) upload(
	ctx context.Context,
	name, contentType string,
	content []byte,
) (blobUpload, error) {
	raw := expandTemplate(client.uploadURL, map[string]string{
		"accountId": client.accountID,
	})
	target, err := validatedHTTPSURL("JMAP upload target", raw)
	if err != nil {
		return blobUpload{}, err
	}
	if err := requireSameOrigin("JMAP upload target", client.apiURL, target); err != nil {
		return blobUpload{}, err
	}
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		target.String(),
		bytes.NewReader(content),
	)
	if err != nil {
		return blobUpload{}, fmt.Errorf("create JMAP upload request: %w", err)
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	if _, _, err := mime.ParseMediaType(contentType); err != nil {
		return blobUpload{}, errors.New("JMAP attachment content type is malformed")
	}
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("Content-Length", strconv.Itoa(len(content)))
	request.Header.Set("X-JMAP-Name", name)
	request.Header.Set("Accept", "application/json")
	client.authorize(request)
	response, err := client.http.Do(request)
	if err != nil {
		return blobUpload{}, fmt.Errorf(
			"%w: upload JMAP attachment: %w",
			application.ErrWriteOutcomeUnknown,
			err,
		)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusCreated && response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		statusErr := fmt.Errorf(
			"JMAP upload returned HTTP %d",
			response.StatusCode,
		)
		if response.StatusCode == http.StatusUnauthorized {
			return blobUpload{}, application.NewProviderAuthenticationFailure(
				application.AuthenticationReasonCredentialRejected,
				statusErr,
			)
		}
		if response.StatusCode >= http.StatusInternalServerError ||
			response.StatusCode >= http.StatusOK &&
				response.StatusCode < http.StatusMultipleChoices {
			return blobUpload{}, fmt.Errorf(
				"%w: %w",
				application.ErrWriteOutcomeUnknown,
				statusErr,
			)
		}
		return blobUpload{}, statusErr
	}
	var upload blobUpload
	if err := decodeBoundedJSON(response.Body, 1<<20, &upload); err != nil {
		return blobUpload{}, fmt.Errorf(
			"%w: verify JMAP upload response: %w",
			application.ErrWriteOutcomeUnknown,
			err,
		)
	}
	if upload.BlobID == "" || upload.AccountID != client.accountID ||
		upload.Size != len(content) {
		return blobUpload{}, fmt.Errorf(
			"%w: JMAP upload returned inconsistent metadata",
			application.ErrWriteOutcomeUnknown,
		)
	}
	return upload, nil
}

type blobUpload struct {
	AccountID string `json:"accountId"`
	BlobID    string `json:"blobId"`
	Type      string `json:"type"`
	Size      int    `json:"size"`
}

func (client *Client) download(
	ctx context.Context,
	blobID, name, contentType string,
	limit int64,
) ([]byte, error) {
	raw := expandTemplate(client.downloadURL, map[string]string{
		"accountId": client.accountID,
		"blobId":    blobID,
		"name":      name,
		"type":      contentType,
	})
	target, err := validatedHTTPSURL("JMAP download target", raw)
	if err != nil {
		return nil, err
	}
	if err := requireSameOrigin("JMAP download target", client.apiURL, target); err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create JMAP download request: %w", err)
	}
	client.authorize(request)
	response, err := client.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("download JMAP blob: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		statusErr := fmt.Errorf("JMAP download returned HTTP %d", response.StatusCode)
		if response.StatusCode == http.StatusUnauthorized {
			return nil, application.NewProviderAuthenticationFailure(
				application.AuthenticationReasonCredentialRejected,
				statusErr,
			)
		}
		return nil, statusErr
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read JMAP blob: %w", err)
	}
	if int64(len(data)) > limit {
		return nil, errors.New("JMAP blob exceeds the configured limit")
	}
	return data, nil
}

// Close zeroes the credential buffer owned by this client.
func (client *Client) Close() error {
	if client == nil {
		return nil
	}
	client.close.Do(func() {
		for index := range client.password {
			client.password[index] = 0
		}
		client.password = nil
	})
	return nil
}

func validatedHTTPSURL(name, raw string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" ||
		parsed.User != nil || parsed.Fragment != "" {
		return nil, fmt.Errorf("%s must be an absolute credential-free HTTPS URL", name)
	}
	return parsed, nil
}

func validatedTemplateURL(name, raw string) (*url.URL, error) {
	expanded := expandTemplate(raw, map[string]string{
		"accountId": "account",
		"blobId":    "blob",
		"name":      "file",
		"type":      "application/octet-stream",
	})
	return validatedHTTPSURL(name, expanded)
}

func requireSameOrigin(name string, approved, candidate *url.URL) error {
	if approved == nil || candidate == nil ||
		!strings.EqualFold(approved.Scheme, candidate.Scheme) ||
		!strings.EqualFold(approved.Hostname(), candidate.Hostname()) ||
		effectiveHTTPSPort(approved) != effectiveHTTPSPort(candidate) {
		return fmt.Errorf("%s must use the configured JMAP session origin", name)
	}
	return nil
}

func effectiveHTTPSPort(value *url.URL) string {
	if value.Port() == "" {
		return "443"
	}
	return value.Port()
}

func expandTemplate(template string, values map[string]string) string {
	result := template
	for key, value := range values {
		escaped := url.PathEscape(value)
		result = strings.ReplaceAll(result, "{"+key+"}", escaped)
		result = strings.ReplaceAll(result, "{"+key+"*}", escaped)
	}
	return result
}

func decodeBoundedJSON(reader io.Reader, maximum int64, target any) error {
	data, err := io.ReadAll(io.LimitReader(reader, maximum+1))
	if err != nil {
		return err
	}
	if int64(len(data)) > maximum {
		return errors.New("JSON response exceeds the configured limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("JSON response contains trailing data")
	}
	return nil
}

func sanitizeProviderError(value methodError) string {
	if value.Type == "" || len(value.Type) > 128 ||
		strings.ContainsAny(value.Type, "\r\n\x00") {
		return "provider error"
	}
	if value.Description == "" {
		return value.Type
	}
	description := strings.Map(func(character rune) rune {
		if character < 0x20 || character == 0x7f {
			return -1
		}
		return character
	}, value.Description)
	if len(description) > 256 {
		description = description[:256]
	}
	return value.Type + ": " + description
}
