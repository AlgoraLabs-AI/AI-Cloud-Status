package ui

import (
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"

	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/i18n"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/providers"
)

// minScreenWidth is the narrowest screen this app promises to show the whole
// table on without a horizontal scrollbar: 1920px, the floor on every machine
// this runs on. Below it, scrolling sideways is accepted.
const minScreenWidth = 1920

// sidePanelFraction is buildContent's HSplit offset — the share of the window
// the check-selection panel takes, leaving the rest for the table.
const sidePanelFraction = 0.24

// tableLaidOutWidth is what columnsLayout actually asks for: every column plus a
// padding before each one and one closing the row.
func tableLaidOutWidth() float32 {
	w := theme.Padding()
	for _, c := range tableColWidths {
		w += c + theme.Padding()
	}
	return w
}

// TestTableFitsTheMinimumScreen is the regression for the horizontal scrollbar
// in the 2026-08-14 screenshot. The table lives in a bidirectional scroll, so
// every pixel the columns total over what the window can show becomes a sideways
// scrollbar — which is what the old 1462px-wide column set did on a 1920px
// screen, where the side panel leaves it about 1450.
func TestTableFitsTheMinimumScreen(t *testing.T) {
	avail := float32(minScreenWidth * (1 - sidePanelFraction))
	if got := tableLaidOutWidth(); got > avail {
		t.Errorf("table lays out at %.0fpx but a %dpx screen leaves it %.0fpx — it will scroll horizontally",
			got, minScreenWidth, avail)
	}
}

// TestColumnHeadersFitTheirColumns guards the other direction: a header is a
// centered canvas.Text, and Fyne neither wraps nor clips it, so a title longer
// than its column paints over both neighbours. Latency ("Latencia", 94px) and
// Uptime ("Disponibilidade", 140px) are the ones that set their column's floor.
func TestColumnHeadersFitTheirColumns(t *testing.T) {
	defer i18n.Set("en")
	for _, lang := range i18n.Languages() {
		i18n.Set(lang.Code)
		tr := i18n.T()
		cols := []string{tr.ColName, tr.ColStatus, tr.ColLastChecked, tr.ColNextPoll,
			tr.ColLatency, tr.ColUptime, tr.ColRegions, tr.ColIncidents}
		for i, title := range cols {
			w := fyne.MeasureText(title, theme.Size(theme.SizeNameSubHeadingText), fyne.TextStyle{Bold: true}).Width
			if w > tableColWidths[i] {
				t.Errorf("language %q: header %q needs %.1fpx, column %d is %.0fpx — it will paint over its neighbours",
					lang.Code, title, w, i, tableColWidths[i])
			}
		}
	}
}

// TestIncidentCellNeverExceedsItsColumn is the direct regression for prefixedLine.
// It used to lay the bold "Updated (12m ago):" prefix and the update note side by
// side, giving the note whatever width was left but never less than 60px — so a
// long prefix plus a long note produced a cell wider than the incidents column.
// Fyne honours a child's MinSize, so that widened the row, then the table, then
// the window. Stacked, the cell can never be wider than the column it sits in.
func TestIncidentCellNeverExceedsItsColumn(t *testing.T) {
	reducedMotionPref = true // the dot animation needs a running driver
	defer func() { reducedMotionPref = false }()
	defer i18n.Set("en")

	now := time.Date(2026, 8, 14, 17, 42, 0, 0, time.UTC)
	inc := providers.Incident{
		Summary:  "Service disruption on Claude services",
		Severity: providers.SevMajor,
		Started:  now.Add(-10 * time.Hour),
		Updated:  now.Add(-12 * time.Minute),
		Note: "2026-08-14 17:30 (UTC-03:00) - We are investigating reports of degraded " +
			"performance affecting the Claude API, Claude Code, and Claude Cowork. " +
			"We will provide an update as soon as possible.",
	}
	limit := tableColWidths[incidentsColIndex]
	for _, lang := range i18n.Languages() {
		i18n.Set(lang.Code)
		cell := incidentsCell(rowSpec{
			id: "p", name: "P", state: stateOutage,
			incidentData: []providers.Incident{inc},
			incidents:    incidentCellText([]providers.Incident{inc}, now),
		}, now)
		if got := cell.MinSize().Width; got > limit {
			t.Errorf("language %q: incidents cell needs %.1fpx, column is %.0fpx — it widens the table",
				lang.Code, got, limit)
		}
	}
}

// TestRowCellsStayInsideTheirColumns is the whole-row version: no cell of a
// fully-populated row may report a MinSize wider than the column it is placed
// in, in any language. columnsLayout resizes cells DOWN to the column width, but
// Fyne sizes the row from the cells' MinSize, so an oversized cell is exactly how
// the table comes to be wider than the window.
func TestRowCellsStayInsideTheirColumns(t *testing.T) {
	reducedMotionPref = true
	defer func() { reducedMotionPref = false }()
	defer i18n.Set("en")

	now := time.Date(2026, 8, 14, 17, 42, 0, 0, time.UTC)
	inc := providers.Incident{
		Summary:  "Elevated error rates across multiple availability zones",
		Severity: providers.SevCritical,
		Started:  now.Add(-3 * time.Hour),
		Updated:  now.Add(-4 * time.Minute),
		Note:     "We are continuing to investigate elevated error rates in several regions.",
	}
	spec := rowSpec{
		id: "aws", name: "Cloudflare DNS (1.1.1.1)", subtitle: "ICMP · 1.1.1.1",
		linkURL: "https://health.aws.amazon.com/health/status",
		state:   stateOutage, when: "17:42:29", latency: "1000ms",
		regions:      []string{"me-central-1", "me-south-1"},
		incidentData: []providers.Incident{inc},
		incidents:    incidentCellText([]providers.Incident{inc}, now),
	}
	for _, lang := range i18n.Languages() {
		i18n.Set(lang.Code)
		c := newRowTestController()
		rw := c.newRowWidget(spec, testCtx(now.Add(30*time.Second)))
		for i, cell := range rw.grid.Objects {
			if got := cell.MinSize().Width; got > tableColWidths[i] {
				t.Errorf("language %q: cell %d needs %.1fpx, column is %.0fpx",
					lang.Code, i, got, tableColWidths[i])
			}
		}
	}
}

// TestColumnsFillTheAvailableWidth is the regression for the blank strip at the
// right edge: the columns were fixed, so on any window wider than their sum the
// surplus was parked in one unused gap instead of going to the text that had to
// wrap inside its column.
func TestColumnsFillTheAvailableWidth(t *testing.T) {
	l := columnsLayout{tableColWidths}
	n := len(tableColWidths)
	pad := theme.Padding()

	// A window comfortably wider than the minimums.
	const avail = 1800
	got := l.resolve(n, avail)

	used := pad
	for _, w := range got {
		used += w + pad
	}
	if leftover := avail - used; leftover > 1 {
		t.Errorf("%.0fpx of %.0f left unused — that is the blank strip this fixes", leftover, float32(avail))
	}
	for i := range got {
		if got[i] < tableColWidths[i] {
			t.Errorf("column %d resolved to %.0f, below its %.0f minimum", i, got[i], tableColWidths[i])
		}
	}
}

// TestSurplusGoesToTheColumnsThatWrap: handing extra width to a column whose
// content is a fixed-size centred value (a timestamp, a countdown, a latency
// figure) does not help it — it just spreads that value further from its
// neighbours, which is MORE whitespace, not less. Only the columns whose text
// actually wraps should grow.
func TestSurplusGoesToTheColumnsThatWrap(t *testing.T) {
	l := columnsLayout{tableColWidths}
	n := len(tableColWidths)
	got := l.resolve(n, 1800)

	for _, i := range []int{colName, colRegions, colIncidents} {
		if got[i] <= tableColWidths[i] {
			t.Errorf("column %d did not grow (%.0f), but its content wraps", i, got[i])
		}
	}
	for _, i := range []int{colStatus, colWhen, colNext, colLatency, colUptime} {
		if got[i] != tableColWidths[i] {
			t.Errorf("column %d grew to %.0f; its content is fixed-size, so the extra is pure whitespace",
				i, got[i])
		}
	}
}

// TestNarrowWindowKeepsTheMinimums: squeezed below the sum of the minimums, the
// columns must NOT shrink. Text would start colliding, and a horizontal
// scrollbar is a far better outcome than unreadable overlap.
func TestNarrowWindowKeepsTheMinimums(t *testing.T) {
	l := columnsLayout{tableColWidths}
	n := len(tableColWidths)
	for _, avail := range []float32{0, 400, 900, 1200} {
		got := l.resolve(n, avail)
		for i := range got {
			if got[i] != tableColWidths[i] {
				t.Errorf("at %.0fpx available, column %d is %.0f, want its %.0f minimum",
					avail, i, got[i], tableColWidths[i])
			}
		}
	}
}

// TestVeryWideWindowSpreadsTheRemainder: once the wrapping columns hit the width
// past which more space stops helping them read, the leftover is shared evenly
// rather than pooled at one edge — otherwise a 4K screen just reopens the same
// hole somewhere else.
func TestVeryWideWindowSpreadsTheRemainder(t *testing.T) {
	l := columnsLayout{tableColWidths}
	n := len(tableColWidths)
	const avail = 3800
	got := l.resolve(n, avail)

	used := theme.Padding()
	for _, w := range got {
		used += w + theme.Padding()
	}
	if leftover := avail - used; leftover > 1 {
		t.Errorf("%.0fpx left unused on a very wide window", leftover)
	}
	for i := range got {
		if got[i] <= tableColWidths[i] {
			t.Errorf("column %d stayed at its minimum (%.0f) while space went unclaimed", i, got[i])
		}
	}
}

// TestColumnWidthGenerationMovesOnResize guards the wiring that keeps wrapped
// cells honest. The incidents note and the region chips bake their wrap width in
// when they are built, so a resize has to be visible to them — otherwise they
// keep wrapping for the previous window size until their text happens to change.
func TestColumnWidthGenerationMovesOnResize(t *testing.T) {
	l := columnsLayout{tableColWidths}
	n := len(tableColWidths)

	publishColumnWidths(l.resolve(n, 1500))
	first := colGen()
	wide := colWidth(colIncidents)

	publishColumnWidths(l.resolve(n, 1500)) // same width again
	if colGen() != first {
		t.Error("generation moved without the width changing — every layout pass would rebuild cells")
	}

	publishColumnWidths(l.resolve(n, 1900))
	if colGen() == first {
		t.Error("generation did not move on a real resize — wrapped cells would keep the old width")
	}
	if colWidth(colIncidents) <= wide {
		t.Error("the incidents column did not widen with the window")
	}
}
