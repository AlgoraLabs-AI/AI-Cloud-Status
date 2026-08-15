package history

import (
	"sort"
	"time"
)

// Observation-continuity tuning. A sample landing further than the derived gap
// from its predecessor is not "the next poll" — it is the far side of a hole in
// which nothing at all was observed (the app was closed, the laptop suspended).
//
// gapFloor exists because a probe round is not instantaneous: during a real
// blackout every connectivity round burns the full probe timeout before the
// interval sleep even starts, so samples at a 1s cadence arrive ~4s apart — right
// at 4× the interval. Deriving the threshold from the cadence alone would then
// shatter one ten-minute blackout into a hundred and fifty one-sample "outages".
// Nothing legitimate is 30 seconds apart at a fast cadence, and a suspend is
// always far longer.
const (
	gapFactor = 4
	gapFloor  = 30 * time.Second
)

// DownRun is one contiguous stretch during which a check was OBSERVED down.
//
// Connectivity pings and custom URL checks have no incident feed — the only
// evidence an outage ever happened is a run of consecutive Up==false samples in
// the ring buffer. DownRuns reconstructs those runs so the drill-down panels can
// show "what went wrong" for checks that IncidentLog knows nothing about.
//
// A polled check can only ever BOUND an outage, never time it, so the three
// timestamps are deliberately distinct:
//   - Start is the first sample seen down. The real failure happened somewhere in
//     the poll interval before it.
//   - End is the last sample seen down.
//   - Recovered is the first sample seen up again within observation range, i.e.
//     the upper bound on when service returned.
//
// Two flags mark the bounds that are NOT known, and callers must render them
// differently or the card lies:
//   - Truncated: the run was not preceded by a recent healthy sample, so Start is
//     a lower bound — the outage may have begun before the ring's horizon or
//     inside an unobserved gap.
//   - !Ongoing && Recovered.IsZero(): the recovery was never observed (the run
//     ends at a hole in the record), so the END is a lower bound. Such a run must
//     read "at least 4m", never "lasted 4m".
//
// Resumed softens that last case with the one fact the hole does not destroy.
// When monitoring stopped mid-outage and came back to a HEALTHY check, the
// service is known to have recovered — just not when. Reporting only "recovery
// not observed" throws that away and reads as "it may still be down", directly
// contradicting the green strip beside it. Resumed carries the timestamp of that
// first healthy observation after the hole (zero when the recovery was seen
// normally, when the check was still down on return, or when monitoring never
// came back at all).
type DownRun struct {
	Start     time.Time
	End       time.Time
	Recovered time.Time
	Resumed   time.Time
	Samples   int
	Ongoing   bool
	Truncated bool
}

// Duration is how long the outage lasted, measured to the tightest bound
// available: to the observed recovery when there is one, to now while it is
// still ongoing, else to the last sample seen down. A single missed poll
// therefore reads as roughly one poll interval rather than zero. It never counts
// unobserved time: an outage that ends at a hole in the record is measured only
// across the samples that actually recorded it.
func (r DownRun) Duration(now time.Time) time.Duration {
	end := r.End
	switch {
	case r.Ongoing:
		end = now
	case !r.Recovered.IsZero():
		end = r.Recovered
	}
	if d := end.Sub(r.Start); d > 0 {
		return d
	}
	return 0
}

// DownRuns groups samples (oldest first) into the stretches where the check was
// observed down, newest run first. now is the wall clock, used only to decide
// whether the newest run is still live.
//
// The continuity threshold is derived from the samples' OWN cadence rather than
// taken from the caller's current poll interval: the retained ring can span an
// interval change, and a caller passing today's 5s cadence would split a series
// recorded at 60s into one "outage" per sample.
//
// Unknown samples are skipped rather than treated as either state: an unreadable
// observation is not evidence of an outage, and it is not evidence of recovery.
func DownRuns(samples []Sample, now time.Time) []DownRun {
	gap := observationGap(samples)
	var out []DownRun
	var cur *DownRun
	var prev, lastUp time.Time

	for _, s := range samples {
		if s.Unknown {
			continue
		}
		// A hole in the record: whatever is on the far side is a fresh
		// observation, never a continuation of what came before it.
		broke := gap > 0 && !prev.IsZero() && s.Time.Sub(prev) > gap
		prev = s.Time
		if broke && cur != nil {
			// Recovered stays zero — WHEN it came back is unknowable across the
			// hole. But if this first observation on the far side is healthy,
			// THAT it came back is not, so record when we saw it.
			if s.Up {
				cur.Resumed = s.Time
			}
			out = append(out, *cur)
			cur = nil
		}
		if s.Up {
			if cur != nil {
				// Only an up sample within observation range is EVIDENCE of
				// recovery. Across a hole the recovery time is unknowable, and
				// dating it to the first post-hole poll would report an overnight
				// shutdown as an overnight outage — which is exactly what happens
				// on every relaunch, since a day of samples survives the restart
				// and the first fresh poll is usually healthy.
				if gap <= 0 || s.Time.Sub(cur.End) <= gap {
					cur.Recovered = s.Time
				}
				out = append(out, *cur)
				cur = nil
			}
			lastUp = s.Time
			continue
		}
		if cur == nil {
			cur = &DownRun{Start: s.Time}
			// The start is only trustworthy if the check was seen HEALTHY shortly
			// before it. Otherwise the outage may already have been under way —
			// before the ring's oldest sample, or inside a hole in the record.
			cur.Truncated = lastUp.IsZero() || (gap > 0 && s.Time.Sub(lastUp) > gap)
		}
		cur.End = s.Time
		cur.Samples++
	}
	if cur != nil {
		// Live only if the newest down sample is recent enough to still describe
		// the present. A series that stops eight hours ago is not an outage that
		// has been running for eight hours — it is a record that stops.
		cur.Ongoing = gap > 0 && now.Sub(cur.End) <= gap
		out = append(out, *cur)
	}
	// Newest first, like IncidentLog.Recent, so the most relevant outage is read
	// without scrolling.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// observationGap is the largest spacing between consecutive samples that still
// counts as continuous observation of this series: a multiple of the series' own
// typical cadence, floored so a slow probe round can never look like a hole. It
// returns 0 when there are too few samples to establish a cadence at all, which
// callers read as "continuity unknowable" (no splitting, nothing declared live).
func observationGap(samples []Sample) time.Duration {
	deltas := make([]time.Duration, 0, len(samples))
	var prev time.Time
	for _, s := range samples {
		if s.Unknown {
			continue
		}
		if !prev.IsZero() {
			if d := s.Time.Sub(prev); d > 0 {
				deltas = append(deltas, d)
			}
		}
		prev = s.Time
	}
	if len(deltas) == 0 {
		return 0
	}
	// The median, not the mean: a single overnight hole would drag a mean far
	// past every real interval and swallow the very gaps this exists to find.
	sort.Slice(deltas, func(i, j int) bool { return deltas[i] < deltas[j] })
	median := deltas[len(deltas)/2]
	if g := median * gapFactor; g > gapFloor {
		return g
	}
	return gapFloor
}
