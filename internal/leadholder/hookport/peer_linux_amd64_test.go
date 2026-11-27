package hookport

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestLiveLeader(t *testing.T) {
	for _, tc := range []struct {
		status string
		live   bool
	}{
		{"Name:\ttest\nState:\tR (running)\nPid:\t1\n", true},
		{"State: t (tracing stop)", true},
		{"State: Z (zombie)", false},
		{"State: X (dead)", false},
		{"TracerState: R", false},
		{"State: R\nState: Z", false},
		{"State: Running", false},
		{"State:", false},
		{"State: S\n" + strings.Repeat("x", procLimit), false},
	} {
		if got := liveLeader([]byte(tc.status)); got != tc.live {
			t.Errorf("liveLeader(%.48q) = %v, want %v", tc.status, got, tc.live)
		}
	}
}

// Go runs init on the process's startup thread. Exit just that thread in
// the re-executed fixture while the runtime's other threads stay alive.
func init() {
	if len(os.Args) == 2 && os.Args[1] == "--hookport-zombie-leader" {
		if unix.Gettid() != os.Getpid() {
			os.Exit(2)
		}
		unix.RawSyscall(unix.SYS_EXIT, 0, 0, 0)
		os.Exit(3)
	}
}

// Supply the known child's identity directly so the test needs no socket.
type childPeer struct {
	KernelPeers
	pid int
	fd  int
}

func (p childPeer) Credentials(net.Conn) (*unix.Ucred, error) {
	return &unix.Ucred{Uid: uint32(os.Geteuid()), Pid: int32(p.pid)}, nil
}

func (p childPeer) PIDFD(net.Conn) (int, error) { return unix.Dup(p.fd) }

func TestZombieLeaderAdmission(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "--hookport-zombie-leader")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	pid := cmd.Process.Pid
	fd, err := unix.PidfdOpen(pid, 0)
	if kernelRefusal(err) {
		unavailable(t, "pidfd_open: "+err.Error())
	}
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	p := childPeer{pid: pid, fd: fd}
	for {
		status, err := p.Status(pid)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(status, []byte("\nState:\tZ (zombie)\n")) {
			break
		}
		if ctx.Err() != nil {
			t.Fatal("leader never became a zombie")
		}
		time.Sleep(time.Millisecond)
	}
	if alive, err := p.Alive(fd); !alive || err != nil {
		t.Fatalf("thread group must still be alive: %v, %v", alive, err)
	}
	if err := admit(nil, p, "/unit"); !errors.Is(err, errLeaderState) {
		t.Fatalf("expected refusal on zombie leader state, got %v", err)
	}
}
