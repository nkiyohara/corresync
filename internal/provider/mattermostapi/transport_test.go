package mattermostapi

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

type syntheticAuthorizer struct{}

func (syntheticAuthorizer) Apply(request *http.Request) error {
	request.Header.Set("Authorization", "Bearer synthetic-mattermost-token")
	return nil
}

func TestMattermostTransportPinsPublicDNSAndRejectsCompression(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Host != "chat.example.test" || request.Header.Get("Authorization") == "" ||
			request.Header.Get("Accept-Encoding") != "identity" {
			t.Errorf("request boundary = host %q, headers %v", request.Host, request.Header)
		}
		writer.Header().Set("Content-Encoding", "gzip")
		_, _ = writer.Write([]byte("not decompressed"))
	}))
	defer server.Close()
	serverAddress := server.Listener.Addr().String()
	client, _, err := newMattermostHTTPClientWith(
		t.Context(), "https://chat.example.test", syntheticAuthorizer{},
		transportOptions{
			lookup: func(context.Context, string, string) ([]netip.Addr, error) {
				return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
			},
			dial: func(ctx context.Context, network, address string) (net.Conn, error) {
				if address != "8.8.8.8:443" {
					t.Fatalf("dial target = %q", address)
				}
				return (&net.Dialer{}).DialContext(ctx, network, serverAddress)
			},
			tls: &tls.Config{InsecureSkipVerify: true}, // #nosec G402 -- synthetic TLS endpoint only.
		},
	)
	if err != nil {
		t.Fatalf("newMattermostHTTPClientWith() error = %v", err)
	}
	request, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://chat.example.test/api/v4/users/me", nil)
	response, err := client.Do(request)
	if err == nil {
		_ = response.Body.Close()
		t.Fatal("Get() accepted a compressed response")
	}
	if !strings.Contains(err.Error(), "compressed responses") {
		t.Fatalf("Get() error = %v", err)
	}
}

func TestMattermostTransportRejectsPrivateAndRebindingTargets(t *testing.T) {
	for _, address := range []string{
		"127.0.0.1", "10.0.0.1", "169.254.169.254", "192.168.1.1",
		"::1", "fc00::1", "fe80::1", "2001:db8::1", "203.0.113.10",
	} {
		_, _, err := newMattermostHTTPClientWith(
			t.Context(), "https://chat.example.test", syntheticAuthorizer{},
			transportOptions{lookup: func(context.Context, string, string) ([]netip.Addr, error) {
				return []netip.Addr{netip.MustParseAddr(address)}, nil
			}},
		)
		if err == nil {
			t.Fatalf("address %s was accepted", address)
		}
	}
	client, pinned, err := newMattermostHTTPClientWith(
		t.Context(), "https://chat.example.test", syntheticAuthorizer{},
		transportOptions{
			lookup: func(context.Context, string, string) ([]netip.Addr, error) {
				return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
			},
			dial: func(context.Context, string, string) (net.Conn, error) {
				return nil, errors.New("synthetic dial failure")
			},
		},
	)
	if err != nil || client == nil {
		t.Fatalf("newMattermostHTTPClientWith() = %v", err)
	}
	if _, err := pinned.DialContext(t.Context(), "tcp", "attacker.example:443"); err == nil {
		t.Fatal("pinned dial accepted a different authority")
	}
}

func TestMattermostOriginRejectsIPLiteralAndConfusion(t *testing.T) {
	for _, origin := range []string{
		"http://chat.example.test", "https://127.0.0.1", "https://[::1]",
		"https://user@chat.example.test", "https://chat.example.test/path",
		"https://chat.example.test?next=attacker", "https://chat..example.test",
	} {
		if _, err := parseMattermostOrigin(origin); err == nil {
			t.Fatalf("parseMattermostOrigin(%q) succeeded", origin)
		}
	}
}
