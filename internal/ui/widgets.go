package ui

import (
	"fmt"
	"image/color"
	"math"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/i18n"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/providers"
)

// transitionDuration is the duration for state-change animations, or 0 when the
// user prefers reduced motion (animations are then skipped entirely).
func transitionDuration() time.Duration {
	if reducedMotion() {
		return 0
	}
	return 250 * time.Millisecond
}

// sectionHeader renders a bold section header for the Settings dialog. Sized at
// sub-heading (not heading): inside a dialog the 20px heading read as comically
// oversized next to the app's 13px body text.
func sectionHeader(title string) fyne.CanvasObject {
	t := canvas.NewText(title, theme.Color(theme.ColorNameForeground))
	t.TextStyle = fyne.TextStyle{Bold: true}
	t.TextSize = theme.Size(theme.SizeNameSubHeadingText)
	return container.NewPadded(t)
}

// columnHeader renders a bold column header, larger than table body text and
// CENTERED within its fixed column slot (columnsLayout resizes each header to
// the column width), so a title can never read as belonging to the neighbouring
// column.
func columnHeader(title string) fyne.CanvasObject {
	t := canvas.NewText(title, theme.Color(theme.ColorNameForeground))
	t.TextStyle = fyne.TextStyle{Bold: true}
	t.TextSize = theme.Size(theme.SizeNameSubHeadingText)
	return container.NewCenter(t)
}

// blurOnToggle wraps a Check's OnChanged so the checkbox releases keyboard
// focus right after it is toggled. Fyne keeps a clicked Check focused, and with
// this app's deliberately strong focus colour the lingering focus ring reads as
// a stuck blue halo around the box rather than as keyboard focus. Every
// user-facing checkbox is created through this. Must be called before the
// window exists is fine — the blur is a no-op until c.window is set.
func (c *Controller) blurOnToggle(chk *widget.Check) *widget.Check {
	orig := chk.OnChanged
	chk.OnChanged = func(b bool) {
		if orig != nil {
			orig(b)
		}
		if c.window != nil {
			c.window.Canvas().Focus(nil)
		}
	}
	return chk
}

// monoLabel renders numeric values (latency / loss) in a monospace face,
// centered in its cell: right-aligned latency values sat flush against the
// uptime strip of the NEXT column and visually merged with it.
func monoLabel(text string) *widget.Label {
	return widget.NewLabelWithStyle(text, fyne.TextAlignCenter, fyne.TextStyle{Monospace: true})
}

// incidentRule is the horizontal rule that FENCES an incident: one above its
// title and one below its last field, wherever incidents are listed. A blank
// line used to do that job and lost it the moment an incident wrapped to four
// lines — with three alerts stacked in one column, title, severity and update
// note all ran together and nothing said which line belonged to which incident.
//
// Its width is fixed rather than measured because the transcript in
// incidentCellText has to reproduce it character for character. 36 '=' measures
// 289px, which clears the 320px incidents column (304px inside the label's
// padding) — the narrowest place it is drawn — without wrapping.
const incidentRule = "===================================="

// incidentRuleLabel is one drawn instance of incidentRule. Each call returns a
// fresh widget: a canvas object belongs to exactly one parent. align follows the
// text around it — centered inside a table cell, leading in the drill-down panel.
func incidentRuleLabel(align fyne.TextAlign) *widget.Label {
	return widget.NewLabelWithStyle(incidentRule, align, fyne.TextStyle{})
}

// severityLine renders "Severity: major" in the hue the 24h uptime strip paints
// that severity with — red for major/critical, amber for minor, green
// otherwise — so the word and the coloured band beside it always agree.
//
// It is a RichText rather than a Label because Fyne colours text only by theme
// colour NAME, and only RichText exposes that per segment. The cost is that
// this one line is not mouse-selectable like the rest of an incident card; it
// carries a single derived word, which is not something anyone copies.
func severityLine(sev providers.Severity, align fyne.TextAlign) fyne.CanvasObject {
	return widget.NewRichText(&widget.TextSegment{
		Text: fmt.Sprintf(i18n.T().SeverityLabel, sev.String()),
		Style: widget.RichTextStyle{
			Alignment: align,
			ColorName: severityColorName(sev),
			Inline:    true,
			TextStyle: fyne.TextStyle{Bold: true},
		},
	})
}

// regionBadge renders a single region chip, no wider than maxText for its label
// (longer names are ellipsised; the full name is in the click-through popup), so
// a chip can never overflow the fixed-width Regions column. Regions the user has
// declared "of interest" are highlighted; a deactivated region (#7) is dimmed.
func regionBadge(name string, highlighted, muted bool, maxText float32) fyne.CanvasObject {
	style := fyne.TextStyle{}
	var bg, fg color.Color
	switch {
	case muted:
		// Dimmed + italic signals "alerts off here" without widening the chip
		// (a text suffix overflowed the fixed-width Regions column).
		bg = theme.Color(theme.ColorNameDisabledButton)
		fg = theme.Color(theme.ColorNameDisabled)
		style.Italic = true
	case highlighted:
		bg = theme.Color(theme.ColorNamePrimary)
		fg = theme.Color(theme.ColorNameForegroundOnPrimary)
		style.Bold = true
	default:
		bg = theme.Color(theme.ColorNameInputBackground)
		fg = theme.Color(theme.ColorNameForeground)
	}
	rect := canvas.NewRectangle(bg)
	rect.CornerRadius = 4
	rect.StrokeColor = theme.Color(theme.ColorNameSeparator)
	rect.StrokeWidth = 1
	size := theme.Size(theme.SizeNameCaptionText)
	txt := canvas.NewText(truncateToWidth(name, maxText, size, style), fg)
	txt.TextStyle = style
	txt.TextSize = size
	return container.NewStack(rect, container.NewPadded(txt))
}

// truncateToWidth shortens s with an ellipsis until it fits maxWidth at the given
// text size/style. Returns s unchanged when it already fits.
func truncateToWidth(s string, maxWidth, size float32, style fyne.TextStyle) string {
	if maxWidth <= 0 || fyne.MeasureText(s, size, style).Width <= maxWidth {
		return s
	}
	const ell = "…"
	r := []rune(s)
	for len(r) > 1 {
		r = r[:len(r)-1]
		if fyne.MeasureText(string(r)+ell, size, style).Width <= maxWidth {
			return string(r) + ell
		}
	}
	return ell
}

// regionBadges lays out region chips, highlighting any that match the user's
// regions of interest and dimming any the user has deactivated. Each chip is
// tappable and opens the deactivate/reactivate popup for that region (#7).
// Returns an empty placeholder when there are no regions.
func (c *Controller) regionBadges(regions, interest []string) fyne.CanvasObject {
	if len(regions) == 0 {
		// Centered in the column slot so the em dash doesn't hug the left edge.
		return hCentered(widget.NewLabel("—"))
	}
	colW := colWidth(regionsColIndex)
	// Cap each chip's TEXT so chip width (text + chip padding) never exceeds the
	// column — guarantees no overflow into neighbouring columns even for a long
	// single region name like "Delhi (asia-south2)".
	maxText := colW - 5*theme.Padding()
	var chips []fyne.CanvasObject
	for _, r := range regions {
		// Highlight only when the user has actually declared regions of interest;
		// with an empty interest set every region "matches", which would
		// highlight everything and defeat the purpose.
		highlighted := len(interest) > 0 && providers.MatchRegion(r, interest)
		chip := regionBadge(r, highlighted, c.regionMuted(r), maxText)
		region := r // capture per-iteration for the closure
		chips = append(chips, newTappableBadge(chip, func() { c.showRegionPopup(region) }))
	}
	// Flow-wrap the chips within the fixed Regions column width so several badges
	// grow the row downward instead of overflowing into the next column, and
	// center the resulting block on the column like every other cell.
	return hCentered(container.New(flowLayout{width: colW}, chips...))
}

// flowLayout lays children left-to-right, wrapping to a new row when the next
// child would exceed width. It is used for region chips in a fixed-width table
// column: because the wrap width is known up front, MinSize reports the true
// wrapped height (so the row grows) without depending on a prior Resize.
type flowLayout struct{ width float32 }

func (l flowLayout) arrange(objs []fyne.CanvasObject, place bool) fyne.Size {
	pad := theme.Padding()
	var x, y, rowH, maxRowW float32
	for _, o := range objs {
		ms := o.MinSize()
		if x > 0 && x+ms.Width > l.width {
			if x-pad > maxRowW {
				maxRowW = x - pad
			}
			x = 0
			y += rowH + pad
			rowH = 0
		}
		if place {
			o.Move(fyne.NewPos(x, y))
			o.Resize(ms)
		}
		x += ms.Width + pad
		if ms.Height > rowH {
			rowH = ms.Height
		}
	}
	if x-pad > maxRowW {
		maxRowW = x - pad
	}
	if maxRowW > l.width {
		maxRowW = l.width // never report wider than the column slot
	}
	return fyne.NewSize(maxRowW, y+rowH)
}

func (l flowLayout) MinSize(objs []fyne.CanvasObject) fyne.Size   { return l.arrange(objs, false) }
func (l flowLayout) Layout(objs []fyne.CanvasObject, _ fyne.Size) { l.arrange(objs, true) }

// hCenterLayout centers its children horizontally at their minimum size while
// keeping them pinned to the TOP of the cell. It is what a table cell wants and
// container.NewCenter is not: Center also centers VERTICALLY, so in a row made
// tall by a multi-line incidents cell the status badge and the uptime strip
// would float halfway down the row while the name they belong to sat at the top.
// A child wider than the cell is capped at the cell width rather than allowed to
// spill into the neighbouring column.
type hCenterLayout struct{}

func (hCenterLayout) MinSize(objs []fyne.CanvasObject) fyne.Size {
	var s fyne.Size
	for _, o := range objs {
		s = s.Max(o.MinSize())
	}
	return s
}

func (hCenterLayout) Layout(objs []fyne.CanvasObject, size fyne.Size) {
	for _, o := range objs {
		ms := o.MinSize()
		w := fyne.Min(ms.Width, size.Width)
		o.Resize(fyne.NewSize(w, ms.Height))
		o.Move(fyne.NewPos((size.Width-w)/2, 0))
	}
}

// hCentered wraps one object in an hCenterLayout cell.
func hCentered(o fyne.CanvasObject) *fyne.Container {
	return container.New(hCenterLayout{}, o)
}

// wrapCellLayout sizes a single word-wrapping Label to a fixed column width and
// reports its wrapped height computed by MEASURING the text — it never resizes a
// child inside MinSize, so it can't trigger the repeated relayout/refresh churn a
// Resize-in-MinSize causes. Layout (where resizing IS allowed) stretches the label
// to the cell so Fyne's own wrapping renders the same line breaks we measured.
type wrapCellLayout struct {
	text  string
	width float32
	style fyne.TextStyle // measurement style; zero value = regular text
}

func (l wrapCellLayout) MinSize([]fyne.CanvasObject) fyne.Size {
	return fyne.NewSize(l.width, wrappedTextHeight(l.text, l.width, l.style))
}

func (l wrapCellLayout) Layout(objs []fyne.CanvasObject, size fyne.Size) {
	for _, o := range objs {
		o.Move(fyne.NewPos(0, 0))
		o.Resize(size)
	}
}

// badgeTextWidth is what the Status column has left for the state label once the
// hue dot and the shape icon (and the padding the HBox puts between the three)
// have taken their fixed share.
func badgeTextWidth() float32 {
	return colWidth(colStatus) - badgeDotSide - badgeIconSide - 2*theme.Padding()
}

// badgeLabelLayout is wrapCellLayout for the status badge, differing in two ways
// that matter. It measures the label's CURRENT text instead of a string captured
// at construction: the badge is updated in place (SetText) rather than rebuilt on
// every refresh, so a captured string would keep reporting the height of the
// state the row USED to be in — a two-line "Status feed unavailable" replaced by
// a one-line "Operational" would leave a row-tall gap, and the reverse would
// clip. And it SHRINKS to the text when the text fits in width, so a short label
// like "Operational" leaves the badge a compact block its Center container can
// place on the column axis; width stays the ceiling, and a label too long for it
// still wraps inside the column exactly as before.
type badgeLabelLayout struct {
	label *widget.Label
	width float32
}

func (l badgeLabelLayout) MinSize([]fyne.CanvasObject) fyne.Size {
	w := l.width
	// wrappedTextHeight wraps at width-3*innerPadding, so a fitted width has to
	// carry that much slack or the text it was measured to hold would wrap.
	if fit := fyne.MeasureText(l.label.Text, theme.TextSize(), fyne.TextStyle{}).Width + 3*theme.InnerPadding(); fit < w {
		w = fit
	}
	return fyne.NewSize(w, wrappedTextHeight(l.label.Text, w, fyne.TextStyle{}))
}

func (l badgeLabelLayout) Layout(objs []fyne.CanvasObject, size fyne.Size) {
	for _, o := range objs {
		o.Move(fyne.NewPos(0, 0))
		o.Resize(size)
	}
}

// wrappedTextHeight returns the height a word-wrapping label needs to render text
// at the given total cell width, matching Fyne's greedy word wrap. It errs toward
// OVER-reporting (a slightly conservative wrap width + counting rune-broken
// over-wide words as multiple lines) so the row never clips its last line — extra
// blank space is harmless, a clipped line is not.
func wrappedTextHeight(text string, width float32, style fyne.TextStyle) float32 {
	size := theme.TextSize()
	pad := theme.InnerPadding()
	// Subtract a bit more than the label's own padding so we wrap at least as
	// eagerly as Fyne's renderer (whose effective text width can be a touch
	// narrower); never under-report.
	avail := width - 3*pad
	lineH := fyne.MeasureText("Ag", size, style).Height
	if avail < lineH || text == "" {
		return lineH + 2*pad
	}
	lines := 0
	for _, para := range strings.Split(text, "\n") {
		words := strings.Fields(para)
		if len(words) == 0 {
			lines++
			continue
		}
		cur := words[0]
		for _, w := range words[1:] {
			if fyne.MeasureText(cur+" "+w, size, style).Width <= avail {
				cur += " " + w
			} else {
				lines += lineSpan(cur, avail, size, style) // the line just closed may itself be over-wide
				cur = w
			}
		}
		lines += lineSpan(cur, avail, size, style)
	}
	if lines < 1 {
		lines = 1
	}
	return float32(lines)*lineH + 2*pad
}

// lineSpan reports how many visual lines s occupies: 1, or more when s is a single
// token wider than avail, which Fyne breaks at the rune level.
func lineSpan(s string, avail, size float32, style fyne.TextStyle) int {
	w := fyne.MeasureText(s, size, style).Width
	if w <= avail {
		return 1
	}
	n := int(math.Ceil(float64(w / avail)))
	if n < 1 {
		n = 1
	}
	return n
}

// tappableBadge wraps a region chip so a click opens its deactivation popup. It
// implements fyne.Tappable (Fyne dispatches a tap to the innermost tappable, so
// the click does NOT also bubble to the row's provider-detail handler) and shows
// a pointer cursor. It is deliberately NOT focusable, so it doesn't compete with
// the row for keyboard Tab traversal.
type tappableBadge struct {
	widget.BaseWidget
	content fyne.CanvasObject
	onTap   func()
}

func newTappableBadge(content fyne.CanvasObject, onTap func()) *tappableBadge {
	b := &tappableBadge{content: content, onTap: onTap}
	b.ExtendBaseWidget(b)
	return b
}

func (b *tappableBadge) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(b.content)
}

func (b *tappableBadge) Tapped(*fyne.PointEvent) {
	if b.onTap != nil {
		b.onTap()
	}
}

func (b *tappableBadge) Cursor() desktop.Cursor { return desktop.PointerCursor }

// focusRow makes a table row keyboard-focusable with a visible focus ring, so
// Tab / Shift+Tab traverses rows (not just checkboxes and buttons). An optional
// onSelect fires on Enter / Space / click.
type focusRow struct {
	widget.BaseWidget
	content  fyne.CanvasObject
	onSelect func()
	focused  bool
}

func newFocusRow(content fyne.CanvasObject, onSelect func()) *focusRow {
	r := &focusRow{content: content, onSelect: onSelect}
	r.ExtendBaseWidget(r)
	return r
}

func (r *focusRow) CreateRenderer() fyne.WidgetRenderer {
	border := canvas.NewRectangle(color.Transparent)
	border.StrokeWidth = 2
	border.StrokeColor = color.Transparent
	border.CornerRadius = 4
	return &focusRowRenderer{row: r, border: border,
		objects: []fyne.CanvasObject{border, r.content}}
}

func (r *focusRow) FocusGained()   { r.focused = true; r.Refresh() }
func (r *focusRow) FocusLost()     { r.focused = false; r.Refresh() }
func (r *focusRow) TypedRune(rune) {}

func (r *focusRow) TypedKey(e *fyne.KeyEvent) {
	switch e.Name {
	case fyne.KeyReturn, fyne.KeyEnter, fyne.KeySpace:
		if r.onSelect != nil {
			r.onSelect()
		}
	}
}

func (r *focusRow) Tapped(*fyne.PointEvent) {
	if c := fyne.CurrentApp().Driver().CanvasForObject(r); c != nil {
		c.Focus(r)
	}
	if r.onSelect != nil {
		r.onSelect()
	}
}

type focusRowRenderer struct {
	row     *focusRow
	border  *canvas.Rectangle
	objects []fyne.CanvasObject
}

func (rr *focusRowRenderer) Layout(s fyne.Size) {
	rr.border.Resize(s)
	rr.row.content.Resize(s)
	rr.row.content.Move(fyne.NewPos(0, 0))
}

func (rr *focusRowRenderer) MinSize() fyne.Size { return rr.row.content.MinSize() }

func (rr *focusRowRenderer) Refresh() {
	if rr.row.focused {
		rr.border.StrokeColor = theme.Color(theme.ColorNameFocus)
	} else {
		rr.border.StrokeColor = color.Transparent
	}
	rr.border.Refresh()
}

func (rr *focusRowRenderer) Objects() []fyne.CanvasObject { return rr.objects }
func (rr *focusRowRenderer) Destroy()                     {}
