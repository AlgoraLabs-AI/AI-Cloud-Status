package ui

import "testing"

func TestNextFreeSlot(t *testing.T) {
	cases := []struct {
		name string
		used map[int]bool
		want int
	}{
		{"empty", map[int]bool{}, 0},
		{"sequential", map[int]bool{0: true, 1: true}, 2},
		{"fills gap left by a closed card", map[int]bool{0: true, 2: true}, 1},
		{"corner freed first", map[int]bool{1: true}, 0},
	}
	for _, tc := range cases {
		if got := nextFreeSlot(tc.used); got != tc.want {
			t.Errorf("%s: nextFreeSlot = %d, want %d", tc.name, got, tc.want)
		}
	}
}
