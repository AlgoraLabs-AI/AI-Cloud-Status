package monitor

import (
	"context"
	"time"
)

// Retry calls fn up to attempts times, sleeping for b.Delay(attempt, jitter())
// between tries whenever fn asks to retry. fn returns (retry, err):
//
//   - err == nil          → success; Retry returns immediately
//   - retry == false      → non-retryable failure (e.g. "no feed"); return err
//   - retry == true       → retryable failure; back off and try again
//
// jitter supplies a fraction in [0,1) for the full-jitter backoff (e.g.
// rand.Float64); a nil jitter is treated as 0 (no jitter). Retry stops early if
// ctx is cancelled during a backoff sleep. It returns the number of attempts
// made and the last error observed.
func Retry(ctx context.Context, attempts int, b Backoff, jitter func() float64, fn func(attempt int) (bool, error)) (int, error) {
	if attempts < 1 {
		attempts = 1
	}
	if jitter == nil {
		jitter = func() float64 { return 0 }
	}
	var err error
	tries := 0
	for attempt := 0; attempt < attempts; attempt++ {
		tries++
		var retry bool
		retry, err = fn(attempt)
		if err == nil || !retry {
			return tries, err
		}
		if attempt == attempts-1 {
			break // out of attempts
		}
		select {
		case <-ctx.Done():
			return tries, err
		case <-time.After(b.Delay(attempt, jitter())):
		}
	}
	return tries, err
}
