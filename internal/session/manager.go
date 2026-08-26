package session

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// Manager retains only the latest authorization snapshot for one exact origin.
type Manager struct {
	mu      sync.RWMutex
	origin  string
	current *Credentials
	ready   chan struct{}
	once    sync.Once
	clock   func() time.Time
}

// NewManager creates an empty session manager for an approved HTTPS origin.
func NewManager(rawOrigin string) (*Manager, error) {
	parsed, err := url.Parse(rawOrigin)
	if err != nil {
		return nil, fmt.Errorf("parse session origin: %w", err)
	}
	origin := requestOrigin(parsed)
	if origin == "" || parsed.Path != "" && parsed.Path != "/" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("session origin must be an HTTPS origin without a path, query, or fragment")
	}
	return &Manager{
		origin: origin,
		ready:  make(chan struct{}),
		clock:  time.Now,
	}, nil
}

// Allows reports whether a request URL is inside the exact approved origin.
func (manager *Manager) Allows(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	return err == nil && requestOrigin(parsed) == manager.origin
}

// Origin returns the exact content-free HTTPS boundary this manager accepts.
func (manager *Manager) Origin() string { return manager.origin }

// Observe updates the in-memory snapshot when a request contains valid bearer
// authorization for the approved origin. It never retains unrelated headers.
func (manager *Manager) Observe(rawURL string, headers http.Header) bool {
	return manager.ObserveAuthorization(rawURL, headers) == nil
}

// ObserveAuthorization updates the current snapshot or returns a bounded,
// content-free reason why an eligible request could not establish a session.
func (manager *Manager) ObserveAuthorization(rawURL string, headers http.Header) error {
	if !manager.Allows(rawURL) {
		return errors.New("observed Outlook Web request is outside the configured origin")
	}
	credentials, err := newCredentials(rawURL, headers, manager.clock())
	if err != nil {
		return err
	}

	manager.mu.Lock()
	manager.current = &credentials
	manager.mu.Unlock()
	manager.once.Do(func() { close(manager.ready) })
	return nil
}

// Ready closes after the first valid authorization snapshot is captured.
func (manager *Manager) Ready() <-chan struct{} { return manager.ready }

// Current returns a defensive copy of the most recent snapshot.
func (manager *Manager) Current() (Credentials, error) {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	if manager.current == nil {
		return Credentials{}, ErrNotReady
	}
	copy := *manager.current
	copy.headers = manager.current.headers.Clone()
	return copy, nil
}

// Wait blocks until the browser emits an authorized request or context ends.
func (manager *Manager) Wait(ctx context.Context) (Credentials, error) {
	select {
	case <-ctx.Done():
		return Credentials{}, ctx.Err()
	case <-manager.ready:
		return manager.Current()
	}
}

// Apply uses the latest snapshot to authorize an exact-origin request.
func (manager *Manager) Apply(request *http.Request) error {
	credentials, err := manager.Current()
	if err != nil {
		return err
	}
	return credentials.Apply(request)
}
