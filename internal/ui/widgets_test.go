package ui

import (
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// TestFlowLayoutNeverExceedsWidth is the regression for the region-chip overflow:
// the flow layout must report a MinSize no wider than its column, so chips can't
// bleed into the neighbouring column.
func TestFlowLayoutNeverExceedsWidth(t *testing.T) {
	test.NewApp()
	defer test.NewApp()

	const cap = 60
	chips := []fyne.CanvasObject{
		widget.NewLabel("alpha"), widget.NewLabel("bravo"), widget.NewLabel("charlie"),
	}
	l := flowLayout{width: cap}
	ms := l.MinSize(chips)
	if ms.Width > cap {
		t.Fatalf("flowLayout MinSize width %.0f exceeds the column cap %d", ms.Width, cap)
	}
	// With three chips wider than the cap together, it must wrap → taller than one chip.
	single := chips[0].MinSize().Height
	if ms.Height <= single {
		t.Fatalf("flowLayout did not wrap: height %.0f <= one chip %.0f", ms.Height, single)
	}
}

// TestTruncateToWidthFits is the regression for a single over-wide region chip:
// truncated text must fit the budget so the chip can't overflow.
func TestTruncateToWidthFits(t *testing.T) {
	test.NewApp()
	defer test.NewApp()

	style := fyne.TextStyle{}
	size := theme.TextSize()
	const budget = 40
	out := truncateToWidth("Delhi (asia-south2) a very long region label", budget, size, style)
	if w := fyne.MeasureText(out, size, style).Width; w > budget {
		t.Fatalf("truncated text width %.0f exceeds budget %d (%q)", w, budget, out)
	}
	// A short string that already fits is returned unchanged.
	if got := truncateToWidth("eu", 200, size, style); got != "eu" {
		t.Fatalf("a fitting string should be unchanged, got %q", got)
	}
}

// TestWrappedTextHeightGrowsWithText is the regression for the incidents wrap:
// longer text in a narrow cell must report a taller height (the row grows).
func TestWrappedTextHeightGrowsWithText(t *testing.T) {
	test.NewApp()
	defer test.NewApp()

	short := wrappedTextHeight("All good.", 300, fyne.TextStyle{})
	long := wrappedTextHeight(
		"Network traffic to the region is experiencing intermittent elevated latency and possible packet loss across several availability zones.",
		120, fyne.TextStyle{})
	if long <= short {
		t.Fatalf("wrapped height did not grow: long %.0f <= short %.0f", long, short)
	}
}

// TestWrappedTextHeightOverwideToken guards the clipping fix: a single unbroken
// token far wider than a narrow cell must report more than one line (Fyne breaks
// it at the rune level), so the row doesn't clip it.
func TestWrappedTextHeightOverwideToken(t *testing.T) {
	test.NewApp()
	defer test.NewApp()

	wide := wrappedTextHeight("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 60, fyne.TextStyle{})
	short := wrappedTextHeight("a", 60, fyne.TextStyle{})
	if wide <= short {
		t.Fatalf("an over-wide token should span multiple lines: wide %.0f <= short %.0f", wide, short)
	}
}
