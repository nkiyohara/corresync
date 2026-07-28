package application

import (
	"context"
	"errors"
	"testing"

	"github.com/nkiyohara/corresync/internal/domain"
)

type discoveryStub struct {
	candidates []ProviderCandidate
	err        error
	address    string
}

func (stub *discoveryStub) Discover(
	_ context.Context,
	address string,
) (AccountDiscoveryObservation, error) {
	stub.address = address
	return AccountDiscoveryObservation{Candidates: stub.candidates}, stub.err
}

func TestAccountDiscoveryNormalizesSortsAndMarksAvailability(t *testing.T) {
	t.Parallel()
	stub := &discoveryStub{candidates: []ProviderCandidate{
		{
			Provider: domain.ProviderGoogleAPI, Confidence: 95,
			Authentication:            DiscoveryExplicitOAuth,
			RequiresExplicitSelection: true,
			Evidence:                  []DiscoveryEvidence{{Source: "known_domain", Detail: "gmail.com"}},
		},
		{
			Provider: domain.ProviderMicrosoftOWA, Confidence: 80,
			Authentication: DiscoveryBrowserFirstParty,
			Endpoints: []DiscoveredEndpoint{
				{Kind: "origin", Value: "https://outlook.office.com"},
			},
			Evidence: []DiscoveryEvidence{{Source: "mx", Detail: "mail.protection.outlook.com"}},
		},
	}}
	service, err := NewAccountDiscoveryService(
		stub,
		[]domain.ProviderID{domain.ProviderMicrosoftOWA},
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Discover(context.Background(), "reader@EXAMPLE.com")
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if stub.address != "reader@example.com" || result.Domain != "example.com" {
		t.Fatalf("normalized discovery = %#v, adapter address = %q", result, stub.address)
	}
	if len(result.Candidates) != 2 ||
		result.Candidates[0].Provider != domain.ProviderGoogleAPI ||
		result.Candidates[0].Available ||
		result.Candidates[1].Provider != domain.ProviderMicrosoftOWA ||
		!result.Candidates[1].Available {
		t.Fatalf("candidates = %#v", result.Candidates)
	}
}

func TestAccountDiscoveryRejectsMalformedOrAdapterData(t *testing.T) {
	t.Parallel()
	service, err := NewAccountDiscoveryService(&discoveryStub{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, address := range []string{"", " reader@example.com", "Reader <reader@example.com>", "reader"} {
		if _, err := service.Discover(context.Background(), address); err == nil {
			t.Fatalf("Discover(%q) unexpectedly succeeded", address)
		}
	}

	stub := &discoveryStub{candidates: []ProviderCandidate{{
		Provider: domain.ProviderGoogleAPI, Confidence: 101,
		Authentication: DiscoveryExplicitOAuth,
		Evidence:       []DiscoveryEvidence{{Source: "test", Detail: "synthetic"}},
	}}}
	service, err = NewAccountDiscoveryService(stub, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Discover(context.Background(), "reader@example.com"); err == nil {
		t.Fatal("Discover() accepted invalid adapter confidence")
	}

	stub = &discoveryStub{err: errors.New("synthetic failure")}
	service, err = NewAccountDiscoveryService(stub, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Discover(context.Background(), "reader@example.com"); err == nil {
		t.Fatal("Discover() ignored adapter error")
	}
}
