package main

import (
	"testing"
	"time"

	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/audit"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/history"
)

// TestHasIssuesCoverageTier covers the promotion this tool's own source asked
// for and could not justify until the coverage record existed.
//
// The distinction that matters: zero captures is an ABSENCE and must stay
// advisory (exit 0), while "polled and never once read" is positive EVIDENCE and
// must fail the run (exit 1). Collapsing the two is what the original comment
// warned trains an operator to ignore exit 1.
func TestHasIssuesCoverageTier(t *testing.T) {
	now := time.Now()

	brokenFeed := audit.Report{HistoryPresent: true, Providers: []audit.ProviderReport{
		{ID: "a", Enabled: true, HasPublicFeed: true, TotalCaptures: 0, HasCoverage: true,
			Coverage: history.CoverageEntry{LastAttempt: now, LastFailureClass: history.OutcomeHTTP,
				ConsecutiveFailures: 25}},
	}}
	if !hasIssues(brokenFeed) {
		t.Error("hasIssues(feed polled and never read) = false; that is positive evidence of a broken feed, not an absence")
	}

	healthyNoCaptures := audit.Report{HistoryPresent: true, Providers: []audit.ProviderReport{
		{ID: "a", Enabled: true, HasPublicFeed: true, TotalCaptures: 0, HasCoverage: true,
			Coverage: history.CoverageEntry{LastAttempt: now, LastSuccess: now}},
	}}
	if hasIssues(healthyNoCaptures) {
		t.Error("hasIssues(no captures but feed read fine) = true; a healthy provider produces no captures by design")
	}

	// The pre-existing contract, unchanged: no coverage record at all stays
	// advisory. This is the assertion main_test.go already pins, restated here
	// against the new code path so a future edit cannot quietly reverse it.
	noEvidence := audit.Report{Providers: []audit.ProviderReport{
		{ID: "a", Enabled: true, HasPublicFeed: true, TotalCaptures: 0},
	}}
	if hasIssues(noEvidence) {
		t.Error("hasIssues(no captures, no coverage) = true; nothing was observed, so nothing can be asserted")
	}
}

// TestHasIssuesDoesNotMixTiers is the regression guard for the failure mode the
// architecture review named: on a DEFAULT install diagnostics are off, so
// alert-log.jsonl and feed-samples/ are absent and every capture-tier verdict is
// vacuous. If the coverage tier were folded into the capture tier, an empty
// capture corpus would read as "all clear" next to a feed that had not been
// readable in weeks — and the converse, a capture-tier fault, must still fail
// even when no coverage was ever recorded.
func TestHasIssuesDoesNotMixTiers(t *testing.T) {
	now := time.Now()

	// Capture tier empty, coverage tier says broken -> must still fail.
	onlyCoverageEvidence := audit.Report{HistoryPresent: true, Providers: []audit.ProviderReport{
		{ID: "a", Enabled: true, HasPublicFeed: true, TotalCaptures: 0, HasCoverage: true,
			Coverage: history.CoverageEntry{LastAttempt: now, LastFailureClass: history.OutcomeTransport,
				ConsecutiveFailures: 25}},
	}}
	if !hasIssues(onlyCoverageEvidence) {
		t.Error("a coverage-tier fault was suppressed because the capture tier was empty")
	}

	// Coverage tier absent, capture tier says unparseable -> must still fail.
	onlyCaptureEvidence := audit.Report{Providers: []audit.ProviderReport{
		{ID: "a", Enabled: true, HasPublicFeed: true, TotalCaptures: 3, ParseErrors: 3},
	}}
	if !hasIssues(onlyCaptureEvidence) {
		t.Error("a capture-tier fault was suppressed because no coverage record existed")
	}
}

// TestHasIssuesIgnoresOptionalProviderCoverage: an Optional provider that
// publishes no public feed is recorded as attempted-with-no-success by design
// (ErrNoFeed is a skip, not a failure). It must never be reported as broken.
func TestHasIssuesIgnoresOptionalProviderCoverage(t *testing.T) {
	now := time.Now()
	optional := audit.Report{HistoryPresent: true, Providers: []audit.ProviderReport{
		{ID: "groq", Enabled: true, HasPublicFeed: false, TotalCaptures: 0, HasCoverage: true,
			Coverage: history.CoverageEntry{LastAttempt: now}},
	}}
	if hasIssues(optional) {
		t.Error("an Optional provider with no public feed was reported as a broken feed; skipping is working as designed")
	}
}
