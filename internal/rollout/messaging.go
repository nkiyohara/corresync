package rollout

import (
	"errors"
	"fmt"

	"github.com/nkiyohara/corresync/internal/domain"
)

// ErrMessagingPending is returned before credential access, browser launch,
// provider traffic, or capability probing while the release-owned manifest is
// incomplete. There is deliberately no environment, flag, config, or MCP
// override.
var ErrMessagingPending = errors.New("v0.9 messaging is not release-enabled")

// MessagingGateStatus is content-free release evidence for one exact route.
// Missing contains only fixed gate names, never paths, credentials, or account
// data.
type MessagingGateStatus struct {
	Provider domain.MessagingProviderID `json:"provider"`
	Route    domain.MessagingRouteKind  `json:"route"`
	Enabled  bool                       `json:"enabled"`
	Missing  []string                   `json:"missing,omitempty"`
}

type messagingEvidence struct {
	architecture     bool
	commonContracts  bool
	adapterContracts bool
	surfaceContracts bool
	documentation    bool
	liveObservation  bool
	securityReview   bool
	teamsParity      bool
}

// MessagingStatus returns the immutable source-owned gate for one route. Each
// literal is changed only by a reviewed release commit that carries the
// corresponding repository evidence.
func MessagingStatus(provider domain.MessagingProviderID, route domain.MessagingRouteKind) (MessagingGateStatus, error) {
	if err := validateMessagingRoute(provider, route); err != nil {
		return MessagingGateStatus{}, err
	}
	evidence := messagingEvidence{
		architecture: true, commonContracts: true,
	}
	switch route {
	case domain.MessagingRouteTeamsGraph, domain.MessagingRouteTeamsWeb:
		// Teams parity remains false until both routes prove one identical
		// release cohort independently.
	case domain.MessagingRouteSlackAPI, domain.MessagingRouteMattermost:
		evidence.teamsParity = true
	}
	missing := evidence.missing()
	return MessagingGateStatus{
		Provider: provider, Route: route, Enabled: len(missing) == 0, Missing: missing,
	}, nil
}

// RequireMessaging fails closed before any external effect while a route's
// complete release manifest is not satisfied.
func RequireMessaging(provider domain.MessagingProviderID, route domain.MessagingRouteKind) error {
	status, err := MessagingStatus(provider, route)
	if err != nil {
		return err
	}
	if status.Enabled {
		return nil
	}
	return fmt.Errorf("%w: %s is awaiting %v", ErrMessagingPending, route, status.Missing)
}

func validateMessagingRoute(provider domain.MessagingProviderID, route domain.MessagingRouteKind) error {
	if err := provider.Validate(); err != nil {
		return err
	}
	if err := route.Validate(); err != nil {
		return err
	}
	switch provider {
	case domain.MessagingProviderMicrosoftTeams:
		if route == domain.MessagingRouteTeamsGraph || route == domain.MessagingRouteTeamsWeb {
			return nil
		}
	case domain.MessagingProviderSlack:
		if route == domain.MessagingRouteSlackAPI {
			return nil
		}
	case domain.MessagingProviderMattermost:
		if route == domain.MessagingRouteMattermost {
			return nil
		}
	}
	return errors.New("messaging provider and route do not match")
}

func (evidence messagingEvidence) missing() []string {
	checks := []struct {
		name string
		ok   bool
	}{
		{"architecture", evidence.architecture},
		{"common_contracts", evidence.commonContracts},
		{"adapter_contracts", evidence.adapterContracts},
		{"surface_contracts", evidence.surfaceContracts},
		{"documentation", evidence.documentation},
		{"live_observation", evidence.liveObservation},
		{"security_review", evidence.securityReview},
		{"teams_parity", evidence.teamsParity},
	}
	missing := make([]string, 0, len(checks))
	for _, check := range checks {
		if !check.ok {
			missing = append(missing, check.name)
		}
	}
	return missing
}
