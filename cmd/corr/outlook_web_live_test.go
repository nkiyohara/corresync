//go:build live

// Live observation harness: Outlook Web mail and calendar (`microsoft-owa`).
//
// Opt-in, all required (anything else skips or stops the test):
//
//	CORRESYNC_LIVE_CONFIRM=outlook-web
//	CORRESYNC_LIVE_ACCOUNT=<alias whose active services are all on this route>
//	CORRESYNC_LIVE_CONFIG=<absolute path to the config holding that alias>
//	CORRESYNC_STATE_DIR=<absolute path to a state directory used only for live runs>
//
// Reversible synthetic writes, optional: CORRESYNC_LIVE_WRITE=outlook-web:<alias>.
// Writes: draft (Drafts) and calendar event (CORRESYNC_LIVE_CALENDAR_NAME).
//
// Sign-in opens the account's own visible browser; complete SSO and MFA there.
// This needs a local display. On a host without one, use the forwarded login
// view manual procedure in docs/manual-test-checklist.md instead.
//
// Run one route at a time from a clean checkout:
//
//	mise exec -- go test -tags live -count=1 -v ./cmd/corr -run '^TestLiveOutlookWebRoute$'
//
// The record format, optional variables, and cleanup are in
// docs/evidence/README.md.

package main

import (
	"testing"

	"github.com/nkiyohara/corresync/internal/livetest"
)

func TestLiveOutlookWebRoute(t *testing.T) {
	optIn := livetest.Require(t, livetest.RouteOutlookWeb)
	runLiveRoute(t, optIn)
}
