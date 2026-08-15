package monitor

import "testing"

func TestEvaluateOffline(t *testing.T) {
	cases := []struct {
		name        string
		connDown    bool
		checked     int
		unreachable int
		want        bool
	}{
		{"connectivity down, no feed evidence => offline", true, 0, 0, true},
		// Reversed deliberately. A fetched-and-parsed status page is a first-hand
		// observation that the internet works; connDown is an inference from ICMP
		// and TCP:443 probes to two fixed IPs. On a network that blocks direct
		// egress but allows HTTPS through a proxy they disagree forever, and the
		// inference used to win: the offline banner sat on screen, checkTotalLoss
		// stayed gated, and every provider outage alert was journaled instead of
		// shown — while the rows those same feeds produced turned red.
		{"a reachable feed outranks failing probes", true, 5, 0, false},
		{"one reachable feed out of many still outranks", true, 5, 4, false},
		{"probes down and every feed unreachable => offline", true, 5, 5, true},
		{"all feeds down with enough providers => offline", false, 3, 3, true},
		{"all feeds down exactly at threshold", false, 2, 2, true},
		{"single provider down is NOT offline", false, 1, 1, false},
		{"some feeds down is not offline", false, 4, 2, false},
		{"no providers, conn up => online", false, 0, 0, false},
		{"all up => online", false, 5, 0, false},
	}
	for _, tc := range cases {
		if got := Evaluate(tc.connDown, tc.checked, tc.unreachable); got != tc.want {
			t.Errorf("%s: Evaluate(%v,%d,%d) = %v, want %v",
				tc.name, tc.connDown, tc.checked, tc.unreachable, got, tc.want)
		}
	}
}

func TestOfflineDetectorTransitions(t *testing.T) {
	d := NewOfflineDetector()

	// Start online: first observation, no change reported.
	if off, changed := d.Update(false, 3, 0); off || changed {
		t.Errorf("initial online: got off=%v changed=%v, want false,false", off, changed)
	}
	// Still online: no change.
	if off, changed := d.Update(false, 3, 0); off || changed {
		t.Errorf("steady online: got off=%v changed=%v", off, changed)
	}
	// Go offline: probes down AND every feed unreachable, i.e. no contrary
	// evidence. (Probes alone no longer suffice while a feed still loads — see
	// TestEvaluateOffline.)
	if off, changed := d.Update(true, 3, 3); !off || !changed {
		t.Errorf("going offline: got off=%v changed=%v, want true,true", off, changed)
	}
	// Stay offline: no further change.
	if off, changed := d.Update(true, 3, 3); !off || changed {
		t.Errorf("steady offline: got off=%v changed=%v, want true,false", off, changed)
	}
	// Recover: change reported.
	if off, changed := d.Update(false, 3, 0); off || !changed {
		t.Errorf("recovery: got off=%v changed=%v, want false,true", off, changed)
	}
	if d.Offline() {
		t.Error("Offline() should be false after recovery")
	}
}

func TestOfflineDetectorStartsOffline(t *testing.T) {
	d := NewOfflineDetector()
	if off, changed := d.Update(true, 0, 0); !off || !changed {
		t.Errorf("launching offline should report changed once: off=%v changed=%v", off, changed)
	}
}
