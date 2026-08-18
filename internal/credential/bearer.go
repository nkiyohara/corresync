package credential

import (
	"bytes"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

// BearerAuthorizer owns one externally resolved bearer value and applies it
// only to the exact HTTPS origin selected by the signed-in human. Protocol
// adapters receive this narrow capability, never the credential itself.
type BearerAuthorizer struct {
	mu     sync.RWMutex
	origin string
	value  []byte
}

// NewBearerAuthorizer transfers a copy of secret into an exact-origin request
// authorizer. The caller should close the source Secret immediately afterward.
func NewBearerAuthorizer(origin string, secret *Secret) (*BearerAuthorizer, error) {
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		parsed.Path != "" && parsed.Path != "/" {
		return nil, errors.New("bearer authorization requires one exact HTTPS origin")
	}
	if secret == nil {
		return nil, errors.New("bearer authorization requires an external credential")
	}
	value := secret.CopyBytes()
	if len(value) == 0 || len(value) > maximumSecretBytes ||
		bytes.ContainsAny(value, "\r\n\x00") || bytes.HasPrefix(value, []byte("Bearer ")) {
		erase(value)
		return nil, errors.New("external bearer credential is malformed")
	}
	for _, character := range value {
		if character < 0x21 || character > 0x7e {
			erase(value)
			return nil, errors.New("external bearer credential is malformed")
		}
	}
	parsed.Path = ""
	parsed.Host = strings.ToLower(parsed.Host)
	return &BearerAuthorizer{origin: parsed.String(), value: value}, nil
}

// Apply authorizes an uncredentialed request without permitting origin
// confusion or overwriting an existing authorization value.
func (authorizer *BearerAuthorizer) Apply(request *http.Request) error {
	if authorizer == nil || request == nil || request.URL == nil {
		return errors.New("bearer authorizer is unavailable")
	}
	authorizer.mu.RLock()
	defer authorizer.mu.RUnlock()
	if len(authorizer.value) == 0 {
		return errors.New("bearer authorizer is closed")
	}
	target := *request.URL
	target.Path = ""
	target.RawPath = ""
	target.RawQuery = ""
	target.Fragment = ""
	target.Host = strings.ToLower(target.Host)
	if target.String() != authorizer.origin {
		return errors.New("bearer authorization target does not match its selected origin")
	}
	if request.Header.Get("Authorization") != "" {
		return errors.New("request is already authorized")
	}
	request.Header.Set("Authorization", "Bearer "+string(authorizer.value))
	return nil
}

// Close overwrites the owned bearer value.
func (authorizer *BearerAuthorizer) Close() error {
	if authorizer == nil {
		return nil
	}
	authorizer.mu.Lock()
	defer authorizer.mu.Unlock()
	erase(authorizer.value)
	authorizer.value = nil
	return nil
}

func erase(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
