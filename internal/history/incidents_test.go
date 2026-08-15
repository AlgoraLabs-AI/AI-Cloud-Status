package history

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func entry(summary, url string, sev int) IncidentEntry {
	return IncidentEntry{Summary: summary, URL: url, Severity: sev}
}

// TestObserveDedupes verifies a re-observed incident updates its existing entry
// (LastSeen, latest summary, worst severity) instead of appending a duplicate.
func TestObserveDedupes(t *testing.T) {
	l := NewIncidentLog()
	t0 := time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC)
	t1 := t0.Add(30 * time.Minute)

	l.Observe("p", []IncidentEntry{entry("Elevated errors", "https://x/inc/1", 1)}, t0)
	l.Observe("p", []IncidentEntry{entry("Elevated errors (edited)", "https://x/inc/1", 2)}, t1)

	got := l.Recent("p", t0.Add(-time.Hour))
	if len(got) != 1 {
		t.Fatalf("Recent = %d entries, want 1", len(got))
	}
	e := got[0]
	if e.Summary != "Elevated errors (edited)" {
		t.Errorf("Summary = %q, want the latest observed", e.Summary)
	}
	if e.Severity != 2 {
		t.Errorf("Severity = %d, want the worst observed (2)", e.Severity)
	}
	if !e.FirstSeen.Equal(t0) || !e.LastSeen.Equal(t1) {
		t.Errorf("FirstSeen/LastSeen = %v/%v, want %v/%v", e.FirstSeen, e.LastSeen, t0, t1)
	}
}

// TestObserveSeverityNeverDowngrades keeps the peak severity when a later poll
// reports the same incident at a lower level.
func TestObserveSeverityNeverDowngrades(t *testing.T) {
	l := NewIncidentLog()
	t0 := time.Now()
	l.Observe("p", []IncidentEntry{entry("Outage", "", 3)}, t0)
	l.Observe("p", []IncidentEntry{entry("Outage", "", 1)}, t0.Add(time.Minute))

	got := l.Recent("p", t0.Add(-time.Hour))
	if len(got) != 1 || got[0].Severity != 3 {
		t.Fatalf("Recent = %+v, want one entry with Severity 3", got)
	}
}

// TestObserveSkipsUnidentifiable drops entries with no summary and no URL.
func TestObserveSkipsUnidentifiable(t *testing.T) {
	l := NewIncidentLog()
	l.Observe("p", []IncidentEntry{entry("", "", 2)}, time.Now())
	if got := l.Recent("p", time.Time{}); len(got) != 0 {
		t.Fatalf("Recent = %d entries, want 0", len(got))
	}
}

// TestRecentWindowAndOrder returns only entries seen after the cutoff, newest
// first.
func TestRecentWindowAndOrder(t *testing.T) {
	l := NewIncidentLog()
	base := time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)
	l.Observe("p", []IncidentEntry{entry("old", "", 1)}, base)
	l.Observe("p", []IncidentEntry{entry("mid", "", 1)}, base.Add(10*time.Hour))
	l.Observe("p", []IncidentEntry{entry("new", "", 1)}, base.Add(20*time.Hour))

	got := l.Recent("p", base.Add(5*time.Hour))
	if len(got) != 2 {
		t.Fatalf("Recent = %d entries, want 2", len(got))
	}
	if got[0].Summary != "new" || got[1].Summary != "mid" {
		t.Errorf("order = [%s %s], want newest first [new mid]", got[0].Summary, got[1].Summary)
	}
}

// TestObserveCap bounds the per-provider journal, evicting the oldest entries.
func TestObserveCap(t *testing.T) {
	l := NewIncidentLog()
	now := time.Now()
	for i := range incidentCap + 10 {
		l.Observe("p", []IncidentEntry{entry(string(rune('A'+i%26))+string(rune('0'+i/26)), "", 1)}, now)
	}
	if got := len(l.Recent("p", time.Time{})); got != incidentCap {
		t.Fatalf("retained %d entries, want the cap %d", got, incidentCap)
	}
}

// TestPruneDropsStale removes entries outside the display window and clears
// empty series.
func TestPruneDropsStale(t *testing.T) {
	l := NewIncidentLog()
	now := time.Now()
	l.Observe("stale", []IncidentEntry{entry("gone", "", 1)}, now.Add(-30*time.Hour))
	l.Observe("fresh", []IncidentEntry{entry("kept", "", 1)}, now.Add(-time.Hour))
	l.Prune(25*time.Hour, now)

	if got := l.Recent("stale", time.Time{}); len(got) != 0 {
		t.Errorf("stale series survived Prune: %+v", got)
	}
	if got := l.Recent("fresh", time.Time{}); len(got) != 1 {
		t.Errorf("fresh series lost: %+v", got)
	}
}

// TestIncidentsSaveLoadRoundTrip persists and restores the journal.
func TestIncidentsSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "incidents.json")
	l := NewIncidentLog()
	now := time.Now().Truncate(time.Second)
	l.Observe("p", []IncidentEntry{entry("Elevated errors", "https://x/inc/1", 2)}, now)
	if err := l.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	restored, err := LoadIncidents(path)
	if err != nil {
		t.Fatalf("LoadIncidents: %v", err)
	}
	got := restored.Recent("p", time.Time{})
	if len(got) != 1 || got[0].Summary != "Elevated errors" || got[0].Severity != 2 {
		t.Fatalf("restored = %+v, want the saved entry", got)
	}
}

// TestLoadIncidentsMissingAndCorrupt starts empty on a missing or unparsable
// file instead of failing launch.
func TestLoadIncidentsMissingAndCorrupt(t *testing.T) {
	dir := t.TempDir()

	l, err := LoadIncidents(filepath.Join(dir, "nope.json"))
	if err != nil || l == nil {
		t.Fatalf("missing file: log=%v err=%v, want empty log and nil error", l, err)
	}

	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	l, err = LoadIncidents(bad)
	if err != nil || l == nil {
		t.Fatalf("corrupt file: log=%v err=%v, want empty log and nil error", l, err)
	}
}
