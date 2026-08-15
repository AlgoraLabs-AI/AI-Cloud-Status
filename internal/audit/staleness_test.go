package audit

import (
	"testing"
	"time"

	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/providers"
)

// The tests here all pin the same failure: the audit certifying, as already
// alerted, a major incident that nothing ever alerted on. That is the one thing
// this tool must never do — a false "all clear" from the tool whose whole job is
// to prove nothing was missed is worse than not running it, because it ends the
// investigation.
//
// Both causes come from the same place. OutageTracker holds its down/healthy
// state in memory only, so a desktop app that is CLOSED while a provider
// recovers emits an outage line with no matching recovery line, ever. That is
// routine for a tray app, not exotic.

func majorCapture(at time.Time) Capture {
	return Capture{Provider: "p", Time: at, Severity: providers.SevMajor}
}

func minorCapture(at time.Time) Capture {
	return Capture{Provider: "p", Time: at, Severity: providers.SevMinor}
}

// TestOpenWindowDoesNotCoverAMuchLaterIncident is C1. An outage window left open
// because its recovery was never logged used to have End == zero, and
// interval.covers reads a zero End as +infinity — so a wholly unrelated outage
// a month later landed "inside" it and was reported as matched.
func TestOpenWindowDoesNotCoverAMuchLaterIncident(t *testing.T) {
	t0 := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	later := t0.Add(30 * 24 * time.Hour)

	windows := []interval{{Start: t0}} // opened, never closed
	captures := []Capture{majorCapture(t0), majorCapture(later)}

	bounded, missing := boundOpenIntervals(windows, captures, minTolerance)
	if _, ok := findCovering(bounded, later, minTolerance); ok {
		t.Error("a major incident 30 days later is still reported as covered by a stale open window")
	}
	if _, ok := findCovering(bounded, t0, minTolerance); !ok {
		t.Error("the window must still cover the incident it was actually opened for")
	}
	if len(missing) == 0 {
		t.Error("a window bounded for lack of evidence must be reported as a missing recovery")
	}
}

// TestOpenWindowStillCoversAContinuousOutage is the counterweight: a genuinely
// ongoing outage produces a run of major captures, and every one of them must
// stay covered. Bounding must key on the gap between captures, not on elapsed
// time since the window opened.
func TestOpenWindowStillCoversAContinuousOutage(t *testing.T) {
	t0 := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	windows := []interval{{Start: t0}}

	var captures []Capture
	for i := 0; i < 200; i++ { // ~4 days of hourly captures, no gap
		captures = append(captures, majorCapture(t0.Add(time.Duration(i)*time.Hour)))
	}

	bounded, _ := boundOpenIntervals(windows, captures, minTolerance)
	for _, c := range captures {
		if _, ok := findCovering(bounded, c.Time, minTolerance); !ok {
			t.Fatalf("capture at %s fell outside the window of a continuously-observed outage", c.Time)
		}
	}
}

// TestOpenWindowClosedByClearingCaptureIsUnchanged guards the pre-existing rule:
// a sub-major capture is direct evidence the incident cleared, and it still
// closes the window at that moment.
func TestOpenWindowClosedByClearingCaptureIsUnchanged(t *testing.T) {
	t0 := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	clear := t0.Add(2 * time.Hour)

	bounded, missing := boundOpenIntervals(
		[]interval{{Start: t0}},
		[]Capture{majorCapture(t0), minorCapture(clear)},
		minTolerance,
	)
	if !bounded[0].End.Equal(clear) {
		t.Errorf("End = %s, want the clearing capture's time %s", bounded[0].End, clear)
	}
	if len(missing) != 1 || !missing[0].ClearedAt.Equal(clear) {
		t.Errorf("missing = %+v, want one entry cleared at %s", missing, clear)
	}
}

// TestSeparateOutagesAreNotMergedAcrossADayLongGap is C2. Two "Provider outage"
// lines with no recovery between them used to merge into one window. The merged
// window has a non-zero End, so boundOpenIntervals skips it entirely and no
// missing recovery is reported — while covers() marks everything in the gap as
// already alerted. A real miss in the middle vanished twice over.
func TestSeparateOutagesAreNotMergedAcrossADayLongGap(t *testing.T) {
	t0 := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	second := t0.Add(6 * 24 * time.Hour)

	windows := intervalsFrom([]transition{
		{Time: t0, Outage: true},
		{Time: second, Outage: true},
		{Time: second.Add(time.Hour), Outage: false},
	})
	if len(windows) != 2 {
		t.Fatalf("got %d windows, want 2 — six days apart with no recovery is not one outage", len(windows))
	}

	// The gap between them must not be covered by either.
	gap := t0.Add(3 * 24 * time.Hour)
	bounded, _ := boundOpenIntervals(windows, []Capture{majorCapture(t0), majorCapture(gap), majorCapture(second)}, minTolerance)
	if _, ok := findCovering(bounded, gap, minTolerance); ok {
		t.Error("an incident three days into the gap is reported as covered")
	}
}

// TestRestartMidOutageStillMerges is the counterweight to the split above: the
// app restarting during a single outage re-alerts within minutes, and that is
// one logical episode, not two.
func TestRestartMidOutageStillMerges(t *testing.T) {
	t0 := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	windows := intervalsFrom([]transition{
		{Time: t0, Outage: true},
		{Time: t0.Add(5 * time.Minute), Outage: true}, // restart, same episode
		{Time: t0.Add(30 * time.Minute), Outage: false},
	})
	if len(windows) != 1 {
		t.Fatalf("got %d windows, want 1 — a restart five minutes in is the same outage", len(windows))
	}
	if !windows[0].Start.Equal(t0) {
		t.Errorf("Start = %s, want the ORIGINAL open %s", windows[0].Start, t0)
	}
}

// TestCaptureTimeReadsUTCAndLegacyNames pins C3's fix. Capture filenames used to
// be local wall clock with no offset while alert-log entries are absolute, so
// correlating them drifted by the zone offset — hours, against a five-minute
// tolerance. New names carry a Z; legacy names keep their original (ambiguous)
// local reading, which is no worse than when they were written.
func TestCaptureTimeReadsUTCAndLegacyNames(t *testing.T) {
	utc, err := captureTime("20260801-155355Z-major.json")
	if err != nil {
		t.Fatalf("captureTime(utc name): %v", err)
	}
	want := time.Date(2026, 8, 1, 15, 53, 55, 0, time.UTC)
	if !utc.Equal(want) {
		t.Errorf("utc name parsed to %s, want %s", utc, want)
	}

	legacy, err := captureTime("20260801-155355-major.json")
	if err != nil {
		t.Fatalf("captureTime(legacy name): %v", err)
	}
	if !legacy.Equal(time.Date(2026, 8, 1, 15, 53, 55, 0, time.Local)) {
		t.Errorf("legacy name should still be read in the local zone, got %s", legacy)
	}

	if _, err := captureTime("nope.json"); err == nil {
		t.Error("a name with no timestamp must error rather than yield the zero time silently")
	}
}

// TestUTCCaptureNamesSurviveAZoneChange is the point of C3 stated as a property:
// the same absolute instant must be recovered no matter what zone the audit runs
// in. Under the old local-wall-clock names this failed by the offset — enough to
// report a correctly-alerted incident as a miss, or to slide a capture into a
// different incident's window and call it matched.
func TestUTCCaptureNamesSurviveAZoneChange(t *testing.T) {
	instant := time.Date(2026, 8, 1, 13, 0, 0, 0, time.UTC)
	name := instant.Format(captureTimeFormat) + "Z-major.json"

	for _, zone := range []string{"UTC", "America/Sao_Paulo", "Asia/Tokyo"} {
		loc, err := time.LoadLocation(zone)
		if err != nil {
			t.Skipf("zone database unavailable: %v", err)
		}
		prev := time.Local
		time.Local = loc
		got, err := captureTime(name)
		time.Local = prev
		if err != nil {
			t.Fatalf("%s: %v", zone, err)
		}
		if !got.Equal(instant) {
			t.Errorf("under %s the capture read as %s, want %s", zone, got, instant)
		}
	}
}
