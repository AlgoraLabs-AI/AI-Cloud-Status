package audit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/alertlog"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/providers"
)

// testProviderID is a synthetic Statuspage-backed provider registered once for
// this test binary, so tests can control its feed payloads directly without
// depending on (or colliding with) any real provider.
const testProviderID = "test-audit-provider"

func init() {
	providers.Register(providers.Provider{
		ID: testProviderID, Name: "Test Audit Provider", Category: providers.CategoryDev,
		Kind: providers.KindStatuspage, URL: "https://example.invalid/summary.json",
	})
}

// statuspageFixture builds a minimal Atlassian-Statuspage-shaped payload with
// one unresolved incident. pageIndicator deliberately can (and in the
// regression-test case, does) disagree with impact — mirroring the real
// mislabeling bug this package exists to see past.
func statuspageFixture(pageIndicator, impact, name string) []byte {
	b, _ := json.Marshal(map[string]any{
		"status": map[string]string{"indicator": pageIndicator},
		"incidents": []map[string]any{
			{"name": name, "status": "investigating", "impact": impact},
		},
	})
	return b
}

// writeCapture writes a feed-samples/<provider>/<ts>-<label>.json file. label
// is whatever FeedCapture would have written at the time — tests intentionally
// use a WRONG label sometimes, since the whole point of this package is to
// never trust it.
func writeCapture(t *testing.T, dir, provider string, ts time.Time, label string, data []byte) string {
	t.Helper()
	pdir := filepath.Join(dir, "feed-samples", provider)
	if err := os.MkdirAll(pdir, 0o755); err != nil {
		t.Fatal(err)
	}
	name := ts.Format(captureTimeFormat) + "-" + label + ".json"
	path := filepath.Join(pdir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// writeAlertLog writes alert-log.jsonl with the given entries.
func writeAlertLog(t *testing.T, dir string, entries []alertlog.Entry) {
	t.Helper()
	var out []byte
	for _, e := range entries {
		b, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, b...)
		out = append(out, '\n')
	}
	if err := os.WriteFile(alertlog.Path(dir), out, 0o644); err != nil {
		t.Fatal(err)
	}
}

// findingsFor returns the test provider's ProviderReport from r, failing the
// test if it's missing.
func findingsFor(t *testing.T, r Report, id string) ProviderReport {
	t.Helper()
	for _, p := range r.Providers {
		if p.ID == id {
			return p
		}
	}
	t.Fatalf("no ProviderReport for %q", id)
	return ProviderReport{}
}

func TestRun_MatchedMajorIncident(t *testing.T) {
	dir := t.TempDir()
	ts := time.Date(2026, 7, 29, 16, 50, 24, 0, time.Local)

	// Mislabeled "minor" on disk, but impact is "major" — the real bug shape.
	writeCapture(t, dir, testProviderID, ts, "minor",
		statuspageFixture("minor", "major", "Elevated errors across all models"))

	writeAlertLog(t, dir, []alertlog.Entry{
		{Time: ts.Add(50 * time.Millisecond), ID: testProviderID, Title: "Provider outage", Body: "..."},
		{Time: ts.Add(2 * time.Hour), ID: testProviderID, Title: "Provider recovered", Recovery: true, Body: "..."},
	})

	report, err := Run(dir)
	if err != nil {
		t.Fatal(err)
	}
	pr := findingsFor(t, report, testProviderID)
	if len(pr.Findings) != 1 {
		t.Fatalf("findings = %d, want 1 (the mislabeled-minor capture should still be recognized as major)", len(pr.Findings))
	}
	if pr.Findings[0].Outcome != Matched {
		t.Errorf("outcome = %v, want Matched", pr.Findings[0].Outcome)
	}
}

func TestRun_UncoveredMajorIncident(t *testing.T) {
	dir := t.TempDir()
	ts := time.Date(2026, 7, 29, 16, 50, 24, 0, time.Local)

	writeCapture(t, dir, testProviderID, ts, "major",
		statuspageFixture("major", "major", "Real outage nobody alerted on"))
	// alert-log exists (so this isn't PreAlertlog) but has no entry anywhere
	// near this provider/time.
	writeAlertLog(t, dir, []alertlog.Entry{
		{Time: ts.Add(-48 * time.Hour), ID: "some-other-provider", Title: "Provider outage"},
	})

	report, err := Run(dir)
	if err != nil {
		t.Fatal(err)
	}
	pr := findingsFor(t, report, testProviderID)
	if len(pr.Findings) != 1 || pr.Findings[0].Outcome != Uncovered {
		t.Fatalf("findings = %+v, want exactly 1 Uncovered", pr.Findings)
	}
	if pr.UncoveredCount() != 1 {
		t.Errorf("UncoveredCount() = %d, want 1", pr.UncoveredCount())
	}
}

func TestRun_SuppressedMajorIncident(t *testing.T) {
	dir := t.TempDir()
	ts := time.Date(2026, 7, 29, 16, 50, 24, 0, time.Local)

	writeCapture(t, dir, testProviderID, ts, "major",
		statuspageFixture("major", "major", "Muted while offline"))
	writeAlertLog(t, dir, []alertlog.Entry{
		{Time: ts, ID: testProviderID, Title: "Provider outage", Suppressed: "offline"},
		{Time: ts.Add(time.Hour), ID: testProviderID, Title: "Provider recovered", Recovery: true, Suppressed: "offline"},
	})

	report, err := Run(dir)
	if err != nil {
		t.Fatal(err)
	}
	pr := findingsFor(t, report, testProviderID)
	if len(pr.Findings) != 1 {
		t.Fatalf("findings = %d, want 1", len(pr.Findings))
	}
	f := pr.Findings[0]
	if f.Outcome != SuppressedOutcome || f.Reason != "offline" {
		t.Errorf("finding = %+v, want SuppressedOutcome with reason %q", f, "offline")
	}
}

func TestRun_PreAlertlogCapture(t *testing.T) {
	dir := t.TempDir()
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.Local)
	logStart := time.Date(2026, 1, 1, 0, 0, 0, 0, time.Local)

	writeCapture(t, dir, testProviderID, old, "major",
		statuspageFixture("major", "major", "Ancient incident, predates the audit log"))
	writeAlertLog(t, dir, []alertlog.Entry{
		{Time: logStart, ID: testProviderID, Title: "Provider outage"},
		{Time: logStart.Add(time.Hour), ID: testProviderID, Title: "Provider recovered", Recovery: true},
	})

	report, err := Run(dir)
	if err != nil {
		t.Fatal(err)
	}
	pr := findingsFor(t, report, testProviderID)
	if len(pr.Findings) != 1 || pr.Findings[0].Outcome != PreAlertlog {
		t.Fatalf("findings = %+v, want exactly 1 PreAlertlog", pr.Findings)
	}
}

func TestRun_StillOpenIncidentMatches(t *testing.T) {
	dir := t.TempDir()
	ts := time.Now().Add(-10 * time.Minute)

	writeCapture(t, dir, testProviderID, ts, "major",
		statuspageFixture("major", "major", "Ongoing, no recovery line yet"))
	writeAlertLog(t, dir, []alertlog.Entry{
		{Time: ts.Add(time.Second), ID: testProviderID, Title: "Provider outage"},
		// no recovery — still open as of "now"
	})

	report, err := Run(dir)
	if err != nil {
		t.Fatal(err)
	}
	pr := findingsFor(t, report, testProviderID)
	if len(pr.Findings) != 1 || pr.Findings[0].Outcome != Matched {
		t.Fatalf("findings = %+v, want exactly 1 Matched (open-ended interval)", pr.Findings)
	}
}

func TestRun_MinorNeverFlagged(t *testing.T) {
	dir := t.TempDir()
	ts := time.Date(2026, 7, 29, 16, 50, 24, 0, time.Local)

	writeCapture(t, dir, testProviderID, ts, "minor",
		statuspageFixture("minor", "minor", "Just a minor blip"))

	report, err := Run(dir)
	if err != nil {
		t.Fatal(err)
	}
	pr := findingsFor(t, report, testProviderID)
	if len(pr.Findings) != 0 {
		t.Fatalf("findings = %+v, want 0 (minor severity is not audited for alert coverage)", pr.Findings)
	}
	if pr.TotalCaptures != 1 {
		t.Errorf("TotalCaptures = %d, want 1", pr.TotalCaptures)
	}
}

func TestRun_NeverCapturedFlagsEnabledFeedBackedProvider(t *testing.T) {
	dir := t.TempDir() // nothing written at all — no config.json, no feed-samples, no alert-log

	report, err := Run(dir)
	if err != nil {
		t.Fatal(err)
	}
	// "openai" is a real, non-Optional (HasPublicFeed) provider that is
	// enabled by default with no config.json present.
	pr := findingsFor(t, report, "openai")
	if !pr.NeverCaptured() {
		t.Errorf("openai.NeverCaptured() = false, want true (enabled, feed-backed, zero captures)")
	}

	// "cohere" is Optional (best-effort feed) — its silence must NOT be
	// flagged the same way.
	cohere := findingsFor(t, report, "cohere")
	if cohere.NeverCaptured() {
		t.Errorf("cohere.NeverCaptured() = true, want false (Optional providers are expected to sometimes have no feed)")
	}
}

func TestRun_RegionScopingMatchesLiveAlerting(t *testing.T) {
	dir := t.TempDir()
	ts := time.Date(2026, 7, 29, 16, 50, 24, 0, time.Local)

	// Statuspage incidents are always global (no region data — see
	// statuspage.go) so region scoping doesn't suppress them; this test
	// instead pins that an EMPTY regions-of-interest list (this repo's
	// current real-world config) does NOT fall back to the page-level
	// indicator when incidents are present — the exact regression this
	// package's trueSeverity helper exists to prevent.
	writeCapture(t, dir, testProviderID, ts, "none", // page indicator says "none"
		statuspageFixture("none", "major", "Page indicator lags the real incident"))
	writeAlertLog(t, dir, []alertlog.Entry{
		{Time: ts, ID: testProviderID, Title: "Provider outage"},
		{Time: ts.Add(time.Hour), ID: testProviderID, Title: "Provider recovered", Recovery: true},
	})

	report, err := Run(dir)
	if err != nil {
		t.Fatal(err)
	}
	pr := findingsFor(t, report, testProviderID)
	if len(pr.Findings) != 1 || pr.Findings[0].Outcome != Matched {
		t.Fatalf("findings = %+v, want exactly 1 Matched — a page-level \"none\" must not hide a major incident", pr.Findings)
	}
}

// TestOutcomeString is a light sanity check that every Outcome has a distinct
// label (the CLI report prints these directly).
func TestOutcomeString(t *testing.T) {
	seen := map[string]bool{}
	for _, o := range []Outcome{Matched, SuppressedOutcome, PreAlertlog, Uncovered} {
		s := o.String()
		if s == "" || s == "unknown" {
			t.Errorf("Outcome %d has no distinct label", o)
		}
		if seen[s] {
			t.Errorf("duplicate Outcome label %q", s)
		}
		seen[s] = true
	}
}

func TestRun_ParseErrorCaptureSkippedNotCrashed(t *testing.T) {
	dir := t.TempDir()
	ts := time.Date(2026, 7, 29, 16, 50, 24, 0, time.Local)
	writeCapture(t, dir, testProviderID, ts, "major", []byte("this is not json"))

	report, err := Run(dir)
	if err != nil {
		t.Fatal(err)
	}
	pr := findingsFor(t, report, testProviderID)
	if pr.TotalCaptures != 1 {
		t.Errorf("TotalCaptures = %d, want 1 (the corrupt file still counts as a capture)", pr.TotalCaptures)
	}
	if len(pr.Findings) != 0 {
		t.Errorf("findings = %+v, want 0 (a parse error must never be treated as a major finding)", pr.Findings)
	}
}

func TestRun_MalformedCaptureFilenameSkipped(t *testing.T) {
	dir := t.TempDir()
	pdir := filepath.Join(dir, "feed-samples", testProviderID)
	if err := os.MkdirAll(pdir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Neither of these matches the "<15-char timestamp>-<label>.json" shape.
	for _, name := range []string{"garbage.json", "20260729-major.json", ".DS_Store"} {
		if err := os.WriteFile(filepath.Join(pdir, name), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	report, err := Run(dir) // must not panic or error on unparseable filenames
	if err != nil {
		t.Fatal(err)
	}
	pr := findingsFor(t, report, testProviderID)
	if pr.TotalCaptures != 0 {
		t.Errorf("TotalCaptures = %d, want 0 (every filename here is unparseable and should be skipped)", pr.TotalCaptures)
	}
}

func TestRun_MalformedAlertLogLineSkipped(t *testing.T) {
	dir := t.TempDir()
	ts := time.Date(2026, 7, 29, 16, 50, 24, 0, time.Local)
	writeCapture(t, dir, testProviderID, ts, "major",
		statuspageFixture("major", "major", "Should still match despite a garbage line elsewhere in the log"))

	good1, _ := json.Marshal(alertlog.Entry{Time: ts, ID: testProviderID, Title: "Provider outage"})
	good2, _ := json.Marshal(alertlog.Entry{Time: ts.Add(time.Hour), ID: testProviderID, Title: "Provider recovered", Recovery: true})
	raw := append(append(append([]byte("not even close to json\n"), good1...), '\n'), append(good2, '\n')...)
	if err := os.WriteFile(alertlog.Path(dir), raw, 0o644); err != nil {
		t.Fatal(err)
	}

	report, err := Run(dir)
	if err != nil {
		t.Fatal(err)
	}
	pr := findingsFor(t, report, testProviderID)
	if len(pr.Findings) != 1 || pr.Findings[0].Outcome != Matched {
		t.Fatalf("findings = %+v, want exactly 1 Matched (the garbage line must not break parsing of the rest)", pr.Findings)
	}
}

func TestIntervalsFrom_RestartMidOutageMergesIntoOneWindow(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	ts := []transition{
		{Time: t0, Outage: true},
		// The app restarted mid-outage and re-alerted — must NOT start a
		// second window.
		{Time: t0.Add(5 * time.Minute), Outage: true},
		{Time: t0.Add(10 * time.Minute), Outage: false},
	}
	windows := intervalsFrom(ts)
	if len(windows) != 1 {
		t.Fatalf("windows = %+v, want exactly 1 merged window", windows)
	}
	if !windows[0].Start.Equal(t0) {
		t.Errorf("Start = %v, want the FIRST open (%v), not the restart re-open", windows[0].Start, t0)
	}
	if !windows[0].End.Equal(t0.Add(10 * time.Minute)) {
		t.Errorf("End = %v, want %v", windows[0].End, t0.Add(10*time.Minute))
	}
}

func TestIntervalsFrom_OrphanRecoverySkippedNotCrashed(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	ts := []transition{
		{Time: t0, Outage: false}, // recovery with no preceding open (retention truncated the open)
		{Time: t0.Add(time.Hour), Outage: true},
		{Time: t0.Add(2 * time.Hour), Outage: false},
	}
	windows := intervalsFrom(ts)
	if len(windows) != 1 {
		t.Fatalf("windows = %+v, want exactly 1 window (the orphan recovery must be ignored, not fabricate a zero-length interval)", windows)
	}
	if !windows[0].Start.Equal(t0.Add(time.Hour)) {
		t.Errorf("Start = %v, want %v", windows[0].Start, t0.Add(time.Hour))
	}
}

func TestIntervalCovers_ToleranceBoundary(t *testing.T) {
	iv := interval{Start: time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC), End: time.Date(2026, 1, 1, 10, 5, 0, 0, time.UTC)}
	tol := 5 * time.Minute
	cases := []struct {
		name string
		t    time.Time
		want bool
	}{
		{"just before tolerance window", iv.Start.Add(-tol - time.Second), false},
		{"exactly at tolerance start", iv.Start.Add(-tol), true},
		{"inside interval", iv.Start.Add(2 * time.Minute), true},
		{"exactly at tolerance end", iv.End.Add(tol), true},
		{"just after tolerance window", iv.End.Add(tol + time.Second), false},
	}
	for _, c := range cases {
		if got := iv.covers(c.t, tol); got != c.want {
			t.Errorf("%s: covers(%v) = %v, want %v", c.name, c.t, got, c.want)
		}
	}
}

func TestIntervalCovers_OpenEndedNeverExpires(t *testing.T) {
	iv := interval{Start: time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)} // End is zero: still open
	farFuture := iv.Start.Add(365 * 24 * time.Hour)
	if !iv.covers(farFuture, time.Minute) {
		t.Error("an open-ended interval must cover any time after Start, however far in the future")
	}
}

func TestTrueSeverity_FallsBackToAggregateOnlyWhenNoIncidents(t *testing.T) {
	// No incidents at all: the aggregate is the only signal, and region
	// filtering is irrelevant with none present — must fall through to it.
	res := providers.Result{Severity: providers.SevMajor}
	if got := trueSeverity(res, nil); got != providers.SevMajor {
		t.Errorf("no-incidents case: trueSeverity = %v, want SevMajor (from the aggregate)", got)
	}

	// Incidents present: even with a MAJOR page-level aggregate, the worst
	// IN-SCOPE incident severity must win, never the aggregate directly —
	// this is the exact regression this helper exists to prevent.
	res2 := providers.Result{
		Severity: providers.SevMajor,
		Incidents: []providers.Incident{
			{Summary: "minor thing", Severity: providers.SevMinor},
		},
	}
	if got := trueSeverity(res2, nil); got != providers.SevMinor {
		t.Errorf("incidents-present case: trueSeverity = %v, want SevMinor (the incident's own severity, not the page aggregate)", got)
	}
}

func TestTrueSeverity_RegionFiltering(t *testing.T) {
	res := providers.Result{
		Incidents: []providers.Incident{
			{Summary: "US outage", Severity: providers.SevCritical, Regions: []string{"us-east-1"}},
			{Summary: "EU blip", Severity: providers.SevMinor, Regions: []string{"eu-west-1"}},
		},
	}
	if got := trueSeverity(res, []string{"eu-west-1"}); got != providers.SevMinor {
		t.Errorf("filtered to eu-west-1: trueSeverity = %v, want SevMinor (the critical US incident is out of scope)", got)
	}
	if got := trueSeverity(res, nil); got != providers.SevCritical {
		t.Errorf("unfiltered: trueSeverity = %v, want SevCritical (worst across all regions)", got)
	}
}

func TestWorstSummary_PicksHighestSeverityIncident(t *testing.T) {
	res := providers.Result{
		Summary: "page-level fallback text",
		Incidents: []providers.Incident{
			{Summary: "minor one", Severity: providers.SevMinor},
			{Summary: "major one", Severity: providers.SevMajor},
		},
	}
	if got := worstSummary(res, nil); got != "major one" {
		t.Errorf("worstSummary = %q, want %q", got, "major one")
	}

	empty := providers.Result{Summary: "page-level fallback text"}
	if got := worstSummary(empty, nil); got != "page-level fallback text" {
		t.Errorf("worstSummary with no incidents = %q, want the page-level fallback", got)
	}
}

func TestRun_NonexistentDirReturnsError(t *testing.T) {
	if _, err := Run(filepath.Join(t.TempDir(), "does-not-exist")); err == nil {
		t.Error("Run on a nonexistent directory should error, not silently report an empty-but-valid audit")
	}
}

// TestRun_MissingRecoveryDoesNotMaskLaterIncident is the regression test for
// the HIGH-severity bug a Codex review found: an outage whose recovery was
// never logged used to leave its reconstructed interval open-ended, and
// interval.covers treats an open End as +infinity — so EVERY subsequent
// major capture for that provider, however unrelated, was silently reported
// as "already covered" by the stale window forever. This pins the fix:
// boundOpenIntervals must close the window at the first later capture that
// proves the feed actually recovered, so a genuinely new incident afterward
// is evaluated on its own — and gets caught as Uncovered here, exactly as it
// should be.
func TestRun_MissingRecoveryDoesNotMaskLaterIncident(t *testing.T) {
	dir := t.TempDir()
	t0 := time.Date(2026, 7, 29, 16, 50, 24, 0, time.Local)
	clearedAt := t0.Add(20 * time.Minute)
	laterIncident := clearedAt.Add(2 * time.Hour)

	writeCapture(t, dir, testProviderID, t0, "major",
		statuspageFixture("major", "major", "First incident"))
	// Proof the feed actually recovered, even though alert-log never got the
	// recovery line: a later capture reading minor/none.
	writeCapture(t, dir, testProviderID, clearedAt, "none",
		statuspageFixture("none", "", ""))
	// A second, wholly unrelated major incident much later — must NOT be
	// swallowed by the first incident's still-open (bugged) window.
	writeCapture(t, dir, testProviderID, laterIncident, "major",
		statuspageFixture("major", "major", "Second, unrelated incident"))

	writeAlertLog(t, dir, []alertlog.Entry{
		{Time: t0, ID: testProviderID, Title: "Provider outage"},
		// no recovery line, ever — the missing-recovery scenario
	})

	report, err := Run(dir)
	if err != nil {
		t.Fatal(err)
	}
	pr := findingsFor(t, report, testProviderID)

	if len(pr.MissingRecoveries) != 1 {
		t.Fatalf("MissingRecoveries = %+v, want exactly 1", pr.MissingRecoveries)
	}
	mr := pr.MissingRecoveries[0]
	if !mr.Start.Equal(t0) || !mr.ClearedAt.Equal(clearedAt) {
		t.Errorf("MissingRecovery = %+v, want Start=%v ClearedAt=%v", mr, t0, clearedAt)
	}

	if len(pr.Findings) != 2 {
		t.Fatalf("findings = %+v, want 2 (both major captures)", pr.Findings)
	}
	if pr.Findings[0].Outcome != Matched {
		t.Errorf("first incident outcome = %v, want Matched", pr.Findings[0].Outcome)
	}
	if pr.Findings[1].Outcome != Uncovered {
		t.Errorf("second incident outcome = %v, want Uncovered — it must NOT be masked by the first incident's "+
			"unrecovered, stale window", pr.Findings[1].Outcome)
	}
}

func TestRun_AllCapturesUnparseableFlagged(t *testing.T) {
	dir := t.TempDir()
	ts := time.Date(2026, 7, 29, 16, 50, 24, 0, time.Local)
	writeCapture(t, dir, testProviderID, ts, "major", []byte("not json at all"))
	writeCapture(t, dir, testProviderID, ts.Add(time.Minute), "major", []byte("{also not valid"))

	report, err := Run(dir)
	if err != nil {
		t.Fatal(err)
	}
	pr := findingsFor(t, report, testProviderID)
	if !pr.AllCapturesUnparseable() {
		t.Errorf("AllCapturesUnparseable() = false, want true (%d/%d captures failed)", pr.ParseErrors, pr.TotalCaptures)
	}
	if pr.ParseErrors != 2 {
		t.Errorf("ParseErrors = %d, want 2", pr.ParseErrors)
	}
}

func TestRun_OneOfManyParseErrorsNotFlaggedAsAllUnparseable(t *testing.T) {
	dir := t.TempDir()
	ts := time.Date(2026, 7, 29, 16, 50, 24, 0, time.Local)
	writeCapture(t, dir, testProviderID, ts, "major", []byte("not json"))
	writeCapture(t, dir, testProviderID, ts.Add(time.Minute), "minor",
		statuspageFixture("minor", "minor", "A perfectly fine capture"))

	report, err := Run(dir)
	if err != nil {
		t.Fatal(err)
	}
	pr := findingsFor(t, report, testProviderID)
	if pr.AllCapturesUnparseable() {
		t.Error("AllCapturesUnparseable() = true, want false — only 1 of 2 captures failed")
	}
	if pr.ParseErrors != 1 {
		t.Errorf("ParseErrors = %d, want 1", pr.ParseErrors)
	}
}

func TestFindCovering_PicksNearestIntervalNotFirst(t *testing.T) {
	tol := 10 * time.Minute
	far := interval{Start: time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC), End: time.Date(2026, 1, 1, 8, 5, 0, 0, time.UTC)}
	near := interval{Start: time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC), End: time.Date(2026, 1, 1, 10, 5, 0, 0, time.UTC)}
	// A capture 8 minutes after `near`'s End (within near's tolerance, and
	// also technically within far's tolerance given how wide 10m is relative
	// to the gap) must resolve to whichever interval it's actually CLOSER to.
	t_ := near.End.Add(8 * time.Minute)

	windows := []interval{far, near} // deliberately list the wrong (further) one first
	got, ok := findCovering(windows, t_, tol)
	if !ok {
		t.Fatal("expected a covering interval")
	}
	if !got.Start.Equal(near.Start) {
		t.Errorf("findCovering picked interval starting %v, want the nearer one starting %v", got.Start, near.Start)
	}
}

func TestBoundOpenIntervals_LeavesGenuinelyOngoingOpen(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	windows := []interval{{Start: t0}} // still open, no End
	captures := []Capture{
		{Time: t0.Add(time.Minute), Severity: providers.SevMajor}, // still major — no clearing evidence
	}
	out, missing := boundOpenIntervals(windows, captures, time.Minute)
	if len(missing) != 0 {
		t.Errorf("missing = %+v, want none (no capture ever showed it clearing)", missing)
	}
	if !out[0].End.IsZero() {
		t.Errorf("End = %v, want zero (still genuinely open)", out[0].End)
	}
}
