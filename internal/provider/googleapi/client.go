// Package googleapi adapts the explicitly authorized Google Calendar API to
// Corresync's closed calendar application port. Google mail uses the separate
// Gmail IMAP/SMTP XOAUTH2 route.
package googleapi

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

// Options selects one explicit Google Calendar public-client grant.
type Options struct {
	APIBase string
	Address string
	HTTP    *http.Client
}

// Client owns one authorized account-scoped Google API transport.
type Client struct {
	api  *restapi.Client
	meet bool
}

// New confirms the configured primary calendar identity and write role with a
// read-only request. Authorization itself is owned by oauthlocal.
func New(ctx context.Context, options Options) (*Client, error) {
	if options.Address != "" {
		parsed, err := mail.ParseAddress(options.Address)
		if err != nil || parsed.Address != options.Address || parsed.Name != "" {
			return nil, errors.New("google account address must be one bare address")
		}
	}
	api, err := restapi.New(restapi.Options{
		BaseURL: options.APIBase, HTTP: options.HTTP,
	})
	if err != nil {
		return nil, err
	}
	client := &Client{api: api}
	var calendar struct {
		ID                   string `json:"id"`
		AccessRole           string `json:"accessRole"`
		ConferenceProperties struct {
			AllowedSolutionTypes []string `json:"allowedConferenceSolutionTypes"`
		} `json:"conferenceProperties"`
	}
	if _, err := api.DoJSON(
		ctx, http.MethodGet, "calendar/v3/users/me/calendarList/primary", nil,
		nil, &calendar, false, nil, http.StatusOK,
	); err != nil {
		_ = api.Close()
		return nil, fmt.Errorf("confirm Google Calendar access: %w", err)
	}
	if calendar.ID == "" {
		_ = api.Close()
		return nil, errors.New("google Calendar returned no primary calendar identity")
	}
	if options.Address != "" &&
		!strings.EqualFold(calendar.ID, options.Address) {
		_ = api.Close()
		return nil, errors.New(
			"google Calendar grant identity does not match the configured account",
		)
	}
	if calendar.AccessRole != "owner" && calendar.AccessRole != "writer" {
		_ = api.Close()
		return nil, errors.New("google primary calendar is not editable")
	}
	for _, solution := range calendar.ConferenceProperties.AllowedSolutionTypes {
		if solution == "hangoutsMeet" {
			client.meet = true
			break
		}
	}
	return client, nil
}

// MeetAvailable reports the authenticated primary calendar's observed,
// side-effect-free conference capability.
func (client *Client) MeetAvailable() bool {
	return client != nil && client.meet
}

// Close releases account-scoped idle connections.
func (client *Client) Close() error {
	if client == nil {
		return nil
	}
	return client.api.Close()
}

type googleCalendarReference struct {
	Calendar string `json:"calendar"`
	Event    string `json:"event"`
}

func encodeEventID(calendarID, eventID string) (string, error) {
	return encodeReference("gge1_", googleCalendarReference{
		Calendar: calendarID, Event: eventID,
	})
}

func decodeEventID(value string) (googleCalendarReference, error) {
	var reference googleCalendarReference
	if err := decodeReference(value, "gge1_", &reference); err != nil ||
		!validGoogleID(reference.Calendar) || !validGoogleID(reference.Event) {
		return googleCalendarReference{}, errors.New("event ID is not a Google identifier")
	}
	return reference, nil
}

func encodeETag(value string) string {
	return "gget1_" + base64.RawURLEncoding.EncodeToString([]byte(value))
}

func decodeETag(value string) (string, error) {
	if !strings.HasPrefix(value, "gget1_") {
		return "", errors.New("event change key is not a Google ETag")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(value, "gget1_"))
	if err != nil || !validETag(string(decoded)) {
		return "", errors.New("google event change key is malformed")
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

func validGoogleID(value string) bool {
	return value != "" && value != "." && value != ".." && len(value) <= 4096 &&
		!strings.ContainsAny(value, "\r\n\x00")
}

func validETag(value string) bool {
	return value != "" && len(value) <= 1024 &&
		!strings.ContainsAny(value, "\r\n\x00")
}

func escaped(value string) string {
	return url.PathEscape(value)
}
