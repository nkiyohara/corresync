// Package rollout contains release-owned capability gates that cannot be
// changed by configuration, environment variables, CLI flags, or MCP input.
package rollout

import "errors"

// GoogleOAuthApproved remains false until Google approves the production
// OAuth application. Gmail, Google Calendar, and Google Tasks code may ship
// and receive synthetic coverage while every released route stays unreachable.
const GoogleOAuthApproved = false

// ErrGoogleOAuthPending is returned before keyring access, browser launch,
// token handling, or provider traffic whenever a Google route is selected.
var ErrGoogleOAuthPending = errors.New(
	"corresync's Google OAuth application is awaiting approval; this release " +
		"includes the Google integration but keeps it disabled, so no Google " +
		"sign-in was started. Gmail, Google Calendar, and Google Tasks support " +
		"are coming soon after approval",
)
