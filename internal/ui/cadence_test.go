package ui

import (
	"testing"
	"time"

	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/history"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/monitor"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/providers"
)

// TestRoundIsMintedOnTheCadenceNotOnTheWake pins the decision that makes a
// failure run count rounds and not clicks: a wake-up before the round is due
// (a "Refresh now") polls inside the round in progress, a wake-up at or after
// it starts the next one, and a round that outlasted the interval is due at
// once — never a fresh interval after the late wake, which is the drift that
// let clicks closer together than one interval stall minting altogether.
func TestRoundIsMintedOnTheCadenceNotOnTheWake(t *testing.T) {
	lastMint := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	interval := 30 * time.Second
	due := roundDue(lastMint, interval)
	if !due.Equal(lastMint.Add(interval)) {
		t.Fatalf("roundDue = %v, want one interval after the last mint", due)
	}
	cases := []struct {
		name string
		now  time.Time
		mint bool
	}{
		{"click 5s in", lastMint.Add(5 * time.Second), false},
		{"click 29s in", lastMint.Add(29 * time.Second), false},
		{"the tick", due, true},
		{"woke late after a slow round", lastMint.Add(46 * time.Second), true},
	}
	for _, tc := range cases {
		if got := mintRound(due, tc.now); got != tc.mint {
			t.Errorf("%s: mintRound = %v, want %v", tc.name, got, tc.mint)
		}
	}
	// Two clicks inside one interval do not move the due time: it is derived
	// from the last MINT, and the loop recomputes it from the same value.
	if again := roundDue(lastMint, interval); !again.Equal(due) {
		t.Errorf("due moved to %v across wakes with no mint; want %v", again, due)
	}
}

// TestARetryIsFiledLikeAScheduledPoll pins the one post-poll path. A manual
// retry used to write the coverage record and nothing else, so a feed the user
// had just watched come back was "read" on the attendance record while the
// uptime strip still had no sample for it — two of the audit's own witnesses
// disagreeing about one observation.
func TestARetryIsFiledLikeAScheduledPoll(t *testing.T) {
	c := &Controller{
		provStates: map[string]provState{},
		history:    history.New(10),
		incidents:  history.NewIncidentLog(),
		outages:    monitor.NewOutageTracker(),
	}
	p := providers.Provider{ID: "p", Name: "P"}
	at := time.Now()
	st := provState{
		checked: true, when: at, latency: 20 * time.Millisecond, attempts: 1,
		res: providers.Result{Feed: providers.FeedReachable}, outcome: history.OutcomeReadable,
	}
	// Exactly what recheckProvider passes: not blind, not cancelled, round 4.
	c.recordPoll(p, st, nil, false, false, 4, time.Minute)

	if _, n := c.history.Last("p"); n != 1 {
		t.Errorf("samples after a retry = %d, want 1 — the strip must see what the record saw", n)
	}
	cov, ok := c.history.Coverage("p")
	if !ok || !cov.LastSuccess.Equal(at) || cov.LastRound != 4 {
		t.Errorf("coverage after a retry = %+v (ok %v), want LastSuccess %v in round 4", cov, ok, at)
	}
	if got := c.provStates["p"]; !got.lastOK.Equal(at) {
		t.Errorf("provStates lastOK = %v, want %v (carryLastOK ran)", got.lastOK, at)
	}
	if !c.lastRoundAnyOK {
		t.Error("a retry that read its feed is proof of a link and must say so")
	}
}
