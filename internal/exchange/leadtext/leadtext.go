package leadtext

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Label names text that came from a lead. Show it next to escaped lead text.
const Label = "unverified lead data"

const hexLower = "0123456789abcdef"
const hexUpper = "0123456789ABCDEF"

// Escape returns s as one line of safe text.
//
// Printable ASCII passes, except the backslash, which becomes two backslashes.
// Newline, carriage return and tab become \n, \r and \t. Every other byte
// below 0x20, and 0x7f, becomes \xNN. A byte that is not valid UTF-8 also
// becomes \xNN. A valid non-ASCII rune passes only if it is printable and not
// dangerous. The dangerous ones are C1 controls, bidirectional controls,
// zero-width and other invisible format characters, variation selectors and
// every other default-ignorable code point, line and paragraph separators,
// invisible filler letters, private use and unassigned code points. They
// become \u{HEX}.
//
// The escaping prefers an unambiguous line over a pretty one. An emoji with a
// variation selector shows the selector as escape text.
//
// The result has no byte below 0x20 or equal to 0x7f, and Unescape returns
// the original text.
func Escape(s string) string {
	if plain(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + len(s)/4 + 8)
	buf := make([]byte, 0, 16)
	for i := 0; i < len(s); {
		var n int
		buf, n = appendUnit(buf[:0], s[i:], false)
		b.Write(buf)
		i += n
	}
	return b.String()
}

// plain reports whether Escape would return s unchanged.
func plain(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 || c >= 0x7f || c == '\\' {
			return false
		}
	}
	return true
}

// Line escapes s and cuts the result to at most max bytes. It never splits an
// escape or a rune. A negative max is treated as zero. The second result says
// whether anything was dropped.
func Line(s string, max int) (out string, cut bool) {
	if max < 0 {
		max = 0
	}
	if plain(s) {
		if len(s) <= max {
			return s, false
		}
		return s[:max], true
	}
	var b strings.Builder
	buf := make([]byte, 0, 16)
	for i := 0; i < len(s); {
		var n int
		buf, n = appendUnit(buf[:0], s[i:], false)
		if b.Len()+len(buf) > max {
			return b.String(), true
		}
		b.Write(buf)
		i += n
	}
	return b.String(), false
}

// appendUnit appends the escaped form of the first rune or invalid byte of s
// to dst. It returns dst and the number of bytes of s it consumed. With
// stream set, newline and tab pass as they are.
func appendUnit(dst []byte, s string, stream bool) ([]byte, int) {
	c := s[0]
	if c < utf8.RuneSelf {
		switch {
		case c == '\n':
			if stream {
				return append(dst, '\n'), 1
			}
			return append(dst, '\\', 'n'), 1
		case c == '\t':
			if stream {
				return append(dst, '\t'), 1
			}
			return append(dst, '\\', 't'), 1
		case c == '\r':
			return append(dst, '\\', 'r'), 1
		case c == '\\':
			return append(dst, '\\', '\\'), 1
		case c < 0x20 || c == 0x7f:
			return appendHexByte(dst, c), 1
		}
		return append(dst, c), 1
	}
	r, size := utf8.DecodeRuneInString(s)
	if r == utf8.RuneError && size <= 1 {
		return appendHexByte(dst, c), 1
	}
	if safeRune(r) {
		return append(dst, s[:size]...), size
	}
	dst = append(dst, '\\', 'u', '{')
	started := false
	for shift := 20; shift >= 0; shift -= 4 {
		d := byte(r>>uint(shift)) & 0xf
		if d != 0 || started || shift == 0 {
			dst = append(dst, hexUpper[d])
			started = true
		}
	}
	return append(dst, '}'), size
}

func appendHexByte(dst []byte, c byte) []byte {
	return append(dst, '\\', 'x', hexLower[c>>4], hexLower[c&0xf])
}

// safeRune reports whether a valid non-ASCII rune may pass as it is.
func safeRune(r rune) bool {
	if !unicode.IsPrint(r) {
		return false
	}
	if unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Co, r) {
		return false
	}
	switch {
	case r >= 0x80 && r <= 0x9f: // C1 controls
		return false
	case r == 0x061c, r == 0x200e, r == 0x200f: // bidirectional marks
		return false
	case r >= 0x202a && r <= 0x202e: // bidirectional embeddings and overrides
		return false
	case r >= 0x2066 && r <= 0x2069: // bidirectional isolates
		return false
	case r >= 0x200b && r <= 0x200d, r == 0x2060, r == 0xfeff: // zero width
		return false
	case r == 0x2028, r == 0x2029: // line and paragraph separators
		return false
	case unicode.Is(unicode.Variation_Selector, r), unicode.Is(unicode.Other_Default_Ignorable_Code_Point, r):
		// Invisible: they print as nothing, so they make two names look the
		// same. This covers the variation selectors, the combining grapheme
		// joiner U+034F, the Hangul fillers, the Khmer invisible vowels
		// U+17B4 and U+17B5, U+2065, U+FFF0 to U+FFF8 and the unassigned
		// and tag-area code points from U+E0000 to U+E0FFF.
		return false
	case r == 0x034f: // combining grapheme joiner, invisible
		return false
	case r == 0x115f, r == 0x1160, r == 0x3164, r == 0xffa0: // invisible Hangul fillers
		return false
	}
	return true
}

// Unescape reverses Escape. It reads \\, \n, \r, \t, \xNN and \u{HEX}. Any
// other backslash sequence, and a backslash at the end, is an error. Text
// other than a backslash passes as it is.
func Unescape(s string) (string, error) {
	if !strings.Contains(s, `\`) {
		return s, nil
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		c := s[i]
		if c != '\\' {
			b.WriteByte(c)
			i++
			continue
		}
		if i+1 >= len(s) {
			return "", fmt.Errorf("leadtext: backslash at end of text (offset %d)", i)
		}
		switch s[i+1] {
		case '\\':
			b.WriteByte('\\')
			i += 2
		case 'n':
			b.WriteByte('\n')
			i += 2
		case 'r':
			b.WriteByte('\r')
			i += 2
		case 't':
			b.WriteByte('\t')
			i += 2
		case 'x':
			if i+4 > len(s) {
				return "", fmt.Errorf("leadtext: short \\x escape (offset %d)", i)
			}
			hi, ok1 := hexVal(s[i+2])
			lo, ok2 := hexVal(s[i+3])
			if !ok1 || !ok2 {
				return "", fmt.Errorf("leadtext: bad \\x escape (offset %d)", i)
			}
			b.WriteByte(hi<<4 | lo)
			i += 4
		case 'u':
			r, n, err := parseRune(s[i:])
			if err != nil {
				return "", fmt.Errorf("%w (offset %d)", err, i)
			}
			b.WriteRune(r)
			i += n
		default:
			return "", fmt.Errorf("leadtext: unknown escape \\%c (offset %d)", s[i+1], i)
		}
	}
	return b.String(), nil
}

// parseRune reads \u{HEX} at the start of s and returns the rune and the
// length of the escape.
func parseRune(s string) (rune, int, error) {
	bad := errors.New("leadtext: bad \\u escape")
	if len(s) < 4 || s[2] != '{' {
		return 0, 0, bad
	}
	var r rune
	digits := 0
	for i := 3; i < len(s); i++ {
		if s[i] == '}' {
			if digits == 0 {
				return 0, 0, bad
			}
			if r > unicode.MaxRune || (r >= 0xd800 && r <= 0xdfff) {
				return 0, 0, errors.New("leadtext: \\u escape is not a code point")
			}
			return r, i + 1, nil
		}
		v, ok := hexVal(s[i])
		if !ok || digits >= 6 {
			return 0, 0, bad
		}
		r = r<<4 | rune(v)
		digits++
	}
	return 0, 0, bad
}

func hexVal(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// Writer cleans a stream of lead text on its way to another writer. Newline
// and tab pass as they are. Everything else is escaped as Escape does it,
// except that a carriage return becomes the text \r. A Writer is not safe for
// concurrent use.
type Writer struct {
	w       io.Writer
	pending []byte // the start of a rune that is not complete yet, at most 3 bytes
	out     []byte
	err     error
	closed  bool
}

// NewWriter returns a Writer that writes cleaned text to w. Call Close when
// the stream ends. Close does not close w.
func NewWriter(w io.Writer) *Writer {
	return &Writer{w: w}
}

// Write cleans p and writes it to the wrapped writer. A rune split across
// calls is held back until it is complete. Write reports the number of bytes
// of p it took, not the number it wrote. When the wrapped writer fails, Write
// returns 0 and its error, although it may have taken some of p into the
// held part of a rune. The error is final: every later call returns it, so
// the caller must not retry the same bytes.
func (w *Writer) Write(p []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	if w.closed {
		return 0, io.ErrClosedPipe
	}
	if len(p) == 0 {
		return 0, nil
	}
	var in string
	if len(w.pending) > 0 {
		in = string(w.pending) + string(p)
	} else {
		in = string(p)
	}
	w.out = w.out[:0]
	i := 0
	for i < len(in) {
		if !utf8.FullRuneInString(in[i:]) {
			break
		}
		var n int
		w.out, n = appendUnit(w.out, in[i:], true)
		i += n
	}
	w.pending = append(w.pending[:0], in[i:]...)
	if err := w.flush(); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Close writes any held partial rune as escaped bytes. It does not close the
// wrapped writer. Calling it again does nothing.
func (w *Writer) Close() error {
	if w.closed {
		return w.err
	}
	w.closed = true
	if w.err != nil {
		return w.err
	}
	w.out = w.out[:0]
	for _, c := range w.pending {
		w.out = appendHexByte(w.out, c)
	}
	w.pending = w.pending[:0]
	return w.flush()
}

func (w *Writer) flush() error {
	if len(w.out) == 0 {
		return nil
	}
	n, err := w.w.Write(w.out)
	if err == nil && n < len(w.out) {
		err = io.ErrShortWrite
	}
	if err != nil {
		w.err = err
	}
	return err
}
