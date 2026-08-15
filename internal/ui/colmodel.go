package ui

import "sync"

// The effective column widths, shared between the layout that COMPUTES them and
// the cells that have to WRAP TEXT to them.
//
// Two kinds of cell cannot be laid out from their content alone: the incidents
// note and the region chips both need to know their column's width in advance,
// because wrapping is what decides their HEIGHT, and Fyne asks for a minimum
// height before it has told anyone how wide they will be. While the columns were
// fixed that was easy — the width was a constant. Once the columns stretch to
// fill the window it stops being one.
//
// So columnsLayout publishes what it worked out, and the cell builders read it.
// The generation counter is what makes it safe: a cell built against an old
// width would wrap at the wrong place, so rowWidget compares generations on each
// refresh and rebuilds those cells when the number moved (see rowWidget.update).
// Without it a resize would leave every incident note wrapped for the previous
// window size until its text happened to change.
var columnModel struct {
	mu    sync.RWMutex
	width []float32
	gen   uint64
}

// publishColumnWidths records the widths a layout pass just resolved. It is
// called from Layout, which Fyne runs on its own thread, so it locks; the
// generation only moves when a width actually changed, so a steady window
// produces no churn and never forces a rebuild.
func publishColumnWidths(widths []float32) {
	columnModel.mu.Lock()
	defer columnModel.mu.Unlock()
	if len(widths) == len(columnModel.width) {
		same := true
		for i, w := range widths {
			// Sub-pixel drift is not a real change. Rebuilding cells for it would
			// churn the widget tree on every layout pass for no visible gain.
			if d := w - columnModel.width[i]; d > 0.5 || d < -0.5 {
				same = false
				break
			}
		}
		if same {
			return
		}
	}
	columnModel.width = append(columnModel.width[:0:0], widths...)
	columnModel.gen++
}

// colWidth returns the current effective width of column i, falling back to its
// configured minimum before the first layout pass has run — which is exactly
// what a cell built during startup should assume.
func colWidth(i int) float32 {
	columnModel.mu.RLock()
	defer columnModel.mu.RUnlock()
	if i < len(columnModel.width) {
		return columnModel.width[i]
	}
	if i < len(tableColWidths) {
		return tableColWidths[i]
	}
	return 100
}

// colGen returns the current width generation. A cell that wraps text records
// this when it is built and is rebuilt when it changes.
func colGen() uint64 {
	columnModel.mu.RLock()
	defer columnModel.mu.RUnlock()
	return columnModel.gen
}
