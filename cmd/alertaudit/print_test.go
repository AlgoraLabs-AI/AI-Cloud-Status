package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/audit"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/history"
)

// The defect this change set exists to fix lived in the FORMATTER, not in the
// data model: two different findings shared one struct and the printer rendered
// a zero time as a real one. Asserting only on the model would let the exact
// same regression return with every model-level test still green, so these run
// printReport for real and read its bytes.

// TestPrintReportNeverFabricatesAClearanceTime is the direct regression guard.
func TestPrintReportNeverFabricatesAClearanceTime(t *testing.T) {
	opened := time.Date(2026, 9, 4, 9, 53, 4, 0, time.Local)
	r := audit.Report{
		GeneratedAt:    time.Now(),
		HistoryPresent: true,
		Providers: []audit.ProviderReport{{
			ID: "mistral", Name: "Mistral", Enabled: true, HasPublicFeed: true, TotalCaptures: 3,
			MissingRecoveries: []audit.MissingRecovery{{Kind: audit.BoundedByHorizon, Start: opened}},
		}},
	}

	var buf bytes.Buffer
	printReport(&buf, r)
	out := buf.String()

	if strings.Contains(out, "0000-") {
		t.Errorf("printReport rendered a zero time as a real one:\n%s", out)
	}
	if strings.Contains(out, "cleared by") {
		t.Error("the bounded case must not claim the feed showed it cleared; nothing was ever observed clearing")
	}
	if !strings.Contains(out, "no corroborating evidence") {
		t.Errorf("the bounded case must say what it actually knows:\n%s", out)
	}
}

// TestPrintReportClearedCaseStillNamesTheProof: the other Kind DOES have a
// capture proving it resolved, and must still print it. Suppressing both would
// be a different kind of dishonesty.
func TestPrintReportClearedCaseStillNamesTheProof(t *testing.T) {
	opened := time.Date(2026, 9, 4, 9, 53, 4, 0, time.Local)
	cleared := opened.Add(2 * time.Hour)
	r := audit.Report{
		GeneratedAt:    time.Now(),
		HistoryPresent: true,
		Providers: []audit.ProviderReport{{
			ID: "mistral", Enabled: true, HasPublicFeed: true, TotalCaptures: 3,
			MissingRecoveries: []audit.MissingRecovery{
				{Kind: audit.ClearedByCapture, Start: opened, ClearedAt: cleared},
			},
		}},
	}

	var buf bytes.Buffer
	printReport(&buf, r)
	out := buf.String()

	if !strings.Contains(out, "cleared by") {
		t.Errorf("the cleared case must name the capture that proves it:\n%s", out)
	}
	if strings.Contains(out, "0000-") {
		t.Errorf("zero time rendered:\n%s", out)
	}
}

// TestPrintReportShowsCoverageFaultEvenWithCaptures pins the agreement between
// the exit code and the report. hasIssues fails the run on a coverage fault; if
// the printer only surfaced it via the zero-captures note, a provider WITH
// captures would exit 1 beside a report saying nothing was found — the exact
// contradiction hasIssues' own doc says trains operators to ignore exit 1.
func TestPrintReportShowsCoverageFaultEvenWithCaptures(t *testing.T) {
	now := time.Now()
	r := audit.Report{
		GeneratedAt:    now,
		HistoryPresent: true,
		Providers: []audit.ProviderReport{{
			ID: "anthropic", Enabled: true, HasPublicFeed: true,
			TotalCaptures: 5, // captures exist, so the NeverCaptured note never fires
			HasCoverage:   true,
			Coverage: history.CoverageEntry{
				LastAttempt: now, LastSuccess: now.Add(-72 * time.Hour), LastInformative: now,
				LastFailureClass: history.OutcomeHTTP, ConsecutiveFailures: 500,
			},
		}},
	}

	if !hasIssues(r) {
		t.Fatal("precondition: this report must fail the run")
	}

	var buf bytes.Buffer
	printReport(&buf, r)
	out := buf.String()

	if !strings.Contains(out, "FEED NOT READABLE") {
		t.Errorf("exit 1 with no coverage section printed:\n%s", out)
	}
	if strings.Contains(out, "No uncovered major incidents") {
		t.Errorf("report printed an all-clear summary while failing the run:\n%s", out)
	}
}

// TestPrintReportSurvivesAZeroKindFinding closes the hole the Kind
// discriminator leaves open on its own.
//
// The zero Kind is KindUnset and asserts nothing, so a finding built without
// naming a Kind falls to the bounded pass — that is the primary defence, and it
// is what this asserts. The formatter's ClearedAt check is the second one, for a
// finding that CLAIMS ClearedByCapture without carrying a time; either way,
// nothing may print a fabricated clearance.
func TestPrintReportSurvivesAZeroKindFinding(t *testing.T) {
	r := audit.Report{
		GeneratedAt:    time.Now(),
		HistoryPresent: true,
		Providers: []audit.ProviderReport{{
			ID: "p1", Enabled: true, HasPublicFeed: true, TotalCaptures: 1,
			// Kind deliberately omitted: this is what a careless caller writes.
			MissingRecoveries: []audit.MissingRecovery{{Start: time.Date(2026, 9, 4, 9, 53, 4, 0, time.Local)}},
		}},
	}

	var buf bytes.Buffer
	printReport(&buf, r)
	if out := buf.String(); strings.Contains(out, "0000-") {
		t.Errorf("a zero-Kind finding printed a fabricated clearance time:\n%s", out)
	}
}

// TestPrintReportKeepsTheTwoKindsApart: a provider carrying BOTH kinds must not
// get its proven-cleared rows filed under the "may still be open" header. A
// single loop with lazily-emitted headers does exactly that once both headers
// have been printed, which is the same misattribution inverted.
func TestPrintReportKeepsTheTwoKindsApart(t *testing.T) {
	t0 := time.Date(2026, 9, 7, 10, 0, 0, 0, time.Local)
	r := audit.Report{
		GeneratedAt:    time.Now(),
		HistoryPresent: true,
		Providers: []audit.ProviderReport{{
			ID: "p1", Enabled: true, HasPublicFeed: true, TotalCaptures: 3,
			MissingRecoveries: []audit.MissingRecovery{
				{Kind: audit.ClearedByCapture, Start: t0, ClearedAt: t0.Add(time.Hour)},
				{Kind: audit.BoundedByHorizon, Start: t0.Add(2 * time.Hour)},
				{Kind: audit.ClearedByCapture, Start: t0.Add(3 * time.Hour), ClearedAt: t0.Add(4 * time.Hour)},
			},
		}},
	}

	var buf bytes.Buffer
	printReport(&buf, r)
	out := buf.String()

	boundedHdr := strings.Index(out, "OUTAGE WINDOW LEFT OPEN —") // position, not presence
	if !strings.Contains(out, "MISSING RECOVERY —") || boundedHdr < 0 {
		t.Fatalf("both headers must appear:\n%s", out)
	}
	// Every "cleared by" row must sit ABOVE the left-open header. Scanning from a
	// moving offset rather than re-searching from 0, so two identical rows are
	// not both resolved to the first one's position.
	for off := 0; ; {
		i := strings.Index(out[off:], "feed showed it cleared by")
		if i < 0 {
			break
		}
		at := off + i
		if at > boundedHdr {
			t.Errorf("a proven-cleared row was printed under the LEFT-OPEN header:\n%s", out)
		}
		off = at + 1
	}
}

// TestPrintReportNeverAgesAZeroSuccess closes the loop on the defect this whole
// change set began with, in the branch that reintroduced it.
//
// A coverage record can legitimately hold "attempted, no failures outstanding,
// never succeeded". Formatting its zero LastSuccess as an age prints roughly
// 739,000 days — a fabricated timestamp rendered as a measurement, which is
// exactly what "cleared by 0000-12-31" was.
//
// The verdict does not produce this state — FeedHealthy requires a success equal
// to a non-zero LastInformative — so through FeedVerdict this record resolves
// to the inconclusive advisory, and THAT is what this test asserts: a record
// with no success on file must never read as healthy, and must never print an
// age. The formatter's own zero guard is defence in depth that this test cannot
// reach through the public path; it is not what passes here.
func TestPrintReportNeverAgesAZeroSuccess(t *testing.T) {
	now := time.Now()
	r := audit.Report{
		GeneratedAt: now, HistoryPresent: true, HistoryFound: true,
		Providers: []audit.ProviderReport{{
			ID: "a", Enabled: true, HasPublicFeed: true, TotalCaptures: 0, HasCoverage: true,
			Coverage: history.CoverageEntry{LastAttempt: now, LastInformative: now},
		}},
	}

	var buf bytes.Buffer
	printReport(&buf, r)
	out := buf.String()

	for _, absurd := range []string{"739", "0000-", "y ago"} {
		if strings.Contains(out, absurd) {
			t.Errorf("a never-succeeded record rendered %q as an age:\n%s", absurd, out)
		}
	}
	if strings.Contains(out, "DID read the feed") {
		t.Errorf("a record with no success on file rendered as healthy:\n%s", out)
	}
	if !strings.Contains(out, "nothing conclusive in the coverage record") {
		t.Errorf("a record with no success on file must render as inconclusive:\n%s", out)
	}
}

// TestPrintReportEvidenceTierStates: present, lost, and absent must read
// differently. A corrupt history reported as "absent" hides an evidence LOSS as
// an evidence gap, and the two call for different responses from the operator.
func TestPrintReportEvidenceTierStates(t *testing.T) {
	base := audit.Report{GeneratedAt: time.Now()}

	withCaptures := audit.Report{
		GeneratedAt: base.GeneratedAt, HistoryPresent: true, HistoryFound: true,
		AlertLogStart: base.GeneratedAt.Add(-24 * time.Hour),
		Providers:     []audit.ProviderReport{{ID: "a", TotalCaptures: 3}},
	}

	cases := []struct {
		name string
		r    audit.Report
		want string
	}{
		{"both tiers really present", withCaptures, "CAPTURE tier present"},
		{"both tiers really present, coverage half", withCaptures, "COVERAGE tier present"},
		{
			// The default install: diagnostics off, so the capture tier is empty.
			// Announcing it present here asserts a tier the line never looked at,
			// and an empty capture corpus reads identically to a clean one.
			"capture tier empty",
			audit.Report{GeneratedAt: base.GeneratedAt, HistoryPresent: true, HistoryFound: true},
			"CAPTURE tier EMPTY",
		},
		{
			// WHY it is empty comes from config.json, not from the emptiness. The
			// two causes need different responses: nothing was ever recorded, or
			// recording was on and the window really was quiet.
			"empty because diagnostics were never on",
			audit.Report{GeneratedAt: base.GeneratedAt, HistoryPresent: true, HistoryFound: true},
			"debug_logging is false in config.json",
		},
		{
			"empty although diagnostics are on",
			audit.Report{GeneratedAt: base.GeneratedAt, HistoryPresent: true, HistoryFound: true, DiagnosticsEnabled: true},
			"genuinely empty rather than disabled",
		},
		{"corrupt", audit.Report{GeneratedAt: base.GeneratedAt, HistoryFound: true, HistoryErr: "unexpected end of JSON input"}, "could NOT be read"},
		{"absent", base, "is absent"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			printReport(&buf, tc.r)
			if !strings.Contains(buf.String(), tc.want) {
				t.Errorf("evidence-tier line missing %q:\n%s", tc.want, buf.String())
			}
		})
	}
}
