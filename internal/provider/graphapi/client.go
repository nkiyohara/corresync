// Package graphapi adapts an explicitly authorized delegated Microsoft Graph
// account to Corresync's closed mail, calendar, and task application ports.
package graphapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"net/url"
	"strings"

	"github.com/nkiyohara/corresync/internal/provider/restapi"
)

// Options selects services sharing one explicit Microsoft Graph public-client
// grant.
type Options struct {
	APIBase   string
	Address   string
	Mail      bool
	Calendar  bool
	Tasks     bool
	TaskWrite bool
	HTTP      *http.Client
}

// Client owns one authorized account-scoped Graph transport.
type Client struct {
	api       *restapi.Client
	apiBase   *url.URL
	userID    string
	address   string
	mail      bool
	calendar  bool
	tasks     bool
	taskWrite bool
}

// New confirms only the explicitly selected delegated services. OAuth itself
// remains owned by oauthlocal and cannot be started by this adapter.
func New(ctx context.Context, options Options) (*Client, error) {
	if !options.Mail && !options.Calendar && !options.Tasks {
		return nil, errors.New("graph route requires mail, calendar, or tasks")
	}
	if options.TaskWrite && !options.Tasks {
		return nil, errors.New("graph task writes require the task service")
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
	apiBase, err := url.Parse(options.APIBase)
	if err != nil {
		_ = api.Close()
		return nil, errors.New("graph API base is malformed")
	}
	client := &Client{
		api: api, apiBase: apiBase, address: options.Address,
		mail: options.Mail, calendar: options.Calendar,
		tasks: options.Tasks, taskWrite: options.TaskWrite,
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
	client.userID = identity.ID
	address := identity.Mail
	if address == "" {
		address = identity.UserPrincipalName
	}
	addressMatches := options.Address == "" ||
		identity.Mail != "" && strings.EqualFold(options.Address, identity.Mail) ||
		identity.UserPrincipalName != "" && strings.EqualFold(options.Address, identity.UserPrincipalName)
	if address == "" || !addressMatches {
		_ = api.Close()
		return nil, errors.New("graph grant identity does not match the configured account")
	}
	client.address = address
	if options.Mail {
		var inbox struct {
			ID string `json:"id"`
		}
		if _, err := api.DoJSON(
			ctx,
			http.MethodGet,
			"me/mailFolders/inbox",
			url.Values{"$select": {"id"}},
			nil,
			&inbox,
			false,
			nil,
			http.StatusOK,
		); err != nil {
			_ = api.Close()
			return nil, fmt.Errorf("confirm Microsoft Graph mail: %w", err)
		}
		if !validGraphID(inbox.ID) {
			_ = api.Close()
			return nil, errors.New("graph returned no primary mail folder")
		}
	}
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
	if options.Tasks {
		var lists struct {
			Value []struct {
				ID string `json:"id"`
			} `json:"value"`
		}
		if _, err := api.DoJSON(
			ctx,
			http.MethodGet,
			"me/todo/lists",
			url.Values{"$select": {"id"}, "$top": {"1"}},
			nil,
			&lists,
			false,
			nil,
			http.StatusOK,
		); err != nil {
			_ = api.Close()
			return nil, fmt.Errorf("confirm Microsoft To Do: %w", err)
		}
		if len(lists.Value) > 1 || len(lists.Value) == 1 && !validGraphID(lists.Value[0].ID) {
			_ = api.Close()
			return nil, errors.New("graph returned an invalid To Do task-list probe")
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
	return decodeStrictJSON(decoded, target)
}

func decodeStrictJSON(encoded []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("JSON value has trailing data")
		}
		return err
	}
	return nil
}

func validGraphID(value string) bool {
	return value != "" && value != "." && value != ".." && len(value) <= 4096 &&
		!strings.ContainsAny(value, "\r\n\x00/?#")
}

func validETag(value string) bool {
	return value != "" && len(value) <= 1024 &&
		!strings.ContainsAny(value, "\r\n\x00")
}

func (client *Client) folderContinuation(
	value string,
) (string, url.Values, error) {
	if client == nil || client.apiBase == nil ||
		value == "" || len(value) > 16<<10 ||
		strings.ContainsAny(value, "\r\n\x00") {
		return "", nil, errors.New("graph continuation URL is malformed")
	}
	target, err := url.Parse(value)
	if err != nil ||
		target.Scheme != client.apiBase.Scheme ||
		target.Host != client.apiBase.Host ||
		target.User != nil ||
		target.Fragment != "" {
		return "", nil, errors.New(
			"graph continuation URL escaped the configured origin",
		)
	}
	basePath := strings.TrimSuffix(client.apiBase.EscapedPath(), "/") + "/"
	if !strings.HasPrefix(target.EscapedPath(), basePath) {
		return "", nil, errors.New(
			"graph continuation URL escaped the configured base path",
		)
	}
	resource := strings.TrimPrefix(target.EscapedPath(), basePath)
	if resource != "me/mailFolders" &&
		!strings.HasPrefix(resource, "me/mailFolders/") {
		return "", nil, errors.New(
			"graph folder continuation escaped the mail-folder collection",
		)
	}
	query, err := url.ParseQuery(target.RawQuery)
	if err != nil {
		return "", nil, errors.New("graph continuation query is malformed")
	}
	return resource, query, nil
}

func escaped(value string) string {
	return url.PathEscape(value)
}
