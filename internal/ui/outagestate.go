package ui

import (
	"encoding/json"
	"log/slog"
	"os"
	"sort"
	"time"

	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/alertlog"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/atomicfile"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/monitor"
)

// outageStateVersion is the on-disk format version for the latched-outage file.
// A file declaring anything else is ignored rather than half-interpreted: this
// is best-effort continuity, so refusing to guess costs one restart's worth of
// recovery alerts and never corrupts the alert trail.
const outageStateVersion = 1

// outageRecoveryMaxAge is how old a latched PER-CHECK outage may be and still
// earn a recovery announcement when the app restarts.
//
// A recovery is news only while its outage is still current. Re-latching one
// from three days ago so the next poll can pop "portaldev is operational again"
// is not a monitoring alert, it is an interruption about ancient history — and
// worse, it trains the user to dismiss the toasts that DO matter. A day matches
// the window every other display in the app already reasons over, so "recent
// enough to alert" and "recent enough to be on screen" mean the same thing.
//
// Connectivity is deliberately NOT held to this bound — see totalLossMaxAge.
const outageRecoveryMaxAge = providerUptimeWindow

// totalLossMaxAge is the same idea for the app-wide "Total internet loss"
// latch, and it is deliberately far shorter than outageRecoveryMaxAge.
//
// "Anthropic is operational again" names a third party whose state the user
// may still care about hours later. "Internet connection restored" is a
// statement about the very machine drawing the toast: if the user is reading
// it, the internet is back, and saying so twenty hours late is pure noise.
// The only case where restoring beats simply re-detecting is a blackout that
// ended while the process was dead — realistically a watchdog relaunch or a
// crash-restart, seconds to a minute. If the blackout is still going at
// launch the detector re-confirms in three rounds and alerts on its own, so
// nothing is lost by letting an old latch go.
const totalLossMaxAge = 10 * time.Minute

// outageStateFile is the persisted set of outages that were latched down when
// the process ended.
type outageStateFile struct {
	Version int                 `json:"version"`
	Checks  []monitor.DownState `json:"checks"`
	// TotalLoss is the app-wide connectivity latch, nil when connectivity was
	// up. It is a sibling rather than a member of Checks because Restore skips
	// an empty ID and because the two carry different lifetimes — see
	// totalLossMaxAge.
	TotalLoss *monitor.DownState `json:"total_loss,omitempty"`
}

// saveOutageState writes the currently latched outages so the next run can still
// announce their recoveries.
//
// Called after every transition rather than on a timer: transitions are rare,
// and the whole point is to survive an END the app did not choose — a crash, a
// watchdog relaunch, the machine losing power. A periodic save would leave
// exactly the window that matters unprotected.
//
// The mutex spans snapshot AND write. atomicfile guarantees the file is never
// torn, but it does not order two savers, and there genuinely are two: provider
// polling and URL polling each fan out one goroutine per check, all of which can
// latch and save in the same round. Without this, a slower goroutine's OLDER
// snapshot can land after a newer one and silently drop a check that had just
// gone down — reintroducing the missing recovery this whole file exists to stop.
func (c *Controller) saveOutageState() {
	if c.outages == nil || c.outageStatePath == "" {
		return
	}
	c.outageSaveMu.Lock()
	defer c.outageSaveMu.Unlock()

	states := c.outages.Snapshot()
	// Carry the opening alert's delivery outcome, which lives in the UI's
	// bookkeeping rather than the detector's.
	c.mu.Lock()
	for i := range states {
		states[i].Suppressed = c.downSuppressed[states[i].ID]
	}
	c.mu.Unlock()

	sort.Slice(states, func(i, j int) bool { return states[i].ID < states[j].ID })

	var totalLoss *monitor.DownState
	if c.totalLoss != nil && c.totalLoss.Down() {
		c.mu.Lock()
		since := c.totalLossSince
		// The connectivity alert is app-wide and carries an EMPTY id, so its
		// suppression outcome lives in downSuppressed[""]. It has to travel with
		// the latch for the same reason a check's does: "offline-banner-shown" is
		// the MOST likely reason this particular alert is swallowed, and losing it
		// means the next recovery pops "Internet connection restored" for an
		// outage the user was never told about.
		suppressed := c.downSuppressed[""]
		c.mu.Unlock()
		totalLoss = &monitor.DownState{ID: totalLossStateID, Since: since, Suppressed: suppressed}
	}
	data, err := json.Marshal(outageStateFile{
		Version: outageStateVersion, Checks: states, TotalLoss: totalLoss,
	})
	if err != nil {
		slog.Warn("outage state marshal failed", "err", err)
		return
	}
	if err := atomicfile.Write(c.outageStatePath, data, 0o600); err != nil {
		slog.Warn("outage state save failed", "path", c.outageStatePath, "err", err)
	}
}

// loadOutageState reads the latched outages persisted by a previous run. A
// missing, corrupt, or unrecognized-version file yields none: this is
// best-effort continuity, never a reason to refuse to launch.
func loadOutageState(path string) outageStateFile {
	if path == "" {
		return outageStateFile{}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("outage state read failed; starting with none", "path", path, "err", err)
		}
		return outageStateFile{}
	}
	var f outageStateFile
	if err := json.Unmarshal(data, &f); err != nil {
		slog.Warn("outage state file is corrupt; starting with none", "path", path, "err", err)
		return outageStateFile{}
	}
	if f.Version != outageStateVersion {
		slog.Warn("outage state file has an unrecognized version; ignoring it",
			"path", path, "version", f.Version, "supported", outageStateVersion)
		return outageStateFile{}
	}
	return f
}

// restoreOutageState re-latches the outages a previous run left open, so a
// restart no longer swallows their recoveries, and disposes of the rest.
//
// Three dispositions, each for a different reason:
//
//   - UNKNOWN id (the check was deleted, or is no longer registered) — dropped
//     silently. Nothing polls it, so it would never transition and never be
//     saved away; it would sit in the file forever, and once past the age limit
//     it would journal a fresh "recovered" line on EVERY launch for a check that
//     no longer exists. Not journaled at all: a check that is gone has no audit
//     window worth closing.
//   - TOO OLD to be news — closed, and the closure IS journaled. internal/audit
//     reconstructs outage windows from alert-log.jsonl and pairs each outage
//     with its recovery; a window whose recovery is simply missing stays open
//     forever and is reported as a miss. Recording the close keeps the audit
//     honest without interrupting anyone.
//   - FRESH — re-latched, along with whether its opening alert was suppressed,
//     so a recovery the user was never set up to understand stays suppressed too.
//
// The file is rewritten at the end no matter which paths ran. Without that, the
// consumed entries survive and every subsequent launch re-journals them.
func (c *Controller) restoreOutageState(f outageStateFile, now time.Time, known func(id string) bool) {
	if c.outages == nil {
		return
	}
	c.restoreTotalLoss(f.TotalLoss, now)
	states := f.Checks
	kept := states[:0:0]
	for _, s := range states {
		if known != nil && !known(s.ID) {
			slog.Info("dropping a latched outage for a check that no longer exists", "id", s.ID)
			continue
		}
		kept = append(kept, s)
	}

	stale := c.outages.Restore(kept, now, outageRecoveryMaxAge)
	staleIDs := make(map[string]bool, len(stale))
	for _, s := range stale {
		staleIDs[s.ID] = true
		slog.Info("outage from a previous run closed without alerting: too old to be news",
			"id", s.ID, "name", s.Name, "since", s.Since)
		if c.alerts != nil {
			c.alerts.Record(alertlog.Entry{
				ID: s.ID, Title: monitor.TitleProviderRecovered,
				Body:       s.Name + " was still down when the app last ran; its recovery was not observed.",
				Recovery:   true,
				Suppressed: "stale-across-restart",
			})
		}
	}

	// Re-seed the suppression pairing for what actually got re-latched, so a
	// recovery whose outage was never shown is still swallowed.
	c.mu.Lock()
	if c.downSuppressed == nil {
		c.downSuppressed = map[string]bool{}
	}
	restored := 0
	for _, s := range kept {
		if staleIDs[s.ID] {
			continue
		}
		restored++
		if s.Suppressed {
			c.downSuppressed[s.ID] = true
		}
	}
	c.mu.Unlock()

	if restored > 0 {
		slog.Info("re-latched outages from the previous run", "count", restored)
	}
	// Collapse the file onto what is actually latched now. This is what stops a
	// stale or unknown entry from being re-consumed on every future launch.
	c.saveOutageState()
}

// isKnownCheck reports whether id still names a check this build monitors: a
// registered provider, a configured custom URL, or a connectivity target.
//
// It exists because a latched outage outlives the process, and the check it
// names may not. A custom URL deleted while the app was closed leaves an id that
// nothing will ever poll again, so it can never transition its way out of the
// state file on its own.
func (c *Controller) isKnownCheck(id string) bool {
	if id == "" {
		return false
	}
	for _, p := range c.providers {
		if p.ID == id {
			return true
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, u := range c.cfg.CustomURLChecks {
		if u.ID == id {
			return true
		}
	}
	for _, t := range c.allTargetsLocked() {
		if t.ID == id {
			return true
		}
	}
	return false
}

// restoreTotalLoss re-latches an app-wide connectivity outage a previous run
// left open, under the much shorter totalLossMaxAge. A latch too old to
// announce is still journaled, because that closing line — not the toast — is
// what stops internal/audit from carrying the window open forever.
func (c *Controller) restoreTotalLoss(s *monitor.DownState, now time.Time) {
	if s == nil || c.totalLoss == nil {
		return
	}
	unusable := s.Since.IsZero() || s.Since.After(now.Add(time.Hour))
	if unusable || now.Sub(s.Since) > totalLossMaxAge {
		slog.Info("connectivity outage from a previous run closed without alerting: too old to be news",
			"since", s.Since)
		if c.alerts != nil {
			c.alerts.Record(alertlog.Entry{
				Title:      monitor.TitleTotalLossRecovered,
				Body:       "Connectivity was still down when the app last ran; its recovery was not observed.",
				Recovery:   true,
				Suppressed: "stale-across-restart",
			})
		}
		return
	}
	c.totalLoss.RestoreLatch()
	c.mu.Lock()
	c.totalLossSince = s.Since
	if s.Suppressed {
		if c.downSuppressed == nil {
			c.downSuppressed = map[string]bool{}
		}
		c.downSuppressed[""] = true
	}
	c.mu.Unlock()
	slog.Info("re-latched the connectivity outage from the previous run", "since", s.Since)
}

// totalLossStateID labels the connectivity latch inside the state file. The
// alert itself is app-wide and carries an EMPTY id (that is how deliver and the
// alert log mark "not check-scoped"), but a persisted record still needs
// something readable when a human opens the file.
const totalLossStateID = "connectivity-total-loss"
