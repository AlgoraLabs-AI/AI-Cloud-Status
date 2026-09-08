package ui

import (
	"context"
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
		name    string
		st      provState
		offline bool
		want    history.CoverageObservation
	}{
		{
			name: "successful read",
			st:   provState{when: at, outcome: history.OutcomeReadable},
			want: history.CoverageObservation{At: at, OK: true, FailureClass: history.OutcomeReadable},
		},
		{
			name: "http failure while online is real evidence",
			st:   provState{when: at, outcome: history.OutcomeHTTP, err: errors.New("unexpected status 405")},
			want: history.CoverageObservation{At: at, FailureClass: history.OutcomeHTTP},
		},
		{
			// The one that matters: the machine's own link is down, so this says
			// nothing about the provider.
			name:    "transport failure while OFFLINE is blind",
			st:      provState{when: at, outcome: history.OutcomeTransport, err: errors.New("no such host")},
			offline: true,
			want:    history.CoverageObservation{At: at, Blind: true, FailureClass: history.OutcomeTransport},
		},
		{
			// THE case the whole record exists for. The offline latch trips when
			// EVERY enabled provider fails at once, which is exactly what a
			// fleet-wide feed outage looks like. If the latch alone decided
			// blindness, that outage would be recorded as "we could not see" from
			// round two onward and the verdict could never fire. An HTTP status
			// proves the server answered, so it is evidence regardless.
			name:    "HTTP failure is NEVER blind, even with the offline latch set",
			st:      provState{when: at, outcome: history.OutcomeHTTP, err: errors.New("unexpected status 405")},
			offline: true,
			want:    history.CoverageObservation{At: at, FailureClass: history.OutcomeHTTP},
		},
		{
			// Same reasoning: bytes arrived, they just did not parse.
			name:    "parse failure is never blind either",
			st:      provState{when: at, outcome: history.OutcomeParse, err: errors.New("bad json")},
			offline: true,
			want:    history.CoverageObservation{At: at, FailureClass: history.OutcomeParse},
		},
		{
			// Shutdown cancelled the request; the feed never answered.
			name: "cancelled at shutdown is blind even when online",
			st:   provState{when: at, outcome: history.OutcomeTransport, err: context.Canceled},
			want: history.CoverageObservation{At: at, Blind: true, FailureClass: history.OutcomeTransport},
		},
		{
			// An Optional provider with no public feed, working as designed.
			name: "skipped is neither success nor failure",
			st:   provState{when: at, outcome: history.OutcomeSkipped, skipped: true},
			want: history.CoverageObservation{At: at, Skipped: true, FailureClass: history.OutcomeSkipped},
		},
		{
			// offline must not turn a SUCCESS into a blind attempt.
			name:    "a successful read while the latch says offline is still a success",
			st:      provState{when: at, outcome: history.OutcomeReadable},
			offline: true,
			want:    history.CoverageObservation{At: at, OK: true, FailureClass: history.OutcomeReadable},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := coverageObservation(tc.st, tc.offline, 0)
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
		provState{when: t0, outcome: history.OutcomeReadable}, false, time.Hour))
	for i := range 20 {
		st := provState{
			when:    t0.Add(time.Duration(i+1) * time.Minute),
			outcome: history.OutcomeTransport,
			err:     errors.New("no such host"),
		}
		h.RecordCoverage("p", coverageObservation(st, true, time.Hour))
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
