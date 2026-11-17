package vt

import (
	uv "github.com/charmbracelet/ultraviolet"
)

// linkTable bounds the distinct hyperlinks that the cells of one screen
// reference ([MaxLiveLinks]).
//
// Every cell copies the pen's link, and the strings are shared, so what a
// screen can hold is the number of distinct links, not the number of cells.
// The table holds the links that were admitted: at most [MaxLiveLinks]
// entries of at most [MaxLinkURL]+[MaxLinkParams] bytes each, about 0.6 MiB
// whatever the session prints.
//
// A link is live while a cell of the screen references it. The screen's
// cells change in many ways (print, erase, insert, delete, scroll, resize) in
// code this package does not own, so the table does not follow them one by
// one. It is exact in a different way: when it is full and a link that is not
// in it arrives, the table is recounted from the cells, which drops every link
// that no cell references any more. A recount costs one pass over the cells.
// To keep the time bounded, a recount is made only when the table is full and
// at least one recount interval of refused cells has passed since the last
// one, so a session printing one new link per cell on a full table costs at
// most [recountCost] cells scanned per cell printed, however large the screen.
// The first time the table is full it recounts at once. A slot that frees up
// after a recount is available after at most one recount interval of refusals
// (the screen's cells divided by [recountCost]).
type linkTable struct {
	set map[uv.Link]struct{}
	// wait is the number of refusals that must pass before the next recount.
	wait int
	// recounts is the number of recounts made, for tests.
	recounts int
}

// recountCost is the most cells scanned by recounts for each cell that was
// refused its link, on average.
const recountCost = 256

// admitLink reports whether text may be printed under link on the screen: the
// link is already in the table, or the table has room, or a recount found
// room. A refusal is the caller's to count.
func (s *Screen) admitLink(link uv.Link) bool {
	t := &s.links
	if _, ok := t.set[link]; ok {
		return true
	}
	if len(t.set) >= MaxLiveLinks {
		if t.wait > 0 {
			t.wait--
			return false
		}
		s.recountLinks()
		if len(t.set) >= MaxLiveLinks {
			return false
		}
	}
	if t.set == nil {
		t.set = make(map[uv.Link]struct{}, 16)
	}
	t.set[link] = struct{}{}
	return true
}

// recountLinks rebuilds the table from the cells of the screen: the links
// that a cell references, up to the limit. A screen that holds more distinct
// links than the limit, which only the API can make, is counted as full.
func (s *Screen) recountLinks() {
	t := &s.links
	t.recounts++
	t.wait = s.Width() * s.Height() / recountCost
	if t.set == nil {
		t.set = make(map[uv.Link]struct{}, 16)
	}
	clear(t.set)
	for _, line := range s.buf.Lines {
		for i := range line {
			link := line[i].Link
			if link.URL == "" {
				continue
			}
			t.set[link] = struct{}{}
			if len(t.set) >= MaxLiveLinks {
				return
			}
		}
	}
}
