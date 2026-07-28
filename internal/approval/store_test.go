package approval

import (
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nkiyohara/corresync/internal/domain"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (clock *fakeClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *fakeClock) Advance(duration time.Duration) {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	clock.now = clock.now.Add(duration)
}

func testOperation(t *testing.T) domain.Operation {
	t.Helper()
	operation, err := domain.NewOperation(
		"mail.send",
		domain.EffectExternalWrite,
		"work",
		struct {
			Subject string `json:"subject"`
		}{Subject: "Synthetic message"},
	)
	if err != nil {
		t.Fatalf("NewOperation() error = %v", err)
	}
	return operation
}

func TestStoreIssueAndConsumeOnce(t *testing.T) {
	t.Parallel()

	store, err := NewStore(Options{})
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	caller := domain.Caller{Surface: "mcp", Instance: "session-1"}
	operation := testOperation(t)

	preview, err := store.Issue(operation, caller)
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	if preview.Token == "" || preview.Operation.Digest != operation.View().Digest {
		t.Fatalf("invalid preview: %+v", preview)
	}

	consumed, err := store.Consume(preview.Token, caller)
	if err != nil {
		t.Fatalf("Consume() error = %v", err)
	}
	if consumed.View() != operation.View() {
		t.Fatalf("consumed operation = %+v, want %+v", consumed.View(), operation.View())
	}
	if _, err := store.Consume(preview.Token, caller); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("replayed Consume() error = %v, want ErrInvalidToken", err)
	}
}

func TestStoreDoesNotConsumeForWrongOperationClass(t *testing.T) {
	t.Parallel()

	store, err := NewStore(Options{})
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	caller := domain.Caller{Surface: "mcp", Instance: "session-1"}
	preview, err := store.Issue(testOperation(t), caller)
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	if _, err := store.ConsumeFor(
		preview.Token, caller, "mail.get_body", domain.EffectSensitiveRead,
	); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("wrong-class ConsumeFor() error = %v", err)
	}
	if _, err := store.ConsumeFor(
		preview.Token, caller, "mail.send", domain.EffectExternalWrite,
	); err != nil {
		t.Fatalf("matching ConsumeFor() error = %v", err)
	}
}

func TestStoreBindsCallerWithoutConsumingOnMismatch(t *testing.T) {
	t.Parallel()

	store, err := NewStore(Options{})
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	original := domain.Caller{Surface: "cli", Instance: "process-1"}
	preview, err := store.Issue(testOperation(t), original)
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}

	other := domain.Caller{Surface: "mcp", Instance: "process-1"}
	if _, err := store.Consume(preview.Token, other); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("wrong-caller Consume() error = %v, want ErrInvalidToken", err)
	}
	if _, err := store.Consume(preview.Token, original); err != nil {
		t.Fatalf("original caller could not consume token: %v", err)
	}
}

func TestStoreExpiresAndPrunes(t *testing.T) {
	t.Parallel()

	clock := &fakeClock{now: time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)}
	store, err := NewStore(Options{TTL: time.Second, MaxPending: 1, Clock: clock})
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	caller := domain.Caller{Surface: "cli", Instance: "process-1"}
	preview, err := store.Issue(testOperation(t), caller)
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	clock.Advance(time.Second)
	if _, err := store.Consume(preview.Token, caller); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expired Consume() error = %v, want ErrInvalidToken", err)
	}
	if _, err := store.Issue(testOperation(t), caller); err != nil {
		t.Fatalf("Issue() after expiry error = %v", err)
	}
}

func TestStoreEnforcesCapacity(t *testing.T) {
	t.Parallel()

	store, err := NewStore(Options{MaxPending: 1})
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	caller := domain.Caller{Surface: "cli", Instance: "process-1"}
	if _, err := store.Issue(testOperation(t), caller); err != nil {
		t.Fatalf("first Issue() error = %v", err)
	}
	if _, err := store.Issue(testOperation(t), caller); !errors.Is(err, ErrCapacity) {
		t.Fatalf("second Issue() error = %v, want ErrCapacity", err)
	}
}

func TestStoreRejectsInvalidOptionsAndRandomFailure(t *testing.T) {
	t.Parallel()

	for _, options := range []Options{
		{TTL: time.Nanosecond},
		{TTL: 16 * time.Minute},
		{MaxPending: -1},
		{MaxPending: 4097},
	} {
		if _, err := NewStore(options); err == nil {
			t.Fatalf("NewStore(%+v) unexpectedly succeeded", options)
		}
	}

	store, err := NewStore(Options{Random: io.LimitReader(&zeroReader{}, 0)})
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	caller := domain.Caller{Surface: "cli", Instance: "process-1"}
	if _, err := store.Issue(testOperation(t), caller); err == nil {
		t.Fatal("Issue() unexpectedly succeeded with failing random source")
	}
}

type zeroReader struct{}

func (*zeroReader) Read(buffer []byte) (int, error) {
	for index := range buffer {
		buffer[index] = 0
	}
	return len(buffer), nil
}

func TestStoreAllowsExactlyOneConcurrentConsumer(t *testing.T) {
	t.Parallel()

	store, err := NewStore(Options{})
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	caller := domain.Caller{Surface: "mcp", Instance: "session-1"}
	preview, err := store.Issue(testOperation(t), caller)
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}

	var successes atomic.Int32
	var wait sync.WaitGroup
	for range 32 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if _, consumeErr := store.Consume(preview.Token, caller); consumeErr == nil {
				successes.Add(1)
			}
		}()
	}
	wait.Wait()
	if got := successes.Load(); got != 1 {
		t.Fatalf("successful consumers = %d, want 1", got)
	}
}
