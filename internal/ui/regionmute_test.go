package ui

import (
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/widget"

	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/config"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/providers"
)

// newRegionTestController builds a Controller with just the fields the region
// mute logic touches.
func newRegionTestController(muted map[string]int64, session map[string]bool) *Controller {
	cfg := config.Default()
	cfg.RegionMutedUntil = muted
	c := &Controller{cfg: cfg, sessionDisabledRegions: session}
	if c.sessionDisabledRegions == nil {
		c.sessionDisabledRegions = map[string]bool{}
	}
	return c
}

func major(regions ...string) providers.Incident {
	return providers.Incident{Summary: "x", Severity: providers.SevMajor, Regions: regions}
}

func TestRegionAlertSuppressed(t *testing.T) {
	forever := map[string]int64{"us-east-1": config.RegionMuteForever}

	cases := []struct {
		name   string
		muted  map[string]int64
		incs   []providers.Incident
		expect bool
	}{
		{
			name:   "no major incident → nothing to suppress",
			muted:  forever,
			incs:   []providers.Incident{{Summary: "minor", Severity: providers.SevMinor, Regions: []string{"us-east-1"}}},
			expect: false,
		},
		{
			name:   "major incident in a muted region → suppress",
			muted:  forever,
			incs:   []providers.Incident{major("us-east-1")},
			expect: true,
		},
		{
			name:   "substring-tolerant match (feed form vs key form) → suppress",
			muted:  forever,
			incs:   []providers.Incident{major("US East (us-east-1)")},
			expect: true,
		},
		{
			name:   "major incident in an UNmuted region → alert",
			muted:  forever,
			incs:   []providers.Incident{major("eu-west-1")},
			expect: false,
		},
		{
			name:   "global major incident is never region-suppressible",
			muted:  forever,
			incs:   []providers.Incident{major( /* no regions = global */ )},
			expect: false,
		},
		{
			name:   "one muted + one unmuted major → alert (the unmuted one)",
			muted:  forever,
			incs:   []providers.Incident{major("us-east-1"), major("eu-west-1")},
			expect: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newRegionTestController(tc.muted, nil)
			res := providers.Result{Feed: providers.FeedReachable, Incidents: tc.incs}
			if got := c.regionAlertSuppressed(res, nil); got != tc.expect {
				t.Fatalf("regionAlertSuppressed = %v, want %v", got, tc.expect)
			}
		})
	}
}

// The Active-incidents cell must drop incidents whose every region is
// deactivated (they'd contradict the green Status), keep incidents with any
// active region, and always keep global ones.
func TestEffectivelyActiveIncidents(t *testing.T) {
	muted := func(r string) bool { return r == "me-central-1" }
	incs := []providers.Incident{
		{Summary: "zombie", Regions: []string{"me-central-1"}},
		{Summary: "live", Regions: []string{"us-east-1"}},
		{Summary: "mixed", Regions: []string{"me-central-1", "us-east-1"}},
		{Summary: "global"},
	}
	got := effectivelyActiveIncidents(incs, muted)
	want := []string{"live", "mixed", "global"}
	if len(got) != len(want) {
		t.Fatalf("kept %d incidents, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].Summary != w {
			t.Errorf("kept[%d] = %q, want %q", i, got[i].Summary, w)
		}
	}
}

// unmutedSeverity must rank a provider by its worst in-scope INCIDENT, not by
// the feed's aggregate indicator — regression for the 2026-07-16 Anthropic
// outage, where a major-impact incident coexisted with a sub-major page
// indicator: the row showed "Outage" (incident-based) while history samples
// recorded up (indicator-based), painting the uptime strip green through a
// displayed outage and suppressing the notification.
func TestUnmutedSeverityIncidentFirst(t *testing.T) {
	cases := []struct {
		name     string
		res      providers.Result
		interest []string
		expect   providers.Severity
	}{
		{
			name: "sub-major indicator + global major incident → major (the Anthropic case)",
			res: providers.Result{
				Severity:  providers.SevMinor,
				Incidents: []providers.Incident{major( /* global */ )},
			},
			expect: providers.SevMajor,
		},
		{
			name:   "major indicator with no incidents → aggregate stands",
			res:    providers.Result{Severity: providers.SevMajor},
			expect: providers.SevMajor,
		},
		{
			name: "major indicator but only a minor incident → minor (matches the row)",
			res: providers.Result{
				Severity:  providers.SevMajor,
				Incidents: []providers.Incident{{Summary: "minor", Severity: providers.SevMinor}},
			},
			expect: providers.SevMinor,
		},
		{
			name: "regional major incident outside the regions of interest → none",
			res: providers.Result{
				Severity:  providers.SevMinor,
				Incidents: []providers.Incident{major("us-east-1")},
			},
			interest: []string{"eu-west-1"},
			expect:   providers.SevNone,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := unmutedSeverity(tc.res, tc.interest); got != tc.expect {
				t.Fatalf("unmutedSeverity = %v, want %v", got, tc.expect)
			}
		})
	}
}

func TestRegionMutedSessionAndForever(t *testing.T) {
	c := newRegionTestController(
		map[string]int64{"forever-region": config.RegionMuteForever, "expired": 1},
		map[string]bool{"session-region": true},
	)
	if !c.regionMuted("forever-region") {
		t.Error("forever mute should read as muted")
	}
	if !c.regionMuted("session-region") {
		t.Error("session (until-restart) mute should read as muted")
	}
	if c.regionMuted("expired") {
		t.Error("an expired timed mute should NOT read as muted")
	}
	if c.regionMuted("not-muted") {
		t.Error("an unmuted region should read as not muted")
	}
}

// tappableBadge must consume taps itself (implement Tappable) and must NOT be
// focusable, so a badge click opens the region popup without also triggering the
// row's provider-detail activation or stealing keyboard focus.
var (
	_ fyne.Tappable = (*tappableBadge)(nil)
)

func TestTappableBadgeNotFocusable(t *testing.T) {
	b := newTappableBadge(widget.NewLabel("x"), nil)
	if _, ok := interface{}(b).(fyne.Focusable); ok {
		t.Fatal("tappableBadge must not be Focusable (it would compete with the row for Tab traversal)")
	}
	tapped := false
	b2 := newTappableBadge(widget.NewLabel("y"), func() { tapped = true })
	b2.Tapped(&fyne.PointEvent{})
	if !tapped {
		t.Fatal("Tapped should invoke onTap")
	}
}
