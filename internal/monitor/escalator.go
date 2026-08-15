package monitor

import (
	"fmt"
	"sync"
	"time"
)

// lossEscalationSchedule is the backoff between repeat packet-loss reminders
// while loss persists, so the user is kept informed without being spammed every
// second: the first reminder fires immediately, then after 10s, 30s, 1m, and 2m,
// holding at 2m thereafter.
var lossEscalationSchedule = []time.Duration{
	10 * time.Second,
	30 * time.Second,
	60 * time.Second,
	120 * time.Second,
}

// LossEscalator decides when to (re-)notify the user about ongoing internet
// packet loss. It emits a "losing packets" notification the moment loss crosses
// the warning threshold, then again on the increasing backoff above for as long
// as loss persists, and a recovery notification when it clears. It is meant for
// PARTIAL loss; sustained total loss is owned by TotalLossDetector, so callers
// should Reset the escalator while the current round is a total blackout. Safe
// for concurrent use.
type LossEscalator struct {
	mu      sync.Mutex
	active  bool
	stepIdx int
	nextAt  time.Time
}

// NewLossEscalator returns a ready-to-use escalator.
func NewLossEscalator() *LossEscalator { return &LossEscalator{} }

// Update reports the current loss percentage at time now and returns a
// Notification to surface (or nil) plus first=true when this is the INITIAL
// detection of a new loss episode — the caller pops a window for the first
// detection (and for total loss) but only toasts the recurring reminders.
//
// It applies HYSTERESIS and a WARM-UP gate so a couple of transient blips can't
// fire it: a new episode starts only when ready is true (enough samples) AND loss
// exceeds LossEnterPercent, and it clears only once loss drops below
// LossExitPercent. now is a parameter for testability.
func (e *LossEscalator) Update(lossPercent float64, ready bool, now time.Time) (note *Notification, first bool) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.active {
		if lossPercent < LossExitPercent {
			e.active = false
			e.stepIdx = 0
			e.nextAt = time.Time{}
			return &Notification{Title: "Internet recovered", Body: "Packet loss has cleared.", Recovery: true}, false
		}
		// Still losing — re-notify on the backoff schedule.
		if now.Before(e.nextAt) {
			return nil, false
		}
		if e.stepIdx < len(lossEscalationSchedule)-1 {
			e.stepIdx++
		}
		e.nextAt = now.Add(lossEscalationSchedule[e.stepIdx])
		return losingPacketsAlert(lossPercent), false
	}

	// Not active: enter a new episode only once warmed up and clearly above the
	// enter threshold.
	if ready && lossPercent > LossEnterPercent {
		e.active = true
		e.stepIdx = 0
		e.nextAt = now.Add(lossEscalationSchedule[0])
		return losingPacketsAlert(lossPercent), true
	}
	return nil, false
}

// Reset clears the active state without emitting a notification.
func (e *LossEscalator) Reset() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.active = false
	e.stepIdx = 0
	e.nextAt = time.Time{}
}

func losingPacketsAlert(lossPercent float64) *Notification {
	return &Notification{
		Title: "You're losing packets",
		Body:  fmt.Sprintf("Internet packet loss is at %.0f%%.", lossPercent),
	}
}
