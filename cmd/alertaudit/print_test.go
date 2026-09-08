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
// ClearedByCapture is the ZERO value of MissingRecoveryKind, so any future
// construction that omits Kind — or any decode/copy path that zeroes it — lands
// on the branch that prints ClearedAt, with ClearedAt zero. That is the original
// fabricated-timestamp defect, verbatim, reachable without anyone writing a bug.
// The formatter must not depend on every author remembering the doc.
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

// TestPrintReportEvidenceTierStates: present, lost, and absent must read
// differently. A corrupt history reported as "absent" hides an evidence LOSS as
// an evidence gap, and the two call for different responses from the operator.
func TestPrintReportEvidenceTierStates(t *testing.T) {
	base := audit.Report{GeneratedAt: time.Now()}

	cases := []struct {
		name string
		r    audit.Report
		want string
	}{
		{"usable", audit.Report{GeneratedAt: base.GeneratedAt, HistoryPresent: true, HistoryFound: true}, "Both present"},
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
