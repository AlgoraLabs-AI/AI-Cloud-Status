// Package audit re-derives ground truth from the app's own evidence trail —
// feed-samples/ raw captures and alert-log.jsonl — to answer, with citations,
// whether every real major-severity incident a provider's feed ever reported
// produced a correctly matched outage alert (and recovery, if resolved), and
// to flag providers that have gone suspiciously long without ever reporting
// anything. It is deliberately Fyne-free (no dependency on internal/ui) so it
// can be driven by a fast, GUI-less CLI (cmd/alertaudit).
package audit

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/alertlog"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/config"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/monitor"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/providers"
)

// captureTimeFormat mirrors providers/feedcapture.go's filename timestamp
// layout exactly — it must be kept in sync with that package.
const captureTimeFormat = "20060102-150405"

// minTolerance is the floor for how far apart a capture and the alert-log
// transition it's checked against may drift and still count as the same
// event — covers the ordinary capture-happens-just-before-the-alert-is-
// recorded ordering (see provider.go's Checker.Check, which captures before
// the caller records the alert) plus general poll-cadence slack.
const minTolerance = 5 * time.Minute

// openWindowHorizon bounds how long an outage window may be believed to still
// be open on evidence alone.
//
// It exists because two different silences look identical in the alert log: an
// outage that is genuinely ongoing, and one whose recovery was never recorded
// because the app was closed when the provider cleared (OutageTracker keeps its
// state in memory only). Without a bound, the second case poisoned every later
// verdict — interval.covers reads a zero End as +infinity, so ONE unrecovered
// window certified every subsequent major capture, for months, as already
// alerted. That is the worst possible failure for an auditing tool: reporting
// "all clear" exactly when something was missed.
//
// A day is well past any real outage this app polls for, and comfortably past
// the gap a laptop being closed overnight produces. Past it, the honest reading
// is that the window's evidence ran out — not that it still covers everything.
const openWindowHorizon = 24 * time.Hour

// Capture is one feed-samples/ file re-parsed with the CURRENTLY REGISTERED
// adapter — deliberately ignoring FeedCapture's own filename label, which
// reads a feed's page-level indicator rather than each incident's own impact
// and can disagree with it (confirmed during a 2026-07-31 manual audit).
// Because it re-parses with today's adapter, a Capture reflects "what the
// CURRENT parser makes of this payload", not necessarily what the parser in
// production at capture time actually decided — a later Finding should be
// read as "current parser vs. historical alert trail", not as absolute proof
// of a historical miss (an intervening parser fix or regression can produce
// an apparent mismatch that isn't one).
type Capture struct {
	Provider string
	Path     string
	Time     time.Time
	Severity providers.Severity
	Summary  string
	ParseErr error
}

// Outcome classifies how a true-major Capture relates to the alert-log.
type Outcome int

const (
	// Matched: an outage window (delivered, not suppressed) covered this capture.
	Matched Outcome = iota
	// SuppressedOutcome: an outage window covered this capture, but delivery
	// was suppressed (muted, DND, deactivated regions, offline) — not a bug.
	SuppressedOutcome
	// PreAlertlog: the capture predates alert-log's oldest entry, so it could
	// never have a matching line — a coverage gap, not a processing miss.
	PreAlertlog
	// Uncovered: no outage window (delivered or suppressed) was found near
	// this capture at all — the one outcome worth a human looking at.
	Uncovered
)

func (o Outcome) String() string {
	switch o {
	case Matched:
		return "matched"
	case SuppressedOutcome:
		return "suppressed"
	case PreAlertlog:
		return "pre-alertlog"
	case Uncovered:
		return "UNCOVERED"
	default:
		return "unknown"
	}
}

// Finding is one true-major Capture and how it was (or wasn't) covered.
type Finding struct {
	Capture Capture
	Outcome Outcome
	Reason  string // suppression reason, populated only for SuppressedOutcome
}

// MissingRecovery is an outage whose recovery was never logged in
// alert-log.jsonl, even though a LATER capture of the same provider's feed
// shows the incident had actually cleared (severity dropped back below
// Major). Surfaced as its own finding because an open-ended interval
// (interval.covers treats a zero End as +infinity) would otherwise silently
// "cover" every subsequent capture forever, masking a genuinely new, unrelated
// major incident as if it were still part of the old one — see
// boundOpenIntervals.
type MissingRecovery struct {
	Start     time.Time // when alert-log recorded the outage opening
	ClearedAt time.Time // the later capture proving it actually resolved
}

// ProviderReport summarizes one provider's audit.
type ProviderReport struct {
	ID                string
	Name              string
	Enabled           bool
	HasPublicFeed     bool // Provider.Optional==false, i.e. expected to always have a feed
	TotalCaptures     int
	ParseErrors       int       // captures the CURRENT adapter fails to parse at all
	LastCaptureTime   time.Time // zero if never captured anything
	LastAlertTime     time.Time // zero if never in alert-log
	Findings          []Finding // only true-major captures, chronological
	MissingRecoveries []MissingRecovery
}

// UncoveredCount returns how many Findings are genuinely Uncovered.
func (r ProviderReport) UncoveredCount() int {
	n := 0
	for _, f := range r.Findings {
		if f.Outcome == Uncovered {
			n++
		}
	}
	return n
}

// NeverCaptured reports whether an enabled, feed-backed provider has zero
// captures on record at all — the advisory signal for "this provider's feed
// request or parser might be broken", distinct from "genuinely always
// operational" (which would still be expected to produce an occasional minor
// blip or at least a few captures over a long enough window).
func (r ProviderReport) NeverCaptured() bool {
	return r.Enabled && r.HasPublicFeed && r.TotalCaptures == 0
}

// AllCapturesUnparseable reports whether EVERY capture on record failed to
// parse with the currently registered adapter — the strongest available
// signal that this provider's parser is broken right now (a live check would
// currently be silently landing on parse-error too, invisible to Findings,
// which only ever look at successfully parsed captures).
func (r ProviderReport) AllCapturesUnparseable() bool {
	return r.TotalCaptures > 0 && r.ParseErrors == r.TotalCaptures
}

// Report is the full audit result.
type Report struct {
	GeneratedAt   time.Time
	AlertLogStart time.Time // zero if alert-log.jsonl doesn't exist / is empty
	Providers     []ProviderReport
}

// transition is one alert-log line for a provider, restricted to the
// outage/recovery event kind (OutageTracker's "Provider outage" / "Provider
// recovered" titles) — the same titles both provider checks and custom URL
// checks use, but URL checks carry a "url-…" id that never matches a
// registered provider ID, so filtering by known provider ID alone already
// excludes them without any extra title/kind field.
type transition struct {
	Time       time.Time
	Outage     bool // true = opens an outage window, false = closes one
	Suppressed string
}

// interval is one reconstructed outage window: [Start, End). End is the zero
// Time when the window is still open (no recovery seen yet, or still ongoing
// as of the alert-log's end).
type interval struct {
	Start, End time.Time
	Suppressed string
}

// covers reports whether t falls within the interval, widened by tolerance on
// both ends (an open-ended interval's End counts as +infinity).
func (iv interval) covers(t time.Time, tolerance time.Duration) bool {
	if t.Before(iv.Start.Add(-tolerance)) {
		return false
	}
	if iv.End.IsZero() {
		return true
	}
	return !t.After(iv.End.Add(tolerance))
}

// distance returns how far t sits outside the interval's STRICT [Start,End]
// bounds (zero tolerance) — zero when t already falls inside without needing
// any tolerance at all. Used to break ties when tolerance windows around
// multiple distinct intervals overlap, so the CLOSEST interval wins rather
// than whichever happens to appear first in the slice.
func (iv interval) distance(t time.Time) time.Duration {
	if t.Before(iv.Start) {
		return iv.Start.Sub(t)
	}
	if !iv.End.IsZero() && t.After(iv.End) {
		return t.Sub(iv.End)
	}
	return 0
}

// Run audits the app data directory dir (normally config.Dir()) and returns a
// full Report. regions is the CURRENT config's regions-of-interest — applied
// exactly as live alerting applies it (Result.SeverityInRegions), so the
// audit's notion of "true major" matches what the app itself would have
// alerted on, not an unfiltered global severity that could flag a region
// outside the user's configured interest as a false miss. Note this uses
// TODAY's region configuration for captures of any age; an older capture
// audited against a since-changed region list is a known limitation (no
// historical config snapshots exist to do better).
func Run(dir string) (Report, error) {
	if fi, err := os.Stat(dir); err != nil {
		return Report{}, fmt.Errorf("app data directory: %w", err)
	} else if !fi.IsDir() {
		return Report{}, fmt.Errorf("app data directory %s is not a directory", dir)
	}

	cfg, err := loadConfig(dir)
	if err != nil {
		return Report{}, fmt.Errorf("load config: %w", err)
	}

	byID := map[string]providers.Provider{}
	for _, p := range providers.Default() {
		byID[p.ID] = p
	}

	entries, alertStart, err := loadAlertLog(alertlog.Path(dir))
	if err != nil {
		return Report{}, fmt.Errorf("load alert log: %w", err)
	}
	transitionsByID := map[string][]transition{}
	lastAlertByID := map[string]time.Time{}
	for _, e := range entries {
		if e.ID == "" {
			continue // app-wide connectivity alert, not provider-scoped
		}
		if _, known := byID[e.ID]; !known {
			continue // e.g. a custom URL check id — out of scope for this audit
		}
		if e.Title != monitor.TitleProviderOutage && e.Title != monitor.TitleProviderRecovered {
			continue
		}
		transitionsByID[e.ID] = append(transitionsByID[e.ID], transition{
			Time: e.Time, Outage: !e.Recovery, Suppressed: e.Suppressed,
		})
		if e.Time.After(lastAlertByID[e.ID]) {
			lastAlertByID[e.ID] = e.Time
		}
	}

	tolerance := minTolerance
	if s := cfg.IntervalSeconds; s > 0 {
		if t := time.Duration(s) * time.Second * 3; t > tolerance {
			tolerance = t
		}
	}

	feedSamplesDir := filepath.Join(dir, "feed-samples")
	report := Report{GeneratedAt: time.Now(), AlertLogStart: alertStart}
	for _, p := range providers.Default() {
		captures, err := loadCaptures(filepath.Join(feedSamplesDir, p.ID), p, cfg.Regions)
		if err != nil {
			return Report{}, fmt.Errorf("load captures for %s: %w", p.ID, err)
		}
		windows := intervalsFrom(transitionsByID[p.ID])
		windows, missing := boundOpenIntervals(windows, captures, tolerance)

		pr := ProviderReport{
			ID:                p.ID,
			Name:              p.Name,
			Enabled:           cfg.IsEnabled(p.ID, true),
			HasPublicFeed:     !p.Optional,
			TotalCaptures:     len(captures),
			LastAlertTime:     lastAlertByID[p.ID],
			MissingRecoveries: missing,
		}
		for _, c := range captures {
			if c.Time.After(pr.LastCaptureTime) {
				pr.LastCaptureTime = c.Time
			}
			if c.ParseErr != nil {
				pr.ParseErrors++
				continue
			}
			if c.Severity < providers.SevMajor {
				continue
			}
			f := Finding{Capture: c}
			switch {
			// The tolerance buffer matters here, not just below: a capture from
			// the SAME poll that produced alert-log's very first-ever line can
			// land a few milliseconds earlier (capture happens during Checker.
			// Check, before the caller records the alert — see provider.go), so
			// a bare Before(alertStart) would misclassify that genuine first
			// match as "predates the log" instead of Matched.
			case !alertStart.IsZero() && c.Time.Before(alertStart.Add(-tolerance)):
				f.Outcome = PreAlertlog
			default:
				iv, ok := findCovering(windows, c.Time, tolerance)
				switch {
				case !ok:
					f.Outcome = Uncovered
				case iv.Suppressed != "":
					f.Outcome = SuppressedOutcome
					f.Reason = iv.Suppressed
				default:
					f.Outcome = Matched
				}
			}
			pr.Findings = append(pr.Findings, f)
		}
		report.Providers = append(report.Providers, pr)
	}
	sort.SliceStable(report.Providers, func(i, j int) bool { return report.Providers[i].Name < report.Providers[j].Name })
	return report, nil
}

// boundOpenIntervals closes any still-open interval as of the first LATER
// capture that proves the incident actually cleared on the raw feed
// (severity dropped below Major), even though alert-log never recorded the
// recovery. Without this, an open-ended interval covers every subsequent
// capture forever (interval.covers treats a zero End as +infinity), which
// would silently mark a genuinely new, unrelated major incident as already
// "covered" by a stale window instead of flagging it. captures must be
// sorted chronologically (loadCaptures already returns them that way). An
// interval with no later clearing evidence is left open — it may genuinely
// still be ongoing, and there is no way to tell the difference from evidence
// alone.
func boundOpenIntervals(windows []interval, captures []Capture, tolerance time.Duration) ([]interval, []MissingRecovery) {
	out := make([]interval, len(windows))
	copy(out, windows)
	var missing []MissingRecovery
	// recordEnd is the newest moment we have any evidence for at all. Captures
	// are sorted chronologically by loadCaptures.
	recordEnd := time.Time{}
	if len(captures) > 0 {
		recordEnd = captures[len(captures)-1].Time
	}
	for i := range out {
		if !out[i].End.IsZero() {
			continue
		}
		cutoff := out[i].Start.Add(tolerance)
		cleared := false
		// lastSupport is the newest capture that still corroborates this window
		// being open: a major reading, close enough in time to the previous one
		// to be the same continuous episode.
		lastSupport := out[i].Start
		for _, c := range captures {
			if c.ParseErr != nil || !c.Time.After(cutoff) {
				continue
			}
			if c.Severity < providers.SevMajor {
				out[i].End = c.Time
				missing = append(missing, MissingRecovery{Start: out[i].Start, ClearedAt: c.Time})
				cleared = true
				break
			}
			// A major capture arriving after a long silence is NOT evidence that
			// this window stayed open through the silence — the app was almost
			// certainly closed, which is precisely why no recovery was ever
			// logged. Stop here and let the later capture be judged on its own.
			if c.Time.Sub(lastSupport) > openWindowHorizon {
				break
			}
			lastSupport = c.Time
		}
		if cleared {
			continue
		}
		// No clearing capture. Whether that means "still ongoing" or "the app was
		// closed when it cleared" cannot be settled from this window alone — but
		// it CAN be settled by whether the record continues past the point the
		// window stopped being corroborated.
		//
		// If it does, the window went stale and must be bounded, because
		// interval.covers treats a zero End as +infinity: one outage whose
		// recovery was never logged would otherwise certify EVERY later major
		// capture, for months, as already alerted — the tool reporting "all
		// clear" exactly when something was missed. Captures only exist for polls
		// that actually ran, so a major capture past the bound is a moment the
		// app WAS running and nothing alerted: a real finding, not something to
		// absorb.
		//
		// If the record does not continue past it, the outage is plausibly still
		// happening. Leave it open — there is nothing later for a stale window to
		// poison, and closing it would invent a recovery that may not exist.
		bound := lastSupport.Add(openWindowHorizon)
		if !recordEnd.IsZero() && recordEnd.After(bound) {
			out[i].End = bound
			missing = append(missing, MissingRecovery{Start: out[i].Start})
		}
	}
	return out, missing
}

// intervalsFrom reconstructs outage windows from a provider's transitions
// (already in file order = chronological). A stray recovery with no
// preceding open (the window's true start fell outside alert-log's retained
// history) is skipped rather than fabricating a zero-length interval. A
// second consecutive open (e.g. the app restarted mid-outage and re-alerted)
// extends the existing window rather than starting a new one.
func intervalsFrom(ts []transition) []interval {
	var out []interval
	var cur *interval
	lastOpen := time.Time{}
	for _, t := range ts {
		switch {
		case t.Outage && cur == nil:
			cur = &interval{Start: t.Time, Suppressed: t.Suppressed}
			lastOpen = t.Time
		case t.Outage && t.Time.Sub(lastOpen) > openWindowHorizon:
			// Too far from the previous open to be the same episode. The app
			// holds outage state in memory only, so being closed across a
			// recovery emits open…open with no recovery between — and merging
			// those swallowed everything in the gap: the merged window has a
			// non-zero End, so it is never examined for a missing recovery, and
			// covers() marked every capture in between as already alerted.
			out = append(out, *cur) // the earlier window ends unrecovered
			cur = &interval{Start: t.Time, Suppressed: t.Suppressed}
			lastOpen = t.Time
		case t.Outage:
			// already open, and recent — the app restarted mid-outage and
			// re-alerted for the same episode. Extend rather than split.
			lastOpen = t.Time
		case !t.Outage && cur != nil:
			cur.End = t.Time
			out = append(out, *cur)
			cur = nil
		}
	}
	if cur != nil {
		out = append(out, *cur) // still open as of the log's end
	}
	return out
}

// findCovering returns the CLOSEST interval covering t within tolerance (the
// one needing the least tolerance-stretch), not merely the first one in the
// slice — tolerance windows around distinct, legitimately separate incidents
// can overlap (tolerance defaults to 3x the poll interval), and picking
// "whichever appears first" could silently attribute a capture to the wrong
// incident's suppression state or hide an adjacent real gap.
func findCovering(windows []interval, t time.Time, tolerance time.Duration) (interval, bool) {
	best := -1
	var bestDist time.Duration
	for i, w := range windows {
		if !w.covers(t, tolerance) {
			continue
		}
		if d := w.distance(t); best < 0 || d < bestDist {
			best, bestDist = i, d
		}
	}
	if best < 0 {
		return interval{}, false
	}
	return windows[best], true
}

// loadConfig reads dir/config.json directly (rather than calling
// config.Load, which always resolves the REAL os.UserConfigDir() regardless
// of the dir passed in — unusable when auditing a custom/test directory).
// Config's fields carry their own json tags, so decoding the raw file is
// equivalent to the app's own load path for the fields this package reads
// (Regions, Enabled, IntervalSeconds); a missing file returns config.Default().
func loadConfig(dir string) (config.Config, error) {
	data, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return config.Default(), nil
		}
		return config.Config{}, err
	}
	cfg := config.Default()
	if err := json.Unmarshal(data, &cfg); err != nil {
		return config.Config{}, err
	}
	return cfg, nil
}

// loadAlertLog reads every line of path into Entries sorted by time, and
// returns the earliest entry's time (zero if the file is absent/empty).
// Unparsable lines are skipped rather than failing the whole read — the audit
// is best-effort evidence, not a strict schema validator.
func loadAlertLog(path string) ([]alertlog.Entry, time.Time, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, time.Time{}, nil
		}
		return nil, time.Time{}, err
	}
	defer f.Close()

	var entries []alertlog.Entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e alertlog.Entry
		if json.Unmarshal(line, &e) != nil {
			continue
		}
		entries = append(entries, e)
	}
	if err := sc.Err(); err != nil {
		return nil, time.Time{}, err
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Time.Before(entries[j].Time) })
	var start time.Time
	if len(entries) > 0 {
		start = entries[0].Time
	}
	return entries, start, nil
}

// captureTime reads the absolute instant a capture was written from its
// filename.
//
// Two shapes exist. Current captures are UTC and carry a "Z" right after the
// timestamp ("20260801-155355Z-major.json"). Legacy captures, written before the
// zone bug was found, are local wall clock with no offset at all
// ("20260801-155355-major.json") and can only be re-derived in the local zone —
// which is exactly as wrong as it was when written, but no worse, and it is the
// only reading available. Anything that predates the fix therefore keeps its
// original ambiguity while everything new is exact.
func captureTime(name string) (time.Time, error) {
	if len(name) < len(captureTimeFormat) {
		return time.Time{}, fmt.Errorf("capture name %q is too short to carry a timestamp", name)
	}
	stamp := name[:len(captureTimeFormat)]
	if len(name) > len(captureTimeFormat) && name[len(captureTimeFormat)] == 'Z' {
		return time.Parse(captureTimeFormat, stamp)
	}
	return time.ParseInLocation(captureTimeFormat, stamp, time.Local)
}

// loadCaptures re-parses every feed-samples/<provider> file with the
// currently registered adapter for p.Kind, ignoring the directory entirely
// when it doesn't exist (a provider that has never captured anything).
// regions is the CURRENT config's regions-of-interest, applied via
// Result.SeverityInRegions exactly as live alerting applies it — an
// unfiltered (nil-interest) severity would flag a major incident confined to
// a region outside the user's configured interest as a false miss, since live
// alerting would correctly never have fired on it either.
func loadCaptures(dir string, p providers.Provider, regions []string) ([]Capture, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Capture
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		ts, err := captureTime(name)
		if err != nil {
			continue // unexpected filename shape; skip rather than guess
		}
		data, err := readCapture(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		c := Capture{Provider: p.ID, Path: filepath.Join(dir, name), Time: ts}
		res, perr := providers.ParseFeed(p, data)
		if perr != nil {
			c.ParseErr = perr
		} else {
			c.Severity = trueSeverity(res, regions)
			c.Summary = worstSummary(res, regions)
		}
		out = append(out, c)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	return out, nil
}

// readCapture reads one archived feed payload, transparently decompressing a
// gzipped one. Captures are written gzipped (they are JSON/XML and compress
// ~10:1, which is what lets the corpus have a size budget at all), but a corpus
// collected by an earlier build is plain — and that corpus is precisely the
// evidence this tool exists to replay, so it must keep reading it.
//
// The suffix is not trusted on its own: the gzip magic number is checked, so a
// file whose name lies is read as what it actually is rather than failing to
// parse for a reason nobody would guess from the error.
func readCapture(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) < 2 || data[0] != 0x1f || data[1] != 0x8b {
		return data, nil
	}
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", filepath.Base(path), err)
	}
	defer zr.Close()
	return io.ReadAll(zr)
}

// trueSeverity is the worst in-scope incident severity, falling back to the
// feed's page-level aggregate only when it lists no discrete incidents.
//
// This deliberately does NOT call Result.SeverityInRegions: that method's
// contract (provider.go) is "an empty interest set returns the global
// Severity unchanged" — a shortcut that's exactly right for its real callers
// (an unconfigured region list means don't filter), but wrong for THIS
// purpose, because "no region filter configured" is not the same question as
// "does this feed have per-incident detail". With an empty interest and a
// non-empty Incidents list, SeverityInRegions would return the page-level
// aggregate/indicator field — the very field FeedCapture's filename label
// (wrongly) uses, and the whole reason this package exists is that the
// aggregate and the worst incident's own impact frequently disagree. Mirrors
// internal/ui/regionmute.go's effectiveSeverity, minus the region-muting
// argument, which is a display/delivery concern orthogonal to "was this
// really a major incident".
func trueSeverity(res providers.Result, regions []string) providers.Severity {
	if len(res.Incidents) == 0 {
		return res.SeverityInRegions(regions)
	}
	worst := providers.SevNone
	for _, inc := range res.IncidentsInRegions(regions) {
		if inc.Severity > worst {
			worst = inc.Severity
		}
	}
	return worst
}

// worstSummary returns the worst-severity in-scope incident's own Summary,
// falling back to the page-level Result.Summary when the feed lists no
// discrete (in-scope) incidents (an aggregate-only adapter, or one whose
// incidents are all outside regions).
func worstSummary(res providers.Result, regions []string) string {
	worst := providers.SevNone
	summary := ""
	for _, inc := range res.IncidentsInRegions(regions) {
		if inc.Severity >= worst {
			worst = inc.Severity
			summary = inc.Summary
		}
	}
	if summary == "" {
		return res.Summary
	}
	return summary
}
