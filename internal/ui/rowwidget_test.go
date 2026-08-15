package ui

import (
	"testing"
	"time"

	"fyne.io/fyne/v2"

	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/config"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/history"
)

func neverMuted(string) bool { return false }

// newRowTestController builds a Controller with just the fields ensureRow touches.
func newRowTestController() *Controller {
	return &Controller{
		cfg:        config.Default(),
		history:    history.New(10),
		prevStates: map[string]statusState{},
		rowCache:   map[string]*rowWidget{},
	}
}

// testCtx is a plain (non-bucketed, connectivity-style) row context.
func testCtx(next time.Time) rowCtx {
	return rowCtx{
		isMuted:  neverMuted,
		nextPoll: next,
		interval: 30 * time.Second,
		window:   time.Minute,
		now:      time.Now(),
	}
}

// ensureRow must REUSE the cached row for an id and update it in place — the
// per-second refresh recreating every widget is what leaked gigabytes of
// renderer/texture cache over long runs.
func TestEnsureRowReusesAndUpdates(t *testing.T) {
	// Disable the state-change animation: it needs a running Fyne app/driver,
	// and headless tests have none.
	t.Setenv("AI_STATUS_PINGER_REDUCED_MOTION", "1")
	c := newRowTestController()
	ctx := testCtx(time.Now().Add(30 * time.Second))

	first, _ := c.ensureRow(rowSpec{id: "p", name: "P", state: stateOK, when: "10:00:00", latency: "5ms"}, ctx)

	second, swapped := c.ensureRow(rowSpec{id: "p", name: "P", state: stateOutage, when: "10:00:30", latency: "7ms", incidents: "Elevated errors"}, ctx)

	if first != second {
		t.Fatal("ensureRow must return the SAME cached rowWidget for an id, not a new one")
	}
	if !swapped {
		t.Error("changing the incidents text must report a layout-affecting swap")
	}
	if got := second.whenLabel.Text; got != "10:00:30" {
		t.Errorf("whenLabel = %q, want in-place update to %q", got, "10:00:30")
	}
	if got := second.latency.Text; got != "7ms" {
		t.Errorf("latency = %q, want %q", got, "7ms")
	}
	if want := visualFor(stateOutage).Label; second.badgeLabel.Text != want {
		t.Errorf("badge label = %q, want %q after state change", second.badgeLabel.Text, want)
	}

	// A refresh with no visible change must not report a swap.
	_, swapped = c.ensureRow(rowSpec{id: "p", name: "P", state: stateOutage, when: "10:00:30", latency: "7ms", incidents: "Elevated errors"}, ctx)
	if swapped {
		t.Error("an identical spec must not report a layout-affecting swap")
	}
}

// The uptime cell must follow new history samples in place: paints mutate and
// the label text tracks the recomputed percentage.
func TestEnsureRowUptimeFollowsHistory(t *testing.T) {
	c := newRowTestController()
	ctx := testCtx(time.Now().Add(30 * time.Second))
	spec := rowSpec{id: "p", name: "P", state: stateOK}

	rw, _ := c.ensureRow(spec, ctx)
	if rw.sparkBox.Visible() {
		t.Error("sparkline should be hidden while there is no history")
	}
	if got := rw.uptimeLabel.Text; got != "—" {
		t.Errorf("uptime label = %q, want em dash with no history", got)
	}

	now := time.Now()
	c.history.Add("p", history.Sample{Time: now, Up: true})
	c.history.Add("p", history.Sample{Time: now.Add(time.Second), Up: false})

	rw2, _ := c.ensureRow(spec, ctx)
	if rw2 != rw {
		t.Fatal("row must be reused across history updates")
	}
	if !rw.sparkBox.Visible() {
		t.Error("sparkline should show once samples exist")
	}
	if got := rw.uptimeLabel.Text; got != "50%" {
		t.Errorf("uptime label = %q, want 50%%", got)
	}
	if want := []int{samplePaintOK, samplePaintDown}; !equalPaints(rw.paints, want) {
		t.Errorf("paints = %v, want %v", rw.paints, want)
	}
}

// A BUCKETED row (provider) renders sparkBuckets time buckets anchored to now:
// covered time keeps its observation, uncovered time is grey/unknown, and the
// percentage still reflects the observed samples only.
func TestEnsureRowBucketedStrip(t *testing.T) {
	c := newRowTestController()
	ctx := testCtx(time.Now().Add(30 * time.Second))
	ctx.bucketed = true
	ctx.window = sparkBuckets * time.Minute // 1 bucket per minute

	now := time.Now()
	c.history.Add("p", history.Sample{Time: now.Add(-time.Minute), Up: true})
	c.history.Add("p", history.Sample{Time: now, Up: false})

	rw, _ := c.ensureRow(rowSpec{id: "p", name: "P", state: stateOutage}, ctx)
	if len(rw.paints) != sparkBuckets {
		t.Fatalf("paints = %d buckets, want %d", len(rw.paints), sparkBuckets)
	}
	if got := rw.uptimeLabel.Text; got != "50%" {
		t.Errorf("uptime label = %q, want 50%% (observed samples only)", got)
	}
	down, unknown := 0, 0
	for _, p := range rw.paints {
		switch p {
		case samplePaintDown:
			down++
		case samplePaintUnknown:
			unknown++
		}
	}
	if down == 0 {
		t.Error("the down sample must paint its bucket red")
	}
	if unknown < sparkBuckets-3 {
		t.Errorf("uncovered time must be grey: unknown=%d", unknown)
	}
}

// The uptime cell must NOT recompute on a plain cosmetic tick: the cache key
// only changes when a sample lands, muting changes, or the anchor advances.
func TestEnsureRowUptimeCacheSkipsRecompute(t *testing.T) {
	c := newRowTestController()
	ctx := testCtx(time.Now().Add(30 * time.Second))
	spec := rowSpec{id: "p", name: "P", state: stateOK}
	c.history.Add("p", history.Sample{Time: time.Now(), Up: true})

	rw, _ := c.ensureRow(spec, ctx)
	key := rw.uptimeKey
	if key.count != 1 {
		t.Fatalf("cache key count = %d, want 1", key.count)
	}
	// Same inputs → same key (no recompute); a new sample → new key.
	if got := c.rowUptimeKey("p", ctx); got != key {
		t.Fatal("key must be stable across cosmetic ticks with unchanged inputs")
	}
	c.history.Add("p", history.Sample{Time: time.Now().Add(time.Second), Up: false})
	if got := c.rowUptimeKey("p", ctx); got == key {
		t.Fatal("a new sample must change the cache key")
	}
	// A muting change must also invalidate.
	ctx2 := ctx
	ctx2.muteSig = "us-east-1"
	if got := c.rowUptimeKey("p", ctx2); got == key {
		t.Fatal("a muting change must change the cache key")
	}
}

// Rows for ids that disappear from the table (disabled/filtered) must be
// evicted from the cache; sections must reuse their box.
func TestEnsureSectionBoxReuse(t *testing.T) {
	c := newRowTestController()
	c.sectionBoxes = map[int]*sectionBox{}
	ctx := testCtx(time.Now().Add(time.Second))

	a, _ := c.ensureRow(rowSpec{id: "a", name: "A", state: stateOK}, ctx)
	b, _ := c.ensureRow(rowSpec{id: "b", name: "B", state: stateOK}, ctx)

	box1 := c.ensureSectionBox(1, "Cloud · every 30s", []fyne.CanvasObject{a.row, b.row})
	box2 := c.ensureSectionBox(1, "Cloud · every 30s", []fyne.CanvasObject{a.row, b.row})
	if box1 != box2 {
		t.Fatal("ensureSectionBox must reuse the cached box for a section")
	}
	box3 := c.ensureSectionBox(1, "Cloud · every 10s", []fyne.CanvasObject{a.row})
	if box3 != box1 {
		t.Fatal("a title/row change must still reuse the same box object (mutated in place)")
	}
	sb := c.sectionBoxes[1]
	if sb.heading.Text != "Cloud · every 10s" {
		t.Errorf("heading = %q, want updated title", sb.heading.Text)
	}
	if len(sb.inner.Objects) != 2 { // heading + 1 row
		t.Errorf("inner objects = %d, want 2 (heading + row)", len(sb.inner.Objects))
	}
}
