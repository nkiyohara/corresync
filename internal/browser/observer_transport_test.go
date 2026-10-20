package browser

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/chromedp/cdproto"
	"github.com/chromedp/cdproto/network"

	"github.com/nkiyohara/corresync/internal/session"
)

func TestNetworkObservationBindsOwnedTargetAndPreservesEventOrder(t *testing.T) {
	t.Parallel()
	manager, err := session.NewManager("https://outlook.cloud.microsoft")
	if err != nil {
		t.Fatal(err)
	}
	observer := newRequestObserver(manager, nil)
	observation := &networkObservation{observer: observer}
	observation.bind("owned-session")
	request := network.EventRequestWillBeSent{
		RequestID: "request-1", Request: &network.Request{
			URL:     "https://outlook.cloud.microsoft/owa/service.svc",
			Headers: network.Headers{"Authorization": "Bearer " + observerSyntheticBearer},
		},
	}
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	message := &cdproto.Message{SessionID: "other-session", Method: "Network.requestWillBeSent", Params: raw}
	if err := observation.observe(message); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Current(); !errors.Is(err, session.ErrNotReady) {
		t.Fatal("foreign tab established a session")
	}
	message.SessionID = "owned-session"
	if err := observation.observe(message); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Current(); err != nil {
		t.Fatal(err)
	}
	done := &cdproto.Message{SessionID: "owned-session", Method: "Network.loadingFinished", Params: []byte(`{"requestId":"request-1"}`)}
	if err := observation.observe(done); err != nil {
		t.Fatal(err)
	}
	if len(observer.early) != 0 || len(observer.eligible) != 0 {
		t.Fatal("completed observation retained state")
	}
}

func TestNetworkObservationRejectsMalformedAndOversizedEvents(t *testing.T) {
	t.Parallel()
	observation := &networkObservation{}
	observation.bind("owned-session")
	for _, raw := range []string{"{", `{}`, `{"request":null}`, strings.Repeat(" ", maximumObserverEventBytes+1)} {
		err := observation.observe(&cdproto.Message{SessionID: "owned-session", Method: "Network.requestWillBeSent", Params: []byte(raw)})
		if err == nil || strings.Contains(err.Error(), raw) && len(raw) > 16 {
			t.Fatal("invalid observation accepted or exposed")
		}
	}
}

func TestNetworkObservationRejectsNonLocalDebuggingEndpoints(t *testing.T) {
	t.Parallel()
	observation := &networkObservation{}
	for _, address := range []string{
		"ws://example.invalid:9222/devtools/browser/fixture",
		"ws://localhost:9222/devtools/browser/fixture",
		"wss://127.0.0.1:9222/devtools/browser/fixture",
		"ws://127.0.0.1:9222/other",
		"ws://user:password@127.0.0.1:9222/devtools/browser/fixture",
		"ws://127.0.0.1:9222/devtools/browser/fixture?secret=fixture",
	} {
		if _, err := observation.dial(t.Context(), address); err == nil {
			t.Fatal("invalid endpoint accepted")
		}
	}
}

func TestRequestObserverBoundsPendingRequests(t *testing.T) {
	t.Parallel()
	manager, err := session.NewManager("https://outlook.cloud.microsoft")
	if err != nil {
		t.Fatal(err)
	}
	observer := newRequestObserver(manager, nil)
	for index := 0; index < maximumPendingExtraHeaders*2; index++ {
		observer.Handle(&network.EventRequestWillBeSent{
			RequestID: network.RequestID(fmt.Sprintf("request-%d", index)),
			Request:   &network.Request{URL: "https://outlook.cloud.microsoft/owa/service.svc"},
		})
	}
	if len(observer.eligible) > maximumPendingExtraHeaders {
		t.Fatal("pending requests exceeded bound")
	}
}
