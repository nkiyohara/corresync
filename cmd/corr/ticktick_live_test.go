//go:build live

// Live observation harness: TickTick tasks (`ticktick`).
//
// Opt-in, all required (anything else skips or stops the test):
//
//	CORRESYNC_LIVE_CONFIRM=ticktick
//	CORRESYNC_LIVE_ACCOUNT=<alias whose active services are all on this route>
//	CORRESYNC_LIVE_CONFIG=<absolute path to the config holding that alias>
//	CORRESYNC_STATE_DIR=<absolute path to a state directory used only for live runs>
//
// Reversible synthetic writes, optional: CORRESYNC_LIVE_WRITE=ticktick:<alias>.
// Writes: task (CORRESYNC_LIVE_TASK_LIST_NAME).
//
// Sign-in opens the system browser; the client secret stays behind its
// separately consented OS-keyring handle. The older adapter-level read-only
// harness in internal/provider/ticktick remains available.
//
// Run one route at a time from a clean checkout:
//
//	mise exec -- go test -tags live -count=1 -v ./cmd/corr -run '^TestLiveTickTickRoute$'
//
// The record format, optional variables, and cleanup are in
// docs/evidence/README.md.

package main

import (
	"testing"

	"github.com/nkiyohara/corresync/internal/livetest"
)

func TestLiveTickTickRoute(t *testing.T) {
	optIn := livetest.Require(t, livetest.RouteTickTick)
	runLiveRoute(t, optIn)
}
