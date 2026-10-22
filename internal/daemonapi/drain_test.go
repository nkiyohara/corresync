package daemonapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nkiyohara/corresync/internal/domain"
)

type drainingBackend struct {
	*fakeBackend
	entered chan struct{}
	release chan struct{}
}

func (backend *drainingBackend) Login(context.Context, domain.AccountID, domain.Caller) (LoginResult, error) {
	close(backend.entered)
	<-backend.release
	return LoginResult{}, nil
}

func TestDrainPreservesInFlightOperationAndRejectsNewRequests(t *testing.T) {
	backend := &drainingBackend{fakeBackend: &fakeBackend{}, entered: make(chan struct{}), release: make(chan struct{})}
	token := syntheticCredential("d")
	server := newTestServer(t, backend, token)
	request := func() *http.Request {
		body := fmt.Sprintf(`{"version":%d,"id":"abcdefghijklmnop","method":"login","caller":{"surface":"cli","instance":"process-1"},"params":{"account":"%s"}}`, ProtocolVersion, testAccountID)
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "http://"+requestHost+requestPath, strings.NewReader(body))
		req.Header.Set("Authorization", authorizationType+token)
		req.Header.Set("Content-Type", contentType)
		return req
	}
	done := make(chan struct{})
	go func() { server.ServeHTTP(httptest.NewRecorder(), request()); close(done) }()
	select {
	case <-backend.entered:
	case <-time.After(time.Second):
		t.Fatal("operation did not start")
	}
	drained := make(chan struct{})
	go func() { server.Drain(); close(drained) }()
	deadline := time.After(time.Second)
	for {
		server.drainMu.Lock()
		draining := server.draining
		server.drainMu.Unlock()
		if draining {
			break
		}
		select {
		case <-deadline:
			t.Fatal("drain did not start")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	select {
	case <-drained:
		t.Fatal("drain interrupted an admitted operation")
	default:
	}
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request())
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("new request admitted during drain: %d", response.Code)
	}
	close(backend.release)
	select {
	case <-drained:
	case <-time.After(time.Second):
		t.Fatal("drain did not complete")
	}
	<-done
}
