package vt

import (
	uv "github.com/charmbracelet/ultraviolet"
)

// linkTable bounds the distinct hyperlinks that the cells of one screen
// reference ([MaxLiveLinks]).
//
// Every OSC 8 makes new strings for the pen's link, so a link that is sent
// again is a new copy of a link the table may already hold. The table
// therefore interns: it keeps one copy of each admitted link and [admitLink]
// returns it, and the cell is given that copy, not the pen's. What the cells
// of a screen hold of link strings is then the table's at most [MaxLiveLinks]
// copies of at most [MaxLinkURL]+[MaxLinkParams] bytes each, about 0.6 MiB
// whatever the session prints, and not one copy per cell. (Cells made by
// the API, such as [Screen.SetCell], keep the link they were given.) Cells
// that are moved, scrolled, inserted, deleted or copied by a resize or by the
// change of screen copy the cell value, which shares the strings.
//
// A link is live while a cell of the screen references it. The screen's
// cells change in many ways (print, erase, insert, delete, scroll, resize) in
// code this package does not own, so the table does not follow them one by
// one. It is exact in a different way: when it is full and a link that is not
// in it arrives, the table is recounted from the cells, which drops every link
// that no cell references any more. A recount costs one pass over the cells,
// which looks a link up in the table only where it differs from the link of
// the cell before: a run of cells under one link, which share one copy of it,
// costs the comparison of two pointers and not the hashing of 2.3 KiB.
// To keep the time bounded, a recount is made only when the table is full and
// at least one recount interval of refused cells has passed since the last
// one, so a session printing one new link per cell on a full table costs at
// most [recountCost] cells scanned per cell printed, however large the screen.
// The first time the table is full it recounts at once. A slot that frees up
// after a recount is available after at most one recount interval of refusals
// (the screen's cells divided by [recountCost]).
type linkTable struct {
	// set holds the admitted links. The value is the copy that every cell
	// printed under the link shares.
	set map[uv.Link]uv.Link
	// wait is the number of refusals that must pass before the next recount.
	wait int
	// recounts is the number of recounts made, and hashed the number of
	// links they looked up in the table, for tests.
	recounts, hashed int
}

// recountCost is the most cells scanned by recounts for each cell that was
// refused its link, on average.
const recountCost = 256

// admitLink reports whether text may be printed under link on the screen: the
// link is already in the table, or the table has room, or a recount found
// room. It returns the copy of the link that the cell must hold, which is the
// table's own, so that the cells of the screen share it. A refusal returns
// the zero link and is the caller's to count.
func (s *Screen) admitLink(link uv.Link) (uv.Link, bool) {
	t := &s.links
	if l, ok := t.set[link]; ok {
		return l, true
	}
	if len(t.set) >= MaxLiveLinks {
		if t.wait > 0 {
			t.wait--
			return uv.Link{}, false
		}
		s.recountLinks()
		if len(t.set) >= MaxLiveLinks {
			return uv.Link{}, false
		}
	}
	if t.set == nil {
		t.set = make(map[uv.Link]uv.Link, 16)
	}
	t.set[link] = link
	return link, true
}

// recountLinks rebuilds the table from the cells of the screen: the links
// that a cell references, up to the limit. A screen that holds more distinct
// links than the limit, which only the API can make, is counted as full.
func (s *Screen) recountLinks() {
	t := &s.links
	t.recounts++
	t.wait = s.Width() * s.Height() / recountCost
	if t.set == nil {
		t.set = make(map[uv.Link]uv.Link, 16)
	}
	clear(t.set)
	var prev uv.Link
	for y, line := range s.buf.Lines {
		if y < len(s.marks) {
			if c := s.marks[y].cell; c != nil && c.Link.URL == "" {
				// Every cell of the line is c, with no link.
				continue
			}
			s.flush(y)
		}
		for i := range line {
			link := line[i].Link
			if link.URL == "" || link == prev {
				continue
			}
			prev = link
			t.hashed++
			if _, ok := t.set[link]; !ok {
				t.set[link] = link
			}
			if len(t.set) >= MaxLiveLinks {
				return
			}
		}
	}
}
