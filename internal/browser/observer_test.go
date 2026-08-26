package browser

import (
	"errors"
	"testing"

	"github.com/chromedp/cdproto/network"

	"github.com/nkiyohara/corresync/internal/session"
)

const observerSyntheticBearer = "observer-synthetic-token-0123456789abcdef"

func TestRequestObserverCapturesEitherEventOrder(t *testing.T) {
	t.Parallel()

	for _, extraFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "request-first", true: "extra-first"}[extraFirst], func(t *testing.T) {
			t.Parallel()
			manager, err := session.NewManager("https://outlook.cloud.microsoft")
			if err != nil {
				t.Fatalf("NewManager() error = %v", err)
			}
			observer := newRequestObserver(manager, nil)
			requestID := network.RequestID("request-1")
			requestEvent := &network.EventRequestWillBeSent{
				RequestID: requestID,
				Request: &network.Request{
					URL:     "https://outlook.cloud.microsoft/owa/service.svc",
					Headers: network.Headers{},
				},
			}
			extraEvent := &network.EventRequestWillBeSentExtraInfo{
				RequestID: requestID,
				Headers: network.Headers{
					"Authorization": "Bearer " + observerSyntheticBearer,
					"X-OWA-CANARY":  "synthetic-canary",
				},
			}
			if extraFirst {
				observer.Handle(extraEvent)
				observer.Handle(requestEvent)
			} else {
				observer.Handle(requestEvent)
				observer.Handle(extraEvent)
			}
			if _, err := manager.Current(); err != nil {
				t.Fatalf("Current() error = %v", err)
			}
		})
	}
}

func TestRequestObserverIgnoresOtherOriginsAndCleansState(t *testing.T) {
	t.Parallel()

	manager, err := session.NewManager("https://outlook.cloud.microsoft")
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	observer := newRequestObserver(manager, nil)
	requestID := network.RequestID("request-1")
	observer.Handle(&network.EventRequestWillBeSent{
		RequestID: requestID,
		Request: &network.Request{
			URL: "https://example.invalid/steal",
			Headers: network.Headers{
				"Authorization": "Bearer " + observerSyntheticBearer,
			},
		},
	})
	if _, err := manager.Current(); !errors.Is(err, session.ErrNotReady) {
		t.Fatalf("Current() error = %v, want ErrNotReady", err)
	}

	observer.Handle(&network.EventRequestWillBeSentExtraInfo{
		RequestID: requestID,
		Headers:   network.Headers{"Authorization": "Bearer " + observerSyntheticBearer},
	})
	observer.Handle(&network.EventLoadingFailed{RequestID: requestID})
	observer.mu.Lock()
	defer observer.mu.Unlock()
	if len(observer.early) != 0 || len(observer.eligible) != 0 {
		t.Fatalf("observer retained completed request state: early=%d eligible=%d", len(observer.early), len(observer.eligible))
	}
}

func TestRequestObserverReportsUnsupportedAuthorizationAndOriginMismatch(t *testing.T) {
	t.Parallel()

	manager, err := session.NewManager("https://outlook.cloud.microsoft")
	if err != nil {
		t.Fatal(err)
	}
	failures := make(chan error, 2)
	observer := newRequestObserver(manager, func(failure error) {
		failures <- failure
	})
	observer.Handle(&network.EventRequestWillBeSent{
		RequestID: "unsupported-authorization",
		Request: &network.Request{
			URL: "https://outlook.cloud.microsoft/owa/service.svc",
			Headers: network.Headers{
				"Authorization": "MSAuth1.0 synthetic-value-that-is-never-retained",
			},
		},
	})
	observer.Handle(&network.EventRequestWillBeSent{
		RequestID: "origin-mismatch",
		Request: &network.Request{
			URL: "https://outlook.example.invalid/owa/service.svc",
			Headers: network.Headers{
				"Action": "FindItem",
			},
		},
	})

	first := <-failures
	second := <-failures
	if !errors.Is(first, session.ErrAuthorizationScheme) {
		t.Fatalf("authorization failure = %v", first)
	}
	if second.Error() != "Outlook Web used service origin https://outlook.example.invalid instead of the configured exact origin https://outlook.cloud.microsoft" {
		t.Fatalf("origin failure = %v", second)
	}
	if _, err := manager.Current(); !errors.Is(err, session.ErrNotReady) {
		t.Fatalf("failed observations established a session: %v", err)
	}
}
