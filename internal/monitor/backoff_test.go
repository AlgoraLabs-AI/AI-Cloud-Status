package monitor

import (
	"testing"
	"time"
)

func TestBackoffCapGrowsAndClamps(t *testing.T) {
	b := Backoff{Base: 100 * time.Millisecond, Max: 1 * time.Second, Factor: 2}
	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{0, 100 * time.Millisecond},
		{1, 200 * time.Millisecond},
		{2, 400 * time.Millisecond},
		{3, 800 * time.Millisecond},
		{4, 1 * time.Second},  // clamped to Max
		{10, 1 * time.Second}, // still clamped
	}
	for _, tc := range cases {
		if got := b.Cap(tc.attempt); got != tc.want {
			t.Errorf("Cap(%d) = %v, want %v", tc.attempt, got, tc.want)
		}
	}
}

func TestBackoffCapMonotonic(t *testing.T) {
	b := DefaultBackoff()
	prev := time.Duration(-1)
	for i := 0; i < 20; i++ {
		got := b.Cap(i)
		if got < prev {
			t.Fatalf("Cap not monotonic: Cap(%d)=%v < prev %v", i, got, prev)
		}
		if got > b.Max {
			t.Fatalf("Cap(%d)=%v exceeds Max %v", i, got, b.Max)
		}
		prev = got
	}
}

func TestBackoffDelayWithinBounds(t *testing.T) {
	b := Backoff{Base: 100 * time.Millisecond, Max: 5 * time.Second, Factor: 2}
	for attempt := 0; attempt < 6; attempt++ {
		cap := b.Cap(attempt)
		for _, j := range []float64{0, 0.25, 0.5, 0.9999} {
			d := b.Delay(attempt, j)
			if d < 0 || d > cap {
				t.Errorf("Delay(%d, %v) = %v out of [0, %v]", attempt, j, d, cap)
			}
		}
	}
}

func TestBackoffDelayScalesWithJitter(t *testing.T) {
	b := Backoff{Base: 1 * time.Second, Max: 10 * time.Second, Factor: 2}
	zero := b.Delay(0, 0)
	half := b.Delay(0, 0.5)
	if zero != 0 {
		t.Errorf("Delay with 0 jitter = %v, want 0", zero)
	}
	if half <= 0 || half > b.Cap(0) {
		t.Errorf("Delay with 0.5 jitter = %v, want in (0, %v]", half, b.Cap(0))
	}
}

func TestBackoffDelayClampsOutOfRangeJitter(t *testing.T) {
	b := Backoff{Base: 1 * time.Second, Max: 10 * time.Second, Factor: 2}
	if d := b.Delay(0, -1); d != 0 {
		t.Errorf("negative jitter should clamp to 0, got %v", d)
	}
	if d := b.Delay(0, 5); d > b.Cap(0) {
		t.Errorf("over-range jitter should clamp within cap, got %v", d)
	}
}
