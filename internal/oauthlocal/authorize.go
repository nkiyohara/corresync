package oauthlocal

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"github.com/nkiyohara/corresync/internal/config"
)

type callbackResult struct {
	code string
	err  error
}

func (manager *Manager) authorize(
	ctx context.Context,
	route config.OAuthRoute,
	provider Provider,
) (storedGrant, error) {
	redirect, err := url.Parse(route.RedirectURI)
	if err != nil || redirect.Scheme != "http" ||
		redirect.Hostname() != "127.0.0.1" {
		return storedGrant{}, errors.New("OAuth redirect must use loopback HTTP")
	}
	callbackPath := redirect.EscapedPath()
	if callbackPath == "" {
		callbackPath = "/"
	}
	listener, err := (&net.ListenConfig{}).Listen(
		ctx,
		"tcp4",
		net.JoinHostPort("127.0.0.1", redirect.Port()),
	)
	if err != nil {
		return storedGrant{}, fmt.Errorf("listen for OAuth callback: %w", err)
	}
	defer func() { _ = listener.Close() }()
	actualAddress, ok := listener.Addr().(*net.TCPAddr)
	if !ok || actualAddress.Port < 1 {
		return storedGrant{}, errors.New("OAuth callback listener has no loopback port")
	}
	actualRedirect := *redirect
	actualRedirect.Host = net.JoinHostPort("127.0.0.1", strconv.Itoa(actualAddress.Port))
	flowRoute := route
	flowRoute.RedirectURI = actualRedirect.String()

	state, err := randomURLValue(32)
	if err != nil {
		return storedGrant{}, err
	}
	verifier := oauth2.GenerateVerifier()
	results := make(chan callbackResult, 1)
	server := &http.Server{
		ReadHeaderTimeout: 5 * time.Second,
		MaxHeaderBytes:    8 << 10,
		Handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if request.Method != http.MethodGet ||
				request.URL.EscapedPath() != callbackPath {
				http.NotFound(writer, request)
				return
			}
			query := request.URL.Query()
			if subtle.ConstantTimeCompare([]byte(query.Get("state")), []byte(state)) != 1 {
				http.Error(writer, "OAuth state did not match", http.StatusBadRequest)
				select {
				case results <- callbackResult{err: errors.New("OAuth callback state mismatch")}:
				default:
				}
				return
			}
			if providerError := query.Get("error"); providerError != "" {
				http.Error(writer, "Authorization was not completed", http.StatusBadRequest)
				select {
				case results <- callbackResult{err: errors.New("OAuth authorization was not completed")}:
				default:
				}
				return
			}
			code := query.Get("code")
			if code == "" || len(code) > 8192 || strings.ContainsAny(code, "\r\n\x00") {
				http.Error(writer, "Authorization code was missing", http.StatusBadRequest)
				select {
				case results <- callbackResult{err: errors.New("OAuth authorization code is malformed")}:
				default:
				}
				return
			}
			writer.Header().Set("Content-Type", "text/html; charset=utf-8")
			writer.Header().Set("Cache-Control", "no-store")
			_, _ = io.WriteString(
				writer,
				"<!doctype html><title>Corresync authorization complete</title>"+
					"<p>Authorization received. You can close this tab.</p>",
			)
			select {
			case results <- callbackResult{code: code}:
			default:
			}
		}),
	}
	serveErrors := make(chan error, 1)
	go func() {
		if serveErr := server.Serve(listener); serveErr != nil &&
			!errors.Is(serveErr, http.ErrServerClosed) {
			serveErrors <- serveErr
		}
	}()
	defer func() { _ = server.Close() }()

	oauthConfig := oauthConfig(flowRoute, provider)
	options := []oauth2.AuthCodeOption{oauth2.S256ChallengeOption(verifier)}
	for name, value := range provider.AuthParams {
		options = append(options, oauth2.SetAuthURLParam(name, value))
	}
	authorizationURL := oauthConfig.AuthCodeURL(state, options...)
	if manager.beforeOpen != nil {
		manager.beforeOpen(provider)
	}
	if err := manager.open(ctx, authorizationURL); err != nil {
		return storedGrant{}, fmt.Errorf("open OAuth authorization page: %w", err)
	}

	waitContext, cancel := context.WithTimeout(ctx, authorizationTimeout)
	defer cancel()
	var result callbackResult
	select {
	case <-waitContext.Done():
		return storedGrant{}, fmt.Errorf("wait for OAuth callback: %w", waitContext.Err())
	case err := <-serveErrors:
		return storedGrant{}, fmt.Errorf("serve OAuth callback: %w", err)
	case result = <-results:
	}
	if result.err != nil {
		return storedGrant{}, result.err
	}
	exchangeContext := context.WithValue(waitContext, oauth2.HTTPClient, manager.http)
	token, err := oauthConfig.Exchange(
		exchangeContext,
		result.code,
		oauth2.VerifierOption(verifier),
	)
	if err != nil {
		return storedGrant{}, fmt.Errorf("exchange OAuth authorization code: %w", err)
	}
	if token.AccessToken == "" {
		return storedGrant{}, errors.New("OAuth token endpoint returned no access token")
	}
	if err := manager.save(route, provider, *token); err != nil {
		return storedGrant{}, fmt.Errorf("store OAuth grant: %w", err)
	}
	return storedGrant{
		Version: 1, Provider: provider.ID, ClientID: route.ClientID,
		RedirectURI: route.RedirectURI,
		Scopes:      append([]string(nil), provider.Scopes...),
		Token:       *token,
	}, nil
}

func randomURLValue(size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func openBrowser(ctx context.Context, target string) error {
	var name string
	var arguments []string
	switch runtime.GOOS {
	case "darwin":
		name, arguments = "open", []string{target}
	case "windows":
		name, arguments = "rundll32", []string{"url.dll,FileProtocolHandler", target}
	default:
		name, arguments = "xdg-open", []string{target}
	}
	// #nosec G204 -- executable names are closed above and target is the
	// provider authorization URL built by net/url, never a shell string.
	command := exec.CommandContext(ctx, name, arguments...)
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	return command.Run()
}
