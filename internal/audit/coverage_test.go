package audit

import (
	"testing"
	"time"

	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/history"
)

// TestFeedVerdictThreeWay pins the distinction that did not exist before: the
// audit could say "no captures" but not whether that meant healthy, broken, or
// never looked at. All three now have separate answers, and the difference
// between FeedUnknown and FeedBroken is the whole point — one is an absence of
// evidence, the other is evidence of a failure.
func TestFeedVerdictThreeWay(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		pr   ProviderReport
		want FeedVerdict
	}{
		{
			name: "no coverage record at all",
			pr:   ProviderReport{ID: "a", Enabled: true, HasPublicFeed: true},
			want: FeedUnknown,
		},
		{
			name: "the last informative poll read the feed",
			pr: ProviderReport{ID: "a", Enabled: true, HasPublicFeed: true, HasCoverage: true,
				Coverage: history.CoverageEntry{
					LastAttempt: now, LastSuccess: now, LastInformative: now,
				}},
			want: FeedHealthy,
		},
		{
			// Blind polls after a real read: LastAttempt keeps advancing but
			// nothing new was learned, so the last thing we know is still that
			// success. Honest, and bounded by the freshness rule below.
			name: "blind polls after a recent success",
			pr: ProviderReport{ID: "a", Enabled: true, HasPublicFeed: true, HasCoverage: true,
				Coverage: history.CoverageEntry{
					LastAttempt: now, LastSuccess: now.Add(-time.Hour), LastInformative: now.Add(-time.Hour),
				}},
			want: FeedHealthy,
		},
		{
			// Every attempt ever made was blind or skipped: nothing was learned,
			// so there is nothing to call healthy.
			name: "attempts made but nothing ever learned",
			pr: ProviderReport{ID: "a", Enabled: true, HasPublicFeed: true, HasCoverage: true,
				Coverage: history.CoverageEntry{LastAttempt: now}},
			want: FeedUnknown,
		},
		{
			name: "sustained run of failures",
			pr: ProviderReport{ID: "a", Enabled: true, HasPublicFeed: true, HasCoverage: true,
				Coverage: history.CoverageEntry{LastAttempt: now, LastInformative: now,
					LastFailureClass: history.OutcomeHTTP, ConsecutiveFailures: sustainedFailureAttempts}},
			want: FeedBroken,
		},
		{
			// One bad poll is not a broken feed. Asserting it from a single
			// attempt is the same absence-vs-evidence error this whole change
			// exists to avoid, pointed the other way.
			name: "one failed poll is not conclusive",
			pr: ProviderReport{ID: "a", Enabled: true, HasPublicFeed: true, HasCoverage: true,
				Coverage: history.CoverageEntry{LastAttempt: now, LastInformative: now,
					LastFailureClass: history.OutcomeTransport, ConsecutiveFailures: 1}},
			want: FeedUnknown,
		},
		{
			// THE case the feature exists for: worked for months, then broke.
			// A verdict resting on "has ever succeeded" reports this as healthy
			// forever, which made the whole mechanism inert in production.
			name: "worked for months, now sustained failure",
			pr: ProviderReport{ID: "a", Enabled: true, HasPublicFeed: true, HasCoverage: true,
				Coverage: history.CoverageEntry{
					LastAttempt: now, LastSuccess: now.Add(-72 * time.Hour), LastInformative: now,
					LastFailureClass: history.OutcomeHTTP, ConsecutiveFailures: 500,
				}},
			want: FeedBroken,
		},
		{
			// A coverage entry that exists but records no attempt is not evidence
			// of anything; it must not read as a broken feed.
			name: "coverage present but no attempt recorded",
			pr: ProviderReport{ID: "a", Enabled: true, HasPublicFeed: true, HasCoverage: true,
				Coverage: history.CoverageEntry{}},
			want: FeedUnknown,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.pr.FeedVerdict(now); got != tc.want {
				t.Errorf("FeedVerdict() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestMissingRecoveryKindIsExplicit guards the reporting defect directly: the
// bounded branch must NOT carry a ClearedAt, and must be distinguishable from
// the cleared branch by its Kind rather than by a reader inspecting a zero time.
//
// The old code produced both cases as the same struct, and the printer formatted
// the zero value into the literal "0000-12-31 21:00:00", stating a clearance
// nobody had observed as though it were a measurement.
func TestMissingRecoveryKindIsExplicit(t *testing.T) {
	opened := time.Now().Add(-2 * time.Hour)
	cleared := MissingRecovery{Kind: ClearedByCapture, Start: opened, ClearedAt: time.Now()}
	if cleared.Kind != ClearedByCapture {
		t.Error("ClearedByCapture must be the zero value so existing constructions stay correct")
	}
	if cleared.ClearedAt.IsZero() {
		t.Error("a ClearedByCapture finding must carry the capture time that proves it")
	}
	if !cleared.Start.Equal(opened) {
		t.Error("Start must survive: it is what names WHICH outage the finding is about")
	}

	bounded := MissingRecovery{Kind: BoundedByHorizon, Start: opened}
	if !bounded.Start.Equal(opened) {
		t.Error("a bounded finding still has to say which outage opened, even with no clearance")
	}
	if !bounded.ClearedAt.IsZero() {
		t.Error("a BoundedByHorizon finding must NOT carry a clearance time — none was ever observed")
	}
	if bounded.Kind == ClearedByCapture {
		t.Error("BoundedByHorizon must be distinguishable from ClearedByCapture without reading ClearedAt")
	}

	// The zero value must assert NOTHING. Making the asserting kind the zero
	// value would leave the original fabricated-timestamp defect one careless
	// `MissingRecovery{Start: x}` away from returning, in every future formatter
	// and decode path. This invariant is what removes that class, rather than
	// each consumer defending against it.
	var zero MissingRecovery
	if zero.Kind == ClearedByCapture {
		t.Error("the zero value is the kind that ASSERTS a clearance; a careless construction would fabricate a timestamp")
	}
	if zero.Kind != KindUnset {
		t.Errorf("zero MissingRecovery Kind = %v, want KindUnset", zero.Kind)
	}
	if !zero.ClearedAt.IsZero() {
		t.Error("precondition: a zero MissingRecovery has no clearance time")
	}
}

// TestCoverageVerdictIndependentOfCaptures documents, at the audit layer, that a
// coverage verdict never depends on how many captures exist and vice versa. The
// two are different grades of evidence: captures only exist while diagnostics
// are on.
func TestCoverageVerdictIndependentOfCaptures(t *testing.T) {
	now := time.Now()
	broken := history.CoverageEntry{LastAttempt: now, LastInformative: now,
		LastFailureClass: history.OutcomeTransport, ConsecutiveFailures: sustainedFailureAttempts}

	withCaptures := ProviderReport{
		ID: "a", Enabled: true, HasPublicFeed: true,
		TotalCaptures: 42, HasCoverage: true, Coverage: broken,
	}
	withoutCaptures := ProviderReport{
		ID: "a", Enabled: true, HasPublicFeed: true,
		TotalCaptures: 0, HasCoverage: true, Coverage: broken,
	}
	if withCaptures.FeedVerdict(now) != withoutCaptures.FeedVerdict(now) {
		t.Error("FeedVerdict changed with the capture count; the coverage tier must not read the capture tier")
	}
	if withCaptures.FeedVerdict(now) != FeedBroken {
		t.Errorf("FeedVerdict() = %v, want FeedBroken", withCaptures.FeedVerdict(now))
	}
}

// TestFeedVerdictNeverInventsHealth is the regression guard for the worst class
// this whole change set is about: reporting a reading that never happened.
//
// Blind and skipped attempts advance neither the failure count nor LastSuccess,
// so a record can legitimately hold "attempted, zero failures, never succeeded".
// A verdict keyed on ConsecutiveFailures == 0 alone calls that HEALTHY and the
// report then prints "the feed was read" about a feed nobody has ever read.
func TestFeedVerdictNeverInventsHealth(t *testing.T) {
	now := time.Now()
	neverRead := ProviderReport{ID: "a", Enabled: true, HasPublicFeed: true, HasCoverage: true,
		Coverage: history.CoverageEntry{LastAttempt: now}} // no failures, no success

	if got := neverRead.FeedVerdict(now); got == FeedHealthy {
		t.Error("a feed that was never once read was reported HEALTHY")
	} else if got != FeedUnknown {
		t.Errorf("FeedVerdict() = %v, want FeedUnknown", got)
	}
}

// TestFeedVerdictGoesUnknownWhenStale: a verdict nobody has re-observed in
// months must not keep failing the run. Silence is not evidence — including
// silence in time.
func TestFeedVerdictGoesUnknownWhenStale(t *testing.T) {
	now := time.Now()
	long := now.Add(-90 * 24 * time.Hour)
	stale := ProviderReport{ID: "a", Enabled: true, HasPublicFeed: true, HasCoverage: true,
		Coverage: history.CoverageEntry{
			LastAttempt: long, LastInformative: long, LastFailureClass: history.OutcomeHTTP,
			ConsecutiveFailures: 500,
		}}

	if got := stale.FeedVerdict(now); got != FeedUnknown {
		t.Errorf("FeedVerdict() = %v on a 90-day-old record, want FeedUnknown", got)
	}
	if stale.FeedFault(now) {
		t.Error("a three-month-old coverage record still failed the run")
	}

	// Staleness is measured from the last INFORMATIVE observation, not from the
	// last attempt: a machine offline for months keeps attempting every round, so
	// an attempt-based rule would call this verdict current forever.
	stillAttempting := stale
	stillAttempting.Coverage.LastAttempt = now
	if stillAttempting.FeedFault(now) {
		t.Error("blind attempts kept a months-old verdict alive; freshness must key on what was LEARNED")
	}

	// The same record, freshly learned, IS a fault.
	fresh := stale
	fresh.Coverage.LastAttempt = now
	fresh.Coverage.LastInformative = now
	if !fresh.FeedFault(now) {
		t.Error("a current sustained failure must still fail the run")
	}
}
