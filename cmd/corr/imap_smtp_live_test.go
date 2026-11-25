//go:build live

// Live observation harness: IMAP/SMTP mail (`imap-smtp`), including the iCloud preset's mail half.
//
// Opt-in, all required (anything else skips or stops the test):
//
//	CORRESYNC_LIVE_CONFIRM=imap-smtp
//	CORRESYNC_LIVE_ACCOUNT=<alias whose active services are all on this route>
//	CORRESYNC_LIVE_CONFIG=<absolute path to the config holding that alias>
//	CORRESYNC_STATE_DIR=<absolute path to a state directory used only for live runs>
//
// Reversible synthetic writes, optional: CORRESYNC_LIVE_WRITE=imap-smtp:<alias>.
// Writes: draft (Drafts).
//
// The credential comes from the configured OS-keyring entry or credential
// helper after its normal consent prompt. Nothing is sent over SMTP.
//
// Run one route at a time from a clean checkout:
//
//	mise exec -- go test -tags live -count=1 -v ./cmd/corr -run '^TestLiveIMAPSMTPRoute$'
//
// The record format, optional variables, and cleanup are in
// docs/evidence/README.md.

package main

import (
	"testing"

	"github.com/nkiyohara/corresync/internal/livetest"
)

func TestLiveIMAPSMTPRoute(t *testing.T) {
	optIn := livetest.Require(t, livetest.RouteIMAPSMTP)
	runLiveRoute(t, optIn)
}
