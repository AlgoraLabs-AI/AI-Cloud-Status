package ui

import (
	"errors"
	"image/color"
	"strings"
	"testing"
	"time"

	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/config"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/history"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/i18n"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/monitor"
)

var detailBase = time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

// TestURLRowsAreClickable is the regression guard for the whole feature on the
// custom-URL side: the rows rendered fine for months while clicking them did
// nothing, because urlEntries never set activate. A nil activate is invisible —
// the row still draws, still updates, and simply ignores the click.
func TestURLRowsAreClickable(t *testing.T) {
	check := config.URLCheck{ID: "url-1", Name: "portal", URL: "https://example.com/health"}
	c := &Controller{
		cfg:       config.Config{CustomURLChecks: []config.URLCheck{check}},
		history:   history.New(10),
		offline:   monitor.NewOfflineDetector(),
		urlStates: map[string]urlState{"url-1": {checked: true, up: true, latency: 30 * time.Millisecond}},
	}
	entries := c.urlEntries(providerUptimeWindow, func(string) bool { return false })
	if len(entries) != 1 {
		t.Fatalf("urlEntries = %d rows, want 1", len(entries))
	}
	if entries[0].spec.activate == nil {
		t.Fatal("custom URL row has no activate — clicking it would do nothing")
	}
}

// TestOutageTextSaysAtLeastWhenRecoveryUnobserved is the honesty guard on the
// wording: a run whose recovery was never seen (the record stops because the app
// was closed) knows only a LOWER bound on its length, and must not be phrased
// like a measured duration.
func TestOutageTextSaysAtLeastWhenRecoveryUnobserved(t *testing.T) {
	i18n.Set("en")
	unobserved := history.DownRun{
		Start: detailBase,
		End:   detailBase.Add(4 * time.Minute),
	}
	got := outageText(unobserved, detailBase.Add(time.Hour))
	if !strings.Contains(got, "at least") {
		t.Errorf("outageText for an unobserved recovery = %q, want it to say \"at least\"", got)
	}
	if strings.Contains(got, "lasted") {
		t.Errorf("outageText = %q, must not claim a measured duration", got)
	}

	resolved := unobserved
	resolved.Recovered = detailBase.Add(5 * time.Minute)
	got = outageText(resolved, detailBase.Add(time.Hour))
	if !strings.Contains(got, "lasted") {
		t.Errorf("outageText for an observed recovery = %q, want \"lasted\"", got)
	}

	ongoing := history.DownRun{Start: detailBase, End: detailBase.Add(time.Minute), Ongoing: true}
	got = outageText(ongoing, detailBase.Add(10*time.Minute))
	if !strings.Contains(got, "ongoing") {
		t.Errorf("outageText for a live outage = %q, want \"ongoing\"", got)
	}
}

// TestOutageTextFlagsTruncatedStart pins that an outage whose beginning was
// never witnessed says so, instead of presenting the oldest retained sample as
// the moment it started.
func TestOutageTextFlagsTruncatedStart(t *testing.T) {
	i18n.Set("en")
	r := history.DownRun{Start: detailBase, End: detailBase.Add(time.Minute), Truncated: true}
	if got := outageText(r, detailBase.Add(time.Hour)); !strings.Contains(got, "began before") {
		t.Errorf("outageText = %q, want the truncated-start note", got)
	}
}

// TestObservedSpanLabelNeverInventsAWindow pins that a heading with too little
// history shows the no-data dash rather than "0s" or a fabricated range.
func TestObservedSpanLabelNeverInventsAWindow(t *testing.T) {
	i18n.Set("en")
	if got := observedSpanLabel(nil); got != i18n.T().NoData {
		t.Errorf("observedSpanLabel(nil) = %q, want the no-data dash", got)
	}
	series := []history.Sample{
		{Time: detailBase, Up: true},
		{Time: detailBase.Add(90 * time.Second), Up: true},
	}
	if got := observedSpanLabel(series); got == i18n.T().NoData || got == "" {
		t.Errorf("observedSpanLabel over 90s of samples = %q, want a real span", got)
	}
}

// TestFormatPercentKeepsRareLossVisible pins the precision rule: one lost probe
// in 600 is 0.17%, and rounding it to a flat "0%" would hide exactly the packet
// loss the panel was opened to find.
func TestFormatPercentKeepsRareLossVisible(t *testing.T) {
	if got := formatPercent(100.0 / 600); got != "0.17%" {
		t.Errorf("formatPercent(1 in 600) = %q, want 0.17%%", got)
	}
	if got := formatPercent(0); got != "0%" {
		t.Errorf("formatPercent(0) = %q, want 0%%", got)
	}
	if got := formatPercent(12.4); got != "12%" {
		t.Errorf("formatPercent(12.4) = %q, want 12%%", got)
	}
}

// TestFormatRTTReportsSubMillisecondSuccess pins the difference from
// formatLatency: a successful Windows ICMP reply under 1ms is recorded as 0, and
// showing an em dash for it would read as "never measured".
func TestFormatRTTReportsSubMillisecondSuccess(t *testing.T) {
	if got := formatRTT(0); got != "<1ms" {
		t.Errorf("formatRTT(0) = %q, want <1ms (a sub-millisecond reply is a measurement)", got)
	}
	if got := formatLatency(0); got != "—" {
		t.Errorf("formatLatency(0) = %q, want the em dash (0 means unknown there)", got)
	}
	if got := formatRTT(12 * time.Millisecond); got != "12ms" {
		t.Errorf("formatRTT(12ms) = %q, want 12ms", got)
	}
}

// TestLatencyChartDistinguishesLossByShape pins that a lost probe is not encoded
// by hue alone: its column runs the FULL height while a latency bar is capped
// below the top, so the two are still tellable apart in greyscale or by a
// colour-blind reader.
func TestLatencyChartDistinguishesLossByShape(t *testing.T) {
	var samples []history.Sample
	for i := 0; i < 100; i++ {
		s := history.Sample{Time: detailBase.Add(time.Duration(i) * time.Second), Up: true, Latency: 20 * time.Millisecond}
		if i == 90 {
			s.Up, s.Latency = false, 0
		}
		samples = append(samples, s)
	}
	const w, h = 100, 16
	img := paintLatencyImage(samples, w, h)
	opaque := func(x int) int {
		n := 0
		for y := 0; y < h; y++ {
			if _, _, _, a := img.At(x, y).RGBA(); a > 0 {
				n++
			}
		}
		return n
	}
	if got := opaque(90); got != h {
		t.Errorf("lost-probe column height = %d, want the full %d", got, h)
	}
	// A healthy column, far from the widened loss marker.
	if got := opaque(10); got >= h {
		t.Errorf("latency column height = %d, want it capped below the full height so loss is distinguishable by shape", got)
	}
	if got := opaque(10); got == 0 {
		t.Error("latency column is invisible; a measured probe must draw at least one pixel")
	}
}

// TestLatencyChartScalesToP95NotPeak pins that a single outlier cannot flatten
// the chart: with one 3s spike against a 20ms baseline, peak scaling would
// render every normal bar as a 1px smear.
func TestLatencyChartScalesToP95NotPeak(t *testing.T) {
	var samples []history.Sample
	for i := 0; i < 40; i++ {
		samples = append(samples, history.Sample{
			Time: detailBase.Add(time.Duration(i) * time.Second), Up: true, Latency: 20 * time.Millisecond,
		})
	}
	samples = append(samples, history.Sample{
		Time: detailBase.Add(40 * time.Second), Up: true, Latency: 3 * time.Second,
	})
	img := paintLatencyImage(samples, 41, 32)
	height := 0
	for y := 0; y < 32; y++ {
		if _, _, _, a := img.At(0, y).RGBA(); a > 0 {
			height++
		}
	}
	if height < 8 {
		t.Errorf("baseline bar height = %d/32; a single outlier flattened the chart", height)
	}
}

// TestLatencyChartIgnoresUnknownSamples guards the raster against a provider
// series ever being pointed at it: an unreadable observation is not a lost
// probe and must not paint a red band.
func TestLatencyChartIgnoresUnknownSamples(t *testing.T) {
	samples := []history.Sample{{Time: detailBase, Unknown: true}}
	img := paintLatencyImage(samples, 1, 8)
	red := color.NRGBA{R: 0xd7, G: 0x2c, B: 0x2c, A: 0xff}
	for y := 0; y < 8; y++ {
		if img.At(0, y) == color.Color(red) {
			t.Fatal("an unknown sample painted the lost-probe band")
		}
	}
}

// TestOutageTextReportsResumptionInsteadOfBareUncertainty is the regression for
// the card the user flagged: it read "at least 1m (recovery not observed)" while
// the strip above it showed a green, fully-recovered day. Monitoring had simply
// stopped for 44 minutes; the service was observed healthy on return, and the
// card has to say so.
func TestOutageTextReportsResumptionInsteadOfBareUncertainty(t *testing.T) {
	i18n.Set("en")
	r := history.DownRun{
		Start:   detailBase,
		End:     detailBase.Add(61 * time.Second),
		Resumed: detailBase.Add(45 * time.Minute),
	}
	got := outageText(r, detailBase.Add(5*time.Hour))
	if !strings.Contains(got, "back up by") {
		t.Errorf("outageText = %q, want it to state when the check was seen healthy again", got)
	}
	if strings.Contains(got, "recovery not observed") {
		t.Errorf("outageText = %q, must not imply the service may still be down", got)
	}
	// The hole is never counted as downtime.
	if !strings.Contains(got, "at least 1m") {
		t.Errorf("outageText = %q, want the observed lower bound of 1m, not the 45m hole", got)
	}
}

// TestUptimePercentNeverFakesAPerfectDay is the regression for a row that showed
// "100%" for a check whose own drill-down listed an outage — 99.87% rounded up.
// For a monitoring app that is the most damaging rounding available.
func TestUptimePercentNeverFakesAPerfectDay(t *testing.T) {
	if got := formatUptimePercent(0.998404); got != "99.8%" {
		t.Errorf("formatUptimePercent(99.84%%) = %q, want 99.8%% — never a fake 100%%", got)
	}
	if got := formatUptimePercent(0.9999); got != "99.9%" {
		t.Errorf("formatUptimePercent(99.99%%) = %q, want 99.9%%", got)
	}
	if got := formatUptimePercent(1); got != "100%" {
		t.Errorf("formatUptimePercent(1.0) = %q, want an exact 100%%", got)
	}
	if got := formatUptimePercent(0); got != "0%" {
		t.Errorf("formatUptimePercent(0) = %q, want an exact 0%%", got)
	}
	// Symmetric: a check that was up for a few minutes of a day is not "0%".
	if got := formatUptimePercent(0.002); got != "0.2%" {
		t.Errorf("formatUptimePercent(0.2%%) = %q, want 0.2%% — not a fake total blackout", got)
	}
	if got := formatUptimePercent(0.87); got != "87%" {
		t.Errorf("formatUptimePercent(87%%) = %q, want a whole 87%%", got)
	}
}

// TestLatencyChartKeepsBriefOutageVisible is the regression for the field case:
// a day of 60s polls is ~1500 samples over ~480 columns, so two dropped probes
// landed in ONE column — a hairline that read as a rendering speck rather than an
// outage.
func TestLatencyChartKeepsBriefOutageVisible(t *testing.T) {
	var samples []history.Sample
	for i := 0; i < 1500; i++ {
		s := history.Sample{
			Time: detailBase.Add(time.Duration(i) * time.Minute), Up: true,
			Responded: true, Latency: 400 * time.Millisecond,
		}
		if i == 1300 || i == 1301 {
			s.Up, s.Responded, s.Latency = false, false, 0
		}
		samples = append(samples, s)
	}
	img := paintLatencyImage(samples, 480, 64)
	wide := 0
	for x := 0; x < 480; x++ {
		if _, _, _, a := img.At(x, 0).RGBA(); a > 0 { // only a lost column reaches the top
			wide++
		}
	}
	if wide < minLossPx {
		t.Errorf("outage marker is %dpx wide, want at least %dpx to be visible at all", wide, minLossPx)
	}
}

// TestOfflineDoesNotBlameTheEndpoint is the regression for the case the user hit:
// both DNS pings stopped answering — the machine's own internet was gone — and a
// custom URL check that could not complete a round-trip was nonetheless reported
// as the ENDPOINT being unreachable. A local outage must never be dressed up as
// a remote one.
func TestOfflineDoesNotBlameTheEndpoint(t *testing.T) {
	failed := urlState{checked: true, up: false, err: errProbe}

	online := urlStatusState(failed, true, false)
	if online != stateUnreachable {
		t.Errorf("with internet, a transport failure = %v, want stateUnreachable", online)
	}

	offline := urlStatusState(failed, true, true)
	if offline != stateOfflineUnknown {
		t.Errorf("without internet, a transport failure = %v, want stateOfflineUnknown", offline)
	}
	// Ranking below OK is what keeps the tray from aggregating the user's own
	// dead link into a cloud-provider outage.
	if stateOfflineUnknown.worse(stateOK) != stateOK {
		t.Error("stateOfflineUnknown must rank below stateOK so it cannot surface as the tray's worst state")
	}
	if stateOfflineUnknown.worse(stateOutage) != stateOutage {
		t.Error("a real outage elsewhere must still outrank the offline-unknown state")
	}
}

// TestOfflineCompletedResponseStillCounts pins the other half: a server that
// ANSWERED (a 500, or a 200 without the expected text) is judged on its merits
// even while the offline detector is tripped — the round-trip completing is
// itself proof something worked.
func TestOfflineCompletedResponseStillCounts(t *testing.T) {
	answered := urlState{checked: true, up: false, code: 500}
	if got := urlStatusState(answered, true, true); got != stateOutage {
		t.Errorf("a completed 500 while offline = %v, want stateOutage", got)
	}
}

var errProbe = errors.New("dial tcp: no such host")
