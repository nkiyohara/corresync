package discovery

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/domain"
)

type resolverStub struct {
	mx     []*net.MX
	mxErr  error
	srv    map[string][]*net.SRV
	srvErr map[string]error
}

func (stub resolverStub) LookupMX(context.Context, string) ([]*net.MX, error) {
	return stub.mx, stub.mxErr
}

func (stub resolverStub) LookupSRV(
	_ context.Context,
	service string,
	_ string,
	_ string,
) (string, []*net.SRV, error) {
	return "", stub.srv[service], stub.srvErr[service]
}

type proberStub struct {
	results map[string]ProbeResult
	errors  map[string]error
}

func (stub proberStub) Probe(_ context.Context, endpoint string) (ProbeResult, error) {
	return stub.results[endpoint], stub.errors[endpoint]
}

func TestDiscoverCombinesCredentialFreeEvidence(t *testing.T) {
	t.Parallel()
	discoverer := New(Options{
		Resolver: resolverStub{
			mx: []*net.MX{{Host: "example.mail.protection.outlook.com.", Pref: 10}},
			srv: map[string][]*net.SRV{
				"imaps":      {{Target: "imap.example.test.", Port: 993}},
				"submission": {{Target: "smtp.example.test.", Port: 465}},
				"caldavs":    {{Target: "calendar.example.test.", Port: 443}},
			},
			srvErr: map[string]error{"jmap": &net.DNSError{IsNotFound: true}},
		},
		Prober: proberStub{
			results: map[string]ProbeResult{
				"https://example.test/.well-known/jmap": {
					Status: "not_found", Endpoint: "https://example.test/.well-known/jmap",
					Detail: "well-known endpoint not found",
				},
				"https://example.test/.well-known/caldav": {
					Status: "observed", Endpoint: "https://calendar.example.test/dav",
					Detail: "HTTPS endpoint responded",
				},
			},
		},
	})
	observation, err := discoverer.Discover(
		context.Background(),
		"reader@example.test",
	)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	providers := make(map[domain.ProviderID]application.ProviderCandidate)
	for _, candidate := range observation.Candidates {
		providers[candidate.Provider] = candidate
	}
	if len(providers) != 4 {
		t.Fatalf("providers = %#v", providers)
	}
	if providers[domain.ProviderMicrosoftOWA].Confidence != 55 {
		t.Fatalf("Outlook candidate = %#v", providers[domain.ProviderMicrosoftOWA])
	}
	graph := providers[domain.ProviderMicrosoftGraph]
	if graph.Confidence != 50 ||
		graph.Authentication != application.DiscoveryExplicitOAuth ||
		!graph.RequiresExplicitSelection ||
		len(graph.Endpoints) != 1 ||
		graph.Endpoints[0].Value != "https://graph.microsoft.com/v1.0" {
		t.Fatalf("Graph candidate = %#v", graph)
	}
	standards := providers[domain.ProviderIMAPSMTP]
	if len(standards.Endpoints) != 2 ||
		standards.Authentication != application.DiscoveryExternalCredential {
		t.Fatalf("standards candidate = %#v", standards)
	}
	caldav := providers[domain.ProviderCalDAV]
	if len(caldav.Endpoints) != 2 {
		t.Fatalf("CalDAV candidate did not merge SRV and well-known: %#v", caldav)
	}
	if caldav.Endpoints[0] != (application.DiscoveredEndpoint{
		Kind: "caldav", Value: "https://calendar.example.test:443/",
	}) {
		t.Fatalf("CalDAV SRV endpoint = %#v", caldav.Endpoints[0])
	}
	if len(observation.Diagnostics) == 0 {
		t.Fatal("discovery omitted diagnostics")
	}
}

func TestDiscoverAddsFirstClassICloudRoutesForDocumentedAddressFamilies(t *testing.T) {
	t.Parallel()
	for _, domainName := range []string{"icloud.com", "me.com", "mac.com"} {
		t.Run(domainName, func(t *testing.T) {
			t.Parallel()
			discoverer := New(Options{
				Resolver: resolverStub{
					mxErr: errors.New("offline"),
					srvErr: map[string]error{
						"imaps": errors.New("offline"), "submission": errors.New("offline"),
						"caldavs": errors.New("offline"), "jmap": errors.New("offline"),
					},
				},
				Prober: proberStub{errors: map[string]error{
					"https://" + domainName + "/.well-known/jmap":   errors.New("offline"),
					"https://" + domainName + "/.well-known/caldav": errors.New("offline"),
				}},
			})
			observation, err := discoverer.Discover(
				t.Context(),
				"reader@"+domainName,
			)
			if err != nil {
				t.Fatal(err)
			}
			providers := make(map[domain.ProviderID]application.ProviderCandidate)
			for _, candidate := range observation.Candidates {
				providers[candidate.Provider] = candidate
			}
			mail := providers[domain.ProviderIMAPSMTP]
			calendar := providers[domain.ProviderCalDAV]
			if mail.Confidence != 98 || mail.RequiresExplicitSelection ||
				len(mail.Endpoints) != 2 || calendar.Confidence != 98 ||
				calendar.RequiresExplicitSelection || len(calendar.Endpoints) != 1 {
				t.Fatalf("iCloud candidates = %#v", providers)
			}
			if mail.Endpoints[0] != (application.DiscoveredEndpoint{
				Kind: "imap", Value: "imap.mail.me.com:993",
			}) || mail.Endpoints[1] != (application.DiscoveredEndpoint{
				Kind: "smtp", Value: "smtp.mail.me.com:587",
			}) || calendar.Endpoints[0] != (application.DiscoveredEndpoint{
				Kind: "caldav", Value: "https://caldav.icloud.com:443/",
			}) {
				t.Fatalf("iCloud endpoints = %#v", providers)
			}
		})
	}
}

func TestDiscoverConstructsCredentialFreeCalDAVSRVURL(t *testing.T) {
	t.Parallel()
	discoverer := New(Options{
		Resolver: resolverStub{
			mxErr: errors.New("offline"),
			srv: map[string][]*net.SRV{
				"caldavs": {{Target: "CalDAV.iCloud.com.", Port: 443}},
			},
			srvErr: map[string]error{
				"imaps": errors.New("offline"), "submission": errors.New("offline"),
				"jmap": errors.New("offline"),
			},
		},
		Prober: proberStub{errors: map[string]error{
			"https://example.test/.well-known/jmap":   errors.New("offline"),
			"https://example.test/.well-known/caldav": errors.New("offline"),
		}},
	})
	observation, err := discoverer.Discover(t.Context(), "reader@example.test")
	if err != nil {
		t.Fatal(err)
	}
	if len(observation.Candidates) != 1 {
		t.Fatalf("candidates = %#v", observation.Candidates)
	}
	candidate := observation.Candidates[0]
	if candidate.Provider != domain.ProviderCalDAV ||
		candidate.Authentication != application.DiscoveryExternalCredential ||
		!candidate.RequiresExplicitSelection ||
		len(candidate.Endpoints) != 1 ||
		candidate.Endpoints[0] != (application.DiscoveredEndpoint{
			Kind: "caldav", Value: "https://caldav.icloud.com:443/",
		}) ||
		len(candidate.Evidence) != 1 ||
		candidate.Evidence[0].Source != "srv_caldavs" {
		t.Fatalf("CalDAV candidate = %#v", candidate)
	}
}

func TestDiscoverConstructsCredentialFreeJMAPSRVSessionURL(t *testing.T) {
	t.Parallel()
	discoverer := New(Options{
		Resolver: resolverStub{
			mxErr: errors.New("offline"),
			srv: map[string][]*net.SRV{
				"jmap": {{Target: "JMAP.example.test.", Port: 443}},
			},
			srvErr: map[string]error{
				"imaps": errors.New("offline"), "submission": errors.New("offline"),
				"caldavs": errors.New("offline"),
			},
		},
		Prober: proberStub{errors: map[string]error{
			"https://example.test/.well-known/jmap":   errors.New("offline"),
			"https://example.test/.well-known/caldav": errors.New("offline"),
		}},
	})
	observation, err := discoverer.Discover(t.Context(), "reader@example.test")
	if err != nil {
		t.Fatal(err)
	}
	if len(observation.Candidates) != 1 {
		t.Fatalf("candidates = %#v", observation.Candidates)
	}
	candidate := observation.Candidates[0]
	if candidate.Provider != domain.ProviderJMAP ||
		candidate.Authentication != application.DiscoveryExternalCredential ||
		!candidate.RequiresExplicitSelection ||
		len(candidate.Endpoints) != 1 ||
		candidate.Endpoints[0] != (application.DiscoveredEndpoint{
			Kind: "jmap", Value: "https://jmap.example.test:443/.well-known/jmap",
		}) ||
		len(candidate.Evidence) != 1 ||
		candidate.Evidence[0].Source != "srv_jmap" {
		t.Fatalf("JMAP candidate = %#v", candidate)
	}
}

func TestDiscoverRejectsInvalidSRVEndpoints(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		target string
		port   uint16
	}{
		{name: "service unavailable", target: ".", port: 443},
		{name: "empty target", port: 443},
		{name: "empty label", target: "caldav..example.test.", port: 443},
		{name: "leading hyphen", target: "-caldav.example.test.", port: 443},
		{name: "ambiguous delimiter", target: "caldav.example.test:443.", port: 443},
		{name: "IP literal", target: "127.0.0.1.", port: 443},
		{name: "zero port", target: "caldav.example.test.", port: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			discoverer := New(Options{
				Resolver: resolverStub{
					mxErr: errors.New("offline"),
					srv: map[string][]*net.SRV{
						"caldavs": {{Target: test.target, Port: test.port}},
					},
					srvErr: map[string]error{
						"imaps": errors.New("offline"), "submission": errors.New("offline"),
						"jmap": errors.New("offline"),
					},
				},
				Prober: proberStub{errors: map[string]error{
					"https://example.test/.well-known/jmap":   errors.New("offline"),
					"https://example.test/.well-known/caldav": errors.New("offline"),
				}},
			})
			observation, err := discoverer.Discover(t.Context(), "reader@example.test")
			if err != nil {
				t.Fatal(err)
			}
			if len(observation.Candidates) != 0 {
				t.Fatalf("invalid SRV target produced candidates = %#v", observation.Candidates)
			}
		})
	}
}

func TestDiscoverBoundsSRVEndpointsPerService(t *testing.T) {
	t.Parallel()
	discoverer := New(Options{
		Resolver: resolverStub{
			mxErr: errors.New("offline"),
			srv: map[string][]*net.SRV{
				"caldavs": {
					{Target: "a.example.test.", Port: 443},
					{Target: "b.example.test.", Port: 443},
					{Target: "c.example.test.", Port: 443},
					{Target: "d.example.test.", Port: 443},
					{Target: "e.example.test.", Port: 443},
				},
			},
			srvErr: map[string]error{
				"imaps": errors.New("offline"), "submission": errors.New("offline"),
				"jmap": errors.New("offline"),
			},
		},
		Prober: proberStub{errors: map[string]error{
			"https://example.test/.well-known/jmap":   errors.New("offline"),
			"https://example.test/.well-known/caldav": errors.New("offline"),
		}},
	})
	observation, err := discoverer.Discover(t.Context(), "reader@example.test")
	if err != nil {
		t.Fatal(err)
	}
	if len(observation.Candidates) != 1 ||
		len(observation.Candidates[0].Endpoints) != maxSRVRecordsPerService {
		t.Fatalf("bounded candidates = %#v", observation.Candidates)
	}
	if endpoint := observation.Candidates[0].Endpoints[3].Value; endpoint != "https://d.example.test:443/" {
		t.Fatalf("last deterministic endpoint = %q", endpoint)
	}
}

func TestDiscoverKnownGoogleDoesNotAuthenticate(t *testing.T) {
	t.Parallel()
	discoverer := New(Options{
		Resolver: resolverStub{
			mxErr: errors.New("offline"),
			srvErr: map[string]error{
				"imaps": errors.New("offline"), "submission": errors.New("offline"),
				"caldavs": errors.New("offline"), "jmap": errors.New("offline"),
			},
		},
		Prober: proberStub{errors: map[string]error{
			"https://gmail.com/.well-known/jmap":   errors.New("offline"),
			"https://gmail.com/.well-known/caldav": errors.New("offline"),
		}},
	})
	observation, err := discoverer.Discover(context.Background(), "reader@gmail.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(observation.Candidates) != 1 {
		t.Fatalf("candidates = %#v", observation.Candidates)
	}
	candidate := observation.Candidates[0]
	if candidate.Provider != domain.ProviderGoogle ||
		!candidate.RequiresExplicitSelection ||
		candidate.Authentication != application.DiscoveryExplicitOAuth ||
		len(candidate.Endpoints) != 1 ||
		candidate.Endpoints[0] != (application.DiscoveredEndpoint{
			Kind: "api", Value: "https://www.googleapis.com",
		}) {
		t.Fatalf("Google candidate could trigger implicit consent: %#v", candidate)
	}
}

func TestDiscoverKnownMicrosoftOffersGraphOnlyByExplicitSelection(t *testing.T) {
	t.Parallel()

	discoverer := New(Options{
		Resolver: resolverStub{
			mxErr: errors.New("offline"),
			srvErr: map[string]error{
				"imaps": errors.New("offline"), "submission": errors.New("offline"),
				"caldavs": errors.New("offline"), "jmap": errors.New("offline"),
			},
		},
		Prober: proberStub{errors: map[string]error{
			"https://outlook.com/.well-known/jmap":   errors.New("offline"),
			"https://outlook.com/.well-known/caldav": errors.New("offline"),
		}},
	})
	observation, err := discoverer.Discover(
		context.Background(),
		"reader@outlook.com",
	)
	if err != nil {
		t.Fatal(err)
	}
	providers := make(map[domain.ProviderID]application.ProviderCandidate)
	for _, candidate := range observation.Candidates {
		providers[candidate.Provider] = candidate
	}
	if len(providers) != 2 ||
		providers[domain.ProviderMicrosoftOWA].RequiresExplicitSelection ||
		!providers[domain.ProviderMicrosoftGraph].RequiresExplicitSelection ||
		providers[domain.ProviderMicrosoftGraph].Authentication !=
			application.DiscoveryExplicitOAuth {
		t.Fatalf("Microsoft candidates = %#v", providers)
	}
}

func TestDiscoverFailsOnCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	discoverer := New(Options{
		Resolver: resolverStub{mxErr: context.Canceled},
		Prober:   proberStub{},
	})
	if _, err := discoverer.Discover(ctx, "reader@example.test"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Discover() error = %v", err)
	}
}
