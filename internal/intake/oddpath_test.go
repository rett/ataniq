package intake

import "testing"

func TestOddPath(t *testing.T) {
	odd := map[string]string{
		"PROGRA~1/x.exe":       "short name",
		"a/B~12":               "short name",
		"git~1/config":         "short name",
		"x~":                   "", // a tilde with no digit after it is a plain name
		"~tmp":                 "",
		"a~b~":                 "",
		"file:stream":          "colon",
		"c:/x":                 "colon",
		"tab\there":            "control",
		"nl\nname":             "control",
		"nul\x00name":          "control",
		"del\x7fname":          "control",
		"c1\u0085name":         "control",
		"bad\xffutf8":          "utf8",
		"cut\xe2\x82":          "utf8",
		"zero\u200bwidth":      "invisible",
		"bidi\u202ename":       "invisible",
		"soft\u00adhyphen":     "invisible",
		"bom\ufeffname":        "invisible",
		"fill\u3164er":         "invisible",
		"e\u0301.txt":          "nfc", // e and a combining acute
		"\u212bngstrom":        "nfc", // the angstrom sign, which NFC rewrites
		"\u2126":               "nfc",
		"\ufb1d":               "nfc",
		"\uf900":               "nfc",
		"\u0958":               "nfc",
		"\U00016d67\U00016d67": "nfc", // Kirat Rai letters that compose
		"\u1100\u1161":         "nfc", // Hangul jamo that join
		"ohm\u03a9":            "",    // the Greek capital omega is form C
		"caf\u00e9":            "",    // precomposed e acute is form C
		"\u00c5ngstr\u00f6m":   "",
		"\u65e5\u672c\u8a9e":   "",
		"\ud55c\uae00.txt":     "", // Hangul syllables are form C
		"plain/path-1_2.go":    "",
		".claude/settings":     "",
		"dir/with space.txt":   "",
		"tilde~in~name/f.txt":  "",
	}
	for path, kind := range odd {
		got := OddPath(path)
		if (got != "") != (kind != "") {
			t.Errorf("OddPath(%q) = %q, want odd=%v (%s)", path, got, kind != "", kind)
		}
	}
	// The reason says what is odd, in the package's words.
	for path, want := range map[string]string{
		"a:b":          "a colon",
		"bad\xffutf8":  "bytes that are not UTF-8",
		"x~1":          "a ~ and a digit, the form of a short name",
		"t\tt":         "a control character",
		"e\u0301":      "not in normalization form C",
		"z\u200bw":     "an invisible character",
		"\xff:~1\x01x": "bytes that are not UTF-8", // the first test that fails decides
	} {
		if got := OddPath(path); got != want {
			t.Errorf("OddPath(%q) = %q, want %q", path, got, want)
		}
	}
}
