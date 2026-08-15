package monitor

import (
	"fmt"
	"sync"
	"time"
)

// recoveryConfirmPolls is how many CONSECUTIVE healthy polls a provider must
// show, while latched down, before OutageTracker actually declares it
// recovered. A single flaky poll mid-incident (feed hiccup, empty payload for
// one round) must not split one continuous outage into "outage, recovered,
// outage" in the toast stream and the audit trail — it did exactly that for a
// real ~2h46m Anthropic incident on 2026-07-29, where one 61s poll read zero
// incidents in the middle of it.
const recoveryConfirmPolls = 2

// Transition titles. These are the JOIN KEY between three packages: this
// detector writes them, internal/ui records them into alert-log.jsonl, and
// internal/audit selects on them to reconstruct outage windows. They were bare
// string literals duplicated across all three, so renaming them here — together
// with this package's own tests, the natural paired edit — would have left the
// audit matching nothing: every window would disappear and every real major
// capture would be reported UNCOVERED, with a fully green test suite. Exported
// so the coupling is visible and compiler-checked.
const (
	TitleProviderOutage    = "Provider outage"
	TitleProviderRecovered = "Provider recovered"
)

// OutageTracker debounces provider service-state notifications: it emits
// exactly one alert when a provider transitions into a service outage, and
// one recovery notification once it has stayed healthy for
// recoveryConfirmPolls consecutive polls in a row.
//
// The outage side stays transition-based and unconditionally instant — a
// provider's own status feed is authoritative, so a single observed outage is
// actionable and must never be delayed. Only the recovery side is debounced:
// better to say "still down" for one extra poll than to cry "fixed" and then
// reopen it. Crucially, a feed we could NOT read is "unknown", not "down" —
// callers must simply not call Update in that case, so an unreadable feed
// never produces an outage alert (and never advances or resets the recovery
// confirmation streak either).
type OutageTracker struct {
	mu      sync.Mutex
	down    map[string]bool
	summary map[string]string // the incident summary that drove the last "down", for the recovery text
	healthy map[string]int    // consecutive healthy polls seen so far while latched down
	// names and since exist so a latched outage can be written to disk and
	// re-latched by the next run: the recovery text needs the display name, and
	// the outage's age decides whether announcing it is still news. See DownState.
	names map[string]string
	since map[string]time.Time
}

// NewOutageTracker returns a ready-to-use tracker.
func NewOutageTracker() *OutageTracker {
	return &OutageTracker{
		down: map[string]bool{}, summary: map[string]string{}, healthy: map[string]int{},
		names: map[string]string{}, since: map[string]time.Time{},
	}
}

// Update records whether provider id read healthy or down on this poll and
// returns a Notification when that's enough to change the tracker's state, or
// nil otherwise. The first observation of a healthy provider is silent (no
// spurious recovery). summary is the worst active incident's own
// title/description (empty when none is available, e.g. a page-level
// indicator with no incident detail) — it is named in the outage alert, and
// echoed back in the recovery alert so the user can tell which incident just
// cleared.
func (o *OutageTracker) Update(id, name, summary string, down bool) *Notification {
	o.mu.Lock()
	defer o.mu.Unlock()

	if down {
		o.healthy[id] = 0 // any down poll cancels a recovery streak in progress
		if o.down[id] {
			if summary != "" {
				o.summary[id] = summary // keep the latest description while still down
			}
			return nil
		}
		o.down[id] = true
		o.summary[id] = summary
		o.names[id] = name
		o.since[id] = time.Now()
		body := fmt.Sprintf("%s is reporting a service outage.", name)
		if summary != "" {
			body = fmt.Sprintf("%s: %s", name, summary)
		}
		return &Notification{Title: TitleProviderOutage, Body: body}
	}

	if !o.down[id] {
		return nil // steady healthy
	}
	o.healthy[id]++
	if o.healthy[id] < recoveryConfirmPolls {
		return nil // healthy, but not for long enough yet to call it recovered
	}
	o.down[id] = false
	o.healthy[id] = 0
	prev := o.summary[id]
	delete(o.summary, id)
	if name == "" {
		name = o.names[id] // re-latched from disk: the caller may not know it
	}
	delete(o.names, id)
	delete(o.since, id)
	body := fmt.Sprintf("%s is operational again.", name)
	if prev != "" {
		body = fmt.Sprintf("%s is operational again — \"%s\" was resolved.", name, prev)
	}
	return &Notification{Title: TitleProviderRecovered, Body: body, Recovery: true}
}

// DownState is one latched outage, in a form that can outlive the process.
//
// The tracker is edge-triggered, so the ONLY record that a check is currently
// down lives in its in-memory maps. A restart therefore silently forgets it, and
// the first healthy poll afterwards is no longer a transition — the user is told
// a check went down and never told it came back. That is not hypothetical: on
// 2026-08-02 an outage alert for a custom URL check fired at 10:29:59, the app
// relaunched at 11:14:30, the check was healthy again at 11:15:01, and no
// recovery was ever emitted. The audit trail shows the outage hanging open to
// this day.
//
// Suppressed records that the OUTAGE alert this state opened was swallowed
// (muted, Do-Not-Disturb, a deactivated region, offline). It has to survive the
// restart with the latch, or the recovery becomes an orphan: the user gets a
// green "…is operational again" card explaining the end of an outage they were
// never told about. That pairing is exactly what recoveryIsOrphaned exists to
// prevent in a single run, and it is memory-only.
type DownState struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Summary    string    `json:"summary,omitempty"`
	Since      time.Time `json:"since"`
	Suppressed bool      `json:"suppressed,omitempty"`
}

// Snapshot returns the currently latched outages, so a caller can persist them.
// Order is unspecified; callers that need stability should sort by ID.
func (o *OutageTracker) Snapshot() []DownState {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]DownState, 0, len(o.down))
	for id, down := range o.down {
		if !down {
			continue
		}
		// Suppressed is deliberately NOT set here: whether the opening alert was
		// actually shown is the UI's bookkeeping (Controller.downSuppressed), not
		// the detector's. The caller fills it in before persisting.
		out = append(out, DownState{
			ID: id, Name: o.names[id], Summary: o.summary[id], Since: o.since[id],
		})
	}
	return out
}

// Restore re-latches outages saved by a previous run so their recoveries can
// still be announced, and returns the ones deliberately NOT restored because
// they are older than maxAge.
//
// The age limit is the whole reason this is not a plain load. A recovery is
// news only while the outage is still current; announcing "portaldev is
// operational again" for something that resolved while the machine was off for
// three days is noise wearing the costume of an alert. Stale entries are handed
// back rather than dropped so the caller can close them in the audit trail —
// the window stays accounted for, it just does not interrupt anyone.
//
// A restored outage is NOT re-announced: it was already alerted by the run that
// latched it.
func (o *OutageTracker) Restore(states []DownState, now time.Time, maxAge time.Duration) (stale []DownState) {
	// A start instant in the future cannot be aged: now.Sub(Since) is negative,
	// so it would read as freshly latched forever. A clock change or a corrupt
	// file is enough to produce one, and internal/history already refuses
	// timestamps beyond the same slack for the same reason.
	const futureSlack = time.Hour
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, s := range states {
		if s.ID == "" {
			continue
		}
		// Unusable is rejected unconditionally: a zero or far-future start makes
		// the state unjudgeable, which is not something a maxAge of 0 should wave
		// through.
		unusable := s.Since.IsZero() || s.Since.After(now.Add(futureSlack))
		if unusable || (maxAge > 0 && now.Sub(s.Since) > maxAge) {
			stale = append(stale, s)
			continue
		}
		o.down[s.ID] = true
		o.names[s.ID] = s.Name
		o.summary[s.ID] = s.Summary
		o.since[s.ID] = s.Since
		o.healthy[s.ID] = 0
	}
	return stale
}

// Reset clears tracked state for a provider id (e.g. when it is disabled) so a
// later transition is not reported spuriously.
func (o *OutageTracker) Reset(id string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.down, id)
	delete(o.summary, id)
	delete(o.healthy, id)
	delete(o.names, id)
	delete(o.since, id)
}
