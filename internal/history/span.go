package history

import (
	"fmt"
	"time"
)

// Span returns the wall-clock duration covered by samples, from the oldest to
// the newest. It is zero for fewer than two samples (nothing to span yet).
//
// The retained window is a fixed COUNT of samples (DefaultCapacity), so the time
// it covers depends on the check interval — a 1s connectivity check spans minutes
// while a slow provider poll spans hours. Span lets the UI label each row with the
// real time range it represents instead of guessing from the sample count.
func Span(samples []Sample) time.Duration {
	if len(samples) < 2 {
		return 0
	}
	return samples[len(samples)-1].Time.Sub(samples[0].Time)
}

// ObservedSpan is the wall-clock time the samples actually COVER: the sum of the
// gaps between consecutive observations, excluding any hole large enough to mean
// the check was not running (see observationGap).
//
// Span is the wrong measure for a statistics heading. The ring is bounded by
// sample COUNT while the launch-time prune keeps a day of wall clock, so after an
// overnight shutdown a connectivity series holds yesterday's tail plus a handful
// of fresh probes — and Span would label ten minutes of real observation "last
// 8h". Every figure under such a heading would then be read against a window
// nine tenths of which was never watched.
func ObservedSpan(samples []Sample) time.Duration {
	gap := observationGap(samples)
	var total time.Duration
	var prev time.Time
	for _, s := range samples {
		if s.Unknown {
			continue
		}
		if !prev.IsZero() {
			if d := s.Time.Sub(prev); d > 0 && (gap <= 0 || d <= gap) {
				total += d
			}
		}
		prev = s.Time
	}
	return total
}

// HumanizeSpan renders a span as a coarse, glanceable label — "45s", "8m", "3h" —
// rounding to the single most natural unit. It returns "" for a non-positive span
// (too little history to label), so callers can omit the suffix entirely.
func HumanizeSpan(d time.Duration) string {
	switch {
	case d <= 0:
		return ""
	case d < 90*time.Second:
		return fmt.Sprintf("%ds", int(d.Round(time.Second).Seconds()))
	case d < 90*time.Minute:
		return fmt.Sprintf("%dm", int(d.Round(time.Minute).Minutes()))
	default:
		return fmt.Sprintf("%dh", int(d.Round(time.Hour).Hours()))
	}
}
