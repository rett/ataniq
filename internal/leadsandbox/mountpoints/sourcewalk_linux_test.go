//go:build linux

package mountpoints

import (
	"errors"
	"path"
	"strings"
	"testing"

	"ataniq/internal/leadsandbox/profile"
)

// walkOf is the walk of the row at inside, from the fixture's inputs.
func (f *fixture) walkOf(inside string) SourceWalk {
	f.t.Helper()
	walks, err := SourceWalks(f.in)
	if err != nil {
		f.t.Fatalf("SourceWalks: %v", err)
	}
	for _, w := range walks {
		if w.Inside == inside {
			return w
		}
	}
	f.t.Fatalf("no walk for the row %s", inside)
	return SourceWalk{}
}

// Every row that Ataniq supplies has a walk that leads to its source, with the
// type the row binds; a row of the host has none.
func TestSourceWalksCoverEveryRowAtaniqSupplies(t *testing.T) {
	for _, rt := range profile.Runtimes() {
		t.Run(string(rt), func(t *testing.T) {
			f := newFixture(t, rt)
			table, err := f.in.Binds()
			if err != nil {
				t.Fatal(err)
			}
			walks, err := SourceWalks(f.in)
			if err != nil {
				t.Fatalf("SourceWalks: %v", err)
			}
			i, below, beside := 0, 0, 0
			for _, b := range table {
				if b.Source == "" || b.SourceRole == profile.RoleHost {
					continue
				}
				if i >= len(walks) {
					t.Fatalf("no walk for the row %s", b.Inside)
				}
				w := walks[i]
				i++
				if w.Inside != b.Inside || w.Path() != b.Source || w.Dir != b.IsDir {
					t.Errorf("row %s (%s, folder %v): walk %+v leads to %s", b.Inside, b.Source, b.IsDir, w, w.Path())
				}
				if path.Clean(w.Rel) != w.Rel || path.IsAbs(w.Rel) || w.Rel == "." || strings.HasPrefix(w.Rel, "..") {
					t.Errorf("row %s: the path beneath the root is %q", b.Inside, w.Rel)
				}
				if strings.HasPrefix(b.Source, f.state+"/") {
					below++
					if !w.BelowState || w.Root != f.state {
						t.Errorf("row %s lies below the state root: walk %+v", b.Inside, w)
					}
				} else {
					beside++
					if w.BelowState || w.Root != path.Dir(b.Source) || w.Rel != path.Base(b.Source) {
						t.Errorf("row %s lies outside the state root: walk %+v, want its parent folder and its name", b.Inside, w)
					}
				}
			}
			if i != len(walks) {
				t.Errorf("%d walks for %d rows", len(walks), i)
			}
			// The fixture has rows of both kinds, or the test says little.
			if below < 10 || beside < 10 {
				t.Errorf("%d rows below the state root, %d outside", below, beside)
			}
			for _, w := range walks {
				for _, hostRow := range []string{"/usr", "/etc/hosts", "/etc/passwd", "/etc/localtime", "/etc/ssl"} {
					if w.Inside == hostRow {
						t.Errorf("the row of the host %s has a walk: %+v", hostRow, w)
					}
				}
			}
		})
	}
}

func TestSourceWalksOfKnownRows(t *testing.T) {
	for _, rt := range profile.Runtimes() {
		t.Run(string(rt), func(t *testing.T) {
			f := newFixture(t, rt)
			lead := "leads/arc-lead"
			cfgInside := "/home/lead/" + rt.ConfigDirName()
			want := []SourceWalk{
				// Below the state root: the state root, and the whole path.
				{Inside: "/home/lead", Root: f.state, Rel: lead + "/home", Dir: true, BelowState: true},
				{Inside: cfgInside, Root: f.state, Rel: lead + "/config/" + string(rt), Dir: true, BelowState: true},
				{Inside: "/work/ataniq", Root: f.state, Rel: lead + "/tree", Dir: true, BelowState: true},
				{Inside: "/work/ataniq/.git/config", Root: f.state, Rel: lead + "/gitmeta/config", BelowState: true},
				{Inside: "/work/ataniq/.git/hooks", Root: f.state, Rel: lead + "/gitmeta/hooks", Dir: true, BelowState: true},
				{Inside: "/work/ataniq/.git/info", Root: f.state, Rel: lead + "/gitmeta/info", Dir: true, BelowState: true},
				{Inside: "/ataniq/mirror/ataniq.git", Root: f.state, Rel: "mirrors/ataniq.git", Dir: true, BelowState: true},
				{Inside: "/ataniq/records", Root: f.state, Rel: lead + "/qargi", Dir: true, BelowState: true},
				{Inside: "/ataniq/requests", Root: f.state, Rel: lead + "/requests", Dir: true, BelowState: true},
				{Inside: "/ataniq/mailbox", Root: f.state, Rel: "mailbox/arc-lead", Dir: true, BelowState: true},
				{Inside: "/ataniq/mail", Root: f.state, Rel: lead + "/mail", Dir: true, BelowState: true},
				{Inside: "/coord", Root: f.state, Rel: "pub/ataniq", Dir: true, BelowState: true},
				// Outside it: the source's own parent folder, and its name.
				{Inside: "/etc/resolv.conf", Root: f.owned, Rel: "resolv.conf"},
				{Inside: "/ataniq/probe", Root: f.owned, Rel: "probe"},
				{Inside: "/ataniq/spec", Root: f.owned, Rel: "spec", Dir: true},
				{Inside: "/ataniq/agent", Root: f.owned, Rel: "agent", Dir: true},
				{Inside: f.in.ManagedInside(), Root: f.owned + "/managed", Rel: "settings.json"},
				{Inside: cfgInside + "/skills", Root: f.owned, Rel: "skills", Dir: true},
				{Inside: cfgInside + "/" + rt.InstructionFileName(), Root: f.owned + "/instructions", Rel: rt.InstructionFileName()},
				{Inside: "/ataniq/git", Root: f.owned, Rel: "git", Dir: true},
				{Inside: "/ataniq/qargi", Root: f.owned, Rel: "qargi", Dir: true},
				{Inside: "/ataniq/assignment", Root: f.owned, Rel: "assignment", Dir: true},
				{Inside: "/ataniq/projection", Root: f.owned, Rel: "projection", Dir: true},
				{Inside: "/work/ataniq/.envrc", Root: f.owned + "/ph", Rel: "envrc"},
				{Inside: "/work/ataniq/.vscode", Root: f.owned + "/ph", Rel: "vscode", Dir: true},
				{Inside: "/work/ataniq/tools/local/x.txt", Root: f.owned + "/ph", Rel: "x.txt"},
			}
			walks, err := SourceWalks(f.in)
			if err != nil {
				t.Fatalf("SourceWalks: %v", err)
			}
			if len(walks) != len(want) {
				t.Errorf("%d walks, want %d: a row was added or dropped", len(walks), len(want))
			}
			for _, w := range want {
				if got := f.walkOf(w.Inside); got != w {
					t.Errorf("row %s:\n got %+v\nwant %+v", w.Inside, got, w)
				}
			}
		})
	}
}

// The walks depend on the inputs alone: nothing on the file system is looked
// at, so a caller in another process gets the same walks.
func TestSourceWalksReadNothing(t *testing.T) {
	f := newFixture(t, profile.Claude)
	want, err := SourceWalks(f.in)
	if err != nil {
		t.Fatal(err)
	}
	opened := recordByName(t)
	f.remove(f.base)
	got, err := SourceWalks(f.in)
	if err != nil {
		t.Fatalf("SourceWalks with nothing on the file system: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("%d walks, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("walk %d: %+v, want %+v", i, got[i], want[i])
		}
	}
	if len(*opened) != 0 {
		t.Errorf("opened by name: %q", *opened)
	}
}

func TestSourceWalksRefuseInvalidInputs(t *testing.T) {
	f := newFixture(t, profile.Claude)
	f.in.NeverMounted = nil
	walks, err := SourceWalks(f.in)
	if !errors.Is(err, ErrInvalid) || walks != nil {
		t.Fatalf("got %v, %v; want ErrInvalid", walks, err)
	}
	var p *Problem
	if !errors.As(err, &p) || p.Err == nil {
		t.Fatalf("the profile's refusal is not kept: %v", err)
	}
}

func TestSourceWalkPath(t *testing.T) {
	for _, c := range []struct {
		w    SourceWalk
		want string
	}{
		{SourceWalk{Root: "/state", Rel: "leads/a/home"}, "/state/leads/a/home"},
		{SourceWalk{Root: "/opt/owned", Rel: "agent"}, "/opt/owned/agent"},
		{SourceWalk{Root: "/", Rel: "opt"}, "/opt"},
		{SourceWalk{Root: "/opt", Rel: "."}, "/opt"},
		{SourceWalk{Root: "/", Rel: "."}, "/"},
	} {
		if got := c.w.Path(); got != c.want {
			t.Errorf("%+v: %q, want %q", c.w, got, c.want)
		}
	}
}

// The rows ResolveSources gives carry the same walks: one for each row that
// Ataniq supplies, none for a row of the host.
func TestResolveSourcesGivesEachRowItsWalk(t *testing.T) {
	for _, rt := range profile.Runtimes() {
		t.Run(string(rt), func(t *testing.T) {
			f := newFixture(t, rt)
			res := f.resolve()
			if !res.OK() {
				t.Fatalf("ResolveSources: %v", res.Err())
			}
			walks, err := SourceWalks(f.in)
			if err != nil {
				t.Fatal(err)
			}
			byInside := map[string]SourceWalk{}
			for _, w := range walks {
				byInside[w.Inside] = w
			}
			own := 0
			for _, s := range res.Sources {
				w, ok := byInside[s.Inside]
				switch {
				case s.Role == profile.RoleHost:
					if ok || s.Walk != (SourceWalk{}) {
						t.Errorf("the row of the host %s has a walk: %+v", s.Inside, s.Walk)
					}
				case !ok || s.Walk != w:
					t.Errorf("row %s: walk %+v, want %+v", s.Inside, s.Walk, w)
				default:
					own++
					if s.Walk.Path() != s.Bound() {
						t.Errorf("row %s: the walk leads to %s and the path bound is %s", s.Inside, s.Walk.Path(), s.Bound())
					}
				}
			}
			if own != len(walks) {
				t.Errorf("%d rows with a walk, want %d", own, len(walks))
			}
		})
	}
}
