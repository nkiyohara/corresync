//go:build live

// Live observation harness: Gmail and Google Calendar API with a user-owned OAuth client (`google`).
//
// Opt-in, all required (anything else skips or stops the test):
//
//	CORRESYNC_LIVE_CONFIRM=google
//	CORRESYNC_LIVE_ACCOUNT=<alias whose active services are all on this route>
//	CORRESYNC_LIVE_CONFIG=<absolute path to the config holding that alias>
//	CORRESYNC_STATE_DIR=<absolute path to a state directory used only for live runs>
//
// Reversible synthetic writes, optional: CORRESYNC_LIVE_WRITE=google:<alias>.
// Writes: calendar event only (CORRESYNC_LIVE_CALENDAR_NAME); Gmail cannot
// permanently delete a draft through this route, so mail stays read-only.
//
// Sign-in opens the system browser for the configured Desktop OAuth client.
//
// Run one route at a time from a clean checkout:
//
//	mise exec -- go test -tags live -count=1 -v ./cmd/corr -run '^TestLiveGoogleRoute$'
//
// The record format, optional variables, and cleanup are in
// docs/evidence/README.md.

package main

import (
	"testing"

	"github.com/nkiyohara/corresync/internal/livetest"
)

func TestLiveGoogleRoute(t *testing.T) {
	optIn := livetest.Require(t, livetest.RouteGoogle)
	runLiveRoute(t, optIn)
}
