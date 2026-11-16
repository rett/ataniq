package terminalconsole

import (
	"fmt"

	uv "github.com/charmbracelet/ultraviolet"
)

// The smallest window that gets the three-pane cockpit. A smaller one gets
// the single-column pages.
const (
	MinCols = 100
	MinRows = 30
)

// The largest area the cockpit draws. A larger window is drawn in its top left
// corner and the rest stays blank, so that no window size, however large,
// makes the console build a huge cell buffer. The session pane has its own,
// smaller limit.
const (
	maxCanvasCols = 512
	maxCanvasRows = 256
)

func sizeText(cols, rows int) string { return fmt.Sprintf("%dx%d", cols, rows) }

// Fixed heights of the blocks in the columns, in rows including the border.
const (
	metaRows    = 7
	focusRows   = 4
	contextRows = 8
)

// layout is where each block of the cockpit is drawn. All rectangles are in
// window cells.
type layout struct {
	cockpit bool
	zoom    bool
	canvas  uv.Rectangle
	header  uv.Rectangle
	keys    uv.Rectangle
	// tree, context, meta, frame, focus, evidence and timeline are the
	// blocks, border included. inner is the session pane's area.
	tree, context, meta, frame, focus, evidence, timeline uv.Rectangle
	inner                                                 uv.Rectangle
}

// layout computes the layout for the current window and zoom state.
func (m *Model) layout() layout {
	var l layout
	if m.width < MinCols || m.height < MinRows {
		return l
	}
	l.cockpit = true
	w, h := min(m.width, maxCanvasCols), min(m.height, maxCanvasRows)
	l.canvas = uv.Rect(0, 0, w, h)
	l.header = uv.Rect(0, 0, w, 1)
	if m.zoom {
		l.zoom = true
		l.inner = uv.Rect(0, 1, w, h-1)
		return l
	}
	l.keys = uv.Rect(0, h-1, w, 1)
	timelineRows := 6
	if h >= 40 {
		timelineRows = 8
	}
	l.timeline = uv.Rect(0, h-1-timelineRows, w, timelineRows)
	bodyY, bodyH := 1, h-2-timelineRows
	left := min(max(w*20/100, 22), 34)
	right := min(max(w*25/100, 28), 42)
	mid := w - left - right
	l.tree = uv.Rect(0, bodyY, left, bodyH-contextRows)
	l.context = uv.Rect(0, bodyY+bodyH-contextRows, left, contextRows)
	l.meta = uv.Rect(left, bodyY, mid, metaRows)
	l.frame = uv.Rect(left, bodyY+metaRows, mid, bodyH-metaRows-focusRows)
	l.focus = uv.Rect(left, bodyY+bodyH-focusRows, mid, focusRows)
	l.evidence = uv.Rect(left+mid, bodyY, right, bodyH)
	l.inner = inset(l.frame)
	return l
}

// inset returns r without its border.
func inset(r uv.Rectangle) uv.Rectangle {
	return uv.Rect(r.Min.X+1, r.Min.Y+1, max(0, r.Dx()-2), max(0, r.Dy()-2))
}
