package monitor

import "testing"

func TestFailureTrackerAlertsAfterThreshold(t *testing.T) {
	f := NewFailureTracker(5)

	// First 5 failures: under/at threshold, no alert (threshold is "more than 5").
	for i := 1; i <= 5; i++ {
		if n := f.Record("openai", "OpenAI", false); n != nil {
			t.Fatalf("failure #%d should not alert (>5 required), got %+v", i, n)
		}
	}

	// 6th consecutive failure exceeds the threshold -> alert once.
	n := f.Record("openai", "OpenAI", false)
	if n == nil {
		t.Fatal("6th consecutive failure should alert")
	}
	if n.Title != "Check failing" {
		t.Errorf("alert title = %q", n.Title)
	}

	// Further failures in the same streak are debounced.
	for i := 0; i < 3; i++ {
		if n := f.Record("openai", "OpenAI", false); n != nil {
			t.Fatalf("continued failures should be debounced, got %+v", n)
		}
	}
}

func TestFailureTrackerRecovery(t *testing.T) {
	f := NewFailureTracker(5)
	for i := 0; i < 6; i++ {
		f.Record("ping", "1.1.1.1", false)
	}
	// Success after an alert -> recovery notification.
	n := f.Record("ping", "1.1.1.1", true)
	if n == nil || n.Title != "Check recovered" || !n.Recovery {
		t.Fatalf("expected recovery notification marked Recovery, got %+v", n)
	}
	// A second success does not re-notify.
	if n := f.Record("ping", "1.1.1.1", true); n != nil {
		t.Fatalf("steady success should not notify, got %+v", n)
	}
}

func TestFailureTrackerNoRecoveryWithoutAlert(t *testing.T) {
	f := NewFailureTracker(5)
	// A few failures below threshold then success: no recovery (never alerted).
	f.Record("x", "X", false)
	f.Record("x", "X", false)
	if n := f.Record("x", "X", true); n != nil {
		t.Fatalf("success without prior alert should not notify, got %+v", n)
	}
}

func TestFailureTrackerStreakResetsBetweenAlerts(t *testing.T) {
	f := NewFailureTracker(5)
	for i := 0; i < 6; i++ {
		f.Record("x", "X", false)
	}
	if n := f.Record("x", "X", true); n == nil { // recovery, resets streak
		t.Fatal("expected recovery")
	}
	// New streak must again require >5 failures before alerting.
	for i := 1; i <= 5; i++ {
		if n := f.Record("x", "X", false); n != nil {
			t.Fatalf("new streak failure #%d should not alert, got %+v", i, n)
		}
	}
	if n := f.Record("x", "X", false); n == nil {
		t.Fatal("6th failure of new streak should alert again")
	}
}

func TestFailureTrackerPerCheckIndependent(t *testing.T) {
	f := NewFailureTracker(5)
	for i := 0; i < 6; i++ {
		f.Record("a", "A", false)
	}
	// "b" has its own independent streak.
	if n := f.Record("b", "B", false); n != nil {
		t.Fatalf("first failure of b should not alert, got %+v", n)
	}
}

// TestFailureTrackerCustomTargetPath mirrors how the connectivity loop records
// a custom connectivity target (a dead IP added by the user): the tracker keys
// on the custom check ID, so failures are counted and a repeated-failure alert
// fires after more than 5 consecutive failures, with recovery on success.
func TestFailureTrackerCustomTargetPath(t *testing.T) {
	f := NewFailureTracker(DefaultFailureThreshold)
	id := CustomTargetID("203.0.113.7") // a custom target's check ID
	name := "203.0.113.7"

	for i := 1; i <= 5; i++ {
		if n := f.Record(id, name, false); n != nil {
			t.Fatalf("custom-target failure #%d should not alert yet, got %+v", i, n)
		}
	}
	n := f.Record(id, name, false)
	if n == nil {
		t.Fatal("custom target should alert after >5 consecutive failures")
	}
	if n.Body == "" || n.Title != "Check failing" {
		t.Errorf("unexpected alert %+v", n)
	}

	// Recovery when the custom target responds again.
	if n := f.Record(id, name, true); n == nil || n.Title != "Check recovered" {
		t.Fatalf("expected recovery for custom target, got %+v", n)
	}
}

func TestFailureTrackerReset(t *testing.T) {
	f := NewFailureTracker(5)
	for i := 0; i < 6; i++ {
		f.Record("x", "X", false)
	}
	f.Reset("x")
	// After reset, a success must not emit a recovery (state cleared).
	if n := f.Record("x", "X", true); n != nil {
		t.Fatalf("success after reset should not notify, got %+v", n)
	}
}
