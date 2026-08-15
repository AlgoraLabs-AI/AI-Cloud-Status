package ui

import (
	"image/color"
	"testing"

	"fyne.io/fyne/v2/theme"

	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/providers"
)

func TestProviderState(t *testing.T) {
	cases := []struct {
		sev     providers.Severity
		checked bool
		want    statusState
	}{
		{providers.SevNone, false, statePending}, // not checked yet
		{providers.SevNone, true, stateOK},
		{providers.SevMinor, true, stateDegraded},
		{providers.SevMajor, true, stateOutage},
		{providers.SevCritical, true, stateOutage},
	}
	for _, tc := range cases {
		if got := providerState(tc.sev, tc.checked); got != tc.want {
			t.Errorf("providerState(%v, %v) = %v, want %v", tc.sev, tc.checked, got, tc.want)
		}
	}
}

// TestSeverityColorMatchesStrip pins the promise the severity labels make: the
// colour of the word "major" is the SAME colour the 24h uptime strip paints for
// a major outage, resolved end to end (severity → theme colour name → appTheme
// → pixel). A reader compares the two side by side, so a second red would read
// as a second meaning. Asserting against the rendered pixel, not the constant,
// is what keeps the painter and the labels from drifting apart.
func TestSeverityColorMatchesStrip(t *testing.T) {
	th := newAppTheme()
	pixel := func(paint int) color.Color {
		return paintUptimeImage([]int{paint}, 1, 1).At(0, 0)
	}
	cases := []struct {
		sev   providers.Severity
		paint int
	}{
		{providers.SevCritical, samplePaintDown},
		{providers.SevMajor, samplePaintDown},
		{providers.SevMinor, samplePaintDegraded},
		{providers.SevNone, samplePaintOK},
	}
	for _, tc := range cases {
		got := th.Color(severityColorName(tc.sev), theme.VariantLight)
		if want := pixel(tc.paint); !sameColor(got, want) {
			t.Errorf("severity %v renders %v, but the strip paints %v", tc.sev, got, want)
		}
	}
}

func sameColor(a, b color.Color) bool {
	ar, ag, ab, aa := a.RGBA()
	br, bg, bb, ba := b.RGBA()
	return ar == br && ag == bg && ab == bb && aa == ba
}

// TestProviderStatusState verifies the three-state UI mapping: an unreadable or
// absent feed becomes stateNoFeed ("unknown") and is NEVER conflated with an
// outage, while a reachable result maps to real service health.
func TestProviderStatusState(t *testing.T) {
	interest := []string(nil)
	cases := []struct {
		name string
		st   provState
		ok   bool
		want statusState
	}{
		{"not checked", provState{}, false, statePending},
		{"feed error", provState{checked: true, err: errStub, res: providers.Result{Feed: providers.FeedUnreachable}}, true, stateNoFeed},
		{"no public feed", provState{checked: true, skipped: true}, true, stateNoFeed},
		{"unreachable result", provState{checked: true, res: providers.Result{Feed: providers.FeedUnreachable}}, true, stateNoFeed},
		{"operational", provState{checked: true, res: providers.Result{Feed: providers.FeedReachable, Severity: providers.SevNone}}, true, stateOK},
		{"degraded", provState{checked: true, res: providers.Result{Feed: providers.FeedReachable, Severity: providers.SevMinor}}, true, stateDegraded},
		{"outage", provState{checked: true, res: providers.Result{Feed: providers.FeedReachable, Severity: providers.SevCritical}}, true, stateOutage},
	}
	noMute := func(string) bool { return false }
	for _, tc := range cases {
		if got := providerStatusState(tc.st, tc.ok, interest, noMute); got != tc.want {
			t.Errorf("%s: providerStatusState = %v, want %v", tc.name, got, tc.want)
		}
	}
}

var errStub = stubError("feed unreachable")

type stubError string

func (e stubError) Error() string { return string(e) }

func TestConnState(t *testing.T) {
	cases := []struct {
		checked   bool
		reachable bool
		loss      float64
		want      statusState
	}{
		{false, false, 0, statePending},
		{true, true, 0, stateOK},
		{true, true, 30, stateDegraded},
		{true, true, 80, stateOutage},
		{true, false, 100, stateUnreachable},
	}
	for _, tc := range cases {
		if got := connState(tc.checked, tc.reachable, tc.loss); got != tc.want {
			t.Errorf("connState(%v,%v,%.0f) = %v, want %v", tc.checked, tc.reachable, tc.loss, got, tc.want)
		}
	}
}

// TestStatusStateWorse verifies the tray aggregation ordering: a confirmed
// outage must outrank an unreachable feed, and pending ranks lowest.
func TestStatusStateWorse(t *testing.T) {
	if got := stateOK.worse(stateOutage); got != stateOutage {
		t.Errorf("OK.worse(Outage) = %v, want Outage", got)
	}
	if got := stateOutage.worse(stateUnreachable); got != stateOutage {
		t.Errorf("Outage.worse(Unreachable) = %v, want Outage", got)
	}
	if got := statePending.worse(stateOK); got != stateOK {
		t.Errorf("Pending.worse(OK) = %v, want OK", got)
	}
	if got := stateDegraded.worse(stateOK); got != stateDegraded {
		t.Errorf("Degraded.worse(OK) = %v, want Degraded", got)
	}
	// An unreadable feed ("unknown") must rank below a known reading so it never
	// masks ok/degraded/outage in the tray aggregate.
	if got := stateNoFeed.worse(stateOK); got != stateOK {
		t.Errorf("NoFeed.worse(OK) = %v, want OK", got)
	}
	if got := stateNoFeed.worse(stateOutage); got != stateOutage {
		t.Errorf("NoFeed.worse(Outage) = %v, want Outage", got)
	}
}

// TestVisualsAreDifferentiated guards the accessibility requirement: each state
// has a distinct, non-empty text label and an icon, so status reads without
// relying on colour.
func TestVisualsAreDifferentiated(t *testing.T) {
	states := []statusState{stateOK, stateDegraded, stateOutage, stateUnreachable, stateNoFeed, statePending}
	labels := map[string]bool{}
	for _, s := range states {
		v := visualFor(s)
		if v.Label == "" {
			t.Errorf("state %v has empty label", s)
		}
		if v.Icon == nil {
			t.Errorf("state %v has no icon", s)
		}
		if v.Color == nil {
			t.Errorf("state %v has no colour", s)
		}
		if labels[v.Label] {
			t.Errorf("duplicate label %q across states", v.Label)
		}
		labels[v.Label] = true
	}
	if len(labels) < 4 {
		t.Errorf("only %d differentiated states, want at least 4", len(labels))
	}
}

func TestTraySummary(t *testing.T) {
	cases := map[statusState]string{
		stateOK:          "All operational",
		stateDegraded:    "Degraded",
		stateOutage:      "Outage",
		stateUnreachable: "Unreachable",
		stateNoFeed:      "Status feed unavailable",
		statePending:     "Checking…",
	}
	for s, want := range cases {
		if got := traySummary(s); got != want {
			t.Errorf("traySummary(%v) = %q, want %q", s, got, want)
		}
	}
}

func TestReducedMotionEnv(t *testing.T) {
	t.Setenv("AI_STATUS_PINGER_REDUCED_MOTION", "")
	if reducedMotion() {
		t.Error("empty env should not enable reduced motion")
	}
	t.Setenv("AI_STATUS_PINGER_REDUCED_MOTION", "1")
	if !reducedMotion() {
		t.Error("\"1\" should enable reduced motion")
	}
	if transitionDuration() != 0 {
		t.Error("reduced motion should yield zero transition duration")
	}
	t.Setenv("AI_STATUS_PINGER_REDUCED_MOTION", "false")
	if reducedMotion() {
		t.Error("\"false\" should disable reduced motion")
	}
	if transitionDuration() == 0 {
		t.Error("non-reduced motion should yield a non-zero duration")
	}
}
