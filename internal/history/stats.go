package history

import "time"

// LatencyStats summarizes the response times in a slice of samples.
//
// A sample carries a measurement when the round-trip actually COMPLETED — the
// check was up, or it responded and merely failed the check's rule (see
// Sample.Responded). Everything else is excluded, and both halves of that rule
// matter:
//
//   - A failed ping records Latency 0, so averaging failures in would make a
//     connection look FASTER the more packets it loses. A failed URL check
//     records the time until the transport gave up, which measures a timeout,
//     not a server.
//   - A slow server answering 500s is still answering. Excluding those responses
//     would hide exactly the slowness the panel exists to show, so a completed
//     response counts even when the check judged it down.
//
// Latency == 0 is NOT used as the exclusion test, because on Windows ICMP
// reports whole milliseconds: a sub-millisecond LAN reply is a genuine success
// recorded as 0. Treating it as "no measurement" would leave a local target with
// a permanently empty latency block and bias every figure upward.
//
// Count is how many measurements the figures rest on, so a caller can tell
// "average 12ms over 600 probes" from "average 12ms over 1".
type LatencyStats struct {
	Count int
	Avg   time.Duration
	Min   time.Duration
	Max   time.Duration
}

// measured reports whether a sample's latency is a real observation of how long
// the endpoint took to answer.
func measured(s Sample) bool {
	return !s.Unknown && (s.Up || s.Responded)
}

// Latency computes the latency summary of samples. An empty result (Count == 0)
// means nothing was ever successfully measured, which callers must render as
// "no data" rather than as zeros.
func Latency(samples []Sample) LatencyStats {
	var out LatencyStats
	var sum time.Duration
	for _, s := range samples {
		if !measured(s) {
			continue
		}
		out.Count++
		sum += s.Latency
		if out.Count == 1 || s.Latency < out.Min {
			out.Min = s.Latency
		}
		if s.Latency > out.Max {
			out.Max = s.Latency
		}
	}
	if out.Count > 0 {
		out.Avg = sum / time.Duration(out.Count)
	}
	return out
}

// LossStats summarizes how many probes went unanswered.
//
// Observed counts only samples the check could actually judge: Unknown samples
// (an unreadable provider feed) are excluded from BOTH sides, because counting
// them as delivered would fake reliability and counting them as lost would fake
// an outage.
//
// LongestStreak is the longest run of consecutive lost probes and Current is the
// run still in progress at the newest sample. Together they answer the question
// a bare percentage cannot: 5% loss spread evenly is a lossy link, while the
// same 5% in one unbroken block was a short, total outage.
type LossStats struct {
	Observed      int
	Lost          int
	LongestStreak int
	Current       int
}

// Loss computes the loss summary of samples.
func Loss(samples []Sample) LossStats {
	var out LossStats
	streak := 0
	for _, s := range samples {
		if s.Unknown {
			continue
		}
		out.Observed++
		if s.Up {
			streak = 0
			continue
		}
		out.Lost++
		streak++
		if streak > out.LongestStreak {
			out.LongestStreak = streak
		}
	}
	out.Current = streak
	return out
}

// Percent is the share of observed probes that were lost, in [0,100]. It is 0
// when nothing was observed — callers should check Observed before showing it.
func (l LossStats) Percent() float64 {
	if l.Observed == 0 {
		return 0
	}
	return float64(l.Lost) / float64(l.Observed) * 100
}
