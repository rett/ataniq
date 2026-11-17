package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// maxSocketPath is the longest unix socket path: sun_path is 108 bytes with
// the terminating zero.
const maxSocketPath = 107

// layout is where one scratch agent lives. Nothing in it is shared with a real
// lead: the paths are under a scratch folder of their own.
type layout struct {
	name        string
	runtimeBase string // $XDG_RUNTIME_DIR
	stateBase   string // $XDG_STATE_HOME or ~/.local/state
	runtime     string // $XDG_RUNTIME_DIR/ataniq/scratch/NAME
	state       string // $XDG_STATE_HOME/ataniq/scratch/NAME
	home        string // state/home, the agent's disposable home and tree
	controlSock string
	attachSock  string
	ticketFile  string
	epochFile   string
	daemonLock  string
	roleLock    string
}

func isAbs(p string) bool { return strings.HasPrefix(p, "/") }

// layoutFor computes the folders for name from the environment. It refuses a
// runtime folder whose sockets could not be bound.
func (a *app) layoutFor(name string) (layout, error) {
	rt := a.getenv("XDG_RUNTIME_DIR")
	if !isAbs(rt) {
		return layout{}, errors.New("XDG_RUNTIME_DIR is not set to an absolute path")
	}
	st := a.getenv("XDG_STATE_HOME")
	if !isAbs(st) {
		h := a.getenv("HOME")
		if !isAbs(h) {
			return layout{}, errors.New("neither XDG_STATE_HOME nor HOME is set to an absolute path")
		}
		st = filepath.Join(h, ".local", "state")
	}
	l := layout{name: name, runtimeBase: rt, stateBase: st}
	l.runtime = filepath.Join(rt, "ataniq", "scratch", name)
	l.state = filepath.Join(st, "ataniq", "scratch", name)
	l.home = filepath.Join(l.state, "home")
	l.controlSock = filepath.Join(l.runtime, "control.sock")
	l.attachSock = filepath.Join(l.runtime, "attach.sock")
	l.ticketFile = filepath.Join(l.runtime, "ticket")
	l.epochFile = filepath.Join(l.state, "epoch")
	l.daemonLock = filepath.Join(l.state, "daemon.lock")
	l.roleLock = filepath.Join(l.state, "role.lock")
	if n := len(l.controlSock); n > maxSocketPath {
		return layout{}, fmt.Errorf("the runtime folder is too deep for a unix socket: control.sock would have a %d-byte path, the limit is %d; use a shorter NAME or a shorter XDG_RUNTIME_DIR", n, maxSocketPath)
	}
	return l, nil
}

// ensureDirs makes the folders from base down through each element of rel,
// mode 0700, owned by uid. A folder that exists must be a real folder (not a
// link) of uid; a looser mode is tightened.
func ensureDirs(base string, uid int, rel ...string) error {
	p := base
	for _, e := range rel {
		p = filepath.Join(p, e)
		if err := os.Mkdir(p, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		if !fi.IsDir() {
			return fmt.Errorf("%s is not a folder", p)
		}
		if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != uid {
			return fmt.Errorf("%s is not owned by this user", p)
		}
		if fi.Mode().Perm()&0o077 != 0 {
			if err := os.Chmod(p, 0o700); err != nil {
				return err
			}
		}
	}
	return nil
}

// makeFolders creates the runtime, state and home folders of l. The base
// folders (XDG_RUNTIME_DIR, the state home) must already exist; ensureDirs
// makes everything below them, creating the state home's own parents too.
func (a *app) makeFolders(l layout) error {
	if err := ensureDirs(l.runtimeBase, a.uid, "ataniq", "scratch", l.name); err != nil {
		return err
	}
	if err := os.MkdirAll(l.stateBase, 0o700); err != nil {
		return err
	}
	return ensureDirs(l.stateBase, a.uid, "ataniq", "scratch", l.name, "home")
}

// randomHex returns 2n hexadecimal characters from crypto/rand.
func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

var errLockTimeout = errors.New("timed out waiting for a lock held by another ataniq-scratch")

// flockWait takes the flock how (unix.LOCK_SH or LOCK_EX) on f, waiting up to
// wait for another holder to let go.
func flockWait(f *os.File, how int, wait time.Duration) error {
	deadline := time.Now().Add(wait)
	for {
		err := unix.Flock(int(f.Fd()), how|unix.LOCK_NB)
		if err == nil {
			return nil
		}
		if err != unix.EWOULDBLOCK && err != unix.EINTR {
			return err
		}
		if !time.Now().Before(deadline) {
			return errLockTimeout
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// openPrivate opens (creating it when create is set) a regular file with mode
// 0600, refusing a link, and tightens the mode of an existing file.
func openPrivate(path string, flag int, create bool) (*os.File, error) {
	if create {
		flag |= os.O_CREATE
	}
	f, err := os.OpenFile(path, flag|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		f.Close()
		if err == nil {
			err = errors.New("not a regular file")
		}
		return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	if fi.Mode().Perm() != 0o600 {
		if err := f.Chmod(0o600); err != nil {
			f.Close()
			return nil, err
		}
	}
	return f, nil
}

// maxEpochFile bounds what is read from the epoch file.
const maxEpochFile = 32

func parseEpoch(b []byte) (uint64, error) {
	s := strings.TrimSuffix(string(b), "\n")
	if s == "" {
		return 0, nil
	}
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil || strconv.FormatUint(n, 10) != s {
		return 0, errors.New("the epoch file is damaged")
	}
	return n, nil
}

func readEpochFrom(f *os.File) (uint64, error) {
	buf := make([]byte, maxEpochFile+1)
	n, err := f.ReadAt(buf, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return 0, err
	}
	if n > maxEpochFile {
		return 0, errors.New("the epoch file is damaged")
	}
	return parseEpoch(buf[:n])
}

// readEpoch returns the epoch of the last start, under a shared lock so that a
// start that is writing it is waited for.
func readEpoch(path string, wait time.Duration) (uint64, error) {
	f, err := openPrivate(path, os.O_RDONLY, false)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, errors.New("no such scratch agent: it was never started")
		}
		return 0, err
	}
	defer f.Close()
	if err := flockWait(f, unix.LOCK_SH, wait); err != nil {
		return 0, err
	}
	n, err := readEpochFrom(f)
	if err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, errors.New("no such scratch agent: it has no epoch")
	}
	return n, nil
}

// epochHold is an exclusive lock on the epoch file with the new epoch
// written. start holds it until the holder has answered, so that two starts
// never take the same number and no other command reads a half-started epoch.
type epochHold struct {
	f    *os.File
	prev uint64
	N    uint64
}

// nextEpoch increases the counter in path (the first epoch is 1) and returns
// the lock on it.
func nextEpoch(path string, wait time.Duration) (*epochHold, error) {
	f, err := openPrivate(path, os.O_RDWR, true)
	if err != nil {
		return nil, err
	}
	if err := flockWait(f, unix.LOCK_EX, wait); err != nil {
		f.Close()
		return nil, err
	}
	prev, err := readEpochFrom(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	if prev == math.MaxUint64 {
		f.Close()
		return nil, errors.New("the epoch counter is exhausted")
	}
	h := &epochHold{f: f, prev: prev, N: prev + 1}
	if err := h.write(h.N); err != nil {
		f.Close()
		return nil, err
	}
	return h, nil
}

func (h *epochHold) write(n uint64) error {
	if n == 0 {
		return h.f.Truncate(0)
	}
	if err := h.f.Truncate(0); err != nil {
		return err
	}
	if _, err := h.f.WriteAt([]byte(strconv.FormatUint(n, 10)+"\n"), 0); err != nil {
		return err
	}
	return h.f.Sync()
}

// Undo puts the previous number back, for a start in which no holder was
// started, so that the holder that is running for the name keeps its epoch.
func (h *epochHold) Undo() error { return h.write(h.prev) }

// Release lets go of the lock.
func (h *epochHold) Release() error { return h.f.Close() }
