package switcher

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeProcs is procs over a table: alive processes and their start times,
// the process groups that hold a process, and the groups killed.
type fakeProcs struct {
	started map[int]uint64
	groups  map[int]bool
	killed  []int
}

func (f *fakeProcs) start(pid int) (uint64, bool, error) {
	st, ok := f.started[pid]
	return st, ok, nil
}

func (f *fakeProcs) groupAlive(pgid int) (bool, error) { return f.groups[pgid], nil }

func (f *fakeProcs) killGroup(pgid int) error {
	f.killed = append(f.killed, pgid)
	delete(f.started, pgid)
	delete(f.groups, pgid)
	return nil
}

// TestPackageStateUnknownBlocks: a package step that fails leaves the
// host's package state unknown, neither snapshot recorded as the host's;
// every switch and rollback is then refused, running nothing, but a switch
// to a release of the snapshot the step went to (which runs the step
// again) and recover --package-state, which settles it on the owner's word.
func TestPackageStateUnknownBlocks(t *testing.T) {
	e := active(t)
	e.release(rel3, "2026-11-15")
	e.runner.fail["ah-pkgstep"] = 1
	if code := e.switchTo(rel3); code != ExitFailed {
		t.Fatalf("exit %d", code)
	}
	e.release("0004-ddddddd", "2026-11-20")
	if rec := e.record(); rec.PackageState != PackageUnknown || rec.PackageFrom != "2026-11-01" || rec.PackageTo != "2026-11-15" || !strings.Contains(rec.Detail, "unknown") {
		t.Fatalf("record: %+v", rec)
	}
	if got := e.status(); !strings.Contains(got, "package snapshot: unknown: the package step from 2026-11-01 to 2026-11-15 did not finish") {
		t.Errorf("status:\n%s", got)
	}

	// Another snapshot, and the rollback, are refused before anything runs.
	e.runner.reset()
	if code := e.switchTo("0004-ddddddd"); code != ExitFailed {
		t.Fatalf("exit %d", code)
	}
	if rec := e.record(); rec.Error != CodePackageUnknown || rec.PackageState != PackageUnknown || !strings.Contains(rec.Detail, "recover --package-state") {
		t.Errorf("record: %+v", rec)
	}
	if code := e.rollBack("previous"); code != ExitFailed {
		t.Fatalf("rollback: exit %d", code)
	}
	if rec := e.record(); rec.Error != CodePackageUnknown || rec.PackageState != PackageUnknown {
		t.Errorf("record: %+v", rec)
	}
	e.ran(map[string]int{"ah-pkgstep": 0, "ah-noaicheck": 0, "reload-units": 0})
	e.links(rel2, rel1)

	// The owner's word: only one of the two ends.
	if code := e.recover(RecoverOptions{SetPackageState: true, PackageState: "2026-11-02"}); code != ExitFailed {
		t.Fatalf("recover another snapshot: exit %d", code)
	}
	if code := e.recover(RecoverOptions{SetPackageState: true, PackageState: "2026-11-01"}); code != ExitOK {
		t.Fatalf("recover: exit %d\n%s", code, e.last)
	}
	rec := e.record()
	if rec.PackageState != "" || rec.PackageSnapshot != "2026-11-01" || rec.PackageStatedAt == "" {
		t.Errorf("record: %+v", rec)
	}
	if got := e.status(); !strings.Contains(got, "package snapshot: 2026-11-01 (stated by the owner at") {
		t.Errorf("status:\n%s", got)
	}
	if code := e.recover(RecoverOptions{SetPackageState: true, PackageState: "2026-11-01"}); code != ExitFailed {
		t.Errorf("recover again: exit %d", code)
	}
	if code := e.switchTo("0004-ddddddd"); code != ExitOK {
		t.Fatalf("switch after recover: exit %d", code)
	}
	if !strings.Contains(strings.Join(e.runner.calls, "\n"), "--from 2026-11-01 --to 2026-11-20") {
		t.Errorf("the step did not run from the stated snapshot: %v", e.runner.calls)
	}
}

// TestPackageStepRunAgainSettles: on a host whose package state is
// unknown, a switch to a release of the snapshot the step went to runs the
// step again from where it started, and its success settles the state.
func TestPackageStepRunAgainSettles(t *testing.T) {
	e := active(t)
	e.release(rel3, "2026-11-15")
	e.runner.fail["ah-pkgstep"] = 1
	if code := e.switchTo(rel3); code != ExitFailed {
		t.Fatalf("exit %d", code)
	}
	e.runner.reset()
	if code := e.switchTo(rel3); code != ExitOK {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(strings.Join(e.runner.calls, "\n"), "--from 2026-11-01 --to 2026-11-15") {
		t.Errorf("commands: %v", e.runner.calls)
	}
	if rec := e.record(); rec.PackageState != "" || rec.PackageSnapshot != "2026-11-15" {
		t.Errorf("record: %+v", rec)
	}
}

// TestPackageStepHoldsTheLock: the package step is given the switch's lock
// file, so that a step that outlives its switch still holds the lock.
func TestPackageStepHoldsTheLock(t *testing.T) {
	e := active(t)
	e.release(rel3, "2026-11-15")
	if code := e.switchTo(rel3); code != ExitOK {
		t.Fatalf("exit %d", code)
	}
	for i, c := range e.runner.calls {
		if strings.Contains(c, "ah-pkgstep") {
			if len(e.runner.extra[i]) != 1 || filepath.Base(e.runner.extra[i][0].Name()) != LockFile {
				t.Errorf("the package step's extra files: %v", e.runner.extra[i])
			}
		} else if len(e.runner.extra[i]) != 0 {
			t.Errorf("%s was given extra files", c)
		}
	}
}

// TestInterruptedPackageStepMustHaveEnded: a resumed packages phase whose
// step was started first makes sure that step has ended: a process of its
// id and start time still running has its group killed; a process of its id
// with another start time is another process; processes left in its group
// with no such process are refused and left to the owner.
func TestInterruptedPackageStepMustHaveEnded(t *testing.T) {
	for _, tc := range []struct {
		name   string
		procs  *fakeProcs
		killed int
		code   int
	}{
		{"still running", &fakeProcs{started: map[int]uint64{4242: 777}, groups: map[int]bool{4242: true}}, 4242, ExitFailed},
		{"id reused", &fakeProcs{started: map[int]uint64{4242: 999}, groups: map[int]bool{4242: true}}, 0, ExitFailed},
		{"ended", &fakeProcs{started: map[int]uint64{}, groups: map[int]bool{}}, 0, ExitFailed},
		{"group left", &fakeProcs{started: map[int]uint64{}, groups: map[int]bool{4242: true}}, 0, ExitFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := active(t)
			e.release(rel3, "2026-11-15")
			e.runner.pid = 4242
			s := e.switcher()
			s.procs = &fakeProcs{started: map[int]uint64{4242: 777}}
			s.crashAt = func(p string) error {
				if p == CrashPackageRan {
					return errCrash
				}
				return nil
			}
			if code := s.Switch(context.Background(), rel3); code != ExitFailed {
				t.Fatalf("exit %d", code)
			}
			e.out.Reset()
			if j, ok := e.journal(); !ok || j.HelperPID != 4242 || j.HelperStart != 777 || j.PackageState != PackageStarted {
				t.Fatalf("journal: %+v", j)
			}
			s = e.switcher()
			s.procs = tc.procs
			code := s.Recover(context.Background(), resumeOnly)
			out := e.out.String()
			e.out.Reset()
			if code != tc.code {
				t.Fatalf("exit %d\n%s", code, out)
			}
			if tc.killed != 0 && (len(tc.procs.killed) != 1 || tc.procs.killed[0] != tc.killed) || tc.killed == 0 && len(tc.procs.killed) != 0 {
				t.Errorf("killed %v", tc.procs.killed)
			}
			_, pending := e.journal()
			if tc.name == "group left" {
				if !pending || !strings.Contains(out, "still run; stop them by hand") {
					t.Errorf("the journal was settled with processes left:\n%s", out)
				}
				return
			}
			if pending {
				t.Fatalf("journal left:\n%s", out)
			}
			if rec := e.record(); rec.PackageState != PackageUnknown {
				t.Errorf("record: %+v", rec)
			}
		})
	}
}

func TestParseStatStart(t *testing.T) {
	line := "4242 (a (b) c) S 1 4242 4242 0 -1 4194560 100 0 0 0 1 2 0 0 20 0 1 0 123456 1000 10\n"
	if n, ok, err := parseStatStart(line); err != nil || !ok || n != 123456 {
		t.Errorf("parseStatStart: %d, %v, %v", n, ok, err)
	}
	if _, _, err := parseStatStart("4242 (x) S 1"); err == nil {
		t.Errorf("a short line parsed")
	}
	// The process of the test itself has a start time.
	if _, ok, err := (procfs{}).start(os.Getpid()); err != nil || !ok {
		t.Errorf("start of this process: %v, %v", ok, err)
	}
	if alive, err := (procfs{}).groupAlive(1 << 30); err != nil || alive {
		t.Errorf("a group that cannot exist: %v, %v", alive, err)
	}
}

// TestExecRunnerExtraFilesAndStart: a command gets its extra files from fd
// 3 on, and OnStart its process id before it is waited for.
func TestExecRunnerExtraFilesAndStart(t *testing.T) {
	dir := t.TempDir()
	f, err := os.Create(filepath.Join(dir, "lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	prog := script(t, dir, "has-fd3", "[ -e /proc/self/fd/3 ] || exit 7\n")
	pid := 0
	_, err = ExecRunner{}.Run(context.Background(), Command{Argv: []string{prog}, Timeout: 10 * time.Second, ExtraFiles: []*os.File{f}, OnStart: func(p int) { pid = p }})
	if err != nil || pid <= 0 {
		t.Errorf("with fd 3: %v, pid %d", err, pid)
	}
	if _, err := (ExecRunner{}).Run(context.Background(), Command{Argv: []string{prog}, Timeout: 10 * time.Second}); err == nil {
		t.Errorf("fd 3 without extra files")
	}
}
