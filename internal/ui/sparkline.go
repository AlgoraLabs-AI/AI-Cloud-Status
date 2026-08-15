package ui

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"sort"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/history"
)

// sparkSize is the on-screen size of the uptime sparkline.
var sparkSize = fyne.NewSize(84, 16)

// uptimeWindowSamples is the nominal sample COUNT the CONNECTIVITY uptime
// window spans: the ping strip shows "what just happened" (interval × 20, e.g.
// 20s at a 1s cadence). Provider strips use the fixed providerUptimeWindow
// instead.
const uptimeWindowSamples = 20

// providerUptimeWindow is the fixed uptime-strip window for provider and
// custom-URL rows. Cloud/AI providers rarely fail and recover within minutes,
// so the old interval×20 window (10-20min) was almost always fully green; a
// day answers the question the strip exists for — "did this go down today, and
// when?". Chosen over 4-12h per a 3-model design review (2026-07-17 dev-note).
const providerUptimeWindow = 24 * time.Hour

// sparkBuckets is the number of logical time buckets a BUCKETED strip is
// reduced to before painting (~17min per bucket at 24h). It matches the strip's
// logical pixel width so every bucket maps to at least one pixel column.
const sparkBuckets = 84

// windowedSamples returns the tail of samples that falls within window of end —
// the fixed recent slice the uptime % and sparkline BOTH read, so they can
// never disagree. A non-positive window keeps everything.
func windowedSamples(samples []history.Sample, window time.Duration, end time.Time) []history.Sample {
	if window <= 0 || len(samples) == 0 {
		return samples
	}
	cutoff := end.Add(-window)
	for i := len(samples) - 1; i >= 0; i-- {
		if samples[i].Time.Before(cutoff) {
			return samples[i+1:]
		}
	}
	return samples
}

// bucketAnchor quantizes now up to the next whole minute: the bucketed strip's
// window end. Keeping the bucket grid stable for a minute at a time lets the
// per-second refresh skip recomputing 24h of samples per row (a sub-pixel
// drift per second is invisible at ~17min per pixel).
func bucketAnchor(now time.Time) time.Time {
	return now.Truncate(time.Minute).Add(time.Minute)
}

// bucketPaints reduces per-sample paints onto sparkBuckets fixed time buckets
// spanning [end-window, end], taking the WORST paint present in each bucket
// (down > degraded > ok) so even a single bad poll stays visible when many
// samples share one pixel. Buckets with no samples — the app was off, or the
// check hadn't started — are samplePaintUnknown (grey): unknown is never
// painted as up. samples and paints are parallel slices.
func bucketPaints(samples []history.Sample, paints []int, window time.Duration, end time.Time) []int {
	out := make([]int, sparkBuckets)
	for i := range out {
		out[i] = samplePaintUnknown
	}
	if window <= 0 {
		return out
	}
	start := end.Add(-window)
	span := window / sparkBuckets
	if span <= 0 {
		return out
	}
	for i, s := range samples {
		if s.Time.Before(start) || s.Time.After(end) {
			continue
		}
		idx := int(s.Time.Sub(start) / span)
		if idx >= sparkBuckets {
			idx = sparkBuckets - 1
		}
		if paints[i] > out[idx] {
			out[idx] = paints[i]
		}
	}
	return out
}

// samplePaints maps each sample to its current strip colour (ok / degraded /
// down) under the active region de-selections, so a deselected region's bands
// recolour in place.
func samplePaints(samples []history.Sample, isMuted func(region string) bool) []int {
	paints := make([]int, len(samples))
	for i, s := range samples {
		paints[i] = samplePaint(s, isMuted)
	}
	return paints
}

// uptimeFraction returns the fraction of OBSERVED samples that are NOT a full
// outage, in [0,1]. Degraded (amber) samples count as up — a degraded service
// is still reachable — so only red bands lower the percentage. Unknown samples
// (unreadable feed) are excluded from the denominator entirely: counting them
// as up would fake availability, counting them as down would fake an outage.
// Returns -1 when there are no observed samples at all.
func uptimeFraction(paints []int) float64 {
	observed, notDown := 0, 0
	for _, p := range paints {
		if p == samplePaintUnknown {
			continue
		}
		observed++
		if p != samplePaintDown {
			notDown++
		}
	}
	if observed == 0 {
		return -1
	}
	return float64(notDown) / float64(observed)
}

// formatUptimePercent renders an uptime fraction for the strip label.
//
// It refuses to round a value that is not exactly 100% UP to "100%". For a
// monitoring app that is the single most damaging rounding available: a check
// that dropped two probes reads 99.87%, printed a flawless "100%", and sat
// directly above an "Outages (1)" list saying it went down — the row and its own
// drill-down contradicting each other over a display artefact. The same applies
// at the bottom: a check that was up for four minutes out of a day is not "0%",
// which reads as never up at all. Both edges fall back to one decimal, which is
// enough to separate them from the absolutes.
func formatUptimePercent(frac float64) string {
	pct := frac * 100
	switch {
	case pct >= 100:
		return "100%"
	case pct <= 0:
		return "0%"
	case pct > 99.5: // would round to a perfect 100%
		return fmt.Sprintf("%.1f%%", math.Floor(pct*10)/10)
	case pct < 0.5: // would round to a total blackout
		return fmt.Sprintf("%.1f%%", math.Ceil(pct*10)/10)
	default:
		return fmt.Sprintf("%.0f%%", pct)
	}
}

// uptimeWindowEnd is where the uptime window closes for a check.
//
// A BUCKETED strip (providers, custom URLs) is a fixed span anchored to NOW: it
// shows the last 24h of wall clock, with uncovered time painted grey. A plain
// strip (connectivity) is a short rolling window anchored to the newest sample.
//
// This exists as one function because regionUptime and uptimeDisplay each used
// to pick their own anchor, and the table SORTS by the first while DISPLAYING
// the second. After a gap longer than the window — a check re-enabled after two
// days, or a laptop resumed from a long suspend — windowedSamples returns
// nothing for the now-anchored end while the sample-anchored one still finds a
// full window, so the cell read "—" while the row was ranked by a stale
// fraction and sorted to the top of its section. regionUptime's own doc claimed
// "the ordering, the percentage, and the sparkline all agree"; they did not.
func uptimeWindowEnd(series []history.Sample, bucketed bool, now time.Time) time.Time {
	if bucketed {
		return bucketAnchor(now)
	}
	if len(series) > 0 {
		return series[len(series)-1].Time
	}
	return now
}

// regionUptime returns the region-aware uptime fraction for check id over window,
// plus the number of samples it covers (0 = no history). It is the muting-aware
// replacement for history.Uptime and feeds the uptime SORT, over exactly the
// samples uptimeDisplay renders — see uptimeWindowEnd.
func (c *Controller) regionUptime(id string, window time.Duration, bucketed bool, isMuted func(region string) bool, now time.Time) (float64, int) {
	series := c.history.Series(id)
	if len(series) == 0 {
		return 0, 0
	}
	samples := windowedSamples(series, window, uptimeWindowEnd(series, bucketed, now))
	if len(samples) == 0 {
		return 0, 0
	}
	frac := uptimeFraction(samplePaints(samples, isMuted))
	if frac < 0 {
		return 0, 0 // only unknown samples in range → uptime is unknowable
	}
	return frac, len(samples)
}

// uptimeDisplay resolves check id's strip paints and uptime percentage text
// under the current region de-selections — the shared inputs of the row uptime
// cell (rowWidget) and the detail panel's uptimeCell. For a BUCKETED strip
// (providers/custom URLs: fixed time window anchored to now) the paints are
// sparkBuckets worst-of-bucket time buckets with grey for uncovered time; for a
// plain strip (connectivity: short rolling window anchored to the newest
// sample) they are the per-sample paints unchanged. The percentage is always
// the fraction of OBSERVED samples that were not down — unknown time never
// counts as up. Returns (nil, "—") when there is no history in range.
func (c *Controller) uptimeDisplay(id string, window time.Duration, bucketed bool, isMuted func(region string) bool, now time.Time) ([]int, string) {
	series := c.history.Series(id)
	end := uptimeWindowEnd(series, bucketed, now)
	samples := windowedSamples(series, window, end)
	if len(samples) == 0 {
		return nil, "—"
	}
	paints := samplePaints(samples, isMuted)
	// Just the percentage — the cadence/window is stated once per section header,
	// not repeated on every row (all rows in a section share the same schedule).
	// An all-unknown window renders the strip but no percentage.
	pct := "—"
	if frac := uptimeFraction(paints); frac >= 0 {
		pct = formatUptimePercent(frac)
	}
	if bucketed {
		return bucketPaints(samples, paints, window, end), pct
	}
	return paints, pct
}

// uptimeCell renders a compact uptime sparkline plus the uptime percentage for
// check id over the fixed window, drawn from its history ring buffer and scoped to
// the user's current region de-selections. It returns a plain em dash when there
// is no history yet. Used by the transient detail panel; the table rows keep a
// persistent, in-place-updated variant (see rowWidget).
func (c *Controller) uptimeCell(id string, window time.Duration, bucketed bool, isMuted func(region string) bool) fyne.CanvasObject {
	paints, label := c.uptimeDisplay(id, window, bucketed, isMuted, time.Now())
	if len(paints) == 0 {
		return widget.NewLabel("—")
	}
	spark := canvas.NewRaster(func(w, h int) image.Image { return paintUptimeImage(paints, w, h) })
	box := container.NewGridWrap(sparkSize, spark)
	return container.NewHBox(box, monoLabel(label))
}

// latencyChartSize is the on-screen size of the drill-down latency chart. It is
// far wider and taller than the row sparkline because it is the panel's hero
// element rather than a cell decoration.
var latencyChartSize = fyne.NewSize(480, 64)

// paintLatencyImage paints a latency column chart: one column per horizontal
// pixel, its height proportional to the latency of the samples that map to it
// against the window's own maximum, with a full-height red column wherever a
// probe was LOST.
//
// It exists because the uptime strip beside it answers only "was it up", and a
// connection can be technically up while being unusable. Latency has no
// representation anywhere else in the app: the table shows a single instantaneous
// number, so a link that swings between 5ms and 700ms looks identical to a steady
// one. This is the only place the SHAPE of the latency is visible.
//
// Columns aggregate rather than sample: each column takes the WORST (highest)
// latency in its slice and marks the column lost if ANY probe in it was lost, so
// a single dropped packet among 600 can never be scaled out of existence.
//
// Two things are deliberate. The scale is the 95th percentile, not the peak: one
// 3-second timeout-adjacent outlier against a 20ms baseline would otherwise
// flatten the entire chart into a featureless smear at the bottom — bars past
// the scale simply clip. And a lost probe is drawn at FULL height while a
// latency bar is capped below it, so the two differ in shape and not only in
// hue; the rest of this app pairs colour with a second cue for the same reason
// (see visualFor's dot-plus-icon badges).
func paintLatencyImage(samples []history.Sample, w, h int) image.Image {
	barCol := color.NRGBA{R: 0x2e, G: 0xa0, B: 0x43, A: 0xff}   // green: measured latency
	downCol := color.NRGBA{R: 0xd7, G: 0x2c, B: 0x2c, A: 0xff}  // red: probe lost
	floorCol := color.NRGBA{R: 0x80, G: 0x80, B: 0x80, A: 0x33} // faint baseline
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	n := len(samples)
	if n == 0 || w <= 0 || h <= 0 {
		return img
	}
	scale := latencyScale(samples)
	// Latency bars never fill the column, leaving the full-height red band
	// unambiguous even in greyscale.
	maxBar := h - h/8
	if maxBar < 1 {
		maxBar = 1
	}

	lost := make([]bool, w)
	measured := make([]bool, w)
	worst := make([]time.Duration, w)
	for x := 0; x < w; x++ {
		lo, hi := x*n/w, (x+1)*n/w
		if hi <= lo {
			hi = lo + 1
		}
		if hi > n {
			hi = n
		}
		for _, s := range samples[lo:hi] {
			if s.Unknown {
				continue
			}
			if !s.Up && !s.Responded {
				lost[x] = true
				continue
			}
			measured[x] = true
			if s.Latency > worst[x] {
				worst[x] = s.Latency
			}
		}
	}
	lost = widenLoss(lost)

	for x := 0; x < w; x++ {
		switch {
		case lost[x]:
			for y := 0; y < h; y++ {
				img.SetNRGBA(x, y, downCol)
			}
		case measured[x] && scale > 0:
			// At least one pixel so even a sub-millisecond reply is visibly a bar.
			bar := int(float64(maxBar) * float64(worst[x]) / float64(scale))
			if bar < 1 {
				bar = 1
			}
			if bar > maxBar {
				bar = maxBar // clipped: past the scale, height stops carrying meaning
			}
			for y := h - bar; y < h; y++ {
				img.SetNRGBA(x, y, barCol)
			}
		default:
			img.SetNRGBA(x, h-1, floorCol) // nothing observed in this slice
		}
	}
	return img
}

// minLossPx is the narrowest a loss marker may be drawn. A day of 60s polls is
// ~1400 samples squeezed into ~480 columns, so two dropped probes land in a
// SINGLE column — one hairline that reads as a rendering speck, or as nothing at
// all. Aggregating the worst sample per column is what keeps the outage from
// being averaged away; this is what keeps it from being too thin to see. Three
// pixels is still honest: the marker says "an outage is here", and the times are
// stated exactly in the outage list below the chart.
const minLossPx = 3

// widenLoss dilates each loss column to minLossPx so a brief outage is visible.
//
// It stands down on a chart too narrow for the marker to be a detail rather than
// the whole picture: during layout the raster can be asked for a handful of
// pixels, and widening there would repaint the entire chart as an outage.
func widenLoss(lost []bool) []bool {
	if len(lost) < 4*minLossPx {
		return lost
	}
	out := make([]bool, len(lost))
	grow := minLossPx / 2
	for x, isLost := range lost {
		if !isLost {
			continue
		}
		lo, hi := x-grow, x+minLossPx-1-grow
		if lo < 0 {
			lo = 0
		}
		if hi >= len(lost) {
			hi = len(lost) - 1
		}
		for i := lo; i <= hi; i++ {
			out[i] = true
		}
	}
	return out
}

// latencyScale is the 95th-percentile measured latency, the chart's full-bar
// height. Scaling to the maximum instead would hand the whole vertical range to
// a single outlier and leave the baseline it is supposed to be compared against
// invisible.
func latencyScale(samples []history.Sample) time.Duration {
	vals := make([]time.Duration, 0, len(samples))
	for _, s := range samples {
		if s.Unknown || (!s.Up && !s.Responded) {
			continue
		}
		vals = append(vals, s.Latency)
	}
	if len(vals) == 0 {
		return 0
	}
	sort.Slice(vals, func(i, j int) bool { return vals[i] < vals[j] })
	idx := (len(vals)*95 + 99) / 100 // nearest-rank p95, 1-based
	if idx > len(vals) {
		idx = len(vals)
	}
	return vals[idx-1]
}

// latencyChart renders the latency column chart for a series, or nil when there
// is nothing to draw.
func latencyChart(samples []history.Sample) fyne.CanvasObject {
	if len(samples) == 0 {
		return nil
	}
	chart := canvas.NewRaster(func(w, h int) image.Image { return paintLatencyImage(samples, w, h) })
	return container.NewGridWrap(latencyChartSize, chart)
}

// paintUptimeImage paints one vertical band per horizontal pixel column — green
// when the mapped sample is operational, amber when degraded, red when down — a
// colour-and-position uptime strip that reads at a glance. It takes the
// already-resolved per-sample paint so the colours match the percentage and the
// Status dot.
func paintUptimeImage(paints []int, w, h int) image.Image {
	okCol, degCol, downCol, unkCol := stripOKColor, stripDegradedColor, stripDownColor, stripUnknownColor
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	n := len(paints)
	if n == 0 || w <= 0 || h <= 0 {
		return img
	}
	for x := 0; x < w; x++ {
		idx := x * n / w
		if idx >= n {
			idx = n - 1
		}
		col := okCol
		switch paints[idx] {
		case samplePaintDown:
			col = downCol
		case samplePaintDegraded:
			col = degCol
		case samplePaintUnknown:
			col = unkCol
		}
		for y := 0; y < h; y++ {
			img.SetNRGBA(x, y, col)
		}
	}
	return img
}
