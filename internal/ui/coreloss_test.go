package ui

import (
	"testing"

	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/monitor"
)

// TestBothDefaultsDown pins the consolidated partial-loss primitive: a round is
// "lost" ONLY when BOTH built-in default resolvers were probed and neither
// answered. One resolver alone (a transient remote-path blip) must not count,
// and a missing/disabled default makes the signal UNAVAILABLE (judged=false) so
// the caller resets rather than guesses.
func TestBothDefaultsDown(t *testing.T) {
	cf := monitor.Target{ID: "ping-cloudflare", Name: "Cloudflare", Host: "1.1.1.1"}
	gg := monitor.Target{ID: "ping-google", Name: "Google", Host: "8.8.8.8"}
	c := &Controller{builtinTargets: []monitor.Target{cf, gg}}

	st := func(tg monitor.Target, reachable bool) monitor.TargetStatus {
		return monitor.TargetStatus{Target: tg, Reachable: reachable}
	}

	cases := []struct {
		name       string
		byID       map[string]monitor.TargetStatus
		wantDown   bool
		wantJudged bool
	}{
		{
			name:       "both down -> lost round",
			byID:       map[string]monitor.TargetStatus{cf.ID: st(cf, false), gg.ID: st(gg, false)},
			wantDown:   true,
			wantJudged: true,
		},
		{
			name:       "only google down -> not a lost round (transient)",
			byID:       map[string]monitor.TargetStatus{cf.ID: st(cf, true), gg.ID: st(gg, false)},
			wantDown:   false,
			wantJudged: true,
		},
		{
			name:       "only cloudflare down -> not a lost round (transient)",
			byID:       map[string]monitor.TargetStatus{cf.ID: st(cf, false), gg.ID: st(gg, true)},
			wantDown:   false,
			wantJudged: true,
		},
		{
			name:       "both up -> healthy judged round",
			byID:       map[string]monitor.TargetStatus{cf.ID: st(cf, true), gg.ID: st(gg, true)},
			wantDown:   false,
			wantJudged: true,
		},
		{
			name:       "a default missing this round -> signal unavailable",
			byID:       map[string]monitor.TargetStatus{cf.ID: st(cf, false)},
			wantDown:   false,
			wantJudged: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			down, judged := c.bothDefaultsDown(tc.byID)
			if down != tc.wantDown || judged != tc.wantJudged {
				t.Errorf("bothDefaultsDown = (down=%v, judged=%v), want (down=%v, judged=%v)",
					down, judged, tc.wantDown, tc.wantJudged)
			}
		})
	}
}

// TestBothDefaultsDownFewerThanTwo: with fewer than two built-in defaults the
// signal can't be judged at all.
func TestBothDefaultsDownFewerThanTwo(t *testing.T) {
	only := monitor.Target{ID: "ping-cloudflare", Host: "1.1.1.1"}
	c := &Controller{builtinTargets: []monitor.Target{only}}
	if _, judged := c.bothDefaultsDown(map[string]monitor.TargetStatus{
		only.ID: {Target: only, Reachable: false},
	}); judged {
		t.Error("judged should be false with fewer than two default targets")
	}
}
