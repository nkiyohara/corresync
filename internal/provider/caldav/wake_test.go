package caldav

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/nkiyohara/corresync/internal/application"
)

func TestCalDAVNetworkLossIsNotSignOutAndTheSameClientRecovers(t *testing.T) {
	t.Parallel()
	server, _, fixtureHTTP := newFixtureServer(t)
	var offline atomic.Bool
	var dials atomic.Int32
	transport := fixtureHTTP.Transport.(*http.Transport).Clone()
	// A pooled connection must not outlive the simulated network loss.
	transport.DisableKeepAlives = true
	address := server.Listener.Addr().String()
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		dials.Add(1)
		if offline.Load() {
			return nil, &net.OpError{Op: "dial", Net: network, Err: syscall.ENETUNREACH}
		}
		var dialer net.Dialer
		return dialer.DialContext(ctx, network, address)
	}
	client, err := New(t.Context(), Options{
		Endpoint: server.URL, Username: "reader@example.invalid",
		Password: []byte("synthetic-secret"),
		Client:   &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	window := application.CalendarListInput{
		Calendar: application.CalendarFolder{
			Kind: application.CalendarFolderDistinguished, ID: "calendar",
		},
		Start: "2026-07-28T00:00:00Z",
		End:   "2026-07-29T00:00:00Z",
	}

	offline.Store(true)
	dials.Store(0)
	_, err = client.ListCalendarEvents(t.Context(), window)
	if err == nil || dials.Load() != 1 {
		t.Fatalf("offline read = %v, dials = %d", err, dials.Load())
	}
	if _, rejected := application.ProviderAuthenticationReason(err); rejected {
		t.Fatalf("network loss was classified as authentication: %v", err)
	}
	dials.Store(0)
	_, err = client.conditionalRequest(
		t.Context(), http.MethodPut, fixtureCalendarPath, fixtureObjectPath,
		"If-Match", `"v1"`, nil,
	)
	if !errors.Is(err, application.ErrWriteOutcomeUnknown) || dials.Load() != 1 {
		t.Fatalf("offline write = %v, dials = %d", err, dials.Load())
	}

	offline.Store(false)
	page, err := client.ListCalendarEvents(t.Context(), window)
	if err != nil || len(page.Events) != 1 {
		t.Fatalf("read after reconnect = %+v, %v", page, err)
	}
}
