package plan

import (
	"strconv"
	"strings"
	"testing"

	"ataniq/internal/leadsandbox/profile"
)

// Rule B1, second half: every bind source Ataniq supplies reaches bubblewrap
// as a descriptor the pass-through step walked to, never as a path.

// everyPlan builds a plan for each runtime, identity mode and seccomp state,
// with and without the optional rows and with an extra placeholder.
func everyPlan(t *testing.T, do func(name string, p *Plan, in profile.Inputs, o Options)) {
	t.Helper()
	for _, rt := range profile.Runtimes() {
		for _, owner := range []bool{false, true} {
			for _, sec := range []bool{false, true} {
				in := fixture(rt)
				if owner {
					in = ownerMode(in)
				}
				if sec {
					in = withSeccomp(in)
					in.ExtraPlaceholders = []profile.Placeholder{{Rel: ".envrc", Source: "/opt/ataniq/placeholders/envrc"}}
				}
				for _, present := range []map[string]bool{allPresent(t, in), nil} {
					o := options(in, present)
					do(string(rt)+" owner="+strconv.FormatBool(owner)+" seccomp="+strconv.FormatBool(sec), mustBuild(t, in, o), in, o)
				}
			}
		}
	}
}

func TestSuppliedRowsAreBoundFromDescriptorsNeverByPath(t *testing.T) {
	everyPlan(t, func(name string, p *Plan, in profile.Inputs, o Options) {
		table, err := in.Binds()
		if err != nil {
			t.Fatal(err)
		}
		host, supplied := map[string]bool{}, map[string]profile.Bind{}
		for _, b := range table {
			switch {
			case b.Source == "":
			case b.SourceRole == profile.RoleHost:
				host[b.Inside] = true
			default:
				supplied[b.Inside] = b
			}
		}
		// The arguments: a bind by path is a row of the host, a bind from a
		// descriptor is a row Ataniq supplies, and each such row appears once.
		byFD := map[string]string{}
		for i := 1; i < len(p.Bwrap)-1; i += width(p.Bwrap[i]) {
			switch p.Bwrap[i] {
			case "--ro-bind", "--bind":
				if !host[p.Bwrap[i+2]] {
					t.Errorf("%s: %s is bound by path and is not a row of the host", name, p.Bwrap[i+2])
				}
			case "--ro-bind-fd", "--bind-fd":
				inside := p.Bwrap[i+2]
				b, ok := supplied[inside]
				if !ok {
					t.Errorf("%s: %s is bound from a descriptor and is not a row Ataniq supplies", name, inside)
				}
				if rw := p.Bwrap[i] == "--bind-fd"; rw != (b.Kind == profile.KindRwBind) {
					t.Errorf("%s: %s is bound with %s, the row is a %s", name, inside, p.Bwrap[i], b.Kind)
				}
				if _, twice := byFD[inside]; twice {
					t.Errorf("%s: %s is bound twice", name, inside)
				}
				byFD[inside] = p.Bwrap[i+1]
			}
		}
		if len(byFD) != len(supplied) {
			t.Errorf("%s: %d rows bound from a descriptor, the table has %d rows Ataniq supplies", name, len(byFD), len(supplied))
		}
		// No argument of bubblewrap is the host path of such a source.
		for _, b := range supplied {
			if indexOf(p.Bwrap, b.Source) >= 0 {
				t.Errorf("%s: the bubblewrap arguments name the source %s of %s", name, b.Source, b.Inside)
			}
		}
		// The walks: one per such row, in the order of the table, leading to
		// the row's source, of the row's type, at the number the row is bound
		// from; the numbers are the lowest from MinBindFD up that are neither
		// the seccomp nor the report descriptor.
		if len(p.Walks) != len(supplied) {
			t.Fatalf("%s: %d walks for %d rows", name, len(p.Walks), len(supplied))
		}
		taken := map[int]bool{o.ReportFD: true}
		if in.SeccompEnabled() {
			taken[o.SeccompFD] = true
		}
		next, n := MinBindFD, 0
		for _, b := range table {
			if _, ok := supplied[b.Inside]; !ok {
				continue
			}
			for taken[next] {
				next++
			}
			w := p.Walks[n]
			if w.Inside != b.Inside || w.Path() != b.Source || w.Dir != b.IsDir || w.FD != next {
				t.Errorf("%s: walk %d is %+v, want descriptor %d for %s from %s", name, n, w, next, b.Inside, b.Source)
			}
			if byFD[b.Inside] != strconv.Itoa(w.FD) {
				t.Errorf("%s: %s is bound from %s, its walk is at %d", name, b.Inside, byFD[b.Inside], w.FD)
			}
			next++
			n++
		}
		// The list of mounts says the same.
		for _, m := range p.Mounts {
			_, sup := supplied[m.Inside]
			if sup != (m.FD != 0) {
				t.Errorf("%s: mount %s has descriptor %d", name, m.Inside, m.FD)
			}
			if sup && byFD[m.Inside] != strconv.Itoa(m.FD) {
				t.Errorf("%s: mount %s lists descriptor %d, the arguments bind %s", name, m.Inside, m.FD, byFD[m.Inside])
			}
		}
		// The step walks exactly these, after the files.
		files := len(p.Files)
		for i, w := range p.Walks {
			at := 2 + 2*files + 4*i
			opt := PassFDsWalkFile
			if w.Dir {
				opt = PassFDsWalkDir
			}
			if got := p.PassFDs[at : at+4]; !equal(got, []string{opt, strconv.Itoa(w.FD), w.Root, w.Rel}) {
				t.Errorf("%s: the step has %q for walk %d, want %s %d %s %s", name, got, i, opt, w.FD, w.Root, w.Rel)
			}
		}
		if len(p.PassFDs) != 2+2*files+4*len(p.Walks)+1 {
			t.Errorf("%s: the step has %d elements", name, len(p.PassFDs))
		}
		if probs := Verify(p, in, o); len(probs) != 0 {
			t.Errorf("%s: %v", name, probs)
		}
	})
}

// The numbers are the plan's choice and pass over the two the options give,
// wherever those lie.
func TestBindDescriptorsPassOverTheSeccompAndReportDescriptors(t *testing.T) {
	in := withSeccomp(fixture(profile.Claude))
	for _, c := range []struct{ seccomp, report int }{{7, 3}, {10, 11}, {11, 10}, {12, 30}, {10, 1023}, {32, 33}, {40, 41}} {
		o := options(in, nil)
		o.SeccompFD, o.ReportFD = c.seccomp, c.report
		p := mustBuild(t, in, o)
		seen := map[int]bool{c.seccomp: true, c.report: true}
		want := MinBindFD
		for _, w := range p.Walks {
			for want == c.seccomp || want == c.report {
				want++
			}
			if w.FD != want || seen[w.FD] {
				t.Fatalf("seccomp %d report %d: walk of %s at %d, want %d", c.seccomp, c.report, w.Inside, w.FD, want)
			}
			seen[w.FD] = true
			want++
		}
		if probs := Verify(p, in, o); len(probs) != 0 {
			t.Errorf("seccomp %d report %d: %v", c.seccomp, c.report, probs)
		}
	}
}

// setWalk replaces the walk of the options for an inside path.
func setWalk(o *Options, inside string, change func(w *SourceWalk)) {
	walks := append([]SourceWalk(nil), o.SourceWalks...)
	for i := range walks {
		if walks[i].Inside == inside {
			change(&walks[i])
		}
	}
	o.SourceWalks = walks
}

// Build refuses options whose walks do not match the table exactly, and
// Verify refuses the same options for a plan that was built from good ones.
func TestSourceWalkRefusals(t *testing.T) {
	in := fixture(profile.Claude)
	for _, c := range []struct {
		name   string
		change func(o *Options)
		field  string
		want   string
	}{
		{"no walks", func(o *Options) { o.SourceWalks = nil }, "source walks", "has no walk"},
		{"a row without a walk", func(o *Options) { o.SourceWalks = o.SourceWalks[1:] }, "source walks", "/etc/resolv.conf has no walk"},
		{"the tree without a walk", func(o *Options) {
			var keep []SourceWalk
			for _, w := range o.SourceWalks {
				if w.Inside != "/work/demo" {
					keep = append(keep, w)
				}
			}
			o.SourceWalks = keep
		}, "source walks", "/work/demo has no walk"},
		{"a walk for a row of the host", func(o *Options) {
			o.SourceWalks = append(append([]SourceWalk(nil), o.SourceWalks...), SourceWalk{Inside: "/usr", Root: "/", Rel: "usr", Dir: true})
		}, "source walks", "not the inside path of a bind row Ataniq supplies"},
		{"a walk for a host file", func(o *Options) {
			o.SourceWalks = append(append([]SourceWalk(nil), o.SourceWalks...), SourceWalk{Inside: "/etc/passwd", Root: "/etc", Rel: "passwd"})
		}, "source walks", "not the inside path of a bind row Ataniq supplies"},
		{"a walk for a row without a source", func(o *Options) {
			o.SourceWalks = append(append([]SourceWalk(nil), o.SourceWalks...), SourceWalk{Inside: "/tmp", Root: "/", Rel: "tmp", Dir: true})
		}, "source walks", "not the inside path of a bind row Ataniq supplies"},
		{"a walk for a path that is no row", func(o *Options) {
			o.SourceWalks = append(append([]SourceWalk(nil), o.SourceWalks...), SourceWalk{Inside: "/x", Root: "/opt/ataniq", Rel: "qargi", Dir: true})
		}, "source walks", "not the inside path of a bind row Ataniq supplies"},
		{"two walks for one row", func(o *Options) {
			o.SourceWalks = append(append([]SourceWalk(nil), o.SourceWalks...), o.SourceWalks[3])
		}, "source walks", "has two walks"},
		{"a walk that leads elsewhere", func(o *Options) {
			setWalk(o, "/ataniq/qargi", func(w *SourceWalk) { w.Rel = "other" })
		}, "source walks", "does not lead to the source"},
		{"a walk into the owner's home", func(o *Options) {
			setWalk(o, "/home/lead", func(w *SourceWalk) { w.Root, w.Rel = "/home", "owner" })
		}, "source walks", "does not lead to the source"},
		{"a walk of the other type for a folder", func(o *Options) {
			setWalk(o, "/ataniq/qargi", func(w *SourceWalk) { w.Dir = false })
		}, "source walks", "is for a regular file and the row binds a folder"},
		{"a walk of the other type for a file", func(o *Options) {
			setWalk(o, "/ataniq/probe", func(w *SourceWalk) { w.Dir = true })
		}, "source walks", "is for a folder and the row binds a regular file"},
		{"a relative folder", func(o *Options) {
			setWalk(o, "/ataniq/qargi", func(w *SourceWalk) { w.Root = "opt/ataniq" })
		}, "folder of the walk of /ataniq/qargi", "absolute"},
		{"an unclean folder", func(o *Options) {
			setWalk(o, "/ataniq/qargi", func(w *SourceWalk) { w.Root = "/opt/ataniq/../ataniq" })
		}, "folder of the walk of /ataniq/qargi", "clean"},
		{"an empty folder", func(o *Options) {
			setWalk(o, "/ataniq/qargi", func(w *SourceWalk) { w.Root = "" })
		}, "folder of the walk of /ataniq/qargi", "empty"},
		{"an empty path", func(o *Options) {
			setWalk(o, "/ataniq/qargi", func(w *SourceWalk) { w.Rel = "" })
		}, "path of the walk of /ataniq/qargi", "empty"},
		{"an absolute path", func(o *Options) {
			setWalk(o, "/ataniq/qargi", func(w *SourceWalk) { w.Rel = "/opt/ataniq/qargi" })
		}, "path of the walk of /ataniq/qargi", "absolute"},
		{"a path through the parent", func(o *Options) {
			setWalk(o, "/ataniq/qargi", func(w *SourceWalk) { w.Root, w.Rel = "/opt/ataniq/bin", "../qargi" })
		}, "path of the walk of /ataniq/qargi", "clean"},
		{"an unclean path", func(o *Options) {
			setWalk(o, "/home/lead", func(w *SourceWalk) { w.Rel = "leads//arc-lead/home" })
		}, "path of the walk of /home/lead", "clean"},
		{"a path with a newline", func(o *Options) {
			setWalk(o, "/home/lead", func(w *SourceWalk) { w.Rel = "leads/arc-lead\n/home" })
		}, "path of the walk of /home/lead", "control"},
		{"a path read as an option", func(o *Options) {
			setWalk(o, "/ataniq/qargi", func(w *SourceWalk) { w.Rel = "--qargi" })
		}, "path of the walk of /ataniq/qargi", "starts with --"},
		{"the folder itself as a file", func(o *Options) {
			setWalk(o, "/ataniq/probe", func(w *SourceWalk) { w.Root, w.Rel = "/opt/ataniq/bin/probe", "." })
		}, "path of the walk of /ataniq/probe", "names the folder and not a file"},
		{"a walk that starts in a folder the lead writes", func(o *Options) {
			// A source cannot lie there (rule G1), so the walk is given the
			// folder alone: it would be opened by a name the lead's parent
			// folder holds, which is fine, but from inside the tree is not.
			setWalk(o, "/work/demo", func(w *SourceWalk) { w.Root, w.Rel = "/state/leads/arc-lead/tree", "." })
		}, "source walks", "a folder the lead can write"},
	} {
		t.Run(c.name, func(t *testing.T) {
			o := options(in, allPresent(t, in))
			c.change(&o)
			p, err := Build(in, o)
			if err == nil {
				t.Fatalf("Build accepted it: %q", p.PassFDs)
			}
			if r := refusal(t, err); r.Field != c.field || !strings.Contains(r.Reason, c.want) {
				t.Errorf("refusal = %q / %q, want %q / ...%q...", r.Field, r.Reason, c.field, c.want)
			}
			good := mustBuild(t, in, options(in, allPresent(t, in)))
			if !hasKind(Verify(good, in, o), ProbOptions) {
				t.Errorf("Verify accepted the options: %v", Verify(good, in, o))
			}
		})
	}
}

// A walk that names the folder itself is a walk like any other: "." is
// accepted for a folder, and the plan passes it on.
func TestWalkOfTheFolderItself(t *testing.T) {
	in := fixture(profile.Claude)
	o := options(in, nil)
	setWalk(&o, "/ataniq/qargi", func(w *SourceWalk) { w.Root, w.Rel = "/opt/ataniq/qargi", "." })
	p := mustBuild(t, in, o)
	w := walkOf(t, p, "/opt/ataniq/qargi")
	if w.Root != "/opt/ataniq/qargi" || w.Rel != "." || w.Path() != "/opt/ataniq/qargi" {
		t.Errorf("walk = %+v", w)
	}
	if probs := Verify(p, in, o); len(probs) != 0 {
		t.Errorf("%v", probs)
	}
	// Options that split the same source otherwise describe another walk: the
	// plan built for one does not verify against the other.
	if probs := Verify(p, in, options(in, nil)); !hasKind(probs, ProbWalk) {
		t.Errorf("a plan with another walk for a row was accepted: %v", probs)
	}
	if got := (SourceWalk{Root: "/", Rel: "data"}).Path(); got != "/data" {
		t.Errorf("a source below the root of the file system: %q", got)
	}
}

func TestDescriptorCountCap(t *testing.T) {
	for _, c := range []struct {
		walks, files int
		want         string
	}{
		{23, 1, ""}, {23 + 64, 2, ""}, {MaxPassFDs - 2, 2, ""}, {MaxPassFDs - 1, 1, ""},
		{MaxPassFDs - 1, 2, "above its cap of 128"}, {MaxPassFDs, 1, "above its cap of 128"}, {2000, 2, "above its cap"},
	} {
		err := checkFDCount(c.walks, c.files)
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%d walks and %d files: %v", c.walks, c.files, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%d walks and %d files: %v, want a refusal with %q", c.walks, c.files, err, c.want)
		}
	}
	// The most a valid profile can ask for is under the cap, and the last
	// number a bind source can get is a descriptor number.
	if most := 23 + 64 + 2; most > MaxPassFDs {
		t.Errorf("the most descriptors of a profile, %d, is above the cap %d", most, MaxPassFDs)
	}
	if MinBindFD+MaxPassFDs+1 > MaxBindFD {
		t.Errorf("the numbers from %d up for %d sources pass %d", MinBindFD, MaxPassFDs, MaxBindFD)
	}
	// With every placeholder the profile allows the plan is built, verified
	// and still far under the command-line cap.
	in := withSeccomp(fixture(profile.Claude))
	for i := 0; i < 64; i++ {
		in.ExtraPlaceholders = append(in.ExtraPlaceholders, profile.Placeholder{Rel: "p" + strconv.Itoa(i), Source: "/opt/ataniq/placeholders/p" + strconv.Itoa(i)})
	}
	o := options(in, allPresent(t, in))
	p := mustBuild(t, in, o)
	if len(p.Walks) != 23+64 || len(p.Walks)+len(p.Files) != 89 {
		t.Errorf("%d walks and %d files", len(p.Walks), len(p.Files))
	}
	if n := argvBytes(p.Argv); n > MaxArgvBytes/4 {
		t.Errorf("the command line of the largest table is %d bytes of %d", n, MaxArgvBytes)
	}
}

func TestBindDescriptorNote(t *testing.T) {
	everyPlan(t, func(name string, p *Plan, _ profile.Inputs, _ Options) {
		if !containsNote(p.Notes, NoteBindFDs) {
			t.Errorf("%s: the note on descriptors is missing", name)
		}
	})
	for _, want := range []string{
		"rule B1", "--bind-fd", "--ro-bind-fd", "never by path", "pass-fds step", "RESOLVE_BENEATH", "RESOLVE_NO_SYMLINKS",
		"checks on the descriptor", "before bubblewrap starts",
		"whether bubblewrap mounts the object behind the descriptor", "closes the descriptor before the command inside starts",
		"qualification items", "[qualify]",
	} {
		if !strings.Contains(NoteBindFDs, want) {
			t.Errorf("the note on descriptors does not say %q", want)
		}
	}
	// The other note no longer says that bubblewrap resolves every source.
	if !strings.Contains(NoteBindSources, "bound from a descriptor") || !strings.Contains(NoteBindSources, "bound by path") {
		t.Errorf("the note on bind sources does not say which rows are bound how: %q", NoteBindSources)
	}
}

// The mutations of a plan that touch the descriptors and the walks.
func TestVerifyBindDescriptorMutations(t *testing.T) {
	const (
		qargi = "/opt/ataniq/qargi"
		home  = "/state/leads/arc-lead/home"
		tree  = "/state/leads/arc-lead/tree"
		probe = "/opt/ataniq/bin/probe"
	)
	// byPath turns the mount of a row into a bind by path of its source.
	byPath := func(t *testing.T, p *Plan, source string) {
		i := findBind(t, p, source)
		p.Bwrap[i] = strings.TrimSuffix(p.Bwrap[i], "-fd")
		p.Bwrap[i+1] = source
	}
	muts := []mutation{
		{"a read-only row Ataniq supplies bound by path", ProbBindByPath, false, func(t *testing.T, p *Plan) { byPath(t, p, qargi) }},
		{"the lead's home bound by path", ProbBindByPath, false, func(t *testing.T, p *Plan) { byPath(t, p, home) }},
		{"a git row bound by path", ProbBindByPath, false, func(t *testing.T, p *Plan) { byPath(t, p, "/state/leads/arc-lead/gitmeta/config") }},
		{"a row bound by path and its walk dropped", ProbBindByPath, false, func(t *testing.T, p *Plan) {
			byPath(t, p, qargi)
			i := findWalk(t, p, qargi)
			p.PassFDs = append(p.PassFDs[:i], p.PassFDs[i+4:]...)
		}},
		{"a row bound by path, everywhere", ProbBindByPath, false, func(t *testing.T, p *Plan) {
			w := walkOf(t, p, qargi)
			byPath(t, p, qargi)
			i := findWalk(t, p, qargi)
			p.PassFDs = append(p.PassFDs[:i], p.PassFDs[i+4:]...)
			for n := range p.Walks {
				if p.Walks[n] == w {
					p.Walks = append(p.Walks[:n], p.Walks[n+1:]...)
					break
				}
			}
			p.Mounts[indexOfMount(t, p, "/ataniq/qargi")].FD = 0
		}},
		{"a row of the host bound from a descriptor", ProbBindFD, false, func(t *testing.T, p *Plan) {
			i := find(t, p, "--ro-bind", "/usr")
			p.Bwrap[i], p.Bwrap[i+1] = "--ro-bind-fd", "99"
			p.PassFDs = append(p.PassFDs[:len(p.PassFDs)-1], "--walk-dir", "99", "/", "usr", "--")
		}},
		{"two mounts bound from one descriptor", ProbBindFD, false, func(t *testing.T, p *Plan) {
			p.Bwrap[findBind(t, p, qargi)+1] = strconv.Itoa(walkOf(t, p, home).FD)
		}},
		{"a mount bound from a descriptor no walk opens", ProbBindFD, false, func(t *testing.T, p *Plan) { p.Bwrap[findBind(t, p, qargi)+1] = "99" }},
		{"a mount bound from descriptor 0", ProbBindFD, false, func(t *testing.T, p *Plan) { p.Bwrap[findBind(t, p, qargi)+1] = "0" }},
		{"a mount bound from a descriptor with a leading zero", ProbBindFD, false, func(t *testing.T, p *Plan) {
			i := findBind(t, p, qargi)
			p.Bwrap[i+1] = "0" + p.Bwrap[i+1]
		}},
		{"a mount bound from a descriptor that is no number", ProbBindFD, false, func(t *testing.T, p *Plan) { p.Bwrap[findBind(t, p, qargi)+1] = "x" }},
		{"a mount bound from descriptor 1024", ProbBindFD, false, func(t *testing.T, p *Plan) { p.Bwrap[findBind(t, p, qargi)+1] = "1024" }},
		{"a walk whose descriptor no mount binds", ProbBindFD, false, func(t *testing.T, p *Plan) {
			p.PassFDs = append(p.PassFDs[:len(p.PassFDs)-1], "--walk-dir", "99", "/home", "owner", "--")
		}},
		{"a mount removed and its walk left", ProbBindFD, false, func(t *testing.T, p *Plan) { cut(p, findBind(t, p, qargi)) }},
		{"two walks at one descriptor", ProbBindFD, false, func(t *testing.T, p *Plan) {
			p.PassFDs[findWalk(t, p, qargi)+1] = strconv.Itoa(walkOf(t, p, home).FD)
		}},
		{"a second walk at the descriptor of a row, to the owner's home", ProbBindFD, false, func(t *testing.T, p *Plan) {
			p.PassFDs = append(p.PassFDs[:len(p.PassFDs)-1], "--walk-dir", strconv.Itoa(walkOf(t, p, home).FD), "/home", "owner", "--")
		}},
		{"a walk with a descriptor that is no number", ProbBindFD, false, func(t *testing.T, p *Plan) { p.PassFDs[findWalk(t, p, qargi)+1] = "+26" }},
		{"a bind descriptor that is the report descriptor", ProbBindFD, false, func(t *testing.T, p *Plan) {
			p.Bwrap[findBind(t, p, qargi)+1] = "3"
			p.PassFDs[findWalk(t, p, qargi)+1] = "3"
		}},
		{"a bind descriptor that is the seccomp descriptor", ProbBindFD, true, func(t *testing.T, p *Plan) {
			p.Bwrap[findBind(t, p, qargi)+1] = "7"
			p.PassFDs[findWalk(t, p, qargi)+1] = "7"
		}},

		{"a walk for a folder made one for a file", ProbWalk, false, func(t *testing.T, p *Plan) { p.PassFDs[findWalk(t, p, qargi)] = "--walk-file" }},
		{"a walk for a file made one for a folder", ProbWalk, false, func(t *testing.T, p *Plan) { p.PassFDs[findWalk(t, p, probe)] = "--walk-dir" }},
		{"a walk that starts lower, to the same source", ProbWalk, false, func(t *testing.T, p *Plan) {
			rewalk(t, p, home, "/state/leads/arc-lead", "home")
		}},
		{"a walk that starts at the source itself", ProbWalk, false, func(t *testing.T, p *Plan) { rewalk(t, p, tree, tree, ".") }},
		{"a walk that starts higher, to the same source", ProbWalk, false, func(t *testing.T, p *Plan) {
			rewalk(t, p, qargi, "/opt", "ataniq/qargi")
		}},
		{"the descriptors of two rows swapped in the mounts", ProbWalk, false, func(t *testing.T, p *Plan) {
			i, j := findBind(t, p, qargi), findBind(t, p, home)
			p.Bwrap[i+1], p.Bwrap[j+1] = p.Bwrap[j+1], p.Bwrap[i+1]
		}},
		{"the descriptors of two rows swapped in the mounts and the walks", ProbWalk, false, func(t *testing.T, p *Plan) {
			i, j := findBind(t, p, qargi), findBind(t, p, home)
			a, b := findWalk(t, p, qargi), findWalk(t, p, home)
			p.Bwrap[i+1], p.Bwrap[j+1] = p.Bwrap[j+1], p.Bwrap[i+1]
			p.PassFDs[a+1], p.PassFDs[b+1] = p.PassFDs[b+1], p.PassFDs[a+1]
		}},
		{"a row bound from another free descriptor, in the mount and the walk", ProbWalk, false, func(t *testing.T, p *Plan) {
			p.Bwrap[findBind(t, p, qargi)+1] = "99"
			p.PassFDs[findWalk(t, p, qargi)+1] = "99"
		}},
		{"Walks dropped", ProbWalk, false, func(t *testing.T, p *Plan) { p.Walks = nil }},
		{"Walks entry dropped", ProbWalk, false, func(t *testing.T, p *Plan) { p.Walks = p.Walks[1:] }},
		{"Walks entry added", ProbWalk, false, func(t *testing.T, p *Plan) {
			p.Walks = append(p.Walks, Walk{FD: 99, Inside: "/x", Root: "/home", Rel: "owner", Dir: true})
		}},
		{"Walks entries swapped", ProbWalk, false, func(t *testing.T, p *Plan) { p.Walks[0], p.Walks[1] = p.Walks[1], p.Walks[0] }},
		{"Walks descriptor changed", ProbWalk, false, func(t *testing.T, p *Plan) { p.Walks[2].FD = 99 }},
		{"Walks folder changed", ProbWalk, false, func(t *testing.T, p *Plan) { p.Walks[2].Root = "/home" }},
		{"Walks path changed", ProbWalk, false, func(t *testing.T, p *Plan) { p.Walks[2].Rel = "owner" }},
		{"Walks type changed", ProbWalk, false, func(t *testing.T, p *Plan) { p.Walks[2].Dir = !p.Walks[2].Dir }},
		{"Walks inside path changed", ProbWalk, false, func(t *testing.T, p *Plan) { p.Walks[2].Inside = "/x" }},

		{"a walk to another source", ProbMountDiffers, false, func(t *testing.T, p *Plan) { rewalk(t, p, qargi, "/opt/ataniq", "other") }},
		{"a walk to another source, in the step, the walks and the mounts", ProbMountDiffers, false, func(t *testing.T, p *Plan) {
			w := walkOf(t, p, qargi)
			rewalk(t, p, qargi, "/opt/ataniq", "other")
			for n := range p.Walks {
				if p.Walks[n] == w {
					p.Walks[n].Rel = "other"
				}
			}
			p.Mounts[indexOfMount(t, p, "/ataniq/qargi")].Source = "/opt/ataniq/other"
		}},
		{"a walk into the owner's ssh folder", ProbNever, false, func(t *testing.T, p *Plan) { rewalk(t, p, qargi, "/home/owner", ".ssh") }},
		{"a walk into the lead's tree", ProbLeadWritable, false, func(t *testing.T, p *Plan) {
			rewalk(t, p, "/state/leads/arc-lead/gitmeta/hooks", "/state", "leads/arc-lead/tree/.git/hooks")
		}},
		{"a walk with a path that is not clean", ProbNever, false, func(t *testing.T, p *Plan) {
			rewalk(t, p, qargi, "/opt/ataniq/qargi", "../../../home/owner")
		}},
		{"a walk option dropped", ProbPassFDs, false, func(t *testing.T, p *Plan) {
			i := findWalk(t, p, qargi)
			p.PassFDs = append(p.PassFDs[:i], p.PassFDs[i+4:]...)
		}},
		{"a walk option cut short", ProbPassFDs, false, func(t *testing.T, p *Plan) { p.PassFDs = append(p.PassFDs[:len(p.PassFDs)-2], "--") }},
		{"the walks before the files", ProbPassFDs, false, func(t *testing.T, p *Plan) {
			step := append([]string{}, p.PassFDs[:2]...)
			step = append(step, p.PassFDs[4:len(p.PassFDs)-1]...)
			p.PassFDs = append(append(step, p.PassFDs[2:4]...), "--")
		}},
		{"the walks in another order", ProbPassFDs, false, func(t *testing.T, p *Plan) {
			a, b := findWalk(t, p, qargi), findWalk(t, p, home)
			for k := 0; k < 4; k++ {
				p.PassFDs[a+k], p.PassFDs[b+k] = p.PassFDs[b+k], p.PassFDs[a+k]
			}
		}},
		{"Mounts descriptor changed", ProbMountList, false, func(t *testing.T, p *Plan) { p.Mounts[indexOfMount(t, p, "/ataniq/qargi")].FD = 99 }},
		{"Mounts descriptor dropped", ProbMountList, false, func(t *testing.T, p *Plan) { p.Mounts[indexOfMount(t, p, "/ataniq/qargi")].FD = 0 }},
		{"Mounts descriptor given to a row of the host", ProbMountList, false, func(t *testing.T, p *Plan) { p.Mounts[indexOfMount(t, p, "/usr")].FD = 10 }},
		{"descriptor note dropped", ProbNotes, false, func(t *testing.T, p *Plan) { p.Notes = removeNote(p.Notes, NoteBindFDs) }},
	}

	plain, plainIn, plainOpt := basePlan(t)
	secIn := withSeccomp(fixture(profile.Claude))
	secOpt := options(secIn, allPresent(t, secIn))
	sec := mustBuild(t, secIn, secOpt)
	seen := map[ProblemKind]bool{}
	for _, m := range muts {
		t.Run(m.name, func(t *testing.T) {
			base, in, opt := plain, plainIn, plainOpt
			if m.seccomp {
				base, in, opt = sec, secIn, secOpt
			}
			p := clonePlan(base)
			m.do(t, p)
			resync(p)
			probs := Verify(p, in, opt)
			if !hasKind(probs, m.want) {
				t.Fatalf("want a %q problem, got %v", m.want, probs)
			}
			// Build's own last word refuses the same plan.
			if err := assertPlan(p, in, opt); err == nil {
				t.Error("assertPlan accepted it")
			}
			seen[m.want] = true
		})
	}
	for _, k := range []ProblemKind{ProbBindByPath, ProbBindFD, ProbWalk} {
		if !seen[k] {
			t.Errorf("no mutation raises %q", k)
		}
	}
}

// A problem about a descriptor or a walk names rows of the table and
// positions, never the folder or the path a tampered plan walks.
func TestBindDescriptorProblemsDoNotCarryPlanText(t *testing.T) {
	base, in, o := basePlan(t)
	for i := range base.Walks {
		p := clonePlan(base)
		p.Walks[i].Inside, p.Walks[i].Root, p.Walks[i].Rel = canary, canary, canary
		for _, pr := range Verify(p, in, o) {
			if strings.Contains(pr.String(), "hunter2") {
				t.Errorf("Walks[%d]: a problem repeats text of the plan: %s", i, pr)
			}
		}
	}
	// A walk of the step sent to a path that holds the canary, with the mount
	// and the lists changed to agree.
	p := clonePlan(base)
	rewalk(t, p, "/opt/ataniq/qargi", "/"+canary, canary)
	resync(p)
	probs := Verify(p, in, o)
	if len(probs) == 0 {
		t.Fatal("Verify found no problem")
	}
	for _, pr := range probs {
		if strings.Contains(pr.String(), "hunter2") {
			t.Errorf("a problem repeats text of the plan: %s", pr)
		}
	}
}

// The pairing reads the step back on its own: a hostile step is a set of
// problems, never a panic.
func TestParseStepWalks(t *testing.T) {
	p, _, _ := basePlan(t)
	got := parseStepWalks(p.PassFDs)
	if len(got) != len(p.Walks) {
		t.Fatalf("%d walks read back, the plan has %d", len(got), len(p.Walks))
	}
	for i, w := range p.Walks {
		if got[i].fd != w.FD || got[i].root != w.Root || got[i].rel != w.Rel || got[i].dir != w.Dir {
			t.Errorf("walk %d read back as %+v, want %+v", i, *got[i], w)
		}
	}
	for _, step := range [][]string{
		nil, {"/p"}, {"/p", "pass-fds"}, {"/p", "pass-fds", "--walk-dir"}, {"/p", "pass-fds", "--walk-dir", "10", "/a"},
		{"/p", "pass-fds", "--walk-dir", "10", "/a", "b"}, {"/p", "pass-fds", "--read"}, {"/p", "pass-fds", "--other", "--walk-dir", "10", "/a", "b", "--"},
	} {
		parseStepWalks(step)
	}
	if w := parseStepWalks([]string{"/p", "pass-fds", "--write", "3=/r", "--walk-file", "011", "/a", "b", "--walk-dir", "12", "/c", "d", "--"}); len(w) != 2 || w[0].fd != 0 || w[1].fd != 12 || !w[1].dir || w[0].dir {
		t.Errorf("walks read back: %+v", w)
	}
	for s, want := range map[string]int{"3": 3, "10": 10, "1023": 1023, "1024": 0, "2": 0, "0": 0, "": 0, "010": 0, "+10": 0, "-1": 0, "1e1": 0, " 10": 0, "99999": 0} {
		if got := fdNumber(s); got != want {
			t.Errorf("fdNumber(%q) = %d, want %d", s, got, want)
		}
	}
}
