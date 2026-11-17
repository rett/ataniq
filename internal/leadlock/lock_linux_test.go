package leadlock

import (
	"bufio"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

const helperEnv = "LEADLOCK_TEST_HELPER"

// TestHelperProcess is the re-exec target, not a test. Modes: "hold" acquires
// the lock named by LEADLOCK_TEST_PATH, prints "ready" and waits; "idle"
// prints "ready" and waits (used with an inherited descriptor).
func TestHelperProcess(t *testing.T) {
	mode := os.Getenv(helperEnv)
	if mode == "" {
		t.Skip("helper process only")
	}
	switch mode {
	case "hold":
		l, err := Acquire(os.Getenv("LEADLOCK_TEST_PATH"))
		if err != nil {
			os.Stdout.WriteString("error " + err.Error() + "\n")
			os.Exit(3)
		}
		_ = l
	case "idle":
	default:
		os.Exit(4)
	}
	os.Stdout.WriteString("ready\n")
	time.Sleep(time.Minute)
	os.Exit(0)
}

// startHelper starts the test binary in the given mode and waits for ready.
func startHelper(t *testing.T, mode, path string, extra ...*os.File) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")
	cmd.Env = append(os.Environ(), helperEnv+"="+mode, "LEADLOCK_TEST_PATH="+path)
	cmd.ExtraFiles = extra
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "ready" {
		t.Fatalf("helper %s not ready: %q %v", mode, line, err)
	}
	return cmd
}

func kill(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	if err := cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
}

func lockPath(t *testing.T) string {
	return filepath.Join(t.TempDir(), "state", "roles", "p", "lead.lock")
}

func mustHeld(t *testing.T, path string) ErrHeld {
	t.Helper()
	l, err := Acquire(path)
	if err == nil {
		l.Release()
		t.Fatal("Acquire succeeded, want ErrHeld")
	}
	var held ErrHeld
	if !errors.As(err, &held) || !errors.Is(err, ErrHeld{}) {
		t.Fatalf("err = %v, want ErrHeld", err)
	}
	return held
}

func TestAcquireCreatesFolderAndFile(t *testing.T) {
	old := unix.Umask(0)
	defer unix.Umask(old)
	path := lockPath(t)
	l, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	state := filepath.Join(strings.TrimSuffix(path, "/roles/p/lead.lock"))
	for _, dir := range []string{state, filepath.Join(state, "roles"), filepath.Dir(path)} {
		fi, err := os.Stat(dir)
		if err != nil || fi.Mode().Perm() != 0o700 {
			t.Fatalf("folder %s: %v %v, want 0700", dir, fi, err)
		}
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 || !fi.Mode().IsRegular() {
		t.Fatalf("file: %v %v, want regular 0600", fi, err)
	}
	if !l.Held() || l.File() == nil || l.Path() != path {
		t.Fatal("Held, File or Path wrong on a held lock")
	}
	data, _ := os.ReadFile(path)
	if string(data) != strconv.Itoa(os.Getpid())+"\n" {
		t.Fatalf("content %q, want own pid and newline", data)
	}
	pid, err := ReadHolder(path)
	if err != nil || pid != os.Getpid() {
		t.Fatalf("ReadHolder = %d, %v", pid, err)
	}
}

func TestDescriptorIsCloexec(t *testing.T) {
	l, err := Acquire(lockPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	flags, err := unix.FcntlInt(l.File().Fd(), unix.F_GETFD, 0)
	if err != nil || flags&unix.FD_CLOEXEC == 0 {
		t.Fatalf("FD_CLOEXEC not set: flags=%d err=%v", flags, err)
	}
}

func TestDaemonPath(t *testing.T) {
	if got := DaemonPath("/x/state"); got != "/x/state/daemon.lock" {
		t.Fatalf("DaemonPath = %q", got)
	}
}

func TestSecondAcquireSameProcessHeld(t *testing.T) {
	path := lockPath(t)
	l, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	before, _ := os.ReadFile(path)
	held := mustHeld(t, path)
	if held.PIDText != strconv.Itoa(os.Getpid()) || held.Path != path {
		t.Fatalf("held = %+v", held)
	}
	if !strings.Contains(held.Error(), "hint") || !strings.Contains(held.Error(), "not proof") {
		t.Fatalf("message does not label the pid as a hint: %q", held.Error())
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatalf("failed acquire changed content: %q -> %q", before, after)
	}
}

func TestSubprocessHoldsThenSigkillReleases(t *testing.T) {
	path := lockPath(t)
	cmd := startHelper(t, "hold", path)
	before, _ := os.ReadFile(path)
	want := strconv.Itoa(cmd.Process.Pid)
	if string(before) != want+"\n" {
		t.Fatalf("file content %q, want holder pid %s", before, want)
	}
	held := mustHeld(t, path)
	if held.PIDText != want {
		t.Fatalf("pid hint %q, want %s", held.PIDText, want)
	}
	if pid, err := ReadHolder(path); err != nil || pid != cmd.Process.Pid {
		t.Fatalf("ReadHolder = %d, %v, want %d", pid, err, cmd.Process.Pid)
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(before) {
		t.Fatalf("failed acquire changed content: %q -> %q", before, after)
	}

	kill(t, cmd)
	l, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire after SIGKILL: %v", err)
	}
	defer l.Release()
	if data, _ := os.ReadFile(path); string(data) != strconv.Itoa(os.Getpid())+"\n" {
		t.Fatalf("content %q after takeover", data)
	}
}

func TestReleaseIdempotentAndReacquire(t *testing.T) {
	path := lockPath(t)
	l, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Release(); err != nil {
		t.Fatal(err)
	}
	if err := l.Release(); err != nil {
		t.Fatalf("second Release: %v", err)
	}
	if l.Held() || l.File() != nil {
		t.Fatal("Held or File after Release")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("lock file was removed: %v", err)
	}
	l2, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire after Release: %v", err)
	}
	l2.Release()
}

// flock belongs to the open file description. An exec'd child does not hold
// the lock (O_CLOEXEC); a child handed the descriptor does, and keeps it
// after the parent releases.
func TestExecChildDoesNotHoldLock(t *testing.T) {
	path := lockPath(t)
	l, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	startHelper(t, "idle", path) // exec'd, no ExtraFiles
	if err := l.Release(); err != nil {
		t.Fatal(err)
	}
	l2, err := Acquire(path)
	if err != nil {
		t.Fatalf("exec'd child kept the lock: %v", err)
	}
	l2.Release()
}

func TestInheritedDescriptorKeepsLock(t *testing.T) {
	path := lockPath(t)
	l, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	cmd := startHelper(t, "idle", path, l.File())
	if err := l.Release(); err != nil {
		t.Fatal(err)
	}
	mustHeld(t, path)
	kill(t, cmd)
	l2, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire after the inheriting child died: %v", err)
	}
	l2.Release()
}

func TestRefusals(t *testing.T) {
	dir := t.TempDir()
	unsafe := func(t *testing.T, path string) {
		t.Helper()
		l, err := Acquire(path)
		if err == nil {
			l.Release()
			t.Fatal("Acquire succeeded")
		}
		if !errors.Is(err, ErrUnsafeFile) {
			t.Fatalf("err = %v, want ErrUnsafeFile", err)
		}
	}
	t.Run("symlink", func(t *testing.T) {
		target := filepath.Join(dir, "target")
		if err := os.WriteFile(target, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(dir, "link.lock")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		unsafe(t, link)
		if _, err := ReadHolder(link); !errors.Is(err, ErrUnsafeFile) {
			t.Fatalf("ReadHolder on symlink: %v", err)
		}
	})
	t.Run("dangling symlink", func(t *testing.T) {
		link := filepath.Join(dir, "dangling.lock")
		if err := os.Symlink(filepath.Join(dir, "absent"), link); err != nil {
			t.Fatal(err)
		}
		unsafe(t, link)
		if _, err := os.Lstat(filepath.Join(dir, "absent")); err == nil {
			t.Fatal("Acquire created the symlink target")
		}
	})
	t.Run("directory", func(t *testing.T) {
		d := filepath.Join(dir, "adir.lock")
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
		unsafe(t, d)
	})
	for _, mode := range []uint32{0o640, 0o604, 0o660, 0o666, 0o700, 0o2600, 0o4600, 0o1600} {
		t.Run("mode "+strconv.FormatUint(uint64(mode), 8), func(t *testing.T) {
			p := filepath.Join(dir, "wide"+strconv.FormatUint(uint64(mode), 8)+".lock")
			if err := os.WriteFile(p, []byte("123\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := syscall.Chmod(p, mode); err != nil {
				t.Fatal(err)
			}
			unsafe(t, p)
			if data, _ := os.ReadFile(p); string(data) != "123\n" {
				t.Fatalf("refused file was changed: %q", data)
			}
		})
	}
	t.Run("fifo", func(t *testing.T) {
		p := filepath.Join(dir, "fifo.lock")
		if err := syscall.Mkfifo(p, 0o600); err != nil {
			t.Skip(err)
		}
		unsafe(t, p)
	})
}

func TestCheckFileOwnerAndMode(t *testing.T) {
	reg := unix.Stat_t{Mode: unix.S_IFREG | 0o600, Uid: 1000}
	if err := checkFile("p", &reg, 1000); err != nil {
		t.Fatalf("own 0600 file refused: %v", err)
	}
	if err := checkFile("p", &reg, 1001); !errors.Is(err, ErrUnsafeFile) {
		t.Fatalf("file owned by another: %v", err)
	}
	reg.Mode = unix.S_IFREG | 0o400
	if err := checkFile("p", &reg, 1000); err != nil {
		t.Fatalf("narrower mode refused: %v", err)
	}
	reg.Mode = unix.S_IFLNK | 0o600
	if err := checkFile("p", &reg, 1000); !errors.Is(err, ErrUnsafeFile) {
		t.Fatalf("non-regular: %v", err)
	}
}

func TestReadHolderNoHint(t *testing.T) {
	dir := t.TempDir()
	if _, err := ReadHolder(filepath.Join(dir, "absent")); err == nil || errors.Is(err, ErrNoHolder) {
		t.Fatalf("absent file: %v", err)
	}
	for name, content := range map[string]string{"empty": "", "text": "hello\n", "neg": "-5\n", "ctl": "12\x1b[31m\n"} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadHolder(p); !errors.Is(err, ErrNoHolder) {
			t.Fatalf("%s: err = %v, want ErrNoHolder", name, err)
		}
	}
	p := filepath.Join(dir, "ok")
	if err := os.WriteFile(p, []byte("4242\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if pid, err := ReadHolder(p); err != nil || pid != 4242 {
		t.Fatalf("ReadHolder = %d, %v", pid, err)
	}
}

// A held lock whose file text is not a pid gives an ErrHeld without a hint.
func TestHeldWithoutHint(t *testing.T) {
	path := lockPath(t)
	l, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	if err := os.WriteFile(path, []byte("\x1b[2Jevil\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	held := mustHeld(t, path)
	if held.PIDText != "" || strings.ContainsRune(held.Error(), 0x1b) {
		t.Fatalf("hint %q leaked: %q", held.PIDText, held.Error())
	}
}

// Unlinking a lock file lets a second locker take a fresh inode while the
// first holder still believes it holds the lock. This is why the package
// never unlinks, and why Acquire refuses a path that stops naming the locked
// file between the flock and its check.
func TestUnlinkedFileGivesFreshLock(t *testing.T) {
	path := lockPath(t)
	l, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	l2, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire on a recreated file: %v", err)
	}
	l2.Release()
}
