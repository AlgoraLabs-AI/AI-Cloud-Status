// Package monitor implements connectivity probing (ICMP with a TCP fallback)
// and rolling packet-loss accounting for the connectivity targets.
package monitor

import "sync"

// LossWindow tracks a fixed-size rolling window of probe outcomes and computes
// the packet-loss percentage over that window. It is safe for concurrent use.
type LossWindow struct {
	mu      sync.Mutex
	size    int
	results []bool // true == probe succeeded
}

// NewLossWindow returns a window retaining the most recent size results.
func NewLossWindow(size int) *LossWindow {
	if size <= 0 {
		size = 1
	}
	return &LossWindow{size: size}
}

// Add records a single probe outcome, evicting the oldest if the window is full.
func (w *LossWindow) Add(success bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.results = append(w.results, success)
	if len(w.results) > w.size {
		w.results = w.results[len(w.results)-w.size:]
	}
}

// LossPercent returns the percentage of failed probes in the window, in the
// range [0,100]. An empty window reports 0.
func (w *LossWindow) LossPercent() float64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.results) == 0 {
		return 0
	}
	lost := 0
	for _, ok := range w.results {
		if !ok {
			lost++
		}
	}
	return float64(lost) / float64(len(w.results)) * 100
}

// Count returns how many results are currently held.
func (w *LossWindow) Count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.results)
}

// Reset empties the window so loss accounting starts fresh (used when a total
// blackout ends, so the just-ended outage doesn't linger in the rolling window).
func (w *LossWindow) Reset() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.results = nil
}
