package sessionpane_test

// snapSpec is a second, independent writer of the snapshot format, written
// from the format's description and not from the encoder: the tests make a
// snapshot from it, change one field, and see what Restore says. A snapshot
// from the encoder of a blank pane is byte for byte the spec's.

type specMode struct {
	kind    byte // 0 ANSI, 1 DEC
	n       int
	setting byte
}

type specCursor struct {
	x, y, shape int
	flags       byte
	pen         int
	url, params string
}

type specCell struct {
	run                int
	content            string
	width, style, link int
}

type specScreen struct {
	scroll        [4]int
	cursor, saved specCursor
	links         [][2]string
	wait          int
	cells         []specCell
}

type specOpen struct {
	x, y, single int
	capped       byte
	text         string
}

type snapSpec struct {
	version    byte
	cols, rows int
	flags      byte
	gl, gr, ss int
	charsets   [4]byte
	lastChar   int
	colours    [3][]uint16 // nil, or 4 values
	modes      []specMode
	tabs       []byte
	open       *specOpen
	title      string
	styles     [][]byte
	styleCount int // when not 0, the count that is written, not len(styles)
	screens    [2]specScreen
	trailing   []byte
}

// blankSpec is the snapshot of a new pane of that size.
func blankSpec(cols, rows int) *snapSpec {
	s := &snapSpec{version: 1, cols: cols, rows: rows, gr: 0}
	for _, m := range []specMode{
		{0, 20, 2},
		{1, 1, 2}, {1, 6, 2}, {1, 7, 1}, {1, 9, 2}, {1, 25, 1}, {1, 66, 2}, {1, 69, 2},
		{1, 1000, 2}, {1, 1001, 2}, {1, 1002, 2}, {1, 1003, 2}, {1, 1004, 2}, {1, 1006, 2},
		{1, 1047, 2}, {1, 1048, 2}, {1, 1049, 2}, {1, 2004, 2},
	} {
		s.modes = append(s.modes, m)
	}
	s.tabs = make([]byte, (cols+7)/8)
	for i := range s.tabs {
		if i*8 < cols {
			s.tabs[i] = 1 // columns 0, 8, 16, ...
		}
	}
	for i := range s.screens {
		s.screens[i] = specScreen{
			scroll: [4]int{0, 0, cols, rows},
			cells:  []specCell{{run: cols * rows, content: " ", width: 1}},
		}
	}
	return s
}

func (s *snapSpec) bytes() []byte {
	var b []byte
	uv := func(v int) {
		u := uint64(v)
		for u >= 0x80 {
			b = append(b, byte(u)|0x80)
			u >>= 7
		}
		b = append(b, byte(u))
	}
	str := func(v string) { uv(len(v)); b = append(b, v...) }
	b = append(b, s.version)
	uv(s.cols)
	uv(s.rows)
	b = append(b, s.flags)
	uv(s.gl)
	uv(s.gr)
	uv(s.ss)
	b = append(b, s.charsets[:]...)
	uv(s.lastChar)
	for _, c := range s.colours {
		if c == nil {
			b = append(b, 0)
			continue
		}
		b = append(b, 1)
		for _, v := range c {
			b = append(b, byte(v>>8), byte(v))
		}
	}
	uv(len(s.modes))
	for _, m := range s.modes {
		b = append(b, m.kind)
		uv(m.n)
		b = append(b, m.setting)
	}
	b = append(b, s.tabs...)
	if o := s.open; o != nil {
		b = append(b, 1)
		uv(o.x)
		uv(o.y)
		uv(o.single)
		b = append(b, o.capped)
		str(o.text)
	} else {
		b = append(b, 0)
	}
	str(s.title)
	if s.styleCount != 0 {
		uv(s.styleCount)
	} else {
		uv(len(s.styles))
	}
	for _, st := range s.styles {
		b = append(b, st...)
	}
	for _, sc := range s.screens {
		for _, v := range sc.scroll {
			uv(v)
		}
		for _, c := range []specCursor{sc.cursor, sc.saved} {
			uv(c.x)
			uv(c.y)
			uv(c.shape)
			b = append(b, c.flags)
			uv(c.pen)
			str(c.url)
			str(c.params)
		}
		uv(len(sc.links))
		for _, l := range sc.links {
			str(l[0])
			str(l[1])
		}
		uv(sc.wait)
		for _, c := range sc.cells {
			uv(c.run)
			str(c.content)
			uv(c.width)
			uv(c.style)
			uv(c.link)
		}
	}
	return append(b, s.trailing...)
}
