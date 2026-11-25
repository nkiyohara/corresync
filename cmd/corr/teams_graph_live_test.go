//go:build live

// Live observation harness: Microsoft Teams through Microsoft Graph (preview).
//
// Opt-in, all required (anything else skips or stops the test):
//
//	CORRESYNC_LIVE_CONFIRM=teams-graph
//	CORRESYNC_LIVE_ACCOUNT=<alias whose active services are all on this route>
//	CORRESYNC_LIVE_CONFIG=<absolute path to the config holding that alias>
//	CORRESYNC_STATE_DIR=<absolute path to a state directory used only for live runs>
//
// Reversible synthetic writes, optional: CORRESYNC_LIVE_WRITE=teams-graph:<alias>.
// Writes: none: a posted message notifies other members and a deleted one leaves
// a visible trace, so this route is read-only.
//
// Sign-in opens the system browser for the configured public OAuth client.
//
// Run one route at a time from a clean checkout:
//
//	mise exec -- go test -tags live -count=1 -v ./cmd/corr -run '^TestLiveTeamsGraphRoute$'
//
// The record format, optional variables, and cleanup are in
// docs/evidence/README.md.

package main

import (
	"testing"

	"github.com/nkiyohara/corresync/internal/livetest"
)

func TestLiveTeamsGraphRoute(t *testing.T) {
	optIn := livetest.Require(t, livetest.RouteTeamsGraph)
	runLiveRoute(t, optIn)
}
