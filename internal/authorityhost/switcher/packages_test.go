package switcher

import (
	"context"
	"errors"
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

// TestLockedBySurvivingHelper: a switch refused because the lock is held
// says who holds it, from the journal: a package step that outlived its
// switch (its process id and start time match), which must be stopped
// by hand, or a switch still running; otherwise only that it is locked.
func TestLockedBySurvivingHelper(t *testing.T) {
	for _, tc := range []struct {
		name  string
		procs func() *fakeProcs
		want  string
	}{
		{"surviving helper", func() *fakeProcs { return &fakeProcs{started: map[int]uint64{4242: 777}} },
			"surviving helper pid 4242 (start time matches): stop it or reboot"},
		{"helper id reused", func() *fakeProcs { return &fakeProcs{started: map[int]uint64{4242: 999}} },
			"another ah-switch holds the lock"},
		{"switch running", func() *fakeProcs {
			return &fakeProcs{started: map[int]uint64{4242: 777, os.Getpid(): 5}}
		}, "an active ah-switch (process"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := active(t)
			e.release(rel3, "2026-11-15")
			e.runner.pid = 4242
			s := e.switcher()
			// The switch's own start time, as the journal records it.
			s.procs = &fakeProcs{started: map[int]uint64{4242: 777, os.Getpid(): 5}}
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
			if j, ok := e.journal(); !ok || j.HelperPID != 4242 || j.SwitchPID != os.Getpid() || j.SwitchStart != 5 {
				t.Fatalf("journal: %+v", j)
			}
			// The step, still running, keeps the lock.
			state, err := os.OpenRoot(filepath.Join(e.root, StateDir))
			if err != nil {
				t.Fatal(err)
			}
			defer state.Close()
			held, err := takeLock(state, true, false, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer held.release()
			s = e.switcher()
			procs := tc.procs()
			if tc.name != "switch running" {
				delete(procs.started, os.Getpid())
			}
			s.procs = procs
			code := s.Recover(context.Background(), resumeOnly)
			out := e.out.String()
			e.out.Reset()
			if code != ExitLocked || !strings.Contains(out, tc.want) {
				t.Errorf("exit %d\n%s", code, out)
			}
			if len(procs.killed) != 0 {
				t.Errorf("killed %v", procs.killed)
			}
		})
	}
}

// TestEarlyStateFailureKeepsTheRecord: a failure to read the state before
// anything else (releases/ unavailable, a link that is not what the switch
// writes) is reported and recorded nowhere: deploy.json, which holds the
// unknown package state, the counter evidence and any pending outcome,
// stays as it is, and once the state is readable again the package state
// still blocks. recover leaves it too.
func TestEarlyStateFailureKeepsTheRecord(t *testing.T) {
	releases := func(e *env) string { return filepath.Join(e.root, BaseDir, "releases") }
	for _, tc := range []struct {
		name          string
		spoil, repair func(e *env)
	}{
		{"releases missing", func(e *env) { os.Rename(releases(e), releases(e)+".away") }, func(e *env) { os.Rename(releases(e)+".away", releases(e)) }},
		{"releases writable by others", func(e *env) { os.Chmod(releases(e), 0o777) }, func(e *env) { os.Chmod(releases(e), 0o755) }},
		{"current not releases/<id>", func(e *env) {
			cur := filepath.Join(e.root, BaseDir, "current")
			os.Remove(cur)
			os.Symlink(e.relDir(rel2), cur)
		}, func(e *env) {
			cur := filepath.Join(e.root, BaseDir, "current")
			os.Remove(cur)
			os.Symlink("releases/"+rel2, cur)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := active(t)
			e.release(rel3, "2026-11-15")
			e.runner.fail["ah-pkgstep"] = 1
			if code := e.switchTo(rel3); code != ExitFailed {
				t.Fatalf("exit %d", code)
			}
			before := e.deployJSON()
			if rec := e.record(); rec.PackageState != PackageUnknown {
				t.Fatalf("record: %+v", rec)
			}
			tc.spoil(e)
			e.runner.reset()
			if code := e.switchTo("0004-ddddddd"); code != ExitFailed {
				t.Fatalf("exit %d", code)
			}
			if !strings.Contains(e.last, "state: FAILED") || !strings.Contains(e.last, "nothing changed or recorded") {
				t.Errorf("output:\n%s", e.last)
			}
			if code := e.rollBack("previous"); code != ExitFailed {
				t.Fatalf("rollback: exit %d", code)
			}
			if code := e.recover(RecoverOptions{SetCounter: true, Counter: 9}); code != ExitFailed {
				t.Fatalf("recover: exit %d", code)
			}
			if got := e.deployJSON(); got != before {
				t.Errorf("deploy.json was written:\n%s\nwas\n%s", got, before)
			}
			if len(e.runner.calls) != 0 {
				t.Errorf("commands run: %v", e.runner.calls)
			}
			tc.repair(e)
			e.release("0004-ddddddd", "2026-11-20")
			if code := e.switchTo("0004-ddddddd"); code != ExitFailed || e.record().Error != CodePackageUnknown {
				t.Errorf("after the repair: exit %d, %+v", code, e.record())
			}
		})
	}
}

// staticRelease writes a static release (comment-only lists, components:
// static) of the package snapshot snapshot. Its ah-pkgstep is the fixture's;
// the fake runner fails it as the builder's static step refuses a
// snapshot change.
func (e *env) staticRelease(id, snapshot string) {
	e.t.Helper()
	dir := e.release(id, snapshot)
	for _, list := range []string{ValidatorsList, ReloadList, HealthCmd, StopList} {
		e.write(filepath.Join(dir, list), "# a static release\n", 0o644)
	}
	data, _ := os.ReadFile(filepath.Join(dir, ManifestFile))
	e.write(filepath.Join(dir, ManifestFile), string(data)+"components: static\n", 0o644)
	e.sums(dir)
}

// TestStaticReleaseOfAnotherSnapshot: a static release changes no package,
// so one built with a package snapshot other than the host's (a wrong
// --package-snapshot) can never switch. It is refused before the
// transaction begins: nothing runs (not its package step, not a stop
// list), decisions are not inhibited, current is unchanged and the host's
// package snapshot stays known, so the same release rebuilt with the
// host's snapshot switches next.
func TestStaticReleaseOfAnotherSnapshot(t *testing.T) {
	e := active(t)
	e.staticRelease(rel3, "2026-11-15")
	e.runner.fail["ah-pkgstep"] = -1
	if code := e.switchTo(rel3); code != ExitFailed {
		t.Fatalf("exit %d\n%s", code, e.last)
	}
	e.unchanged("releases/"+rel2, "packages")
	if rec := e.record(); rec.Error != CodePackageFailed || rec.PackageState != "" || rec.PackageSnapshot != "2026-11-01" || rec.ServicesStopped {
		t.Errorf("record: %+v", rec)
	}
	if len(e.runner.calls) != 0 {
		t.Errorf("commands run: %v", e.runner.calls)
	}
	if _, ok := e.inhibited(); ok {
		t.Errorf("decisions inhibited although nothing began")
	}
	e.noJournal()
	// The same id rebuilt would be refused as release-exists: the
	// message asks for the host's snapshot and a higher counter.
	if !strings.Contains(e.last, "build it with the host's --package-snapshot 2026-11-01 and a higher counter") {
		t.Errorf("output:\n%s", e.last)
	}
	const rel4 = "0004-ddddddd"
	e.staticRelease(rel4, "2026-11-01")
	if code := e.switchTo(rel4); code != ExitOK {
		t.Fatalf("the static release of the host's snapshot: exit %d\n%s", code, e.last)
	}
}

// staticStep models the builder's generated bin/ah-pkgstep of a static
// release on the fake runner: it changes nothing and exits 0 when --from
// is empty or equal to --to, and fails for any other --from.
func staticStep(argv []string) error {
	if filepath.Base(argv[0]) != "ah-pkgstep" {
		return nil
	}
	var from, to string
	for i := 1; i+1 < len(argv); i++ {
		switch argv[i] {
		case "--from":
			from = argv[i+1]
		case "--to":
			to = argv[i+1]
		}
	}
	if from != "" && from != to {
		return errors.New("ah-pkgstep: exit status 1")
	}
	return nil
}

// TestStaticReleaseCannotSettleAnUnknownState: a first switch whose real
// package step fails leaves the package state unknown, from no snapshot
// to S. A static release of snapshot S would run its generated step with
// an empty --from, which changes nothing and exits 0: that must not be
// taken as the step finished. It is refused before anything begins,
// nothing runs, and the state stays unknown until the owner states it or
// a release with a package step of its own runs the step to the end.
func TestStaticReleaseCannotSettleAnUnknownState(t *testing.T) {
	e := newEnv(t)
	e.release(rel1, "2026-11-15")
	e.runner.fail["ah-pkgstep"] = 1
	if code := e.switchTo(rel1); code != ExitFailed {
		t.Fatalf("first switch: exit %d\n%s", code, e.last)
	}
	if rec := e.record(); rec.PackageState != PackageUnknown || rec.PackageFrom != "" || rec.PackageTo != "2026-11-15" {
		t.Fatalf("record after the failed step: %+v", rec)
	}
	e.runner.gate = staticStep
	e.staticRelease(rel2, "2026-11-15")
	e.runner.reset()
	if code := e.switchTo(rel2); code != ExitFailed {
		t.Fatalf("the static release: exit %d\n%s", code, e.last)
	}
	if n := e.runner.called("ah-pkgstep"); n != 0 {
		t.Errorf("the static release's step ran %d times", n)
	}
	rec := e.record()
	if rec.PackageState != PackageUnknown || rec.PackageSnapshot != "" || rec.Error != CodePackageUnknown {
		t.Errorf("record: %+v", rec)
	}
	if !strings.Contains(e.last, "a static release changes no package") {
		t.Errorf("output:\n%s", e.last)
	}
	e.noJournal()
}

// TestStaticReleaseCannotEstablishASnapshot: on a host with a current
// release whose deploy.json is gone (only root removes it), the package
// snapshot is not known; a static release must not be taken for a first
// switch, whose generated step exits 0 for an empty --from, and record
// its own snapshot as the host's. It is refused before anything begins,
// and so is it again once the refusal itself is recorded.
func TestStaticReleaseCannotEstablishASnapshot(t *testing.T) {
	e := active(t)
	e.runner.gate = staticStep
	if err := os.Remove(filepath.Join(e.root, StateDir, DeployFile)); err != nil {
		t.Fatal(err)
	}
	e.staticRelease(rel3, "another")
	for _, want := range []string{"deploy.json is missing", "records no package snapshot"} {
		if code := e.switchTo(rel3); code != ExitFailed {
			t.Fatalf("exit %d\n%s", code, e.last)
		}
		if !strings.Contains(e.last, want) || !strings.Contains(e.last, "a static release cannot establish one") {
			t.Errorf("output lacks %q:\n%s", want, e.last)
		}
		e.unchanged("releases/"+rel2, "packages")
		if rec := e.record(); rec.PackageSnapshot != "" || rec.Error != CodePackageFailed {
			t.Errorf("record: %+v", rec)
		}
		if len(e.runner.calls) != 0 {
			t.Errorf("commands run: %v", e.runner.calls)
		}
		if _, ok := e.inhibited(); ok {
			t.Errorf("decisions inhibited although nothing began")
		}
	}
}
