package sessionpane

import (
	"slices"

	uv "github.com/charmbracelet/ultraviolet"
)

// RecentRowLimit bounds the main-screen scrollback of an opted-in client.
// Snapshots and digests contain only terminal state.
const RecentRowLimit = 64

type recentLines []uv.Line

func (ls recentLines) CellAt(x, y int) *uv.Cell { return ls[y].At(x) }

type recentPage struct {
	lines  recentLines
	number int // one is the page immediately before the live screen
}

func (r recentPage) count(height int) int {
	return (len(r.lines) + height - 1) / height
}

// PageRecent moves one page back (direction -1) or forward (+1). Zero
// returns directly to live output. Height is the number of visible terminal
// rows. It reports whether the viewport is displaying history.
func (p *Pane) PageRecent(direction, height int) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if direction == 0 {
		p.recent = recentPage{}
		return false
	}
	height = min(height, MaxRows)
	if height <= 0 {
		return p.recent.number != 0
	}
	r := &p.recent
	switch direction {
	case -1:
		if r.number == 0 {
			// A synchronized update may already have scrolled rows that
			// are still on the held frame. Wait until that frame is over.
			if p.holding(p.clock()) || p.emu.ScrollbackLen() == 0 {
				return false
			}
			// x/vt clones each row when it scrolls. The row is immutable,
			// but the list is reused as older rows are evicted.
			r.lines = slices.Clone(p.emu.Scrollback().Lines())
		}
		r.number = min(r.number+1, r.count(height))
	case 1:
		r.number = max(0, min(r.number, r.count(height))-1)
	}
	if r.number == 0 {
		r.lines = nil
	}
	return r.number != 0
}

// DrawViewport draws the selected recent page, or the same live cells as
// Draw while following output. It returns the page number and count, both
// zero while live. Historical cells follow the same clipping, width and
// hyperlink rules as live cells.
func (p *Pane) DrawViewport(dst Canvas, area uv.Rectangle) (page, pages int) {
	area = area.Intersect(dst.Bounds())
	if area.Empty() {
		return 0, 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	r := &p.recent
	if r.number == 0 {
		src, cols, rows := p.source()
		p.draw(dst, area, src, cols, rows)
		return 0, 0
	}
	height := min(area.Dy(), MaxRows)
	pages = r.count(height)
	r.number = min(r.number, pages)
	end := len(r.lines) - (r.number-1)*height
	lines := r.lines[max(0, end-height):end]
	p.draw(dst, area, lines, MaxCols, len(lines))
	return r.number, pages
}
