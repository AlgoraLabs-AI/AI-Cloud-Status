package monitor

import "time"

// Backoff computes exponentially-increasing retry delays with full jitter, used
// to space out provider feed re-fetches after a failure so a flaky endpoint (or
// a brief local network blip) does not turn into a tight retry loop. "Full
// jitter" means the actual delay is a random value in [0, cap], which spreads
// retries out and avoids synchronized thundering-herd retries across providers.
type Backoff struct {
	// Base is the delay ceiling for the first retry (attempt 0).
	Base time.Duration
	// Max caps the delay ceiling no matter how many attempts have elapsed.
	Max time.Duration
	// Factor is the exponential growth multiplier per attempt (e.g. 2.0).
	Factor float64
}

// DefaultBackoff returns sensible defaults: 500ms base, doubling, capped at 30s.
func DefaultBackoff() Backoff {
	return Backoff{Base: 500 * time.Millisecond, Max: 30 * time.Second, Factor: 2}
}

// Cap returns the un-jittered delay ceiling for a zero-based attempt number:
// Base * Factor^attempt, clamped to [Base, Max]. It is monotonically
// non-decreasing in attempt.
func (b Backoff) Cap(attempt int) time.Duration {
	base := b.Base
	if base <= 0 {
		base = 500 * time.Millisecond
	}
	factor := b.Factor
	if factor < 1 {
		factor = 2
	}
	d := float64(base)
	for i := 0; i < attempt; i++ {
		d *= factor
		if b.Max > 0 && d >= float64(b.Max) {
			return b.Max
		}
	}
	out := time.Duration(d)
	if b.Max > 0 && out > b.Max {
		return b.Max
	}
	return out
}

// Delay returns the jittered delay for attempt, given a jitter fraction in
// [0,1) (e.g. from rand.Float64()). The result lies in [0, Cap(attempt)].
// Callers supply the random source so the function stays deterministic and
// testable; the production caller passes a real random fraction.
func (b Backoff) Delay(attempt int, jitter float64) time.Duration {
	if jitter < 0 {
		jitter = 0
	}
	if jitter >= 1 {
		jitter = 0.999999
	}
	return time.Duration(float64(b.Cap(attempt)) * jitter)
}
