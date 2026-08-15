package ui

import (
	"context"
	"testing"
	"time"

	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/monitor"
)

// TestResumedFromSuspendIgnoresSleep is the regression for a real relaunch that
// should never have happened: on 2026-08-02 the app logged "Fyne event loop
// STALLED, stale=43m32s" and restarted itself 0.8s after the machine woke, while
// the Windows event log showed sleep at 10:31:00 and resume at 11:14:30. The
// loop was fine — the watchdog was comparing a wall-clock heartbeat age against
// a monotonic ticker that does not run while the machine is suspended, so every
// lid-close longer than the stall threshold killed the app on wake.
func TestResumedFromSuspendIgnoresSleep(t *testing.T) {
	c := &Controller{}
	stale := time.Now().Add(-43 * time.Minute)
	c.lastUIBeat.Store(stale.UnixNano())

	if !c.resumedFromSuspend(43 * time.Minute) {
		t.Fatal("a 43m wall-clock gap between watchdog ticks is a machine suspend, not a stalled loop")
	}
	// The heartbeat must be treated as fresh, or the very next iteration relaunches
	// anyway and the guard achieves nothing.
	if age := time.Since(time.Unix(0, c.lastUIBeat.Load())); age > watchdogStallAfter {
		t.Errorf("heartbeat age after resume = %v, want it reset below the %v stall threshold", age, watchdogStallAfter)
	}
}

// TestResumedFromSuspendLeavesRealStallsAlone pins that a normally-scheduled
// iteration is never mistaken for a resume — otherwise the guard would swallow
// the genuine deadlock the watchdog exists to catch.
func TestResumedFromSuspendLeavesRealStallsAlone(t *testing.T) {
	c := &Controller{}
	beat := time.Now().Add(-5 * time.Minute)
	c.lastUIBeat.Store(beat.UnixNano())

	if c.resumedFromSuspend(watchdogProbeEvery) {
		t.Fatal("an on-schedule tick must not be read as a suspend")
	}
	if got := time.Unix(0, c.lastUIBeat.Load()); !got.Equal(beat) {
		t.Errorf("heartbeat was rewritten to %v; a non-resume tick must leave the stall evidence intact", got)
	}
}

// TestResumedFromSuspendClearsLossWindows pins the companion cleanup: the rolling
// loss windows are full of probes that failed while the radio powered down for
// suspend. Left in place they describe a lossy link rather than a sleeping
// machine, and fire a "you're losing packets" alert seconds after the lid opens.
func TestResumedFromSuspendClearsLossWindows(t *testing.T) {
	// 192.0.2.1 is TEST-NET-1: reserved, unroutable, and guaranteed never to
	// answer, so every probe round records a loss.
	target := monitor.Target{ID: "t1", Name: "t1", Host: "192.0.2.1"}
	eng := monitor.NewEngine([]monitor.Target{target}, 10, 200*time.Millisecond, nil)
	c := &Controller{engine: eng}

	ctx := context.Background()
	for i := 0; i < 3; i++ {
		eng.RunOnce(ctx)
	}
	if got := eng.AvgTargetLossPercent(); got == 0 {
		t.Skip("probes unexpectedly succeeded; cannot stage the pre-suspend loss")
	}

	c.lastUIBeat.Store(time.Now().Add(-time.Hour).UnixNano())
	if !c.resumedFromSuspend(time.Hour) {
		t.Fatal("expected the hour-long gap to read as a suspend")
	}
	if got := eng.AvgTargetLossPercent(); got != 0 {
		t.Errorf("loss after resume = %v%%, want a cleared window — those probes failed to a "+
			"radio powering down for sleep, not to a lossy link", got)
	}
}

// TestOfflineIgnoresPreBlackoutProviderEvidence is the regression for the alert
// the user was never supposed to see. At 10:29:08 the app announced "Total
// internet loss"; 51 seconds later it fired "Provider outage — portaldev:
// request failed", blaming a remote endpoint for the user's own dead link. The
// offline gate had been vetoed by a provider fetch that succeeded BEFORE the
// network dropped but was still inside the 2-minute freshness window.
//
// Evaluate itself is unchanged and still correct; what recomputeOffline must do
// is stop feeding it observations that predate the blackout.
func TestOfflineIgnoresPreBlackoutProviderEvidence(t *testing.T) {
	blackoutStart := time.Now().Add(-30 * time.Second)
	staleSuccess := blackoutStart.Add(-5 * time.Second) // succeeded just before the drop
	freshSuccess := blackoutStart.Add(10 * time.Second) // succeeded during it

	// counts mirrors recomputeOffline's filter: an observation older than the
	// blackout contributes nothing either way.
	counts := func(when time.Time) (checked, unreachable int) {
		if !blackoutStart.IsZero() && when.Before(blackoutStart) {
			return 0, 0
		}
		return 1, 0 // one provider, reachable
	}

	checked, unreachable := counts(staleSuccess)
	if got := monitor.Evaluate(true, checked, unreachable); !got {
		t.Error("connectivity is down and the only provider success predates the blackout: want offline")
	}

	// The proxied-network case the positive-evidence rule exists for: ICMP and
	// TCP:443 to the fixed resolvers are blocked, but provider HTTPS keeps
	// working DURING the blackout, so it still wins.
	checked, unreachable = counts(freshSuccess)
	if got := monitor.Evaluate(true, checked, unreachable); got {
		t.Error("a provider fetch that succeeded DURING the blackout proves we are online: want not offline")
	}
}

// TestTrayMenuOpenIsNotAStall is the regression for a relaunch that fired twice
// on 2026-08-15 while nothing was wrong: right-clicking the tray icon and leaving
// the menu up for 41s logged "Fyne event loop STALLED, stale=40.008s" and
// restarted the app.
//
// An open NSMenu runs a nested modal run loop that does not drain the fyne.Do
// queue, so the heartbeat stops advancing while the app is perfectly healthy —
// which is indistinguishable from a wedge unless the watchdog is told the menu
// is up.
func TestTrayMenuOpenIsNotAStall(t *testing.T) {
	c := &Controller{}
	// A beat from a minute ago, then the user opens the menu: every beat since is
	// queued behind the modal loop, so the OPEN is newer than the last one drained.
	c.lastUIBeat.Store(time.Now().Add(-time.Minute).UnixNano())
	c.lastTrayOpen.Store(time.Now().Add(-50 * time.Second).UnixNano())

	if !c.trayMenuHoldsTheBeat() {
		t.Fatal("a tray open recorded after the last drained beat means the menu is still up, not that the loop wedged")
	}
}

// TestClosingTheTrayMenuEndsTheGrace pins how the guard releases itself. There is
// no menu-close callback to listen for; what happens instead is that the queued
// beats flush the instant the menu goes away, carrying lastUIBeat past the open.
// If that did not end the suppression, one tray open would disable the watchdog
// for the rest of the run.
func TestClosingTheTrayMenuEndsTheGrace(t *testing.T) {
	c := &Controller{}
	c.lastTrayOpen.Store(time.Now().Add(-30 * time.Second).UnixNano())
	c.lastUIBeat.Store(time.Now().UnixNano()) // the flush after the menu closed

	if c.trayMenuHoldsTheBeat() {
		t.Fatal("once a beat drains later than the open, the menu is closed and the watchdog must resume judging")
	}
}

// TestTrayMenuGraceIsBounded: the signal says "opened", never "closed", so a loop
// that wedges WHILE the menu is up would otherwise be excused forever. Past the
// bound the wedge is the likelier reading and the watchdog takes over again.
func TestTrayMenuGraceIsBounded(t *testing.T) {
	c := &Controller{}
	old := time.Now().Add(-trayMenuGraceMax - time.Minute)
	c.lastTrayOpen.Store(old.UnixNano())
	c.lastUIBeat.Store(old.Add(-time.Minute).UnixNano()) // still older than the open

	if c.trayMenuHoldsTheBeat() {
		t.Fatalf("a tray open older than %v must stop excusing a silent heartbeat", trayMenuGraceMax)
	}
}

// TestNoTrayOpenNeverSuppresses is the Windows case, and the floor everywhere
// else: systray only ever writes TrayOpenedCh on macOS and Linux, so on Windows
// the timestamp stays zero and this guard must be completely inert.
func TestNoTrayOpenNeverSuppresses(t *testing.T) {
	c := &Controller{}
	c.lastUIBeat.Store(time.Now().Add(-10 * time.Minute).UnixNano())

	if c.trayMenuHoldsTheBeat() {
		t.Fatal("with no tray open on record the guard must never fire — a real stall has to reach the relaunch")
	}
}
