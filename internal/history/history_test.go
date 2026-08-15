package history

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAddBoundsToCapacity(t *testing.T) {
	h := New(3)
	base := time.Unix(0, 0)
	for i := 0; i < 10; i++ {
		h.Add("p", Sample{Time: base.Add(time.Duration(i) * time.Second), Up: true, Latency: time.Duration(i) * time.Millisecond})
	}
	if got := h.Len("p"); got != 3 {
		t.Fatalf("Len = %d, want 3 (capped)", got)
	}
	series := h.Series("p")
	// Should retain the last three (i = 7,8,9).
	if series[0].Latency != 7*time.Millisecond || series[2].Latency != 9*time.Millisecond {
		t.Errorf("retained wrong window: %v", series)
	}
}

// TestSetCapOverride pins the per-check retention override: a high-frequency
// check (connectivity ping) is bounded below the provider-sized default, both
// on future Adds and on the already-retained series.
func TestSetCapOverride(t *testing.T) {
	h := New(100)
	base := time.Unix(0, 0)
	for i := 0; i < 10; i++ {
		h.Add("ping", Sample{Time: base.Add(time.Duration(i) * time.Second), Up: true})
	}
	h.SetCap("ping", 4)
	if got := h.Len("ping"); got != 4 {
		t.Fatalf("Len after SetCap = %d, want 4 (existing series trimmed)", got)
	}
	for i := 10; i < 20; i++ {
		h.Add("ping", Sample{Time: base.Add(time.Duration(i) * time.Second), Up: true})
	}
	if got := h.Len("ping"); got != 4 {
		t.Fatalf("Len = %d, want 4 (override caps future adds)", got)
	}
	// Other checks keep the default cap.
	for i := 0; i < 10; i++ {
		h.Add("prov", Sample{Time: base.Add(time.Duration(i) * time.Second), Up: true})
	}
	if got := h.Len("prov"); got != 10 {
		t.Fatalf("default-cap series Len = %d, want 10", got)
	}
	// Clearing the override restores the default bound.
	h.SetCap("ping", 0)
	for i := 20; i < 40; i++ {
		h.Add("ping", Sample{Time: base.Add(time.Duration(i) * time.Second), Up: true})
	}
	if got := h.Len("ping"); got != 24 {
		t.Fatalf("Len after clearing override = %d, want 24", got)
	}
}

func TestLast(t *testing.T) {
	h := New(10)
	if _, n := h.Last("p"); n != 0 {
		t.Fatalf("Last on empty = %d samples, want 0", n)
	}
	base := time.Unix(1700000000, 0)
	h.Add("p", Sample{Time: base})
	h.Add("p", Sample{Time: base.Add(time.Minute)})
	last, n := h.Last("p")
	if n != 2 || !last.Equal(base.Add(time.Minute)) {
		t.Fatalf("Last = %v,%d want newest,2", last, n)
	}
}

// TestPrune pins the restore-time hygiene: samples older than the display
// window or implausibly far in the future (clock changes, corrupt persisted
// data) are dropped; series left empty disappear.
func TestPrune(t *testing.T) {
	h := New(100)
	now := time.Unix(1_800_000_000, 0)
	h.Add("p", Sample{Time: now.Add(-30 * time.Hour), Up: true}) // too old
	h.Add("p", Sample{Time: now.Add(-2 * time.Hour), Up: true})  // keep
	h.Add("p", Sample{Time: now.Add(-time.Minute), Up: false})   // keep
	h.Add("p", Sample{Time: now.Add(26 * time.Hour), Up: true})  // future → drop
	h.Add("stale", Sample{Time: now.Add(-100 * time.Hour), Up: true})

	h.Prune(24*time.Hour, now)
	if got := h.Len("p"); got != 2 {
		t.Fatalf("Len after prune = %d, want 2 (%v)", got, h.Series("p"))
	}
	if got := h.Len("stale"); got != 0 {
		t.Fatalf("fully-stale series Len = %d, want 0 (deleted)", got)
	}
}

func TestUptime(t *testing.T) {
	h := New(10)
	if _, ok := h.Uptime("none"); ok {
		t.Error("Uptime should report ok=false with no samples")
	}
	h.Add("p", Sample{Up: true})
	h.Add("p", Sample{Up: true})
	h.Add("p", Sample{Up: false})
	h.Add("p", Sample{Up: true})
	got, ok := h.Uptime("p")
	if !ok {
		t.Fatal("Uptime should be ok with samples")
	}
	if got != 0.75 {
		t.Errorf("Uptime = %v, want 0.75", got)
	}
}

func TestLastLatency(t *testing.T) {
	h := New(10)
	if _, ok := h.LastLatency("p"); ok {
		t.Error("LastLatency should be false with no samples")
	}
	h.Add("p", Sample{Latency: 10 * time.Millisecond})
	h.Add("p", Sample{Latency: 25 * time.Millisecond})
	got, ok := h.LastLatency("p")
	if !ok || got != 25*time.Millisecond {
		t.Errorf("LastLatency = %v,%v want 25ms,true", got, ok)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "history.json")
	h := New(100)
	base := time.Unix(1700000000, 0).UTC()
	for i := 0; i < 5; i++ {
		h.Add("openai", Sample{Time: base.Add(time.Duration(i) * time.Minute), Up: i%2 == 0, Latency: time.Duration(i) * time.Millisecond})
	}
	h.Add("ping-google", Sample{Time: base, Up: true, Latency: 5 * time.Millisecond})

	if err := h.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path, 100)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Len("openai") != 5 || got.Len("ping-google") != 1 {
		t.Fatalf("lengths after load: openai=%d ping-google=%d", got.Len("openai"), got.Len("ping-google"))
	}
	gotSeries := got.Series("openai")
	wantSeries := h.Series("openai")
	for i := range wantSeries {
		if !gotSeries[i].Time.Equal(wantSeries[i].Time) || gotSeries[i].Up != wantSeries[i].Up || gotSeries[i].Latency != wantSeries[i].Latency {
			t.Errorf("sample %d = %+v, want %+v", i, gotSeries[i], wantSeries[i])
		}
	}
}

// TestDownRegionsRoundTrip locks the schema change: the per-sample DownRegions
// survive a save/load cycle, and an old sample without the field loads as nil
// (an unscoped/global down) rather than failing.
func TestDownRegionsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")
	h := New(100)
	base := time.Unix(1700000000, 0).UTC()
	h.Add("aws", Sample{Time: base, Up: false, DownRegions: []string{"me-central-1", "me-south-1"}})
	h.Add("aws", Sample{Time: base.Add(time.Minute), Up: true})      // omitempty → no dr
	h.Add("aws", Sample{Time: base.Add(2 * time.Minute), Up: false}) // unscoped down, nil dr

	if err := h.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path, 100)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	s := got.Series("aws")
	if len(s) != 3 {
		t.Fatalf("len = %d, want 3", len(s))
	}
	if len(s[0].DownRegions) != 2 || s[0].DownRegions[0] != "me-central-1" || s[0].DownRegions[1] != "me-south-1" {
		t.Fatalf("sample0 DownRegions = %v, want [me-central-1 me-south-1]", s[0].DownRegions)
	}
	if s[1].DownRegions != nil {
		t.Fatalf("up sample should have nil DownRegions, got %v", s[1].DownRegions)
	}
	if s[2].DownRegions != nil {
		t.Fatalf("unscoped down should have nil DownRegions, got %v", s[2].DownRegions)
	}
}

func TestLoadMissingReturnsEmpty(t *testing.T) {
	h, err := Load(filepath.Join(t.TempDir(), "absent.json"), 50)
	if err != nil {
		t.Fatalf("Load missing: %v", err)
	}
	if len(h.IDs()) != 0 {
		t.Error("missing file should yield empty history")
	}
}

func TestLoadCorruptReturnsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")
	if err := os.WriteFile(path, []byte("{not valid json"), 0o644); err != nil {
		t.Fatal(err)
	}
	h, err := Load(path, 50)
	if err != nil {
		t.Fatalf("Load corrupt should be non-fatal: %v", err)
	}
	if len(h.IDs()) != 0 {
		t.Error("corrupt file should yield empty history")
	}
}

// TestLoadRebondsToSmallerCapacity ensures shrinking the build's retention
// trims previously-saved series to the new cap.
func TestLoadRebondsToSmallerCapacity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")
	h := New(100)
	for i := 0; i < 50; i++ {
		h.Add("p", Sample{Time: time.Unix(int64(i), 0), Up: true})
	}
	if err := h.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path, 10)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Len("p") != 10 {
		t.Errorf("Len after re-bound = %d, want 10", got.Len("p"))
	}
	// The retained samples must be the most recent ten (i = 40..49).
	series := got.Series("p")
	if series[0].Time.Unix() != 40 || series[9].Time.Unix() != 49 {
		t.Errorf("re-bound kept wrong window: first=%d last=%d", series[0].Time.Unix(), series[9].Time.Unix())
	}
}

func TestSaveIsAtomicNoResidue(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "history.json")
	h := New(10)
	h.Add("p", Sample{Up: true})
	if err := h.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "history.json" {
			t.Errorf("unexpected residue after atomic save: %q", e.Name())
		}
	}
}
