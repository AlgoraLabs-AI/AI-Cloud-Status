package ui

import (
	"fmt"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/config"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/history"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/i18n"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/monitor"
)

// showConnDetail opens the drill-down for a connectivity (ping) target.
//
// Providers get their panel from a status feed; a ping target has no feed at
// all, so everything here is reconstructed from the recorded samples: the shape
// of the latency, how much was lost, and the outages that runs of failed probes
// imply. The row can only ever show one instantaneous number — a link swinging
// between 5ms and 700ms looks identical there to a steady one.
//
// The status it renders comes from the TargetStatus the ROW was built from, not
// from a fresh engine snapshot: a poll landing between the paint and the click
// would otherwise make the panel contradict the row that opened it.
func (c *Controller) showConnDetail(s monitor.TargetStatus) {
	t := i18n.T()
	checked := !s.LastChecked.IsZero()
	v := visualFor(connState(checked, s.Reachable, s.LossPercent))

	window := c.connInterval() * uptimeWindowSamples
	series := c.history.Series(s.ID)

	mode := "ICMP"
	if s.Mode == monitor.ModeTCP {
		mode = "TCP:443"
	}
	latency := t.NoData
	if checked {
		if s.Reachable {
			latency = formatRTT(s.RTT)
		} else {
			latency = t.LatencyUnreachable
		}
	}

	body := container.NewVBox(
		selectable(widget.NewLabelWithStyle(s.Name+" — "+v.Label, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})),
		selectable(newWrappedLabel(mode+" · "+s.Host)),
		widget.NewSeparator(),
		selectable(widget.NewLabel(fmt.Sprintf(t.ConnDetailMeta, formatTime(s.LastChecked), latency, mode))),
	)
	if s.UsingFallback {
		body.Add(newWrappedLabel(t.ProbeFallbackNote))
	}
	// The same cell the row draws, over the same window, so at least one element
	// of the panel is pixel-identical to what was clicked.
	body.Add(container.NewHBox(widget.NewLabel(uptimeRowLabel(window)), c.uptimeCell(s.ID, window, false, c.muteSnapshot())))
	body.Add(widget.NewSeparator())

	addSampleStats(body, series, false)
	addOutageSection(body, series)

	scroll := container.NewVScroll(body)
	scroll.SetMinSize(fyne.NewSize(520, 420))
	dialog.ShowCustom(fmt.Sprintf(t.PingDetailsTitle, s.Name), t.Close, scroll, c.window)
}

// showURLDetail opens the drill-down for a custom URL check: its last response,
// the response-time figures over the retained window, and the outages implied by
// runs of failed probes — the same "what happened lately" the provider panel
// gets from a feed, derived instead from the check's own samples.
func (c *Controller) showURLDetail(u config.URLCheck, st urlState, ok bool) {
	t := i18n.T()
	v := visualFor(urlStatusState(st, ok, c.offline.Offline()))
	series := c.history.Series(u.ID)

	response := t.NoData
	if ok && st.checked {
		if st.err == nil {
			response = formatRTT(st.latency)
		} else {
			response = t.LatencyUnreachable
		}
	}
	result := st.detail
	if result == "" {
		result = t.NoData
	}

	body := container.NewVBox(
		selectable(widget.NewLabelWithStyle(u.Name+" — "+v.Label, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})),
		// Redacted like the row subtitle: a monitored URL can legitimately carry a
		// token in its userinfo, and this panel is on screen during screen shares.
		// The row deliberately protects that; the drill-down must not leak it.
		selectable(newWrappedLabel(urlModeLabel(u.Mode)+" · "+redactURL(u.URL))),
	)
	if u.Mode == config.URLModeContains && u.Expect != "" {
		body.Add(selectable(newWrappedLabel(fmt.Sprintf(t.ExpectedTextLabel, u.Expect))))
	}
	body.Add(widget.NewSeparator())
	body.Add(selectable(newWrappedLabel(fmt.Sprintf(t.URLDetailMeta, formatTime(st.when), response, result))))
	body.Add(container.NewHBox(widget.NewLabel(uptimeRowLabel(providerUptimeWindow)), c.uptimeCell(u.ID, providerUptimeWindow, true, c.muteSnapshot())))
	body.Add(widget.NewSeparator())

	addSampleStats(body, series, true)
	addOutageSection(body, series)

	scroll := container.NewVScroll(body)
	scroll.SetMinSize(fyne.NewSize(520, 420))
	dialog.ShowCustom(fmt.Sprintf(t.CheckDetailsTitle, u.Name), t.Close, scroll, c.window)
}

// uptimeRowLabel names the uptime strip together with the window it covers.
//
// The strip is deliberately the row's own cell over the row's own window, so the
// two always agree — but the statistics below it are computed over the whole
// retained series, which is a DIFFERENT and usually longer span. Leaving the
// strip unlabelled put a bare percentage over one window directly above a block
// headed with another, inviting the reader to reconcile two numbers that were
// never measuring the same thing.
func uptimeRowLabel(window time.Duration) string {
	t := i18n.T()
	if span := history.HumanizeSpan(window); span != "" {
		return t.ColUptime + " (" + fmt.Sprintf(t.UptimeRange, span) + "):"
	}
	return t.ColUptime + ":"
}

// addSampleStats appends the latency/loss block for a recorded series.
// asResponse switches the average's wording between a ping's latency and an HTTP
// check's response time.
//
// Every heading states the span the samples ACTUALLY cover — see
// history.ObservedSpan, which is the observed time and not the distance from the
// oldest sample to the newest. The ring is bounded by sample COUNT while the
// launch prune keeps a day of wall clock, so a connectivity series can hold
// yesterday's tail plus ten fresh minutes; a heading reading "last 8h" over that
// would misrepresent every figure beneath it.
func addSampleStats(body *fyne.Container, series []history.Sample, asResponse bool) {
	t := i18n.T()
	lat := history.Latency(series)
	if lat.Count == 0 {
		body.Add(widget.NewLabel(t.NoMeasurementsYet))
		return
	}
	span := observedSpanLabel(series)

	if chart := latencyChart(series); chart != nil {
		body.Add(widget.NewLabelWithStyle(fmt.Sprintf(t.LatencyTrend, span), fyne.TextAlignLeading, fyne.TextStyle{Bold: true}))
		body.Add(chart)
	}

	body.Add(widget.NewLabelWithStyle(fmt.Sprintf(t.StatsHeader, span), fyne.TextAlignLeading, fyne.TextStyle{Bold: true}))
	avg := t.StatAvgLatency
	if asResponse {
		avg = t.StatAvgResponse
	}
	body.Add(selectable(widget.NewLabel(fmt.Sprintf(avg, formatRTT(lat.Avg)))))
	body.Add(selectable(widget.NewLabel(fmt.Sprintf(t.StatMinMax, formatRTT(lat.Min), formatRTT(lat.Max)))))

	loss := history.Loss(series)
	if loss.Observed > 0 {
		body.Add(selectable(widget.NewLabel(fmt.Sprintf(t.StatPacketLoss, formatPercent(loss.Percent()), loss.Lost, loss.Observed))))
		// The same loss percentage means different things scattered or in one
		// block, so the longest unbroken run is stated whenever there was one.
		if loss.LongestStreak > 1 {
			body.Add(selectable(widget.NewLabel(fmt.Sprintf(t.StatLongestLoss, loss.LongestStreak))))
		}
	}
	body.Add(widget.NewSeparator())
}

// addOutageSection appends the reconstructed outage list for a series — the
// closest thing a feed-less check has to an incident history.
func addOutageSection(body *fyne.Container, series []history.Sample) {
	t := i18n.T()
	now := time.Now()
	span := observedSpanLabel(series)
	runs := history.DownRuns(series, now)
	if len(runs) == 0 {
		body.Add(widget.NewLabel(fmt.Sprintf(t.NoOutagesWindow, span)))
		return
	}
	body.Add(widget.NewLabelWithStyle(fmt.Sprintf(t.OutagesHeader, span, len(runs)), fyne.TextAlignLeading, fyne.TextStyle{Bold: true}))
	for _, r := range runs {
		body.Add(outageCard(r, now))
	}
}

// outageCard renders one reconstructed outage.
func outageCard(r history.DownRun, now time.Time) fyne.CanvasObject {
	label := selectable(widget.NewLabel(outageText(r, now)))
	label.Wrapping = fyne.TextWrapWord
	return container.NewPadded(label)
}

// outageText is the sentence describing one reconstructed outage.
//
// The wording tracks which bounds were actually observed. A polled check learns
// of a failure up to one interval late and of a recovery up to one interval
// early, so the times shown are the observed ones; and when the recovery was
// never seen at all — the record simply stops, because the app was closed — it
// reads "at least", never "lasted". Claiming a duration nobody measured is the
// one thing this list must not do.
func outageText(r history.DownRun, now time.Time) string {
	t := i18n.T()
	elapsed := formatElapsed(r.Duration(now))
	var text string
	switch {
	case r.Ongoing:
		text = fmt.Sprintf(t.OutageOngoing, formatSpanClock(r.Start, now), elapsed)
	case !r.Recovered.IsZero():
		text = fmt.Sprintf(t.OutageResolved, formatSpanClock(r.Start, now), formatSpanClock(r.Recovered, now), elapsed)
	case !r.Resumed.IsZero():
		// Monitoring stopped mid-outage and came back to a healthy check. The
		// duration is unknowable, but "it recovered, some time before HH:MM" is
		// not — and it is the difference between this card agreeing with the
		// green strip above it and appearing to contradict it.
		text = fmt.Sprintf(t.OutageResumedUp, formatSpanClock(r.Start, now), formatSpanClock(r.End, now),
			elapsed, formatSpanClock(r.Resumed, now))
	default:
		text = fmt.Sprintf(t.OutageAtLeast, formatSpanClock(r.Start, now), formatSpanClock(r.End, now), elapsed)
	}
	if r.Truncated {
		text += t.OutageTruncatedNote
	}
	return text + " · " + formatZone(r.Start)
}

// observedSpanLabel is the humanized time a sample series actually covers,
// or the "no data" dash when there is too little history to span anything —
// so a heading never claims a window it cannot back.
func observedSpanLabel(series []history.Sample) string {
	if s := history.HumanizeSpan(history.ObservedSpan(series)); s != "" {
		return s
	}
	return i18n.T().NoData
}

// formatPercent renders a percentage with just enough precision to keep a rare
// loss visible: "3%" reads fine, but a single lost probe in 600 is 0.17% and
// would round to a flat "0%" — which is exactly the packet loss the user opened
// this panel to find.
func formatPercent(p float64) string {
	if p > 0 && p < 1 {
		return fmt.Sprintf("%.2f%%", p)
	}
	return fmt.Sprintf("%.0f%%", p)
}
