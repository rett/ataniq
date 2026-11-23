package credkind

import (
	"bytes"
	"unicode/utf8"
)

// This file reads the CBOR of the authenticator data strictly: definite
// lengths only, every integer and length in its shortest form, text that is
// valid UTF-8, no tag, no float and no simple value but false, true, null and
// undefined, and the keys of every map in the canonical order of CTAP2
// (major type, then encoded length, then bytes), which also forbids a
// duplicate key. The reader is bounded in depth and in the number of items.

// The bounds of the reader.
const (
	maxCBORDepth = 8
	maxCBORItems = 64
)

// CBOR major types.
const (
	cborUint  = 0
	cborNint  = 1
	cborBytes = 2
	cborText  = 3
	cborArray = 4
	cborMap   = 5
	cborTag   = 6
	cborOther = 7
)

// cborReader reads from b, starting at pos.
type cborReader struct {
	b   []byte
	pos int
}

// head reads an initial byte and its argument. It refuses a reserved or
// indefinite additional information, an argument not in its shortest form,
// a tag, and every value of major type 7 but false, true, null and undefined.
func (r *cborReader) head() (major byte, arg uint64, ok bool) {
	if r.pos >= len(r.b) {
		return 0, 0, false
	}
	ib := r.b[r.pos]
	major, ai := ib>>5, ib&0x1f
	r.pos++
	if major == cborTag {
		return 0, 0, false
	}
	if major == cborOther {
		// 20..23: false, true, null, undefined. Floats (25..27), the
		// two-byte simple values (24) and the unassigned ones are refused.
		return major, uint64(ai), ai >= 20 && ai <= 23
	}
	switch {
	case ai < 24:
		return major, uint64(ai), true
	case ai > 27:
		return 0, 0, false
	}
	n := 1 << (ai - 24)
	if len(r.b)-r.pos < n {
		return 0, 0, false
	}
	for i := 0; i < n; i++ {
		arg = arg<<8 | uint64(r.b[r.pos+i])
	}
	r.pos += n
	if arg < [...]uint64{24, 1 << 8, 1 << 16, 1 << 32}[ai-24] {
		return 0, 0, false
	}
	return major, arg, true
}

// take returns the next n bytes.
func (r *cborReader) take(n uint64) ([]byte, bool) {
	if n > uint64(len(r.b)-r.pos) {
		return nil, false
	}
	s := r.b[r.pos : r.pos+int(n)]
	r.pos += int(n)
	return s, true
}

// item reads one item and everything inside it; items counts down.
func (r *cborReader) item(depth int, items *int) bool {
	if depth > maxCBORDepth || *items <= 0 {
		return false
	}
	*items--
	major, arg, ok := r.head()
	if !ok {
		return false
	}
	switch major {
	case cborUint, cborNint, cborOther:
		return true
	case cborBytes:
		_, ok := r.take(arg)
		return ok
	case cborText:
		s, ok := r.take(arg)
		return ok && utf8.Valid(s)
	case cborArray:
		if arg > uint64(len(r.b)-r.pos) { // every item takes a byte
			return false
		}
		for i := uint64(0); i < arg; i++ {
			if !r.item(depth+1, items) {
				return false
			}
		}
		return true
	case cborMap:
		return r.mapBody(arg, depth, items, false)
	}
	return false
}

// mapBody reads n key and value pairs, the keys in canonical order. With
// textKeys every key must be a text string.
func (r *cborReader) mapBody(n uint64, depth int, items *int, textKeys bool) bool {
	if n > uint64(len(r.b)-r.pos)/2 { // every pair takes two bytes
		return false
	}
	var prev []byte
	for i := uint64(0); i < n; i++ {
		start := r.pos
		if textKeys && (start >= len(r.b) || r.b[start]>>5 != cborText) {
			return false
		}
		if !r.item(depth+1, items) {
			return false
		}
		key := r.b[start:r.pos]
		if prev != nil && !canonicalLess(prev, key) {
			return false
		}
		prev = key
		if !r.item(depth+1, items) {
			return false
		}
	}
	return true
}

// canonicalLess reports whether the encoded key a sorts before b in the
// canonical CBOR of CTAP2: lower major type first, then the shorter encoding,
// then the lower bytes.
func canonicalLess(a, b []byte) bool {
	if a[0]>>5 != b[0]>>5 {
		return a[0]>>5 < b[0]>>5
	}
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	return bytes.Compare(a, b) < 0
}

// extensionsOK reports whether b is exactly one CBOR map whose keys are text
// strings (extension identifiers), all of it strict: the extensions of the
// authenticator data.
func extensionsOK(b []byte) bool {
	r := &cborReader{b: b}
	major, n, ok := r.head()
	if !ok || major != cborMap {
		return false
	}
	items := maxCBORItems
	return r.mapBody(n, 0, &items, true) && r.pos == len(b)
}

// intIs reads one integer item and reports whether it is want, and whether
// it was an integer.
func (r *cborReader) intIs(want int64) (equal, isInt bool) {
	major, arg, ok := r.head()
	if !ok || (major != cborUint && major != cborNint) || arg > 1<<62 {
		return false, false
	}
	if major == cborUint {
		return int64(arg) == want, true
	}
	return -1-int64(arg) == want, true
}
