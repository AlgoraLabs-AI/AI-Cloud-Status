package ui

import (
	"testing"
	"time"

	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/history"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/providers"
)

func noMutes(string) bool { return false }

// TestUptimeSortAgreesWithWhatIsDisplayed is the regression for a table that
// ranked rows by a number it did not show.
//
// regionUptime (which feeds the SORT) anchored its window to the newest sample;
// uptimeDisplay (the percentage in the cell) anchors to now. After a gap longer
// than the window — a check re-enabled after two days, or a laptop resumed from
// a long suspend — the now-anchored window is empty and the cell reads "—",
// while the sample-anchored one still finds a full window and ranks the row by
// a stale fraction. With the table sorted by Uptime, a row showing no value at
// all floated to the top of its section on the strength of an old outage.
func TestUptimeSortAgreesWithWhatIsDisplayed(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	h := history.New(0)
	c := &Controller{history: h}

	// A run of samples that ended two days ago: entirely outside a 24h window
	// anchored to now, entirely inside one anchored to the newest sample.
	stale := now.Add(-48 * time.Hour)
	for i := range 20 {
		h.Add("provider", history.Sample{Time: stale.Add(time.Duration(i) * time.Minute), Up: false})
	}

	_, pct := c.uptimeDisplay("provider", 24*time.Hour, true, noMutes, now)
	_, n := c.regionUptime("provider", 24*time.Hour, true, noMutes, now)

	if pct == "—" && n > 0 {
		t.Errorf("the cell shows %q but the sort ranks the row on %d samples — they disagree", pct, n)
	}
	if pct != "—" && n == 0 {
		t.Errorf("the cell shows %q but the sort sees no samples", pct)
	}
}

// TestUptimeAgreesForFreshData is the counterweight: with data in range both
// must see it, and agree on the value.
func TestUptimeAgreesForFreshData(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	h := history.New(0)
	c := &Controller{history: h}

	// 10 samples in the last hour, 3 of them down → 70%.
	for i := range 10 {
		h.Add("provider", history.Sample{
			Time: now.Add(-time.Hour + time.Duration(i)*time.Minute),
			Up:   i >= 3,
		})
	}

	_, pct := c.uptimeDisplay("provider", 24*time.Hour, true, noMutes, now)
	frac, n := c.regionUptime("provider", 24*time.Hour, true, noMutes, now)

	if n != 10 {
		t.Errorf("sort saw %d samples, want 10", n)
	}
	if pct != "70%" {
		t.Errorf("displayed %q, want 70%%", pct)
	}
	if frac < 0.69 || frac > 0.71 {
		t.Errorf("sort fraction = %v, want ~0.70 — the same number the cell shows", frac)
	}
}

// TestConnectivityUptimeStillAnchorsToNewestSample pins the other side of the
// rule: connectivity rows use a short rolling window anchored to the latest
// sample, not to wall clock, so a brief pause in probing must not blank them.
func TestConnectivityUptimeStillAnchorsToNewestSample(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	h := history.New(0)
	c := &Controller{history: h}

	last := now.Add(-30 * time.Minute)
	for i := range 10 {
		h.Add("ping", history.Sample{Time: last.Add(time.Duration(i) * time.Second), Up: true})
	}

	_, n := c.regionUptime("ping", 5*time.Minute, false, noMutes, now)
	if n == 0 {
		t.Error("a connectivity row lost its uptime because probing paused briefly")
	}
}

// TestStaleRuleIsSharedWithTheAdapters pins that the drill-down split and the
// adapters' demotion now decide staleness with the same rule. Three hand-written
// copies of a 15-day horizon, each choosing its own timestamp, meant a row and
// its own drill-down could disagree about the same incident.
func TestStaleRuleIsSharedWithTheAdapters(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	old := now.Add(-providers.StaleAfter - time.Hour)
	fresh := now.Add(-time.Hour)

	incs := []providers.Incident{
		{Summary: "zombie", Updated: old, Started: old},
		{Summary: "live", Updated: fresh, Started: old}, // old start, recent update
		{Summary: "no timestamps at all"},               // must fail open
		{Summary: "started only, old", Started: old},    // falls back to Started
	}
	live, stale := splitStaleIncidents(incs, now)

	if len(stale) != 2 || stale[0].Summary != "zombie" || stale[1].Summary != "started only, old" {
		t.Errorf("stale = %v, want the zombie and the old start-only incident", names(stale))
	}
	if len(live) != 2 || live[0].Summary != "live" {
		t.Errorf("live = %v, want the actively-updated one and the timestamp-less one", names(live))
	}
	// And the same verdict from the shared rule the adapters use.
	for _, inc := range incs {
		want := inc.Summary == "zombie" || inc.Summary == "started only, old"
		if got := inc.IsStale(now); got != want {
			t.Errorf("Incident(%q).IsStale = %v, want %v", inc.Summary, got, want)
		}
	}
}

func names(incs []providers.Incident) []string {
	out := make([]string, len(incs))
	for i, inc := range incs {
		out[i] = inc.Summary
	}
	return out
}
