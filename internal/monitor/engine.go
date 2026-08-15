package monitor

import (
	"context"
	"sync"
	"time"
)

// Target is a connectivity endpoint to probe.
type Target struct {
	ID   string
	Name string
	Host string
}

// TargetStatus is the latest known state of a single target, suitable for
// display in the UI.
type TargetStatus struct {
	Target
	Reachable   bool
	LossPercent float64
	RTT         time.Duration
	Mode        ProbeMode
	LastChecked time.Time
	// UsingFallback is true when the TCP fallback ran because ICMP was
	// unavailable/unprivileged on this host.
	UsingFallback bool
}

// Notifier delivers a desktop notification. The UI provides a Fyne-backed
// implementation; tests provide a fake.
type Notifier interface {
	Notify(title, body string)
}

// NotifierFunc adapts a function to the Notifier interface.
type NotifierFunc func(title, body string)

// Notify implements Notifier.
func (f NotifierFunc) Notify(title, body string) { f(title, body) }

// Engine probes a set of connectivity targets on demand, maintains rolling
// loss windows, and emits debounced connectivity notifications.
type Engine struct {
	timeout  time.Duration
	notifier Notifier

	mu          sync.Mutex
	targets     []Target
	perTarget   map[string]*LossWindow
	status      map[string]TargetStatus
	internet    *LossWindow
	alert       AlertState
	icmpUsable  bool // observed at least one successful ICMP probe
	sawFallback bool // observed at least one TCP fallback
}

// NewEngine creates an Engine for the given targets. windowSize is the rolling
// window length used for loss calculations.
func NewEngine(targets []Target, windowSize int, timeout time.Duration, n Notifier) *Engine {
	e := &Engine{
		timeout:   timeout,
		notifier:  n,
		targets:   targets,
		perTarget: make(map[string]*LossWindow, len(targets)),
		status:    make(map[string]TargetStatus, len(targets)),
		internet:  NewLossWindow(windowSize),
	}
	for _, t := range targets {
		e.perTarget[t.ID] = NewLossWindow(windowSize)
	}
	return e
}

// RunOnce probes every target once, updates rolling loss, and fires a
// connectivity notification if the aggregate state changed. The aggregate is
// "internet up" when ANY target is reachable.
func (e *Engine) RunOnce(ctx context.Context) {
	e.mu.Lock()
	targets := append([]Target(nil), e.targets...)
	e.mu.Unlock()

	// Probe every target concurrently so a round is bounded by the slowest single
	// probe (~one timeout), not the sum — otherwise, with N down targets each
	// hitting the timeout, a "1 second" round would take N×timeout during an
	// outage, exactly when responsiveness matters most.
	results := make([]ProbeResult, len(targets))
	var wg sync.WaitGroup
	for i, t := range targets {
		wg.Add(1)
		go func(i int, host string) {
			defer wg.Done()
			results[i] = Probe(ctx, host, e.timeout)
		}(i, t.Host)
	}
	wg.Wait()

	anyReachable := false
	now := time.Now()
	e.mu.Lock()
	for i, t := range targets {
		res := results[i]
		if res.Mode == ModeTCP {
			e.sawFallback = true
		} else if res.Success {
			e.icmpUsable = true
		}
		if res.Success {
			anyReachable = true
		}
		win := e.perTarget[t.ID]
		win.Add(res.Success)
		e.status[t.ID] = TargetStatus{
			Target:        t,
			Reachable:     res.Success,
			LossPercent:   win.LossPercent(),
			RTT:           res.RTT,
			Mode:          res.Mode,
			LastChecked:   now,
			UsingFallback: res.Mode == ModeTCP,
		}
	}
	// Aggregate internet health: a successful round is one where at least one
	// target answered.
	e.internet.Add(anyReachable)
	e.mu.Unlock()
	level := ClassifyLoss(e.internet.LossPercent())

	e.mu.Lock()
	note := e.alert.Transition(level)
	e.mu.Unlock()

	if note != nil && e.notifier != nil {
		e.notifier.Notify(note.Title, note.Body)
	}
}

// Snapshot returns the current status of all targets in their configured order.
func (e *Engine) Snapshot() []TargetStatus {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]TargetStatus, 0, len(e.targets))
	for _, t := range e.targets {
		if s, ok := e.status[t.ID]; ok {
			out = append(out, s)
		} else {
			out = append(out, TargetStatus{Target: t})
		}
	}
	return out
}

// InternetLossPercent returns the aggregate rolling loss percentage — the share
// of recent rounds in which NO target answered (i.e. total-blackout rounds).
func (e *Engine) InternetLossPercent() float64 { return e.internet.LossPercent() }

// MinSampleCount returns the smallest per-target probe count — the partial-loss
// warm-up gate uses it so no alert fires until every target's window has enough
// samples to compute a trustworthy loss percentage.
func (e *Engine) MinSampleCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.minSampleCountLocked()
}

func (e *Engine) minSampleCountLocked() int {
	if len(e.targets) == 0 {
		return 0
	}
	min := -1
	for _, t := range e.targets {
		if w := e.perTarget[t.ID]; w != nil {
			if c := w.Count(); min < 0 || c < min {
				min = c
			}
		}
	}
	if min < 0 {
		return 0
	}
	return min
}

// AvgTargetLossPercent returns the MEAN rolling packet-loss across all targets.
//
// Averaging (rather than the worst single target) is deliberate: the targets are
// independent public resolvers (Google + Cloudflare DNS), so loss on just one is
// far more likely a remote-path/resolver problem than the user's own connection,
// and diluting it avoids the false "you're losing packets" alarms that a single
// flaky path would otherwise raise. The signal only climbs when MULTIPLE targets
// degrade together — i.e. when the loss is genuinely on the user's side. Total
// blackout (every target down) is owned separately by TotalLossDetector.
func (e *Engine) AvgTargetLossPercent() float64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.avgTargetLossLocked()
}

func (e *Engine) avgTargetLossLocked() float64 {
	if len(e.targets) == 0 {
		return 0
	}
	sum := 0.0
	for _, t := range e.targets {
		if w := e.perTarget[t.ID]; w != nil {
			sum += w.LossPercent()
		}
	}
	return sum / float64(len(e.targets))
}

// LossSnapshot returns the warm-up sample count and the average per-target loss
// read under a SINGLE lock, so the partial-loss escalator sees a consistent pair
// (no sample can land between two separate reads). Callers should prefer this over
// calling MinSampleCount and AvgTargetLossPercent back to back.
func (e *Engine) LossSnapshot() (minSamples int, avgLoss float64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.minSampleCountLocked(), e.avgTargetLossLocked()
}

// ResetLoss clears every rolling loss window (per-target and aggregate) and the
// connectivity alert state. Used when a total blackout ends so the just-ended
// outage doesn't linger in the windows and re-trigger partial-loss alerts.
func (e *Engine) ResetLoss() {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, w := range e.perTarget {
		w.Reset()
	}
	e.internet.Reset()
	e.alert = AlertState{}
}

// FallbackNote returns a non-empty UI note when the TCP fallback is in use
// because ICMP required privileges this process lacks.
func (e *Engine) FallbackNote() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.sawFallback && !e.icmpUsable {
		return "ICMP unavailable without elevated privileges — using TCP:443 fallback probes."
	}
	return ""
}
