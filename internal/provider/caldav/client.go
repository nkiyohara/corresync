// Package caldav adapts RFC 4791 calendar collections to Corresync's closed
// calendar and task application ports.
package caldav

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav/caldav"

	"github.com/nkiyohara/corresync/internal/application"
)

const maximumCalDAVResponseBytes = 8 << 20

// Options identifies one explicitly configured CalDAV account.
type Options struct {
	Endpoint     string
	CalendarPath string
	Username     string
	Password     []byte
	Client       *http.Client
}

// TaskOptions identifies one explicitly configured CalDAV VTODO route.
type TaskOptions struct {
	Endpoint     string
	TaskListPath string
	Username     string
	Password     []byte
	Client       *http.Client
}

// Client owns one external credential and a redirect-rejecting HTTPS client.
type Client struct {
	endpoint     *url.URL
	calendarHome string
	calendarPath string
	calendars    []caldav.Calendar
	taskListPath string
	taskLists    []caldav.Calendar
	taskListInfo map[string]taskListInfo
	username     string
	principal    string
	calendarUser string
	scheduling   bool
	outboxPath   string
	password     []byte
	http         *http.Client
	dav          *caldav.Client
	passwordMu   sync.RWMutex
	close        sync.Once
}

// New performs authenticated principal and calendar discovery. It must only be
// called from explicit local CLI login.
func New(ctx context.Context, options Options) (*Client, error) {
	client, calendars, err := newClient(ctx, connectionOptions{
		endpoint: options.Endpoint,
		username: options.Username,
		password: options.Password,
		client:   options.Client,
	})
	if err != nil {
		return nil, err
	}
	client.discoverScheduling(ctx, client.principal)
	selected, eventCalendars, err := selectCollections(
		calendars,
		ical.CompEvent,
		options.CalendarPath,
		"calendar",
	)
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	client.calendarPath = selected
	client.calendars = eventCalendars
	return client, nil
}

// NewTasks performs authenticated VTODO collection discovery. It must only be
// called from explicit local CLI login.
func NewTasks(ctx context.Context, options TaskOptions) (*Client, error) {
	client, calendars, err := newClient(ctx, connectionOptions{
		endpoint: options.Endpoint,
		username: options.Username,
		password: options.Password,
		client:   options.Client,
	})
	if err != nil {
		return nil, err
	}
	selected, taskLists, err := selectCollections(
		calendars,
		ical.CompToDo,
		options.TaskListPath,
		"task list",
	)
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	client.taskListPath = selected
	client.taskLists = taskLists
	client.taskListInfo, err = client.discoverTaskListInfo(
		ctx, client.calendarHome, taskLists,
	)
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	return client, nil
}

type connectionOptions struct {
	endpoint string
	username string
	password []byte
	client   *http.Client
}

func newClient(
	ctx context.Context,
	options connectionOptions,
) (*Client, []caldav.Calendar, error) {
	endpoint, err := validateHTTPSURL("CalDAV endpoint", options.endpoint)
	if err != nil {
		return nil, nil, err
	}
	if options.username == "" || len(options.username) > 320 ||
		strings.TrimSpace(options.username) != options.username ||
		strings.ContainsAny(options.username, "\r\n\x00") {
		return nil, nil, errors.New("CalDAV username is malformed")
	}
	if len(options.password) == 0 || len(options.password) > 64<<10 {
		return nil, nil, errors.New("CalDAV credential is empty or too large")
	}
	httpClient := options.client
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	} else {
		copy := *httpClient
		httpClient = &copy
		if httpClient.Timeout == 0 {
			httpClient.Timeout = 30 * time.Second
		}
	}
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return errors.New("CalDAV redirects are not accepted")
	}
	var transport *http.Transport
	switch configured := httpClient.Transport.(type) {
	case nil:
		transport = http.DefaultTransport.(*http.Transport).Clone()
	case *http.Transport:
		transport = configured.Clone()
	default:
		return nil, nil, errors.New("CalDAV requires an inspectable HTTP transport")
	}
	if transport.TLSClientConfig != nil {
		transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	} else {
		transport.TLSClientConfig = &tls.Config{}
	}
	if transport.TLSClientConfig.InsecureSkipVerify {
		return nil, nil, errors.New("TLS certificate verification cannot be disabled")
	}
	if transport.TLSClientConfig.MinVersion < tls.VersionTLS12 {
		transport.TLSClientConfig.MinVersion = tls.VersionTLS12
	}
	httpClient.Transport = transport
	client := &Client{
		endpoint: endpoint, username: options.username,
		password: append([]byte(nil), options.password...),
		http:     httpClient,
	}
	dav, err := caldav.NewClient((*authorizedHTTPClient)(client), endpoint.String())
	if err != nil {
		_ = client.Close()
		return nil, nil, err
	}
	client.dav = dav
	principal, err := dav.FindCurrentUserPrincipal(ctx)
	if err != nil {
		_ = client.Close()
		return nil, nil, fmt.Errorf("discover CalDAV principal: %w", err)
	}
	principal, ok := client.davPath(principal)
	if !ok {
		_ = client.Close()
		return nil, nil, errors.New("CalDAV discovery returned an invalid principal path")
	}
	client.principal = principal
	home, err := dav.FindCalendarHomeSet(ctx, principal)
	if err != nil {
		_ = client.Close()
		return nil, nil, fmt.Errorf("discover CalDAV home: %w", err)
	}
	home, ok = client.davPath(home)
	if !ok {
		_ = client.Close()
		return nil, nil, errors.New("CalDAV discovery returned an invalid calendar home path")
	}
	client.calendarHome = home
	calendars, err := dav.FindCalendars(ctx, home)
	if err != nil {
		_ = client.Close()
		return nil, nil, fmt.Errorf("discover CalDAV calendars: %w", err)
	}
	if len(calendars) >
		application.MaxCalendarFolderOffset+application.MaxCalendarFolderPageSize {
		_ = client.Close()
		return nil, nil, errors.New("CalDAV calendar discovery exceeds the configured limit")
	}
	sort.Slice(calendars, func(left, right int) bool {
		return calendars[left].Path < calendars[right].Path
	})
	return client, calendars, nil
}

func selectCollections(
	calendars []caldav.Calendar,
	component, selectedPath, label string,
) (string, []caldav.Calendar, error) {
	if selectedPath != "" && !validDAVPath(selectedPath) {
		return "", nil, fmt.Errorf("CalDAV %s path is malformed", label)
	}
	selected := ""
	matching := make([]caldav.Calendar, 0, len(calendars))
	for _, calendar := range calendars {
		if !supportsComponent(calendar.SupportedComponentSet, component) {
			continue
		}
		if !validDAVPath(calendar.Path) {
			return "", nil, errors.New("CalDAV discovery returned an invalid calendar path")
		}
		matching = append(matching, calendar)
		if selectedPath == "" || calendar.Path == selectedPath {
			if selected == "" {
				selected = calendar.Path
			}
		}
	}
	if selected == "" {
		if selectedPath != "" {
			return "", nil, fmt.Errorf("configured CalDAV %s was not discovered", label)
		}
		return "", nil, fmt.Errorf("CalDAV account has no %s collection", component)
	}
	return selected, matching, nil
}

type schedulingMultiStatus struct {
	Responses []struct {
		Href      string `xml:"href"`
		PropStats []struct {
			Status string `xml:"status"`
			Prop   struct {
				UserAddresses struct {
					Hrefs []string `xml:"href"`
				} `xml:"calendar-user-address-set"`
				Outbox struct {
					Href string `xml:"href"`
				} `xml:"schedule-outbox-URL"`
			} `xml:"prop"`
		} `xml:"propstat"`
	} `xml:"response"`
}

func (client *Client) discoverScheduling(ctx context.Context, principal string) {
	if !validDAVPath(principal) {
		return
	}
	target := *client.endpoint
	target.Path = principal
	target.RawPath = ""
	target.RawQuery = ""
	target.Fragment = ""
	const body = `<?xml version="1.0" encoding="utf-8"?>` +
		`<D:propfind xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">` +
		`<D:prop><C:calendar-user-address-set/><C:schedule-outbox-URL/>` +
		`</D:prop></D:propfind>`
	request, err := http.NewRequestWithContext(
		ctx,
		"PROPFIND",
		target.String(),
		strings.NewReader(body),
	)
	if err != nil {
		return
	}
	request.Header.Set("Content-Type", "application/xml; charset=utf-8")
	request.Header.Set("Depth", "0")
	response, err := (*authorizedHTTPClient)(client).Do(request)
	if err != nil {
		return
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusMultiStatus {
		_, _ = io.Copy(io.Discard, response.Body)
		return
	}
	var result schedulingMultiStatus
	decoder := xml.NewDecoder(response.Body)
	if err := decoder.Decode(&result); err != nil || len(result.Responses) != 1 {
		return
	}
	for _, propstat := range result.Responses[0].PropStats {
		if !strings.Contains(propstat.Status, " 200 ") {
			continue
		}
		outbox, ok := client.davPath(propstat.Prop.Outbox.Href)
		if !ok {
			continue
		}
		address := ""
		for _, href := range propstat.Prop.UserAddresses.Hrefs {
			parsed, err := url.Parse(strings.TrimSpace(href))
			if err != nil || !strings.EqualFold(parsed.Scheme, "mailto") ||
				!bareCalendarAddress(parsed.Opaque) {
				continue
			}
			address = parsed.Opaque
			break
		}
		if address == "" {
			continue
		}
		client.calendarUser = address
		client.outboxPath = outbox
		client.scheduling = true
		return
	}
}

func (client *Client) davPath(raw string) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", false
	}
	if parsed.IsAbs() &&
		(parsed.Scheme != client.endpoint.Scheme ||
			!strings.EqualFold(parsed.Host, client.endpoint.Host)) {
		return "", false
	}
	if !validDAVPath(parsed.Path) {
		return "", false
	}
	return parsed.Path, true
}

// SchedulingAvailable reports an authenticated RFC 6638 server-managed
// scheduling route discovered for this account.
func (client *Client) SchedulingAvailable() bool {
	return client != nil && client.scheduling
}

type authorizedHTTPClient Client

func (client *authorizedHTTPClient) Do(request *http.Request) (*http.Response, error) {
	typed := (*Client)(client)
	typed.passwordMu.RLock()
	request.SetBasicAuth(typed.username, string(typed.password))
	typed.passwordMu.RUnlock()
	response, err := typed.http.Do(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode == http.StatusUnauthorized {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		_ = response.Body.Close()
		return nil, application.NewProviderAuthenticationFailure(
			application.AuthenticationReasonCredentialRejected,
			errors.New("CalDAV server rejected authentication"),
		)
	}
	if response.ContentLength > maximumCalDAVResponseBytes {
		_ = response.Body.Close()
		return nil, errors.New("CalDAV response exceeds the configured limit")
	}
	response.Body = &boundedReadCloser{
		reader: io.LimitReader(response.Body, maximumCalDAVResponseBytes+1),
		body:   response.Body,
		limit:  maximumCalDAVResponseBytes,
	}
	return response, nil
}

type boundedReadCloser struct {
	reader io.Reader
	body   io.Closer
	read   int64
	limit  int64
}

func (reader *boundedReadCloser) Read(buffer []byte) (int, error) {
	count, err := reader.reader.Read(buffer)
	reader.read += int64(count)
	if reader.read > reader.limit {
		return count, errors.New("CalDAV response exceeds the configured limit")
	}
	return count, err
}

func (reader *boundedReadCloser) Close() error {
	return reader.body.Close()
}

func (client *Client) Close() error {
	if client == nil {
		return nil
	}
	client.close.Do(func() {
		client.passwordMu.Lock()
		defer client.passwordMu.Unlock()
		for index := range client.password {
			client.password[index] = 0
		}
		client.password = nil
	})
	return nil
}

func validateHTTPSURL(name, raw string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("%s must be an absolute credential-free HTTPS URL", name)
	}
	return parsed, nil
}

func validDAVPath(value string) bool {
	return strings.HasPrefix(value, "/") && len(value) <= 2048 &&
		!strings.ContainsAny(value, "\r\n\x00?#")
}

func supportsComponent(values []string, component string) bool {
	if len(values) == 0 {
		return true
	}
	for _, value := range values {
		if strings.EqualFold(value, component) {
			return true
		}
	}
	return false
}

type eventReference struct {
	Calendar     string `json:"calendar"`
	Path         string `json:"path"`
	UID          string `json:"uid"`
	RecurrenceID string `json:"recurrenceId,omitempty"`
}

func encodeEventID(reference eventReference) (string, error) {
	data, err := json.Marshal(reference)
	if err != nil {
		return "", err
	}
	return "cde1_" + base64.RawURLEncoding.EncodeToString(data), nil
}

func decodeEventID(value string) (eventReference, error) {
	if !strings.HasPrefix(value, "cde1_") {
		return eventReference{}, errors.New("event ID is not a CalDAV identifier")
	}
	data, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(value, "cde1_"))
	if err != nil || len(data) > 4096 {
		return eventReference{}, errors.New("CalDAV event ID is malformed")
	}
	var reference eventReference
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&reference); err != nil ||
		!validDAVPath(reference.Calendar) ||
		!validDAVPath(reference.Path) ||
		reference.UID == "" ||
		!pathWithin(reference.Path, reference.Calendar) {
		return eventReference{}, errors.New("CalDAV event ID is malformed")
	}
	return reference, nil
}

func pathWithin(candidate, calendar string) bool {
	base := strings.TrimSuffix(path.Clean(calendar), "/") + "/"
	return strings.HasPrefix(path.Clean(candidate), base)
}

func (client *Client) calendarFor(folder application.CalendarFolder) (string, error) {
	if folder.Kind == application.CalendarFolderDistinguished {
		return client.calendarPath, nil
	}
	if !strings.HasPrefix(folder.ID, "cdc1_") {
		return "", errors.New("calendar ID is not a CalDAV identifier")
	}
	data, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(folder.ID, "cdc1_"))
	if err != nil || !validDAVPath(string(data)) {
		return "", errors.New("CalDAV calendar ID is malformed")
	}
	if !client.hasCalendar(string(data)) {
		return "", errors.New("CalDAV calendar was not discovered for this account")
	}
	return string(data), nil
}

func (client *Client) hasCalendar(calendarPath string) bool {
	for _, calendar := range client.calendars {
		if calendar.Path == calendarPath {
			return true
		}
	}
	return false
}

func (client *Client) hasTaskList(taskListPath string) bool {
	for _, taskList := range client.taskLists {
		if taskList.Path == taskListPath {
			return true
		}
	}
	return false
}

func (client *Client) hasCollection(collectionPath string) bool {
	return client.hasCalendar(collectionPath) || client.hasTaskList(collectionPath)
}

func (client *Client) objectURL(
	objectPath string,
	calendarPath string,
) (*url.URL, error) {
	if !client.hasCollection(calendarPath) ||
		!validDAVPath(objectPath) ||
		!pathWithin(objectPath, calendarPath) {
		return nil, errors.New("CalDAV object path escapes the selected calendar")
	}
	target := *client.endpoint
	target.Path = objectPath
	target.RawPath = ""
	target.RawQuery = ""
	target.Fragment = ""
	return &target, nil
}

func (client *Client) conditionalRequest(
	ctx context.Context,
	method, calendarPath, objectPath, conditionName, conditionValue string,
	calendar *ical.Calendar,
) (string, error) {
	return client.conditionalRequestWithHeaders(
		ctx,
		method,
		calendarPath,
		objectPath,
		http.Header{conditionName: {conditionValue}},
		calendar,
	)
}

func (client *Client) conditionalRequestWithHeaders(
	ctx context.Context,
	method, calendarPath, objectPath string,
	headers http.Header,
	calendar *ical.Calendar,
) (string, error) {
	target, err := client.objectURL(objectPath, calendarPath)
	if err != nil {
		return "", err
	}
	var body io.Reader
	if calendar != nil {
		var encoded bytes.Buffer
		if err := ical.NewEncoder(&encoded).Encode(calendar); err != nil {
			return "", err
		}
		if encoded.Len() > application.MaxCalendarBodyBytes+(64<<10) {
			return "", errors.New("encoded calendar object exceeds the limit")
		}
		body = &encoded
	}
	request, err := http.NewRequestWithContext(ctx, method, target.String(), body)
	if err != nil {
		return "", err
	}
	if calendar != nil {
		request.Header.Set("Content-Type", ical.MIMEType)
	}
	for name, values := range headers {
		for _, value := range values {
			request.Header.Add(name, value)
		}
	}
	response, err := (*authorizedHTTPClient)(client).Do(request)
	if err != nil {
		if _, authenticationFailure := application.ProviderAuthenticationReason(err); authenticationFailure {
			return "", err
		}
		return "", fmt.Errorf("%w: execute conditional CalDAV write: %w",
			application.ErrWriteOutcomeUnknown, err)
	}
	defer func() { _ = response.Body.Close() }()
	if _, err := io.Copy(io.Discard, response.Body); err != nil {
		return "", fmt.Errorf(
			"%w: read conditional CalDAV write response: %w",
			application.ErrWriteOutcomeUnknown,
			err,
		)
	}
	switch response.StatusCode {
	case http.StatusCreated, http.StatusNoContent, http.StatusOK:
	case http.StatusPreconditionFailed:
		return "", errors.New("CalDAV write precondition failed")
	default:
		statusErr := fmt.Errorf(
			"CalDAV write returned HTTP %d",
			response.StatusCode,
		)
		if response.StatusCode >= http.StatusInternalServerError ||
			response.StatusCode >= http.StatusOK &&
				response.StatusCode < http.StatusMultipleChoices {
			return "", fmt.Errorf(
				"%w: %w",
				application.ErrWriteOutcomeUnknown,
				statusErr,
			)
		}
		return "", statusErr
	}
	return strongETag(response.Header.Get("ETag")), nil
}

func (client *Client) scheduleTag(
	ctx context.Context,
	calendarPath, objectPath string,
) (string, error) {
	target, err := client.objectURL(objectPath, calendarPath)
	if err != nil {
		return "", err
	}
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodHead,
		target.String(),
		nil,
	)
	if err != nil {
		return "", err
	}
	response, err := (*authorizedHTTPClient)(client).Do(request)
	if err != nil {
		return "", err
	}
	defer func() { _ = response.Body.Close() }()
	_, _ = io.Copy(io.Discard, response.Body)
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf(
			"CalDAV schedule-tag lookup returned HTTP %d",
			response.StatusCode,
		)
	}
	tag := strongETag(response.Header.Get("Schedule-Tag"))
	if tag == "" {
		return "", errors.New(
			"CalDAV scheduling resource returned no strong Schedule-Tag",
		)
	}
	return tag, nil
}

func strongETag(raw string) string {
	if raw == "" || strings.HasPrefix(raw, "W/") {
		return ""
	}
	value, err := strconv.Unquote(raw)
	if err != nil || !validObjectETag(value) {
		return ""
	}
	return value
}

func validObjectETag(value string) bool {
	return value != "" && len(value) <= 1024 &&
		!strings.ContainsAny(value, "\"\\\r\n\x00") &&
		!strings.ContainsFunc(value, func(character rune) bool {
			return character < 0x21 || character == 0x7f
		})
}

func newUID() (string, error) {
	value := make([]byte, 18)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value) + "@corresync.invalid", nil
}
