package ui

import (
	"fmt"
	"image"
	"image/color"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/i18n"
	"github.com/AlgoraLabs-AI/AI-Cloud-Status/internal/providers"
)

// The fixed parts of the status badge: the hue dot and the shape icon. They are
// constants because badgeTextWidth subtracts them from the Status column to get
// what is left for the label, and a literal in one place and not the other is
// how the label came to overflow its column in the first place.
const (
	badgeDotSide  = float32(14)
	badgeIconSide = float32(16)
)

// Column indices into a row's columnsLayout grid, matching tableColWidths.
const (
	colName = iota
	colStatus
	colWhen
	colNext
	colLatency
	colUptime
	colRegions
	colIncidents
)

// rowWidget is a persistent, in-place-updatable status-table row, cached per
// check id across refreshes. Recreating every row's widget tree on each refresh
// — which runs once per connectivity round, every second by default — churned
// hundreds of canvas objects (labels, hyperlinks, raster textures) per second
// through Fyne's renderer/texture cache, growing the process by gigabytes over
// a long run. Instead, a row is built once and each refresh only touches what
// actually changed: label texts are Set in place, the sparkline raster mutates
// its paints and Refreshes, and whole cells (name, regions, incidents) are
// swapped only when their inputs change, which is rare.
type rowWidget struct {
	row  *focusRow
	grid *fyne.Container // the columnsLayout cells, indexed by the col* constants

	dot        *canvas.Circle
	badgeIcon  *widget.Icon
	badgeLabel *widget.Label
	whenLabel  *widget.Label
	countdown  *widget.Label
	latency    *widget.Label

	sparkBox    *fyne.Container // fixed-size wrap holding the raster; hidden while no samples
	spark       *canvas.Raster
	paints      []int // the raster generator reads this; mutate, then spark.Refresh()
	uptimeLabel *widget.Label
	uptimeText  string
	bucketed    bool      // provider/URL rows: time-bucketed 24h strip; conn rows: per-sample
	uptimeKey   uptimeKey // inputs of the last paint computation (see uptimeKey)

	spec       rowSpec // last rendered spec (closures are not compared, only reassigned)
	regionsSig string  // last rendered regions-cell fingerprint
	// colGen is the column-width generation the width-dependent cells (regions,
	// incidents) were last built against. The columns stretch with the window, and
	// those two cells bake their wrap width in at construction, so a cell built
	// before a resize keeps wrapping for the old width until it is rebuilt. See
	// colmodel.go.
	colGen uint64
}

// rowCtx is the per-refresh rendering context shared by every row of a section:
// the regions of interest, the muting snapshot and its fingerprint, the poll
// schedule, and the uptime window shape.
type rowCtx struct {
	interest []string
	isMuted  func(region string) bool
	muteSig  string
	nextPoll time.Time
	interval time.Duration
	window   time.Duration
	bucketed bool
	now      time.Time
}

// uptimeKey captures every input of a row's uptime cell. The cell recomputes
// only when the key changes: without this, the 1s cosmetic tick would re-scan
// the full 24h sample window (2880 samples at a 30s cadence) for every provider
// row every second — the CPU shape of the exact churn the rowWidget cache was
// built to avoid. In practice a provider row recomputes when a poll lands, a
// mute changes, or the minute-quantized bucket anchor advances.
type uptimeKey struct {
	last    time.Time
	count   int
	muteSig string
	window  time.Duration
	anchor  time.Time
}

// rowUptimeKey builds the current uptime-cell cache key for a check.
func (c *Controller) rowUptimeKey(id string, ctx rowCtx) uptimeKey {
	key := uptimeKey{muteSig: ctx.muteSig, window: ctx.window}
	key.last, key.count = c.history.Last(id)
	if ctx.bucketed {
		key.anchor = bucketAnchor(ctx.now)
	}
	return key
}

// ensureRow returns the cached row for spec.id updated to the new spec,
// creating it on first sight. Must run on the Fyne thread (refreshLocked).
// It reports whether a layout-affecting cell was swapped so the caller can
// refresh the table once per pass instead of per row.
func (c *Controller) ensureRow(spec rowSpec, ctx rowCtx) (*rowWidget, bool) {
	if rw, ok := c.rowCache[spec.id]; ok {
		return rw, rw.update(c, spec, ctx)
	}
	rw := c.newRowWidget(spec, ctx)
	c.rowCache[spec.id] = rw
	return rw, true
}

// newRowWidget builds a fresh row for spec, retaining references to every
// mutable part so later refreshes can update it in place.
func (c *Controller) newRowWidget(spec rowSpec, ctx rowCtx) *rowWidget {
	rw := &rowWidget{spec: spec, bucketed: ctx.bucketed}

	// Status badge: dot (hue) + icon (shape) + text, all kept for later updates.
	v := visualFor(spec.state)
	rw.dot = canvas.NewCircle(v.Color)
	rw.badgeIcon = widget.NewIcon(v.Icon)
	rw.badgeLabel = widget.NewLabel(v.Label)
	// The label WRAPS inside what the Status column has left. Fyne never clips a
	// label: a plain one reports its full text width as its minimum and simply
	// paints past the column boundary, which is how "Status feed unavailable"
	// came to be printed straight through the Last-checked timestamp beside it
	// ("Status feed unavaila23:40:38"). Every label longer than the column has
	// the same bug — German "Statusseite nicht verfügbar", the offline-unknown
	// state in any language — so the fix is the width, not the wording.
	rw.badgeLabel.Wrapping = fyne.TextWrapWord
	// Centered as a group: badgeLabelLayout shrinks to the text when it fits, so
	// the dot/icon/label trio is a compact block the Center container can place on
	// the column axis instead of a full-width strip pinned to the left edge.
	badge := hCentered(container.NewHBox(
		container.NewGridWrap(fyne.NewSize(badgeDotSide, badgeDotSide), rw.dot),
		container.NewGridWrap(fyne.NewSize(badgeIconSide, badgeIconSide), rw.badgeIcon),
		container.New(badgeLabelLayout{label: rw.badgeLabel, width: badgeTextWidth()}, rw.badgeLabel),
	))
	c.animateStateChange(spec.id, spec.state, rw.dot)

	rw.whenLabel = widget.NewLabelWithStyle(spec.when, fyne.TextAlignCenter, fyne.TextStyle{})
	rw.countdown = widget.NewLabelWithStyle(countdownText(ctx.nextPoll, ctx.interval), fyne.TextAlignCenter, fyne.TextStyle{})
	rw.latency = monoLabel(spec.latency)

	rw.uptimeKey = c.rowUptimeKey(spec.id, ctx)
	paints, pct := c.uptimeDisplay(spec.id, ctx.window, ctx.bucketed, ctx.isMuted, ctx.now)
	rw.paints = paints
	rw.uptimeText = pct
	rw.spark = canvas.NewRaster(func(w, h int) image.Image { return paintUptimeImage(rw.paints, w, h) })
	rw.sparkBox = container.NewGridWrap(sparkSize, rw.spark)
	rw.uptimeLabel = monoLabel(pct)
	if len(paints) == 0 {
		rw.sparkBox.Hide() // box layouts skip hidden children, so only "—" shows
	}

	rw.regionsSig = regionsSig(spec.regions, ctx.interest, ctx.isMuted)

	rw.grid = container.New(columnsLayout{tableColWidths},
		nameCell(spec.name, spec.subtitle, spec.linkURL),
		badge,
		rw.whenLabel,
		rw.countdown,
		rw.latency,
		hCentered(container.NewHBox(rw.sparkBox, rw.uptimeLabel)),
		c.regionBadges(spec.regions, ctx.interest),
		incidentsCell(spec, ctx.now),
	)
	rw.row = newFocusRow(rw.grid, spec.activate)
	// After the grid exists, for the same reason update() records it last: the
	// grid's own layout is what publishes the widths this row's cells were sized
	// against.
	rw.colGen = colGen()
	return rw
}

// update refreshes the row to the new spec, touching only what changed. It
// reports whether a cell was swapped (layout may have changed height).
func (rw *rowWidget) update(c *Controller, spec rowSpec, ctx rowCtx) bool {
	old := rw.spec
	rw.spec = spec
	// The activate/retry closures capture the poll state they were built from;
	// reassigning activate each refresh keeps the detail panel opening with the
	// CURRENT state rather than the one from row creation.
	rw.row.onSelect = spec.activate

	swapped := false
	if spec.name != old.name || spec.subtitle != old.subtitle || spec.linkURL != old.linkURL {
		rw.grid.Objects[colName] = nameCell(spec.name, spec.subtitle, spec.linkURL)
		swapped = true
	}

	if spec.state != old.state {
		v := visualFor(spec.state)
		rw.badgeIcon.SetResource(v.Icon)
		rw.badgeLabel.SetText(v.Label)
		// Set the target colour directly first: with reduced motion the animation
		// below is skipped entirely and would otherwise leave the old hue.
		rw.dot.FillColor = v.Color
		rw.dot.Refresh()
	}
	c.animateStateChange(spec.id, spec.state, rw.dot)

	if spec.when != old.when {
		rw.whenLabel.SetText(spec.when)
	}
	if txt := countdownText(ctx.nextPoll, ctx.interval); rw.countdown.Text != txt {
		rw.countdown.SetText(txt)
	}
	if spec.latency != old.latency {
		rw.latency.SetText(spec.latency)
	}

	// Recompute the uptime cell only when one of its inputs changed (new
	// sample, muting change, window/anchor advance) — see uptimeKey.
	if key := c.rowUptimeKey(spec.id, ctx); key != rw.uptimeKey {
		rw.uptimeKey = key
		paints, pct := c.uptimeDisplay(spec.id, ctx.window, rw.bucketed, ctx.isMuted, ctx.now)
		if pct != rw.uptimeText {
			rw.uptimeText = pct
			rw.uptimeLabel.SetText(pct)
		}
		if !equalPaints(paints, rw.paints) {
			rw.paints = paints
			if len(paints) == 0 {
				rw.sparkBox.Hide()
			} else {
				rw.sparkBox.Show()
				rw.spark.Refresh()
			}
		}
	}

	// A resize changes how wide these two cells may wrap, and both bake that
	// width in when they are built — so the width generation is as much an input
	// to them as their content is.
	resized := colGen() != rw.colGen

	if sig := regionsSig(spec.regions, ctx.interest, ctx.isMuted); sig != rw.regionsSig || resized {
		rw.regionsSig = sig
		rw.grid.Objects[colRegions] = c.regionBadges(spec.regions, ctx.interest)
		swapped = true
	}
	if spec.incidents != old.incidents || (spec.retry == nil) != (old.retry == nil) || resized {
		rw.grid.Objects[colIncidents] = incidentsCell(spec, ctx.now)
		swapped = true
	}
	if swapped {
		rw.grid.Refresh()
	}
	// Recorded AFTER the refresh, not before the checks above. The refresh is
	// what makes Fyne lay the grid out, and the layout is what publishes the
	// widths — so reading the generation at the top of this function records the
	// value from BEFORE this pass's own layout, leaving the row permanently one
	// generation behind and reporting a swap on the very next refresh even when
	// nothing changed. That breaks the "no change, no work" contract the whole
	// row cache is built on.
	rw.colGen = colGen()
	return swapped
}

// incidentsCell renders the incidents column: structured incident blocks with
// mixed styling when incident data is present, a plain wrapped label otherwise
// (custom URL checks), with the Retry action stacked below for error states.
// Every text fragment is selectable so it can be copied straight from the row.
func incidentsCell(spec rowSpec, now time.Time) fyne.CanvasObject {
	width := colWidth(incidentsColIndex)
	var content fyne.CanvasObject
	if len(spec.incidentData) > 0 {
		content = incidentBlocks(spec.incidentData, width, now)
	} else {
		content = wrappedCellLabel(spec.incidents, width, fyne.TextStyle{})
	}
	if spec.retry == nil {
		return content
	}
	retry := widget.NewButtonWithIcon(i18n.T().Retry, theme.ViewRefreshIcon(), spec.retry)
	retry.Importance = widget.LowImportance
	return container.NewVBox(content, hCentered(retry))
}

// wrappedCellLabel builds a selectable, word-wrapping label sized for a fixed
// cell width (wrapCellLayout reports the wrapped height by measuring, without
// Resize-in-MinSize churn). Centered, like every other cell in the table.
func wrappedCellLabel(text string, width float32, style fyne.TextStyle) fyne.CanvasObject {
	l := widget.NewLabelWithStyle(text, fyne.TextAlignCenter, style)
	l.Wrapping = fyne.TextWrapWord
	l.Selectable = true
	return container.New(wrapCellLayout{text: text, width: width, style: style}, l)
}

// fieldPrefix strips the value placeholder from a localized "Label: %s" format
// string, leaving just the bold-able field label ("Updated:").
func fieldPrefix(format string) string {
	return strings.TrimSpace(strings.ReplaceAll(format, "%s", ""))
}

// agedPrefix is fieldPrefix with the timestamp's age folded in AHEAD of the
// colon — "Started (10h 3m ago):" — so the row answers "how long ago" without
// the reader subtracting an absolute timestamp from the wall clock. The
// absolute time still follows, because "10h ago" and "12:22" answer different
// questions and both get asked.
//
// The age goes inside the prefix rather than after the value because the value
// side wraps: appended there, the parenthetical would drift to the end of a
// three-line note and stop reading as a property of the timestamp.
//
// A separator that is not a bare ":" (some catalogs use a full-width colon)
// still works — the age is inserted before the last colon-like rune, or simply
// appended when the label has none.
func agedPrefix(format string, ts, now time.Time) string {
	prefix := fieldPrefix(format)
	age := fmt.Sprintf(i18n.T().AgeAgo, formatElapsed(now.Sub(ts)))
	if i := strings.LastIndexFunc(prefix, isColon); i >= 0 {
		return prefix[:i] + " " + age + prefix[i:]
	}
	return prefix + " " + age
}

// isColon reports whether r is a colon in any of the scripts the catalogs use.
func isColon(r rune) bool { return r == ':' || r == '：' }

// prefixedLine renders a BOLD field prefix ("Updated (12m ago):") on its own
// line with the value STACKED UNDERNEATH it, both centered and both selectable.
//
// It used to put them side by side in an HBox, and that is what pushed the table
// wider than the window. The prefix took whatever it measured and the value got
// the remainder — but the remainder was floored at 60px, so a long prefix and a
// multi-sentence update note produced a cell whose MinSize EXCEEDED the incidents
// column. Fyne honours a child's MinSize, so the row grew past the column
// boundary, the table grew past the window, and a horizontal scrollbar appeared
// on a screen wide enough not to need one. Stacking removes the arithmetic
// entirely: the prefix and the value each get the full column width, so the cell
// can never be wider than its column, and a long note wraps down instead of out.
func prefixedLine(prefix, rest string, width float32) fyne.CanvasObject {
	p := widget.NewLabelWithStyle(prefix, fyne.TextAlignCenter, fyne.TextStyle{Bold: true})
	p.Selectable = true
	return container.NewVBox(p, wrappedCellLabel(strings.TrimSpace(rest), width, fyne.TextStyle{}))
}

// incidentBlocks renders the structured incidents, each fenced by an
// incidentRule: BOLD title, the severity in its strip hue, "Updated (2h 41m
// ago):" with "<time> - <note>" on the line below it (newest information first,
// bold field label), then "Started (10h 3m ago):" with its time below. Pieces
// the feed doesn't provide are omitted. Every part is centered on the column,
// like the rest of the table.
//
// The fence opens every incident and the last one is closed after the loop, so
// consecutive incidents share one rule instead of stacking two. There is no
// longer a second, dashed rule inside the block between the severity and the
// timestamps: two kinds of horizontal line in one cell made the reader work out
// which one meant "new incident".
func incidentBlocks(incidents []providers.Incident, width float32, now time.Time) fyne.CanvasObject {
	t := i18n.T()
	box := container.NewVBox()
	for _, inc := range incidents {
		label := inc.Label()
		if label == "" {
			continue
		}
		box.Add(incidentRuleLabel(fyne.TextAlignCenter))
		box.Add(wrappedCellLabel(label, width, fyne.TextStyle{Bold: true}))
		box.Add(severityLine(inc.Severity, fyne.TextAlignCenter))

		note := strings.TrimSpace(inc.Note)
		if r := []rune(note); len(r) > incidentNoteMax {
			note = strings.TrimSpace(string(r[:incidentNoteMax])) + "…"
		}
		hasDetail := !inc.Updated.IsZero() || !inc.Started.IsZero() || note != ""
		if !hasDetail {
			continue
		}
		switch {
		case !inc.Updated.IsZero() && note != "":
			box.Add(prefixedLine(agedPrefix(t.UpdatedLabel, inc.Updated, now), formatIncidentTime(inc.Updated)+" - "+note, width))
		case !inc.Updated.IsZero():
			box.Add(prefixedLine(agedPrefix(t.UpdatedLabel, inc.Updated, now), formatIncidentTime(inc.Updated), width))
		case note != "":
			box.Add(wrappedCellLabel(note, width, fyne.TextStyle{}))
		}
		if !inc.Started.IsZero() {
			box.Add(prefixedLine(agedPrefix(t.StartedLabel, inc.Started, now), formatIncidentTime(inc.Started), width))
		}
	}
	if len(box.Objects) > 0 {
		box.Add(incidentRuleLabel(fyne.TextAlignCenter)) // closes the last fence
	}
	return box
}

// regionsSig fingerprints the regions cell's visual inputs (each region plus its
// highlight and muted rendering) so the chip flow is rebuilt only when one of
// them changes.
func regionsSig(regions, interest []string, isMuted func(region string) bool) string {
	if len(regions) == 0 {
		return ""
	}
	var b strings.Builder
	for _, r := range regions {
		b.WriteString(r)
		if len(interest) > 0 && providers.MatchRegion(r, interest) {
			b.WriteByte('*')
		}
		if isMuted(r) {
			b.WriteByte('!')
		}
		b.WriteByte(';')
	}
	return b.String()
}

// equalPaints reports whether two paint slices are identical.
func equalPaints(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// sameObjects reports whether two canvas-object slices hold the same objects in
// the same order.
func sameObjects(a, b []fyne.CanvasObject) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// sectionBox is a persistent per-section group box (border + heading + rows),
// the section-level companion to rowWidget: reused across refreshes and
// mutated in place so no containers are recreated on the per-second tick.
type sectionBox struct {
	box     fyne.CanvasObject // the bordered stack placed in the table
	heading *widget.Label
	inner   *fyne.Container // VBox: heading + rows
	rows    []fyne.CanvasObject
	title   string
}

// ensureSectionBox returns the cached group box for a section key with its
// title and row set brought up to date, creating it on first sight.
func (c *Controller) ensureSectionBox(key int, title string, rows []fyne.CanvasObject) fyne.CanvasObject {
	sb, ok := c.sectionBoxes[key]
	if !ok {
		heading := widget.NewLabelWithStyle(title, fyne.TextAlignCenter, fyne.TextStyle{Bold: true})
		inner := container.NewVBox(append([]fyne.CanvasObject{heading}, rows...)...)
		border := canvas.NewRectangle(color.Transparent)
		border.StrokeColor = theme.Color(theme.ColorNameInputBorder)
		border.StrokeWidth = 1
		border.CornerRadius = 6
		sb = &sectionBox{box: container.NewStack(border, inner), heading: heading, inner: inner, rows: rows, title: title}
		c.sectionBoxes[key] = sb
		return sb.box
	}
	if title != sb.title {
		sb.title = title
		sb.heading.SetText(title)
	}
	if !sameObjects(sb.rows, rows) {
		sb.rows = rows
		sb.inner.Objects = append([]fyne.CanvasObject{sb.heading}, rows...)
		sb.inner.Refresh()
	}
	return sb.box
}
