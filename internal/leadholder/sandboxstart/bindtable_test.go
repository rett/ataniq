//go:build linux && ataniq_canary

package sandboxstart

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ataniq/internal/leadsandbox/plan"
	"ataniq/internal/leadsandbox/profile"
)

// BindTable is the start's own table, with each row's concrete host source:
// the rows are those of the inputs the start checks, every read-only and
// read-write row names a host source, the start's plan binds each of them at
// its inside path with the bind operation of its kind and from its source,
// and a changed source (the agent install here) is the source of its row. A
// refusal of the inputs is the start's refusal.
//
// The plan is built by the start's own steps up to the plan (validate, the
// resolv.conf, the resolution of the sources, the plan), on the fake host,
// without the canary step: the plan does not use the canary's facts, so no
// socket or listener is made, and the test runs where sockets are denied.
// For each read-only or read-write row the plan emits, the bubblewrap
// option is the one of its kind: a row Ataniq supplies is bound from a
// descriptor (--ro-bind-fd or --bind-fd FD INSIDE) whose walk in the
// pass-through step (--walk-dir or --walk-file FD ROOT REL) leads to the
// row's source; a row of the host is bound by path (--ro-bind SOURCE
// INSIDE) from its source as resolved.
func TestBindTableIsTheStartsTableWithItsSources(t *testing.T) {
	w := newWorld(t)
	uid, gid := w.sys.IDs()
	binds, err := BindTable(w.in, uid, gid)
	if err != nil {
		t.Fatal(err)
	}
	pin, err := w.in.checkedProfileInputs(uid, gid)
	if err != nil {
		t.Fatal(err)
	}
	want, err := pin.Binds()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(binds, want) {
		t.Fatalf("BindTable is not the table of the start's inputs:\n%v\n%v", binds, want)
	}
	a := &attempt{ctx: context.Background(), in: w.in, sys: w.sys, launch: w.lch}
	defer a.closeAll()
	for i, step := range []func() error{a.validate, a.resolv, a.resolveSources, a.buildPlan} {
		if err := step(); err != nil {
			t.Fatalf("step %d of the start: %v", i, err)
		}
	}
	if n := w.sys.count["NewCanary"]; n != 0 || a.canary != nil {
		t.Fatalf("the plan was built with a canary (%d made)", n)
	}
	pl := a.pl
	emitted := map[string]plan.Mount{}
	for _, m := range pl.Mounts {
		emitted[m.Inside] = m
	}
	hostSources := a.res.HostSources()
	n, fds := 0, 0
	for _, b := range binds {
		if b.Kind != profile.KindRoBind && b.Kind != profile.KindRwBind {
			continue
		}
		if b.Source == "" || !strings.HasPrefix(b.Source, "/") {
			t.Errorf("the row at %s has no host source: %+v", b.Inside, b)
		}
		m, ok := emitted[b.Inside]
		if !ok {
			if !b.Optional {
				t.Errorf("the start's plan does not emit the row at %s (%s from %s)", b.Inside, b.SourceRole, b.Source)
			}
			continue
		}
		n++
		op := bwrapBindOf(pl.Bwrap, b.Inside)
		if b.SourceRole == profile.RoleHost {
			want := []string{"--ro-bind", hostSources[b.Inside], b.Inside}
			if b.Kind != profile.KindRoBind || m.FD != 0 || !slices.Equal(op, want) || hostSources[b.Inside] == "" {
				t.Errorf("the host row at %s is bound by %q, want %q", b.Inside, op, want)
			}
			continue
		}
		fds++
		name := "--ro-bind-fd"
		if b.Kind == profile.KindRwBind {
			name = "--bind-fd"
		}
		if want := []string{name, strconv.Itoa(m.FD), b.Inside}; m.FD <= 0 || !slices.Equal(op, want) {
			t.Errorf("the row at %s is bound by %q, want %q", b.Inside, op, want)
			continue
		}
		var walk *plan.Walk
		for i := range pl.Walks {
			if pl.Walks[i].FD == m.FD {
				walk = &pl.Walks[i]
			}
		}
		if walk == nil || walk.Inside != b.Inside || walk.Path() != b.Source || m.Source != b.Source {
			t.Errorf("the descriptor %d of %s does not walk to its source %s: %+v", m.FD, b.Inside, b.Source, walk)
			continue
		}
		kind := "--walk-file"
		if walk.Dir {
			kind = "--walk-dir"
		}
		if !slices.ContainsFunc(windows(pl.PassFDs, 4), func(w []string) bool {
			return slices.Equal(w, []string{kind, strconv.Itoa(m.FD), walk.Root, walk.Rel})
		}) {
			t.Errorf("the pass-through step does not open %s %d %s %s for %s", kind, m.FD, walk.Root, walk.Rel, b.Inside)
		}
	}
	if n < 5 || fds < 3 {
		t.Fatalf("the plan binds %d rows of the table, %d from descriptors", n, fds)
	}

	moved := w.in
	moved.Sources.AgentInstall = w.in.Sources.AgentInstall + "-moved"
	binds, err = BindTable(moved, uid, gid)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, b := range binds {
		if b.SourceRole == profile.RoleAgent {
			found = true
			if b.Source != moved.Sources.AgentInstall {
				t.Errorf("the agent's row binds %s, not the moved install %s", b.Source, moved.Sources.AgentInstall)
			}
		}
	}
	if !found {
		t.Error("the table has no row of the agent install")
	}

	bad := w.in
	bad.Runtime = "no-such-runtime"
	_, err = BindTable(bad, uid, gid)
	_, derr := ProfileDigest(bad, uid, gid)
	if err == nil || derr == nil || err.Error() != derr.Error() {
		t.Errorf("BindTable of inputs the start refuses: %v; ProfileDigest: %v", err, derr)
	}
}

// bwrapBindOf returns the bubblewrap option that binds inside, with its two
// operands, or nil: the plan's bind options are an option, a source or a
// descriptor, and the inside path.
func bwrapBindOf(args []string, inside string) []string {
	for i := 0; i+2 < len(args); i++ {
		switch args[i] {
		case "--ro-bind", "--bind", "--ro-bind-fd", "--bind-fd":
			if args[i+2] == inside {
				return args[i : i+3]
			}
		}
	}
	return nil
}

// windows returns every run of n consecutive elements of s.
func windows(s []string, n int) [][]string {
	var out [][]string
	for i := 0; i+n <= len(s); i++ {
		out = append(out, s[i:i+n])
	}
	return out
}
