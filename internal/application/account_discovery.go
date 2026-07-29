package application

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"slices"
	"strings"

	"github.com/nkiyohara/corresync/internal/domain"
	"golang.org/x/net/idna"
)

const MaxDiscoveryCandidates = 32

// DiscoveryAuthentication describes the next consent boundary without
// initiating it. Discovery never authenticates.
type DiscoveryAuthentication string

const (
	DiscoveryBrowserFirstParty  DiscoveryAuthentication = "browser_first_party"
	DiscoveryExplicitOAuth      DiscoveryAuthentication = "explicit_oauth"
	DiscoveryExternalCredential DiscoveryAuthentication = "external_credential"
)

// DiscoveryEvidence is a content-free reason for one provider candidate.
type DiscoveryEvidence struct {
	Source string `json:"source"`
	Detail string `json:"detail"`
}

// DiscoveryDiagnostic records a bounded, content-free discovery outcome even
// when it produced no provider candidate.
type DiscoveryDiagnostic struct {
	Source string `json:"source"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

// DiscoveredEndpoint is an endpoint observed without credentials.
type DiscoveredEndpoint struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

// ProviderCandidate is an explainable, non-authoritative discovery result.
// Confidence orders evidence; it never grants permission or proves support.
type ProviderCandidate struct {
	Provider                  domain.ProviderID       `json:"provider"`
	Confidence                int                     `json:"confidence"`
	Authentication            DiscoveryAuthentication `json:"authentication"`
	RequiresExplicitSelection bool                    `json:"requiresExplicitSelection"`
	Available                 bool                    `json:"available"`
	Endpoints                 []DiscoveredEndpoint    `json:"endpoints"`
	Evidence                  []DiscoveryEvidence     `json:"evidence"`
}

// AccountDiscoveryResult contains no credential or mailbox content.
type AccountDiscoveryResult struct {
	Address     string                `json:"address"`
	Domain      string                `json:"domain"`
	Candidates  []ProviderCandidate   `json:"candidates"`
	Diagnostics []DiscoveryDiagnostic `json:"diagnostics"`
}

// AccountDiscoveryObservation is the raw, credential-free adapter result.
type AccountDiscoveryObservation struct {
	Candidates  []ProviderCandidate
	Diagnostics []DiscoveryDiagnostic
}

// AccountDiscoverer is implemented by credential-free outer adapters.
type AccountDiscoverer interface {
	Discover(context.Context, string) (AccountDiscoveryObservation, error)
}

// AccountDiscoveryService validates and normalizes provider evidence for both
// CLI and MCP transports.
type AccountDiscoveryService struct {
	discoverer AccountDiscoverer
	available  map[domain.ProviderID]struct{}
}

// NewAccountDiscoveryService creates the shared discovery use case.
func NewAccountDiscoveryService(
	discoverer AccountDiscoverer,
	available []domain.ProviderID,
) (*AccountDiscoveryService, error) {
	if discoverer == nil {
		return nil, errors.New("account discoverer is required")
	}
	availability := make(map[domain.ProviderID]struct{}, len(available))
	for _, provider := range available {
		if err := provider.Validate(); err != nil {
			return nil, err
		}
		availability[provider] = struct{}{}
	}
	return &AccountDiscoveryService{
		discoverer: discoverer,
		available:  availability,
	}, nil
}

// Discover gathers evidence without authenticating or changing configuration.
func (service *AccountDiscoveryService) Discover(
	ctx context.Context,
	address string,
) (AccountDiscoveryResult, error) {
	normalized, domainName, err := normalizeDiscoveryAddress(address)
	if err != nil {
		return AccountDiscoveryResult{}, err
	}
	observation, err := service.discoverer.Discover(ctx, normalized)
	if err != nil {
		return AccountDiscoveryResult{}, fmt.Errorf("discover account providers: %w", err)
	}
	candidates := observation.Candidates
	if len(candidates) > MaxDiscoveryCandidates {
		return AccountDiscoveryResult{}, fmt.Errorf(
			"provider discovery returned more than %d candidates",
			MaxDiscoveryCandidates,
		)
	}
	normalizedCandidates := make([]ProviderCandidate, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		if err := validateProviderCandidate(candidate); err != nil {
			return AccountDiscoveryResult{}, err
		}
		candidate.Available = false
		if _, available := service.available[candidate.Provider]; available {
			candidate.Available = true
		}
		slices.SortFunc(candidate.Endpoints, func(left, right DiscoveredEndpoint) int {
			if compared := strings.Compare(left.Kind, right.Kind); compared != 0 {
				return compared
			}
			return strings.Compare(left.Value, right.Value)
		})
		slices.SortFunc(candidate.Evidence, func(left, right DiscoveryEvidence) int {
			if compared := strings.Compare(left.Source, right.Source); compared != 0 {
				return compared
			}
			return strings.Compare(left.Detail, right.Detail)
		})
		key := candidateKey(candidate)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		normalizedCandidates = append(normalizedCandidates, candidate)
	}
	if len(observation.Diagnostics) > 32 {
		return AccountDiscoveryResult{}, errors.New("provider discovery diagnostics are unbounded")
	}
	diagnostics := slices.Clone(observation.Diagnostics)
	for _, diagnostic := range diagnostics {
		if err := validateDiscoveryText("diagnostic source", diagnostic.Source, 32); err != nil {
			return AccountDiscoveryResult{}, err
		}
		switch diagnostic.Status {
		case "observed", "not_found", "unavailable":
		default:
			return AccountDiscoveryResult{}, errors.New("discovery diagnostic status is invalid")
		}
		if err := validateDiscoveryText("diagnostic detail", diagnostic.Detail, 512); err != nil {
			return AccountDiscoveryResult{}, err
		}
	}
	slices.SortFunc(diagnostics, func(left, right DiscoveryDiagnostic) int {
		if compared := strings.Compare(left.Source, right.Source); compared != 0 {
			return compared
		}
		if compared := strings.Compare(left.Status, right.Status); compared != 0 {
			return compared
		}
		return strings.Compare(left.Detail, right.Detail)
	})
	slices.SortStableFunc(normalizedCandidates, func(left, right ProviderCandidate) int {
		if left.Confidence != right.Confidence {
			return right.Confidence - left.Confidence
		}
		if left.Available != right.Available {
			if left.Available {
				return -1
			}
			return 1
		}
		return strings.Compare(string(left.Provider), string(right.Provider))
	})
	return AccountDiscoveryResult{
		Address: normalized, Domain: domainName, Candidates: normalizedCandidates,
		Diagnostics: diagnostics,
	}, nil
}

func normalizeDiscoveryAddress(value string) (string, string, error) {
	if value == "" || strings.TrimSpace(value) != value || len(value) > 254 ||
		strings.ContainsAny(value, "\r\n\x00") {
		return "", "", errors.New("address must be one bare email address")
	}
	parsed, err := mail.ParseAddress(value)
	if err != nil || parsed.Name != "" || parsed.Address != value {
		return "", "", errors.New("address must be one bare email address")
	}
	at := strings.LastIndexByte(value, '@')
	if at < 1 || at == len(value)-1 {
		return "", "", errors.New("address must be one bare email address")
	}
	domainName, err := idna.Lookup.ToASCII(strings.ToLower(value[at+1:]))
	if err != nil || domainName == "" || len(domainName) > 253 {
		return "", "", errors.New("address has an invalid domain")
	}
	return value[:at+1] + domainName, domainName, nil
}

func validateProviderCandidate(candidate ProviderCandidate) error {
	if err := candidate.Provider.Validate(); err != nil {
		return err
	}
	if candidate.Confidence < 0 || candidate.Confidence > 100 {
		return errors.New("provider candidate confidence must be between 0 and 100")
	}
	switch candidate.Authentication {
	case DiscoveryBrowserFirstParty, DiscoveryExplicitOAuth, DiscoveryExternalCredential:
	default:
		return errors.New("provider candidate authentication is invalid")
	}
	if len(candidate.Endpoints) > 8 || len(candidate.Evidence) == 0 ||
		len(candidate.Evidence) > 16 {
		return errors.New("provider candidate evidence is missing or unbounded")
	}
	for _, endpoint := range candidate.Endpoints {
		if err := validateDiscoveryText("endpoint kind", endpoint.Kind, 32); err != nil {
			return err
		}
		if err := validateDiscoveryText("endpoint value", endpoint.Value, 2048); err != nil {
			return err
		}
	}
	for _, evidence := range candidate.Evidence {
		if err := validateDiscoveryText("evidence source", evidence.Source, 32); err != nil {
			return err
		}
		if err := validateDiscoveryText("evidence detail", evidence.Detail, 512); err != nil {
			return err
		}
	}
	return nil
}

func validateDiscoveryText(name, value string, maximum int) error {
	if value == "" || len(value) > maximum || strings.TrimSpace(value) != value ||
		strings.ContainsAny(value, "\r\n\x00") {
		return fmt.Errorf("%s is malformed", name)
	}
	return nil
}

func candidateKey(candidate ProviderCandidate) string {
	var key strings.Builder
	key.WriteString(string(candidate.Provider))
	key.WriteByte('|')
	for _, endpoint := range candidate.Endpoints {
		key.WriteString(endpoint.Kind)
		key.WriteByte('=')
		key.WriteString(endpoint.Value)
		key.WriteByte(';')
	}
	return key.String()
}
