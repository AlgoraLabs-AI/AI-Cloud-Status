package ui

import (
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/history"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/i18n"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/providers"
)

// labelTexts collects the text of every Label in a widget tree, so a test can
// assert what the panel actually SAYS without a running driver.
func labelTexts(o fyne.CanvasObject) []string {
	switch v := o.(type) {
	case *widget.Label:
		return []string{v.Text}
	case *fyne.Container:
		var out []string
		for _, child := range v.Objects {
			out = append(out, labelTexts(child)...)
		}
		return out
	default:
		return nil
	}
}

func containsText(objs []string, want string) bool {
	for _, s := range objs {
		if s == want {
			return true
		}
	}
	return false
}

// TestIncidentVerdictIsAlwaysStated walks the four readable-feed combinations.
// Three of them used to print no verdict at all: the panel jumped from the mute
// checkbox straight into "Stale / zombie alerts (1)", leaving "does anything
// need me?" unanswered.
func TestIncidentVerdictIsAlwaysStated(t *testing.T) {
	defer i18n.Set("en")
	i18n.Set("en")
	tr := i18n.T()
	cases := []struct {
		name               string
		live, stale, muted int
		want               string
	}{
		{"clean", 0, 0, 0, tr.NoActiveIncidents},
		{"live", 2, 0, 0, "Active incidents (2):"},
		{"stale only", 0, 1, 0, tr.NoActionableIncidents},
		{"muted only", 0, 0, 3, tr.NoActionableIncidents},
		{"stale and muted", 0, 1, 2, tr.NoActionableIncidents},
	}
	for _, tc := range cases {
		if got := incidentVerdict(tc.live, tc.stale, tc.muted); got != tc.want {
			t.Errorf("%s: verdict = %q, want %q", tc.name, got, tc.want)
		}
	}
	// The qualified verdict must not claim "operational" — something is still on
	// the feed, and the dialog headline above already carries the state word.
	if strings.Contains(strings.ToLower(tr.NoActionableIncidents), "operational") {
		t.Errorf("NoActionableIncidents claims operational: %q", tr.NoActionableIncidents)
	}
}

// TestHistoryDropsWhatIsAlreadyOnScreen pins the dedup: the journal keeps
// currently-open incidents too, and the same incident as a tall card above AND
// a history line below reads as two incidents.
func TestHistoryDropsWhatIsAlreadyOnScreen(t *testing.T) {
	recent := []history.IncidentEntry{
		{Summary: "Embedding API Degraded", URL: "https://status.example.com/incidents/abc"},
		{Summary: "Elevated errors"}, // no URL: identity falls back to the summary
		{Summary: "Resolved earlier today", URL: "https://status.example.com/incidents/old"},
	}
	shown := []providers.Incident{
		{Summary: "Embedding API Degraded", URL: "https://status.example.com/incidents/abc"},
		{Summary: "Elevated errors"},
	}
	got := withoutShown(recent, shown)
	if len(got) != 1 || got[0].Summary != "Resolved earlier today" {
		t.Fatalf("withoutShown kept %d entries (%v), want only the one not drawn above", len(got), got)
	}

	// A summary edited on the provider's side keeps its URL identity.
	renamed := []providers.Incident{{Summary: "Embedding API Degraded (updated title)", URL: "https://status.example.com/incidents/abc"}}
	if got := withoutShown(recent[:1], renamed); len(got) != 0 {
		t.Errorf("URL identity did not survive a retitled incident: %v", got)
	}

	// Nothing drawn above: the history is untouched.
	if got := withoutShown(recent, nil); len(got) != len(recent) {
		t.Errorf("withoutShown(nil) dropped entries: %d of %d left", len(got), len(recent))
	}
}

// TestHistoryHeadingSaysAlsoWhenSomethingIsAbove: what survives the dedup is no
// longer the full history, so titling it "Incident history" would promise a
// completeness it does not have.
func TestHistoryHeadingSaysAlsoWhenSomethingIsAbove(t *testing.T) {
	defer i18n.Set("en")
	i18n.Set("en")
	// RELATIVE to now, never an absolute date: recentIncidents filters the journal
	// against time.Now() minus the 24h window, so a hardcoded timestamp puts the
	// fixture outside the window the day after it is written and the test starts
	// failing on the calendar rather than on the code.
	seen := time.Now().Add(-2 * time.Hour)

	c := newRowTestController()
	c.incidents = history.NewIncidentLog()
	c.incidents.Observe("anthropic", []history.IncidentEntry{
		{Summary: "Older thing", Severity: int(providers.SevMinor)},
		{Summary: "Open right now", Severity: int(providers.SevMajor)},
	}, seen)

	plain := container.NewVBox()
	c.addRecentIncidents(plain, "anthropic", nil, seen)
	if !containsText(labelTexts(plain), i18n.T().RecentIncidents24h) {
		t.Errorf("with nothing above, heading = %v, want the plain history title", labelTexts(plain))
	}

	alt := container.NewVBox()
	c.addRecentIncidents(alt, "anthropic", nil, seen, providers.Incident{Summary: "Open right now"})
	texts := labelTexts(alt)
	if !containsText(texts, i18n.T().AlsoRecentIncidents24h) {
		t.Errorf("with a card above, heading = %v, want the \"also\" title", texts)
	}
	if containsText(texts, "Open right now") {
		t.Error("the incident drawn above was listed again in the history")
	}

	// Dedup that empties the list drops the section entirely — no orphan heading.
	empty := container.NewVBox()
	c.addRecentIncidents(empty, "anthropic", nil, seen,
		providers.Incident{Summary: "Older thing"}, providers.Incident{Summary: "Open right now"})
	if len(empty.Objects) != 0 {
		t.Errorf("fully deduped history still rendered %v", labelTexts(empty))
	}
}

// TestColdStartHistorySaysLastSeenNotResolved: with nothing read this run (the
// journal came off disk and the first poll failed), the app knows only when it
// last saw each incident. Claiming "resolved" there infers a resolution from an
// absence that is its own downtime.
func TestColdStartHistorySaysLastSeenNotResolved(t *testing.T) {
	defer i18n.Set("en")
	i18n.Set("en")
	e := history.IncidentEntry{
		Summary:   "Service disruption",
		Started:   time.Now().Add(-3 * time.Hour),
		FirstSeen: time.Now().Add(-3 * time.Hour),
		LastSeen:  time.Now().Add(-90 * time.Minute),
	}
	texts := labelTexts(recentIncidentCard(e, time.Time{}))
	joined := strings.Join(texts, " | ")
	if !strings.Contains(joined, "last seen") {
		t.Errorf("cold-start card = %q, want a last-seen statement", joined)
	}
	if strings.Contains(joined, "resolved") {
		t.Errorf("cold-start card claims resolution it cannot know: %q", joined)
	}
}
