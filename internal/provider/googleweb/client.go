// Package googleweb adapts a visible, browser-owned Google session without
// extracting cookies, passwords, bearer tokens, or general browser storage.
package googleweb

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"net/url"
	"strings"

	"github.com/nkiyohara/corresync/internal/browser"
)

const (
	defaultMailOrigin     = "https://mail.google.com"
	defaultCalendarOrigin = "https://calendar.google.com"
)

// Driver is the closed semantic browser surface used by this adapter.
// Implementations retain all authorization material inside the browser.
type Driver interface {
	WaitForGoogleWeb(context.Context, []string) error
	GoogleIdentity(context.Context, string) (string, error)
	GoogleMailRows(context.Context, string) (browser.GoogleMailSnapshot, error)
	GoogleMailBody(context.Context, string) (string, error)
	GoogleCalendarRows(context.Context, string) (browser.GoogleCalendarSnapshot, error)
}

// Options identifies one isolated, visible Google browser profile.
type Options struct {
	ExpectedAddress string
	MailOrigin      string
	CalendarOrigin  string
	Mail            bool
	Calendar        bool
	Driver          Driver
}

// Client maps closed typed operations to semantic browser projections.
type Client struct {
	mailOrigin     *url.URL
	calendarOrigin *url.URL
	mail           bool
	calendar       bool
	driver         Driver
}

// New waits for visible interactive sign-in to reach every configured service.
func New(ctx context.Context, options Options) (*Client, error) {
	if !options.Mail && !options.Calendar {
		return nil, errors.New("google Web route requires mail or calendar")
	}
	if options.Driver == nil {
		return nil, errors.New("google Web browser driver is required")
	}
	if options.ExpectedAddress != "" {
		parsed, err := mail.ParseAddress(options.ExpectedAddress)
		if err != nil || parsed.Address != options.ExpectedAddress ||
			parsed.Name != "" {
			return nil, errors.New("google Web account address must be one bare address")
		}
	}
	client := &Client{
		mail: options.Mail, calendar: options.Calendar, driver: options.Driver,
	}
	origins := make([]string, 0, 2)
	var err error
	if options.Mail {
		client.mailOrigin, err = googleOrigin(
			"google Web mail origin",
			options.MailOrigin,
			defaultMailOrigin,
			"mail.google.com",
		)
		if err != nil {
			return nil, err
		}
		origins = append(origins, client.mailOrigin.String())
	}
	if options.Calendar {
		client.calendarOrigin, err = googleOrigin(
			"google Web calendar origin",
			options.CalendarOrigin,
			defaultCalendarOrigin,
			"calendar.google.com",
		)
		if err != nil {
			return nil, err
		}
		origins = append(origins, client.calendarOrigin.String())
	}
	if err := client.driver.WaitForGoogleWeb(ctx, origins); err != nil {
		return nil, fmt.Errorf(
			"google Web sign-in did not reach the configured application; "+
				"complete browser sign-in and verify Workspace service access: %w",
			err,
		)
	}
	confirmedAddress := ""
	for _, origin := range origins {
		address, err := client.driver.GoogleIdentity(
			ctx,
			googleApplicationURL(origin),
		)
		if err != nil {
			return nil, fmt.Errorf("confirm google Web account identity: %w", err)
		}
		if address == "" ||
			confirmedAddress != "" && !strings.EqualFold(confirmedAddress, address) ||
			options.ExpectedAddress != "" &&
				!strings.EqualFold(options.ExpectedAddress, address) {
			return nil, errors.New(
				"google Web browser identity does not match the configured account",
			)
		}
		confirmedAddress = address
	}
	return client, nil
}

func googleApplicationURL(origin string) string {
	switch origin {
	case defaultMailOrigin:
		return origin + "/mail/u/0/#inbox"
	case defaultCalendarOrigin:
		return origin + "/calendar/u/0/r/agenda"
	default:
		return origin
	}
}

func googleOrigin(name, raw, fallback, host string) (*url.URL, error) {
	if raw == "" {
		raw = fallback
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host != host ||
		parsed.User != nil || parsed.Path != "" && parsed.Path != "/" ||
		parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New(name + " must be the exact provider-owned HTTPS origin")
	}
	parsed.Path = ""
	return parsed, nil
}

func encodeReference(prefix string, value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(encoded), nil
}

func decodeReference(value, prefix string, target any) error {
	if !strings.HasPrefix(value, prefix) || len(value) > 8192 {
		return errors.New("google Web identifier is malformed")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(value, prefix))
	if err != nil || len(decoded) == 0 || len(decoded) > 4096 ||
		json.Unmarshal(decoded, target) != nil {
		return errors.New("google Web identifier is malformed")
	}
	return nil
}
