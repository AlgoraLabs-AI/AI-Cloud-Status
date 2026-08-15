package audit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/alertlog"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/config"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/history"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/monitor"
)

const fixtureID = "url-1365c6b0"

// stage writes a config, an alert log and a sample history into a temp dir.
func stage(t *testing.T, entries []alertlog.Entry, samples []history.Sample) string {
	t.Helper()
	dir := t.TempDir()

	cfg := config.Default()
	cfg.CustomURLChecks = []config.URLCheck{
		{ID: fixtureID, Name: "portaldev", URL: "https://example.com/health"},
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	log := alertlog.Open(filepath.Join(dir, "alert-log.jsonl"))
	for _, e := range entries {
		log.Record(e)
	}

	h := history.New(history.DefaultCapacity)
	for _, s := range samples {
		h.Add(fixtureID, s)
	}
	if err := h.Save(filepath.Join(dir, "history.json")); err != nil {
		t.Fatal(err)
	}
	return dir
}

// series builds a 60s-cadence run: healthyN good polls, then downN failed ones,
// then a healthy tail when recovers is set.
func series(start time.Time, healthyN, downN int, recovers bool) []history.Sample {
	var out []history.Sample
	at := start
	for i := 0; i < healthyN; i++ {
		out = append(out, history.Sample{Time: at, Up: true, Responded: true, Latency: 400 * time.Millisecond})
		at = at.Add(time.Minute)
	}
	for i := 0; i < downN; i++ {
		out = append(out, history.Sample{Time: at})
		at = at.Add(time.Minute)
	}
	if recovers {
		for i := 0; i < 3; i++ {
			out = append(out, history.Sample{Time: at, Up: true, Responded: true, Latency: 400 * time.Millisecond})
			at = at.Add(time.Minute)
		}
	}
	return out
}

// TestContinuityCatchesTheUnclosedWindow is THE regression: the 2026-08-02
// shape. An outage alert with no recovery, while the sample history plainly
// shows the check healthy again. The provider audit answered "no missing
// recoveries" for this, because custom URL checks have no capture archive.
func TestContinuityCatchesTheUnclosedWindow(t *testing.T) {
	start := time.Now().Add(-3 * time.Hour)
	down := start.Add(10 * time.Minute)
	dir := stage(t,
		[]alertlog.Entry{{
			Time: down, ID: fixtureID, Title: monitor.TitleProviderOutage,
			Body: "portaldev: request failed",
		}},
		series(start, 10, 2, true),
	)

	rep, err := RunContinuity(dir)
	if err != nil {
		t.Fatal(err)
	}
	un := rep.Unclosed()
	if len(un) != 1 {
		t.Fatalf("Unclosed = %d windows, want 1: %+v", len(un), rep.Windows)
	}
	if un[0].ID != fixtureID {
		t.Errorf("finding names %q, want %q", un[0].ID, fixtureID)
	}
	if un[0].RecoveredAt.IsZero() {
		t.Error("the finding does not say when the samples showed it healthy again")
	}
}

// TestContinuityCleanWhenPaired pins the negative: add the recovery line and the
// same data is clean.
func TestContinuityCleanWhenPaired(t *testing.T) {
	start := time.Now().Add(-3 * time.Hour)
	down := start.Add(10 * time.Minute)
	dir := stage(t, []alertlog.Entry{
		{Time: down, ID: fixtureID, Title: monitor.TitleProviderOutage},
		{Time: down.Add(2 * time.Minute), ID: fixtureID, Title: monitor.TitleProviderRecovered, Recovery: true},
	}, series(start, 10, 2, true))

	rep, err := RunContinuity(dir)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(rep.Unclosed()); n != 0 {
		t.Errorf("Unclosed = %d, want 0 for a properly paired window: %+v", n, rep.Windows)
	}
}

// TestContinuityIgnoresAnOngoingOutage pins that a check still down is not a
// defect — an open window is the correct state.
func TestContinuityIgnoresAnOngoingOutage(t *testing.T) {
	start := time.Now().Add(-30 * time.Minute)
	down := start.Add(10 * time.Minute)
	dir := stage(t,
		[]alertlog.Entry{{Time: down, ID: fixtureID, Title: monitor.TitleProviderOutage}},
		series(start, 10, 5, false), // never recovers, and the tail is fresh
	)
	rep, err := RunContinuity(dir)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(rep.Unclosed()); n != 0 {
		t.Errorf("Unclosed = %d, want 0 while the check is still down: %+v", n, rep.Windows)
	}
}

// TestContinuityIgnoresAnUnobservedRecovery pins the biggest false-positive
// class: the samples simply STOP (the app was closed, or the check disabled
// mid-outage), so nothing proves it ever came back.
func TestContinuityIgnoresAnUnobservedRecovery(t *testing.T) {
	start := time.Now().Add(-8 * time.Hour)
	down := start.Add(10 * time.Minute)
	dir := stage(t,
		[]alertlog.Entry{{Time: down, ID: fixtureID, Title: monitor.TitleProviderOutage}},
		series(start, 10, 3, false), // stops while down, hours ago
	)
	rep, err := RunContinuity(dir)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(rep.Unclosed()); n != 0 {
		t.Errorf("Unclosed = %d, want 0 when the recovery was never observed: %+v", n, rep.Windows)
	}
}

// TestContinuityIgnoresATruncatedRun pins that a run starting at the ring's
// oldest sample cannot be attributed to an alert — its real start may predate
// everything retained.
func TestContinuityIgnoresATruncatedRun(t *testing.T) {
	start := time.Now().Add(-3 * time.Hour)
	dir := stage(t,
		[]alertlog.Entry{{Time: start, ID: fixtureID, Title: monitor.TitleProviderOutage}},
		series(start, 0, 3, true), // opens mid-outage: nothing healthy precedes it
	)
	rep, err := RunContinuity(dir)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(rep.Unclosed()); n != 0 {
		t.Errorf("Unclosed = %d, want 0 for a truncated run: %+v", n, rep.Windows)
	}
}

// TestContinuityDropsDeletedChecks pins that a check no longer in the config is
// not audited: it leaves an orphan series and an unclosable window, and blaming
// the app for a check the user removed is noise.
func TestContinuityDropsDeletedChecks(t *testing.T) {
	start := time.Now().Add(-3 * time.Hour)
	dir := stage(t,
		[]alertlog.Entry{{
			Time: start.Add(10 * time.Minute), ID: "url-deleted", Title: monitor.TitleProviderOutage,
		}},
		series(start, 10, 2, true),
	)
	rep, err := RunContinuity(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range rep.Windows {
		if w.ID == "url-deleted" {
			t.Errorf("a deleted check was audited: %+v", w)
		}
	}
}

// TestContinuityCountsStaleClosures pins that the deliberate restart-closes are
// counted and reported rather than silently swallowed — a rising number is the
// symptom that restarts are eating recoveries.
func TestContinuityCountsStaleClosures(t *testing.T) {
	start := time.Now().Add(-3 * time.Hour)
	dir := stage(t, []alertlog.Entry{
		{Time: start, ID: fixtureID, Title: monitor.TitleProviderOutage},
		{Time: start.Add(time.Minute), ID: fixtureID, Title: monitor.TitleProviderRecovered,
			Recovery: true, Suppressed: "stale-across-restart"},
	}, series(start, 10, 2, true))

	rep, err := RunContinuity(dir)
	if err != nil {
		t.Fatal(err)
	}
	if rep.StaleClosures != 1 {
		t.Errorf("StaleClosures = %d, want 1", rep.StaleClosures)
	}
}

// TestContinuityConnectivityIsNeverAssertedFromSamples pins the honesty bound on
// the app-wide alerts: the ping ring holds roughly ten minutes, so the audit
// reports the open window but never claims to have proved it.
func TestContinuityConnectivityIsNeverAssertedFromSamples(t *testing.T) {
	start := time.Now().Add(-2 * time.Hour)
	dir := stage(t,
		[]alertlog.Entry{{Time: start, Title: monitor.TitleTotalLoss, Body: "no internet"}},
		series(start, 10, 2, true),
	)
	rep, err := RunContinuity(dir)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, w := range rep.Windows {
		if w.ID == "" {
			found = true
			if w.Outcome == ContinuityUnclosed {
				t.Error("a connectivity window was asserted as a defect from samples that cannot cover it")
			}
		}
	}
	if !found {
		t.Error("the connectivity window was not reported at all")
	}
	if n := len(rep.Unclosed()); n != 0 {
		t.Errorf("Unclosed = %d, want 0 — connectivity is journal-only evidence", n)
	}
}
