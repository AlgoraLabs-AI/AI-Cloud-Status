package ui

import (
	"errors"
	"testing"
	"time"

	"fyne.io/fyne/v2/container"

	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/history"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/providers"
)

// TestCarryLastOKSurvivesAFailedPoll pins the memory a failed poll must not
// erase: when the feed WAS last readable. `when` advances on every attempt,
// failed ones included, so it cannot answer that — and without an answer the
// history panel would date every incident against a poll that saw nothing.
func TestCarryLastOKSurvivesAFailedPoll(t *testing.T) {
	good := time.Date(2026, 8, 14, 0, 0, 0, 0, time.Local)
	bad := good.Add(time.Minute)

	ok := carryLastOK(provState{}, provState{checked: true, when: good, res: providers.Result{}})
	if !ok.lastOK.Equal(good) {
		t.Fatalf("readable feed stamped lastOK = %v, want %v", ok.lastOK, good)
	}

	failed := carryLastOK(ok, provState{checked: true, when: bad, err: errors.New("tls: failed to verify certificate")})
	if !failed.lastOK.Equal(good) {
		t.Errorf("failed poll left lastOK = %v, want the previous readable poll %v", failed.lastOK, good)
	}
	if !failed.when.Equal(bad) {
		t.Errorf("when = %v, want the attempt time %v — the two are different questions", failed.when, bad)
	}

	// A provider with no public feed is not an observation either.
	skipped := carryLastOK(ok, provState{checked: true, skipped: true, when: bad})
	if !skipped.lastOK.Equal(good) {
		t.Errorf("skipped poll left lastOK = %v, want %v", skipped.lastOK, good)
	}
}

// TestPastIncidentsSurviveAnUnreadableFeed is the regression for the panel that
// went blank: a certificate rejection on the CURRENT poll is not evidence that
// nothing happened in the last 24h. The journal is local and complete up to the
// last readable poll, so it must still render.
func TestPastIncidentsSurviveAnUnreadableFeed(t *testing.T) {
	// Relative to now — see the note in TestHistoryHeadingSaysAlsoWhenSomethingIsAbove:
	// the 24h journal window is measured from the wall clock, so an absolute
	// fixture date makes this test rot with the calendar.
	seen := time.Now().Add(-2 * time.Hour)
	c := newRowTestController()
	c.incidents = history.NewIncidentLog()
	c.incidents.Observe("anthropic", []history.IncidentEntry{
		{Summary: "Service disruption on Claude Code", Severity: int(providers.SevMinor), Started: seen},
	}, seen)

	body := container.NewVBox()
	c.addRecentIncidents(body, "anthropic", nil, seen.Add(30*time.Minute))
	if len(body.Objects) == 0 {
		t.Fatal("an unreadable feed dropped the last-24h history; the journal still has the incident")
	}

	// A provider with nothing journaled adds nothing — no empty heading.
	empty := container.NewVBox()
	c.addRecentIncidents(empty, "openai", nil, seen)
	if len(empty.Objects) != 0 {
		t.Errorf("empty journal rendered %d objects, want none", len(empty.Objects))
	}
}
