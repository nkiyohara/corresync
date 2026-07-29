// Package discovery finds provider candidates without authenticating.
package discovery

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/domain"
)

const (
	defaultTimeout = 8 * time.Second
	maxRedirects   = 4
)

// Resolver is the credential-free DNS surface used during discovery.
type Resolver interface {
	LookupMX(context.Context, string) ([]*net.MX, error)
	LookupSRV(context.Context, string, string, string) (string, []*net.SRV, error)
}

// ProbeResult is one HTTPS well-known observation.
type ProbeResult struct {
	Status   string
	Endpoint string
	Detail   string
}

// Prober observes one HTTPS endpoint without credentials or response content.
type Prober interface {
	Probe(context.Context, string) (ProbeResult, error)
}

// Options allow deterministic tests without live DNS or HTTP.
type Options struct {
	Resolver Resolver
	Prober   Prober
}

// Discoverer implements application.AccountDiscoverer.
type Discoverer struct {
	resolver Resolver
	prober   Prober
}

// New returns a credential-free provider discoverer.
func New(options Options) *Discoverer {
	resolver := options.Resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	prober := options.Prober
	if prober == nil {
		prober = newHTTPSProber()
	}
	return &Discoverer{resolver: resolver, prober: prober}
}

// Discover collects explainable hints. Individual DNS or well-known failures
// become diagnostics and do not turn a manual configuration into an error.
func (discoverer *Discoverer) Discover(
	ctx context.Context,
	address string,
) (application.AccountDiscoveryObservation, error) {
	at := strings.LastIndexByte(address, '@')
	if at < 0 || at == len(address)-1 {
		return application.AccountDiscoveryObservation{}, errors.New("normalized address is invalid")
	}
	domainName := strings.ToLower(address[at+1:])
	collector := newCandidateCollector()
	diagnostics := make([]application.DiscoveryDiagnostic, 0, 12)

	addKnownDomainCandidates(collector, domainName)

	mxRecords, err := discoverer.resolver.LookupMX(ctx, domainName)
	switch {
	case err == nil:
		if len(mxRecords) == 0 {
			diagnostics = append(diagnostics, diagnostic("mx", "not_found", domainName))
		} else {
			diagnostics = append(diagnostics, diagnostic("mx", "observed", domainName))
			addMXCandidates(collector, mxRecords)
		}
	case ctx.Err() != nil:
		return application.AccountDiscoveryObservation{}, ctx.Err()
	default:
		diagnostics = append(diagnostics, diagnostic("mx", "unavailable", classifyLookupError(err)))
	}

	for _, query := range []struct {
		service  string
		kind     string
		provider domain.ProviderID
		auth     application.DiscoveryAuthentication
	}{
		{"imaps", "imap", domain.ProviderIMAPSMTP, application.DiscoveryExternalCredential},
		{"submission", "smtp", domain.ProviderIMAPSMTP, application.DiscoveryExternalCredential},
		{"caldavs", "caldav", domain.ProviderCalDAV, application.DiscoveryExternalCredential},
		{"jmap", "jmap", domain.ProviderJMAP, application.DiscoveryExternalCredential},
	} {
		_, records, lookupErr := discoverer.resolver.LookupSRV(
			ctx,
			query.service,
			"tcp",
			domainName,
		)
		source := "srv_" + query.service
		switch {
		case lookupErr == nil:
			if len(records) == 0 {
				diagnostics = append(diagnostics, diagnostic(source, "not_found", domainName))
				continue
			}
			diagnostics = append(diagnostics, diagnostic(source, "observed", domainName))
			slices.SortFunc(records, compareSRV)
			for _, record := range records {
				target := strings.TrimSuffix(strings.ToLower(record.Target), ".")
				if target == "" || record.Port == 0 {
					continue
				}
				collector.add(candidateInput{
					provider: query.provider, confidence: 80, authentication: query.auth,
					explicit: true,
					endpoint: application.DiscoveredEndpoint{
						Kind:  query.kind,
						Value: net.JoinHostPort(target, strconv.Itoa(int(record.Port))),
					},
					evidence: application.DiscoveryEvidence{
						Source: source,
						Detail: domainName,
					},
				})
			}
		case ctx.Err() != nil:
			return application.AccountDiscoveryObservation{}, ctx.Err()
		default:
			diagnostics = append(
				diagnostics,
				diagnostic(source, "unavailable", classifyLookupError(lookupErr)),
			)
		}
	}

	for _, wellKnown := range []struct {
		path     string
		source   string
		kind     string
		provider domain.ProviderID
	}{
		{"/.well-known/jmap", "well_known_jmap", "jmap", domain.ProviderJMAP},
		{"/.well-known/caldav", "well_known_caldav", "caldav", domain.ProviderCalDAV},
	} {
		endpoint := "https://" + domainName + wellKnown.path
		result, probeErr := discoverer.prober.Probe(ctx, endpoint)
		if probeErr != nil {
			if ctx.Err() != nil {
				return application.AccountDiscoveryObservation{}, ctx.Err()
			}
			diagnostics = append(
				diagnostics,
				diagnostic(wellKnown.source, "unavailable", classifyProbeError(probeErr)),
			)
			continue
		}
		diagnostics = append(
			diagnostics,
			diagnostic(wellKnown.source, result.Status, result.Detail),
		)
		if result.Status == "observed" {
			collector.add(candidateInput{
				provider: wellKnown.provider, confidence: 85,
				authentication: application.DiscoveryExternalCredential,
				explicit:       true,
				endpoint: application.DiscoveredEndpoint{
					Kind: wellKnown.kind, Value: result.Endpoint,
				},
				evidence: application.DiscoveryEvidence{
					Source: wellKnown.source, Detail: domainName,
				},
			})
		}
	}

	return application.AccountDiscoveryObservation{
		Candidates:  collector.candidates(),
		Diagnostics: diagnostics,
	}, nil
}

type candidateInput struct {
	provider       domain.ProviderID
	confidence     int
	authentication application.DiscoveryAuthentication
	explicit       bool
	endpoint       application.DiscoveredEndpoint
	evidence       application.DiscoveryEvidence
}

type candidateCollector struct {
	values map[domain.ProviderID]*application.ProviderCandidate
}

func newCandidateCollector() *candidateCollector {
	return &candidateCollector{values: make(map[domain.ProviderID]*application.ProviderCandidate)}
}

func (collector *candidateCollector) add(input candidateInput) {
	candidate := collector.values[input.provider]
	if candidate == nil {
		candidate = &application.ProviderCandidate{
			Provider: input.provider, Authentication: input.authentication,
			Endpoints: make([]application.DiscoveredEndpoint, 0, 4),
			Evidence:  make([]application.DiscoveryEvidence, 0, 4),
		}
		collector.values[input.provider] = candidate
	}
	if input.confidence > candidate.Confidence {
		candidate.Confidence = input.confidence
	}
	candidate.RequiresExplicitSelection = candidate.RequiresExplicitSelection || input.explicit
	if input.endpoint.Kind != "" && !slices.Contains(candidate.Endpoints, input.endpoint) {
		candidate.Endpoints = append(candidate.Endpoints, input.endpoint)
	}
	if !slices.Contains(candidate.Evidence, input.evidence) {
		candidate.Evidence = append(candidate.Evidence, input.evidence)
	}
}

func (collector *candidateCollector) candidates() []application.ProviderCandidate {
	result := make([]application.ProviderCandidate, 0, len(collector.values))
	for _, candidate := range collector.values {
		result = append(result, *candidate)
	}
	return result
}

func addKnownDomainCandidates(collector *candidateCollector, domainName string) {
	switch domainName {
	case "outlook.com", "hotmail.com", "live.com", "msn.com":
		collector.add(candidateInput{
			provider: domain.ProviderMicrosoftOWA, confidence: 98,
			authentication: application.DiscoveryBrowserFirstParty,
			endpoint: application.DiscoveredEndpoint{
				Kind: "origin", Value: "https://outlook.live.com",
			},
			evidence: application.DiscoveryEvidence{
				Source: "known_domain", Detail: domainName,
			},
		})
		collector.add(candidateInput{
			provider: domain.ProviderMicrosoftGraph, confidence: 92,
			authentication: application.DiscoveryExplicitOAuth,
			explicit:       true,
			endpoint: application.DiscoveredEndpoint{
				Kind: "api", Value: "https://graph.microsoft.com/v1.0",
			},
			evidence: application.DiscoveryEvidence{
				Source: "known_domain", Detail: domainName,
			},
		})
	case "gmail.com", "googlemail.com":
		collector.add(candidateInput{
			provider: domain.ProviderGoogleAPI, confidence: 98,
			authentication: application.DiscoveryExplicitOAuth, explicit: true,
			evidence: application.DiscoveryEvidence{
				Source: "known_domain", Detail: domainName,
			},
		})
	}
}

func addMXCandidates(collector *candidateCollector, records []*net.MX) {
	for _, record := range records {
		host := strings.TrimSuffix(strings.ToLower(record.Host), ".")
		switch {
		case strings.HasSuffix(host, ".mail.protection.outlook.com"):
			collector.add(candidateInput{
				provider: domain.ProviderMicrosoftOWA, confidence: 55,
				authentication: application.DiscoveryBrowserFirstParty,
				endpoint: application.DiscoveredEndpoint{
					Kind: "origin", Value: "https://outlook.office.com",
				},
				evidence: application.DiscoveryEvidence{
					Source: "mx", Detail: "mail.protection.outlook.com",
				},
			})
			collector.add(candidateInput{
				provider: domain.ProviderMicrosoftGraph, confidence: 50,
				authentication: application.DiscoveryExplicitOAuth,
				explicit:       true,
				endpoint: application.DiscoveredEndpoint{
					Kind: "api", Value: "https://graph.microsoft.com/v1.0",
				},
				evidence: application.DiscoveryEvidence{
					Source: "mx", Detail: "mail.protection.outlook.com",
				},
			})
		case host == "aspmx.l.google.com" || strings.HasSuffix(host, ".google.com"):
			collector.add(candidateInput{
				provider: domain.ProviderGoogleAPI, confidence: 55,
				authentication: application.DiscoveryExplicitOAuth, explicit: true,
				evidence: application.DiscoveryEvidence{
					Source: "mx", Detail: "google-hosted mail exchange",
				},
			})
		}
	}
}

func diagnostic(source, status, detail string) application.DiscoveryDiagnostic {
	return application.DiscoveryDiagnostic{Source: source, Status: status, Detail: detail}
}

func compareSRV(left, right *net.SRV) int {
	if left.Priority != right.Priority {
		return int(left.Priority) - int(right.Priority)
	}
	if left.Weight != right.Weight {
		return int(right.Weight) - int(left.Weight)
	}
	if compared := strings.Compare(left.Target, right.Target); compared != 0 {
		return compared
	}
	return int(left.Port) - int(right.Port)
}

func classifyLookupError(err error) string {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		if dnsErr.IsNotFound {
			return "record not found"
		}
		if dnsErr.IsTimeout {
			return "DNS lookup timed out"
		}
	}
	return "DNS lookup unavailable"
}

func classifyProbeError(err error) string {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return "HTTPS endpoint unavailable"
	}
	return "HTTPS probe unavailable"
}

type httpsProber struct {
	client *http.Client
}

func newHTTPSProber() *httpsProber {
	client := &http.Client{
		Timeout: defaultTimeout,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return errors.New("too many redirects")
			}
			if request.URL.Scheme != "https" || request.URL.User != nil {
				return errors.New("well-known redirect must remain credential-free HTTPS")
			}
			return nil
		},
	}
	return &httpsProber{client: client}
}

func (prober *httpsProber) Probe(ctx context.Context, endpoint string) (ProbeResult, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" ||
		parsed.User != nil {
		return ProbeResult{}, errors.New("well-known endpoint must use credential-free HTTPS")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return ProbeResult{}, err
	}
	request.Header.Set("Accept", "application/json, */*;q=0.1")
	request.Header.Set("User-Agent", "corresync-discovery/1")
	response, err := prober.client.Do(request)
	if err != nil {
		return ProbeResult{}, err
	}
	defer func() { _ = response.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1))

	result := ProbeResult{
		Status: "observed", Endpoint: response.Request.URL.String(),
		Detail: "HTTPS endpoint responded",
	}
	switch response.StatusCode {
	case http.StatusNotFound, http.StatusGone:
		result.Status = "not_found"
		result.Detail = "well-known endpoint not found"
	default:
		if response.StatusCode >= 500 {
			result.Status = "unavailable"
			result.Detail = "well-known endpoint returned a server error"
		}
	}
	if result.Status == "observed" && response.Request.URL.Scheme != "https" {
		return ProbeResult{}, fmt.Errorf("well-known endpoint downgraded to %q", response.Request.URL.Scheme)
	}
	return result, nil
}
