// Package browser launches isolated, visible Chromium sessions for explicitly
// configured provider-owned web applications.
package browser

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"

	"github.com/nkiyohara/corresync/internal/session"
)

// Options define the browser-owned authentication boundary.
type Options struct {
	Origin            string
	AdditionalOrigins []string
	StartURL          string
	ProfileDir        string
	Executable        string
	Headless          bool
	// BrowserOwnedOnly disables the Outlook network authorization observer.
	// Closed semantic DOM drivers use this mode so cookies, bearer tokens, and
	// browser storage remain exclusively inside the browser process.
	BrowserOwnedOnly bool
}

// Browser owns a Chromium process, target, observer, and session manager.
type Browser struct {
	context         context.Context
	cancelContext   context.CancelFunc
	cancelAllocator context.CancelFunc
	sessions        *session.Manager
	allowedOrigins  map[string]struct{}
	interactionMu   sync.Mutex
	closeOnce       sync.Once
	closeErr        error
}

// Launch opens a visible, isolated browser and navigates to Outlook Web. It
// returns before authentication completes; call WaitForSession afterward.
func Launch(parent context.Context, options Options) (*Browser, error) {
	if err := validateOptions(options); err != nil {
		return nil, err
	}
	if !options.Headless {
		if err := RequireGraphicalSession(); err != nil {
			return nil, err
		}
	}
	executable, err := ResolveExecutable(options.Executable)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(options.ProfileDir, 0o700); err != nil {
		return nil, fmt.Errorf("create browser profile: %w", err)
	}
	if err := os.Chmod(options.ProfileDir, 0o700); err != nil { // #nosec G302 -- private directories require owner execute.
		return nil, fmt.Errorf("protect browser profile: %w", err)
	}

	var manager *session.Manager
	if !options.BrowserOwnedOnly {
		manager, err = session.NewManager(options.Origin)
		if err != nil {
			return nil, err
		}
	}
	execOptions := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	execOptions = append(
		execOptions,
		chromedp.ExecPath(executable),
		chromedp.UserDataDir(options.ProfileDir),
		chromedp.Flag("headless", options.Headless),
		chromedp.Flag("disable-gpu", false),
		chromedp.NoFirstRun,
		chromedp.NoDefaultBrowserCheck,
	)
	allocatorContext, cancelAllocator := chromedp.NewExecAllocator(parent, execOptions...)
	browserContext, cancelContext := chromedp.NewContext(allocatorContext)
	instance := &Browser{
		context:         browserContext,
		cancelContext:   cancelContext,
		cancelAllocator: cancelAllocator,
		sessions:        manager,
		allowedOrigins:  allowedBrowserOrigins(options),
	}
	if manager != nil {
		observer := newRequestObserver(manager)
		chromedp.ListenTarget(browserContext, observer.Handle)
		if err := chromedp.Run(browserContext, network.Enable()); err != nil {
			_ = instance.Close()
			return nil, fmt.Errorf("start browser network observer: %w", err)
		}
	}
	loginURL := options.StartURL
	if loginURL == "" {
		loginURL = strings.TrimSuffix(options.Origin, "/") + "/mail/"
	}
	if err := chromedp.Run(browserContext, chromedp.Navigate(loginURL)); err != nil {
		_ = instance.Close()
		return nil, fmt.Errorf("navigate to Outlook Web: %w", err)
	}
	return instance, nil
}

// WaitForSession waits for an authorized first-party Outlook Web request.
func (browser *Browser) WaitForSession(ctx context.Context) (session.Credentials, error) {
	if browser.sessions == nil {
		return session.Credentials{}, errors.New(
			"authorization observation is disabled for this browser",
		)
	}
	return browser.sessions.Wait(ctx)
}

// CurrentSession returns the current browser-observed authorization snapshot
// without waiting for a new request.
func (browser *Browser) CurrentSession() (session.Credentials, error) {
	if browser.sessions == nil {
		return session.Credentials{}, errors.New(
			"authorization observation is disabled for this browser",
		)
	}
	return browser.sessions.Current()
}

// Apply applies the newest browser-observed credentials to an exact-origin
// request without exposing them to callers.
func (browser *Browser) Apply(request *http.Request) error {
	if browser.sessions == nil {
		return errors.New(
			"authorization observation is disabled for this browser",
		)
	}
	return browser.sessions.Apply(request)
}

// Close gracefully closes the target and its owned browser process once.
func (browser *Browser) Close() error {
	browser.closeOnce.Do(func() {
		browser.closeErr = chromedp.Cancel(browser.context)
		browser.cancelContext()
		browser.cancelAllocator()
	})
	return browser.closeErr
}

func validateOptions(options Options) error {
	if options.ProfileDir == "" {
		return errors.New("browser profile directory is required")
	}
	if !filepath.IsAbs(options.ProfileDir) {
		return errors.New("browser profile directory must be absolute")
	}
	if strings.ContainsAny(options.Executable, "\r\n\x00") {
		return errors.New("browser executable contains a forbidden character")
	}
	if _, err := session.NewManager(options.Origin); err != nil {
		return fmt.Errorf("validate browser origin: %w", err)
	}
	allowed := allowedBrowserOrigins(options)
	for _, origin := range options.AdditionalOrigins {
		if _, err := session.NewManager(origin); err != nil {
			return fmt.Errorf("validate additional browser origin: %w", err)
		}
	}
	if options.StartURL != "" {
		target, err := url.Parse(options.StartURL)
		if err != nil || target.User != nil ||
			strings.ContainsAny(options.StartURL, "\r\n\x00") {
			return errors.New("browser start URL is malformed")
		}
		if _, exists := allowed[browserRequestOrigin(target)]; !exists {
			return errors.New("browser start URL is outside the approved origins")
		}
	}
	return nil
}

func allowedBrowserOrigins(options Options) map[string]struct{} {
	result := make(map[string]struct{}, 1+len(options.AdditionalOrigins))
	for _, raw := range append([]string{options.Origin}, options.AdditionalOrigins...) {
		parsed, err := url.Parse(raw)
		if err == nil {
			if origin := browserRequestOrigin(parsed); origin != "" {
				result[origin] = struct{}{}
			}
		}
	}
	return result
}

func browserRequestOrigin(target *url.URL) string {
	if target == nil || target.Scheme != "https" || target.Host == "" ||
		target.User != nil {
		return ""
	}
	return "https://" + strings.ToLower(target.Host)
}
