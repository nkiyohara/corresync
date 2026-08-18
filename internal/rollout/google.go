// Package rollout contains release-owned capability gates that cannot be
// changed by configuration, environment variables, CLI flags, or MCP input.
package rollout

import "errors"

// GoogleBYOOAuthEnabled makes only explicitly configured, user-owned Google
// Desktop clients available. It is release-owned so discovery, configuration,
// CLI, and MCP input cannot silently change the accepted authentication route.
const GoogleBYOOAuthEnabled = true

// GoogleManagedOAuthEnabled remains false until a separately reviewed release
// has an approved Corresync-owned client and opt-in live evidence. BYO client
// settings can never open this route.
const GoogleManagedOAuthEnabled = false

// ErrGoogleBYOOAuthUnavailable is retained as the fail-closed boundary for a
// future release that deliberately withdraws the user-owned route.
var ErrGoogleBYOOAuthUnavailable = errors.New(
	"user-owned Google OAuth is not available in this release",
)

// ErrGoogleManagedOAuthUnavailable is returned before keyring access, browser
// launch, token handling, or provider traffic if the dormant managed route is
// ever selected while its release gate is closed.
var ErrGoogleManagedOAuthUnavailable = errors.New(
	"corresync-managed Google OAuth is not offered in this release; use a " +
		"Google Desktop OAuth client from a Cloud project you control",
)
