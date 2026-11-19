package card

import (
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"ataniq/internal/approval/record"
)

// This file holds what the tests of the package share for looking at a
// model: a dump of its values, the hostile texts, and the check that
// nothing of a lead renders raw.

// dump returns every exported leaf of v by its path, such as
// "Diff.Rows[0].Added", with its value written as text. A LeadText is
// written by its raw bytes, a time as a record writes it, and a nil pointer
// as such; a list also has an entry for its length. It is how the tests
// compare what two models say.
func dump(v any) map[string]string {
	out := make(map[string]string)
	dumpValue(reflect.ValueOf(v), "", out)
	return out
}

var (
	leadTextType = reflect.TypeOf(LeadText{})
	timeType     = reflect.TypeOf(time.Time{})
)

func dumpValue(v reflect.Value, path string, out map[string]string) {
	switch {
	case v.Type() == leadTextType:
		out[path] = "lead:" + v.Interface().(LeadText).bytes()
		return
	case v.Type() == timeType:
		out[path] = "time:" + record.FormatTime(v.Interface().(time.Time))
		return
	}
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			out[path] = "nil"
			return
		}
		dumpValue(v.Elem(), path, out)
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			f := v.Type().Field(i)
			if !f.IsExported() {
				panic("the model has an unexported field: " + path + "." + f.Name)
			}
			dumpValue(v.Field(i), strings.TrimPrefix(path+"."+f.Name, "."), out)
		}
	case reflect.Slice, reflect.Array:
		out[path+".len"] = strconv.Itoa(v.Len())
		for i := 0; i < v.Len(); i++ {
			dumpValue(v.Index(i), path+"["+strconv.Itoa(i)+"]", out)
		}
	case reflect.String:
		out[path] = "string:" + v.String()
	case reflect.Int, reflect.Int64:
		out[path] = "int:" + strconv.FormatInt(v.Int(), 10)
	case reflect.Bool:
		out[path] = "bool:" + strconv.FormatBool(v.Bool())
	default:
		panic(fmt.Sprintf("the model has a field of a kind the dump does not know: %s (%s)", path, v.Kind()))
	}
}

// changed returns the paths whose value differs between two dumps, sorted. A
// path that is in one dump only differs.
func changed(a, b map[string]string) []string {
	var out []string
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			out = append(out, k)
		}
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// leadTextsOf returns every LeadText that v holds in an exported field,
// with its path.
func leadTextsOf(v any) map[string]LeadText {
	out := make(map[string]LeadText)
	var walk func(v reflect.Value, path string)
	walk = func(v reflect.Value, path string) {
		if v.Type() == leadTextType {
			out[path] = v.Interface().(LeadText)
			return
		}
		if v.Type() == timeType {
			return
		}
		switch v.Kind() {
		case reflect.Pointer:
			if !v.IsNil() {
				walk(v.Elem(), path)
			}
		case reflect.Struct:
			for i := 0; i < v.NumField(); i++ {
				walk(v.Field(i), path+"."+v.Type().Field(i).Name)
			}
		case reflect.Slice, reflect.Array:
			for i := 0; i < v.Len(); i++ {
				walk(v.Index(i), path+"["+strconv.Itoa(i)+"]")
			}
		}
	}
	walk(reflect.ValueOf(v), "")
	return out
}

// plainStringsOf returns every plain string that v holds in an exported
// field, with its path.
func plainStringsOf(v any) map[string]string {
	out := make(map[string]string)
	for path, value := range dump(v) {
		if s, ok := strings.CutPrefix(value, "string:"); ok {
			out[path] = s
		}
	}
	return out
}

// rawByte returns the reason why s must not be drawn as it is, or "" when
// it may: a control byte, bytes that are not UTF-8, a C1 control, a
// direction override or an invisible character.
func rawByte(s string) string {
	if !utf8.ValidString(s) {
		return "bytes that are not UTF-8"
	}
	for _, r := range s {
		switch {
		case r < 0x20 || r == 0x7f:
			return fmt.Sprintf("the control byte %#02x", r)
		case r >= 0x80 && r <= 0x9f:
			return fmt.Sprintf("the C1 control U+%04X", r)
		case r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069, r == 0x200e, r == 0x200f, r == 0x061c:
			return fmt.Sprintf("the direction control U+%04X", r)
		case r >= 0x200b && r <= 0x200d, r == 0x2060, r == 0xfeff, r == 0x2028, r == 0x2029:
			return fmt.Sprintf("the invisible character U+%04X", r)
		}
	}
	return ""
}

// checkNothingRaw fails the test when any lead text of v renders a raw
// byte at any width, when a plain string of v would need escaping, or when
// printing v writes a raw byte.
func checkNothingRaw(t testing.TB, v any) {
	t.Helper()
	for path, text := range leadTextsOf(v) {
		for _, max := range []int{-1, 0, 1, 7, 80, 4096, 4 << 20} {
			line, cut := text.Line(max)
			if why := rawByte(line); why != "" {
				t.Errorf("%s.Line(%d) holds %s", path, max, why)
			}
			if max >= 0 && len(line) > max {
				t.Errorf("%s.Line(%d) is %d bytes long", path, max, len(line))
			}
			if max == 4<<20 && cut && text.Len() < 1<<18 {
				t.Errorf("%s.Line(%d) is cut, and the text has only %d bytes", path, max, text.Len())
			}
		}
	}
	for path, s := range plainStringsOf(v) {
		if !hostText(s) {
			t.Errorf("%s is a plain string that would need escaping: %q", path, s)
		}
	}
	for _, format := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
		if why := rawByte(fmt.Sprintf(format, v)); why != "" {
			t.Errorf("printing with %s writes %s", format, why)
		}
	}
}

// hostileTexts is the hostile set that every view of a card is fed: escape
// sequences, control bytes, direction overrides and invisible characters,
// bytes that are not UTF-8, and text shaped like the card's own chrome. The
// one line of 1 MiB is longLine, since it fits no record.
func hostileTexts() []string {
	var c0, c1 strings.Builder
	for b := 0; b < 0x20; b++ {
		c0.WriteByte(byte(b))
	}
	c0.WriteByte(0x7f)
	for r := rune(0x80); r <= 0x9f; r++ {
		c1.WriteRune(r)
	}
	return []string{
		"\x1b[2J\x1b[H\x1b[31mred\x1b[0m", // CSI
		"\x1b]0;a title\x07",              // OSC ended by BEL
		"\x1b]52;c;c2VjcmV0\x1b\\",        // OSC ended by ST
		"\x1bP1$r0m\x1b\\",                // DCS
		"\x9b2J\x9d0;title\x9c\x90q\x9c",  // C1 controls as single bytes
		c0.String(),                       // every C0 control and DEL
		c1.String(),                       // every C1 control as a code point
		"abc\u202edetrevni\u202c \u2066isolate\u2069 \u200f", // direction overrides
		"zero\u200bwidth\u200c\u200d\u2060\ufeffjoiners",     // zero-width code points
		"\xff\xfe\xc0\x80\xed\xa0\x80\xf8",                   // not UTF-8
		"half a rune \xe2\x82",                               // a cut character
		" [a] approve   [x] deny   [d] diff   [l] open the lead   [esc] back",
		"+------------+\n|  APPROVED  |\n+------------+",
		" APPROVAL ZZZZ   project-a / role-a@e7 (runtime-a)         asked 14:02, expires 14:32",
		"Lead's note (lead-stated, not checked)\n  applied: develop 93c8d68 -> 0194daf",
		"this request ended: approved\r\x1b[1A\x1b[2Kdecided past the limit",
		`back\slash and \x1b[2J spelled out`,
		"",
	}
}

// longLine is the hostile set's one line of 1 MiB, with an escape sequence
// at both ends and in the middle.
func longLine() string {
	half := strings.Repeat("A", 1<<19)
	return "\x1b[2J" + half + "\x1b]0;x\x07" + half + "\x1b[0m"
}
