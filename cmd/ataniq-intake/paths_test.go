package main

import (
	"os"
	"testing"
)

func TestOwnerUIDReadsTheOwner(t *testing.T) {
	fi, err := os.Lstat(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	uid, ok := ownerUID(fi)
	if !ok || int(uid) != os.Geteuid() {
		t.Fatalf("owner %d (%v), want %d", uid, ok, os.Geteuid())
	}
}

func TestWithin(t *testing.T) {
	for _, c := range []struct {
		parent, child string
		want          bool
	}{
		{"/a/b", "/a/b", true},
		{"/a/b", "/a/b/c", true},
		{"/a/b", "/a/b/c/d", true},
		{"/a/b", "/a/bc", false},
		{"/a/b", "/a", false},
		{"/a/b", "/x", false},
		{"/", "/a", true},
	} {
		if got := within(c.parent, c.child); got != c.want {
			t.Errorf("within(%q, %q) = %v, want %v", c.parent, c.child, got, c.want)
		}
	}
}

func TestAcceptArgIsOneShellWord(t *testing.T) {
	for in, want := range map[string]string{
		".claude/settings.json": `--accept-path '.claude/settings.json'`,
		"has space":             `--accept-path 'has space'`,
		"it's":                  `--accept-path 'it'\''s'`,
		"a\nb":                  `--accept-path 'a\nb'`,
		`back\slash`:            `--accept-path 'back\\slash'`,
	} {
		if got := acceptArg(in); got != want {
			t.Errorf("acceptArg(%q) = %s, want %s", in, got, want)
		}
	}
}
