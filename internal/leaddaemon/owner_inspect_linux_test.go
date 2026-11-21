//go:build linux && ataniq_ownerchannel

package leaddaemon

import (
	"errors"
	"math"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// TestOwnerTheTerminalOfAStat: field 7 of /proc/<pid>/stat names the device of
// the controlling terminal; a command name with parentheses and spaces does
// not shift the fields; a device that is not a pseudo-terminal or a console is
// an error, not a terminal.
func TestOwnerTheTerminalOfAStat(t *testing.T) {
	stat := func(comm string, tty string) string {
		return "4242 (" + comm + ") S 1 4242 4242 " + tty + " 4242 4194560 100 0 0 0 0 0 0 0 20 0 1 0 987654 1000 100 18446744073709551615"
	}
	for _, tt := range []struct {
		name string
		stat string
		want string
		bad  bool
	}{
		{"no terminal", stat("sleep", "0"), "", false},
		{"pts 0", stat("sh", strconv.Itoa(136<<8)), "/dev/pts/0", false},
		{"pts 7", stat("sh", strconv.Itoa(136<<8|7)), "/dev/pts/7", false},
		{"pts 300 on the next major", stat("sh", strconv.Itoa(137<<8|44)), "/dev/pts/300", false},
		{"the highest pts major", stat("sh", strconv.Itoa(143<<8|255)), "/dev/pts/2047", false},
		{"the major under the pts range", stat("sh", strconv.Itoa(135<<8|1)), "", true},
		{"the major over the pts range", stat("sh", strconv.Itoa(144<<8|1)), "", true},
		{"a major far over the pts range", stat("sh", strconv.Itoa(200<<8|1)), "", true},
		{"a virtual console", stat("login", strconv.Itoa(4<<8|1)), "/dev/tty1", false},
		{"a name with parentheses and spaces", stat("a) S 1 2 3 99 (b c", strconv.Itoa(136<<8|3)), "/dev/pts/3", false},
		{"the device /dev/tty", stat("x", strconv.Itoa(5<<8)), "", true},
		{"a serial line", stat("x", strconv.Itoa(4<<8|64)), "", true},
		{"a device of no terminal major", stat("x", strconv.Itoa(8<<8|1)), "", true},
		{"a number that is not one", stat("x", "x"), "", true},
		{"a negative number", stat("x", "-1"), "", true},
		{"no command name end", "4242 sleep S 1 2 3 0", "", true},
		{"too few fields", "4242 (x) S 1 2", "", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ttyOfStat(tt.stat)
			if (err != nil) != tt.bad || got != tt.want {
				t.Fatalf("%q, %v; want %q, error %v", got, err, tt.want, tt.bad)
			}
		})
	}
}

// ownerAccepted is "now", a moment after a process the test started.
func ownerAccepted() time.Time { return time.Now() }

// TestOwnerInspectThisProcess: the real inspector finds the test process, with
// the terminal that /proc/self/stat names, when the connection is as young as
// the process allows.
func TestOwnerInspectThisProcess(t *testing.T) {
	b, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		t.Skip("no /proc")
	}
	want, err := ttyOfStat(string(b))
	if err != nil {
		t.Skipf("this process's terminal is not one the inspector names: %v", err)
	}
	got, err := ProcInspector{}.Inspect(PeerRef{PID: os.Getpid(), AcceptedAt: ownerAccepted()})
	if err != nil || got.TTY != want {
		t.Fatalf("%+v, %v; want the terminal %q", got, err, want)
	}
}

// TestOwnerInspectRefusesAReusedProcessId: a process that started after the
// connection was accepted cannot be the peer that connected, so it is not
// judged: the connection here is taken to have been accepted long before this
// process existed.
func TestOwnerInspectRefusesAReusedProcessId(t *testing.T) {
	_, err := ProcInspector{}.Inspect(PeerRef{PID: os.Getpid(), AcceptedAt: time.Now().Add(-100 * 24 * time.Hour)})
	if err == nil || !strings.Contains(err.Error(), "reused") {
		t.Fatalf("%v", err)
	}
}

// TestOwnerInspectCannotJudgeWhatIsNotThere: no process id, a process that
// ended and was reaped, and a zombie are errors, not a peer with no terminal.
func TestOwnerInspectCannotJudgeWhatIsNotThere(t *testing.T) {
	for _, pid := range []int{0, -1, 1<<22 + 5} {
		if _, err := (ProcInspector{}).Inspect(PeerRef{PID: pid, AcceptedAt: time.Now()}); err == nil {
			t.Errorf("pid %d was judged", pid)
		}
	}

	cmd := exec.Command("true")
	if err := cmd.Start(); err != nil {
		t.Skip(err)
	}
	pid := cmd.Process.Pid
	deadline := time.Now().Add(10 * time.Second)
	for {
		b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
		if err == nil && strings.Contains(string(b), ") Z ") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the child did not become a zombie")
		}
		time.Sleep(5 * time.Millisecond)
	}
	_, err := ProcInspector{}.Inspect(PeerRef{PID: pid, AcceptedAt: time.Now()})
	if err == nil || !strings.Contains(err.Error(), "ended") {
		t.Fatalf("a zombie: %v", err)
	}
	cmd.Wait()
	if _, err := (ProcInspector{}).Inspect(PeerRef{PID: pid, AcceptedAt: time.Now()}); err == nil {
		t.Fatal("a reaped process was judged")
	}
}

// TestOwnerInspectAProcessInItsOwnSessionHasNoTerminal: a child that made its
// own session and has no controlling terminal is found, with an empty terminal.
func TestOwnerInspectAProcessInItsOwnSessionHasNoTerminal(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	got, err := ProcInspector{}.Inspect(PeerRef{PID: cmd.Process.Pid, AcceptedAt: time.Now()})
	if err != nil || got.TTY != "" {
		t.Fatalf("%+v, %v", got, err)
	}
}

// TestOwnerInspectAProcessWithAControllingTerminal: a child that has a
// pseudo-terminal as its controlling terminal is found with that terminal's
// device path.
func TestOwnerInspectAProcessWithAControllingTerminal(t *testing.T) {
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("no pseudo-terminal: %v", err)
	}
	defer master.Close()
	if err := unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		t.Skip(err)
	}
	n, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
	if err != nil {
		t.Skip(err)
	}
	name := "/dev/pts/" + strconv.Itoa(n)
	slave, err := os.OpenFile(name, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("cannot open %s: %v", name, err)
	}
	defer slave.Close()
	cmd := exec.Command("sleep", "30")
	cmd.Stdin = slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot give a child a controlling terminal: %v", err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	got, err := ProcInspector{}.Inspect(PeerRef{PID: cmd.Process.Pid, AcceptedAt: time.Now()})
	if err != nil || got.TTY != name {
		t.Fatalf("%+v, %v; want %s", got, err, name)
	}
}

// inspectFake answers the questions of the inspector one by one, for this
// process's pidfd, so that a test can make exactly one of them say "no".
func inspectFake(stat string, exited bool) procReads {
	rd := realProcReads
	rd.stat = func(int) ([]byte, error) { return []byte(stat), nil }
	rd.exited = func(*os.File) (bool, error) { return exited, nil }
	rd.uptime = func() (time.Duration, error) { return 1000 * time.Hour, nil }
	return rd
}

// ownerStatOf is the text of /proc/<pid>/stat of a process in state, started
// start ticks after boot, with no terminal.
func ownerStatOf(state string, start uint64) string {
	return "4242 (x) " + state + " 1 4242 4242 0 4242 4194560 100 0 0 0 0 0 0 0 20 0 1 0 " +
		strconv.FormatUint(start, 10) + " 1000 100 18446744073709551615"
}

// TestOwnerInspectEachCheckOfTheEndOfAProcessHoldsAlone: the pidfd's answer
// that the process exited and the state of the stat (zombie, dead) each
// refuse the peer when the other says it is running; neither check hides the
// other.
func TestOwnerInspectEachCheckOfTheEndOfAProcessHoldsAlone(t *testing.T) {
	peer := PeerRef{PID: os.Getpid(), AcceptedAt: time.Now()}
	ready := ownerStatOf("S", 100)
	if got, err := inspectProc(peer, inspectFake(ready, false)); err != nil || got.TTY != "" {
		t.Fatalf("a running process: %+v, %v", got, err)
	}
	for _, tt := range []struct {
		name   string
		stat   string
		exited bool
	}{
		{"only the pidfd says it exited", ready, true},
		{"only the stat says zombie", ownerStatOf("Z", 100), false},
		{"only the stat says dead", ownerStatOf("X", 100), false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := inspectProc(peer, inspectFake(tt.stat, tt.exited))
			if err == nil || !strings.Contains(err.Error(), "ended") {
				t.Fatalf("%v", err)
			}
		})
	}
	// An error from the pidfd is no answer either.
	rd := inspectFake(ready, false)
	rd.exited = func(*os.File) (bool, error) { return false, errors.New("poll failed") }
	if _, err := inspectProc(peer, rd); err == nil {
		t.Fatal("a pidfd that could not be asked was taken")
	}
}

// TestOwnerInspectTheStartTimeDoesNotOverflow: a process that started after
// more than 1067 days of uptime (a start time past 9.2e9 ticks, whose
// nanoseconds overflow an int64) still has its age worked out, so the
// reuse check is right: a process older than the connection is judged and one
// younger than it is refused.
func TestOwnerInspectTheStartTimeDoesNotOverflow(t *testing.T) {
	const day = 24 * time.Hour
	start := uint64(1100 * 24 * 3600 * 100) // 1100 days after boot, in ticks
	if got := processAge(1200*day, start); got != 100*day {
		t.Fatalf("age %v, want 100 days", got)
	}
	if got := processAge(1100*day+time.Hour, start); got != time.Hour {
		t.Fatalf("age %v, want an hour", got)
	}
	if got := processAge(5*time.Second, math.MaxUint64); got >= 0 {
		t.Fatalf("an impossible start time gave the age %v", got)
	}
	// Through the inspector: up 1200 days, started 1100 days after boot.
	rd := inspectFake(ownerStatOf("S", start), false)
	rd.uptime = func() (time.Duration, error) { return 1200 * day, nil }
	old := PeerRef{PID: os.Getpid(), AcceptedAt: time.Now().Add(-50 * day)}
	if _, err := inspectProc(old, rd); err != nil {
		t.Fatalf("a process 100 days old, connected 50 days ago: %v", err)
	}
	young := PeerRef{PID: os.Getpid(), AcceptedAt: time.Now().Add(-200 * day)}
	if _, err := inspectProc(young, rd); err == nil || !strings.Contains(err.Error(), "reused") {
		t.Fatalf("a process 100 days old, connected 200 days ago: %v", err)
	}
}
