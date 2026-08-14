package restapi

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	maximumReadBackoff     = 2 * time.Second
	transientCircuitDelay  = 5 * time.Second
	maximumThrottleCircuit = 5 * time.Minute
	minimumThrottleCircuit = time.Second
)

// CircuitOpenError is content-free and scoped to one account-owned API
// client. It prevents a provider outage or throttle from becoming an
// unbounded request loop while leaving unrelated clients untouched.
type CircuitOpenError struct {
	RetryAfter time.Duration
	Throttled  bool
}

func (failure *CircuitOpenError) Error() string {
	kind := "transient provider failure"
	if failure.Throttled {
		kind = "provider throttle"
	}
	return fmt.Sprintf("API circuit is open after %s; retry after %s", kind, failure.RetryAfter)
}

func (failure *CircuitOpenError) RetryAfterDuration() time.Duration {
	return failure.RetryAfter
}

type readResilience struct {
	mu        sync.Mutex
	openUntil time.Time
	throttled bool
	now       func() time.Time
	sleep     func(context.Context, time.Duration) error
}

func newReadResilience() *readResilience {
	return &readResilience{now: time.Now, sleep: sleepContext}
}

func (resilience *readResilience) BeforeRead() error {
	resilience.mu.Lock()
	defer resilience.mu.Unlock()
	now := resilience.now()
	if !now.Before(resilience.openUntil) {
		resilience.openUntil = time.Time{}
		resilience.throttled = false
		return nil
	}
	return &CircuitOpenError{
		RetryAfter: resilience.openUntil.Sub(now),
		Throttled:  resilience.throttled,
	}
}

func (resilience *readResilience) Backoff(
	ctx context.Context,
	attempt int,
	retryAfter time.Duration,
) error {
	delay := time.Duration(attempt*attempt) * 100 * time.Millisecond
	if retryAfter > delay {
		delay = retryAfter
	}
	if delay > maximumReadBackoff {
		delay = maximumReadBackoff
	}
	return resilience.sleep(ctx, delay)
}

func (resilience *readResilience) Now() time.Time {
	resilience.mu.Lock()
	defer resilience.mu.Unlock()
	return resilience.now()
}

func (resilience *readResilience) OpenTransient() {
	resilience.open(transientCircuitDelay, false)
}

func (resilience *readResilience) OpenThrottle(delay time.Duration) {
	if delay < minimumThrottleCircuit {
		delay = minimumThrottleCircuit
	}
	if delay > maximumThrottleCircuit {
		delay = maximumThrottleCircuit
	}
	resilience.open(delay, true)
}

func (resilience *readResilience) open(delay time.Duration, throttled bool) {
	resilience.mu.Lock()
	defer resilience.mu.Unlock()
	until := resilience.now().Add(delay)
	if until.After(resilience.openUntil) {
		resilience.openUntil = until
		resilience.throttled = throttled
	}
}

func (resilience *readResilience) Succeed() {
	resilience.mu.Lock()
	resilience.openUntil = time.Time{}
	resilience.throttled = false
	resilience.mu.Unlock()
}

func retryAfterDuration(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	seconds, secondsErr := strconv.Atoi(value)
	var delay time.Duration
	if secondsErr == nil {
		if seconds < 1 {
			return 0
		}
		delay = time.Duration(seconds) * time.Second
	} else {
		deadline, err := http.ParseTime(value)
		if err != nil || !deadline.After(now) {
			return 0
		}
		delay = deadline.Sub(now)
	}
	if delay > maximumThrottleCircuit {
		return maximumThrottleCircuit
	}
	return delay
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
