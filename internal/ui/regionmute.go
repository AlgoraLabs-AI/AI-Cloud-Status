package ui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/config"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/history"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/i18n"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/providers"
)

// regionMuted reports whether region (an incident's affected-region string)
// matches any active deactivation — a session "until restart" entry or a
// persisted timed/forever entry. Matching is substring-tolerant via
// providers.MatchRegion, so the feed's region form and the muted key need not be
// identical.
func (c *Controller) regionMuted(region string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.regionMutedLocked(region, time.Now().Unix())
}

// regionMutedLocked is regionMuted without taking the lock (caller holds c.mu).
func (c *Controller) regionMutedLocked(region string, nowUnix int64) bool {
	for key := range c.sessionDisabledRegions {
		if providers.MatchRegion(region, []string{key}) {
			return true
		}
	}
	for key, until := range c.cfg.RegionMutedUntil {
		// Check the forever sentinel BEFORE the expiry comparison: -1 > now is
		// false, so an expiry-only test would wrongly treat "forever" as expired.
		if until == config.RegionMuteForever || until > nowUnix {
			if providers.MatchRegion(region, []string{key}) {
				return true
			}
		}
	}
	return false
}

// muteKeys captures the active region-deactivation keys ONCE under c.mu,
// sorted for stable fingerprinting. Hot paths (buildRows iterates up to
// hundreds of history samples per row) MUST snapshot instead of calling
// regionMuted per sample: c.mu is a non-reentrant sync.Mutex, so per-sample
// locking both contends with the poll goroutines and risks self-deadlock from
// any locked caller.
func (c *Controller) muteKeys() []string {
	c.mu.Lock()
	now := time.Now().Unix()
	keys := make([]string, 0, len(c.sessionDisabledRegions)+len(c.cfg.RegionMutedUntil))
	for k := range c.sessionDisabledRegions {
		keys = append(keys, k)
	}
	for k, until := range c.cfg.RegionMutedUntil {
		// Mirror regionMutedLocked: keep the forever sentinel and unexpired timed
		// entries; -1 > now is false, so test the sentinel before the expiry.
		if until == config.RegionMuteForever || until > now {
			keys = append(keys, k)
		}
	}
	c.mu.Unlock()
	sort.Strings(keys)
	return keys
}

// mutePredicate builds the immutable region-muted predicate over a key snapshot.
func mutePredicate(keys []string) func(region string) bool {
	return func(region string) bool {
		for _, k := range keys {
			if providers.MatchRegion(region, []string{k}) {
				return true
			}
		}
		return false
	}
}

// muteSig fingerprints a mute-key snapshot; the uptime-cell cache uses it to
// detect that a (de)activation changed and paints must be recomputed.
func muteSig(keys []string) string { return strings.Join(keys, ";") }

// muteSnapshot returns the region-muted predicate over the current
// deactivations (see muteKeys).
func (c *Controller) muteSnapshot() func(region string) bool {
	return mutePredicate(c.muteKeys())
}

// incidentEffectivelyActive is the single muting kernel shared by status
// (effectiveSeverity) and alert delivery (regionAlertSuppressed): a major
// incident still counts when it is global (un-muteable) or has at least one
// affected region the user has NOT deactivated. Keeping one kernel means the
// Status column and the alert can never silently disagree about what muting hides.
func incidentEffectivelyActive(inc providers.Incident, isMuted func(region string) bool) bool {
	if inc.IsGlobal() {
		return true
	}
	for _, reg := range inc.Regions {
		if !isMuted(reg) {
			return true
		}
	}
	return false
}

// effectivelyActiveIncidents filters incidents down to those still effectively
// active under the user's region de-selections — the Active-incidents-column
// counterpart of effectiveSeverity, so the cell can't keep showing an incident
// whose every region was deactivated while the Status reads Operational. The
// region chips stay (they render the muted state); only the incident text and
// its severity contribution go.
func effectivelyActiveIncidents(incs []providers.Incident, isMuted func(region string) bool) []providers.Incident {
	var out []providers.Incident
	for _, inc := range incs {
		if incidentEffectivelyActive(inc, isMuted) {
			out = append(out, inc)
		}
	}
	return out
}

// effectiveSeverity is the worst service-health severity that survives the user's
// region de-selections: the worst severity among in-interest incidents that are
// still effectively active. It is the muting-aware replacement for
// res.SeverityInRegions used by the displayed Status and the tray aggregate.
func effectiveSeverity(res providers.Result, interest []string, isMuted func(region string) bool) providers.Severity {
	// With no per-incident detail there is nothing region-scoped to mute, so the
	// provider's aggregate severity stands (matches res.SeverityInRegions, which
	// returns the aggregate when interest is empty and no incidents are present).
	if len(res.Incidents) == 0 {
		return res.SeverityInRegions(interest)
	}
	worst := providers.SevNone
	for _, inc := range res.IncidentsInRegions(interest) {
		if inc.Severity > worst && incidentEffectivelyActive(inc, isMuted) {
			worst = inc.Severity
		}
	}
	return worst
}

// unmutedSeverity is effectiveSeverity with no region de-selections applied: the
// worst in-scope INCIDENT severity, falling back to the feed's aggregate
// indicator only when the feed lists no incidents. History samples and outage
// alerts MUST use this (not res.SeverityInRegions, which with an empty interest
// set returns only the page-level indicator) so they can never disagree with the
// displayed Status: Statuspage derives its indicator from component states, and
// a major-impact incident can coexist with a sub-major indicator — which showed
// "Outage" in the row while recording green uptime samples and suppressing the
// outage notification. Muting is deliberately NOT applied here: samples record
// the unmuted truth and the strip re-applies muting at paint time (sampleDown),
// while alert delivery applies it separately via regionAlertSuppressed.
func unmutedSeverity(res providers.Result, interest []string) providers.Severity {
	return effectiveSeverity(res, interest, func(string) bool { return false })
}

// unmutedWorstSummary returns the Summary of the worst-severity in-scope
// incident (the same one unmutedSeverity's value comes from), so an outage
// alert can name the specific incident instead of a generic "reporting an
// outage" line. Empty when the feed lists no incidents (a page-level
// indicator with no incident detail).
func unmutedWorstSummary(res providers.Result, interest []string) string {
	worst := providers.SevNone
	summary := ""
	for _, inc := range res.IncidentsInRegions(interest) {
		if inc.Severity >= worst {
			worst = inc.Severity
			summary = inc.Summary
		}
	}
	return summary
}

// sampleDownRegions derives the per-sample DownRegions for a provider history
// sample from its result and the regions of interest. unscoped is true when a
// MAJOR incident is global (or coexists with regional ones) — recorded as an
// empty region set so muting can never falsely green it. Returns (nil, false)
// when nothing major is in scope (the sample is up).
func sampleDownRegions(res providers.Result, interest []string) (regions []string, unscoped bool) {
	seen := map[string]bool{}
	for _, inc := range res.IncidentsInRegions(interest) {
		if inc.Severity < providers.SevMajor {
			continue
		}
		if inc.IsGlobal() {
			return nil, true // a global major outage dominates: always-down
		}
		for _, reg := range inc.Regions {
			if reg != "" && !seen[reg] {
				seen[reg] = true
				regions = append(regions, reg)
			}
		}
	}
	return regions, false
}

// sampleDegradedRegions derives the per-sample DegradedRegions for a provider
// history sample: the regions of in-interest MINOR incidents. unscoped is true
// when a Minor incident is global. Returns (nil, false) when nothing minor is in
// scope. Mirrors sampleDownRegions one severity down.
func sampleDegradedRegions(res providers.Result, interest []string) (regions []string, unscoped bool) {
	seen := map[string]bool{}
	for _, inc := range res.IncidentsInRegions(interest) {
		if inc.Severity != providers.SevMinor {
			continue
		}
		if inc.IsGlobal() {
			unscoped = true
			continue
		}
		for _, reg := range inc.Regions {
			if reg != "" && !seen[reg] {
				seen[reg] = true
				regions = append(regions, reg)
			}
		}
	}
	return regions, unscoped
}

// Paint levels for the uptime strip, worst-last so a higher value is more severe.
const (
	samplePaintOK = iota
	samplePaintDegraded
	samplePaintDown
)

// samplePaintUnknown marks a bucketed-strip time bucket with NO observations
// (app off, check not yet running). Deliberately below samplePaintOK so any
// real observation wins the worst-of-bucket comparison, and painted grey —
// unknown time is never shown as up. It never appears in per-SAMPLE paints.
const samplePaintUnknown = -1

// anyRegionActive reports whether at least one region is NOT currently muted.
func anyRegionActive(regions []string, isMuted func(region string) bool) bool {
	for _, reg := range regions {
		if !isMuted(reg) {
			return true
		}
	}
	return false
}

// sampleDown reports whether a sample reads as a full outage (red) under the
// current region de-selections. An "up" sample is never down; an unscoped down
// (no regions) is always down; a region-scoped down is down only while at least
// one of its regions is still active.
func sampleDown(s history.Sample, isMuted func(region string) bool) bool {
	if s.Up {
		return false
	}
	if len(s.DownRegions) == 0 {
		return true // unscoped down: global outage, connectivity/URL, or old sample
	}
	return anyRegionActive(s.DownRegions, isMuted)
}

// sampleDegraded reports whether a sample reads as degraded (amber) under the
// current region de-selections — analogous to sampleDown one level down.
func sampleDegraded(s history.Sample, isMuted func(region string) bool) bool {
	if !s.Degraded {
		return false
	}
	if len(s.DegradedRegions) == 0 {
		return true // unscoped degradation (global minor incident)
	}
	return anyRegionActive(s.DegradedRegions, isMuted)
}

// samplePaint resolves a sample to its strip colour under the current region
// de-selections: unknown (grey) first — an unreadable feed is never an outage —
// then down (red) wins over degraded (amber) wins over ok (green). This is the
// single classifier behind the uptime %, the sparkline, the detail panel, and
// the uptime sort, so they always agree.
func samplePaint(s history.Sample, isMuted func(region string) bool) int {
	switch {
	case s.Unknown:
		return samplePaintUnknown
	case sampleDown(s, isMuted):
		return samplePaintDown
	case sampleDegraded(s, isMuted):
		return samplePaintDegraded
	default:
		return samplePaintOK
	}
}

// regionAlertSuppressed reports whether a provider's outage alert should be
// withheld because every in-scope MAJOR incident driving it is confined to
// regions the user has deactivated. A global incident (no regions) is never
// suppressible, so a provider-wide outage always alerts. Returns false when no
// qualifying incident exists (there is nothing to suppress) so this can ONLY
// suppress, never cause, an alert. It gates delivery only — the stateful outage
// edge detector still advances, so the outage re-alerts on its next real change.
func (c *Controller) regionAlertSuppressed(res providers.Result, interest []string) bool {
	isMuted := c.muteSnapshot()
	sawMajor := false
	for _, inc := range res.IncidentsInRegions(interest) {
		if inc.Severity < providers.SevMajor {
			continue
		}
		sawMajor = true
		if incidentEffectivelyActive(inc, isMuted) {
			return false // an effectively-active major incident → alert
		}
	}
	return sawMajor
}

// deactivateRegion mutes region-scoped alerts for region for the duration d (a
// timed deactivation: 1h / 4h / 24h).
func (c *Controller) deactivateRegion(region string, d time.Duration) {
	c.setRegionMute(region, time.Now().Add(d).Unix())
}

// deactivateRegionForever mutes region-scoped alerts until the user reactivates.
func (c *Controller) deactivateRegionForever(region string) {
	c.setRegionMute(region, config.RegionMuteForever)
}

// setRegionMute persists a timed/forever region deactivation, superseding any
// "until restart" session entry for the same region.
func (c *Controller) setRegionMute(region string, until int64) {
	c.mu.Lock()
	c.clearRegionMutesLocked(region) // supersede tolerantly, not just the exact key
	c.cfg.SetRegionMuteUntil(region, until)
	cfg := c.cfg.Clone() // detached snapshot: the save runs unlocked, possibly off-thread
	c.mu.Unlock()
	c.saveCfg(cfg)
	c.refresh()
}

// deactivateRegionUntilRestart mutes region-scoped alerts for this run only.
func (c *Controller) deactivateRegionUntilRestart(region string) {
	c.mu.Lock()
	c.clearRegionMutesLocked(region) // session entry becomes the single source of truth
	if c.sessionDisabledRegions == nil {
		c.sessionDisabledRegions = map[string]bool{}
	}
	c.sessionDisabledRegions[config.NormalizeRegion(region)] = true
	cfg := c.cfg.Clone()
	c.mu.Unlock()
	c.saveCfg(cfg)
	c.refresh()
}

// reactivateRegion clears every deactivation (session + persisted) for region.
// Because it deletes the persisted entry, the expiry sweep won't later see it as
// a timed expiry, so a manual reactivate never triggers a "resumed" notice.
func (c *Controller) reactivateRegion(region string) {
	c.mu.Lock()
	c.clearRegionMutesLocked(region)
	cfg := c.cfg.Clone()
	c.mu.Unlock()
	c.saveCfg(cfg)
	c.refresh()
}

// clearRegionMutesLocked removes every deactivation entry — persisted and
// session — that could make regionMutedLocked(region) return true. Caller holds
// c.mu.
//
// Clearing has to be the exact inverse of matching, and matching is deliberately
// tolerant: providers.MatchRegion is case-insensitive and substring-tolerant in
// BOTH directions, so a user-typed "us-east-1" is meant to cover a feed's
// "US East (us-east-1)". Clearing was an exact map delete. The two disagreed
// whenever two providers name the same region differently: mute "us-east-1"
// until you reactivate it, and the chip another provider renders as
// "US East (us-east-1)" shows dimmed with a Reactivate button that deletes a key
// which does not exist. The dialog closes, the chip stays dimmed, and every major
// incident in that region stays suppressed with no way to undo it from that chip.
//
// The inverse of a tolerant match over-deletes: reactivating "us-east-1" also
// clears a broader "us" mute. That is consistent rather than sloppy — the broad
// key is WHY the chip read as muted — and it errs toward alerting, which is the
// right direction for this app.
func (c *Controller) clearRegionMutesLocked(region string) {
	want := []string{region}
	for key := range c.cfg.RegionMutedUntil {
		if providers.MatchRegion(key, want) {
			c.cfg.ClearRegionMute(key)
		}
	}
	for key := range c.sessionDisabledRegions {
		if providers.MatchRegion(key, want) {
			delete(c.sessionDisabledRegions, key)
		}
	}
}

// pruneExpiredRegionMutes drops persisted timed deactivations that have elapsed
// (keeping forever entries) and notifies the user that monitoring resumed for
// each. Called once per provider poll tick. Snapshot + save happen under the
// lock; the toast fires after releasing it (notify hops to the Fyne thread).
func (c *Controller) pruneExpiredRegionMutes() {
	c.mu.Lock()
	expired := c.cfg.PruneExpiredRegionMutes(time.Now().Unix())
	cfg := c.cfg.Clone() // this runs on the provider goroutine — must not share the live map
	c.mu.Unlock()
	if len(expired) == 0 {
		return
	}
	c.saveCfg(cfg)
	t := i18n.T()
	for _, region := range expired {
		c.notify(t.RegionResumedTitle, fmt.Sprintf(t.RegionResumedBody, region))
	}
}

// regionMuteStatus returns a human-readable description of region's current
// deactivation, for the popup.
// It looks up entries the same tolerant way regionMutedLocked decides the chip
// is muted in the first place. An exact key lookup here fell through to the bare
// "Deactivated." for any chip whose text merely MATCHES a stored key — losing
// the "until you reactivate it" wording exactly when the user needs to know
// which kind of mute they are looking at.
func (c *Controller) regionMuteStatus(region string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	want := []string{region}
	for key := range c.sessionDisabledRegions {
		if providers.MatchRegion(key, want) {
			return i18n.T().RegionDeactivatedUntilRestart
		}
	}
	// Report the LONGEST-lasting matching entry: it is the one that actually
	// keeps the chip silent, and "forever" outranks any timed entry.
	best, found := int64(0), false
	for key, until := range c.cfg.RegionMutedUntil {
		if !providers.MatchRegion(key, want) {
			continue
		}
		if !found || until == config.RegionMuteForever || (best != config.RegionMuteForever && until > best) {
			best, found = until, true
		}
	}
	if found {
		if best == config.RegionMuteForever {
			return i18n.T().RegionDeactivatedForever
		}
		left := time.Until(time.Unix(best, 0))
		if left < time.Minute {
			return i18n.T().RegionDeactivatedUnderMinute
		}
		return fmt.Sprintf(i18n.T().RegionDeactivatedFor, history.HumanizeSpan(left))
	}
	return i18n.T().RegionDeactivatedPlain
}

// regionIncidentRef is one live incident affecting a region, with the provider
// it belongs to, for the region popup's "what is silenced here" list.
type regionIncidentRef struct {
	provider string
	inc      providers.Incident
}

// regionIncidents returns the current in-feed incidents that name the given
// region, across every provider with a readable feed, in provider display order.
func (c *Controller) regionIncidents(region string) []regionIncidentRef {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []regionIncidentRef
	for _, p := range c.providers {
		st, ok := c.provStates[p.ID]
		if !ok || !st.checked || !st.res.Reachable() {
			continue
		}
		for _, inc := range st.res.Incidents {
			for _, r := range inc.Regions {
				if providers.MatchRegion(r, []string{region}) {
					out = append(out, regionIncidentRef{provider: p.Name, inc: inc})
					break
				}
			}
		}
	}
	return out
}

// staleIncidentDays is the age in days past which the region popup labels an
// incident stale. Derived from providers.StaleAfter rather than restated, so the
// popup, the drill-down split and the adapters' demotion can never drift apart.
const staleIncidentDays = int(providers.StaleAfter / (24 * time.Hour))

// regionIncidentLines renders one incident for the region popup: its title and
// a since-when line, so a glance answers "what is silenced here and is it a
// months-old zombie or something live".
func regionIncidentLines(ref regionIncidentRef) []fyne.CanvasObject {
	title := newWrappedLabel(ref.provider + " — " + ref.inc.Summary)
	title.TextStyle = fyne.TextStyle{Bold: true}
	lines := []fyne.CanvasObject{title}
	if !ref.inc.Started.IsZero() {
		lines = append(lines, widget.NewLabel(fmt.Sprintf("Started %s (%d days ago)",
			formatIncidentTime(ref.inc.Started), int(time.Since(ref.inc.Started).Hours()/24))))
	}
	if !ref.inc.Updated.IsZero() {
		days := int(time.Since(ref.inc.Updated).Hours() / 24)
		line := fmt.Sprintf(i18n.T().RegionIncidentLastUpdate, formatIncidentTime(ref.inc.Updated), days)
		if days > staleIncidentDays {
			line += i18n.T().RegionStaleZombieSuffix
		}
		lines = append(lines, widget.NewLabel(line))
	}
	return lines
}

// showRegionPopup opens the deactivate/reactivate dialog for a clicked region
// badge. Must run on the Fyne thread (it is, via the badge tap handler).
func (c *Controller) showRegionPopup(region string) {
	if c.window == nil {
		return
	}
	t := i18n.T()
	var dlg dialog.Dialog
	var content fyne.CanvasObject

	if c.regionMuted(region) {
		info := widget.NewLabel(c.regionMuteStatus(region))
		info.Wrapping = fyne.TextWrapWord
		rows := []fyne.CanvasObject{info}
		// Name what is being silenced here — an incident's title and age let the
		// user tell a months-old zombie (safe to keep deactivated) from a live
		// outage they may want alerts for again.
		if refs := c.regionIncidents(region); len(refs) > 0 {
			rows = append(rows, widget.NewSeparator())
			for _, ref := range refs {
				rows = append(rows, regionIncidentLines(ref)...)
			}
		}
		reactivate := widget.NewButtonWithIcon(t.RegionReactivateNow, nil, func() {
			c.reactivateRegion(region)
			dlg.Hide()
		})
		reactivate.Importance = widget.HighImportance
		rows = append(rows, reactivate)
		content = container.NewVBox(rows...)
	} else {
		msg := widget.NewLabel(t.RegionDeactivateIntro)
		msg.Wrapping = fyne.TextWrapWord
		timed := func(label string, d time.Duration) *widget.Button {
			return widget.NewButton(label, func() { c.deactivateRegion(region, d); dlg.Hide() })
		}
		forever := widget.NewButton(t.RegionUntilReactivate, func() {
			c.deactivateRegionForever(region)
			dlg.Hide()
		})
		forever.Importance = widget.DangerImportance
		untilRestart := widget.NewButton(t.RegionUntilRestart, func() {
			c.deactivateRegionUntilRestart(region)
			dlg.Hide()
		})
		content = container.NewVBox(
			msg,
			widget.NewSeparator(),
			widget.NewLabel(t.RegionDeactivateFor),
			container.NewHBox(timed(t.Region1Hour, time.Hour), timed(t.Region4Hours, 4*time.Hour), timed(t.Region24Hours, 24*time.Hour)),
			container.NewHBox(forever, untilRestart),
		)
	}

	dlg = dialog.NewCustom(t.RegionTitlePrefix+region, t.Close, content, c.window)
	dlg.Show()
}
