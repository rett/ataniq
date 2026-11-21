package intake

import (
	"unicode"
	"unicode/utf8"
)

// OddPath says why the execution-config matcher cannot be trusted with a
// path, or returns "" for a path it can. The matcher folds ASCII case, the
// trailing dots and spaces of an element and the short name "git~1", and
// nothing else (see package execconfig), so a path that another file system
// could read as a different name is flagged here and needs its own sign-off.
// A path is odd when it:
//
//   - holds bytes that are not UTF-8;
//   - holds a control character (U+0000 to U+001F, U+007F, U+0080 to
//     U+009F) or a format character (category Cf: zero-width and direction
//     controls, the soft hyphen) or a Hangul filler;
//   - holds a colon, which names an alternate data stream on NTFS;
//   - holds "~" followed by a digit, the form of an NTFS short name;
//   - is not in Unicode normalization form C.
//
// The module has no normalization tables, so the last test is a superset of
// the real one: [mayChangeInNFC] is true for every character that NFC
// changes or that can combine with a neighbour, and for every combining
// mark. A path in form C that holds a combining mark, as paths of some
// scripts do, is flagged too; that costs a sign-off and never hides a path.
func OddPath(path string) string {
	if !utf8.ValidString(path) {
		return "bytes that are not UTF-8"
	}
	for i := 0; i < len(path); i++ {
		if path[i] == '~' && i+1 < len(path) && path[i+1] >= '0' && path[i+1] <= '9' {
			return "a ~ and a digit, the form of a short name"
		}
	}
	for _, r := range path {
		switch {
		case r == ':':
			return "a colon"
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f):
			return "a control character"
		case r == 0x3164 || r == 0xffa0 || unicode.Is(unicode.Cf, r):
			return "an invisible character"
		case mayChangeInNFC(r):
			return "not in normalization form C"
		}
	}
	return ""
}

// nfcRanges are the characters from U+0300 up that NFC rewrites or can
// combine, other than the combining marks (Mn, Mc, Me), which [mayChangeInNFC]
// takes by category: the precomposed characters that are not in form C
// (composition exclusions and singletons, with their compatibility ideograph
// blocks), the Hangul jamo that join into syllables, and the Kirat Rai block,
// whose letters compose.
var nfcRanges = [][2]rune{
	{0x0340, 0x0344}, {0x0374, 0x0374}, {0x037e, 0x037e}, {0x0387, 0x0387},
	{0x0958, 0x095f}, {0x09dc, 0x09df}, {0x0a33, 0x0a33}, {0x0a36, 0x0a36},
	{0x0a59, 0x0a5e}, {0x0b5c, 0x0b5d}, {0x0f43, 0x0f43}, {0x0f4d, 0x0f4d},
	{0x0f52, 0x0f52}, {0x0f57, 0x0f57}, {0x0f5c, 0x0f5c}, {0x0f69, 0x0f69},
	{0x0f73, 0x0f73}, {0x0f75, 0x0f76}, {0x0f78, 0x0f78}, {0x0f81, 0x0f81},
	{0x0f93, 0x0f93}, {0x0f9d, 0x0f9d}, {0x0fa2, 0x0fa2}, {0x0fa7, 0x0fa7},
	{0x0fac, 0x0fac}, {0x0fb9, 0x0fb9},
	{0x1100, 0x11ff}, {0x1f71, 0x1f71}, {0x1f73, 0x1f73}, {0x1f75, 0x1f75},
	{0x1f77, 0x1f77}, {0x1f79, 0x1f79}, {0x1f7b, 0x1f7b}, {0x1f7d, 0x1f7d},
	{0x1fbb, 0x1fbb}, {0x1fbe, 0x1fbe}, {0x1fc9, 0x1fc9}, {0x1fcb, 0x1fcb},
	{0x1fd3, 0x1fd3}, {0x1fdb, 0x1fdb}, {0x1fe3, 0x1fe3}, {0x1feb, 0x1feb},
	{0x1fee, 0x1fef}, {0x1ff9, 0x1ff9}, {0x1ffb, 0x1ffb}, {0x1ffd, 0x1ffd},
	{0x2000, 0x2001}, {0x2126, 0x2126}, {0x212a, 0x212b}, {0x2329, 0x232a},
	{0x2adc, 0x2adc},
	{0xf900, 0xfa6d}, {0xfa70, 0xfad9}, {0xfb1d, 0xfb1d}, {0xfb1f, 0xfb1f},
	{0xfb2a, 0xfb36}, {0xfb38, 0xfb3c}, {0xfb3e, 0xfb3e}, {0xfb40, 0xfb41},
	{0xfb43, 0xfb44}, {0xfb46, 0xfb4e},
	{0x16d40, 0x16d7f}, {0x1d15e, 0x1d164}, {0x1d1bb, 0x1d1c0}, {0x2f800, 0x2fa1d},
}

// mayChangeInNFC reports whether r is a combining mark or one of the
// characters of [nfcRanges]. Below U+0300 NFC changes nothing.
func mayChangeInNFC(r rune) bool {
	if r < 0x300 {
		return false
	}
	if unicode.In(r, unicode.Mn, unicode.Mc, unicode.Me) {
		return true
	}
	for _, rg := range nfcRanges {
		if r >= rg[0] && r <= rg[1] {
			return true
		}
	}
	return false
}
