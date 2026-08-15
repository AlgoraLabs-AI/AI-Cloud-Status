package monitor

import "testing"

// downN feeds n both-down rounds, returning the last notification (if any).
func downN(d *TotalLossDetector, n int) *Notification {
	var note *Notification
	for i := 0; i < n; i++ {
		note = d.Update(true)
	}
	return note
}

func TestTotalLossRequiresConfirmation(t *testing.T) {
	d := NewTotalLossDetector()

	// Fewer than confirmDown consecutive down rounds → no alert.
	if note := downN(d, totalLossConfirmDown-1); note != nil {
		t.Fatalf("%d down rounds should not yet alert, got %+v", totalLossConfirmDown-1, note)
	}
	if d.Down() {
		t.Fatal("should not be confirmed-down before the threshold")
	}

	// The confirming round fires the alert exactly once.
	n := d.Update(true)
	if n == nil || n.Title != "Total internet loss" {
		t.Fatalf("the %dth down round should alert, got %+v", totalLossConfirmDown, n)
	}
	if !d.Down() {
		t.Fatal("should be confirmed-down")
	}
	// Continued down → no repeat.
	if n := d.Update(true); n != nil {
		t.Fatalf("continued down should not repeat, got %+v", n)
	}
}

// TestTotalLossIgnoresTransientBlip is the regression for the user-reported
// false "total internet loss" storm: a one- or two-round both-down blip that
// recovers must NEVER alert.
func TestTotalLossIgnoresTransientBlip(t *testing.T) {
	d := NewTotalLossDetector()
	for round := 0; round < 20; round++ {
		// Two down rounds (below the confirm threshold) then a good round, repeatedly.
		if n := d.Update(true); n != nil {
			t.Fatalf("transient down should not alert (round %d), got %+v", round, n)
		}
		if n := d.Update(true); n != nil {
			t.Fatalf("transient down should not alert (round %d), got %+v", round, n)
		}
		if n := d.Update(false); n != nil {
			t.Fatalf("transient recovery should not alert (round %d), got %+v", round, n)
		}
	}
	if d.Down() {
		t.Fatal("transient blips must never confirm total loss")
	}
}

func TestTotalLossRecoveryNeedsConfirmation(t *testing.T) {
	d := NewTotalLossDetector()
	if downN(d, totalLossConfirmDown) == nil {
		t.Fatal("expected confirmed total loss")
	}

	// Good rounds below the recovery threshold must NOT yet announce recovery.
	for i := 1; i < totalLossConfirmUp; i++ {
		if n := d.Update(false); n != nil {
			t.Fatalf("good round %d (< %d) should not yet recover, got %+v", i, totalLossConfirmUp, n)
		}
		if !d.Down() {
			t.Fatal("should still be down until recovery is confirmed")
		}
	}

	// The confirming good round → recovery, exactly once.
	rec := d.Update(false)
	if rec == nil || rec.Title != "Internet connection restored" {
		t.Fatalf("expected recovery on the %dth good round, got %+v", totalLossConfirmUp, rec)
	}
	if d.Down() {
		t.Fatal("should not be down after recovery")
	}
	if n := d.Update(false); n != nil {
		t.Fatalf("steady healthy should not repeat recovery, got %+v", n)
	}
}

// TestTotalLossRecoversFromAFlappingLink is the regression for a deadlock: with
// consecutive-only recovery, upStreak was reset to 0 by every down round, so a
// strictly alternating link could never reach confirmUp and the detector stayed
// latched down forever. The user's last notification said the internet was
// totally gone while it worked half the time — and because the UI checks Down()
// ahead of the partial-loss branches, the packet-loss alert that WAS accurate
// was suppressed every round too.
func TestTotalLossRecoversFromAFlappingLink(t *testing.T) {
	d := NewTotalLossDetector()
	if downN(d, totalLossConfirmDown) == nil {
		t.Fatal("expected confirmed total loss")
	}

	var recovered *Notification
	for i := 0; i < 50 && recovered == nil; i++ {
		if n := d.Update(i%2 == 1); n != nil { // alternating: good, bad, good, bad…
			recovered = n
		}
	}
	if recovered == nil {
		t.Fatal("a link that answers every other round must eventually recover; the detector latched forever")
	}
	if !recovered.Recovery {
		t.Fatalf("expected a recovery notification, got %+v", recovered)
	}
	if d.Down() {
		t.Fatal("should not still report total loss after recovering")
	}
}

// TestTotalLossOneGoodRoundMidOutageDoesNotRecover is the false-positive
// counterweight: widening recovery to N-within-a-window must not let a single
// lucky round announce "restored" during a real outage.
func TestTotalLossOneGoodRoundMidOutageDoesNotRecover(t *testing.T) {
	d := NewTotalLossDetector()
	if downN(d, totalLossConfirmDown) == nil {
		t.Fatal("expected confirmed total loss")
	}

	// One good round every window+1 rounds — never two inside the same window.
	for i := 0; i < 40; i++ {
		good := i%(totalLossRecoveryWindow+1) == 0
		if n := d.Update(!good); n != nil {
			t.Fatalf("round %d: an isolated good round must not announce recovery, got %+v", i, n)
		}
	}
	if !d.Down() {
		t.Fatal("a sustained outage with isolated blips must stay down")
	}
}

// TestTotalLossRecoveryIgnoresPreLatchRounds pins that the recovery window is
// cleared when the outage latches: good rounds observed BEFORE the outage must
// not count toward getting out of it, or the very rounds that preceded the
// outage would immediately recover it.
func TestTotalLossRecoveryIgnoresPreLatchRounds(t *testing.T) {
	d := NewTotalLossDetector()
	for i := 0; i < totalLossConfirmUp; i++ {
		d.Update(false) // healthy rounds before anything goes wrong
	}
	if downN(d, totalLossConfirmDown) == nil {
		t.Fatal("expected confirmed total loss")
	}
	if n := d.Update(true); n != nil {
		t.Fatalf("still-down round right after the latch must not recover, got %+v", n)
	}
	if !d.Down() {
		t.Fatal("should still be down")
	}
}

// TestTotalLossDoesNotReAlertOnEachFlapCycle is the counterweight to widening
// the recovery rule. Exit became "confirmUp good within a window" while entry
// stayed "confirmDown consecutive bad"; without a matching guard on entry, a
// link cycling D D D U U alerts and recovers once per cycle — at a 3s probe
// timeout that is a notification pair every ~10 seconds, which is worse than
// the stuck alert it replaced.
func TestTotalLossDoesNotReAlertOnEachFlapCycle(t *testing.T) {
	d := NewTotalLossDetector()
	alerts, recoveries := 0, 0
	for cycle := 0; cycle < 10; cycle++ {
		for _, bothDown := range []bool{true, true, true, false, false} {
			if n := d.Update(bothDown); n != nil {
				if n.Recovery {
					recoveries++
				} else {
					alerts++
				}
			}
		}
	}
	if alerts != 1 || recoveries != 1 {
		t.Errorf("got %d outage and %d recovery notifications over 10 flap cycles, want 1 and 1", alerts, recoveries)
	}
}

// TestTotalLossFirstDetectionStaysSnappy pins that the anti-churn rule did not
// slow down the case that matters most: a link that was healthy and then dies
// must still latch after confirmDown rounds, not after a whole window. The
// window legitimately holds good rounds from before the blackout, so a blanket
// clean-window requirement would have cost every first detection.
func TestTotalLossFirstDetectionStaysSnappy(t *testing.T) {
	d := NewTotalLossDetector()
	for i := 0; i < totalLossRecoveryWindow; i++ {
		d.Update(false) // a healthy stretch fills the window with good rounds
	}
	for i := 1; i < totalLossConfirmDown; i++ {
		if n := d.Update(true); n != nil {
			t.Fatalf("round %d should not yet confirm, got %+v", i, n)
		}
	}
	if n := d.Update(true); n == nil {
		t.Fatalf("expected the alert on round %d, right after a healthy stretch", totalLossConfirmDown)
	}
}

// TestTotalLossReAlertsOnAGenuineSecondBlackout is the other side: seeding the
// window all-good on recovery must not make the detector deaf to a real second
// outage later on.
func TestTotalLossReAlertsOnAGenuineSecondBlackout(t *testing.T) {
	d := NewTotalLossDetector()
	if downN(d, totalLossConfirmDown) == nil {
		t.Fatal("expected the first confirmed total loss")
	}
	for i := 0; i < totalLossConfirmUp; i++ {
		d.Update(false)
	}
	if d.Down() {
		t.Fatal("expected recovery before the second blackout")
	}

	var second *Notification
	for i := 0; i < totalLossRecoveryWindow+totalLossConfirmDown+2 && second == nil; i++ {
		second = d.Update(true)
	}
	if second == nil || second.Recovery {
		t.Fatalf("a sustained second blackout must re-alert, got %+v", second)
	}
}

// TestTotalLossZeroValueStillRecovers guards the constructor-bypass trap: a
// zero-value detector has window 0, which would make every good round invisible
// and reinstate the permanent latch this mechanism removes.
func TestTotalLossZeroValueStillRecovers(t *testing.T) {
	d := &TotalLossDetector{confirmDown: totalLossConfirmDown, confirmUp: totalLossConfirmUp}
	if downN(d, totalLossConfirmDown) == nil {
		t.Fatal("expected confirmed total loss")
	}
	var rec *Notification
	for i := 0; i < 10 && rec == nil; i++ {
		rec = d.Update(false)
	}
	if rec == nil {
		t.Fatal("a zero-value detector never recovered — window fell back to 0")
	}
}

func TestTotalLossResetClearsLatch(t *testing.T) {
	d := NewTotalLossDetector()
	if downN(d, totalLossConfirmDown) == nil {
		t.Fatal("expected confirmed total loss")
	}
	d.Reset()
	if d.Down() {
		t.Fatal("Reset should clear the latched down state")
	}
	// Reset must not emit a spurious recovery, and a fresh confirmed loss re-alerts.
	if downN(d, totalLossConfirmDown) == nil {
		t.Fatal("both-down after reset should re-alert once confirmed")
	}
}

func TestTotalLossNoRecoveryWithoutPriorLoss(t *testing.T) {
	d := NewTotalLossDetector()
	if n := d.Update(false); n != nil {
		t.Fatalf("no prior loss should not recover, got %+v", n)
	}
}
