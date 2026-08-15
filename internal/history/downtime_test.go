package history

import (
	"testing"
	"time"
)

var downBase = time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

// at builds a sample n seconds after the base instant.
func at(n int, up bool) Sample {
	return Sample{Time: downBase.Add(time.Duration(n) * time.Second), Up: up}
}

// second returns the base instant plus n seconds, for the `now` argument.
func second(n int) time.Time { return downBase.Add(time.Duration(n) * time.Second) }

// steady builds count healthy samples one second apart starting at offset, so a
// series has a cadence for observationGap to find.
func steady(offset, count int) []Sample {
	out := make([]Sample, 0, count)
	for i := 0; i < count; i++ {
		out = append(out, at(offset+i, true))
	}
	return out
}

// TestDownRunsGroupsConsecutiveFailures pins the core reconstruction: only the
// consecutive down samples form a run, and the first following up sample is
// recorded as the recovery bound.
func TestDownRunsGroupsConsecutiveFailures(t *testing.T) {
	samples := append(steady(0, 5), at(5, false), at(6, false), at(7, true), at(8, true))
	runs := DownRuns(samples, second(10))
	if len(runs) != 1 {
		t.Fatalf("DownRuns = %d runs, want 1: %+v", len(runs), runs)
	}
	r := runs[0]
	if !r.Start.Equal(second(5)) || !r.End.Equal(second(6)) {
		t.Errorf("run bounds = %v..%v, want +5s..+6s", r.Start, r.End)
	}
	if !r.Recovered.Equal(second(7)) {
		t.Errorf("Recovered = %v, want +7s (first up sample)", r.Recovered)
	}
	if r.Samples != 2 || r.Ongoing || r.Truncated {
		t.Errorf("run = %+v, want 2 samples, not ongoing, not truncated", r)
	}
}

// TestDownRunsSingleMissIsOnePollLong is the honesty guard for a one-sample
// outage: measured start-to-start it is zero, which would render as "lasted 0s"
// for what was really up to a full poll interval of downtime. Measuring to the
// observed recovery gives the right order of magnitude.
func TestDownRunsSingleMissIsOnePollLong(t *testing.T) {
	samples := []Sample{at(0, true), at(60, false), at(120, true)}
	runs := DownRuns(samples, second(180))
	if len(runs) != 1 {
		t.Fatalf("DownRuns = %d runs, want 1", len(runs))
	}
	if got := runs[0].Duration(second(200)); got != time.Minute {
		t.Errorf("Duration = %v, want 1m (down sample → observed recovery)", got)
	}
}

// TestDownRunsSplitsOnObservationGap is the anti-fabrication guard: the app was
// closed between two failures, so nothing is known about the hours in between.
// Reporting one continuous outage across that hole would invent an outage that
// was never observed.
func TestDownRunsSplitsOnObservationGap(t *testing.T) {
	samples := append(steady(0, 5), at(5, false), at(6, false))
	samples = append(samples, at(8*3600, false), at(8*3600+1, false))
	runs := DownRuns(samples, second(8*3600+2))
	if len(runs) != 2 {
		t.Fatalf("DownRuns = %d runs, want 2 (the 8h hole splits them): %+v", len(runs), runs)
	}
	if !runs[0].Start.Equal(downBase.Add(8 * time.Hour)) {
		t.Errorf("runs[0].Start = %v, want the post-gap run first", runs[0].Start)
	}
	if runs[1].Ongoing {
		t.Error("the pre-gap run must not be ongoing — its recovery was never observed")
	}
	if !runs[1].Recovered.IsZero() {
		t.Errorf("pre-gap run Recovered = %v, want zero (no up sample was ever seen)", runs[1].Recovered)
	}
	if got := runs[1].Duration(second(9 * 3600)); got != time.Second {
		t.Errorf("pre-gap Duration = %v, want 1s (bounded by its own samples, not the hole)", got)
	}
}

// TestDownRunsRecoveryAcrossGapIsNotObserved is the blocker both design reviews
// caught independently: closing a run on the first sample after an overnight
// shutdown dates the recovery to relaunch time and reports a one-second blip as
// an eight-hour outage. It fires on EVERY launch, because a day of samples
// survives the restart and the first fresh poll is usually healthy.
func TestDownRunsRecoveryAcrossGapIsNotObserved(t *testing.T) {
	samples := append(steady(0, 5), at(5, false))
	samples = append(samples, at(8*3600, true))
	runs := DownRuns(samples, second(8*3600+1))
	if len(runs) != 1 {
		t.Fatalf("DownRuns = %d runs, want 1: %+v", len(runs), runs)
	}
	if !runs[0].Recovered.IsZero() {
		t.Errorf("Recovered = %v, want zero — an up sample 8h later is not evidence of when it recovered", runs[0].Recovered)
	}
	if got := runs[0].Duration(second(8*3600 + 1)); got != 0 {
		t.Errorf("Duration = %v, want 0 (a single observed failure, not 8h of fabricated downtime)", got)
	}
}

// TestDownRunsStaleTailIsNotOngoing pins that a record which simply STOPS is not
// an outage that has been running ever since. Without the freshness check the
// panel greets the user after a resume with "ongoing for 8h".
func TestDownRunsStaleTailIsNotOngoing(t *testing.T) {
	samples := append(steady(0, 5), at(5, false), at(6, false))
	runs := DownRuns(samples, second(8*3600))
	if len(runs) != 1 {
		t.Fatalf("DownRuns = %d runs, want 1", len(runs))
	}
	if runs[0].Ongoing {
		t.Error("Ongoing = true for a series that stopped 8h ago; want false")
	}
	if got := runs[0].Duration(second(8 * 3600)); got != time.Second {
		t.Errorf("Duration = %v, want 1s (bounded by its own samples, not extended to now)", got)
	}
}

// TestDownRunsOngoingTailUsesNow pins that a run still open at a FRESH newest
// sample keeps growing against the wall clock instead of freezing at the last
// poll.
func TestDownRunsOngoingTailUsesNow(t *testing.T) {
	samples := append(steady(0, 5), at(5, false), at(6, false))
	runs := DownRuns(samples, second(20))
	if len(runs) != 1 || !runs[0].Ongoing {
		t.Fatalf("runs = %+v, want a single ongoing run", runs)
	}
	if got := runs[0].Duration(second(20)); got != 15*time.Second {
		t.Errorf("Duration = %v, want 15s (start → now)", got)
	}
}

// TestDownRunsBlackoutWithSlowRoundsIsOneOutage is the shredding guard: during a
// real blackout every probe round burns its full timeout before the interval
// sleep, so samples arrive several seconds apart at a 1s cadence. A threshold
// derived from the cadence alone would report one outage per sample.
func TestDownRunsBlackoutWithSlowRoundsIsOneOutage(t *testing.T) {
	samples := steady(0, 30) // 1s cadence while healthy
	for i := 0; i < 20; i++ {
		samples = append(samples, at(30+i*4, false)) // 4s apart while failing
	}
	runs := DownRuns(samples, second(30+20*4))
	if len(runs) != 1 {
		t.Fatalf("DownRuns = %d runs, want 1 continuous blackout: %+v", len(runs), runs)
	}
	if runs[0].Samples != 20 {
		t.Errorf("Samples = %d, want all 20 failures in one run", runs[0].Samples)
	}
}

// TestDownRunsTruncatedWhenStartIsUnverified pins that a run not preceded by a
// recent healthy sample reports its start as a lower bound — whether because the
// ring evicted the beginning or because a hole hides it.
func TestDownRunsTruncatedWhenStartIsUnverified(t *testing.T) {
	// Series opens mid-outage: nothing before it was ever seen.
	runs := DownRuns([]Sample{at(0, false), at(1, false), at(2, true)}, second(3))
	if len(runs) != 1 || !runs[0].Truncated {
		t.Fatalf("runs = %+v, want one truncated run (no healthy sample precedes it)", runs)
	}
	// A healthy sample immediately before it makes the start trustworthy.
	runs = DownRuns(append(steady(0, 5), at(5, false), at(6, true)), second(7))
	if len(runs) != 1 || runs[0].Truncated {
		t.Fatalf("runs = %+v, want one NON-truncated run", runs)
	}
	// Healthy long ago, then a hole, then failures: the start is unverified again.
	samples := append(steady(0, 5), at(8*3600, false), at(8*3600+1, false))
	runs = DownRuns(samples, second(8*3600+2))
	if len(runs) != 1 || !runs[0].Truncated {
		t.Fatalf("runs = %+v, want one truncated run (the last healthy sample is 8h back)", runs)
	}
}

// TestDownRunsSkipsUnknownSamples pins that an unreadable observation neither
// starts, extends, nor closes an outage.
func TestDownRunsSkipsUnknownSamples(t *testing.T) {
	samples := append(steady(0, 5),
		Sample{Time: second(5), Unknown: true},
		at(6, false),
		Sample{Time: second(7), Unknown: true},
		at(8, false),
		at(9, true),
	)
	runs := DownRuns(samples, second(10))
	if len(runs) != 1 {
		t.Fatalf("DownRuns = %d runs, want 1 (unknowns must not split it): %+v", len(runs), runs)
	}
	if runs[0].Samples != 2 {
		t.Errorf("Samples = %d, want 2 (unknowns are not counted as down)", runs[0].Samples)
	}
}

// TestDownRunsClockJumpBackwards guards against a system clock change producing
// a negative duration or a panic.
func TestDownRunsClockJumpBackwards(t *testing.T) {
	samples := append(steady(0, 5), at(5, false), at(4, false), at(6, true))
	runs := DownRuns(samples, second(10))
	for _, r := range runs {
		if d := r.Duration(second(10)); d < 0 {
			t.Errorf("Duration = %v, want a non-negative value", d)
		}
	}
}

// TestDownRunsAllHealthy confirms a clean series produces nothing at all.
func TestDownRunsAllHealthy(t *testing.T) {
	if runs := DownRuns(steady(0, 5), second(10)); len(runs) != 0 {
		t.Fatalf("DownRuns on a healthy series = %+v, want none", runs)
	}
	if runs := DownRuns(nil, second(10)); len(runs) != 0 {
		t.Fatalf("DownRuns(nil) = %+v, want none", runs)
	}
}

// TestObservationGapFloorsAtThirtySeconds pins the floor that keeps a slow probe
// round from reading as a hole in the record.
func TestObservationGapFloorsAtThirtySeconds(t *testing.T) {
	if got := observationGap(steady(0, 10)); got != gapFloor {
		t.Errorf("observationGap at a 1s cadence = %v, want the %v floor", got, gapFloor)
	}
	// A 60s cadence dominates the floor: 4 × 60s.
	slow := []Sample{at(0, true), at(60, true), at(120, true)}
	if got := observationGap(slow); got != 4*time.Minute {
		t.Errorf("observationGap at a 60s cadence = %v, want 4m", got)
	}
	if got := observationGap(nil); got != 0 {
		t.Errorf("observationGap(nil) = %v, want 0 (cadence unknowable)", got)
	}
}

// TestDownRunsRecordsResumptionWhenHealthy is the fix for a card that read as a
// contradiction: monitoring stopped mid-outage and came back to a healthy check,
// so WHEN it recovered is unknowable but THAT it recovered is not. Reporting only
// "recovery not observed" threw that away and sat under a green uptime strip.
func TestDownRunsRecordsResumptionWhenHealthy(t *testing.T) {
	// The real shape from the field: two failed 60s polls, a 44-minute hole, then
	// a healthy poll.
	samples := []Sample{
		{Time: downBase, Up: true},
		{Time: downBase.Add(60 * time.Second), Up: true},
		{Time: downBase.Add(120 * time.Second), Up: true},
		{Time: downBase.Add(180 * time.Second)},
		{Time: downBase.Add(241 * time.Second)},
		{Time: downBase.Add(241*time.Second + 44*time.Minute), Up: true},
	}
	runs := DownRuns(samples, downBase.Add(2*time.Hour))
	if len(runs) != 1 {
		t.Fatalf("DownRuns = %d runs, want 1: %+v", len(runs), runs)
	}
	r := runs[0]
	if !r.Recovered.IsZero() {
		t.Errorf("Recovered = %v, want zero — the recovery time is unknowable across the hole", r.Recovered)
	}
	if !r.Resumed.Equal(downBase.Add(241*time.Second + 44*time.Minute)) {
		t.Errorf("Resumed = %v, want the first healthy sample after the hole", r.Resumed)
	}
	if got := r.Duration(downBase.Add(2 * time.Hour)); got != 61*time.Second {
		t.Errorf("Duration = %v, want 61s — the hole must never be counted as downtime", got)
	}
}

// TestDownRunsNoResumptionWhenStillDown pins that coming back to a check that is
// STILL failing is not evidence of recovery: that starts a new run instead.
func TestDownRunsNoResumptionWhenStillDown(t *testing.T) {
	// A 60s cadence throughout, so the only discontinuity is the deliberate hole.
	var samples []Sample
	for i := 0; i < 5; i++ {
		samples = append(samples, at(i*60, true))
	}
	samples = append(samples, at(300, false))
	samples = append(samples, at(7200, false), at(7260, false))
	runs := DownRuns(samples, second(7300))
	if len(runs) != 2 {
		t.Fatalf("DownRuns = %d runs, want 2: %+v", len(runs), runs)
	}
	// runs[1] is the older one (newest first).
	if !runs[1].Resumed.IsZero() {
		t.Errorf("Resumed = %v, want zero — the check was still down when monitoring returned", runs[1].Resumed)
	}
}
