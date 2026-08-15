package monitor

import (
	"context"
	"errors"
	"testing"
	"time"
)

// noJitter makes Delay return 0 so retry tests never actually sleep.
func noJitter() float64 { return 0 }

func TestRetrySucceedsFirstTry(t *testing.T) {
	calls := 0
	tries, err := Retry(context.Background(), 3, DefaultBackoff(), noJitter, func(int) (bool, error) {
		calls++
		return true, nil // err nil => success regardless of retry flag
	})
	if err != nil || tries != 1 || calls != 1 {
		t.Fatalf("tries=%d calls=%d err=%v, want 1/1/nil", tries, calls, err)
	}
}

func TestRetryNonRetryableStopsImmediately(t *testing.T) {
	sentinel := errors.New("no feed")
	calls := 0
	tries, err := Retry(context.Background(), 5, DefaultBackoff(), noJitter, func(int) (bool, error) {
		calls++
		return false, sentinel // not retryable
	})
	if err != sentinel || tries != 1 || calls != 1 {
		t.Fatalf("tries=%d calls=%d err=%v, want 1/1/sentinel", tries, calls, err)
	}
}

func TestRetrySucceedsAfterTransientFailures(t *testing.T) {
	transient := errors.New("temporary")
	calls := 0
	tries, err := Retry(context.Background(), 5, DefaultBackoff(), noJitter, func(attempt int) (bool, error) {
		calls++
		if attempt < 2 {
			return true, transient // retry
		}
		return false, nil // success on the 3rd try
	})
	if err != nil || tries != 3 || calls != 3 {
		t.Fatalf("tries=%d calls=%d err=%v, want 3/3/nil", tries, calls, err)
	}
}

func TestRetryExhaustsAttempts(t *testing.T) {
	transient := errors.New("temporary")
	calls := 0
	tries, err := Retry(context.Background(), 3, DefaultBackoff(), noJitter, func(int) (bool, error) {
		calls++
		return true, transient
	})
	if err != transient || tries != 3 || calls != 3 {
		t.Fatalf("tries=%d calls=%d err=%v, want 3/3/transient", tries, calls, err)
	}
}

func TestRetryStopsOnContextCancel(t *testing.T) {
	transient := errors.New("temporary")
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled before the first backoff sleep

	calls := 0
	// A long backoff with full jitter ensures the time.After branch cannot win
	// the race against the already-cancelled context.
	longBackoff := Backoff{Base: time.Hour, Max: time.Hour, Factor: 2}
	tries, err := Retry(ctx, 5, longBackoff, func() float64 { return 0.999 }, func(int) (bool, error) {
		calls++
		return true, transient
	})
	if tries != 1 || calls != 1 || err != transient {
		t.Fatalf("tries=%d calls=%d err=%v, want 1/1/transient (cancelled before retry)", tries, calls, err)
	}
}

func TestRetryClampsAttempts(t *testing.T) {
	calls := 0
	tries, _ := Retry(context.Background(), 0, DefaultBackoff(), noJitter, func(int) (bool, error) {
		calls++
		return true, errors.New("x")
	})
	if tries != 1 || calls != 1 {
		t.Fatalf("attempts<1 should clamp to 1; tries=%d calls=%d", tries, calls)
	}
}
