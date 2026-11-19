package wire_test

import (
	"strings"
	"testing"

	"ataniq/internal/leadholder/wire"
)

func TestSanitizeDetail(t *testing.T) {
	for _, c := range []struct{ name, in, want string }{
		{"empty", "", ""},
		{"printable", "a b ~!", "a b ~!"},
		{"the edges", " ~", " ~"},
		{"just outside", "\x1f\x7f", "??"},
		{"newline and tab", "a\nb\tc", "a?b?c"},
		{"a nul", "a\x00b", "a?b"},
		{"a high byte", "a\xffb", "a?b"},
		{"a two-byte character", "aéb", "a??b"},
		{"a three-byte character", "中", "???"},
		{"an escape sequence", "\x1b[31mred", "?[31mred"},
	} {
		if got := wire.SanitizeDetail(c.in); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func TestSanitizeDetailCuts(t *testing.T) {
	long := strings.Repeat("a", wire.MaxInjectDetail)
	if got := wire.SanitizeDetail(long); got != long {
		t.Fatalf("a detail at the cap changed, length %d", len(got))
	}
	// One byte over is cut first and the replacement comes after; the
	// replacement keeps the byte count, so a multi-byte character at the cut is
	// cut in the middle, as ?.
	if got := wire.SanitizeDetail(long + "b"); got != long {
		t.Fatalf("one over: length %d", len(got))
	}
	in := strings.Repeat("a", wire.MaxInjectDetail-1) + "é"
	want := strings.Repeat("a", wire.MaxInjectDetail-1) + "?"
	if got := wire.SanitizeDetail(in); got != want {
		t.Fatalf("a character over the cut: length %d", len(got))
	}
	big := strings.Repeat("中", 1000)
	got := wire.SanitizeDetail(big)
	if len(got) != wire.MaxInjectDetail || strings.Trim(got, "?") != "" {
		t.Fatalf("big: length %d", len(got))
	}
}

func TestSanitizeDetailAlwaysPasses(t *testing.T) {
	for _, s := range []string{"", "ok", "a\x00\xff b", strings.Repeat("\x80", 2000), strings.Repeat("x", 513)} {
		out := wire.SanitizeDetail(s)
		if err := wire.CheckInjectDetail(out); err != nil {
			t.Errorf("%q: %v", s, err)
		}
		if again := wire.SanitizeDetail(out); again != out {
			t.Errorf("not idempotent for %q", s)
		}
		r := wire.InjectResult{Outcome: wire.InjectOutcomeNotNow, Reason: wire.InjectReasonBlocked, Written: wire.InjectWrittenNothing, Detail: out}
		if err := r.Validate(); err != nil {
			t.Errorf("%q: %v", s, err)
		}
	}
}
