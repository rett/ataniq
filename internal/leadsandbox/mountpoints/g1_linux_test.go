//go:build linux

package mountpoints

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ataniq/internal/leadsandbox/plan"
	"ataniq/internal/leadsandbox/profile"
)

// This file is the test of rule G1: never bind a source path that has a
// component a sandbox can write. It builds the real plan from the fixture's
// inputs, so it needs both the profile and the plan.

func (f *fixture) planOptions() plan.Options {
	return plan.Options{
		PastaPath:  "/usr/bin/pasta",
		BwrapPath:  "/usr/bin/bwrap",
		AgentArgv:  []string{"/ataniq/agent/bin/agent"},
		ReportPath: f.base + "/run/report.fifo",
		ReportDir:  f.base + "/run",
		ReportFD:   3,
		// HostPresent is nil: no optional host row is emitted, so the plan
		// does not depend on the host's layout.
	}
}

func (f *fixture) buildPlan() *plan.Plan {
	f.t.Helper()
	p, err := plan.Build(f.in, f.planOptions())
	if err != nil {
		f.t.Fatalf("Build: %v", err)
	}
	return p
}

// ownedSources are the sources of the plan that lie in the fixture's folders
// (the host rows /usr, /etc/hosts and the like are the host's, not the test's).
func ownedSources(f *fixture, p *plan.Plan) []string {
	var out []string
	for _, m := range p.Mounts {
		if m.Source != "" && strings.HasPrefix(m.Source, f.base+"/") {
			out = append(out, m.Source)
		}
	}
	return out
}

func TestG1PlantedLinksInTheTreeNeverReachTheBindSources(t *testing.T) {
	type plantFn func(f *fixture)
	plants := []struct {
		name  string
		plant plantFn
	}{
		{"a link at .git/config", func(f *fixture) {
			f.write(f.out+"/owner-config", "[credential]\n\thelper = steal\n")
			f.remove(f.tree + "/.git/config")
			f.symlink(f.out+"/owner-config", f.tree+"/.git/config")
		}},
		{"a link at .git/hooks", func(f *fixture) {
			f.mkdir(f.out+"/owner-hooks", 0o755)
			f.remove(f.tree + "/.git/hooks")
			f.symlink(f.out+"/owner-hooks", f.tree+"/.git/hooks")
		}},
		{"a link at .git/info", func(f *fixture) {
			f.mkdir(f.out+"/owner-info", 0o755)
			f.remove(f.tree + "/.git/info")
			f.symlink(f.out+"/owner-info", f.tree+"/.git/info")
		}},
		{"a link at a middle component (.git)", func(f *fixture) {
			f.mkdir(f.out+"/owner-git/hooks", 0o755)
			f.mkdir(f.out+"/owner-git/info", 0o755)
			f.write(f.out+"/owner-git/config", "x")
			f.remove(f.tree + "/.git")
			f.symlink(f.out+"/owner-git", f.tree+"/.git")
		}},
	}
	for _, rt := range profile.Runtimes() {
		for _, pl := range plants {
			t.Run(string(rt)+" "+pl.name, func(t *testing.T) {
				f := newFixture(t, rt)
				f.prepare()
				want := f.buildPlan() // before the plant
				pl.plant(f)

				// The start-up check sees the plant: the mount point is a link.
				if rep := f.check(); !rep.Has(ErrSymlink) {
					t.Fatalf("Check does not see the link: %v", rep.Err())
				}

				// The plan built afterwards is the same text, and binds
				// nothing from the tree's .git.
				got := f.buildPlan()
				if strings.Join(got.Argv, "\x00") != strings.Join(want.Argv, "\x00") {
					t.Fatal("a link in the tree changed the plan")
				}
				gitRows := map[string]string{
					"/work/ataniq/.git/config": f.gitMeta + "/config",
					"/work/ataniq/.git/hooks":  f.gitMeta + "/hooks",
					"/work/ataniq/.git/info":   f.gitMeta + "/info",
				}
				seen := 0
				for _, m := range got.Mounts {
					if m.LeadWritable {
						t.Errorf("%s: LeadWritable", m.Inside)
					}
					if src, ok := gitRows[m.Inside]; ok {
						seen++
						if m.Source != src {
							t.Errorf("%s: source %s, want %s", m.Inside, m.Source, src)
						}
					}
					// Textually: only the tree's own bind has a source in the
					// tree, and none has one in or below its .git.
					if m.Source == f.tree+"/.git" || strings.HasPrefix(m.Source, f.tree+"/") {
						t.Errorf("%s: source %s lies in the lead's tree", m.Inside, m.Source)
					}
					if strings.Contains(m.Source, "/.git") {
						t.Errorf("%s: source %s is below a .git", m.Inside, m.Source)
					}
				}
				if seen != 3 {
					t.Errorf("%d of the 3 git rows found", seen)
				}
				for _, a := range got.Bwrap {
					if strings.HasPrefix(a, f.tree+"/") {
						t.Errorf("the argument %s names a path in the tree", a)
					}
				}

				// By resolving: every source of the plan resolves to itself
				// (no link on the way), and CheckBindSources passes on them.
				res := f.resolve()
				srcs := ownedSources(f, got)
				if len(srcs) < 10 {
					t.Fatalf("only %d owned sources: %v", len(srcs), srcs)
				}
				for _, s := range res.Sources {
					if strings.HasPrefix(s.Source, f.base+"/") && s.Real != s.Source {
						t.Errorf("the source %s of %s resolves to %s", s.Source, s.Inside, s.Real)
					}
				}
				for _, s := range srcs {
					if real, err := filepath.EvalSymlinks(s); err != nil || real != s {
						t.Errorf("the source %s resolves to %q (%v)", s, real, err)
					}
				}
				var realSrcs []string
				for _, s := range res.Sources {
					if !s.Skipped && strings.HasPrefix(s.Real, f.base+"/") {
						realSrcs = append(realSrcs, s.Real)
					}
				}
				if rep := CheckBindSources(realSrcs); !rep.OK() {
					t.Fatalf("CheckBindSources: %v", rep.Err())
				}
			})
		}
	}
}

func TestG1ALinkAtABindSourceIsRefusedNamingTheRow(t *testing.T) {
	// An Ataniq-owned source replaced by a link, and a source below a linked
	// parent: CheckBindSources refuses each, naming the source of the row.
	cases := []struct {
		name  string
		plant func(f *fixture)
		row   func(f *fixture) string // the source of the row
		link  func(f *fixture) string // the component the problem names
	}{
		{"git config source replaced by a link", func(f *fixture) {
			f.write(f.out+"/owner-config", "x")
			f.remove(f.gitMeta + "/config")
			f.symlink(f.out+"/owner-config", f.gitMeta+"/config")
		}, func(f *fixture) string { return f.gitMeta + "/config" }, func(f *fixture) string { return f.gitMeta + "/config" }},
		{"git hooks source replaced by a link", func(f *fixture) {
			f.mkdir(f.out+"/owner-hooks", 0o755)
			f.remove(f.gitMeta + "/hooks")
			f.symlink(f.out+"/owner-hooks", f.gitMeta+"/hooks")
		}, func(f *fixture) string { return f.gitMeta + "/hooks" }, func(f *fixture) string { return f.gitMeta + "/hooks" }},
		{"git info source replaced by a link", func(f *fixture) {
			f.mkdir(f.out+"/owner-info", 0o755)
			f.remove(f.gitMeta + "/info")
			f.symlink(f.out+"/owner-info", f.gitMeta+"/info")
		}, func(f *fixture) string { return f.gitMeta + "/info" }, func(f *fixture) string { return f.gitMeta + "/info" }},
		{"the git metadata folder replaced by a link", func(f *fixture) {
			if err := os.Rename(f.gitMeta, f.out+"/moved-meta"); err != nil {
				f.t.Fatal(err)
			}
			f.symlink(f.out+"/moved-meta", f.gitMeta)
		}, func(f *fixture) string { return f.gitMeta + "/info" }, func(f *fixture) string { return f.gitMeta }},
		{"an owned folder replaced by a link", func(f *fixture) {
			f.mkdir(f.out+"/agent", 0o755)
			f.remove(f.in.AgentInstall)
			f.symlink(f.out+"/agent", f.in.AgentInstall)
		}, func(f *fixture) string { return f.in.AgentInstall }, func(f *fixture) string { return f.in.AgentInstall }},
		{"a source below a linked parent", func(f *fixture) {
			if err := os.Rename(f.owned+"/managed", f.out+"/managed-moved"); err != nil {
				f.t.Fatal(err)
			}
			f.symlink(f.out+"/managed-moved", f.owned+"/managed")
		}, func(f *fixture) string { return f.in.ManagedSettings }, func(f *fixture) string { return f.owned + "/managed" }},
		{"a source below a linked state folder", func(f *fixture) {
			if err := os.Rename(f.state+"/mirrors", f.out+"/mirrors-moved"); err != nil {
				f.t.Fatal(err)
			}
			f.symlink(f.out+"/mirrors-moved", f.state+"/mirrors")
		}, func(f *fixture) string { return f.state + "/mirrors/ataniq.git" }, func(f *fixture) string { return f.state + "/mirrors" }},
		{"a source below the linked lead folder", func(f *fixture) {
			if err := os.Rename(f.state+"/leads/arc-lead", f.out+"/lead-moved"); err != nil {
				f.t.Fatal(err)
			}
			f.symlink(f.out+"/lead-moved", f.state+"/leads/arc-lead")
		}, func(f *fixture) string { return f.gitMeta + "/hooks" }, func(f *fixture) string { return f.state + "/leads/arc-lead" }},
	}
	for _, rt := range profile.Runtimes() {
		for _, c := range cases {
			t.Run(string(rt)+" "+c.name, func(t *testing.T) {
				f := newFixture(t, rt)
				f.prepare()
				p := f.buildPlan()
				srcs := ownedSources(f, p)
				if rep := CheckBindSources(srcs); !rep.OK() {
					t.Fatalf("before the plant: %v", rep.Err())
				}
				c.plant(f)
				rep := CheckBindSources(srcs)
				row := c.row(f)
				var found *Problem
				for _, pr := range rep.Problems {
					if pr.Path == row && pr.Kind == ErrSymlink {
						found = pr
					}
				}
				if found == nil {
					t.Fatalf("the row %s was not refused as a link: %v", row, rep.Err())
				}
				if !strings.Contains(found.Detail, c.link(f)) {
					t.Errorf("the problem does not name the link %s: %q", c.link(f), found.Detail)
				}
				// Every refusal is about a source of the plan, and nothing was followed.
				for _, pr := range rep.Problems {
					ok := false
					for _, s := range srcs {
						ok = ok || s == pr.Path
					}
					if !ok {
						t.Errorf("a problem about %s, which is no source of the plan", pr.Path)
					}
				}
			})
		}
	}
}

func TestG1NoSourceOfEitherRuntimeLiesInAFolderTheLeadWrites(t *testing.T) {
	for _, rt := range profile.Runtimes() {
		f := newFixture(t, rt)
		p := f.buildPlan()
		var writable []string
		for _, m := range p.Mounts {
			if m.Kind == profile.KindRwBind {
				writable = append(writable, m.Source)
			}
		}
		if len(writable) < 3 {
			t.Fatalf("%s: %d read-write rows", rt, len(writable))
		}
		for _, m := range p.Mounts {
			if m.Source == "" {
				continue
			}
			for _, w := range writable {
				if m.Kind != profile.KindRwBind && (m.Source == w || strings.HasPrefix(m.Source, w+"/")) {
					t.Errorf("%s: the source %s of %s lies in the lead-writable folder %s", rt, m.Source, m.Inside, w)
				}
			}
		}
	}
}
