// Package graphapi adapts an explicitly authorized delegated Microsoft Graph
// account to Corresync's closed mail and calendar application ports.
package graphapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"net/url"
	"strings"

	"github.com/nkiyohara/corresync/internal/provider/restapi"
)

// Options selects services sharing one explicit Microsoft Graph public-client
// grant.
type Options struct {
	APIBase  string
	Address  string
	Mail     bool
	Calendar bool
	HTTP     *http.Client
}

// Client owns one authorized account-scoped Graph transport.
type Client struct {
	api      *restapi.Client
	address  string
	mail     bool
	calendar bool
}

// New confirms only the explicitly selected delegated services. OAuth itself
// remains owned by oauthlocal and cannot be started by this adapter.
func New(ctx context.Context, options Options) (*Client, error) {
	if !options.Mail && !options.Calendar {
		return nil, errors.New("graph route requires mail or calendar")
	}
	if options.Address != "" {
		parsed, err := mail.ParseAddress(options.Address)
		if err != nil || parsed.Address != options.Address || parsed.Name != "" {
			return nil, errors.New("graph account address must be one bare address")
		}
	}
	api, err := restapi.New(restapi.Options{
		BaseURL: options.APIBase,
		HTTP:    options.HTTP,
	})
	if err != nil {
		return nil, err
	}
	client := &Client{
		api: api, address: options.Address,
		mail: options.Mail, calendar: options.Calendar,
	}
	var identity struct {
		ID                string `json:"id"`
		Mail              string `json:"mail"`
		UserPrincipalName string `json:"userPrincipalName"`
	}
	if _, err := api.DoJSON(
		ctx,
		http.MethodGet,
		"me",
		url.Values{"$select": {"id,mail,userPrincipalName"}},
		nil,
		&identity,
		false,
		nil,
		http.StatusOK,
	); err != nil {
		_ = api.Close()
		return nil, fmt.Errorf("confirm Microsoft Graph identity: %w", err)
	}
	if !validGraphID(identity.ID) {
		_ = api.Close()
		return nil, errors.New("graph returned no delegated user identity")
	}
	address := identity.Mail
	if address == "" {
		address = identity.UserPrincipalName
	}
	if address == "" ||
		options.Address != "" && !strings.EqualFold(options.Address, address) {
		_ = api.Close()
		return nil, errors.New("graph grant identity does not match the configured account")
	}
	client.address = address
	if options.Calendar {
		var calendar struct {
			ID      string `json:"id"`
			CanEdit bool   `json:"canEdit"`
		}
		if _, err := api.DoJSON(
			ctx,
			http.MethodGet,
			"me/calendar",
			url.Values{"$select": {"id,canEdit"}},
			nil,
			&calendar,
			false,
			nil,
			http.StatusOK,
		); err != nil {
			_ = api.Close()
			return nil, fmt.Errorf("confirm Microsoft Graph calendar: %w", err)
		}
		if !validGraphID(calendar.ID) || !calendar.CanEdit {
			_ = api.Close()
			return nil, errors.New("graph primary calendar is not editable")
		}
	}
	return client, nil
}

// Close releases account-scoped idle connections.
func (client *Client) Close() error {
	if client == nil {
		return nil
	}
	return client.api.Close()
}

type graphMessageReference struct {
	ID string `json:"id"`
}

type graphFolderReference struct {
	ID string `json:"id"`
}

type graphEventReference struct {
	Calendar string `json:"calendar"`
	Event    string `json:"event"`
}

func encodeMessageID(id string) (string, error) {
	return encodeReference("mgm1_", graphMessageReference{ID: id})
}

func decodeMessageID(value string) (graphMessageReference, error) {
	var reference graphMessageReference
	if err := decodeReference(value, "mgm1_", &reference); err != nil ||
		!validGraphID(reference.ID) {
		return graphMessageReference{}, errors.New("message ID is not a Graph identifier")
	}
	return reference, nil
}

func encodeFolderID(id string) (string, error) {
	return encodeReference("mgf1_", graphFolderReference{ID: id})
}

func decodeFolderID(value string) (graphFolderReference, error) {
	var reference graphFolderReference
	if err := decodeReference(value, "mgf1_", &reference); err != nil ||
		!validGraphID(reference.ID) {
		return graphFolderReference{}, errors.New("mail folder ID is not a Graph identifier")
	}
	return reference, nil
}

func encodeEventID(calendar, event string) (string, error) {
	return encodeReference(
		"mge1_",
		graphEventReference{Calendar: calendar, Event: event},
	)
}

func decodeEventID(value string) (graphEventReference, error) {
	var reference graphEventReference
	if err := decodeReference(value, "mge1_", &reference); err != nil ||
		!validGraphID(reference.Calendar) || !validGraphID(reference.Event) {
		return graphEventReference{}, errors.New("event ID is not a Graph identifier")
	}
	return reference, nil
}

func encodeETag(value string) string {
	return "mgt1_" + base64.RawURLEncoding.EncodeToString([]byte(value))
}

func decodeETag(value string) (string, error) {
	if !strings.HasPrefix(value, "mgt1_") {
		return "", errors.New("change key is not a Graph ETag")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(value, "mgt1_"))
	if err != nil || !validETag(string(decoded)) {
		return "", errors.New("graph ETag is malformed")
	}
	return string(decoded), nil
}

func encodeReference(prefix string, value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(encoded), nil
}

func decodeReference(value, prefix string, target any) error {
	if !strings.HasPrefix(value, prefix) {
		return errors.New("identifier prefix is invalid")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(value, prefix))
	if err != nil || len(decoded) > 8192 {
		return errors.New("identifier is malformed")
	}
	decoder := json.NewDecoder(bytes.NewReader(decoded))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

func validGraphID(value string) bool {
	return value != "" && len(value) <= 4096 &&
		!strings.ContainsAny(value, "\r\n\x00/?#")
}

func validETag(value string) bool {
	return value != "" && len(value) <= 1024 &&
		!strings.ContainsAny(value, "\r\n\x00")
}

func escaped(value string) string {
	return url.PathEscape(value)
}
