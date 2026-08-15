//go:build windows

package ui

import "testing"

// TestGridOffsets pins the sampling grid inside the 8%..92% band, strictly
// increasing, with the requested number of points — the guarantees that keep
// probes off the window edges and spread across the client area.
func TestGridOffsets(t *testing.T) {
	for _, n := range []int{deadCanvasGridCols, deadCanvasGridRows} {
		offs := gridOffsets(n)
		if len(offs) != n {
			t.Fatalf("gridOffsets(%d) returned %d offsets", n, len(offs))
		}
		if offs[0] != 8 || offs[n-1] != 92 {
			t.Errorf("gridOffsets(%d) endpoints = %d..%d, want 8..92", n, offs[0], offs[n-1])
		}
		for i := 1; i < n; i++ {
			if offs[i] <= offs[i-1] {
				t.Errorf("gridOffsets(%d) not strictly increasing at %d: %v", n, i, offs)
			}
		}
	}
}
