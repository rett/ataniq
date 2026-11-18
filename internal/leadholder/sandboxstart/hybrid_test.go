//go:build linux && ataniq_canary

package sandboxstart

import (
	"errors"
	"os"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"ataniq/internal/leadsandbox/assert"
)

// hybridWorld is a world whose report channel and bound folder are the real
// ones, in the world's real runtime folder. Its launcher plays the pass-through
// step and the probe: it reads the spec the sequence wrote, opens the FIFO
// write-only and writes the report line for the spec's digest. Nothing starts.
func hybridWorld(t *testing.T) *world {
	t.Helper()
	w, _ := hybridWorldWriters(t)
	return w
}

// hybridWorldWriters is hybridWorld with the probe's write ends, which the test
// closes when it is done with them.
func hybridWorldWriters(t *testing.T) (*world, *[]*os.File) {
	t.Helper()
	var writers []*os.File
	w := newWorld(t)
	w.sys.real = realSys(t)
	w.lch.onLaunch = func(argv []string) error {
		data, err := os.ReadFile(boundSpecDir(w.run, sampleRole) + "/" + specFileName)
		if err != nil {
			return err
		}
		spec, err := assert.DecodeSpecBytes(data)
		if err != nil {
			return err
		}
		f, err := os.OpenFile(ReportFIFO(w.run, sampleRole), os.O_WRONLY, 0)
		if err != nil {
			return err
		}
		writers = append(writers, f)
		t.Cleanup(func() { f.Close() })
		_, err = f.Write(probeLine(spec.Digest))
		return err
	}
	return w, &writers
}

func runtimeHolds(t *testing.T, w *world) []string {
	t.Helper()
	return names(t, RuntimeFolder(w.run, sampleRole))
}

// TestHybridStartLeavesTheRuntimeFolderEmpty runs a start that succeeds over the
// real FIFO and the real bound folder: the report line comes through the FIFO,
// and once the start is done the runtime folder holds nothing of it.
func TestHybridStartLeavesTheRuntimeFolderEmpty(t *testing.T) {
	w := hybridWorld(t)
	st, err := w.run_()
	if err != nil {
		t.Fatal(err)
	}
	if !st.Waived || st.Child == nil {
		t.Fatalf("waived %v, child %v", st.Waived, st.Child)
	}
	if got := runtimeHolds(t, w); len(got) != 0 {
		t.Errorf("the runtime folder holds %v after the start", got)
	}
	// The folders existed while the sequence ran: the spec the launcher read was
	// the one written.
	if len(w.sys.bounds) != 1 || len(w.sys.bounds[0].spec) == 0 {
		t.Error("no spec was written")
	}
}

// TestHybridFailureLeavesNothing makes the start fail after the FIFO and the
// folder exist, at each later step, and checks the runtime folder is as it was.
func TestHybridFailureLeavesNothing(t *testing.T) {
	boom := errors.New("injected failure")
	cases := []struct {
		step string
		set  func(w *world)
	}{
		{StepCanary, func(w *world) { w.sys.fail["NewCanary"] = boom }},
		{StepBuild, func(w *world) { w.in.AgentArgv = []string{"relative"} }},
		{StepVerify, func(w *world) { w.in.ExpectedDigest = strings.Repeat("0", 64) }},
		{StepSpec, func(w *world) { w.sys.fail["WriteSpec"] = boom }},
		{StepRecheck, func(w *world) { w.sys.failN["CheckMountPoints#2"] = boom }},
		{StepBindSources, func(w *world) { w.sys.bindRep = problemReport("/ataniq/agent") }},
		{StepLaunch, func(w *world) { w.lch.err = boom }},
		{StepReadReport, func(w *world) { w.lch.onLaunch = func([]string) error { return nil } }},
		{StepOutside, func(w *world) { w.sys.lock.held = false }},
		{StepTrailing, func(w *world) {
			inner := w.lch.onLaunch
			w.lch.onLaunch = func(argv []string) error {
				if err := inner(argv); err != nil {
					return err
				}
				f, err := os.OpenFile(ReportFIFO(w.run, sampleRole), os.O_WRONLY, 0)
				if err != nil {
					return err
				}
				defer f.Close()
				_, err = f.Write([]byte("more"))
				return err
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.step, func(t *testing.T) {
			w := hybridWorld(t)
			c.set(w)
			if c.step == StepReadReport {
				// The child exits without a line: the channel sees the end.
				w.lch.onLaunch = func([]string) error { return nil }
				w.in.TimeLimitSeconds = 1
			}
			_, err := w.run_()
			var se *StepError
			if !errors.As(err, &se) || se.Step != c.step {
				t.Fatalf("error %v, want a refusal at %s", err, c.step)
			}
			if got := runtimeHolds(t, w); len(got) != 0 {
				t.Errorf("the runtime folder holds %v after the failure", got)
			}
			assertNothingOpen(t, w)
			if w.lch.launches() == 1 && len(w.lch.kids) == 1 {
				if k := w.lch.kids[0]; len(k.stops) != 1 || k.closes != 1 {
					t.Errorf("child stops %v closes %d", k.stops, k.closes)
				}
			}
		})
	}
}

// TestHybridStartAfterAKilledHolder starts over the leftovers of a holder that
// was killed: the report FIFO and the bound folder it made.
func TestHybridStartAfterAKilledHolder(t *testing.T) {
	w := hybridWorld(t)
	rd := ReportFolder(w.run, sampleRole)
	mkdir(t, rd, 0o700)
	if err := unix.Mkfifo(rd+"/probe.fifo", 0o600); err != nil {
		t.Fatal(err)
	}
	bd := boundFolder(w.run, sampleRole)
	mkdir(t, bd+"/spec", 0o700)
	os.WriteFile(bd+"/resolv.conf", []byte("old"), 0o644)
	os.WriteFile(bd+"/spec/spec.json", []byte("old"), 0o600)
	if _, err := w.run_(); err != nil {
		t.Fatal(err)
	}
	if got := runtimeHolds(t, w); len(got) != 0 {
		t.Errorf("the runtime folder holds %v", got)
	}
}

// openFDs counts the descriptors of this process.
func openFDs(t *testing.T) int {
	t.Helper()
	ents, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skipf("no /proc/self/fd: %v", err)
	}
	return len(ents)
}

// TestHybridStartsLeakNoDescriptor runs starts that succeed and starts that fail
// at several steps, and counts the descriptors of the process: the sequence
// leaves none open once the probe's own write end is closed.
func TestHybridStartsLeakNoDescriptor(t *testing.T) {
	run := func(fail func(w *world)) {
		w, writers := hybridWorldWriters(t)
		if fail != nil {
			fail(w)
		}
		w.run_()
		for _, f := range *writers {
			f.Close()
		}
	}
	// A first start warms up the runtime's own descriptors (the poller).
	run(nil)
	before := openFDs(t)
	for i := 0; i < 3; i++ {
		run(nil)
		run(func(w *world) { w.sys.lock.held = false })
		run(func(w *world) { w.sys.fail["WriteSpec"] = errors.New("injected") })
		run(func(w *world) { w.lch.err = errors.New("injected") })
	}
	if after := openFDs(t); after != before {
		t.Errorf("%d descriptors before, %d after", before, after)
	}
}
