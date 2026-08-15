package ui

import (
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/widget"

	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/history"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/i18n"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/providers"
)

// formatElapsed shows at most two units, drops a zero trailing unit, and never
// renders a negative age from a feed timestamp that sits in the future.
func TestFormatElapsed(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{-2 * time.Hour, "0m"},
		{30 * time.Second, "0m"},
		{45 * time.Minute, "45m"},
		{2*time.Hour + 15*time.Minute, "2h 15m"},
		{3 * time.Hour, "3h"},
		{23*time.Hour + 59*time.Minute, "23h 59m"},
		{24 * time.Hour, "1d"},
		{18*24*time.Hour + 4*time.Hour, "18d 4h"},
		{153*24*time.Hour + 6*time.Hour, "153d 6h"},
	}
	for _, tc := range cases {
		if got := formatElapsed(tc.d); got != tc.want {
			t.Errorf("formatElapsed(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

// An event still open but untouched past the zombie horizon must leave the
// live list — otherwise a months-old straggler pads "Active incidents (N)" and
// buries whatever is genuinely happening now. Staleness is measured from the
// LAST UPDATE, so a long outage under active management stays live however old
// its start; an incident with no timestamps at all fails open into live.
func TestSplitStaleIncidents(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	old := now.Add(-153 * 24 * time.Hour)
	incs := []providers.Incident{
		{Summary: "zombie", Started: old, Updated: old},
		{Summary: "long but managed", Started: old, Updated: now.Add(-2 * time.Hour)},
		{Summary: "fresh", Started: now.Add(-90 * time.Minute)},
		{Summary: "no update reported, old start", Started: old},
		{Summary: "no timestamps at all"},
	}

	live, stale := splitStaleIncidents(incs, now)
	wantLive := []string{"long but managed", "fresh", "no timestamps at all"}
	wantStale := []string{"zombie", "no update reported, old start"}

	check := func(label string, got []providers.Incident, want []string) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s = %d incidents, want %d: %+v", label, len(got), len(want), got)
		}
		for i, w := range want {
			if got[i].Summary != w {
				t.Errorf("%s[%d] = %q, want %q", label, i, got[i].Summary, w)
			}
		}
	}
	check("live", live, wantLive)
	check("stale", stale, wantStale)
}

// cardText joins the text of every Label in a card's widget tree, so a test can
// assert on what the card actually renders without reaching for a fixed child
// index that any layout tweak would invalidate.
func cardText(t *testing.T, obj fyne.CanvasObject) string {
	t.Helper()
	var parts []string
	var walk func(fyne.CanvasObject)
	walk = func(o fyne.CanvasObject) {
		switch v := o.(type) {
		case *widget.Label:
			parts = append(parts, v.Text)
		case *fyne.Container:
			for _, child := range v.Objects {
				walk(child)
			}
		}
	}
	walk(obj)
	return strings.Join(parts, " | ")
}

// The age parenthetical closes the incident title so the drill-down answers
// "how long has this been going on" without mental arithmetic on the Started
// timestamp — and it is what marks a zombie on sight. A feed that reports no
// start time gets no suffix rather than an invented age.
func TestIncidentAgeSuffix(t *testing.T) {
	i18n.Set("en")
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	got := incidentAgeSuffix(providers.Incident{Started: now.Add(-2*time.Hour - 15*time.Minute)}, now)
	if want := " (started 2h 15m ago)"; got != want {
		t.Errorf("incidentAgeSuffix = %q, want %q", got, want)
	}
	if got := incidentAgeSuffix(providers.Incident{}, now); got != "" {
		t.Errorf("incidentAgeSuffix with no start = %q, want empty", got)
	}
}

// A history card's clock times are bare HH:MM, so the card must also say how
// long ago its endpoint was: "resolved 12:18" alone doesn't distinguish twenty
// minutes from twenty hours. A still-ongoing entry (LastSeen at the provider's
// current poll) counts from its start instead.
func TestRecentIncidentCardStatesAge(t *testing.T) {
	i18n.Set("en")
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	resolved := history.IncidentEntry{
		Summary:  "Increased HTTP Errors in London",
		Started:  now.Add(-5 * time.Hour),
		LastSeen: now.Add(-2*time.Hour - 10*time.Minute),
	}
	if text := cardText(t, recentIncidentCard(resolved, now)); !strings.Contains(text, "(resolved 2h 10m ago)") {
		t.Errorf("resolved card = %q, want it to contain %q", text, "(resolved 2h 10m ago)")
	}

	ongoing := history.IncidentEntry{
		Summary:  "Network Performance Issues in Hamburg",
		Started:  now.Add(-90 * time.Minute),
		LastSeen: now,
	}
	if text := cardText(t, recentIncidentCard(ongoing, now)); !strings.Contains(text, "(started 1h 30m ago)") {
		t.Errorf("ongoing card = %q, want it to contain %q", text, "(started 1h 30m ago)")
	}
}

// The rolling 24-hour history window straddles midnight for half of every day.
// Two bare HH:MM clock times across that boundary read backwards — an incident
// that began at 12:20 yesterday and cleared at 12:18 today would render as
// "started 12:20, resolved 12:18", which looks like it resolved before it
// began. The off-day end must carry its date.
func TestRecentIncidentCardDatesAcrossMidnight(t *testing.T) {
	i18n.Set("en")
	// Local() drives the rendering, so anchor the case in the OS-local zone
	// rather than UTC — otherwise the calendar day under test depends on where
	// the suite happens to run.
	now := time.Date(2026, 8, 1, 12, 18, 0, 0, time.Local)
	e := history.IncidentEntry{
		Summary:  "Network Performance Issues in Hamburg",
		Started:  time.Date(2026, 7, 31, 12, 20, 0, 0, time.Local),
		LastSeen: now.Add(-8 * time.Minute),
	}

	text := cardText(t, recentIncidentCard(e, now))
	if !strings.Contains(text, "started 07-31 12:20") {
		t.Errorf("card = %q, want the previous-day start dated (%q)", text, "started 07-31 12:20")
	}
	// The end is on the reference day, so it stays bare — dating both would be
	// noise on the common same-day case.
	if !strings.Contains(text, "resolved 12:10") {
		t.Errorf("card = %q, want the same-day end left bare (%q)", text, "resolved 12:10")
	}
}

// The common case — both ends on the reference day — must stay compact.
func TestFormatSpanClockSameDayStaysBare(t *testing.T) {
	ref := time.Date(2026, 8, 1, 23, 59, 0, 0, time.Local)
	if got := formatSpanClock(time.Date(2026, 8, 1, 0, 1, 0, 0, time.Local), ref); got != "00:01" {
		t.Errorf("formatSpanClock same day = %q, want %q", got, "00:01")
	}
	// Two minutes apart but on opposite sides of midnight: near in time, still
	// a different calendar day, so the date must appear.
	if got := formatSpanClock(time.Date(2026, 7, 31, 23, 58, 0, 0, time.Local), ref); got != "07-31 23:58" {
		t.Errorf("formatSpanClock across midnight = %q, want %q", got, "07-31 23:58")
	}
}
