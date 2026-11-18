package sessionpane

import (
	"crypto/sha256"
	"encoding/binary"
	"hash"
	"image/color"

	uv "github.com/charmbracelet/ultraviolet"
)

// digestDomain starts every digest, so that a digest of another kind of data
// never equals one of a pane.
const digestDomain = "ataniq sessionpane digest v2\n"

// Digest returns the SHA-256 of the pane's real state: what a viewer sees of
// it and what decides what the next bytes do. It reads the emulator, never
// the frame held for synchronized output. Two panes that received the same
// bytes in the same chunks, or one that was restored from the other's
// snapshot and then received the same bytes, have the same digest.
//
// The bytes hashed, in this order, with every integer a 32 bit big endian
// number unless said otherwise, and a string its byte length and the bytes:
//
//  1. the bytes "ataniq sessionpane digest v2" and a line feed, as they are;
//  2. the columns and the rows;
//  3. every cell of the active screen, row by row: the width, the content
//     (a string), the style, the link;
//     a style is the attributes byte, the underline byte, and the foreground,
//     background and underline colours, each a tag byte (0 none, 1 basic, 2
//     indexed, 3 RGBA, 4 CMYK, 5 alpha, 255 another kind) and its bytes as the
//     snapshot has them (for another kind, the four 16 bit values of its RGBA
//     method); a link is a number, 0 for none, else 1 and up: the links of
//     the screen are numbered in the order of their first use in the cells, as
//     they are read, so that a link is hashed once and not once for every
//     cell under it. A link with no URL is no link;
//  4. the links: their number, then each in the order of its number, the URL
//     and the parameters (strings);
//  5. the cursor: column, row, a byte 1 when visible else 0, the shape, a byte
//     1 when it blinks else 0;
//  6. the modes that are set (see ModeSet): the number of ANSI modes and each
//     of them ascending, then the number of DEC private modes and each;
//  7. the title (a string).
//
// The encoding is prefix-free: the cells are a fixed grid, a string has its
// length before it, a link in a cell is a number of a fixed length that the
// list after the cells explains, and a list has its length before it.
func (p *Pane) Digest() [32]byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	h := &digestWriter{h: sha256.New()}
	h.raw([]byte(digestDomain))
	h.u32(p.cols)
	h.u32(p.rows)
	// The links in the order of their first use, each hashed once after the
	// cells: a screen whose cells are under links of 2 KiB would otherwise
	// hash megabytes, under the pane's lock.
	links := map[uv.Link]int{}
	var order []uv.Link
	var prev uv.Link
	prevN := 0
	for y := 0; y < p.rows; y++ {
		for x := 0; x < p.cols; x++ {
			c := p.emu.CellAt(x, y)
			if c == nil {
				c = &uv.Cell{}
			}
			h.u32(c.Width)
			h.str(c.Content)
			h.style(&c.Style)
			l := normLink(c.Link)
			switch {
			case l.URL == "":
				prevN = 0
			case l != prev:
				n, ok := links[l]
				if !ok {
					order = append(order, l)
					n = len(order)
					links[l] = n
				}
				prevN = n
			}
			prev = l
			h.u32(prevN)
		}
	}
	h.u32(len(order))
	for _, l := range order {
		h.str(l.URL)
		h.str(l.Params)
	}
	pos := p.emu.CursorPosition()
	h.u32(pos.X)
	h.u32(pos.Y)
	shape, blink := p.emu.CursorStyle()
	h.byte(b2u(!p.emu.CursorHidden()))
	h.u32(int(shape))
	h.byte(b2u(blink))
	modes := p.modeSet()
	h.u32(len(modes.ANSI))
	for _, m := range modes.ANSI {
		h.u32(m)
	}
	h.u32(len(modes.DEC))
	for _, m := range modes.DEC {
		h.u32(m)
	}
	h.str(p.title)
	return h.sum()
}

// digestWriter buffers what it hashes, so that the 100000 cells of a large
// screen cost few calls into the hash.
type digestWriter struct {
	h   hash.Hash
	buf [4096]byte
	n   int
}

func (w *digestWriter) raw(b []byte) {
	for len(b) > 0 {
		if w.n == len(w.buf) {
			w.flush()
		}
		c := copy(w.buf[w.n:], b)
		w.n += c
		b = b[c:]
	}
}

func (w *digestWriter) flush() {
	w.h.Write(w.buf[:w.n])
	w.n = 0
}

func (w *digestWriter) byte(v byte) { w.raw([]byte{v}) }

func (w *digestWriter) u32(v int) {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], uint32(v))
	w.raw(b[:])
}

func (w *digestWriter) str(s string) {
	w.u32(len(s))
	for len(s) > 0 {
		if w.n == len(w.buf) {
			w.flush()
		}
		c := copy(w.buf[w.n:], s)
		w.n += c
		s = s[c:]
	}
}

func (w *digestWriter) style(s *uv.Style) {
	var buf [styleBytes + 3*4]byte
	b := append(buf[:0], s.Attrs, byte(s.Underline))
	for _, c := range [3]color.Color{s.Fg, s.Bg, s.UnderlineColor} {
		var ok bool
		if b, ok = appendColour(b, c); !ok {
			r, g, bl, a := c.RGBA()
			b = append(b, 255)
			for _, v := range [4]uint32{r, g, bl, a} {
				b = append(b, byte(v>>8), byte(v))
			}
		}
	}
	w.raw(b)
}

func (w *digestWriter) sum() [32]byte {
	w.flush()
	var out [32]byte
	copy(out[:], w.h.Sum(nil))
	return out
}
