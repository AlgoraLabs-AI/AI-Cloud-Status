package monitor

import "sync"

// Confirmation thresholds for total internet loss. They debounce the raw
// "both defaults down" signal so a transient blip — both resolvers happening to
// miss a single probe at once — cannot fire a "total internet loss" alert. A real
// outage is confirmed only after totalLossConfirmDown consecutive down rounds, and
// recovery only after totalLossConfirmUp good rounds within the last
// totalLossRecoveryWindow rounds (so a one-round reconnect mid-outage doesn't
// prematurely announce "restored"). During an outage each round costs roughly one
// probe timeout, so these are a handful of seconds.
const (
	totalLossConfirmDown = 3
	totalLossConfirmUp   = 2
	// totalLossRecoveryWindow is how many recent rounds the confirmUp good
	// rounds may be spread across. Consecutive-only recovery deadlocked on a
	// flapping link: upStreak was reset to 0 by every down round, so strict
	// alternation could never reach confirmUp and the detector stayed latched
	// forever — reporting total loss while the connection worked half the time,
	// and (because the UI checks Down() before the partial-loss branches)
	// suppressing the packet-loss alert that WAS accurate. "Total loss" claims
	// nothing gets through; a handful of successful rounds disproves it. The
	// window keeps the original guarantee that one lucky round mid-outage does
	// not announce "restored".
	totalLossRecoveryWindow = 5
)

// TotalLossDetector raises a single, distinct alert when sustained total internet
// loss is detected (both default targets down for several consecutive rounds) and
// a recovery alert when connectivity returns. It is edge-triggered with hysteresis
// so transient packet loss can't trip it. Safe for concurrent use.
type TotalLossDetector struct {
	mu         sync.Mutex
	down       bool   // a confirmed total-loss episode is active (alert already fired)
	downStreak int    // consecutive both-down rounds
	recent     uint32 // bitmask of the last `window` rounds, 1 = good (not both down)
	// justRecovered means the last transition was a recovery, so re-latching is
	// held to the stricter clean-window rule. See mayLatch.
	justRecovered bool

	confirmDown int
	confirmUp   int
	window      int
}

// NewTotalLossDetector returns a ready-to-use detector.
func NewTotalLossDetector() *TotalLossDetector {
	return &TotalLossDetector{
		confirmDown: totalLossConfirmDown,
		confirmUp:   totalLossConfirmUp,
		window:      totalLossRecoveryWindow,
	}
}

// Update reports the current "both defaults down" condition and returns a
// Notification to emit on a CONFIRMED transition (down after enough consecutive
// down rounds, or recovered after enough good rounds), or nil otherwise.
func (d *TotalLossDetector) Update(bothDown bool) *Notification {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.record(!bothDown)

	if bothDown {
		d.downStreak++
		if !d.down && d.downStreak >= d.confirmDown && d.mayLatch() {
			d.down, d.justRecovered = true, false
			d.recent = 0 // recovery is judged only on rounds after the latch
			return totalLossAlert()
		}
		return nil
	}

	d.downStreak = 0
	if d.down && d.goodInWindow() >= d.confirmUp {
		d.down, d.justRecovered = false, true
		d.recent = ^uint32(0) // all-good: re-latching must flush a full window
		return totalLossRecovery()
	}
	return nil
}

// mayLatch gates re-entry after a recovery.
//
// Widening the EXIT to "confirmUp good rounds within a window" while leaving
// entry at "confirmDown consecutive bad" makes the pair oscillate: a link
// cycling D D D U U alerts and recovers once per cycle — a notification pair
// every ~10s at a 3s probe timeout, which is worse than the stuck alert it
// replaced. So a re-latch must first flush the whole window clean.
//
// That stricter rule applies ONLY after a recovery. Requiring it always would
// delay every FIRST detection from confirmDown rounds to a full window, because
// a link that was healthy before the blackout legitimately leaves good rounds in
// the window — and slowing down genuine blackout detection is the opposite of
// what this detector is for.
func (d *TotalLossDetector) mayLatch() bool {
	if !d.justRecovered {
		return true
	}
	if d.goodInWindow() == 0 {
		d.justRecovered = false // window is flushed; normal rules resume
		return true
	}
	return false
}

// record pushes one round into the rolling window, dropping the oldest.
func (d *TotalLossDetector) record(good bool) {
	d.recent <<= 1
	if good {
		d.recent |= 1
	}
}

// goodInWindow counts the good rounds among the last `window` recorded rounds.
// window must stay in [1, 32]: `recent` is a uint32, and a zero window would
// make the mask zero so no good round is ever counted — which is the latch bug
// this whole mechanism exists to prevent.
func (d *TotalLossDetector) goodInWindow() int {
	if d.window <= 0 || d.window > 32 {
		d.window = totalLossRecoveryWindow
	}
	mask := uint32(1)<<uint(d.window) - 1
	n := 0
	for bits := d.recent & mask; bits != 0; bits &= bits - 1 {
		n++
	}
	return n
}

// RestoreLatch re-marks a confirmed total-loss episode that a PREVIOUS run
// left open, so its recovery is still announced after a restart.
//
// Only the CONCLUSION is restored. downStreak, the recent-rounds bitmask and
// justRecovered are deliberately left zero: they describe rounds that are over,
// and a zeroed bitmask is exactly the state a live latch leaves behind anyway
// (see Update, which clears recent on latching so recovery is judged only on
// rounds after it). Recovery therefore still has to earn its confirmUp good
// rounds, and a further down round cannot re-announce because the latch is
// already set.
//
// The one thing NOT carried across is the stricter re-latch cooldown that
// justRecovered imposes; a restart between a recovery and the next episode
// therefore relaxes anti-oscillation back to the nominal threshold. That is
// accepted: the alternative is persisting round-level bookkeeping whose
// meaning does not survive the gap in observation.
func (d *TotalLossDetector) RestoreLatch() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.down = true
	d.downStreak = 0
	d.recent = 0
	d.justRecovered = false
}

// Down reports whether a confirmed total internet-loss episode is active.
func (d *TotalLossDetector) Down() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.down
}

// Reset clears any latched state without emitting a notification. Use it when
// total loss can no longer be judged (e.g. a default target was disabled) so a
// later both-down condition re-confirms and re-alerts cleanly.
func (d *TotalLossDetector) Reset() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.down = false
	d.downStreak = 0
	d.recent = 0
	d.justRecovered = false
}

// Connectivity alert titles. Like the provider titles above these are the
// JOIN KEY between the detector, the alert journal, and any auditor reading
// it back — a renamed literal would silently orphan every past window.
const (
	TitleTotalLoss          = "Total internet loss"
	TitleTotalLossRecovered = "Internet connection restored"
)

func totalLossAlert() *Notification {
	return &Notification{
		Title: TitleTotalLoss,
		Body:  "No internet connection — both 1.1.1.1 and 8.8.8.8 are unreachable.",
	}
}

func totalLossRecovery() *Notification {
	return &Notification{
		Title:    TitleTotalLossRecovered,
		Body:     "Connectivity to the default targets has returned.",
		Recovery: true,
	}
}
