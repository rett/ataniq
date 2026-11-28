package cgroupsig

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

const (
	// MaxProcsBytes bounds how much of one cgroup.procs file is read.
	MaxProcsBytes = 1 << 20

	// Batch is how many pidfds are open at once while a list of processes is
	// signalled.
	Batch = 128

	killFile  = "cgroup.kill"
	procsFile = "cgroup.procs"
)

var (
	// ErrGone means the control group does not exist (any more): it was
	// removed, or a write to it found it removed.
	ErrGone = errors.New("cgroupsig: control group is gone")
	// ErrIdentity means the directory at the group's path is not the one the
	// recorded identity names. Nothing was written to it.
	ErrIdentity = errors.New("cgroupsig: control group identity differs")
	// ErrNoKill means the group exists but has no cgroup.kill file (a kernel
	// before 5.14). Nothing was sent, and nothing else is tried.
	ErrNoKill = errors.New("cgroupsig: cgroup.kill is not available")
)

// Identity names a directory object by device, inode and owner.
type Identity struct {
	Device uint64
	Inode  uint64
	UID    uint32
}

// Kernel is the pidfd part of the host, a seam for tests.
type Kernel interface {
	// PidfdOpen opens a pidfd for pid.
	PidfdOpen(pid int) (fd int, err error)
	// PidfdSendSignal sends sig through a pidfd.
	PidfdSendSignal(fd int, sig syscall.Signal) error
	// Close closes a descriptor PidfdOpen returned.
	Close(fd int) error
}

// HostKernel is the [Kernel] of the running host.
type HostKernel struct{}

var _ Kernel = HostKernel{}

func (HostKernel) PidfdOpen(pid int) (int, error) { return unix.PidfdOpen(pid, 0) }

func (HostKernel) PidfdSendSignal(fd int, sig syscall.Signal) error {
	return unix.PidfdSendSignal(fd, sig, nil, 0)
}

func (HostKernel) Close(fd int) error { return unix.Close(fd) }

// CountsAsFailure says whether the error of a pidfd_open or pidfd_send_signal
// is a failure to count. ESRCH means the process is gone, which is what the
// caller wanted; anything else (ENOSYS before Linux 5.3, EPERM, EMFILE) means
// a process that should have been signalled may not have been.
func CountsAsFailure(err error) bool { return err != unix.ESRCH }

// Member is a process listed in a cgroup.procs file, and the name of the file
// that lists it (the key under which [SignalListed] asks its relist function).
type Member struct {
	PID   int
	Procs string
}

// Outcome counts what one [SignalListed] did.
type Outcome struct {
	// Signalled is how many processes a signal was sent to.
	Signalled int
	// PidfdFailures counts pidfd_open and pidfd_send_signal calls that failed
	// for another reason than a process that is gone.
	PidfdFailures int
	// Unconfirmed counts processes whose membership could not be confirmed
	// because the list they came from could not be read again; they were not
	// signalled.
	Unconfirmed int
}

// SignalListed signals the members through pidfds opened now, in the order
// given, but only those that relist (called once per distinct Procs, after
// every pidfd is open) still shows in the file they came from. A pid that
// took the number after it was listed is not in the group and is not listed,
// so it is skipped. relist returns nil when the list cannot be read.
func SignalListed(k Kernel, members []Member, sig syscall.Signal, relist func(procs string) map[int]bool) Outcome {
	type opened struct {
		m  Member
		fd int
	}
	var out Outcome
	var fds []opened
	defer func() {
		for _, o := range fds {
			_ = k.Close(o.fd)
		}
	}()
	for _, m := range members {
		fd, err := k.PidfdOpen(m.PID)
		if err != nil {
			if CountsAsFailure(err) {
				out.PidfdFailures++
			}
			continue
		}
		fds = append(fds, opened{m, fd})
	}
	lists := map[string]map[int]bool{}
	for _, o := range fds {
		set, seen := lists[o.m.Procs]
		if !seen {
			set = relist(o.m.Procs)
			lists[o.m.Procs] = set
		}
		if set == nil {
			out.Unconfirmed++
			continue
		}
		if !set[o.m.PID] {
			continue
		}
		if err := k.PidfdSendSignal(o.fd, sig); err != nil {
			if CountsAsFailure(err) {
				out.PidfdFailures++
			}
			continue
		}
		out.Signalled++
	}
	return out
}

// ReadProcs reads a cgroup.procs file, bounded. truncated is true when the
// file is longer than [MaxProcsBytes] (the pids read are then none).
func ReadProcs(path string) (pids []int, truncated bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	return readProcsFrom(f)
}

func readProcsFrom(r io.Reader) (pids []int, truncated bool, err error) {
	b, err := io.ReadAll(io.LimitReader(r, MaxProcsBytes+1))
	if err != nil {
		return nil, false, err
	}
	if len(b) > MaxProcsBytes {
		return nil, true, nil
	}
	for _, line := range strings.Fields(string(b)) {
		pid, perr := strconv.Atoi(line)
		if perr != nil || pid <= 0 {
			if len(line) > 32 {
				line = line[:32]
			}
			return nil, false, fmt.Errorf("unexpected line in cgroup.procs: %q", line)
		}
		pids = append(pids, pid)
	}
	return pids, false, nil
}

// Group is a control group directory pinned by an open descriptor. It is safe
// for concurrent use.
type Group struct {
	k    Kernel
	root string
	path string
	want Identity
	f    *os.File
}

// Open resolves path (a clean absolute control group path, not "/") below
// root, without following links, and returns the group when the directory's
// identity is want. A path that does not exist is [ErrGone]; another
// directory there is [ErrIdentity]. k carries the pidfd calls; nil means the
// host's.
func Open(k Kernel, root, path string, want Identity) (*Group, error) {
	if k == nil {
		k = HostKernel{}
	}
	if !strings.HasPrefix(path, "/") || path == "/" || filepath.Clean(path) != path || strings.ContainsAny(path, "\x00\n\r") {
		return nil, errors.New("cgroupsig: invalid control group path")
	}
	f, id, err := openDir(root, path)
	if err != nil {
		if gone(err) {
			return nil, fmt.Errorf("%w: %s", ErrGone, path)
		}
		return nil, fmt.Errorf("cgroupsig: open %s: %w", path, err)
	}
	if id != want {
		_ = f.Close()
		return nil, fmt.Errorf("%w: %s", ErrIdentity, path)
	}
	return &Group{k: k, root: root, path: path, want: want, f: f}, nil
}

// Close releases the descriptor.
func (g *Group) Close() error { return g.f.Close() }

// gone says whether err proves that a path does not exist (as opposed to one
// that could not be read).
func gone(err error) bool {
	return errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ENODEV) || errors.Is(err, syscall.ENOTDIR)
}

// openDir opens root and then each component of path with O_NOFOLLOW and
// returns the last directory with its identity from fstat.
func openDir(root, path string) (*os.File, Identity, error) {
	d, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, Identity{}, err
	}
	for _, p := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		n, e := unix.Openat(d, p, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		_ = unix.Close(d)
		if e != nil {
			return nil, Identity{}, e
		}
		d = n
	}
	var st unix.Stat_t
	if err := unix.Fstat(d, &st); err != nil {
		_ = unix.Close(d)
		return nil, Identity{}, err
	}
	return os.NewFile(uintptr(d), path), Identity{uint64(st.Dev), uint64(st.Ino), st.Uid}, nil
}

// vanished says whether the group's path now proves to be removed or another
// directory. An error other than a missing path proves nothing.
func (g *Group) vanished() bool {
	f, id, err := openDir(g.root, g.path)
	if err != nil {
		return gone(err)
	}
	_ = f.Close()
	return id != g.want
}

// openIn opens a file of the group through the pinned descriptor.
func (g *Group) openIn(name string, flags int) (int, error) {
	return unix.Openat(int(g.f.Fd()), name, flags|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
}

// Kill writes "1" to the group's cgroup.kill through the pinned descriptor. A
// group that was removed is [ErrGone]; a live group with no cgroup.kill is
// [ErrNoKill]. No signal is sent by any other means.
func (g *Group) Kill() error {
	fd, err := g.openIn(killFile, unix.O_WRONLY)
	if err != nil {
		return g.writeFailure(err)
	}
	defer unix.Close(fd)
	n, err := unix.Write(fd, []byte("1"))
	switch {
	case err != nil:
		return g.writeFailure(err)
	case n != 1:
		return fmt.Errorf("cgroupsig: short write to %s", killFile)
	}
	return nil
}

func (g *Group) writeFailure(err error) error {
	if errors.Is(err, syscall.ENODEV) || (gone(err) && g.vanished()) {
		return fmt.Errorf("%w: %s", ErrGone, g.path)
	}
	if errors.Is(err, syscall.ENOENT) {
		return fmt.Errorf("%w: %s", ErrNoKill, g.path)
	}
	return fmt.Errorf("cgroupsig: write %s of %s: %w", killFile, g.path, err)
}

// list reads cgroup.procs of the group through the pinned descriptor.
func (g *Group) list() ([]int, error) {
	fd, err := g.openIn(procsFile, unix.O_RDONLY)
	if err != nil {
		if errors.Is(err, syscall.ENODEV) || (gone(err) && g.vanished()) {
			return nil, fmt.Errorf("%w: %s", ErrGone, g.path)
		}
		return nil, fmt.Errorf("cgroupsig: open %s of %s: %w", procsFile, g.path, err)
	}
	f := os.NewFile(uintptr(fd), procsFile)
	defer f.Close()
	pids, truncated, err := readProcsFrom(f)
	switch {
	case err != nil && (errors.Is(err, syscall.ENODEV) || errors.Is(err, syscall.ENOENT)):
		return nil, fmt.Errorf("%w: %s", ErrGone, g.path)
	case err != nil:
		return nil, fmt.Errorf("cgroupsig: read %s of %s: %w", procsFile, g.path, err)
	case truncated:
		return nil, fmt.Errorf("cgroupsig: %s of %s lists too many processes", procsFile, g.path)
	}
	return pids, nil
}

// relist is the membership check of [SignalListed] for this group: the set
// listed now, nil when it cannot be read.
func (g *Group) relist(string) map[int]bool {
	pids, err := g.list()
	if err != nil {
		return nil
	}
	set := make(map[int]bool, len(pids))
	for _, p := range pids {
		set[p] = true
	}
	return set
}

// Signal sends sig to the processes the group lists, each through a pidfd
// opened before the group is listed again (see [SignalListed]). mainPID, when
// it is listed, goes first; a number that is not listed is skipped. A group
// that is gone is [ErrGone]. A failure of a pidfd call, or a process whose
// membership could not be confirmed, is an error after the others were
// signalled.
func (g *Group) Signal(sig syscall.Signal, mainPID int) (Outcome, error) {
	pids, err := g.list()
	if err != nil {
		return Outcome{}, err
	}
	members := make([]Member, 0, len(pids))
	for _, p := range pids {
		if p == mainPID {
			members = append(members, Member{PID: p})
			break
		}
	}
	for _, p := range pids {
		if p != mainPID {
			members = append(members, Member{PID: p})
		}
	}
	var total Outcome
	for rest := members; len(rest) > 0; {
		n := min(len(rest), Batch)
		o := SignalListed(g.k, rest[:n], sig, g.relist)
		total.Signalled += o.Signalled
		total.PidfdFailures += o.PidfdFailures
		total.Unconfirmed += o.Unconfirmed
		rest = rest[n:]
	}
	if total.Unconfirmed > 0 && g.vanished() {
		return total, fmt.Errorf("%w: %s", ErrGone, g.path)
	}
	if total.PidfdFailures > 0 || total.Unconfirmed > 0 {
		return total, fmt.Errorf("cgroupsig: signal %d of %s: %d pidfd failure(s), %d unconfirmed", int(sig), g.path, total.PidfdFailures, total.Unconfirmed)
	}
	return total, nil
}
