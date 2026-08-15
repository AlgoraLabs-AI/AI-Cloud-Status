package history

import (
	"testing"
	"time"
)

// ms builds an up sample with the given latency, n seconds after the base.
func ms(n int, latency time.Duration) Sample {
	return Sample{Time: downBase.Add(time.Duration(n) * time.Second), Up: true, Latency: latency}
}

// TestLatencyIgnoresFailedProbes is the headline honesty guard: a failed ping
// records Latency == 0, so averaging it in would make a lossy link look faster
// than a clean one and would report a 0ms "fastest" for every check that ever
// missed a poll.
func TestLatencyIgnoresFailedProbes(t *testing.T) {
	samples := []Sample{
		ms(0, 20*time.Millisecond),
		{Time: downBase.Add(time.Second), Up: false}, // lost ping: Latency 0
		ms(2, 40*time.Millisecond),
	}
	got := Latency(samples)
	if got.Count != 2 {
		t.Fatalf("Count = %d, want 2 (only the completed round-trips)", got.Count)
	}
	if got.Avg != 30*time.Millisecond {
		t.Errorf("Avg = %v, want 30ms (not diluted by the failure)", got.Avg)
	}
	if got.Min != 20*time.Millisecond {
		t.Errorf("Min = %v, want 20ms (a lost probe is not the fastest one)", got.Min)
	}
	if got.Max != 40*time.Millisecond {
		t.Errorf("Max = %v, want 40ms", got.Max)
	}
}

// TestLatencyExcludesTransportFailures pins the URL side of the same rule: a
// timeout or DNS failure carries an elapsed time that measures how long the
// transport waited, not how fast the server is.
func TestLatencyExcludesTransportFailures(t *testing.T) {
	samples := []Sample{
		ms(0, 30*time.Millisecond),
		{Time: downBase.Add(time.Second), Up: false, Latency: 10 * time.Second}, // timeout
	}
	got := Latency(samples)
	if got.Count != 1 || got.Avg != 30*time.Millisecond {
		t.Errorf("Latency = %+v, want a single 30ms measurement (the 10s timeout is not a response)", got)
	}
}

// TestLatencyIncludesCompletedFailedResponses is the other half: a server
// answering 500s slowly IS answering, and dropping those responses would hide
// exactly the slowness the panel exists to surface.
func TestLatencyIncludesCompletedFailedResponses(t *testing.T) {
	samples := []Sample{
		ms(0, 20*time.Millisecond),
		{Time: downBase.Add(time.Second), Up: false, Responded: true, Latency: 400 * time.Millisecond},
	}
	got := Latency(samples)
	if got.Count != 2 {
		t.Fatalf("Count = %d, want 2 (a completed 500 is a real response time)", got.Count)
	}
	if got.Avg != 210*time.Millisecond {
		t.Errorf("Avg = %v, want 210ms", got.Avg)
	}
	if got.Max != 400*time.Millisecond {
		t.Errorf("Max = %v, want 400ms (the failing response is the slow one)", got.Max)
	}
}

// TestLatencyCountsSubMillisecondSuccess pins the Windows quantization case:
// IcmpSendEcho reports whole milliseconds, so a LAN reply under 1ms is a genuine
// success recorded as 0. Excluding it would leave a local target with a
// permanently empty latency block.
func TestLatencyCountsSubMillisecondSuccess(t *testing.T) {
	got := Latency([]Sample{ms(0, 0), ms(1, 2*time.Millisecond)})
	if got.Count != 2 {
		t.Fatalf("Count = %d, want 2 (a 0ms reply is sub-millisecond, not missing)", got.Count)
	}
	if got.Min != 0 {
		t.Errorf("Min = %v, want 0 (the sub-millisecond reply is the fastest)", got.Min)
	}
	if got.Avg != time.Millisecond {
		t.Errorf("Avg = %v, want 1ms", got.Avg)
	}
}

// TestLatencyIgnoresUnknownSamples pins that an unreadable observation
// contributes no measurement.
func TestLatencyIgnoresUnknownSamples(t *testing.T) {
	samples := []Sample{
		ms(0, 10*time.Millisecond),
		{Time: downBase.Add(time.Second), Unknown: true, Latency: 900 * time.Millisecond},
	}
	if got := Latency(samples); got.Count != 1 || got.Avg != 10*time.Millisecond {
		t.Errorf("Latency = %+v, want a single 10ms measurement", got)
	}
}

// TestLatencyEmptyReportsNoData pins that "nothing measured" is distinguishable
// from "measured zero".
func TestLatencyEmptyReportsNoData(t *testing.T) {
	got := Latency([]Sample{{Time: downBase, Up: false}})
	if got.Count != 0 || got.Avg != 0 || got.Max != 0 {
		t.Errorf("Latency of an all-failed series = %+v, want a zero-Count result", got)
	}
}

// TestLossCountsAndStreaks pins the loss figures, including the distinction
// between scattered loss and one unbroken outage.
func TestLossCountsAndStreaks(t *testing.T) {
	samples := []Sample{
		ms(0, time.Millisecond),
		{Time: downBase.Add(1 * time.Second)},
		{Time: downBase.Add(2 * time.Second)},
		{Time: downBase.Add(3 * time.Second)},
		ms(4, time.Millisecond),
		{Time: downBase.Add(5 * time.Second)},
	}
	got := Loss(samples)
	if got.Observed != 6 || got.Lost != 4 {
		t.Fatalf("Loss = %+v, want 6 observed / 4 lost", got)
	}
	if got.LongestStreak != 3 {
		t.Errorf("LongestStreak = %d, want 3", got.LongestStreak)
	}
	if got.Current != 1 {
		t.Errorf("Current = %d, want 1 (the run still open at the newest sample)", got.Current)
	}
	if pct := got.Percent(); pct < 66.6 || pct > 66.7 {
		t.Errorf("Percent = %v, want ~66.67", pct)
	}
}

// TestLossExcludesUnknownFromBothSides pins that an unreadable observation
// neither fakes reliability nor fakes an outage.
func TestLossExcludesUnknownFromBothSides(t *testing.T) {
	samples := []Sample{
		ms(0, time.Millisecond),
		{Time: downBase.Add(time.Second), Unknown: true},
	}
	got := Loss(samples)
	if got.Observed != 1 || got.Lost != 0 {
		t.Errorf("Loss = %+v, want 1 observed / 0 lost", got)
	}
	if got.Percent() != 0 {
		t.Errorf("Percent = %v, want 0", got.Percent())
	}
}

// TestLossPercentWithNoObservations guards the division: an all-unknown window
// reports 0 rather than NaN.
func TestLossPercentWithNoObservations(t *testing.T) {
	got := Loss([]Sample{{Time: downBase, Unknown: true}})
	if got.Observed != 0 || got.Percent() != 0 {
		t.Errorf("Loss = %+v, Percent = %v; want 0 observed and 0%%", got, got.Percent())
	}
}

// TestObservedSpanExcludesGaps is the guard for the statistics heading: the ring
// is bounded by sample COUNT while the prune keeps a day of wall clock, so after
// an overnight shutdown a raw Span would label ten minutes of real observation
// "last 8h".
func TestObservedSpanExcludesGaps(t *testing.T) {
	samples := steady(0, 60) // 60 samples, 1s apart
	samples = append(samples, steady(8*3600, 60)...)
	if got := Span(samples); got < 8*time.Hour {
		t.Fatalf("Span = %v, want the full 8h+ distance (this is what makes it wrong here)", got)
	}
	got := ObservedSpan(samples)
	if got < 118*time.Second || got > 120*time.Second {
		t.Errorf("ObservedSpan = %v, want ~118s (two minutes of real observation, not 8h)", got)
	}
}
