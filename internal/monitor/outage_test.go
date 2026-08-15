package monitor

import "testing"

func TestOutageTrackerTransitions(t *testing.T) {
	o := NewOutageTracker()

	// First healthy observation is silent (no spurious recovery).
	if n := o.Update("openai", "OpenAI", "", false); n != nil {
		t.Fatalf("first healthy observation alerted: %+v", n)
	}
	// Transition into outage alerts exactly once, naming the incident.
	n := o.Update("openai", "OpenAI", "Elevated error rates", true)
	if n == nil || n.Title != "Provider outage" || n.Recovery {
		t.Fatalf("transition to down = %+v, want a non-recovery 'Provider outage' alert", n)
	}
	if n.Body != "OpenAI: Elevated error rates" {
		t.Errorf("outage body = %q, want the incident summary named", n.Body)
	}
	// Steady outage is debounced.
	if n := o.Update("openai", "OpenAI", "Elevated error rates", true); n != nil {
		t.Errorf("steady outage alerted again: %+v", n)
	}
	// A single healthy poll is not enough to call it recovered yet.
	if n := o.Update("openai", "OpenAI", "", false); n != nil {
		t.Fatalf("one healthy poll recovered early: %+v", n)
	}
	// Second consecutive healthy poll confirms the recovery, naming what resolved.
	n = o.Update("openai", "OpenAI", "", false)
	if n == nil || n.Title != "Provider recovered" || !n.Recovery {
		t.Fatalf("transition to up = %+v, want a 'Provider recovered' alert marked Recovery", n)
	}
	if n.Body != `OpenAI is operational again — "Elevated error rates" was resolved.` {
		t.Errorf("recovery body = %q, want the resolved incident named", n.Body)
	}
	// Steady healthy is debounced.
	if n := o.Update("openai", "OpenAI", "", false); n != nil {
		t.Errorf("steady healthy alerted: %+v", n)
	}
}

// TestOutageTrackerFlapSuppressed verifies the exact failure mode found in the
// 2026-07-29 Anthropic incident: one healthy poll in the middle of a
// continuous outage (a feed hiccup) must not fire a spurious recovery, and the
// outage must not be re-announced when it reappears the very next poll.
func TestOutageTrackerFlapSuppressed(t *testing.T) {
	o := NewOutageTracker()
	if n := o.Update("anthropic", "Anthropic", "Elevated errors", true); n == nil {
		t.Fatal("initial outage should alert")
	}
	if n := o.Update("anthropic", "Anthropic", "Elevated errors", true); n != nil {
		t.Fatalf("steady outage alerted again: %+v", n)
	}
	// The blip: one healthy poll.
	if n := o.Update("anthropic", "Anthropic", "", false); n != nil {
		t.Fatalf("single-poll blip fired a notification: %+v", n)
	}
	// The incident reappears the next poll — must not re-announce the outage,
	// and the recovery streak must have been cancelled by the down poll.
	if n := o.Update("anthropic", "Anthropic", "Elevated errors", true); n != nil {
		t.Fatalf("reappearing outage re-announced: %+v", n)
	}
	if n := o.Update("anthropic", "Anthropic", "", false); n != nil {
		t.Fatalf("recovery streak should have reset to 1, not fired yet: %+v", n)
	}
	n := o.Update("anthropic", "Anthropic", "", false)
	if n == nil || !n.Recovery {
		t.Fatalf("two genuine consecutive healthy polls should recover: %+v", n)
	}
}

// TestOutageTrackerFirstObservationDown verifies an already-down provider on the
// very first observation still alerts (it is a real, actionable outage).
func TestOutageTrackerFirstObservationDown(t *testing.T) {
	o := NewOutageTracker()
	if n := o.Update("aws", "AWS", "", true); n == nil {
		t.Error("first observation of a down provider should alert")
	}
}

func TestOutageTrackerReset(t *testing.T) {
	o := NewOutageTracker()
	o.Update("aws", "AWS", "", true) // latched down
	o.Reset("aws")
	// After reset the tracker forgot the down state, so the next down is a fresh
	// transition that alerts again.
	if n := o.Update("aws", "AWS", "", true); n == nil {
		t.Error("after Reset a re-observed outage should alert")
	}
}
