package ui

import (
	"errors"
	"testing"
	"time"

	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/history"
)

// The observation builder is the ONLY thing that decides what the attendance
// record is told about a poll, and history.RecordCoverage's handling of Blind
// and Skipped is well tested on the other side of that boundary. This is the
// side that was untested: invert the Blind condition or drop Skipped and every
// package still compiles and passes, while the next wifi drop gives all fifteen
// providers a sustained failure run — which now sets an exit code.
func TestCoverageObservation(t *testing.T) {
	at := time.Now()

	cases := []struct {
		name      string
		st        provState
		noLink    bool
		cancelled bool
		want      history.CoverageObservation
	}{
		{
			// FailureClass is empty because nothing failed: RecordCoverage's doc
			// says the field is "a failure class, or empty on success".
			name: "successful read",
			st:   provState{when: at, outcome: history.OutcomeReadable},
			want: history.CoverageObservation{At: at, OK: true},
		},
		{
			name: "http failure with a link is real evidence",
			st:   provState{when: at, outcome: history.OutcomeHTTP, err: errors.New("unexpected status 405")},
			want: history.CoverageObservation{At: at, FailureClass: history.OutcomeHTTP},
		},
		{
			// The machine's own link is down, so this says nothing about the
			// provider.
			name:   "transport failure with NO LINK is blind",
			st:     provState{when: at, outcome: history.OutcomeTransport, err: errors.New("no such host")},
			noLink: true,
			want:   history.CoverageObservation{At: at, Blind: true, FailureClass: history.OutcomeTransport},
		},
		{
			// THE case the record exists for, and the reason blindness must key
			// on the connectivity PROBE rather than on the offline latch: the
			// latch folds in "every provider failed", so a fleet-wide feed outage
			// would excuse itself. Here the probe says the link is fine.
			name: "transport failure WITH a link is evidence, not blindness",
			st:   provState{when: at, outcome: history.OutcomeTransport, err: errors.New("no such host")},
			want: history.CoverageObservation{At: at, FailureClass: history.OutcomeTransport},
		},
		{
			// No exemption for "it answered". A captive portal or intercepting
			// proxy replies 200 with a login page, which parses as garbage — so
			// "the far end answered" is not proof the PROVIDER answered, and
			// exempting it would file a portal as evidence against all fifteen
			// feeds at once. The probe already covers the case the exemption was
			// invented for: with a real link, this is evidence (the case above).
			name:   "HTTP failure with no link is blind — an answer may not be the provider's",
			st:     provState{when: at, outcome: history.OutcomeHTTP, err: errors.New("unexpected status 405")},
			noLink: true,
			want:   history.CoverageObservation{At: at, Blind: true, FailureClass: history.OutcomeHTTP},
		},
		{
			name:   "parse failure with no link is blind — this is what a portal looks like",
			st:     provState{when: at, outcome: history.OutcomeParse, err: errors.New("bad json")},
			noLink: true,
			want:   history.CoverageObservation{At: at, Blind: true, FailureClass: history.OutcomeParse},
		},
		{
			// Cancellation comes from the CALLER's ctx, not from st.err:
			// monitor.Retry returns the last attempt's error when cancellation
			// lands during a backoff, so st.err is silent about it exactly then.
			name:      "cancelled at shutdown is blind even with a link, and even with a non-context error",
			st:        provState{when: at, outcome: history.OutcomeTransport, err: errors.New("no such host")},
			cancelled: true,
			want:      history.CoverageObservation{At: at, Blind: true, FailureClass: history.OutcomeTransport},
		},
		{
			name: "skipped is neither success nor failure, and carries no failure class",
			st:   provState{when: at, outcome: history.OutcomeSkipped, skipped: true},
			want: history.CoverageObservation{At: at, Skipped: true},
		},
		{
			// A dead link must not turn a SUCCESS into a blind attempt.
			name:   "a successful read while the probe says no link is still a success",
			st:     provState{when: at, outcome: history.OutcomeReadable},
			noLink: true,
			want:   history.CoverageObservation{At: at, OK: true},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := coverageObservation(tc.st, tc.noLink, tc.cancelled, 0)
			if got != tc.want {
				t.Errorf("coverageObservation() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestCoverageObservationFeedsRecordCoverageCorrectly closes the loop across the
// package boundary: the builder's output, fed to the real recorder, must produce
// the attendance the scenario deserves.
func TestCoverageObservationFeedsRecordCoverageCorrectly(t *testing.T) {
	h := history.New(100)
	t0 := time.Now().Add(-time.Hour)

	// One real read, then the link dies and stays dead for twenty rounds.
	h.RecordCoverage("p", coverageObservation(
		provState{when: t0, outcome: history.OutcomeReadable}, false, false, time.Hour))
	for i := range 20 {
		st := provState{
			when:    t0.Add(time.Duration(i+1) * time.Minute),
			outcome: history.OutcomeTransport,
			err:     errors.New("no such host"),
		}
		h.RecordCoverage("p", coverageObservation(st, true, false, time.Hour))
	}

	cov, ok := h.Coverage("p")
	if !ok {
		t.Fatal("no coverage recorded")
	}
	if cov.ConsecutiveFailures != 0 {
		t.Errorf("ConsecutiveFailures = %d after 20 offline rounds, want 0 — the router is not the provider's fault",
			cov.ConsecutiveFailures)
	}
	if !cov.LastSuccess.Equal(t0) {
		t.Errorf("LastSuccess = %v, want the real read at %v", cov.LastSuccess, t0)
	}
}
