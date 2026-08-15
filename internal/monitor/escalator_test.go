package monitor

import (
	"testing"
	"time"
)

func TestLossEscalatorBackoffAndRecovery(t *testing.T) {
	e := NewLossEscalator()
	base := time.Unix(1_700_000_000, 0)

	// Below the enter threshold → no notification.
	if n, _ := e.Update(10, true, base); n != nil {
		t.Fatalf("10%% loss (below enter) should not notify, got %v", n)
	}

	// Loss crosses the enter threshold (warmed up) → immediate alert, first=true.
	n, first := e.Update(25, true, base)
	if n == nil || n.Title != "You're losing packets" || !first {
		t.Fatalf("expected losing-packets first alert, got note=%v first=%v", n, first)
	}

	// Hysteresis: loss between exit(5) and enter(15) while active → stays active,
	// no recovery, no premature re-notify before the backoff.
	if n, _ := e.Update(10, true, base.Add(2*time.Second)); n != nil {
		t.Fatalf("loss in the hysteresis band should not recover or re-notify, got %v", n)
	}

	// At 10s → re-notify as a reminder (first=false).
	n, first = e.Update(20, true, base.Add(10*time.Second))
	if n == nil || first {
		t.Fatalf("should re-notify at 10s as a reminder, got note=%v first=%v", n, first)
	}
	// Next gap is 30s (not cumulative): +20s no, +40s yes.
	if n, _ := e.Update(20, true, base.Add(20*time.Second)); n != nil {
		t.Fatalf("should not re-notify before the 30s gap, got %v", n)
	}
	if n, _ := e.Update(20, true, base.Add(40*time.Second)); n == nil {
		t.Fatal("should re-notify after the 30s gap")
	}

	// Loss clears below the exit threshold → recovery, then silence.
	if n, _ := e.Update(0, true, base.Add(time.Hour)); n == nil || n.Title != "Internet recovered" || !n.Recovery {
		t.Fatalf("expected recovery alert marked Recovery when loss clears, got %v", n)
	}
	if n, _ := e.Update(0, true, base.Add(2*time.Hour)); n != nil {
		t.Fatalf("no notification when not losing, got %v", n)
	}
}

// TestLossEscalatorWarmupGate: even high loss must NOT alert until ready (enough
// samples) — this kills the bogus "losing packets" right after a blackout reset.
func TestLossEscalatorWarmupGate(t *testing.T) {
	e := NewLossEscalator()
	base := time.Unix(1_700_000_000, 0)
	if n, _ := e.Update(95, false, base); n != nil {
		t.Fatalf("95%% loss while NOT warmed up should not alert, got %v", n)
	}
	if n, first := e.Update(95, true, base.Add(time.Second)); n == nil || !first {
		t.Fatal("once warmed up, high loss should alert")
	}
}

// TestLossEscalatorThresholdBoundaries pins the exact comparators so an off-by-one
// (>= vs >, <= vs <) can't slip through: enter is strictly > LossEnterPercent and
// exit is strictly < LossExitPercent, so the threshold values themselves are inert.
func TestLossEscalatorThresholdBoundaries(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)

	// Exactly at the enter threshold must NOT start an episode.
	e := NewLossEscalator()
	if n, _ := e.Update(LossEnterPercent, true, base); n != nil {
		t.Fatalf("loss == LossEnterPercent (%.0f) should not enter, got %v", LossEnterPercent, n)
	}

	// Once active, exactly at the exit threshold must NOT recover.
	e2 := NewLossEscalator()
	if n, first := e2.Update(LossEnterPercent+1, true, base); n == nil || !first {
		t.Fatal("expected to enter just above the threshold")
	}
	if n, _ := e2.Update(LossExitPercent, true, base.Add(2*time.Second)); n != nil {
		t.Fatalf("loss == LossExitPercent (%.0f) should not recover, got %v", LossExitPercent, n)
	}
}

func TestLossEscalatorHoldsAtTwoMinutes(t *testing.T) {
	e := NewLossEscalator()
	base := time.Unix(1_700_000_000, 0)
	at := base
	e.Update(40, true, at) // first detection
	gaps := []time.Duration{10, 30, 60, 120, 120, 120}
	for i, g := range gaps {
		at = at.Add(g * time.Second)
		if n, _ := e.Update(40, true, at); n == nil {
			t.Fatalf("expected reminder at step %d (gap %ds)", i, g)
		}
	}
}

func TestLossEscalatorResetReArms(t *testing.T) {
	e := NewLossEscalator()
	base := time.Unix(1_700_000_000, 0)
	if n, first := e.Update(40, true, base); n == nil || !first {
		t.Fatal("expected initial alert with first=true")
	}
	e.Reset() // total-loss takes over
	if n, first := e.Update(40, true, base.Add(time.Second)); n == nil || !first {
		t.Fatal("after Reset, loss should re-arm and notify immediately as first")
	}
}
