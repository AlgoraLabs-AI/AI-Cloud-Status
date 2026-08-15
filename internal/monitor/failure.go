package monitor

import (
	"fmt"
	"sync"
)

// DefaultFailureThreshold is the number of consecutive failures a single check
// may have before an alert fires. The alert fires when the streak exceeds this
// value (i.e. on the 6th consecutive failure for the default of 5).
const DefaultFailureThreshold = 5

// FailureTracker tracks consecutive failures per check (ping targets and
// providers) and emits a debounced alert when a check's failure streak exceeds
// the threshold, plus a recovery notification when it next succeeds. It is safe
// for concurrent use.
type FailureTracker struct {
	mu        sync.Mutex
	threshold int
	counts    map[string]int
	alerted   map[string]bool
}

// NewFailureTracker returns a tracker that alerts once a check exceeds
// threshold consecutive failures.
func NewFailureTracker(threshold int) *FailureTracker {
	if threshold < 0 {
		threshold = 0
	}
	return &FailureTracker{
		threshold: threshold,
		counts:    map[string]int{},
		alerted:   map[string]bool{},
	}
}

// Record updates the streak for check id (using name in any notification) and
// returns a Notification to emit, or nil. It returns an alert the first time a
// streak exceeds the threshold, and a recovery notification on the first
// success after an alert was raised. Steady states return nil (debounced).
func (f *FailureTracker) Record(id, name string, success bool) *Notification {
	f.mu.Lock()
	defer f.mu.Unlock()

	if success {
		f.counts[id] = 0
		if f.alerted[id] {
			f.alerted[id] = false
			return &Notification{
				Title:    "Check recovered",
				Body:     fmt.Sprintf("%s is responding again.", name),
				Recovery: true,
			}
		}
		return nil
	}

	f.counts[id]++
	if f.counts[id] > f.threshold && !f.alerted[id] {
		f.alerted[id] = true
		return &Notification{
			Title: "Check failing",
			Body:  fmt.Sprintf("%s has failed %d consecutive checks.", name, f.counts[id]),
		}
	}
	return nil
}

// Reset clears any tracked state for a check id (e.g. when it is removed or
// disabled) so a later recovery notification is not emitted spuriously.
func (f *FailureTracker) Reset(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.counts, id)
	delete(f.alerted, id)
}
