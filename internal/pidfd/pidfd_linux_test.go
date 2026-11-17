package pidfd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

const helperEnv = "PIDFD_TEST_HELPER"

// TestMain lets the test binary act as its own child: the child reads standard
// input until it is closed and then exits.
func TestMain(m *testing.M) {
	if os.Getenv(helperEnv) == "1" {
		io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func startChild(t *testing.T) (*exec.Cmd, io.WriteCloser) {
	t.Helper()
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), helperEnv+"=1")
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		in.Close()
		cmd.Process.Kill()
		cmd.Wait()
	})
	return cmd, in
}

func skipUnsupported(t *testing.T, err error) {
	t.Helper()
	if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EPERM) {
		t.Skip("pidfd_open unavailable:", err)
	}
}

func TestEventsDecoding(t *testing.T) {
	for _, tc := range []struct {
		events int16
		exited bool
		fail   bool
	}{
		{0, false, false},
		{unix.POLLIN, true, false},
		{unix.POLLHUP, true, false},
		{unix.POLLIN | unix.POLLHUP, true, false},
		{unix.POLLERR, false, true},
		{unix.POLLNVAL, false, true},
		{unix.POLLIN | unix.POLLERR, false, true},
		{unix.POLLHUP | unix.POLLNVAL, false, true},
		{unix.POLLOUT, false, true},
	} {
		got, err := Events(tc.events)
		if (err != nil) != tc.fail || got != tc.exited {
			t.Errorf("events %#x: got %v, %v", tc.events, got, err)
		}
	}
}

func TestOpenAndExitedChild(t *testing.T) {
	cmd, in := startChild(t)
	f, err := Open(cmd.Process.Pid)
	skipUnsupported(t, err)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if dead, err := Exited(f); err != nil || dead {
		t.Fatalf("running child: %v %v", dead, err)
	}
	in.Close()
	deadline := time.Now().Add(10 * time.Second)
	for {
		dead, err := Exited(f)
		if err != nil {
			t.Fatal(err)
		}
		if dead {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("child exit not seen")
		}
		time.Sleep(5 * time.Millisecond)
	}
	// The exit stays visible after the parent has reaped the child.
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if dead, err := Exited(f); err != nil || !dead {
		t.Fatalf("reaped child: %v %v", dead, err)
	}
}

func TestExitedRepeatsThePollOnEINTR(t *testing.T) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	calls := 0
	dead, err := exitedWith(f, func(p []unix.PollFd, timeout int) (int, error) {
		calls++
		if timeout != 0 {
			t.Errorf("poll timeout %d, want 0", timeout)
		}
		if calls <= 3 {
			return -1, unix.EINTR
		}
		p[0].Revents = unix.POLLIN
		return 1, nil
	})
	if err != nil || !dead || calls != 4 {
		t.Fatalf("got %v, %v after %d polls, want exited after 4", dead, err, calls)
	}
}

func TestExitedReturnsOtherPollErrors(t *testing.T) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	calls := 0
	dead, err := exitedWith(f, func([]unix.PollFd, int) (int, error) {
		calls++
		return -1, unix.EBADF
	})
	if err != unix.EBADF || dead || calls != 1 {
		t.Fatalf("got %v, %v after %d polls, want EBADF after 1", dead, err, calls)
	}
}

func TestOpenRejectsInvalidPid(t *testing.T) {
	for _, pid := range []int{-1, 0} {
		f, err := Open(pid)
		skipUnsupported(t, err)
		if err == nil {
			f.Close()
			t.Fatalf("pid %d opened", pid)
		}
	}
}

func TestBootID(t *testing.T) {
	id, err := BootID()
	if err != nil {
		t.Fatal(err)
	}
	if !ValidBoot(id) {
		t.Fatal(id)
	}
	again, err := BootID()
	if err != nil || again != id {
		t.Fatal("boot identity not stable", again, err)
	}
}

func TestValidBoot(t *testing.T) {
	const good = "0123abcd-4567-89ab-cdef-0123456789ab"
	for _, tc := range []struct {
		s  string
		ok bool
	}{
		{good, true},
		{"", false},
		{good[:35], false},
		{good + "0", false},
		{strings.ToUpper(good), false},
		{strings.Replace(good, "-", "x", 1), false},
		{strings.Replace(good, "a", "g", 1), false},
		{strings.ReplaceAll(good, "-", ""), false},
		{"0123abcd-4567-89ab-cdef-0123456789a-", false},
	} {
		if ValidBoot(tc.s) != tc.ok {
			t.Errorf("%q: want %v", tc.s, tc.ok)
		}
	}
}

func statLine(state string, start string) string {
	f := make([]string, 20)
	for i := range f {
		f[i] = "0"
	}
	f[0], f[19] = state, start
	return "100 (synthetic ) comm) " + strings.Join(f, " ")
}

func TestParseStat(t *testing.T) {
	for _, tc := range []struct {
		name  string
		stat  string
		state string
		start uint64
		err   error
	}{
		{"sleeping", statLine("S", "123"), "S", 123, nil},
		{"zombie", statLine("Z", "123"), "Z", 123, nil},
		{"command with parentheses", "7 (a) (b) R " + strings.Repeat("0 ", 18) + "9", "R", 9, nil},
		{"empty", "", "", 0, ErrStatFormat},
		{"no command end", "100 malformed", "", 0, ErrStatFormat},
		{"command end without space", "100 (x)", "", 0, ErrStatFormat},
		{"short", "100 (x) R 1 2", "", 0, ErrStatShort},
		{"start not a number", statLine("S", "abc"), "S", 0, ErrStatStart},
		{"start zero", statLine("Z", "0"), "Z", 0, ErrStatStart},
		{"start negative", statLine("S", "-5"), "S", 0, ErrStatStart},
		{"start overflow", statLine("S", "18446744073709551616"), "S", 0, ErrStatStart},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseStat(tc.stat)
			if !errors.Is(err, tc.err) || got.State != tc.state || got.Start != tc.start {
				t.Fatalf("got %+v, %v", got, err)
			}
		})
	}
	if !(Stat{State: "Z"}).Gone() || !(Stat{State: "X"}).Gone() || (Stat{State: "S"}).Gone() {
		t.Fatal("gone states")
	}
}

func TestOriginalEnded(t *testing.T) {
	for _, tc := range []struct {
		state string
		start string
		ended bool
	}{{"S", "123", false}, {"R", "123", false}, {"Z", "123", true}, {"X", "123", true}, {"R", "124", true}, {"S", "122", true}} {
		got, err := OriginalEnded(123, statLine(tc.state, tc.start))
		if err != nil || got != tc.ended {
			t.Errorf("%+v: %v %v", tc, got, err)
		}
	}
	for stat, want := range map[string]string{
		"":                  "invalid recovery process stat",
		"100 malformed":     "invalid recovery process stat",
		"100 (x) R":         "short recovery process stat",
		statLine("S", "x"):  "invalid recovery starttime",
		statLine("S", "0"):  "invalid recovery starttime",
		statLine("S", "-1"): "invalid recovery starttime",
	} {
		if ended, err := OriginalEnded(123, stat); err == nil || ended || err.Error() != want {
			t.Errorf("%q: %v %v, want %s", stat, ended, err, want)
		}
	}
	// An impossible start time is an error whatever the text says, even for a
	// zombie or a process that would otherwise differ.
	for _, state := range []string{"S", "Z", "X"} {
		if ended, err := OriginalEnded(0, statLine(state, "123")); err == nil || ended || err.Error() != "invalid recovery starttime" {
			t.Errorf("start 0, state %s: %v %v", state, ended, err)
		}
	}
}

func TestReadStatAndEndedAgainstOwnProcess(t *testing.T) {
	s, err := ReadStat(os.Getpid())
	if err != nil || s.Start == 0 || s.Gone() {
		t.Fatalf("own stat: %+v %v", s, err)
	}
	boot, err := BootID()
	if err != nil {
		t.Fatal(err)
	}
	if ended, err := Ended(boot, os.Getpid(), s.Start); err != nil || ended {
		t.Fatalf("own process ended: %v %v", ended, err)
	}
	// Another start time on the same pid is a reused pid.
	if ended, err := Ended(boot, os.Getpid(), s.Start+1); err != nil || !ended {
		t.Fatalf("reused pid: %v %v", ended, err)
	}
	// A different boot is an error and never an answer.
	other := "11111111-1111-1111-1111-111111111111"
	if boot == other {
		other = "22222222-2222-2222-2222-222222222222"
	}
	if ended, err := Ended(other, os.Getpid(), s.Start); err == nil || ended || !strings.HasPrefix(err.Error(), "unsupported: ") {
		t.Fatalf("different boot: %v %v", ended, err)
	}
	if _, err := ReadStat(0); err == nil {
		t.Fatal("pid 0 read")
	}
}

func TestEndedImpossibleIdentity(t *testing.T) {
	boot, err := BootID()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		pid   int
		start uint64
	}{
		{"pid zero", 0, 12345},
		{"negative pid", -1, 12345},
		{"pid above the kernel maximum", 1 << 22, 12345},
		{"pid far above the kernel maximum", 1<<22 + 1, 12345},
		{"largest int pid", int(^uint(0) >> 1), 12345},
		{"start zero for a living process", os.Getpid(), 0},
		{"both impossible", 0, 0},
	} {
		if ended, err := Ended(boot, tc.pid, tc.start); err == nil || ended || err.Error() != "invalid process identity" {
			t.Errorf("%s: %v %v", tc.name, ended, err)
		}
	}
	// The identity is checked before the boot, so a different boot does not
	// hide it.
	if ended, err := Ended("11111111-1111-1111-1111-111111111111", 0, 1); err == nil || ended || err.Error() != "invalid process identity" {
		t.Errorf("pid zero, other boot: %v %v", ended, err)
	}
	if ended, err := Ended("11111111-1111-1111-1111-111111111111", 1<<22, 1); err == nil || ended || err.Error() != "invalid process identity" {
		t.Errorf("pid above the maximum, other boot: %v %v", ended, err)
	}
	// The maximum itself and the pid 1 are possible identities: they get as far
	// as the boot check.
	for _, pid := range []int{1, 1<<22 - 1} {
		if ended, err := Ended("11111111-1111-1111-1111-111111111111", pid, 1); err == nil || ended || !strings.HasPrefix(err.Error(), "unsupported: ") {
			t.Errorf("pid %d, other boot: %v %v", pid, ended, err)
		}
	}
}

func TestEndedAfterChildExit(t *testing.T) {
	cmd, in := startChild(t)
	pid := cmd.Process.Pid
	s, err := ReadStat(pid)
	if err != nil {
		t.Fatal(err)
	}
	boot, err := BootID()
	if err != nil {
		t.Fatal(err)
	}
	if ended, err := Ended(boot, pid, s.Start); err != nil || ended {
		t.Fatalf("running child ended: %v %v", ended, err)
	}
	in.Close()
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if ended, err := Ended(boot, pid, s.Start); err != nil || !ended {
		t.Fatalf("reaped child not ended: %v %v", ended, err)
	}
}

func TestReadKernelBounds(t *testing.T) {
	dir := t.TempDir()
	write := func(name, text string) string {
		p := dir + "/" + name
		if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	p := write("file", "12345")
	if b, err := readKernel(p, 5); err != nil || string(b) != "12345" {
		t.Fatal(string(b), err)
	}
	if _, err := readKernel(p, 4); err == nil {
		t.Fatal("excessive read accepted")
	}
	if err := os.Symlink(p, dir+"/link"); err != nil {
		t.Fatal(err)
	}
	if _, err := readKernel(dir+"/link", 64); err == nil {
		t.Fatal("final symlink followed")
	}
	if err := os.Symlink(dir, dir+"/dirlink"); err != nil {
		t.Fatal(err)
	}
	if _, err := readKernel(dir+"/dirlink/file", 64); err == nil {
		t.Fatal("intermediate symlink followed")
	}
	for _, bad := range []string{"relative", "/a/../b", "/a//b", "/a b", "/a%b", "/a\nb", "/a\x00b", "/" + strings.Repeat("x", 600)} {
		if _, err := readKernel(bad, 64); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	_, err := readKernel(dir+"/missing", 64)
	if !os.IsNotExist(err) {
		t.Fatalf("missing file: %v", fmt.Sprint(err))
	}
}
