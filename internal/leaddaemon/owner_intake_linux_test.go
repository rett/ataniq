//go:build linux && ataniq_ownerchannel

package leaddaemon

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"ataniq/internal/approval/ownerchannel"
	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/exchange/leadtext"
)

// ownerIntake is an Intake whose patch is the lines it was given, as the real
// intake writes it: escaped. Everything else answers nothing.
type ownerIntake struct {
	ownerchannel.Intake
	lines []string
	err   error
}

func (i ownerIntake) Patch(context.Context, ownerchannel.ReviewRef) (ownerchannel.ReviewPatch, error) {
	return ownerchannel.ReviewPatch{ReviewDigest: ownerReviewDigest, Lines: i.lines}, i.err
}

// TestOwnerThePatchIsTurnedBackToRawLinesBeforeItIsPaged: the intake's lines
// are escaped; the wrapper gives the service the raw bytes, so that the pages
// carry the raw bytes (the console's pager escapes them once) and a page does
// not split an escape sequence; the raw bytes of a hostile line come out as
// they went in.
func TestOwnerThePatchIsTurnedBackToRawLinesBeforeItIsPaged(t *testing.T) {
	raw := []string{
		"+plain",
		"+\x1b[2J\x1b]0;title\x07 clear",
		"+tab\there\r",
		"+back\\slash and \\n literally",
		"+bidi ‮ and zero width ​",
		"+bad utf8 \xff\xfe",
		"+" + strings.Repeat("\x1b", 300<<10),
		"",
	}
	escaped := make([]string, len(raw))
	for i, l := range raw {
		escaped[i] = leadtext.Escape(l)
		if strings.ContainsAny(escaped[i], "\x1b\t\r\n\x07") {
			t.Fatalf("the sample line %d is not escaped", i)
		}
	}
	w := UnescapingIntake{Intake: ownerIntake{lines: escaped}}
	rp, err := w.Patch(context.Background(), ownerchannel.ReviewRef{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(rp.Lines, raw) || rp.ReviewDigest != ownerReviewDigest {
		t.Fatalf("the lines are not the raw ones: %q", rp.Lines)
	}
	// Through the pages and back, the bytes are the raw ones, and a page holds
	// a raw escape byte, never the text \x1b.
	pages, err := ownerwire.SplitPatch("ap-one", ownerReviewDigest, rp.Lines)
	if err != nil {
		t.Fatal(err)
	}
	var sawRaw bool
	for _, p := range pages {
		for _, l := range p.Lines {
			sawRaw = sawRaw || strings.Contains(string(l.Text), "\x1b")
			if strings.Contains(string(l.Text), `\x1b`) {
				t.Fatal("a page holds an escaped line")
			}
		}
	}
	if !sawRaw {
		t.Fatal("no page holds the raw escape byte")
	}
	back, err := ownerwire.JoinPatch(pages)
	if err != nil || !slices.Equal(back, raw) {
		t.Fatalf("the pages do not rejoin to the raw lines: %v", err)
	}
	// What a skipped step would give: the pages of the escaped lines are not
	// the patch.
	if slices.Equal(escaped, raw) {
		t.Fatal("the sample cannot tell the two apart")
	}
}

// TestOwnerALineThatIsNotEscapedIsNotPassedOn: a line with a raw control byte,
// a backslash sequence the escaping does not write, or a backslash at the end
// fails the whole read; nothing partly turned back is returned. The inner
// intake's own error passes through, and its other methods are the inner's.
func TestOwnerALineThatIsNotEscapedIsNotPassedOn(t *testing.T) {
	for _, tt := range []struct {
		name  string
		lines []string
	}{
		{"a raw escape byte", []string{"+fine", "+\x1b[2J"}},
		{"a raw newline", []string{"+a\nb"}},
		{"a raw delete", []string{"+\x7f"}},
		{"an escape the escaping does not write", []string{"+a\\qb"}},
		{"a backslash at the end", []string{"+a\\"}},
		{"a short hex escape", []string{"+\\x4"}},
		{"a code point that is not one", []string{"+\\u{d800}"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rp, err := UnescapingIntake{Intake: ownerIntake{lines: tt.lines}}.Patch(context.Background(), ownerchannel.ReviewRef{})
			if err == nil || !strings.Contains(err.Error(), ErrPatchNotEscaped.Error()) || len(rp.Lines) != 0 || rp.ReviewDigest != "" {
				t.Fatalf("%+v, %v", rp, err)
			}
		})
	}
	inner := fmt.Errorf("the mirror is gone")
	if _, err := (UnescapingIntake{Intake: ownerIntake{err: inner}}).Patch(context.Background(), ownerchannel.ReviewRef{}); err != inner {
		t.Fatalf("%v", err)
	}
	// A pure pass-through of everything else.
	var _ ownerchannel.Intake = UnescapingIntake{}
	if !bytes.Equal([]byte(leadtext.Escape("a\\b")), []byte(`a\\b`)) {
		t.Fatal("the escaping is not what these cases assume")
	}
}

// TestOwnerAPatchAsTheIntakeWritesItIsPaged: the input is what the intake's
// own writer, leadtext.NewWriter, makes of a patch with tab-indented lines (Go
// code, a Makefile), a carriage return, an escape byte and a backslash: the
// tab stays raw in it, every other control byte is escaped. The wrapper turns
// it back, the pages rejoin to the raw lines byte for byte, and a long
// tab-indented line is split and rejoined too.
func TestOwnerAPatchAsTheIntakeWritesItIsPaged(t *testing.T) {
	long := "+\t" + strings.Repeat("\tx\x1b", 100<<10)
	rawLines := []string{
		"ataniq intake diff base=aa tip=bb",
		"diff --git a/main.go b/main.go",
		"@@ -1,3 +1,4 @@",
		" func main() {",
		"+\tfmt.Println(\"x\")",
		"+\t\tif a {\t// tab inside",
		"-\told\r",
		"+all:\n\t@echo \"a\\b\"\x1b[2J",
		long,
		"+\t",
	}
	// The first line is the intake's header, written before the writer.
	var buf bytes.Buffer
	buf.WriteString(rawLines[0] + "\n")
	w := leadtext.NewWriter(&buf)
	body := strings.Join(rawLines[1:], "\n") + "\n"
	if _, err := w.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	written := buf.String()
	if !strings.Contains(written, "\t") || strings.ContainsAny(written, "\x1b\r\x7f") {
		t.Fatal("the writer is not what this test assumes: it passes the tab and escapes the rest")
	}
	lines := strings.Split(strings.TrimSuffix(written, "\n"), "\n")
	// The raw expectation has the embedded "\n" of one sample split, as the
	// writer's lines are.
	var want []string
	for _, l := range rawLines {
		want = append(want, strings.Split(l, "\n")...)
	}
	if len(lines) != len(want) {
		t.Fatalf("%d lines written, %d expected", len(lines), len(want))
	}
	rp, err := UnescapingIntake{Intake: ownerIntake{lines: lines}}.Patch(context.Background(), ownerchannel.ReviewRef{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(rp.Lines, want) {
		t.Fatalf("the lines are not the raw ones: %q", rp.Lines)
	}
	pages, err := ownerwire.SplitPatch("ap-one", ownerReviewDigest, rp.Lines)
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) < 2 {
		t.Fatalf("the long line was not split: %d pages", len(pages))
	}
	back, err := ownerwire.JoinPatch(pages)
	if err != nil || !slices.Equal(back, want) {
		t.Fatalf("the pages do not rejoin to the raw lines: %v", err)
	}
}
