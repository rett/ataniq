package hookport

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestLiveLeader(t *testing.T) {
	for _, tc := range []struct {
		stat string
		live bool
	}{
		{"123 (peer) R 1", true},
		{"123 (spaces ) Z (\nand parens)) t 1", true},
		{"123 (peer) Z 1", false},
		{"123 (peer) X 1", false},
		{"123 (pretend) R 1) Z 1", false},
		{"124 (peer) R 1", false},
		{"123 peer) R 1", false},
		{"123 (peer R 1", false},
		{"123 (peer) Running 1", false},
		{"123 (peer) ", false},
		{"123 (peer) S " + strings.Repeat("0", procLimit), false},
	} {
		if got := liveLeader(123, []byte(tc.stat)); got != tc.live {
			t.Errorf("liveLeader(%.48q) = %v, want %v", tc.stat, got, tc.live)
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

func TestChurningPeerChild(t *testing.T) {
	if os.Args[len(os.Args)-1] != "--hookport-churn" {
		return
	}
	ready := make(chan struct{})
	for range 4 {
		go func() {
			for first := true; ; first = false {
				done := make(chan struct{})
				go func() {
					// Returning while locked retires this OS thread.
					runtime.LockOSThread()
					close(done)
				}()
				<-done
				if first {
					ready <- struct{}{}
				}
			}
		}()
	}
	for range 4 {
		<-ready
	}
	os.Stdout.Write([]byte{'R'})
	io.Copy(io.Discard, os.Stdin)
}

func TestChurningPeerAdmission(t *testing.T) {
	ref, err := selfGroup()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestChurningPeerChild$", "--", "--hookport-churn")
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		input.Close()
		if err := cmd.Wait(); err != nil {
			t.Error(err)
		}
	}()
	var ready [1]byte
	if _, err := io.ReadFull(output, ready[:]); err != nil || ready[0] != 'R' {
		t.Fatalf("peer with churning threads did not start: %q, %v", ready, err)
	}
	fd, err := unix.PidfdOpen(cmd.Process.Pid, 0)
	if kernelRefusal(err) {
		unavailable(t, "pidfd_open: "+err.Error())
	}
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	p := childPeer{pid: cmd.Process.Pid, fd: fd}
	const attempts = 200
	accepted := 0
	for range attempts {
		if err := admit(nil, p, ref); err == nil {
			accepted++
		} else if !errors.Is(err, errPeer) {
			t.Fatal(err)
		}
		if err := admit(nil, p, ref+"/other"); !errors.Is(err, errPeer) {
			t.Fatalf("expected out-of-group refusal: %v", err)
		}
	}
	t.Logf("admitted %d/%d in group, 0/%d outside", accepted, attempts, attempts)
	// Exiting threads can still report a readable, mismatched cgroup.
	if accepted < attempts*3/4 {
		t.Fatal("fewer than 75% of in-group attempts admitted")
	}
}

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
		stat, err := p.Stat(pid)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(stat, []byte(") Z ")) {
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
