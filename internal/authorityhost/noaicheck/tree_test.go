package noaicheck

import (
	"errors"
	"io/fs"
	"testing"
)

func TestResolve(t *testing.T) {
	f := newFixture(t)
	f.file("usr/bin/x", "")
	f.link("bin", "usr/bin")
	f.link("abs", "/usr/bin")
	f.link("up", "../../../../usr")
	f.link("chain", "abs")
	f.link("loop/a", "b")
	f.link("loop/b", "a")
	f.link("self", "self")
	f.link("dangling", "/missing/place")
	tr, err := openTree(f.root)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.close()
	cases := []struct {
		in, want string
		err      error
	}{
		{"usr/bin/x", "usr/bin/x", nil},
		{"/usr/bin/x", "usr/bin/x", nil},
		{"bin/x", "usr/bin/x", nil},
		{"abs/x", "usr/bin/x", nil},
		{"up/bin", "usr/bin", nil},
		{"chain/x", "usr/bin/x", nil},
		{"../../bin", "usr/bin", nil},
		{".", ".", nil},
		{"loop/a", "", errLoop},
		{"self/x", "", errLoop},
		{"dangling", "", fs.ErrNotExist},
		{"usr/bin/x/y", "", nil},
	}
	for _, tc := range cases {
		var got string
		var err error
		within(t, func() { got, err = tr.resolve(tc.in) })
		switch {
		case tc.in == "usr/bin/x/y":
			if err == nil {
				t.Errorf("%s: resolved below a file to %q", tc.in, got)
			}
		case tc.err != nil:
			if !errors.Is(err, tc.err) {
				t.Errorf("%s: err = %v, want %v", tc.in, err, tc.err)
			}
		case err != nil || got != tc.want:
			t.Errorf("%s: got %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
}

func TestReadFileRefusesNonRegular(t *testing.T) {
	f := newFixture(t)
	f.dir("etc/profile.d")
	f.file("etc/big", string(make([]byte, 100)))
	tr, err := openTree(f.root)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.close()
	if _, _, err := tr.readFile("etc/profile.d", 10); err == nil {
		t.Error("read a folder as a file")
	}
	if _, _, err := tr.readFile("etc/big", 10); !errors.Is(err, errTooLarge) {
		t.Errorf("read past the limit: %v", err)
	}
	if _, data, err := tr.readFile("etc/big", 100); err != nil || len(data) != 100 {
		t.Errorf("read at the limit: %d bytes, %v", len(data), err)
	}
}

// A walk does not follow links, ends on a loop and reports an unreadable
// folder instead of stopping.
func TestWalk(t *testing.T) {
	skipIfRoot(t)
	f := newFixture(t)
	f.file("opt/a/b/c/d/e", "")
	f.link("opt/loop", ".")
	f.link("opt/a/up", "../..")
	f.unreadable("opt/locked")
	tr, err := openTree(f.root)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.close()
	var seen []string
	var failed []string
	within(t, func() {
		tr.walk("opt", 4, func(rel string, d fs.DirEntry) { seen = append(seen, rel) },
			func(rel string, err error) { failed = append(failed, rel) })
	})
	want := map[string]bool{"opt/a/b/c/d": true, "opt/loop": true, "opt/a/up": true, "opt/locked": true}
	for _, s := range seen {
		if s == "opt/a/b/c/d/e" {
			t.Error("walked deeper than 4 levels")
		}
		delete(want, s)
	}
	if len(want) != 0 {
		t.Errorf("not visited: %v (visited %v)", want, seen)
	}
	if len(failed) != 1 || failed[0] != "opt/locked" {
		t.Errorf("failed = %v, want opt/locked", failed)
	}
}

func TestField(t *testing.T) {
	cases := map[string]string{
		"/usr/bin/claude":    "/usr/bin/claude",
		"":                   `""`,
		"two words":          `"two words"`,
		"x\nok":              `"x\nok"`,
		"a\"b":               `"a\"b"`,
		"\xff":               `"\xff"`,
		"caf\u00e9":          `"caf\u00e9"`,
		"\u2028":             `"\u2028"`,
		"esc\x1b[2J":         `"esc\x1b[2J"`,
		"c1\u009b":           `"c1\u009b"`,
		"back\\slash":        `"back\\slash"`,
		"bidi\u202eexe":      `"bidi\u202eexe"`,
		"/home/user/.claude": "/home/user/.claude",
	}
	for in, want := range cases {
		if got := field(in); got != want {
			t.Errorf("field(%q) = %s, want %s", in, got, want)
		}
	}
}
