// Package session owns short-lived Outlook Web authorization material.
package session

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var ErrNotReady = errors.New("outlook web session is not ready")

var (
	ErrAuthorizationMissing = errors.New("observed Outlook Web request has no authorization")
	ErrAuthorizationScheme  = errors.New("observed Outlook Web request uses an unsupported authorization scheme")
	ErrAuthorizationInvalid = errors.New("observed Outlook Web authorization is malformed")
)

var forwardedHeaders = map[string]struct{}{
	"X-Anchormailbox":          {},
	"X-Clientid":               {},
	"X-Owa-Canary":             {},
	"X-Owa-Clientbuildversion": {},
	"X-Owa-Sessionid":          {},
}

// Credentials is an immutable in-memory snapshot. It deliberately exposes no
// token getter and serializes only as a redacted string.
type Credentials struct {
	origin        string
	authorization string
	headers       http.Header
	capturedAt    time.Time
}

// Apply authorizes a request only when its origin exactly matches the capture.
func (credentials Credentials) Apply(request *http.Request) error {
	if request == nil || request.URL == nil {
		return errors.New("request URL is required")
	}
	if requestOrigin(request.URL) != credentials.origin {
		return fmt.Errorf("request origin %q does not match session origin", requestOrigin(request.URL))
	}
	if request.Header.Get("Authorization") != "" {
		return errors.New("request already contains authorization")
	}
	request.Header.Set("Authorization", credentials.authorization)
	for name, values := range credentials.headers {
		for _, value := range values {
			request.Header.Add(name, value)
		}
	}
	return nil
}

// CapturedAt reports snapshot freshness without exposing authorization data.
func (credentials Credentials) CapturedAt() time.Time {
	return credentials.capturedAt
}

func (credentials Credentials) String() string { return "[redacted Outlook Web session]" }

func (credentials Credentials) GoString() string { return credentials.String() }

func newCredentials(rawURL string, headers http.Header, now time.Time) (Credentials, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return Credentials{}, fmt.Errorf("parse observed request URL: %w", err)
	}
	origin := requestOrigin(parsed)
	if origin == "" {
		return Credentials{}, errors.New("observed request has no HTTPS origin")
	}

	authorization := headers.Get("Authorization")
	parts := strings.Fields(authorization)
	if authorization == "" {
		return Credentials{}, ErrAuthorizationMissing
	}
	if len(parts) != 2 {
		return Credentials{}, ErrAuthorizationInvalid
	}
	if !strings.EqualFold(parts[0], "Bearer") {
		return Credentials{}, ErrAuthorizationScheme
	}
	if len(parts[1]) < 32 || len(parts[1]) > 16<<10 || strings.ContainsAny(parts[1], "\r\n\x00") {
		return Credentials{}, ErrAuthorizationInvalid
	}

	selected := make(http.Header)
	for observedName, values := range headers {
		name := http.CanonicalHeaderKey(observedName)
		if _, allowed := forwardedHeaders[name]; !allowed {
			continue
		}
		for _, value := range values {
			if value == "" || len(value) > 4<<10 || strings.ContainsAny(value, "\r\n\x00") {
				continue
			}
			selected.Add(name, value)
		}
	}
	return Credentials{
		origin:        origin,
		authorization: "Bearer " + parts[1],
		headers:       selected,
		capturedAt:    now.UTC(),
	}, nil
}

func requestOrigin(target *url.URL) string {
	if target == nil || target.Scheme != "https" || target.Host == "" || target.User != nil {
		return ""
	}
	return "https://" + strings.ToLower(target.Host)
}
