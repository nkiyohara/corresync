// Package googleapi adapts explicitly authorized Gmail and Google Calendar
// APIs to Corresync's closed application ports.
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
	"strconv"
	"strings"
	"time"

	"github.com/nkiyohara/corresync/internal/provider/restapi"
)

// Options selects services sharing one explicit Google public-client grant.
type Options struct {
	APIBase  string
	Address  string
	Mail     bool
	Calendar bool
	HTTP     *http.Client
}

// Client owns one authorized account-scoped Google API transport.
type Client struct {
	api      *restapi.Client
	address  string
	mail     bool
	calendar bool
}

// New confirms only the explicitly configured services with read-only profile
// requests. Authorization itself is owned by oauthlocal.
func New(ctx context.Context, options Options) (*Client, error) {
	if !options.Mail && !options.Calendar {
		return nil, errors.New("google API route requires mail or calendar")
	}
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
	client := &Client{
		api: api, address: options.Address,
		mail: options.Mail, calendar: options.Calendar,
	}
	if options.Mail {
		var profile struct {
			EmailAddress string `json:"emailAddress"`
		}
		if _, err := api.DoJSON(
			ctx, http.MethodGet, "gmail/v1/users/me/profile", nil,
			nil, &profile, false, nil, http.StatusOK,
		); err != nil {
			_ = api.Close()
			return nil, fmt.Errorf("confirm Gmail access: %w", err)
		}
		if profile.EmailAddress == "" ||
			options.Address != "" &&
				!strings.EqualFold(profile.EmailAddress, options.Address) {
			_ = api.Close()
			return nil, errors.New("gmail grant identity does not match the configured account")
		}
		client.address = profile.EmailAddress
	}
	if options.Calendar {
		var calendar struct {
			ID         string `json:"id"`
			AccessRole string `json:"accessRole"`
		}
		if _, err := api.DoJSON(
			ctx, http.MethodGet, "calendar/v3/calendars/primary", nil,
			nil, &calendar, false, nil, http.StatusOK,
		); err != nil {
			_ = api.Close()
			return nil, fmt.Errorf("confirm Google Calendar access: %w", err)
		}
		if calendar.ID == "" {
			_ = api.Close()
			return nil, errors.New("google Calendar returned no primary calendar identity")
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

type googleMessageReference struct {
	ID string `json:"id"`
}

func encodeMessageID(id string) (string, error) {
	return encodeReference("ggm1_", googleMessageReference{ID: id})
}

func decodeMessageID(value string) (googleMessageReference, error) {
	var reference googleMessageReference
	if err := decodeReference(value, "ggm1_", &reference); err != nil ||
		!validGoogleID(reference.ID) {
		return googleMessageReference{}, errors.New("message ID is not a Google identifier")
	}
	return reference, nil
}

func encodeHistoryID(value string) string {
	return "ggh1_" + base64.RawURLEncoding.EncodeToString([]byte(value))
}

func decodeHistoryID(value string) (string, error) {
	if !strings.HasPrefix(value, "ggh1_") {
		return "", errors.New("message change key is not a Google history identifier")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(value, "ggh1_"))
	if err != nil || !validGoogleID(string(decoded)) {
		return "", errors.New("google message change key is malformed")
	}
	return string(decoded), nil
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

func millisecondsTime(value string) string {
	milliseconds, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return ""
	}
	return time.UnixMilli(milliseconds).UTC().Format(time.RFC3339)
}
