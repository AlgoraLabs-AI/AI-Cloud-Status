package monitor

import "fmt"

// ConnLevel classifies overall internet connectivity by packet-loss severity.
type ConnLevel int

const (
	// ConnOK means loss is at or below the warning threshold (<=10%).
	ConnOK ConnLevel = iota
	// ConnDegraded means loss is above 10% (and at or below 50%).
	ConnDegraded
	// ConnBad means loss is above 50% (but not total).
	ConnBad
	// ConnDown means total loss / no internet (100%).
	ConnDown
)

// Loss thresholds expressed as percentages.
const (
	WarnLossPercent = 10.0
	HighLossPercent = 50.0

	// LossEnterPercent / LossExitPercent give the partial-loss alert hysteresis:
	// it only fires once average loss exceeds Enter, and only clears once it drops
	// below Exit, so loss hovering near a single threshold can't flap the alert.
	LossEnterPercent = 15.0
	LossExitPercent  = 5.0

	// MinLossSamples is the per-target warm-up: no partial-loss alert until at
	// least this many probes exist, so a single miss against a near-empty window
	// (e.g. right after a blackout recovery clears it) can't read as huge loss.
	MinLossSamples = 20
)

// ClassifyLoss maps a packet-loss percentage to a ConnLevel.
func ClassifyLoss(lossPercent float64) ConnLevel {
	switch {
	case lossPercent >= 100:
		return ConnDown
	case lossPercent > HighLossPercent:
		return ConnBad
	case lossPercent > WarnLossPercent:
		return ConnDegraded
	default:
		return ConnOK
	}
}

func (l ConnLevel) String() string {
	switch l {
	case ConnOK:
		return "ok"
	case ConnDegraded:
		return "degraded"
	case ConnBad:
		return "bad"
	case ConnDown:
		return "down"
	default:
		return "unknown"
	}
}

// Notification is a desktop alert to be surfaced to the user. Recovery marks
// good-news notifications (a check/provider/connection returning to healthy):
// they are informational, so the UI presents them without silence/disable
// actions.
type Notification struct {
	Title    string
	Body     string
	Recovery bool
}

// AlertState debounces connectivity notifications so that exactly one alert is
// emitted per state change (plus a recovery alert when connectivity returns).
// The zero value is ready to use.
type AlertState struct {
	level       ConnLevel
	initialized bool
}

// Transition records the latest classified level and returns a Notification to
// emit, or nil if no notification is warranted (no change, or steady OK state).
func (a *AlertState) Transition(level ConnLevel) *Notification {
	// First observation: only alert if we are already in a bad state.
	if !a.initialized {
		a.initialized = true
		a.level = level
		if level == ConnOK {
			return nil
		}
		return notificationFor(level)
	}

	if level == a.level {
		return nil
	}
	a.level = level

	if level == ConnOK {
		return &Notification{
			Title:    "Internet restored",
			Body:     "Connectivity has recovered.",
			Recovery: true,
		}
	}
	return notificationFor(level)
}

// Level returns the current connectivity level.
func (a *AlertState) Level() ConnLevel { return a.level }

func notificationFor(level ConnLevel) *Notification {
	switch level {
	case ConnDegraded:
		return &Notification{
			Title: "Connectivity degraded",
			Body:  fmt.Sprintf("Packet loss above %.0f%%.", WarnLossPercent),
		}
	case ConnBad:
		return &Notification{
			Title: "Connectivity severely degraded",
			Body:  fmt.Sprintf("Packet loss above %.0f%%.", HighLossPercent),
		}
	case ConnDown:
		return &Notification{
			Title: "No internet connection",
			Body:  "All connectivity probes are failing.",
		}
	default:
		return nil
	}
}
