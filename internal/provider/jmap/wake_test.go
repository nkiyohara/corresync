package jmap

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/nkiyohara/corresync/internal/application"
)

// wakeTransport lets a test take the network away from an authenticated
// client and give it back, and observe mid-flight write requests.
type wakeTransport struct {
	base      http.RoundTripper
	offline   atomic.Bool
	attempts  atomic.Int32
	writes    atomic.Int32
	holdWrite atomic.Bool
	heldWrite chan struct{}
}

func (transport *wakeTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.attempts.Add(1)
	if transport.offline.Load() {
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ENETUNREACH}
	}
	if request.Body != nil {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		request.Body = io.NopCloser(bytes.NewReader(body))
		if bytes.Contains(body, []byte(`"Email/set"`)) {
			transport.writes.Add(1)
			if transport.holdWrite.Load() {
				// The request has left the process; no response arrives.
				close(transport.heldWrite)
				<-request.Context().Done()
				return nil, request.Context().Err()
			}
		}
	}
	return transport.base.RoundTrip(request)
}

func newWakeJMAPClient(t *testing.T) (*Client, *wakeTransport) {
	t.Helper()
	fixture := newSyntheticJMAP(t)
	transport := &wakeTransport{
		base:      fixture.server.Client().Transport,
		heldWrite: make(chan struct{}),
	}
	client, err := New(t.Context(), Options{
		SessionURL: fixture.server.URL + "/session",
		Username:   "reader@example.invalid",
		Password:   []byte("synthetic-secret"),
		Client:     &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client, transport
}

func wakeInbox() application.MailListInput {
	return application.MailListInput{
		Folder: application.MailFolder{
			Kind: application.MailFolderDistinguished, ID: "inbox",
		},
		Limit: 25,
	}
}

func TestJMAPNetworkLossIsNotSignOutAndTheSameClientRecovers(t *testing.T) {
	t.Parallel()
	client, transport := newWakeJMAPClient(t)

	transport.offline.Store(true)
	transport.attempts.Store(0)
	_, err := client.ListMessages(t.Context(), wakeInbox())
	if err == nil || transport.attempts.Load() != 1 {
		t.Fatalf("offline read = %v, attempts = %d", err, transport.attempts.Load())
	}
	if _, rejected := application.ProviderAuthenticationReason(err); rejected {
		t.Fatalf("network loss was classified as authentication: %v", err)
	}
	transport.attempts.Store(0)
	_, err = client.CreateMailDraft(t.Context(), application.MailDraftInput{
		To: []string{"recipient@example.invalid"}, Subject: "Offline draft",
	})
	if err == nil || transport.attempts.Load() != 1 {
		t.Fatalf("offline write = %v, attempts = %d", err, transport.attempts.Load())
	}
	if _, rejected := application.ProviderAuthenticationReason(err); rejected {
		t.Fatalf("offline write was classified as authentication: %v", err)
	}

	transport.offline.Store(false)
	if _, err := client.ListMessages(t.Context(), wakeInbox()); err != nil {
		t.Fatalf("read after reconnect = %v", err)
	}
}

func TestJMAPWriteInterruptedMidFlightIsUnknownAndNotReplayed(t *testing.T) {
	t.Parallel()
	client, transport := newWakeJMAPClient(t)
	transport.holdWrite.Store(true)
	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		<-transport.heldWrite
		cancel()
	}()
	_, err := client.CreateMailDraft(ctx, application.MailDraftInput{
		To: []string{"recipient@example.invalid"}, Subject: "Interrupted draft",
	})
	if !errors.Is(err, application.ErrWriteOutcomeUnknown) || transport.writes.Load() != 1 {
		t.Fatalf("interrupted write = %v, writes = %d", err, transport.writes.Load())
	}
	if _, rejected := application.ProviderAuthenticationReason(err); rejected {
		t.Fatalf("interrupted write was classified as authentication: %v", err)
	}
}
