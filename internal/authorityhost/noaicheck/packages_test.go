package noaicheck

import (
	"reflect"
	"testing"
)

func TestCheckPackages(t *testing.T) {
	pkg := func(n, d string) Hit { return Hit{Kind: KindPackage, Path: n, Detail: d} }
	desc := func(name string) string { return "%FILENAME%\nx.pkg\n\n%NAME%\n" + name + "\n\n%VERSION%\n1-1\n" }
	cases := []struct {
		name      string
		allow     []string
		plant     func(f *fixture)
		want      []Hit
		wantNotes []string
	}{
		{"clean, not enforced", nil, nil, nil, []string{NotePackagesNotEnforced}},
		{"clean, enforced", []string{"bash"}, nil, nil, nil},
		{"a forbidden package while not enforced", nil, func(f *fixture) {
			f.file("var/lib/pacman/local/nodejs-22.0.0-1/desc", desc("nodejs"))
		}, []Hit{pkg("nodejs", DetailForbidden)}, []string{NotePackagesNotEnforced}},
		{"a forbidden package that is also allowed", []string{"bash", "npm"}, func(f *fixture) {
			f.file("var/lib/pacman/local/npm-10.0.0-1/desc", desc("npm"))
		}, []Hit{pkg("npm", DetailForbidden)}, nil},
		{"a package not allowed", []string{"bash"}, func(f *fixture) {
			f.file("var/lib/pacman/local/zsh-5.9-1/desc", desc("zsh"))
		}, []Hit{pkg("zsh", DetailNotAllowed)}, nil},
		{"a package not allowed while not enforced", nil, func(f *fixture) {
			f.file("var/lib/pacman/local/zsh-5.9-1/desc", desc("zsh"))
		}, nil, []string{NotePackagesNotEnforced}},
		{"an entry without a name", []string{"bash"}, func(f *fixture) {
			f.file("var/lib/pacman/local/odd-1-1/desc", "%VERSION%\n1\n")
		}, []Hit{pkg("/var/lib/pacman/local/odd-1-1", DetailNoName)}, nil},
		{"an entry without desc", []string{"bash"}, func(f *fixture) {
			f.dir("var/lib/pacman/local/odd-1-1")
		}, []Hit{{Kind: KindUnreadable, Path: "/var/lib/pacman/local/odd-1-1/desc", Detail: "no desc file"}}, nil},
		{"no database while enforced", []string{"bash"}, func(f *fixture) {
			mustRemoveAll(f, "var/lib/pacman")
		}, []Hit{pkg("/var/lib/pacman/local", DetailMissing)}, nil},
		{"no database while not enforced", nil, func(f *fixture) {
			mustRemoveAll(f, "var/lib/pacman")
		}, nil, []string{NotePackagesNotEnforced}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := cleanFixture(t)
			if tc.plant != nil {
				tc.plant(f)
			}
			l := testLists()
			l.PackagesAllow = tc.allow
			c := f.checker(l)
			c.CheckPackages()
			r := c.Report()
			if !(len(r.Hits) == 0 && len(tc.want) == 0) && !reflect.DeepEqual(r.Hits, tc.want) {
				t.Errorf("hits %v, want %v", r.Hits, tc.want)
			}
			if !(len(r.Notes) == 0 && len(tc.wantNotes) == 0) && !reflect.DeepEqual(r.Notes, tc.wantNotes) {
				t.Errorf("notes %v, want %v", r.Notes, tc.wantNotes)
			}
		})
	}
}

func TestDescName(t *testing.T) {
	for in, want := range map[string]string{
		"%NAME%\nbash\n":           "bash",
		"%BASE%\nx\n\n%NAME%\ny\n": "y",
		"%NAME%":                   "",
		"":                         "",
		"%VERSION%\n1\n":           "",
	} {
		if got := descName(in); got != want {
			t.Errorf("descName(%q) = %q, want %q", in, got, want)
		}
	}
}
