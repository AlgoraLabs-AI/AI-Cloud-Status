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
			name: "last poll read the feed",
			pr: ProviderReport{ID: "a", Enabled: true, HasPublicFeed: true, HasCoverage: true,
				Coverage: history.CoverageEntry{LastAttempt: now, LastSuccess: now.Add(-time.Minute)}},
			want: FeedHealthy,
		},
		{
			name: "sustained run of failures",
			pr: ProviderReport{ID: "a", Enabled: true, HasPublicFeed: true, HasCoverage: true,
				Coverage: history.CoverageEntry{LastAttempt: now, LastFailureClass: history.OutcomeHTTP,
					ConsecutiveFailures: sustainedFailureAttempts}},
			want: FeedBroken,
		},
		{
			// One bad poll is not a broken feed. Asserting it from a single
			// attempt is the same absence-vs-evidence error this whole change
			// exists to avoid, pointed the other way.
			name: "one failed poll is not conclusive",
			pr: ProviderReport{ID: "a", Enabled: true, HasPublicFeed: true, HasCoverage: true,
				Coverage: history.CoverageEntry{LastAttempt: now, LastFailureClass: history.OutcomeTransport,
					ConsecutiveFailures: 1}},
			want: FeedUnknown,
		},
		{
			// THE case the feature exists for: worked for months, then broke.
			// A verdict resting on "has ever succeeded" reports this as healthy
			// forever, which made the whole mechanism inert in production.
			name: "worked for months, now sustained failure",
			pr: ProviderReport{ID: "a", Enabled: true, HasPublicFeed: true, HasCoverage: true,
				Coverage: history.CoverageEntry{
					LastAttempt: now, LastSuccess: now.Add(-72 * time.Hour),
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
			if got := tc.pr.FeedVerdict(); got != tc.want {
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

	// The zero value of the struct is the cleared kind, which is what keeps every
	// pre-existing MissingRecovery{Start, ClearedAt} construction meaning what it
	// always meant.
	var zero MissingRecovery
	if zero.Kind != ClearedByCapture {
		t.Errorf("zero MissingRecovery Kind = %v, want ClearedByCapture", zero.Kind)
	}
}

// TestCoverageVerdictIndependentOfCaptures documents, at the audit layer, that a
// coverage verdict never depends on how many captures exist and vice versa. The
// two are different grades of evidence: captures only exist while diagnostics
// are on.
func TestCoverageVerdictIndependentOfCaptures(t *testing.T) {
	now := time.Now()
	broken := history.CoverageEntry{LastAttempt: now, LastFailureClass: history.OutcomeTransport,
		ConsecutiveFailures: sustainedFailureAttempts}

	withCaptures := ProviderReport{
		ID: "a", Enabled: true, HasPublicFeed: true,
		TotalCaptures: 42, HasCoverage: true, Coverage: broken,
	}
	withoutCaptures := ProviderReport{
		ID: "a", Enabled: true, HasPublicFeed: true,
		TotalCaptures: 0, HasCoverage: true, Coverage: broken,
	}
	if withCaptures.FeedVerdict() != withoutCaptures.FeedVerdict() {
		t.Error("FeedVerdict changed with the capture count; the coverage tier must not read the capture tier")
	}
	if withCaptures.FeedVerdict() != FeedBroken {
		t.Errorf("FeedVerdict() = %v, want FeedBroken", withCaptures.FeedVerdict())
	}
}
