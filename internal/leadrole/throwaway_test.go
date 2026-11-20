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
