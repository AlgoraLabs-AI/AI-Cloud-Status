package monitor

import "testing"

func TestAlertStateDebounce(t *testing.T) {
	var a AlertState

	// Steady OK from the start should never alert.
	if n := a.Transition(ConnOK); n != nil {
		t.Fatalf("first OK should not alert, got %+v", n)
	}
	if n := a.Transition(ConnOK); n != nil {
		t.Fatalf("repeated OK should not alert, got %+v", n)
	}

	// Entering degraded alerts once.
	n := a.Transition(ConnDegraded)
	if n == nil {
		t.Fatal("entering degraded should alert")
	}
	// Staying degraded does not re-alert.
	if n := a.Transition(ConnDegraded); n != nil {
		t.Fatalf("staying degraded should not re-alert, got %+v", n)
	}

	// Worsening to down alerts again.
	if n := a.Transition(ConnDown); n == nil {
		t.Fatal("worsening to down should alert")
	}

	// Recovery alerts with a restored message.
	n = a.Transition(ConnOK)
	if n == nil {
		t.Fatal("recovery should alert")
	}
	if n.Title != "Internet restored" {
		t.Errorf("recovery title = %q, want %q", n.Title, "Internet restored")
	}
}

func TestAlertStateFirstObservationBad(t *testing.T) {
	var a AlertState
	// If the very first observation is already bad, alert immediately.
	if n := a.Transition(ConnDown); n == nil {
		t.Fatal("first observation in down state should alert")
	}
	if a.Level() != ConnDown {
		t.Errorf("level = %v, want ConnDown", a.Level())
	}
}

func TestAlertStateNoFlapWithinSameLevel(t *testing.T) {
	var a AlertState
	_ = a.Transition(ConnBad) // first bad -> alert
	count := 0
	for i := 0; i < 5; i++ {
		if a.Transition(ConnBad) != nil {
			count++
		}
	}
	if count != 0 {
		t.Errorf("got %d extra alerts staying in same level, want 0", count)
	}
}
