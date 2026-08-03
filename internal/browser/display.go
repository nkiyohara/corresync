package browser

import (
	"errors"
	"fmt"
	"os"
	"runtime"
)

// ErrGraphicalSessionUnavailable identifies a visible-browser launch that
// cannot succeed because Linux exposes no graphical display to this process.
var ErrGraphicalSessionUnavailable = errors.New("graphical session unavailable")

// RequireGraphicalSession checks the minimum environment required before a
// visible Chromium process is launched. Headless callers must skip this check.
func RequireGraphicalSession() error {
	return requireGraphicalSession(runtime.GOOS, os.LookupEnv)
}

func requireGraphicalSession(
	goos string,
	lookupEnv func(string) (string, bool),
) error {
	if goos != "linux" {
		return nil
	}
	for _, name := range []string{"DISPLAY", "WAYLAND_DISPLAY"} {
		if value, exists := lookupEnv(name); exists && value != "" {
			return nil
		}
	}
	return fmt.Errorf(
		"%w: DISPLAY and WAYLAND_DISPLAY are both unset",
		ErrGraphicalSessionUnavailable,
	)
}
