package browser

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/chromedp/cdproto/network"

	"github.com/nkiyohara/corresync/internal/session"
)

const maximumPendingExtraHeaders = 2048

type requestObserver struct {
	mu       sync.Mutex
	sessions *session.Manager
	report   func(error)
	eligible map[network.RequestID]string
	early    map[network.RequestID]http.Header
}

func newRequestObserver(
	sessions *session.Manager,
	report func(error),
) *requestObserver {
	return &requestObserver{
		sessions: sessions,
		report:   report,
		eligible: make(map[network.RequestID]string),
		early:    make(map[network.RequestID]http.Header),
	}
}

func (observer *requestObserver) Handle(event any) {
	switch typed := event.(type) {
	case *network.EventRequestWillBeSent:
		observer.request(typed)
	case *network.EventRequestWillBeSentExtraInfo:
		observer.extra(typed)
	case *network.EventLoadingFinished:
		observer.done(typed.RequestID)
	case *network.EventLoadingFailed:
		observer.done(typed.RequestID)
	}
}

func (observer *requestObserver) request(event *network.EventRequestWillBeSent) {
	rawURL := event.Request.URL
	if !observer.sessions.Allows(rawURL) {
		if observed, ok := outlookServiceOrigin(event.Request); ok {
			observer.reportFailure(&sessionOriginMismatchError{
				configured: observer.sessions.Origin(),
				observed:   observed,
			})
		}
		return
	}
	headers := convertHeaders(event.Request.Headers)
	observer.observe(rawURL, headers)

	observer.mu.Lock()
	defer observer.mu.Unlock()
	observer.eligible[event.RequestID] = rawURL
	if extra, exists := observer.early[event.RequestID]; exists {
		observer.observe(rawURL, extra)
		delete(observer.early, event.RequestID)
	}
}

func (observer *requestObserver) extra(event *network.EventRequestWillBeSentExtraInfo) {
	headers := convertHeaders(event.Headers)
	observer.mu.Lock()
	defer observer.mu.Unlock()
	if rawURL, exists := observer.eligible[event.RequestID]; exists {
		observer.observe(rawURL, headers)
		return
	}
	if len(observer.early) >= maximumPendingExtraHeaders {
		clear(observer.early)
	}
	observer.early[event.RequestID] = headers
}

func (observer *requestObserver) observe(rawURL string, headers http.Header) {
	if headers.Get("Authorization") == "" {
		return
	}
	err := observer.sessions.ObserveAuthorization(rawURL, headers)
	if errors.Is(err, session.ErrAuthorizationScheme) ||
		errors.Is(err, session.ErrAuthorizationInvalid) {
		observer.reportFailure(err)
	}
}

func (observer *requestObserver) reportFailure(failure error) {
	if observer.report != nil {
		observer.report(failure)
	}
}

type sessionOriginMismatchError struct {
	configured string
	observed   string
}

func (failure *sessionOriginMismatchError) Error() string {
	return "Outlook Web used service origin " + failure.observed +
		" instead of the configured exact origin " + failure.configured
}

func outlookServiceOrigin(request *network.Request) (string, bool) {
	if request == nil {
		return "", false
	}
	target, err := url.Parse(request.URL)
	if err != nil || target.Scheme != "https" || target.Host == "" ||
		target.User != nil || !strings.EqualFold(target.Path, "/owa/service.svc") {
		return "", false
	}
	headers := convertHeaders(request.Headers)
	if headers.Get("Action") == "" && headers.Get("X-Owa-Actionname") == "" {
		return "", false
	}
	return "https://" + strings.ToLower(target.Host), true
}

func (observer *requestObserver) done(requestID network.RequestID) {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	delete(observer.eligible, requestID)
	delete(observer.early, requestID)
}

func convertHeaders(headers network.Headers) http.Header {
	converted := make(http.Header)
	for name, rawValue := range headers {
		if value, ok := rawValue.(string); ok {
			converted.Add(name, value)
		}
	}
	return converted
}
