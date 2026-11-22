package switcher

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// PackageUnknown is the package state of a host whose package step did not
// finish: it exited with a failure or was interrupted, and may have changed
// some packages. Neither the old nor the new snapshot is recorded as the
// host's until the owner says which it is at (ah-switch recover
// --package-state) or the step runs again to the end.
const PackageUnknown = "unknown"

// helperEndWait is how long the switch waits for the process group of an
// earlier package step to end after it was killed.
const helperEndWait = 10 * time.Second

// procs looks at processes; a test replaces it.
type procs interface {
	// start returns the start time of the process pid (in clock ticks
	// since boot, field 22 of /proc/PID/stat), and whether it exists.
	start(pid int) (uint64, bool, error)
	// groupAlive reports whether any process is in the process group pgid.
	groupAlive(pgid int) (bool, error)
	// killGroup kills every process of the process group pgid.
	killGroup(pgid int) error
}

// procfs is procs on the host's /proc.
type procfs struct{}

func (procfs) start(pid int) (uint64, bool, error) {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return parseStatStart(string(data))
}

// parseStatStart reads the start time out of a /proc/PID/stat line: the
// fields after the command name, which ends at the last ')'.
func parseStatStart(stat string) (uint64, bool, error) {
	i := strings.LastIndexByte(stat, ')')
	if i < 0 {
		return 0, false, errors.New("stat does not parse")
	}
	f := strings.Fields(stat[i+1:])
	// f[0] is field 3 (state); starttime is field 22.
	if len(f) < 20 {
		return 0, false, errors.New("stat does not parse")
	}
	n, err := strconv.ParseUint(f[19], 10, 64)
	if err != nil {
		return 0, false, errors.New("stat does not parse")
	}
	return n, true, nil
}

func (procfs) groupAlive(pgid int) (bool, error) {
	err := syscall.Kill(-pgid, 0)
	switch {
	case err == nil, errors.Is(err, syscall.EPERM):
		return true, nil
	case errors.Is(err, syscall.ESRCH):
		return false, nil
	}
	return false, err
}

func (procfs) killGroup(pgid int) error {
	err := syscall.Kill(-pgid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

// helperEnded establishes that the package step a journal names (its
// process, which led its own process group, and its start time) has ended
// before another starts or its outcome is settled. A process of that id
// with the same start time is that step still running: its group is killed
// and waited for. A process of that id with another start time is another
// process: the id was reused, so the group, whose id was the step's, is
// gone. No such process, but processes still in its group, is refused:
// whose they are cannot be told, and they are left to the owner.
func (s *Switcher) helperEnded(pid int, start uint64) error {
	if pid <= 0 {
		return nil
	}
	st, alive, err := s.procs.start(pid)
	if err != nil {
		return failf(CodePackageUnknown, "whether the earlier package step (process %d) ended cannot be told: %s", pid, q(err.Error()))
	}
	switch {
	case alive && st == start:
		s.printf("packages: the earlier package step (process %d) still runs; its process group is killed", pid)
		if err := s.procs.killGroup(pid); err != nil {
			return failf(CodePackageUnknown, "the earlier package step (process %d) cannot be killed: %s", pid, q(err.Error()))
		}
		deadline := s.cfg.Now().Add(helperEndWait)
		for {
			busy, err := s.procs.groupAlive(pid)
			if err == nil && !busy {
				return nil
			}
			if !s.cfg.Now().Before(deadline) {
				return failf(CodePackageUnknown, "the process group of the earlier package step (process %d) did not end after it was killed", pid)
			}
			s.cfg.Sleep(100 * time.Millisecond)
		}
	case alive:
		return nil
	}
	busy, err := s.procs.groupAlive(pid)
	if err != nil {
		return failf(CodePackageUnknown, "whether the earlier package step's process group (%d) ended cannot be told: %s", pid, q(err.Error()))
	}
	if busy {
		return failf(CodePackageUnknown, "processes of the earlier package step's process group (%d) still run; stop them by hand", pid)
	}
	return nil
}

// runPackageStep runs the package step of the transaction t from its
// journal's PackageFrom to PackageTo. The journal says started before it
// runs, and is given the step's process id and start time once it runs; the
// step holds the switch lock too (it is given the lock's file as its fd 3),
// and is killed when the switch dies, so no later switch can overlap it.
// An exit that is not 0 leaves the host's package state unknown.
func (s *Switcher) runPackageStep(ctx context.Context, t *txn, argv []string) error {
	t.j.PackageState = PackageStarted
	t.j.HelperPID, t.j.HelperStart = 0, 0
	if err := s.save(t); err != nil {
		return err
	}
	if err := s.crash(CrashPackageStart); err != nil {
		return err
	}
	var extra []*os.File
	if s.lockFile != nil {
		extra = []*os.File{s.lockFile}
	}
	_, err := s.cfg.Runner.Run(ctx, Command{
		Argv:       argv,
		Timeout:    PackageStepTimeout,
		ExtraFiles: extra,
		OnStart: func(pid int) {
			st, ok, err := s.procs.start(pid)
			if err != nil || !ok {
				s.printf("packages: warning: the start time of the package step (process %d) cannot be read", pid)
			}
			t.j.HelperPID, t.j.HelperStart = pid, st
			if err := s.save(t); err != nil {
				s.printf("packages: warning: %s", describe(err))
			}
		},
	})
	if err != nil {
		t.j.PackageState = PackageUnknown
		return failf(CodePackageFailed, "%s; the host's package snapshot is now unknown", summary(err))
	}
	if err := s.crash(CrashPackageRan); err != nil {
		return err
	}
	t.j.PackageState = PackageDone
	if err := s.save(t); err != nil {
		return err
	}
	return s.crash(CrashPackageDone)
}

// packageUnknownError is the refusal of a switch on a host whose package
// state is unknown.
func packageUnknownError(from, to string) error {
	return failf(CodePackageUnknown, "the host's package snapshot is unknown: the package step from %s to %s did not finish; run ah-switch recover --package-state SNAPSHOT (%s or %s, the one the host is at), or switch to a release of snapshot %s, which runs the step again", q(from), q(to), q(from), q(to), q(to))
}

// recoverPackageState records the owner's statement of the snapshot the
// host's packages are at, which settles an unknown package state.
func (s *Switcher) recoverPackageState(snapshot string) int {
	rec, ok, err := s.readRecord()
	if err != nil {
		s.printf("recover: FAILED: %s", describe(err))
		return ExitFailed
	}
	if !ok || rec.PackageState != PackageUnknown {
		s.printf("recover: the host's package snapshot is known (%s); nothing to recover", q(rec.PackageSnapshot))
		return ExitFailed
	}
	if snapshot != rec.PackageFrom && snapshot != rec.PackageTo {
		s.printf("recover: FAILED: --package-state %s is neither %s nor %s, the two ends of the package step that did not finish", q(snapshot), q(rec.PackageFrom), q(rec.PackageTo))
		return ExitFailed
	}
	if s.cfg.Dry {
		s.printf("recover: would record the package snapshot %s as stated by the owner", q(snapshot))
		return ExitOK
	}
	rec.PackageSnapshot = snapshot
	rec.PackageState, rec.PackageFrom, rec.PackageTo = "", "", ""
	rec.PackageStatedAt = s.now()
	rec.PendingReport = false
	if err := s.writeRecord(&rec); err != nil {
		s.printf("recover: FAILED: %s not written: %s", DeployFile, q(err.Error()))
		return ExitFailed
	}
	s.printf("recover: the package snapshot is %s, as stated by the owner at %s", q(snapshot), rec.PackageStatedAt)
	return ExitOK
}

// packageNote is how status shows the package state of rec.
func packageNote(rec Record) string {
	switch {
	case rec.PackageState == PackageUnknown:
		return fmt.Sprintf("unknown: the package step from %s to %s did not finish; run ah-switch recover --package-state SNAPSHOT", q(rec.PackageFrom), q(rec.PackageTo))
	case rec.PackageStatedAt != "":
		return fmt.Sprintf("%s (stated by the owner at %s)", q(rec.PackageSnapshot), q(rec.PackageStatedAt))
	}
	return q(rec.PackageSnapshot)
}
