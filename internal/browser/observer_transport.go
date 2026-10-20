package browser

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/remote"
)

const maximumObserverEventBytes = 1 << 20

// networkObservation consumes only the owned tab's four network events in wire
// order. It uses no event subscription or asynchronous queue. This preserves
// the previous synchronous observer while avoiding chromedp's unbounded public
// subscription queues. The browser-owned-only routes never install this hook.
type networkObservation struct {
	mu        sync.RWMutex
	sessionID target.SessionID
	observer  *requestObserver
}

func (observation *networkObservation) bind(sessionID target.SessionID) {
	observation.mu.Lock()
	defer observation.mu.Unlock()
	observation.sessionID = sessionID
}

func (observation *networkObservation) dial(ctx context.Context, address string) (chromedp.Transport, error) {
	endpoint, err := url.Parse(address)
	if err != nil || endpoint.Scheme != "ws" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || !strings.HasPrefix(endpoint.Path, "/devtools/browser/") {
		return nil, errors.New("invalid local browser debugging endpoint")
	}
	host, err := netip.ParseAddr(endpoint.Hostname())
	if err != nil || !host.IsLoopback() || endpoint.Port() == "" {
		return nil, errors.New("browser debugging endpoint is not loopback")
	}
	dialContext, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	connection, err := remote.DialContext(dialContext, address)
	if err != nil {
		return nil, errors.New("connect to local browser debugging endpoint")
	}
	return &observedTransport{Transport: connection, observation: observation}, nil
}

type observedTransport struct {
	chromedp.Transport
	observation *networkObservation
}

func (transport *observedTransport) Read(ctx context.Context, message *cdproto.Message) error {
	if err := transport.Transport.Read(ctx, message); err != nil {
		return err
	}
	return transport.observation.observe(message)
}

func (observation *networkObservation) observe(message *cdproto.Message) error {
	observation.mu.RLock()
	sessionID := observation.sessionID
	observation.mu.RUnlock()
	if sessionID == "" || message.SessionID != sessionID || message.ID != 0 {
		return nil
	}
	var event any
	switch string(message.Method) {
	case "Network.requestWillBeSent":
		event = new(network.EventRequestWillBeSent)
	case "Network.requestWillBeSentExtraInfo":
		event = new(network.EventRequestWillBeSentExtraInfo)
	case "Network.loadingFinished":
		event = new(network.EventLoadingFinished)
	case "Network.loadingFailed":
		event = new(network.EventLoadingFailed)
	default:
		return nil
	}
	if len(message.Params) > maximumObserverEventBytes || json.Unmarshal(message.Params, event) != nil {
		return errors.New("invalid browser network observation")
	}
	if request, ok := event.(*network.EventRequestWillBeSent); ok && request.Request == nil {
		return errors.New("browser network observation has no request")
	}
	observation.observer.Handle(event)
	return nil
}
