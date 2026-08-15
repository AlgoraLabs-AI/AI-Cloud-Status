package ui

import (
	"fmt"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/history"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/i18n"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/providers"
)

// selectable marks a label's text as mouse-selectable (copyable) and returns
// it — every informational text in the detail panel goes through this so
// incident titles, notes, and endpoints can be copied out.
func selectable(l *widget.Label) *widget.Label {
	l.Selectable = true
	return l
}

// showProviderDetail opens the incident drill-down for a provider: its overall
// status, liveness, an uptime sparkline, a per-incident breakdown (title,
// severity, affected components/regions, started/updated times, source link),
// and a mute toggle plus an "Open status page" action.
func (c *Controller) showProviderDetail(p providers.Provider, st provState, ok bool, interest []string) {
	isMuted := c.muteSnapshot()
	window := providerUptimeWindow
	state := providerStatusState(st, ok, interest, isMuted)
	v := visualFor(state)

	headline := selectable(widget.NewLabelWithStyle(p.Name+" — "+v.Label, fyne.TextAlignLeading, fyne.TextStyle{Bold: true}))
	endpoint := selectable(newWrappedLabel(providers.TypeLabel + " · " + p.URL))

	// Liveness summary.
	when := "—"
	if !st.when.IsZero() {
		when = st.when.Format("2006-01-02 15:04:05")
	}
	t := i18n.T()
	meta := selectable(widget.NewLabel(fmt.Sprintf(t.DetailMeta, when, formatLatency(st.latency), st.attempts)))
	if up, n := c.regionUptime(p.ID, window, true, isMuted, time.Now()); n > 0 {
		meta.SetText(meta.Text + fmt.Sprintf(t.DetailUptime, up*100))
	}

	muted := c.isMuted(p.ID)
	muteChk := c.blurOnToggle(widget.NewCheck(t.MuteProvider, func(b bool) {
		c.setMute(p.ID, b)
	}))
	muteChk.SetChecked(muted)

	body := container.NewVBox(headline, endpoint, widget.NewSeparator(), meta, c.uptimeCell(p.ID, window, true, isMuted), muteChk, widget.NewSeparator())

	// State-specific content.
	switch {
	case !ok || !st.checked:
		body.Add(widget.NewLabel(t.NotCheckedYet))
	case st.skipped:
		body.Add(widget.NewLabel(t.NoPublicFeed))
	case st.err != nil:
		// Two lines, not one: the localized sentence says what went wrong in
		// words, and the raw transport error below it is what the user can
		// actually paste to a network admin ("x509: certificate is valid for
		// *.corp-proxy, not status.anthropic.com"). Neither substitutes for the
		// other, and the row alone never had room for the second.
		body.Add(newWrappedLabel(feedFailureMessage(st.err)))
		body.Add(selectable(newWrappedLabel(fmt.Sprintf(t.FeedErrDetail, st.err))))
		body.Add(widget.NewButtonWithIcon(t.RetryNow, theme.ViewRefreshIcon(), func() { c.recheckProvider(p) }))
		// Not being able to read the feed RIGHT NOW says nothing about the
		// incidents that already happened. The journal is local and complete up
		// to the last readable poll, so the 24h history stays on screen — losing
		// it was the panel confusing "I can't see" with "there was nothing".
		// Its reference clock is lastOK, not the failed attempt: an incident
		// still open when the feed went dark must not read "resolved just now"
		// because we stopped being able to look.
		c.addRecentIncidents(body, p.ID, interest, st.lastOK)
	default:
		// A months-old event the provider never closed is not news, and letting
		// it pad "Active incidents (N)" buries whatever is genuinely happening
		// now. Split it out under its own heading instead of dropping it — it
		// IS still open on the feed, so it stays visible, just not counted as
		// live.
		// Muted incidents are separated out, not hidden. The headline above is
		// mute-aware (providerStatusState), so listing them as "Active incidents"
		// made this dialog contradict itself: green at the top, active outages
		// underneath. Dropping them instead would be worse — a drill-down exists
		// to explain WHY a row reads green, and "you deactivated these regions"
		// is that explanation. So they keep their own heading.
		inScope := st.res.IncidentsInRegions(interest)
		var unmuted, muted []providers.Incident
		for _, inc := range inScope {
			if incidentEffectivelyActive(inc, isMuted) {
				unmuted = append(unmuted, inc)
			} else {
				muted = append(muted, inc)
			}
		}
		live, stale := splitStaleIncidents(unmuted, time.Now())
		// The verdict is unconditional. It used to be printed only when NOTHING
		// at all was in scope, so a provider whose only finding was a zombie or
		// a silenced-region incident opened straight into "Stale / zombie alerts
		// (1)" with no statement of whether anything needed attention.
		//
		// Which verdict matters: with something still on the feed, "operational"
		// claims more than this panel knows, and it would be a SECOND verdict
		// competing with the mute-aware one in the headline above — the same
		// self-contradiction the muted section was split out to end.
		body.Add(widget.NewLabelWithStyle(incidentVerdict(len(live), len(stale), len(muted)), fyne.TextAlignLeading, fyne.TextStyle{Bold: true}))
		if len(live) > 0 {
			body.Add(c.fencedIncidentCards(live))
		}
		if len(stale) > 0 {
			body.Add(widget.NewSeparator())
			body.Add(widget.NewLabelWithStyle(fmt.Sprintf(t.StaleIncidentsN, len(stale)), fyne.TextAlignLeading, fyne.TextStyle{Bold: true}))
			body.Add(c.fencedIncidentCards(stale))
		}
		if len(muted) > 0 {
			body.Add(widget.NewSeparator())
			body.Add(widget.NewLabelWithStyle(fmt.Sprintf(t.MutedIncidentsN, len(muted)), fyne.TextAlignLeading, fyne.TextStyle{Bold: true}))
			body.Add(c.fencedIncidentCards(muted))
		}
		// "What happened today" is a different question from "what is open now",
		// and it used to be answered only when the second answer was empty —
		// the history vanished exactly when someone opening this panel during an
		// outage would want it. It is now always here, minus whatever is already
		// drawn above: the journal keeps open incidents too, and the same
		// incident as a tall card AND a history line reads as two incidents.
		shown := append(append(append([]providers.Incident{}, live...), stale...), muted...)
		c.addRecentIncidents(body, p.ID, interest, st.when, shown...)
	}

	// Source-page action.
	open := widget.NewButtonWithIcon(t.OpenStatusPage, theme.ComputerIcon(), func() { c.openURL(p.StatusPage()) })
	body.Add(widget.NewSeparator())
	body.Add(open)

	scroll := container.NewVScroll(body)
	scroll.SetMinSize(fyne.NewSize(520, 420))
	dialog.ShowCustom(fmt.Sprintf(t.IncidentDetailsTitle, p.Name), t.Close, scroll, c.window)
}

// splitStaleIncidents partitions incidents into the live ones and the
// stale/zombie ones, so the panel heading names what the adapters' amber
// demotion already implied.
//
// The rule itself lives in providers.Incident.IsStale, not here. It used to be
// a third hand-written copy of the same 15-day horizon — with its own choice of
// which timestamp counts — so a row and its own drill-down could disagree about
// the same incident.
func splitStaleIncidents(incs []providers.Incident, now time.Time) (live, stale []providers.Incident) {
	for _, inc := range incs {
		if inc.IsStale(now) {
			stale = append(stale, inc)
			continue
		}
		live = append(live, inc)
	}
	return live, stale
}

// incidentVerdict is the one line that answers "does anything here need me?",
// printed for EVERY readable-feed state. It used to be printed only when
// nothing at all was in scope, so a provider whose only finding was a zombie or
// an incident in a silenced region opened straight into a section heading with
// no verdict at all.
//
// With something still on the feed the wording stops short of "operational":
// that is a stronger claim than this panel can make, and it would be a second
// verdict competing with the mute-aware one in the dialog headline — the same
// self-contradiction ("green at the top, active outages underneath") that
// splitting the muted section out was meant to end.
func incidentVerdict(live, stale, muted int) string {
	t := i18n.T()
	switch {
	case live > 0:
		return fmt.Sprintf(t.ActiveIncidentsN, live)
	case stale > 0 || muted > 0:
		return t.NoActionableIncidents
	default:
		return t.NoActiveIncidents
	}
}

// addRecentIncidents appends the last-24h incident history — heading plus one
// fenced card per journaled incident — when the journal has anything in scope.
// It is a method on the panel body rather than inline code because two states
// need it: a provider reading clean, and a provider whose feed we currently
// cannot read at all.
//
// ref is the clock the cards' "ongoing vs resolved" is measured against: the
// last time the feed was actually READ. An entry last seen at ref is still open
// as far as anyone knows; anything older resolved. A ZERO ref means nothing has
// been read this run at all (the journal was restored from disk and the first
// poll failed) — then neither claim is available and the cards say only when
// each incident was last seen.
//
// shown are the incidents already drawn above from the current feed; they are
// subtracted by incident identity. The heading changes with them: what is left
// is no longer "the incident history", it is what ALSO happened.
func (c *Controller) addRecentIncidents(body *fyne.Container, id string, interest []string, ref time.Time, shown ...providers.Incident) {
	recent := withoutShown(c.recentIncidents(id, interest), shown)
	if len(recent) == 0 {
		return
	}
	t := i18n.T()
	heading := t.RecentIncidents24h
	if len(shown) > 0 {
		heading = t.AlsoRecentIncidents24h
	}
	body.Add(widget.NewSeparator())
	body.Add(widget.NewLabelWithStyle(heading, fyne.TextAlignLeading, fyne.TextStyle{Bold: true}))
	body.Add(fencedRecentCards(recent, ref))
}

// withoutShown drops the journal entries that the panel already rendered as
// current-feed cards, matching on the journal's own identity (history.Key —
// the incident's page URL, or its summary when the feed provides none) so the
// two sides cannot drift into disagreeing about what "the same incident" is.
func withoutShown(recent []history.IncidentEntry, shown []providers.Incident) []history.IncidentEntry {
	if len(shown) == 0 || len(recent) == 0 {
		return recent
	}
	drawn := make(map[string]bool, len(shown))
	for _, inc := range shown {
		if k := history.IncidentKey(inc.URL, inc.Summary); k != "" {
			drawn[k] = true
		}
	}
	out := make([]history.IncidentEntry, 0, len(recent))
	for _, e := range recent {
		if !drawn[e.Key()] {
			out = append(out, e)
		}
	}
	return out
}

// fencedIncidentCards stacks incident cards fenced by incidentRule — one rule
// above each card and one closing the last — so a list of three incidents reads
// as three enclosed blocks. Padding alone did not: an incident whose note runs
// to four wrapped lines and the next incident's title were the same distance
// apart as that incident's own severity line.
func (c *Controller) fencedIncidentCards(incs []providers.Incident) fyne.CanvasObject {
	box := container.NewVBox()
	for _, inc := range incs {
		box.Add(incidentRuleLabel(fyne.TextAlignLeading))
		box.Add(c.incidentCard(inc))
	}
	if len(incs) > 0 {
		box.Add(incidentRuleLabel(fyne.TextAlignLeading))
	}
	return box
}

// fencedRecentCards is fencedIncidentCards for the last-24h history entries.
func fencedRecentCards(entries []history.IncidentEntry, now time.Time) fyne.CanvasObject {
	box := container.NewVBox()
	for _, e := range entries {
		box.Add(incidentRuleLabel(fyne.TextAlignLeading))
		box.Add(recentIncidentCard(e, now))
	}
	if len(entries) > 0 {
		box.Add(incidentRuleLabel(fyne.TextAlignLeading))
	}
	return box
}

// incidentCard renders a single incident's drill-down detail.
func (c *Controller) incidentCard(inc providers.Incident) fyne.CanvasObject {
	title := selectable(widget.NewLabelWithStyle(incidentTitle(inc)+incidentAgeSuffix(inc, time.Now()), fyne.TextAlignLeading, fyne.TextStyle{Bold: true}))
	title.Wrapping = fyne.TextWrapWord

	t := i18n.T()
	rows := []fyne.CanvasObject{title}
	rows = append(rows, severityLine(inc.Severity, fyne.TextAlignLeading))

	if len(inc.Regions) > 0 {
		rows = append(rows, selectable(newWrappedLabel(fmt.Sprintf(t.AffectedRegionsLabel, strings.Join(inc.Regions, ", ")))))
	} else {
		rows = append(rows, selectable(widget.NewLabel(t.AffectedRegionsGlobal)))
	}
	if len(inc.Components) > 0 {
		rows = append(rows, selectable(newWrappedLabel(fmt.Sprintf(t.AffectedComponents, strings.Join(inc.Components, ", ")))))
	}
	if !inc.Started.IsZero() {
		rows = append(rows, selectable(widget.NewLabel(fmt.Sprintf(t.StartedLabel, formatIncidentTime(inc.Started)))))
	}
	if !inc.Updated.IsZero() {
		rows = append(rows, selectable(widget.NewLabel(fmt.Sprintf(t.UpdatedLabel, formatIncidentTime(inc.Updated)))))
	}
	if note := strings.TrimSpace(inc.Note); note != "" {
		rows = append(rows, selectable(newWrappedLabel(note)))
	}
	if inc.URL != "" {
		url := inc.URL
		rows = append(rows, widget.NewButtonWithIcon(t.OpenIncident, theme.ComputerIcon(), func() { c.openURL(url) }))
	}
	return container.NewPadded(container.NewVBox(rows...))
}

// incidentTitle returns a non-empty title for an incident card.
func incidentTitle(inc providers.Incident) string {
	if inc.Summary != "" {
		return inc.Summary
	}
	return i18n.T().IncidentFallback
}

// incidentAgeSuffix is the " (started 2h 15m ago)" parenthetical closing an
// incident title, so "how long has this been going on" is answerable at a
// glance instead of by subtracting the Started timestamp further down the card
// from the wall clock. It is also what tells a zombie apart on sight: a stale
// card reads "(started 153d 6h ago)". Empty when the feed reports no start
// time — an age can't be invented from nothing.
func incidentAgeSuffix(inc providers.Incident, now time.Time) string {
	if inc.Started.IsZero() {
		return ""
	}
	return " " + fmt.Sprintf(i18n.T().IncidentStartedAgo, formatElapsed(now.Sub(inc.Started)))
}

// incidentEntries converts a feed's incidents to journal entries for the
// last-24h incident history.
func incidentEntries(incs []providers.Incident) []history.IncidentEntry {
	out := make([]history.IncidentEntry, 0, len(incs))
	for _, inc := range incs {
		out = append(out, history.IncidentEntry{
			Summary:  inc.Summary,
			Severity: int(inc.Severity),
			Regions:  inc.Regions,
			URL:      inc.URL,
			Started:  inc.Started,
		})
	}
	return out
}

// recentIncidents returns the journaled incidents for provider id seen within
// the last 24h that are relevant to the regions of interest — the same scoping
// as the active-incident list: an entry with no regions is global and always in
// scope.
func (c *Controller) recentIncidents(id string, interest []string) []history.IncidentEntry {
	if c.incidents == nil {
		return nil
	}
	all := c.incidents.Recent(id, time.Now().Add(-providerUptimeWindow))
	if len(interest) == 0 {
		return all
	}
	// Scope via providers.Incident.inScope rather than re-deriving it: this used
	// to be a hand-written copy of the same rule, and a copy is free to drift
	// from the original the moment either changes.
	var out []history.IncidentEntry
	for _, e := range all {
		if (providers.Incident{Regions: e.Regions}).InScope(interest) {
			out = append(out, e)
		}
	}
	return out
}

// recentIncidentCard renders one journaled incident: title (with a
// parenthetical start/resolved-or-ongoing range) and severity, compact. now is
// the provider's last-checked time — an entry whose LastSeen matches it is
// still being freshly observed on the raw feed this very poll, so it reads as
// "ongoing" even though it landed in the (usually-resolved) history list: the
// journal records every incident unfiltered, but this list only renders when
// region-of-interest filtering has emptied the ACTIVE list, so a still-open
// incident confined to a region the user isn't watching shows up here instead
// of above — it must not be mislabeled as resolved.
func recentIncidentCard(e history.IncidentEntry, now time.Time) fyne.CanvasObject {
	t := i18n.T()
	name := e.Summary
	if name == "" {
		name = t.IncidentFallback
	}
	started := e.Started
	if started.IsZero() {
		started = e.FirstSeen
	}
	// The zone comes from the LAST time shown, so a range spanning a DST
	// change is labeled with the offset the reader most likely has now.
	// Each range closes with how long ago its endpoint was: the clock times are
	// mostly bare HH:MM, so "12:18" alone doesn't say whether that was twenty
	// minutes or twenty hours back.
	var rng string
	if now.IsZero() {
		// Nothing has been read this run: the journal came off disk and the
		// first poll failed. "Resolved" would be inferred from an absence that
		// is only our own downtime, and "ongoing" asserts knowledge we do not
		// have. Say when we last saw it and stop there.
		rng = fmt.Sprintf(t.IncidentStartedAgo, formatElapsed(time.Since(started))) +
			" " + fmt.Sprintf(t.IncidentLastSeenAgo, formatElapsed(time.Since(e.LastSeen)))
	} else if !e.LastSeen.Before(now) {
		rng = fmt.Sprintf(t.IncidentOngoingRange, formatSpanClock(started, now), formatZone(started)) +
			" " + fmt.Sprintf(t.IncidentStartedAgo, formatElapsed(now.Sub(started)))
	} else {
		rng = fmt.Sprintf(t.IncidentResolvedRange, formatSpanClock(started, now), formatSpanClock(e.LastSeen, now), formatZone(e.LastSeen)) +
			" " + fmt.Sprintf(t.IncidentResolvedAgo, formatElapsed(now.Sub(e.LastSeen)))
	}
	title := selectable(widget.NewLabelWithStyle(name+" "+rng, fyne.TextAlignLeading, fyne.TextStyle{Bold: true}))
	title.Wrapping = fyne.TextWrapWord
	sev := severityLine(providers.Severity(e.Severity), fyne.TextAlignLeading)
	return container.NewPadded(container.NewVBox(title, sev))
}
