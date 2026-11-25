//go:build linux && ataniq_canary

package sandboxstart

import (
	"slices"
	"strings"
	"testing"

	"ataniq/internal/leadsandbox/profile"
)

// BindTable is the start's own table, with each row's concrete host source:
// the rows are those of the inputs the start checks, every read-only and
// read-write row names a host source, the start's plan binds each of them at
// its inside path, and a changed source (the agent install here) is the
// source of its row. A refusal of the inputs is the start's refusal.
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
	st, err := w.run_()
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, b := range binds {
		if b.Kind != profile.KindRoBind && b.Kind != profile.KindRwBind {
			continue
		}
		n++
		if b.Source == "" || !strings.HasPrefix(b.Source, "/") {
			t.Errorf("the row at %s has no host source: %+v", b.Inside, b)
		}
		if !slices.Contains(st.Plan.Bwrap, b.Inside) {
			t.Errorf("the start's plan does not bind %s (%s from %s)", b.Inside, b.SourceRole, b.Source)
		}
	}
	if n < 5 {
		t.Fatalf("the table has %d binds", n)
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
