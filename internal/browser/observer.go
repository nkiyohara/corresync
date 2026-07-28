package browser

import (
	"net/http"
	"sync"

	"github.com/chromedp/cdproto/network"

	"github.com/nkiyohara/corresync/internal/session"
)

const maximumPendingExtraHeaders = 2048

type requestObserver struct {
	mu       sync.Mutex
	sessions *session.Manager
	eligible map[network.RequestID]string
	early    map[network.RequestID]http.Header
}

func newRequestObserver(sessions *session.Manager) *requestObserver {
	return &requestObserver{
		sessions: sessions,
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
		return
	}
	headers := convertHeaders(event.Request.Headers)
	observer.sessions.Observe(rawURL, headers)

	observer.mu.Lock()
	defer observer.mu.Unlock()
	observer.eligible[event.RequestID] = rawURL
	if extra, exists := observer.early[event.RequestID]; exists {
		observer.sessions.Observe(rawURL, extra)
		delete(observer.early, event.RequestID)
	}
}

func (observer *requestObserver) extra(event *network.EventRequestWillBeSentExtraInfo) {
	headers := convertHeaders(event.Headers)
	observer.mu.Lock()
	defer observer.mu.Unlock()
	if rawURL, exists := observer.eligible[event.RequestID]; exists {
		observer.sessions.Observe(rawURL, headers)
		return
	}
	if len(observer.early) >= maximumPendingExtraHeaders {
		clear(observer.early)
	}
	observer.early[event.RequestID] = headers
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
