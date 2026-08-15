package monitor

import (
	"strings"
	"testing"
	"time"
)

// TestSnapshotRestoreKeepsTheRecoveryAlive replays the exact failure observed in
// the field on 2026-08-02: a custom URL check alerted an outage at 10:29:59, the
// process relaunched at 11:14:30, the check was healthy again at 11:15:01 — and
// because the tracker is edge-triggered and lived only in memory, the first
// healthy poll was not a transition and no recovery was ever emitted. The user
// was told a check went down and never told it came back.
func TestSnapshotRestoreKeepsTheRecoveryAlive(t *testing.T) {
	first := NewOutageTracker()
	if n := first.Update("url-1", "portaldev", "request failed", true); n == nil {
		t.Fatal("expected the outage alert")
	}

	saved := first.Snapshot()
	if len(saved) != 1 {
		t.Fatalf("Snapshot = %d entries, want 1", len(saved))
	}
	if saved[0].ID != "url-1" || saved[0].Name != "portaldev" || saved[0].Summary != "request failed" {
		t.Fatalf("Snapshot lost the identity needed for the recovery text: %+v", saved[0])
	}
	if saved[0].Since.IsZero() {
		t.Error("Snapshot has no start time, so freshness can never be judged")
	}

	// --- the process restarts ---
	next := NewOutageTracker()
	if stale := next.Restore(saved, time.Now(), 24*time.Hour); len(stale) != 0 {
		t.Fatalf("a minutes-old outage was treated as stale: %+v", stale)
	}
	// Restoring must not itself announce anything: the outage was already alerted.
	// The recovery takes recoveryConfirmPolls healthy polls, exactly as if the
	// process had never died.
	if n := next.Update("url-1", "portaldev", "", false); n != nil {
		t.Fatalf("first healthy poll should still be debounced, got %+v", n)
	}
	n := next.Update("url-1", "portaldev", "", false)
	if n == nil {
		t.Fatal("no recovery after a restart — this is the bug")
	}
	if !n.Recovery || n.Title != TitleProviderRecovered {
		t.Errorf("notification = %+v, want a recovery", n)
	}
	if !strings.Contains(n.Body, "portaldev") {
		t.Errorf("recovery body = %q, want the check's name", n.Body)
	}
	if !strings.Contains(n.Body, "request failed") {
		t.Errorf("recovery body = %q, want the summary that drove the outage", n.Body)
	}
}

// TestRestoreDropsStaleOutages pins the freshness policy: an outage from before
// a long shutdown is closed silently and handed back so the caller can journal
// it, instead of popping a toast about ancient history.
func TestRestoreDropsStaleOutages(t *testing.T) {
	now := time.Now()
	states := []DownState{
		{ID: "fresh", Name: "Fresh", Since: now.Add(-2 * time.Hour)},
		{ID: "old", Name: "Old", Since: now.Add(-72 * time.Hour)},
		{ID: "undated", Name: "Undated"}, // zero Since: unjudgeable, so not news
	}
	stale := NewOutageTracker()
	got := stale.Restore(states, now, 24*time.Hour)
	if len(got) != 2 {
		t.Fatalf("stale = %d entries, want 2 (old + undated): %+v", len(got), got)
	}
	// The fresh one is latched and still owes a recovery...
	stale.Update("fresh", "Fresh", "", false)
	if n := stale.Update("fresh", "Fresh", "", false); n == nil {
		t.Error("the fresh outage lost its pending recovery")
	}
	// ...the stale ones are gone, so a healthy poll is simply steady state.
	if n := stale.Update("old", "Old", "", false); n != nil {
		t.Errorf("a stale outage still announced a recovery: %+v", n)
	}
}

// TestRestoreRecoveryWithoutACallerName pins that a re-latched outage can still
// name itself: the poll that finally sees it healthy may not carry a display
// name, and "is operational again" with a blank subject helps nobody.
func TestRestoreRecoveryWithoutACallerName(t *testing.T) {
	o := NewOutageTracker()
	o.Restore([]DownState{{ID: "p1", Name: "Mistral", Since: time.Now()}}, time.Now(), time.Hour)
	o.Update("p1", "", "", false)
	n := o.Update("p1", "", "", false)
	if n == nil {
		t.Fatal("expected a recovery")
	}
	if !strings.Contains(n.Body, "Mistral") {
		t.Errorf("recovery body = %q, want the name carried across the restart", n.Body)
	}
}

// TestSnapshotIsEmptyWhenNothingIsDown guards against persisting a file full of
// resolved checks that would be re-latched forever.
func TestSnapshotIsEmptyWhenNothingIsDown(t *testing.T) {
	o := NewOutageTracker()
	o.Update("p1", "P1", "boom", true)
	o.Update("p1", "P1", "", false)
	o.Update("p1", "P1", "", false) // recovered
	if got := o.Snapshot(); len(got) != 0 {
		t.Errorf("Snapshot after recovery = %+v, want none", got)
	}
	o.Update("p2", "P2", "boom", true)
	o.Reset("p2")
	if got := o.Snapshot(); len(got) != 0 {
		t.Errorf("Snapshot after Reset = %+v, want none", got)
	}
}

// TestRestoredTotalLossRecoversInTwoGoodRounds pins that restoring only the
// CONCLUSION is enough: the zeroed bitmask is exactly the state a live latch
// leaves behind, so recovery still earns its confirmUp good rounds and arrives.
func TestRestoredTotalLossRecoversInTwoGoodRounds(t *testing.T) {
	d := NewTotalLossDetector()
	d.RestoreLatch()
	if !d.Down() {
		t.Fatal("RestoreLatch did not latch")
	}
	if n := d.Update(false); n != nil {
		t.Fatalf("one good round should not be enough: %+v", n)
	}
	n := d.Update(false)
	if n == nil || !n.Recovery || n.Title != TitleTotalLossRecovered {
		t.Fatalf("second good round = %+v, want the restored recovery", n)
	}
}

// TestRestoredTotalLossDoesNotReAnnounce pins that a restart during an ongoing
// blackout does not re-tell the user the internet is down.
func TestRestoredTotalLossDoesNotReAnnounce(t *testing.T) {
	d := NewTotalLossDetector()
	d.RestoreLatch()
	for i := 0; i < 6; i++ {
		if n := d.Update(true); n != nil {
			t.Fatalf("round %d re-announced the outage: %+v", i, n)
		}
	}
}

// TestTotalLossTitlesAreTheExportedJoinKeys guards the constants an auditor
// matches on: a renamed literal would silently orphan every past window.
func TestTotalLossTitlesAreTheExportedJoinKeys(t *testing.T) {
	d := NewTotalLossDetector()
	var outage *Notification
	for i := 0; i < totalLossConfirmDown; i++ {
		outage = d.Update(true)
	}
	if outage == nil || outage.Title != TitleTotalLoss {
		t.Fatalf("outage = %+v, want Title == TitleTotalLoss", outage)
	}
}
