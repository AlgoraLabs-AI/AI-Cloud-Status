package audit

import (
	"path/filepath"
	"sort"
	"time"

	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/history"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/monitor"
)

// The continuity audit covers what the provider audit structurally cannot.
//
// The provider audit re-derives the truth from archived RAW FEED PAYLOADS — an
// external witness, independent of anything this app decided. Custom URL checks
// and the app-wide connectivity alerts produce no such archive, so they were
// outside its reach entirely: it answered "no missing recoveries" for the
// 2026-08-02 episode in which a URL outage and a total-loss alert were both
// opened and never closed.
//
// What this section CAN do is cross-examine two INDEPENDENT internal witnesses.
// The sample history is written by the poller; the alert journal is written by
// the detector/deliver path. They are separate code paths, and the 2026-08-02
// failure is precisely a divergence between them: the sampler recorded down then
// up, while the alert path recorded only down.
//
// What it CANNOT do, and what the report must say out loud: detect a check that
// was never probed, probed wrongly, or silently disabled. Both witnesses would
// then agree — and both would be wrong. Only external evidence catches that, and
// only the provider section has any.

// ContinuityOutcome classifies one open alert window.
type ContinuityOutcome int

const (
	// ContinuityUnclosed is a real defect: an outage was announced, the sample
	// history shows the check recovered, and no recovery was ever journaled.
	ContinuityUnclosed ContinuityOutcome = iota
	// ContinuityStillDown — the check has not recovered, so an open window is
	// correct, not a finding.
	ContinuityStillDown
	// ContinuityInconclusive — the samples cannot settle it: the run is
	// truncated at the ring's horizon, its recovery was never observed, or the
	// series is gone. Never reported as a defect.
	ContinuityInconclusive
	// ContinuityNoEvidence — nothing in the retained history covers the window
	// at all (the app was off, or the samples aged out).
	ContinuityNoEvidence
)

func (o ContinuityOutcome) String() string {
	switch o {
	case ContinuityUnclosed:
		return "UNCLOSED"
	case ContinuityStillDown:
		return "still down"
	case ContinuityInconclusive:
		return "inconclusive"
	default:
		return "no evidence"
	}
}

// OpenWindow is one outage alert whose recovery is missing from the journal.
type OpenWindow struct {
	ID         string
	Name       string
	Opened     time.Time
	Suppressed string // the reason the OPENING alert was swallowed, if it was
	Outcome    ContinuityOutcome
	// RecoveredAt is when the sample history shows the check healthy again.
	// Only meaningful for ContinuityUnclosed.
	RecoveredAt time.Time
}

// ContinuityReport is the capture-less half of the audit.
type ContinuityReport struct {
	// Checked names the check ids this section actually examined.
	Checked []string
	// Windows holds every open window found, whatever its outcome.
	Windows []OpenWindow
	// StaleClosures counts recoveries journaled as "stale-across-restart" — the
	// deliberate closes. Not a defect, but a rising number is the symptom that
	// restarts are eating recoveries.
	StaleClosures int
	// HistoryHorizon is the oldest sample retained across the audited series,
	// i.e. how far back any claim here can reach.
	HistoryHorizon time.Time
}

// Unclosed returns only the windows that are real defects.
func (r ContinuityReport) Unclosed() []OpenWindow {
	var out []OpenWindow
	for _, w := range r.Windows {
		if w.Outcome == ContinuityUnclosed {
			out = append(out, w)
		}
	}
	return out
}

// RunContinuity audits the alert sources that have no capture corpus: custom URL
// checks and the app-wide connectivity alerts.
func RunContinuity(dir string) (ContinuityReport, error) {
	var rep ContinuityReport

	cfg, err := loadConfig(dir)
	if err != nil {
		return rep, err
	}
	entries, _, err := loadAlertLog(filepath.Join(dir, "alert-log.jsonl"))
	if err != nil {
		return rep, err
	}
	hist, err := history.Load(filepath.Join(dir, "history.json"), history.DefaultCapacity)
	if err != nil {
		return rep, err
	}

	// Only checks that still exist: a deleted URL leaves an orphan series and an
	// unclosable window, and accusing the app over a check the user removed is
	// noise.
	names := map[string]string{}
	known := map[string]bool{}
	for _, u := range cfg.CustomURLChecks {
		known[u.ID] = true
		names[u.ID] = u.Name
	}

	byID := map[string][]transition{}
	for _, e := range entries {
		switch {
		case e.ID == "":
			// App-wide connectivity. Weak evidence by construction — see below.
			if e.Title != monitor.TitleTotalLoss && e.Title != monitor.TitleTotalLossRecovered {
				continue
			}
		case known[e.ID]:
			if e.Title != monitor.TitleProviderOutage && e.Title != monitor.TitleProviderRecovered {
				continue
			}
		default:
			continue // providers are the other section's job; unknown ids are gone
		}
		// Counted here, INSIDE the filter, so provider closures (which the other
		// section owns) cannot inflate a number printed under this heading.
		if e.Recovery && e.Suppressed == "stale-across-restart" {
			rep.StaleClosures++
		}
		byID[e.ID] = append(byID[e.ID], transition{
			Time: e.Time, Outage: !e.Recovery, Suppressed: e.Suppressed,
		})
	}

	for id := range byID {
		rep.Checked = append(rep.Checked, id)
	}
	sort.Strings(rep.Checked)

	for _, id := range rep.Checked {
		series := hist.Series(id)
		if len(series) > 0 {
			if rep.HistoryHorizon.IsZero() || series[0].Time.Before(rep.HistoryHorizon) {
				rep.HistoryHorizon = series[0].Time
			}
		}
		runs := history.DownRuns(series, time.Now())
		for _, iv := range intervalsFrom(byID[id]) {
			if !iv.End.IsZero() {
				continue // properly paired
			}
			w := OpenWindow{
				ID: id, Name: names[id], Opened: iv.Start,
				Suppressed: iv.Suppressed,
				Outcome:    classifyOpenWindow(id, iv.Start, runs),
			}
			if w.Outcome == ContinuityUnclosed {
				w.RecoveredAt = recoveryFor(iv.Start, runs)
			}
			if w.Name == "" {
				w.Name = id
			}
			rep.Windows = append(rep.Windows, w)
		}
	}
	return rep, nil
}

// classifyOpenWindow decides what the SAMPLES say about an alert window whose
// recovery was never journaled.
//
// The honesty gates come straight from history.DownRun's own flags, which is why
// this reuses DownRuns rather than re-deriving runs:
//
//   - Truncated  — the run starts at the ring's oldest sample, so its beginning
//     is a lower bound and it may predate the alert entirely.
//   - Ongoing    — still down; an open window is correct.
//   - Recovered  — the healthy sample that closed it was actually observed. Only
//     this proves the check came back, and only then is a missing recovery
//     alert a defect.
//
// Connectivity (empty id) can never reach a verdict here: the ping series is a
// 600-sample ring, roughly ten minutes, so by the time anyone runs the audit the
// evidence is long gone. Its windows are reported from the journal alone.
func classifyOpenWindow(id string, opened time.Time, runs []history.DownRun) ContinuityOutcome {
	if id == "" {
		return ContinuityNoEvidence
	}
	for _, r := range runs {
		if !overlapsAlert(r, opened) {
			continue
		}
		switch {
		case r.Ongoing:
			return ContinuityStillDown
		case r.Truncated || r.Recovered.IsZero() || !r.Recovered.After(opened):
			// A recovery at or before the alert belongs to an earlier episode; it
			// cannot be evidence that THIS window should have been closed.
			return ContinuityInconclusive
		default:
			return ContinuityUnclosed
		}
	}
	return ContinuityNoEvidence
}

// recoveryFor returns the observed recovery instant of the run matching an
// alert window.
func recoveryFor(opened time.Time, runs []history.DownRun) time.Time {
	for _, r := range runs {
		if overlapsAlert(r, opened) {
			return r.Recovered
		}
	}
	return time.Time{}
}

// alertSampleSlack is how far an alert may sit from the sample run it describes.
// A check is polled on an interval and the alert fires from the poll's result,
// so the two are within one poll of each other; the history file is also only
// flushed periodically, which can shift a boundary by up to a minute.
const alertSampleSlack = 5 * time.Minute

// overlapsAlert reports whether a down-run plausibly IS the outage an alert
// opened at the given instant.
func overlapsAlert(r history.DownRun, opened time.Time) bool {
	start := r.Start.Add(-alertSampleSlack)
	end := r.End.Add(alertSampleSlack)
	if !r.Recovered.IsZero() {
		end = r.Recovered.Add(alertSampleSlack)
	}
	return !opened.Before(start) && !opened.After(end)
}
