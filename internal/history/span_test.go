package history

import (
	"testing"
	"time"
)

func TestSpan(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	if got := Span(nil); got != 0 {
		t.Fatalf("nil samples span = %v, want 0", got)
	}
	if got := Span([]Sample{{Time: base}}); got != 0 {
		t.Fatalf("single sample span = %v, want 0", got)
	}
	s := []Sample{
		{Time: base},
		{Time: base.Add(30 * time.Second)},
		{Time: base.Add(5 * time.Minute)},
	}
	if got := Span(s); got != 5*time.Minute {
		t.Fatalf("span = %v, want 5m (oldest→newest)", got)
	}
}

func TestHumanizeSpan(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, ""},
		{-time.Second, ""},
		{45 * time.Second, "45s"},
		{89 * time.Second, "89s"},
		{90 * time.Second, "2m"}, // crosses into minutes, rounded
		{8 * time.Minute, "8m"},
		{89 * time.Minute, "89m"},
		{90 * time.Minute, "2h"}, // crosses into hours, rounded
		{3 * time.Hour, "3h"},
		{25 * time.Hour, "25h"},
	}
	for _, c := range cases {
		if got := HumanizeSpan(c.d); got != c.want {
			t.Errorf("HumanizeSpan(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}
