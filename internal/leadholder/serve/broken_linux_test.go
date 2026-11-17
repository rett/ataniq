//go:build linux

package serve

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"ataniq/internal/leadholder/session"
	"ataniq/internal/leadholder/wire"
)

// shortWrite makes a write to an agent that does not read time out soon.
func shortWrite(o *envOpts) {
	o.session = func(c *session.Config) { c.PTY.WriteTimeout = 300 * time.Millisecond }
}

// bigInput is more than the terminal's input buffer holds, and the most one
// Input frame carries, so that a write to an agent that does not read cannot
// finish.
var bigInput = strings.Repeat("x", wire.MaxClientPayload)

// When the session's input is broken a writer's Input and Paste are refused by
// the session. The writer gets one input-broken notice for its connection,
// which is counted and logged once, and status reports the indication.
func TestWriterToldOnceThatTheInputIsBroken(t *testing.T) {
	e := newEnv(t, "raw; say READY; wait", shortWrite)
	waitScreen(t, e.sess, "READY")
	w := e.attach(wire.RoleWriter)
	w.waitSnapshots(1)
	v := e.attach(wire.RoleViewer)
	v.waitSnapshots(1)

	// Any write that fails or times out breaks it (here one made by hand).
	if h := e.sess.Input([]byte(bigInput)); h.Err == nil {
		t.Fatalf("a write to an agent that does not read finished: %+v", h)
	}
	if !e.sess.InputBroken() {
		t.Fatal("the input is not broken")
	}
	if w.noticeCount(reasonInputBroken) != 0 {
		t.Fatal("told before anything was typed")
	}

	w.input("a")
	n := w.waitNotice(reasonInputBroken)
	if n.Text == "" {
		t.Error("an empty notice")
	}
	w.input("b")
	w.paste("c")
	e.waitCount("input_refused", 3) // the three frames were refused by the session, in order
	if got := w.noticeCount(reasonInputBroken); got != 1 {
		t.Errorf("%d notices for one connection", got)
	}
	if got := e.count(cntInputBroken); got != 1 {
		t.Errorf("counted %d", got)
	}
	e.waitEvent("the first refusal", named("refusal", "input-broken"))
	if v.noticeCount(reasonInputBroken) != 0 {
		t.Error("the viewer was told")
	}

	st := e.mustControl(wire.Request{Op: wire.OpStatus, Epoch: testEpoch}).Status
	if st.Counters["input_broken"] != 1 || st.Counters[cntInputBroken] != 1 {
		t.Errorf("status counters: input_broken %d, %s %d", st.Counters["input_broken"], cntInputBroken, st.Counters[cntInputBroken])
	}

	// Another connection of a writer is told once for itself.
	w.detach()
	w.waitEnd()
	w2 := e.attach(wire.RoleWriter)
	w2.waitSnapshots(1)
	w2.input("d")
	w2.waitNotice(reasonInputBroken)
	if got := e.count(cntInputBroken); got != 2 {
		t.Errorf("counted %d, want 2", got)
	}
}

// The write that breaks the input is the writer's own: that writer is told too,
// and a later frame is not told again.
func TestWriterWhoseWriteBrokeTheInputIsTold(t *testing.T) {
	e := newEnv(t, "raw; say READY; wait", shortWrite)
	waitScreen(t, e.sess, "READY")
	w := e.attach(wire.RoleWriter)
	w.waitSnapshots(1)
	w.input(bigInput)
	w.waitNotice(reasonInputBroken)
	if !e.sess.InputBroken() {
		t.Error("the input is not broken")
	}
	w.input("a")
	e.waitCount("input_refused", 1)
	if got := w.noticeCount(reasonInputBroken); got != 1 {
		t.Errorf("%d notices", got)
	}
	// The output goes on and the connection stays.
	w.waitChecks(1)
	w.noProblems()
}

// The service adds its counts to the session's with AddCount, which refuses
// the names of the session's own counters: no counter of this package has one
// of them, none is too long, and there are no more than the session keeps.
func TestNoCounterNameIsReserved(t *testing.T) {
	reserved := map[string]bool{}
	for _, n := range session.ReservedCounters() {
		reserved[n] = true
	}
	// The names are the string constants cnt* of the package's own files.
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]string{} // constant -> counter name
	fset := token.NewFileSet()
	for _, en := range entries {
		if !strings.HasSuffix(en.Name(), ".go") || strings.HasSuffix(en.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, en.Name(), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, sp := range gd.Specs {
				vs := sp.(*ast.ValueSpec)
				for i, id := range vs.Names {
					if !strings.HasPrefix(id.Name, "cnt") || i >= len(vs.Values) {
						continue
					}
					if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
						v, err := strconv.Unquote(lit.Value)
						if err != nil {
							t.Fatal(err)
						}
						names[id.Name] = v
					}
				}
			}
		}
	}
	if len(names) < 20 {
		t.Fatalf("found only %d counter constants: %v", len(names), names)
	}
	seen := map[string]string{}
	for id, name := range names {
		if reserved[name] {
			t.Errorf("%s = %q is one of the session's reserved counters", id, name)
		}
		if len(name) == 0 || len(name) > session.MaxCounterName {
			t.Errorf("%s = %q is not a name the session keeps (at most %d bytes)", id, name, session.MaxCounterName)
		}
		if other, dup := seen[name]; dup {
			t.Errorf("%s and %s are both %q", id, other, name)
		}
		seen[name] = id
	}
	if len(names) > session.MaxExtraCounters {
		t.Errorf("%d counters, the session keeps at most %d names", len(names), session.MaxExtraCounters)
	}
	// And a real session accepts every one of them.
	e, _ := newBase(t, "raw; wait")
	for id, name := range names {
		if err := e.sess.AddCount(name, 0); err != nil {
			t.Errorf("AddCount(%s = %q): %v", id, name, err)
		}
	}
	if n := e.sess.Counters()["extra_counters_dropped"]; n != 0 {
		t.Errorf("%d counter names were dropped", n)
	}
}
