package card

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"ataniq/internal/exchange/leadtext"
)

// A lead text gives its bytes only escaped: through Line, through the fmt
// package and as JSON.
func TestLeadTextRendersOnlyEscaped(t *testing.T) {
	for _, raw := range hostileTexts() {
		text := NewLeadText(raw)
		if text.Len() != len(raw) {
			t.Errorf("Len is %d for %d bytes", text.Len(), len(raw))
		}
		full, cut := text.Line(8 * len(raw))
		if cut {
			t.Errorf("%.20q: cut at eight times its length", raw)
		}
		if back, err := leadtext.Unescape(full); err != nil || back != raw {
			t.Errorf("%.20q: the line does not unescape to the text (%v)", raw, err)
		}
		if want, wantCut := leadtext.Line(raw, 40); true {
			got, gotCut := text.Line(40)
			if got != want || gotCut != wantCut {
				t.Errorf("%.20q: Line(40) is not leadtext's", raw)
			}
		}
		for _, format := range []string{"%v", "%s", "%q", "%+v", "%#v", "%x", "%d"} {
			if got := fmt.Sprintf(format, text); got != full {
				t.Errorf("%.20q printed with %s is not the escaped text", raw, format)
			}
		}
		if got := fmt.Sprint(text, []LeadText{text}, struct{ T LeadText }{text}, &text); rawByte(got) != "" {
			t.Errorf("%.20q printed inside other values holds %s", raw, rawByte(got))
		}
		data, err := json.Marshal(map[string]any{"text": text, "list": []LeadText{text}})
		if err != nil {
			t.Fatal(err)
		}
		if why := rawByte(string(data)); why != "" {
			t.Errorf("%.20q as JSON holds %s", raw, why)
		}
		checkNothingRaw(t, text)
	}
	// A verb that does not fit makes the fmt package read unexported
	// fields; it finds no text there.
	hidden := NewLeadText("hidden\x1b[2J text")
	for _, format := range []string{"%d", "%s", "%v", "%x", "%t"} {
		type holder struct{ Path LeadText }
		got := fmt.Sprintf(format, struct {
			P *holder
			L []LeadText
		}{&holder{Path: hidden}, []LeadText{hidden}})
		if why := rawByte(got); why != "" || strings.Contains(got, "2J text") && !strings.Contains(got, `\x1b[2J text`) {
			t.Errorf("printed with %s behind a pointer: %q", format, got)
		}
	}
	if reflect.TypeOf(LeadText{}).NumField() != 1 || reflect.TypeOf(LeadText{}).Field(0).IsExported() {
		t.Error("LeadText has an exported field")
	}
	a, empty := NewLeadText("a"), NewLeadText("")
	if !a.Equal(NewLeadText("a")) || a.Equal(NewLeadText("b")) || a.Equal(empty) ||
		!empty.Equal(LeadText{}) || empty != (LeadText{}) || empty.Len() != 0 {
		t.Error("two lead texts are not compared by their bytes, or empty text is not the zero value")
	}
}

func TestLeadTextLines(t *testing.T) {
	for text, want := range map[string][]string{
		"":             nil,
		"one":          {"one"},
		"one\n":        {"one"},
		"one\ntwo":     {"one", "two"},
		"\n":           {""},
		"one\n\n":      {"one", ""},
		"\none":        {"", "one"},
		"cr\r\nstays":  {"cr\r", "stays"},
		"\x1b[2J\nx\n": {"\x1b[2J", "x"},
	} {
		var got []string
		for _, l := range NewLeadText(text).Lines() {
			got = append(got, l.bytes())
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%q: got %q, want %q", text, got, want)
		}
	}
}

// One line of 1 MiB is cut and escaped like any other text.
func TestOneLineOfOneMebibyte(t *testing.T) {
	long := longLine()
	if len(long) < 1<<20 || strings.Contains(long, "\n") {
		t.Fatal("the line is not one line of 1 MiB")
	}
	text := NewLeadText(long)
	checkNothingRaw(t, text)
	for _, max := range []int{80, 4096} {
		got, cut := text.Line(max)
		if !cut || len(got) > max || len(got) < max-4 {
			t.Errorf("Line(%d): %d bytes, cut %v", max, len(got), cut)
		}
	}
	if got, cut := text.Line(1 << 21); cut || !strings.HasPrefix(got, `\x1b[2JAAAA`) {
		t.Errorf("the whole line: cut %v, starts %.12q", cut, got)
	}
	if lines := text.Lines(); len(lines) != 1 {
		t.Errorf("the line splits into %d", len(lines))
	}
}
