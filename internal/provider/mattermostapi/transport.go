package mattermostapi

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const (
	mattermostAPIPrefix = "/api/v4/"
	defaultHTTPTimeout  = 30 * time.Second
)

// Authorizer applies one consented external credential without exposing its
// value to this adapter.
type Authorizer interface {
	Apply(*http.Request) error
}

type lookupNetIP func(context.Context, string, string) ([]netip.Addr, error)
type dialContext func(context.Context, string, string) (net.Conn, error)

type transportOptions struct {
	lookup lookupNetIP
	dial   dialContext
	tls    *tls.Config
}

type pinnedOrigin struct {
	origin  *url.URL
	address []netip.Addr
	next    atomic.Uint64
	dial    dialContext
}

func newMattermostHTTPClient(
	ctx context.Context,
	rawOrigin string,
	authorizer Authorizer,
) (*http.Client, *pinnedOrigin, error) {
	return newMattermostHTTPClientWith(ctx, rawOrigin, authorizer, transportOptions{})
}

func newMattermostHTTPClientWith(
	ctx context.Context,
	rawOrigin string,
	authorizer Authorizer,
	options transportOptions,
) (*http.Client, *pinnedOrigin, error) {
	origin, err := parseMattermostOrigin(rawOrigin)
	if err != nil {
		return nil, nil, err
	}
	if authorizer == nil {
		return nil, nil, errors.New("mattermost authorizer is required")
	}
	lookup := options.lookup
	if lookup == nil {
		lookup = net.DefaultResolver.LookupNetIP
	}
	addresses, err := lookup(ctx, "ip", origin.Hostname())
	if err != nil {
		return nil, nil, errors.New("resolve selected Mattermost origin")
	}
	addresses, err = publicAddresses(addresses)
	if err != nil {
		return nil, nil, err
	}
	dial := options.dial
	if dial == nil {
		networkDialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
		dial = networkDialer.DialContext
	}
	pinned := &pinnedOrigin{origin: origin, address: addresses, dial: dial}
	tlsConfig := &tls.Config{ // #nosec G402 -- certificate verification remains enabled.
		MinVersion: tls.VersionTLS12,
		ServerName: origin.Hostname(),
	}
	if options.tls != nil {
		tlsConfig = options.tls.Clone()
		tlsConfig.MinVersion = max(tlsConfig.MinVersion, tls.VersionTLS12)
		tlsConfig.ServerName = origin.Hostname()
	}
	base := &http.Transport{
		Proxy:                 nil,
		DialContext:           pinned.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          4,
		MaxIdleConnsPerHost:   2,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
		ExpectContinueTimeout: time.Second,
		DisableCompression:    true,
		TLSClientConfig:       tlsConfig,
	}
	guard := &mattermostRoundTripper{origin: origin, authorizer: authorizer, next: base}
	client := &http.Client{
		Transport: guard,
		Timeout:   defaultHTTPTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("mattermost redirects are not accepted")
		},
	}
	return client, pinned, nil
}

func parseMattermostOrigin(raw string) (*url.URL, error) {
	origin, err := url.Parse(raw)
	if err != nil || origin.Scheme != "https" || origin.Hostname() == "" ||
		origin.User != nil || origin.RawQuery != "" || origin.Fragment != "" ||
		origin.Path != "" && origin.Path != "/" {
		return nil, errors.New("mattermost origin must be one credential-free HTTPS origin")
	}
	if net.ParseIP(origin.Hostname()) != nil || !validDNSName(origin.Hostname()) {
		return nil, errors.New("mattermost origin must use an explicit DNS hostname")
	}
	port := origin.Port()
	if port != "" {
		parsed, parseErr := strconv.Atoi(port)
		if parseErr != nil || parsed < 1 || parsed > 65535 {
			return nil, errors.New("mattermost origin port is malformed")
		}
	}
	origin.Path = ""
	origin.Host = strings.ToLower(origin.Host)
	return origin, nil
}

func validDNSName(host string) bool {
	if host == "" || len(host) > 253 || strings.HasPrefix(host, ".") ||
		strings.HasSuffix(host, ".") || strings.Contains(host, "..") {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
				character >= '0' && character <= '9' || character == '-' {
				continue
			}
			return false
		}
	}
	return true
}

func publicAddresses(addresses []netip.Addr) ([]netip.Addr, error) {
	if len(addresses) == 0 || len(addresses) > 16 {
		return nil, errors.New("selected Mattermost origin has no bounded DNS answer")
	}
	result := make([]netip.Addr, 0, len(addresses))
	seen := make(map[netip.Addr]struct{}, len(addresses))
	for _, address := range addresses {
		address = address.Unmap()
		if !isPublicAddress(address) {
			return nil, errors.New("selected Mattermost origin resolves to a non-public address")
		}
		if _, exists := seen[address]; exists {
			continue
		}
		seen[address] = struct{}{}
		result = append(result, address)
	}
	if len(result) == 0 {
		return nil, errors.New("selected Mattermost origin has no usable DNS answer")
	}
	return result, nil
}

var specialAddressPrefixes = mustPrefixes(
	"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8",
	"169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24",
	"192.88.99.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24",
	"203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4",
	"::/96", "::1/128", "64:ff9b::/96", "64:ff9b:1::/48", "100::/64",
	"2001::/32", "2001:2::/48", "2001:10::/28", "2001:20::/28",
	"2001:db8::/32", "2002::/16", "3fff::/20", "5f00::/16",
	"fc00::/7", "fec0::/10", "fe80::/10", "ff00::/8",
)

func mustPrefixes(values ...string) []netip.Prefix {
	result := make([]netip.Prefix, 0, len(values))
	for _, value := range values {
		result = append(result, netip.MustParsePrefix(value))
	}
	return result
}

func isPublicAddress(address netip.Addr) bool {
	if !address.IsValid() || !address.IsGlobalUnicast() || address.IsPrivate() ||
		address.IsLoopback() || address.IsLinkLocalUnicast() || address.IsUnspecified() {
		return false
	}
	for _, prefix := range specialAddressPrefixes {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}

func (pinned *pinnedOrigin) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if pinned == nil || pinned.origin == nil || len(pinned.address) == 0 {
		return nil, errors.New("mattermost origin is not pinned")
	}
	expected := pinned.origin.Host
	if pinned.origin.Port() == "" {
		expected = net.JoinHostPort(pinned.origin.Hostname(), "443")
	}
	if address != expected || network != "tcp" {
		return nil, errors.New("mattermost connection escaped the pinned origin")
	}
	port := pinned.origin.Port()
	if port == "" {
		port = "443"
	}
	start := int((pinned.next.Add(1) - 1) % uint64(len(pinned.address))) // #nosec G115 -- modulo is bounded by at most 16 pinned addresses.
	var failures []error
	for offset := 0; offset < len(pinned.address); offset++ {
		target := net.JoinHostPort(pinned.address[(start+offset)%len(pinned.address)].String(), port)
		connection, err := pinned.dial(ctx, network, target)
		if err == nil {
			return connection, nil
		}
		failures = append(failures, err)
	}
	return nil, fmt.Errorf("connect selected Mattermost origin: %w", errors.Join(failures...))
}

type mattermostRoundTripper struct {
	origin     *url.URL
	authorizer Authorizer
	next       http.RoundTripper
}

func (transport *mattermostRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	if request == nil || request.URL == nil || transport.origin == nil ||
		request.URL.Scheme != transport.origin.Scheme ||
		!strings.EqualFold(request.URL.Host, transport.origin.Host) ||
		!strings.HasPrefix(request.URL.EscapedPath(), mattermostAPIPrefix) ||
		request.URL.User != nil || request.URL.Fragment != "" {
		return nil, errors.New("mattermost request escaped the selected API origin")
	}
	if request.Header.Get("Authorization") != "" {
		return nil, errors.New("mattermost request was preauthorized")
	}
	request.Header.Set("Accept-Encoding", "identity")
	if err := transport.authorizer.Apply(request); err != nil {
		return nil, errors.New("authorize Mattermost request")
	}
	authorization := request.Header.Get("Authorization")
	if authorization == "" || len(authorization) > 64<<10 ||
		strings.ContainsAny(authorization, "\r\n\x00") {
		request.Header.Del("Authorization")
		return nil, errors.New("mattermost authorization is malformed")
	}
	response, err := transport.next.RoundTrip(request)
	request.Header.Del("Authorization")
	if err != nil {
		return nil, err
	}
	encoding := strings.TrimSpace(response.Header.Get("Content-Encoding"))
	if encoding != "" && !strings.EqualFold(encoding, "identity") {
		_ = response.Body.Close()
		return nil, errors.New("mattermost compressed responses are not accepted")
	}
	return response, nil
}
