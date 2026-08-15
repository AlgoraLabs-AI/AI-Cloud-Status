package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/config"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/history"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/i18n"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/providers"
)

func minor(regions ...string) providers.Incident {
	return providers.Incident{Summary: "x", Severity: providers.SevMinor, Regions: regions}
}

// muteSet returns an isMuted predicate that matches the given region keys via the
// same substring-tolerant matcher used in production.
func muteSet(keys ...string) func(string) bool {
	return func(region string) bool {
		for _, k := range keys {
			if providers.MatchRegion(region, []string{k}) {
				return true
			}
		}
		return false
	}
}

func TestSampleDownRegions(t *testing.T) {
	cases := []struct {
		name         string
		incs         []providers.Incident
		wantRegions  []string
		wantUnscoped bool
	}{
		{"minor only → nothing down", []providers.Incident{minor("us-east-1")}, nil, false},
		{"regional major", []providers.Incident{major("us-east-1")}, []string{"us-east-1"}, false},
		{"global major → unscoped", []providers.Incident{major()}, nil, true},
		{"mixed global+regional → unscoped (global wins)", []providers.Incident{major("us-east-1"), major()}, nil, true},
		{"two regional majors → union", []providers.Incident{major("us-east-1"), major("eu-west-1")}, []string{"us-east-1", "eu-west-1"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := providers.Result{Feed: providers.FeedReachable, Incidents: tc.incs}
			regions, unscoped := sampleDownRegions(res, nil)
			if unscoped != tc.wantUnscoped {
				t.Fatalf("unscoped = %v, want %v", unscoped, tc.wantUnscoped)
			}
			if len(regions) != len(tc.wantRegions) {
				t.Fatalf("regions = %v, want %v", regions, tc.wantRegions)
			}
			for i, r := range tc.wantRegions {
				if regions[i] != r {
					t.Fatalf("regions = %v, want %v", regions, tc.wantRegions)
				}
			}
		})
	}
}

func TestSampleDegradedRegions(t *testing.T) {
	cases := []struct {
		name         string
		incs         []providers.Incident
		wantRegions  []string
		wantUnscoped bool
	}{
		{"major only → nothing degraded", []providers.Incident{major("us-east-1")}, nil, false},
		{"regional minor", []providers.Incident{minor("ap-south-1")}, []string{"ap-south-1"}, false},
		{"global minor → unscoped", []providers.Incident{minor()}, nil, true},
		{"minor alongside major → only minor regions", []providers.Incident{major("us-east-1"), minor("ap-south-1")}, []string{"ap-south-1"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := providers.Result{Feed: providers.FeedReachable, Incidents: tc.incs}
			regions, unscoped := sampleDegradedRegions(res, nil)
			if unscoped != tc.wantUnscoped {
				t.Fatalf("unscoped = %v, want %v", unscoped, tc.wantUnscoped)
			}
			if len(regions) != len(tc.wantRegions) {
				t.Fatalf("regions = %v, want %v", regions, tc.wantRegions)
			}
			for i, r := range tc.wantRegions {
				if regions[i] != r {
					t.Fatalf("regions = %v, want %v", regions, tc.wantRegions)
				}
			}
		})
	}
}

func TestSamplePaint(t *testing.T) {
	cases := []struct {
		name   string
		sample history.Sample
		muted  func(string) bool
		want   int
	}{
		{"operational", history.Sample{Up: true}, muteSet("us-east-1"), samplePaintOK},
		{"unscoped down always red", history.Sample{Up: false}, muteSet("us-east-1"), samplePaintDown},
		{"regional down active → red", history.Sample{Up: false, DownRegions: []string{"us-east-1"}}, muteSet(), samplePaintDown},
		{"regional down muted → green", history.Sample{Up: false, DownRegions: []string{"us-east-1"}}, muteSet("us-east-1"), samplePaintOK},
		{"two down regions, one active → red", history.Sample{Up: false, DownRegions: []string{"us-east-1", "eu-west-1"}}, muteSet("us-east-1"), samplePaintDown},
		{"unscoped degraded → amber", history.Sample{Up: true, Degraded: true}, muteSet(), samplePaintDegraded},
		{"regional degraded active → amber", history.Sample{Up: true, Degraded: true, DegradedRegions: []string{"ap-south-1"}}, muteSet(), samplePaintDegraded},
		{"regional degraded muted → green", history.Sample{Up: true, Degraded: true, DegradedRegions: []string{"ap-south-1"}}, muteSet("ap-south-1"), samplePaintOK},
		{"down wins over degraded", history.Sample{Up: false, DownRegions: []string{"us-east-1"}, Degraded: true, DegradedRegions: []string{"ap-south-1"}}, muteSet(), samplePaintDown},
		{"major muted reveals amber from minor", history.Sample{Up: false, DownRegions: []string{"us-east-1"}, Degraded: true, DegradedRegions: []string{"ap-south-1"}}, muteSet("us-east-1"), samplePaintDegraded},
		{"both muted → green", history.Sample{Up: false, DownRegions: []string{"us-east-1"}, Degraded: true, DegradedRegions: []string{"ap-south-1"}}, muteSet("us-east-1", "ap-south-1"), samplePaintOK},
		// The 2026-07-17 regression: an unreadable FEED (local internet blip) must
		// paint grey/unknown, never red — mirroring Status/alerting, which never
		// treat "could not check" as an outage.
		{"unknown feed → grey, never red", history.Sample{Up: false, Unknown: true}, muteSet(), samplePaintUnknown},
		{"unknown wins over any down detail", history.Sample{Up: false, Unknown: true, DownRegions: []string{"us-east-1"}}, muteSet(), samplePaintUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := samplePaint(tc.sample, tc.muted); got != tc.want {
				t.Fatalf("samplePaint = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestUptimeFractionIgnoresDegraded locks the decision: degraded (amber) bands do
// NOT lower the percentage — only full outages (red) do.
func TestUptimeFractionIgnoresDegraded(t *testing.T) {
	if got := uptimeFraction([]int{samplePaintOK, samplePaintDegraded, samplePaintOK, samplePaintDegraded}); got != 1.0 {
		t.Fatalf("all-not-down fraction = %v, want 1.0", got)
	}
	if got := uptimeFraction([]int{samplePaintOK, samplePaintDown, samplePaintDegraded, samplePaintOK}); got != 0.75 {
		t.Fatalf("one-down-of-four = %v, want 0.75", got)
	}
}

// TestUptimeFractionExcludesUnknown locks the unknown semantics: unreadable-feed
// samples are outside the denominator (neither up nor down), and a window with
// ONLY unknown samples has no percentage at all (-1), not a fake 0% or 100%.
func TestUptimeFractionExcludesUnknown(t *testing.T) {
	if got := uptimeFraction([]int{samplePaintOK, samplePaintUnknown, samplePaintOK}); got != 1.0 {
		t.Fatalf("unknown counted in denominator: %v, want 1.0", got)
	}
	if got := uptimeFraction([]int{samplePaintDown, samplePaintUnknown}); got != 0 {
		t.Fatalf("one-down-one-unknown = %v, want 0 (the only OBSERVED sample was down)", got)
	}
	if got := uptimeFraction([]int{samplePaintUnknown, samplePaintUnknown}); got != -1 {
		t.Fatalf("all-unknown = %v, want -1 (unknowable)", got)
	}
	if got := uptimeFraction(nil); got != -1 {
		t.Fatalf("empty = %v, want -1", got)
	}
}

// An unknown sample must not colour its time bucket: a bucket holding only
// unknown observations stays grey, and a green observation in the same bucket
// still wins over unknown.
func TestBucketPaintsUnknownSamples(t *testing.T) {
	end := time.Unix(1_800_000_000, 0)
	window := sparkBuckets * time.Minute // 1 bucket per minute
	samples := []history.Sample{
		{Time: end.Add(-30 * time.Second)}, // newest bucket: unknown only
		{Time: end.Add(-90 * time.Second)}, // second bucket: unknown + OK
		{Time: end.Add(-100 * time.Second)},
	}
	paints := []int{samplePaintUnknown, samplePaintUnknown, samplePaintOK}
	out := bucketPaints(samples, paints, window, end)
	if out[sparkBuckets-1] != samplePaintUnknown {
		t.Errorf("unknown-only bucket = %d, want unknown/grey", out[sparkBuckets-1])
	}
	if out[sparkBuckets-2] != samplePaintOK {
		t.Errorf("mixed bucket = %d, want OK (observation beats unknown)", out[sparkBuckets-2])
	}
}

// TestEffectiveSeverityAgreesWithAlertSuppression locks the invariant from the
// consensus review: status (effectiveSeverity) and alert delivery
// (regionAlertSuppressed) must use the SAME muting kernel, so a fully-muted major
// outage drops Status to operational EXACTLY when the alert is suppressed.
func TestEffectiveSeverityAgreesWithAlertSuppression(t *testing.T) {
	cases := []struct {
		name            string
		incs            []providers.Incident
		muted           map[string]int64
		wantSuppress    bool // alert withheld
		wantOperational bool // effective severity below major
	}{
		{"regional major fully muted", []providers.Incident{major("us-east-1")}, map[string]int64{"us-east-1": config.RegionMuteForever}, true, true},
		{"regional major unmuted", []providers.Incident{major("us-east-1")}, nil, false, false},
		{"global major never muteable", []providers.Incident{major()}, map[string]int64{"us-east-1": config.RegionMuteForever}, false, false},
		{"one muted one active", []providers.Incident{major("us-east-1"), major("eu-west-1")}, map[string]int64{"us-east-1": config.RegionMuteForever}, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newRegionTestController(tc.muted, nil)
			res := providers.Result{Feed: providers.FeedReachable, Incidents: tc.incs}
			if got := c.regionAlertSuppressed(res, nil); got != tc.wantSuppress {
				t.Fatalf("regionAlertSuppressed = %v, want %v", got, tc.wantSuppress)
			}
			operational := effectiveSeverity(res, nil, c.muteSnapshot()) < providers.SevMajor
			if operational != tc.wantOperational {
				t.Fatalf("operational = %v, want %v", operational, tc.wantOperational)
			}
			// The whole point: the two must never disagree for a major outage.
			if tc.wantSuppress != operational {
				t.Fatalf("status/alert disagree: suppress=%v operational=%v", tc.wantSuppress, operational)
			}
		})
	}
}

func TestWindowedSamples(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	mk := func(offsets ...int) []history.Sample {
		out := make([]history.Sample, len(offsets))
		for i, o := range offsets {
			out[i] = history.Sample{Time: base.Add(time.Duration(o) * time.Second), Up: true}
		}
		return out
	}
	all := mk(0, 10, 20, 30, 40) // newest at +40s
	newest := base.Add(40 * time.Second)
	// window 25s ending at newest (+40) → cutoff +15 → keep +20,+30,+40
	got := windowedSamples(all, 25*time.Second, newest)
	if len(got) != 3 {
		t.Fatalf("windowed len = %d, want 3 (%v)", len(got), got)
	}
	if got[0].Time != base.Add(20*time.Second) {
		t.Fatalf("first kept = %v, want +20s", got[0].Time.Sub(base))
	}
	// A window wider than the data keeps everything.
	if n := len(windowedSamples(all, time.Hour, newest)); n != 5 {
		t.Fatalf("wide window kept %d, want 5", n)
	}
	// Empty / non-positive window keeps everything.
	if n := len(windowedSamples(all, 0, newest)); n != 5 {
		t.Fatalf("zero window kept %d, want 5", n)
	}
	// An end anchored well past the newest sample (restored stale data with the
	// window anchored to now) keeps only what falls inside [end-window, end].
	if n := len(windowedSamples(all, 25*time.Second, newest.Add(time.Hour))); n != 0 {
		t.Fatalf("stale-data window kept %d, want 0", n)
	}
}

// TestIncidentCellText pins the Active-incidents cell format: each incident
// fenced by an incidentRule — the label, its severity, start/last-update times
// each carrying their age ahead of the colon, and the (capped) latest note;
// incidents with no metadata stay a label plus severity. Consecutive incidents
// share the rule between them, and the last one is closed.
func TestIncidentCellText(t *testing.T) {
	started := time.Date(2026, 7, 17, 10, 0, 0, 0, time.Local)
	updated := time.Date(2026, 7, 17, 12, 30, 0, 0, time.Local)
	now := time.Date(2026, 7, 17, 14, 45, 0, 0, time.Local)
	got := incidentCellText([]providers.Incident{
		{Summary: "Workers AI degraded", Severity: providers.SevMinor,
			Started: started, Updated: updated, Note: "We are continuing to investigate."},
		{Summary: "Bare incident", Severity: providers.SevMinor},
	}, now)
	want := incidentRule + "\n" +
		"Workers AI degraded\n" +
		"Severity: minor\n" +
		"Updated (2h 15m ago): " + updated.Format("2006-01-02 15:04 (UTC-07:00)") + " - We are continuing to investigate.\n" +
		"Started (4h 45m ago): " + started.Format("2006-01-02 15:04 (UTC-07:00)") + "\n" +
		incidentRule + "\n" +
		"Bare incident\n" +
		"Severity: minor\n" +
		incidentRule
	if got != want {
		t.Fatalf("incidentCellText:\n%q\nwant:\n%q", got, want)
	}

	// A very long note is capped with an ellipsis.
	long := strings.Repeat("x", 500)
	got = incidentCellText([]providers.Incident{{Summary: "A", Note: long}}, now)
	if len([]rune(got)) > incidentNoteMax+40+2*len(incidentRule) || !strings.Contains(got, "…") {
		t.Fatalf("long note not capped: %d runes", len([]rune(got)))
	}
}

// TestAgedPrefixPlacesAgeBeforeColon guards the one thing that makes the aged
// field labels readable: the parenthetical belongs INSIDE the label, ahead of
// the colon, in every catalog — including the CJK ones, whose colon is the
// full-width '：' and would otherwise land the age after the separator.
func TestAgedPrefixPlacesAgeBeforeColon(t *testing.T) {
	now := time.Date(2026, 7, 17, 14, 45, 0, 0, time.Local)
	ts := now.Add(-2*time.Hour - 15*time.Minute)
	defer i18n.Set("en")
	for _, lang := range i18n.Languages() {
		i18n.Set(lang.Code)
		label := i18n.T().StartedLabel
		got := agedPrefix(label, ts, now)
		if !strings.Contains(got, "2h 15m") {
			t.Errorf("language %q: agedPrefix(%q) = %q, missing the age", lang.Code, label, got)
		}
		if i := strings.LastIndexFunc(got, isColon); i >= 0 && strings.ContainsRune(got[i:], ')') {
			t.Errorf("language %q: agedPrefix = %q, age landed after the colon", lang.Code, got)
		}
	}
}

// TestBucketPaints pins the bucketed strip semantics: worst observation wins a
// bucket, uncovered time is grey/unknown, and a short outage that shares a
// bucket with many green samples still paints red — the point-sampling renderer
// this replaced would have skipped it entirely.
func TestBucketPaints(t *testing.T) {
	end := time.Unix(1_800_000_000, 0)
	window := 24 * time.Hour
	span := window / sparkBuckets // ~17m9s per bucket

	var samples []history.Sample
	var paints []int
	add := func(age time.Duration, paint int) {
		samples = append(samples, history.Sample{Time: end.Add(-age)})
		paints = append(paints, paint)
	}
	// Bucket 0 (oldest): one degraded observation.
	add(window-span/2, samplePaintDegraded)
	// Last bucket: many OK samples plus ONE down sample — must paint red.
	for i := 0; i < 30; i++ {
		add(time.Duration(i)*30*time.Second, samplePaintOK)
	}
	add(5*time.Minute, samplePaintDown)

	out := bucketPaints(samples, paints, window, end)
	if len(out) != sparkBuckets {
		t.Fatalf("buckets = %d, want %d", len(out), sparkBuckets)
	}
	if out[0] != samplePaintDegraded {
		t.Errorf("oldest bucket = %d, want degraded", out[0])
	}
	if out[sparkBuckets-1] != samplePaintDown {
		t.Errorf("newest bucket = %d, want down (worst-of-bucket must beat 30 OK samples)", out[sparkBuckets-1])
	}
	unknown := 0
	for _, p := range out {
		if p == samplePaintUnknown {
			unknown++
		}
	}
	// Everything between the two covered ends must be grey — unknown ≠ up.
	if unknown != sparkBuckets-2 {
		t.Errorf("unknown buckets = %d, want %d (uncovered time must be grey, never green)", unknown, sparkBuckets-2)
	}

	// A sample outside [end-window, end] contributes nothing.
	out = bucketPaints(
		[]history.Sample{{Time: end.Add(-window - time.Hour)}, {Time: end.Add(time.Hour)}},
		[]int{samplePaintDown, samplePaintDown}, window, end)
	for i, p := range out {
		if p != samplePaintUnknown {
			t.Fatalf("bucket %d = %d, want unknown for out-of-window samples", i, p)
		}
	}
}

func TestOfflineBannerVisible(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	cases := []struct {
		name  string
		raw   bool
		since time.Time
		want  bool
	}{
		{"online", false, time.Time{}, false},
		{"offline but just started", true, now.Add(-2 * time.Second), false},
		{"offline exactly at threshold", true, now.Add(-offlineBannerDelay), true},
		{"offline well past threshold", true, now.Add(-30 * time.Second), true},
		{"raw offline but no anchor", true, time.Time{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := offlineBannerVisible(tc.raw, tc.since, now, offlineBannerDelay); got != tc.want {
				t.Fatalf("offlineBannerVisible = %v, want %v", got, tc.want)
			}
		})
	}
}
