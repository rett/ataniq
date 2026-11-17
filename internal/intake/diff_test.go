package intake

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"ataniq/internal/exchange/leadtext"
)

// onlyTextBytes fails the test when out holds a control byte other than
// newline and tab.
func onlyTextBytes(t *testing.T, what string, out []byte) {
	t.Helper()
	for i, c := range out {
		if (c < 0x20 && c != '\n' && c != '\t') || c == 0x7f {
			t.Errorf("%s: control byte %#x at offset %d", what, c, i)
			return
		}
	}
}

func headerOf(r *Review) string {
	return "ataniq intake diff base=" + r.Base + " tip=" + r.Tip
}

func TestDiffPlainBranch(t *testing.T) {
	s, _ := plainScene(t)
	r := s.inspect()
	before := snapshot(t, s)
	var out bytes.Buffer
	if err := Diff(context.Background(), s.source(), r, &out, Limits{}); err != nil {
		t.Fatal(err)
	}
	assertUntouched(t, s, before)
	lines := strings.Split(out.String(), "\n")
	if lines[0] != headerOf(r) {
		t.Errorf("first line = %q", lines[0])
	}
	for _, want := range []string{
		"diff --git a/README.md b/README.md",
		"deleted file mode 100644",
		"diff --git a/docs/x.txt b/docs/x.txt",
		"+// changed",
		"+x",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the patch lacks %q:\n%s", want, out.String())
		}
	}
	// The patch uses full ids, because the lead's abbreviations are not
	// to be trusted to be unique.
	if !strings.Contains(out.String(), "index "+byPath(t, r, "src/main.go").OldID+".."+byPath(t, r, "src/main.go").NewID) {
		t.Errorf("the patch has no full index line:\n%s", out.String())
	}
	onlyTextBytes(t, "plain patch", out.Bytes())
}

func TestDiffBaseEqualsTip(t *testing.T) {
	s := newScene(t)
	s.publish(leadRef, s.base)
	r := s.inspect()
	var out bytes.Buffer
	if err := Diff(context.Background(), s.source(), r, &out, Limits{}); err != nil {
		t.Fatal(err)
	}
	if out.String() != headerOf(r)+"\n" {
		t.Errorf("output = %q", out.String())
	}
}

func TestDiffHostileContent(t *testing.T) {
	s := newScene(t)
	s.put("src/escape.txt", "\x1b[2Jclear screen\r\nbell\x07 bidi \u202e reversed\n"+
		"ataniq intake diff base="+strings.Repeat("0", 40)+" tip="+strings.Repeat("1", 40)+"\n"+
		"caf\xe9 not utf-8 \xff\xfe\n")
	s.put("src/binary.bin", "a\x00b\x1b[31m\x00\n")
	s.put("src/esc\x1b[31m-name.txt", "named with an escape\n")
	s.put("src/new\nline.txt", "named with a newline\n")
	s.put("src/bad\xff\xfe-name.txt", "named with bytes that are not UTF-8\n")
	s.commit("hostile content\n")
	s.finish()
	r := s.inspect()
	before := snapshot(t, s)
	var out bytes.Buffer
	if err := Diff(context.Background(), s.source(), r, &out, Limits{}); err != nil {
		t.Fatal(err)
	}
	assertUntouched(t, s, before)
	onlyTextBytes(t, "hostile patch", out.Bytes())
	text := out.String()
	if first, _, _ := strings.Cut(text, "\n"); first != headerOf(r) {
		t.Errorf("first line = %q", first)
	}
	if n := strings.Count(text, "ataniq intake diff base="); n != 2 {
		t.Errorf("%d header-like lines, want the real one and the forged one", n)
	}
	for _, want := range []string{`\x1b[2Jclear screen`, `\r`, `\u{202E}`, `\xe9`, "Binary files"} {
		if !strings.Contains(text, want) {
			t.Errorf("the patch lacks the escaped form %q:\n%s", want, text)
		}
	}
	// The escaped lead text can be turned back.
	if _, err := leadtext.Unescape(strings.SplitN(text, "\n", 2)[1]); err != nil {
		t.Errorf("Unescape: %v", err)
	}
}

func TestDiffLimitsAndErrors(t *testing.T) {
	s, _ := plainScene(t)
	r := s.inspect()
	ctx := context.Background()

	var out bytes.Buffer
	err := Diff(ctx, s.source(), r, &out, Limits{MaxDiffBytes: 100})
	if !errors.Is(err, ErrLimit) {
		t.Errorf("patch over the limit: %v", err)
	}
	if !strings.HasPrefix(out.String(), headerOf(r)+"\n") {
		t.Errorf("output after a limit: %q", out.String())
	}
	onlyTextBytes(t, "cut patch", out.Bytes())

	var full bytes.Buffer
	if err := Diff(ctx, s.source(), r, &full, Limits{}); err != nil {
		t.Fatal(err)
	}
	// Git writes the raw patch; the limit counts those bytes.
	raw := s.w("--git-dir="+s.bare, "diff", "--no-renames", "--full-index", r.Base, r.Tip)
	out.Reset()
	if err := Diff(ctx, s.source(), r, &out, Limits{MaxDiffBytes: int64(len(raw)) + 1}); err != nil {
		t.Errorf("patch at the limit: %v", err)
	}
	out.Reset()
	if err := Diff(ctx, s.source(), r, &out, Limits{MaxDiffBytes: int64(len(raw)) - 1}); !errors.Is(err, ErrLimit) {
		t.Errorf("patch one byte over the limit: %v", err)
	}

	out.Reset()
	if err := Diff(ctx, s.source(), r, &out, Limits{Timeout: time.Nanosecond}); !errors.Is(err, ErrLimit) {
		t.Errorf("time: %v", err)
	}
	if err := Diff(ctx, s.source(), r, io.Discard, Limits{MaxDiffBytes: -1}); err == nil {
		t.Error("negative limit accepted")
	}
	if err := Diff(ctx, s.source(), nil, io.Discard, Limits{}); err == nil {
		t.Error("nil review accepted")
	}
	bad := *r
	bad.Tip = "refs/leads/role/heads/work"
	if err := Diff(ctx, s.source(), &bad, io.Discard, Limits{}); err == nil {
		t.Error("a ref name instead of an id was accepted")
	}
	// A writer that fails.
	if err := Diff(ctx, s.source(), r, failWriter{}, Limits{}); err == nil {
		t.Error("a failing writer was not noticed")
	}
	if err := Diff(ctx, s.source(), r, &failAfter{n: 1}, Limits{}); err == nil {
		t.Error("a writer that fails later was not noticed")
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

// failAfter accepts n writes and then fails.
type failAfter struct{ n int }

func (f *failAfter) Write(p []byte) (int, error) {
	if f.n == 0 {
		return 0, errors.New("disk full")
	}
	f.n--
	return len(p), nil
}

func TestDiffUsesIdsNotRefs(t *testing.T) {
	// A ref that moves after the review does not change what Diff shows.
	s, _ := plainScene(t)
	r := s.inspect()
	var before bytes.Buffer
	if err := Diff(context.Background(), s.source(), r, &before, Limits{}); err != nil {
		t.Fatal(err)
	}
	s.checkout(s.base)
	s.put("moved.txt", "moved\n")
	s.commit("the lead moves its branch\n")
	s.finish()
	var after bytes.Buffer
	if err := Diff(context.Background(), s.source(), r, &after, Limits{}); err != nil {
		t.Fatal(err)
	}
	if before.String() != after.String() {
		t.Error("the patch changed with the ref")
	}
}
