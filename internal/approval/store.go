// Package approval issues and consumes exact, caller-bound operation previews.
package approval

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/nkiyohara/corresync/internal/domain"
)

const (
	defaultTTL        = 2 * time.Minute
	defaultMaxPending = 128
	tokenBytes        = 32
	tokenPrefix       = "opv1_"
)

var (
	// ErrInvalidToken deliberately combines missing, malformed, wrong-caller,
	// expired, and replayed tokens so callers cannot probe pending approvals.
	ErrInvalidToken = errors.New("invalid or expired approval token")
	ErrCapacity     = errors.New("approval capacity reached")
)

// Clock makes expiry deterministic in tests.
type Clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// Options configures bounded in-memory approval storage.
type Options struct {
	TTL        time.Duration
	MaxPending int
	Clock      Clock
	Random     io.Reader
}

// Preview is safe metadata returned before a consequential operation.
// Token is a secret capability and must never be logged.
type Preview struct {
	Token     string               `json:"token"`
	ExpiresAt time.Time            `json:"expiresAt"`
	Operation domain.OperationView `json:"operation"`
}

type pending struct {
	operation domain.Operation
	caller    domain.Caller
	expiresAt time.Time
}

// Store holds pending operations only in process memory.
type Store struct {
	mu         sync.Mutex
	clock      Clock
	random     io.Reader
	ttl        time.Duration
	maxPending int
	pending    map[string]pending
}

// NewStore creates a bounded, concurrency-safe approval store.
func NewStore(options Options) (*Store, error) {
	if options.TTL == 0 {
		options.TTL = defaultTTL
	}
	if options.TTL < time.Second || options.TTL > 15*time.Minute {
		return nil, errors.New("approval TTL must be between 1 second and 15 minutes")
	}
	if options.MaxPending == 0 {
		options.MaxPending = defaultMaxPending
	}
	if options.MaxPending < 1 || options.MaxPending > 4096 {
		return nil, errors.New("max pending approvals must be between 1 and 4096")
	}
	if options.Clock == nil {
		options.Clock = realClock{}
	}
	if options.Random == nil {
		options.Random = rand.Reader
	}

	return &Store{
		clock:      options.Clock,
		random:     options.Random,
		ttl:        options.TTL,
		maxPending: options.MaxPending,
		pending:    make(map[string]pending),
	}, nil
}

// Issue stores an immutable operation and returns a short-lived capability.
func (store *Store) Issue(operation domain.Operation, caller domain.Caller) (Preview, error) {
	if err := caller.Validate(); err != nil {
		return Preview{}, fmt.Errorf("validate approval caller: %w", err)
	}

	bytes := make([]byte, tokenBytes)
	if _, err := io.ReadFull(store.random, bytes); err != nil {
		return Preview{}, fmt.Errorf("generate approval token: %w", err)
	}
	token := tokenPrefix + base64.RawURLEncoding.EncodeToString(bytes)
	now := store.clock.Now()
	expiresAt := now.Add(store.ttl)

	store.mu.Lock()
	defer store.mu.Unlock()
	store.pruneExpired(now)
	if len(store.pending) >= store.maxPending {
		return Preview{}, ErrCapacity
	}
	if _, exists := store.pending[token]; exists {
		return Preview{}, errors.New("approval token collision")
	}
	store.pending[token] = pending{
		operation: operation,
		caller:    caller,
		expiresAt: expiresAt,
	}

	return Preview{
		Token:     token,
		ExpiresAt: expiresAt,
		Operation: operation.View(),
	}, nil
}

// Consume returns and removes the operation bound to token and caller.
func (store *Store) Consume(token string, caller domain.Caller) (domain.Operation, error) {
	return store.consume(token, caller, "", "")
}

// ConsumeFor atomically verifies the expected operation class before consuming
// a token. A mismatched tool cannot burn a valid approval for another action.
func (store *Store) ConsumeFor(
	token string,
	caller domain.Caller,
	name string,
	effect domain.Effect,
) (domain.Operation, error) {
	return store.consume(token, caller, name, effect)
}

func (store *Store) consume(
	token string,
	caller domain.Caller,
	name string,
	effect domain.Effect,
) (domain.Operation, error) {
	if err := caller.Validate(); err != nil {
		return domain.Operation{}, ErrInvalidToken
	}
	if len(token) != len(tokenPrefix)+base64.RawURLEncoding.EncodedLen(tokenBytes) ||
		token[:len(tokenPrefix)] != tokenPrefix {
		return domain.Operation{}, ErrInvalidToken
	}

	store.mu.Lock()
	defer store.mu.Unlock()

	entry, exists := store.pending[token]
	if !exists || entry.caller != caller {
		return domain.Operation{}, ErrInvalidToken
	}
	if !store.clock.Now().Before(entry.expiresAt) {
		delete(store.pending, token)
		return domain.Operation{}, ErrInvalidToken
	}
	if name != "" && (entry.operation.Name() != name || entry.operation.Effect() != effect) {
		return domain.Operation{}, ErrInvalidToken
	}
	delete(store.pending, token)
	return entry.operation, nil
}

func (store *Store) pruneExpired(now time.Time) {
	for token, entry := range store.pending {
		if !now.Before(entry.expiresAt) {
			delete(store.pending, token)
		}
	}
}
