package rollout

import (
	"errors"
	"slices"
	"testing"

	"github.com/nkiyohara/corresync/internal/domain"
)

func TestMessagingRoutesRemainReleaseClosed(t *testing.T) {
	t.Parallel()

	routes := []struct {
		provider domain.MessagingProviderID
		route    domain.MessagingRouteKind
	}{
		{domain.MessagingProviderMicrosoftTeams, domain.MessagingRouteTeamsGraph},
		{domain.MessagingProviderMicrosoftTeams, domain.MessagingRouteTeamsWeb},
		{domain.MessagingProviderSlack, domain.MessagingRouteSlackAPI},
		{domain.MessagingProviderMattermost, domain.MessagingRouteMattermost},
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
