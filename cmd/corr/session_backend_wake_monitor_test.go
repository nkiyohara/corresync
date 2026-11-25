package main

import (
	"context"
	"errors"
	"net"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/eventqueue"
)

const wakeMonitorInterval = time.Minute

// A successful poll reads the inbox window twice; a failed poll stops after
// the first failed read.
const (
	readsPerSuccessfulPoll = 2
	readsPerFailedPoll     = 1
)

// startWakeMonitor runs the daemon's real monitor loop for one account. Call it
// inside a synctest bubble so the ticker follows the bubble's fake clock.
func startWakeMonitor(
	t *testing.T,
	backend *sessionBackend,
	account *wakeTestAccount,
) (stop func()) {
	t.Helper()
	engine, err := application.NewMonitorEngine(
		eventqueue.NewAt(t.TempDir()), &lockedAudit{}, nil, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	backend.monitorEngine = engine
	policy := application.MonitorPolicy{
		Account: account.id, Alias: account.alias, Mode: domain.MonitorQueue,
		PollInterval: wakeMonitorInterval, Retention: 24 * time.Hour,
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	backend.mu.Lock()
	active := backend.accounts[account.id]
	backend.monitorStarted[account.id] = true
	backend.monitorCancel[account.id] = cancel
	backend.monitorDone[account.id] = done
	backend.mu.Unlock()
	backend.active.Add(1)
	go backend.monitorLoop(ctx, account.id, policy, active.mail, active.mailLease, done)
	return func() {
		cancel()
		<-done
	}
}

func TestSessionBackendMonitorCollapsesOverduePollsAfterWake(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		work := &wakeTestAccount{
			alias: "work", id: "acc_00000000000000000000000000000001",
			provider: domain.ProviderJMAP, port: newWakeMailPort(),
		}
		backend := newWakeTestBackend(t, work)
		stop := startWakeMonitor(t, backend, work)
		defer stop()
		synctest.Wait()
		if got := work.port.lists.Load(); got != readsPerSuccessfulPoll {
			t.Fatalf("initial poll reads = %d", got)
		}

		// The next poll is in flight when the machine sleeps; the process does
		// not run again until ten and a half intervals later.
		release := work.port.freeze()
		time.Sleep(wakeMonitorInterval)
		synctest.Wait()
		if got := work.port.lists.Load(); got != readsPerSuccessfulPoll+1 {
			t.Fatalf("frozen poll reads = %d", got)
		}
		time.Sleep(10*wakeMonitorInterval + wakeMonitorInterval/2)
		wake := time.Now()
		release()
		synctest.Wait()

		// The interrupted poll finishes and the ten overdue ticks collapse into
		// exactly one more poll at wake time.
		const afterWake = 3 * readsPerSuccessfulPoll
		if got := work.port.lists.Load(); got != afterWake {
			t.Fatalf("reads after wake = %d, want %d", got, afterWake)
		}
		time.Sleep(wakeMonitorInterval/2 - time.Second)
		synctest.Wait()
		if got := work.port.lists.Load(); got != afterWake {
			t.Fatalf("reads before the next interval = %d, want %d", got, afterWake)
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if got := work.port.lists.Load(); got != afterWake+readsPerSuccessfulPoll {
			t.Fatalf("reads at the next interval = %d", got)
		}
		work.port.mu.Lock()
		calledAt := append([]time.Time(nil), work.port.calledAt...)
		work.port.mu.Unlock()
		atWake := 0
		for _, called := range calledAt {
			if called.Equal(wake) {
				atWake++
			}
		}
		// The second read of the interrupted poll plus one full poll ran at
		// wake time; nothing else did.
		if atWake != 1+readsPerSuccessfulPoll {
			t.Fatalf("reads at wake = %d, times = %v", atWake, calledAt)
		}
	})
}

func TestSessionBackendMonitorKeepsPollingThroughNetworkLossWithoutSignOut(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		work := &wakeTestAccount{
			alias: "work", id: "acc_00000000000000000000000000000001",
			provider: domain.ProviderJMAP, port: newWakeMailPort(),
		}
		backend := newWakeTestBackend(t, work)
		stop := startWakeMonitor(t, backend, work)
		defer stop()
		synctest.Wait()

		// After wake the network is still down for five intervals.
		work.port.failReads(&net.DNSError{
			Err: "no such host", Name: "mail.example.invalid", IsTemporary: true,
		})
		const offlineIntervals = 5
		time.Sleep(offlineIntervals * wakeMonitorInterval)
		synctest.Wait()
		offlineReads := int32(readsPerSuccessfulPoll + offlineIntervals*readsPerFailedPoll)
		if got := work.port.lists.Load(); got != offlineReads {
			t.Fatalf("offline reads = %d, want one per interval (%d)", got, offlineReads)
		}
		if state := mailServiceState(t, backend, "work"); state.State !=
			application.AuthenticationStateAuthenticated {
			t.Fatalf("mail status while offline = %+v", state)
		}
		if work.closer.calls.Load() != 0 {
			t.Fatal("network loss closed the session")
		}
		backend.mu.Lock()
		running := backend.monitorStarted[work.id]
		backend.mu.Unlock()
		if !running {
			t.Fatal("network loss stopped the monitor")
		}

		// The network returns and the same monitor resumes on its next tick.
		work.port.failReads(nil)
		time.Sleep(wakeMonitorInterval)
		synctest.Wait()
		if got := work.port.lists.Load(); got != offlineReads+readsPerSuccessfulPoll {
			t.Fatalf("reads after reconnect = %d", got)
		}
	})
}

func TestSessionBackendMonitorStopsOnExpiryAfterWakeAndRequiresSignIn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		work := &wakeTestAccount{
			alias: "work", id: "acc_00000000000000000000000000000001",
			provider: domain.ProviderJMAP, port: newWakeMailPort(),
		}
		home := &wakeTestAccount{
			alias: "home", id: "acc_00000000000000000000000000000002",
			provider: domain.ProviderJMAP, port: newWakeMailPort(),
		}
		backend := newWakeTestBackend(t, work, home)
		stop := startWakeMonitor(t, backend, work)
		synctest.Wait()

		// The session expired during sleep; the first poll after wake sees it.
		work.port.failReads(application.NewProviderAuthenticationFailure(
			application.AuthenticationReasonSessionExpired,
			errors.New("synthetic expiry"),
		))
		time.Sleep(wakeMonitorInterval)
		synctest.Wait()
		backend.mu.Lock()
		_, tracked := backend.monitorDone[work.id]
		backend.mu.Unlock()
		if tracked {
			t.Fatal("expired monitor is still tracked")
		}
		stop()
		if work.closer.calls.Load() != 1 {
			t.Fatalf("expired session closes = %d", work.closer.calls.Load())
		}
		reads := work.port.lists.Load()
		time.Sleep(10 * wakeMonitorInterval)
		synctest.Wait()
		if work.port.lists.Load() != reads {
			t.Fatal("monitor kept polling an expired session")
		}
		if state := mailServiceState(t, backend, "work"); state.State !=
			application.AuthenticationStateReauthenticationNeeded ||
			state.Reason != application.AuthenticationReasonSessionExpired {
			t.Fatalf("work status = %+v", state)
		}
		if _, err := backend.ListMail(t.Context(), wakeInboxInput(home.id), wakeCaller); err != nil {
			t.Fatalf("other account after monitor expiry = %v", err)
		}
	})
}
