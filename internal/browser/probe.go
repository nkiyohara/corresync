package browser

import (
	"context"
	"errors"
	"os"
	"runtime"
	"strings"

	"github.com/chromedp/chromedp"
)

var (
	// ErrLinuxSandboxUnavailable is content-free so doctor never exposes raw
	// Chromium stderr, private paths, or host policy details.
	ErrLinuxSandboxUnavailable = errors.New(
		"chromium could not start with the Linux sandbox; install a system-managed " +
			"Chrome or Chromium whose AppArmor policy covers its executable path, then " +
			"configure that path. Corresync will not disable the sandbox",
	)
	errBrowserProbeFailed = errors.New(
		"the Chromium executable resolved but could not start with normal sandbox settings",
	)
	errBrowserProbeTimeout  = errors.New("the Chromium launch check timed out")
	errBrowserProbeCanceled = errors.New("the Chromium launch check was canceled")
)

// Probe starts and closes a headless blank Chromium target with the same
// sandbox-relevant allocator options used by Launch. It performs no navigation,
// authentication, or provider request.
func Probe(ctx context.Context, configured string) (string, error) {
	executable, err := ResolveExecutable(configured)
	if err != nil {
		return "", err
	}
	profileDirectory, err := os.MkdirTemp("", "corresync-browser-probe-")
	if err != nil {
		return "", errors.New("create private Chromium launch-check profile")
	}
	defer func() { _ = os.RemoveAll(profileDirectory) }()
	if err := os.Chmod(profileDirectory, 0o700); err != nil { // #nosec G302 -- private probe profile.
		return "", errors.New("protect private Chromium launch-check profile")
	}

	allocatorContext, cancelAllocator := chromedp.NewExecAllocator(
		ctx,
		allocatorOptions(executable, profileDirectory, true)...,
	)
	defer cancelAllocator()
	browserContext, cancelBrowser := chromedp.NewContext(allocatorContext)
	defer cancelBrowser()
	var ready bool
	if err := chromedp.Run(browserContext, chromedp.Evaluate("true", &ready)); err != nil {
		return "", classifyProbeError(ctx, err, runtime.GOOS)
	}
	if !ready {
		return "", errBrowserProbeFailed
	}
	return executable, nil
}

func classifyProbeError(ctx context.Context, err error, goos string) error {
	if errors.Is(context.Cause(ctx), context.DeadlineExceeded) ||
		errors.Is(err, context.DeadlineExceeded) {
		return errBrowserProbeTimeout
	}
	if errors.Is(context.Cause(ctx), context.Canceled) ||
		errors.Is(err, context.Canceled) {
		return errBrowserProbeCanceled
	}
	if goos != "linux" {
		return errBrowserProbeFailed
	}
	detail := strings.ToLower(err.Error())
	for _, marker := range []string{
		"apparmor",
		"failed to move to new namespace",
		"namespace sandbox",
		"no usable sandbox",
		"running as root without --no-sandbox",
		"setuid sandbox",
		"suid sandbox",
		"userns",
		"zygote_host_impl_linux",
	} {
		if strings.Contains(detail, marker) {
			return ErrLinuxSandboxUnavailable
		}
	}
	return errBrowserProbeFailed
}
