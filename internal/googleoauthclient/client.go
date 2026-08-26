// Package googleoauthclient parses the bounded Desktop OAuth configuration
// downloaded from Google Cloud. It never persists or logs the generated client
// credential.
package googleoauthclient

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
)

const (
	maximumDocumentBytes = 32 << 10
	maximumClientIDBytes = 512
	maximumSecretBytes   = 4 << 10
)

// Client is the validated, caller-owned projection of one Google Desktop
// client configuration. Close overwrites the mutable credential bytes.
type Client struct {
	ID     string
	Secret []byte
}

// Close overwrites the generated credential held in memory.
func (client *Client) Close() {
	if client == nil {
		return
	}
	for index := range client.Secret {
		client.Secret[index] = 0
	}
	client.Secret = nil
}

type document struct {
	Installed *installedClient `json:"installed"`
}

type installedClient struct {
	ClientID                    string   `json:"client_id"`
	ProjectID                   string   `json:"project_id"`
	AuthURI                     string   `json:"auth_uri"`
	TokenURI                    string   `json:"token_uri"`
	AuthProviderX509Certificate string   `json:"auth_provider_x509_cert_url"`
	ClientSecret                string   `json:"client_secret"`
	RedirectURIs                []string `json:"redirect_uris"`
}

// ParseFile reads one explicit local file with a strict size limit.
func ParseFile(path string) (Client, error) {
	if path == "" || len(path) > 4096 || strings.ContainsAny(path, "\r\n\x00") {
		return Client{}, errors.New("google OAuth client JSON path is malformed")
	}
	file, err := os.Open(path) // #nosec G304 -- this is an explicit local CLI input.
	if err != nil {
		return Client{}, fmt.Errorf("open Google OAuth client JSON: %w", err)
	}
	defer func() { _ = file.Close() }()
	return Parse(io.LimitReader(file, maximumDocumentBytes+1))
}

// Parse validates exactly one installed/Desktop client document.
func Parse(reader io.Reader) (Client, error) {
	if reader == nil {
		return Client{}, errors.New("google OAuth client JSON is unavailable")
	}
	raw, err := io.ReadAll(reader)
	if err != nil {
		return Client{}, errors.New("read Google OAuth client JSON")
	}
	defer overwrite(raw)
	if len(raw) == 0 || len(raw) > maximumDocumentBytes {
		return Client{}, errors.New("google OAuth client JSON is empty or too large")
	}
	var decoded document
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return Client{}, errors.New("google OAuth client JSON is malformed")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Client{}, errors.New("google OAuth client JSON has trailing content")
	}
	if decoded.Installed == nil {
		return Client{}, errors.New("google OAuth client must have application type Desktop app")
	}
	installed := decoded.Installed
	if !validBounded(installed.ClientID, maximumClientIDBytes) {
		return Client{}, errors.New("google OAuth client ID is malformed")
	}
	if !validBounded(installed.ClientSecret, maximumSecretBytes) {
		return Client{}, errors.New("google OAuth client credential is missing or malformed")
	}
	if installed.AuthURI != "https://accounts.google.com/o/oauth2/auth" &&
		installed.AuthURI != "https://accounts.google.com/o/oauth2/v2/auth" {
		return Client{}, errors.New("google OAuth authorization endpoint is unexpected")
	}
	if installed.TokenURI != "https://oauth2.googleapis.com/token" {
		return Client{}, errors.New("google OAuth token endpoint is unexpected")
	}
	if len(installed.RedirectURIs) == 0 || len(installed.RedirectURIs) > 8 {
		return Client{}, errors.New("google Desktop client has no bounded loopback redirect list")
	}
	for _, rawRedirect := range installed.RedirectURIs {
		redirect, parseErr := url.Parse(rawRedirect)
		if parseErr != nil || redirect.Scheme != "http" || redirect.User != nil ||
			redirect.RawQuery != "" || redirect.Fragment != "" ||
			(redirect.Hostname() != "localhost" && redirect.Hostname() != "127.0.0.1" &&
				redirect.Hostname() != "::1") {
			return Client{}, errors.New("google Desktop client redirect list is malformed")
		}
	}
	return Client{ID: installed.ClientID, Secret: []byte(installed.ClientSecret)}, nil
}

func validBounded(value string, maximum int) bool {
	return value != "" && len(value) <= maximum &&
		strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\r\n\x00")
}

func overwrite(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
