package rollout

import (
	"errors"
	"slices"
	"testing"

	"github.com/nkiyohara/corresync/internal/domain"
)

func TestSlackMessagingRemainsReleaseClosed(t *testing.T) {
	t.Parallel()

	routes := []struct {
		provider domain.MessagingProviderID
		route    domain.MessagingRouteKind
	}{
		{domain.MessagingProviderSlack, domain.MessagingRouteSlackAPI},
	}
	for _, item := range routes {
		status, err := MessagingStatus(item.provider, item.route)
		if err != nil {
			t.Fatalf("MessagingStatus(%q) error = %v", item.route, err)
		}
		if status.Enabled || len(status.Missing) == 0 ||
			!slices.Contains(status.Missing, "live_observation") ||
			!slices.Contains(status.Missing, "security_review") {
			t.Fatalf("route %q unexpectedly enabled: %+v", item.route, status)
		}
		if err := RequireMessaging(item.provider, item.route); !errors.Is(err, ErrMessagingPending) {
			t.Fatalf("RequireMessaging(%q) error = %v", item.route, err)
		}
	}
}

func TestMessagingGateRejectsCrossProviderRoutes(t *testing.T) {
	t.Parallel()

	if _, err := MessagingStatus(domain.MessagingProviderSlack, domain.MessagingRouteTeamsWeb); err == nil {
		t.Fatal("Slack was allowed to select the Teams Web route")
	}
	if _, err := MessagingStatus(domain.MessagingProviderMicrosoftTeams, domain.MessagingRouteMattermost); err == nil {
		t.Fatal("Teams was allowed to select the Mattermost route")
	}
}

func TestTeamsPreviewRetainsMissingStableEvidence(t *testing.T) {
	t.Parallel()
	for _, route := range []domain.MessagingRouteKind{domain.MessagingRouteTeamsGraph, domain.MessagingRouteTeamsWeb} {
		status, err := MessagingStatus(domain.MessagingProviderMicrosoftTeams, route)
		if err != nil || !status.Enabled || !status.Preview || !slices.Contains(status.Missing, "live_observation") {
			t.Fatalf("Teams preview status = %+v, %v", status, err)
		}
		if err := RequireMessaging(domain.MessagingProviderMicrosoftTeams, route); err != nil {
			t.Fatal(err)
		}
	}
	if !MessagingCatalogEnabled() {
		t.Fatal("Teams preview catalog is hidden")
	}
}

func TestMattermostPreviewRetainsMissingStableEvidence(t *testing.T) {
	t.Parallel()
	status, err := MessagingStatus(domain.MessagingProviderMattermost, domain.MessagingRouteMattermost)
	if err != nil || !status.Enabled || !status.Preview || !slices.Contains(status.Missing, "live_observation") {
		t.Fatalf("Mattermost preview = %+v, %v", status, err)
	}
}
