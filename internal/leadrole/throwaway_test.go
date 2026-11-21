package leadrole

import (
	"strings"
	"testing"
)

func TestThrowawayPrefixValue(t *testing.T) {
	if ThrowawayPrefix != "throwaway-" {
		t.Fatalf("ThrowawayPrefix = %q", ThrowawayPrefix)
	}
}

func TestIsThrowaway(t *testing.T) {
	long := ThrowawayPrefix + strings.Repeat("a", MaxProjectLen-len(ThrowawayPrefix))
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"typical", "throwaway-one", true},
		{"one character after the prefix", "throwaway-a", true},
		{"digit after the prefix", "throwaway-1", true},
		{"hyphens after the prefix", "throwaway-a-b--c", true},
		{"longest valid name", long, true},
		{"prefix alone", "throwaway-", false},
		{"word without the hyphen", "throwaway", false},
		{"prefix then a hyphen", "throwaway--", false},
		{"too long", long + "a", false},
		{"upper case prefix", "Throwaway-one", false},
		{"upper case rest", "throwaway-One", false},
		{"prefix in the middle", "my-throwaway-one", false},
		{"similar prefix", "throwaways-one", false},
		{"other reserved-looking word", "canary-one", false},
		{"empty", "", false},
		{"space", "throwaway- one", false},
		{"slash", "throwaway-one/lead", false},
		{"underscore", "throwaway-a_b", false},
		{"trailing hyphen", "throwaway-one-", false},
		{"ordinary project", "ataniq", false},
	}
	for _, c := range cases {
		if got := IsThrowaway(c.in); got != c.want {
			t.Errorf("%s: IsThrowaway(%q) = %v, want %v", c.name, c.in, got, c.want)
		}
	}
}

// The prefix is reserved by callers, not by the grammar: ValidProject and Lead
// still accept a throwaway name.
func TestThrowawayNameStaysValid(t *testing.T) {
	const p = "throwaway-one"
	if !ValidProject(p) {
		t.Fatalf("ValidProject(%q) = false", p)
	}
	r, err := Lead(p)
	if err != nil {
		t.Fatalf("Lead(%q): %v", p, err)
	}
	if got := r.String(); got != p+"/lead" {
		t.Errorf("String = %q", got)
	}
	if !IsThrowaway(r.Project()) {
		t.Errorf("IsThrowaway(%q) = false", r.Project())
	}
}

func TestScratchPrefixValue(t *testing.T) {
	if ScratchPrefix != "scratch-" {
		t.Fatalf("ScratchPrefix = %q", ScratchPrefix)
	}
}

func TestIsScratch(t *testing.T) {
	long := ScratchPrefix + strings.Repeat("a", MaxProjectLen-len(ScratchPrefix))
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"typical", "scratch-alpha", true},
		{"one character after the prefix", "scratch-a", true},
		{"digit after the prefix", "scratch-1", true},
		{"hyphens after the prefix", "scratch-a-b--c", true},
		{"longest valid name", long, true},
		{"prefix alone", "scratch-", false},
		{"word without the hyphen", "scratch", false},
		{"prefix then a hyphen", "scratch--", false},
		{"too long", long + "a", false},
		{"upper case prefix", "Scratch-one", false},
		{"upper case rest", "scratch-One", false},
		{"prefix in the middle", "my-scratch-one", false},
		{"similar prefix", "scratchy-one", false},
		{"the other reserved prefix", "throwaway-one", false},
		{"empty", "", false},
		{"space", "scratch- one", false},
		{"slash", "scratch-one/lead", false},
		{"underscore", "scratch-a_b", false},
		{"trailing hyphen", "scratch-one-", false},
		{"ordinary project", "ataniq", false},
	}
	for _, c := range cases {
		if got := IsScratch(c.in); got != c.want {
			t.Errorf("%s: IsScratch(%q) = %v, want %v", c.name, c.in, got, c.want)
		}
	}
}

// The prefix is reserved by callers, not by the grammar: ValidProject and Lead
// still accept a scratch name.
func TestScratchNameStaysValid(t *testing.T) {
	const p = "scratch-alpha"
	if !ValidProject(p) {
		t.Fatalf("ValidProject(%q) = false", p)
	}
	r, err := Lead(p)
	if err != nil {
		t.Fatalf("Lead(%q): %v", p, err)
	}
	if got := r.String(); got != p+"/lead" {
		t.Errorf("String = %q", got)
	}
	if !IsScratch(r.Project()) || IsThrowaway(r.Project()) {
		t.Errorf("kinds of %q: scratch %v, throwaway %v", r.Project(), IsScratch(r.Project()), IsThrowaway(r.Project()))
	}
}

// The zero Reserved is the throwaway kind, which keeps a canary config that
// never sets the kind meaning what it always meant.
func TestReservedKinds(t *testing.T) {
	var zero Reserved
	if zero != ReservedThrowaway {
		t.Errorf("the zero Reserved is %v, want throwaway", zero)
	}
	cases := []struct {
		project string
		kind    Reserved
		reserve bool
	}{
		{"throwaway-one", ReservedThrowaway, true},
		{"scratch-one", ReservedScratch, true},
		{"ataniq", 0, false},
		{"throwaway-", 0, false},
		{"scratch-", 0, false},
		{"", 0, false},
		{"my-scratch-one", 0, false},
	}
	for _, c := range cases {
		k, ok := ReservedOf(c.project)
		if ok != c.reserve || (ok && k != c.kind) {
			t.Errorf("ReservedOf(%q) = %v, %v; want %v, %v", c.project, k, ok, c.kind, c.reserve)
		}
		for _, kind := range []Reserved{ReservedThrowaway, ReservedScratch} {
			want := c.reserve && kind == c.kind
			if got := IsReserved(c.project, kind); got != want {
				t.Errorf("IsReserved(%q, %v) = %v, want %v", c.project, kind, got, want)
			}
		}
		if IsReserved(c.project, Reserved(2)) || IsReserved(c.project, Reserved(-1)) {
			t.Errorf("IsReserved(%q) accepted a value that is not a kind", c.project)
		}
	}
}

func TestReservedString(t *testing.T) {
	for k, want := range map[Reserved]string{ReservedThrowaway: "throwaway", ReservedScratch: "scratch", Reserved(7): "reserved(7)"} {
		if got := k.String(); got != want {
			t.Errorf("Reserved(%d).String() = %q, want %q", int(k), got, want)
		}
	}
}
