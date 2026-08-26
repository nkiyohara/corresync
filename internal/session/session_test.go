package session

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

const syntheticBearer = "synthetic-not-a-real-token-0123456789abcdef"

func TestManagerCapturesAndAppliesMinimumHeaders(t *testing.T) {
	t.Parallel()

	manager, err := NewManager("https://OUTLOOK.cloud.microsoft/")
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	manager.clock = func() time.Time {
		return time.Date(2026, 7, 17, 12, 0, 0, 0, time.FixedZone("test", 3600))
	}
	headers := http.Header{
		"Authorization":            {"Bearer " + syntheticBearer},
		"X-AnchorMailbox":          {"synthetic@example.invalid"},
		"X-OWA-ClientBuildVersion": {"20260711001.05"},
		"Cookie":                   {"must-not-be-copied"},
		"X-Unrelated":              {"must-not-be-copied"},
	}
	if !manager.Observe("https://outlook.cloud.microsoft/owa/service.svc", headers) {
		t.Fatal("Observe() did not capture valid request")
	}

	request, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		"https://outlook.cloud.microsoft/owa/service.svc",
		nil,
	)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	if err := manager.Apply(request); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if request.Header.Get("Authorization") != "Bearer "+syntheticBearer {
		t.Fatal("Apply() did not set captured bearer authorization")
	}
	if request.Header.Get("X-AnchorMailbox") == "" || request.Header.Get("X-Owa-Clientbuildversion") == "" {
		t.Fatalf("Apply() omitted routing headers: %v", request.Header)
	}
	if request.Header.Get("Cookie") != "" || request.Header.Get("X-Unrelated") != "" {
		t.Fatalf("Apply() copied unrelated headers: %v", request.Header)
	}

	credentials, err := manager.Current()
	if err != nil {
		t.Fatalf("Current() error = %v", err)
	}
	if got := credentials.CapturedAt(); !got.Equal(time.Date(2026, 7, 17, 11, 0, 0, 0, time.UTC)) {
		t.Fatalf("CapturedAt() = %v", got)
	}
	for _, rendered := range []string{fmt.Sprint(credentials), fmt.Sprintf("%#v", credentials)} {
		if strings.Contains(rendered, syntheticBearer) {
			t.Fatal("credential formatting exposed bearer authorization")
		}
	}
}

func TestManagerRejectsOriginConfusionAndMalformedAuthorization(t *testing.T) {
	t.Parallel()

	manager, err := NewManager("https://outlook.cloud.microsoft")
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	valid := http.Header{"Authorization": {"Bearer " + syntheticBearer}}
	for _, rawURL := range []string{
		"http://outlook.cloud.microsoft/owa/service.svc",
		"https://outlook.cloud.microsoft.example/owa/service.svc",
		"https://outlook.office.com/owa/service.svc",
		"https://user@outlook.cloud.microsoft/owa/service.svc",
	} {
		if manager.Observe(rawURL, valid) {
			t.Fatalf("Observe(%q) unexpectedly captured request", rawURL)
		}
	}
	for _, authorization := range []string{
		"",
		"Basic abcdefghijklmnopqrstuvwxyz123456",
		"Bearer short",
		"Bearer " + syntheticBearer + " extra",
		"Bearer " + syntheticBearer + "\nInjected: value",
	} {
		if manager.Observe(
			"https://outlook.cloud.microsoft/owa/service.svc",
			http.Header{"Authorization": {authorization}},
		) {
			t.Fatalf("Observe() accepted malformed authorization %q", authorization)
		}
	}
}

func TestManagerClassifiesUnsupportedAuthorizationWithoutRetainingIt(t *testing.T) {
	t.Parallel()

	manager, err := NewManager("https://outlook.cloud.microsoft")
	if err != nil {
		t.Fatal(err)
	}
	err = manager.ObserveAuthorization(
		"https://outlook.cloud.microsoft/owa/service.svc",
		http.Header{"Authorization": {"MSAuth1.0 synthetic-value-that-is-never-retained"}},
	)
	if !errors.Is(err, ErrAuthorizationScheme) {
		t.Fatalf("ObserveAuthorization() error = %v", err)
	}
	if strings.Contains(err.Error(), "synthetic-value") {
		t.Fatal("authorization classification exposed its value")
	}
	if _, err := manager.Current(); !errors.Is(err, ErrNotReady) {
		t.Fatalf("Current() error = %v, want ErrNotReady", err)
	}
}

func TestManagerWaitAndCurrentLifecycle(t *testing.T) {
	t.Parallel()

	manager, err := NewManager("https://outlook.cloud.microsoft")
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	if _, err := manager.Current(); !errors.Is(err, ErrNotReady) {
		t.Fatalf("Current() error = %v, want ErrNotReady", err)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := manager.Wait(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait() error = %v, want context.Canceled", err)
	}

	ready := make(chan Credentials, 1)
	go func() {
		credentials, waitErr := manager.Wait(context.Background())
		if waitErr == nil {
			ready <- credentials
		}
	}()
	manager.Observe(
		"https://outlook.cloud.microsoft/owa/service.svc",
		http.Header{"Authorization": {"Bearer " + syntheticBearer}},
	)
	select {
	case <-ready:
	case <-time.After(time.Second):
		t.Fatal("Wait() did not unblock after capture")
	}
}

func TestCredentialsRejectsMismatchedOrPreauthorizedRequest(t *testing.T) {
	t.Parallel()

	manager, err := NewManager("https://outlook.cloud.microsoft")
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	manager.Observe(
		"https://outlook.cloud.microsoft/owa/service.svc",
		http.Header{"Authorization": {"Bearer " + syntheticBearer}},
	)
	credentials, err := manager.Current()
	if err != nil {
		t.Fatalf("Current() error = %v", err)
	}

	other := &http.Request{URL: &url.URL{Scheme: "https", Host: "example.invalid"}, Header: make(http.Header)}
	if err := credentials.Apply(other); err == nil {
		t.Fatal("Apply() unexpectedly authorized another origin")
	}
	authorized, requestErr := http.NewRequestWithContext(
		context.Background(),
		http.MethodGet,
		"https://outlook.cloud.microsoft/owa/",
		nil,
	)
	if requestErr != nil {
		t.Fatalf("NewRequest() error = %v", requestErr)
	}
	authorized.Header.Set("Authorization", "Bearer existing")
	if err := credentials.Apply(authorized); err == nil {
		t.Fatal("Apply() unexpectedly replaced existing authorization")
	}
}

func TestNewManagerRejectsInvalidOrigins(t *testing.T) {
	t.Parallel()

	for _, origin := range []string{
		"",
		"http://outlook.example",
		"https://user@outlook.example",
		"https://outlook.example/owa",
		"https://outlook.example?query=true",
	} {
		if _, err := NewManager(origin); err == nil {
			t.Fatalf("NewManager(%q) unexpectedly succeeded", origin)
		}
	}
}
