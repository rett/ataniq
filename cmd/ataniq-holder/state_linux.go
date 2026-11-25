//go:build linux

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"golang.org/x/sys/unix"

	"ataniq/internal/leadholder/buildid"
	"ataniq/internal/leadholder/session"
)

// StateFileName is the holder's state file in the state folder. It is written
// before the agent starts (the lead-hosting design, section 1.1). A launcher
// reads its build member under this name (buildid.RecordFileName).
const StateFileName = buildid.RecordFileName

// PreviousFinalName is where a final record left by an earlier epoch is moved
// before a new epoch writes its state file: final.json then always belongs to
// the epoch that ended last, never to one that is still running. A record
// already there is replaced.
const PreviousFinalName = session.FinalName + ".previous"

// stateFileVersion is the format version of the state file.
const stateFileVersion = 1

// stateFile is the content of holder.json: what the holder was started with and
// which process it is. It holds the agent's program and nothing else of the
// agent's command line or environment, which may carry secrets.
type stateFile struct {
	Version        int    `json:"version"`
	Role           string `json:"role"`
	Epoch          uint64 `json:"epoch"`
	LaunchIntent   string `json:"launch_intent"`
	Runtime        string `json:"runtime"`
	RuntimeVersion string `json:"runtime_version"`
	ProfileDigest  string `json:"profile_digest"`
	Tree           string `json:"tree"`
	Pid            int    `json:"pid"`
	// PidStartTicks is the process's start time in clock ticks since boot
	// (field 22 of /proc/PID/stat), which with the pid names the process for good.
	PidStartTicks uint64    `json:"pid_start_ticks"`
	Started       time.Time `json:"started"`
	Cols          int       `json:"cols"`
	Rows          int       `json:"rows"`
	// Agent is the agent's program, ARGV[0].
	Agent string `json:"agent"`
	// Build is the build identity of this holder, read from the executable
	// that runs (/proc/<pid>/exe) when it started. A launcher compares it with
	// the build it expects before it attaches (buildid); a holder of a build
	// before this member writes none. A launcher of a build before this member
	// does not read holder.json at all, and the canary run kit's reader prints
	// any member, so no older reader refuses a file that has it.
	Build buildid.Identity `json:"build"`
	// Scratch is true for a scratch agent, the only kind a build without the
	// ataniq_canary tag hosts.
	Scratch bool `json:"scratch"`
	// The fields below are canary mode's, which exists only in a build with
	// the ataniq_canary tag; scratch mode leaves them out of the file.
	//
	// Canary is true in canary mode.
	Canary bool `json:"canary,omitempty"`
	// Waiver is the waiver of assertion 10, in full, once the start sequence of
	// the sandbox has succeeded with it. Without a waiver it is absent.
	Waiver string `json:"waiver,omitempty"`
	// ProfileVersion is the version of the sandbox profile the start sequence
	// used; ProfileDigest, above, is then the digest of the plan it launched,
	// which is the digest the holder was pinned to.
	ProfileVersion int `json:"profile_version,omitempty"`
	// SandboxStarted is true once the start sequence has succeeded: before
	// that the file names a holder whose agent is not yet running.
	SandboxStarted bool `json:"sandbox_started,omitempty"`
	// StartNotes are what the start sequence noticed and did not refuse on.
	StartNotes []string `json:"start_notes,omitempty"`
}

// startTicks reads the start time, in clock ticks since boot, of the process
// whose stat file it is given.
func startTicks(stat []byte) (uint64, error) {
	// The command name is in parentheses and may hold spaces and parentheses:
	// the fields that count start after the last ")".
	i := bytes.LastIndexByte(stat, ')')
	if i < 0 {
		return 0, errors.New("no command name in the stat file")
	}
	f := bytes.Fields(stat[i+1:])
	// f[0] is field 3 (the state), so field 22 is f[19].
	if len(f) < 20 {
		return 0, errors.New("the stat file is short")
	}
	return strconv.ParseUint(string(f[19]), 10, 64)
}

func selfStartTicks() (uint64, error) {
	b, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return 0, err
	}
	return startTicks(b)
}

// writeStateFile writes holder.json in dir (0600): a temporary name, then a
// rename, so a reader never sees a partial file.
func writeStateFile(dir string, st stateFile) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	dfd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(dfd)
	tmp := ".holder.json.tmp-" + strconv.Itoa(os.Getpid())
	fd, err := unix.Openat(dfd, tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if errors.Is(err, unix.EEXIST) {
		// A leftover of an earlier holder with this pid.
		if uerr := unix.Unlinkat(dfd, tmp, 0); uerr != nil && !errors.Is(uerr, unix.ENOENT) {
			return uerr
		}
		fd, err = unix.Openat(dfd, tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	}
	if err != nil {
		return err
	}
	fail := func(err error) error {
		unix.Close(fd)
		_ = unix.Unlinkat(dfd, tmp, 0)
		return err
	}
	// The mode is exactly 0600, whatever the umask.
	if err := unix.Fchmod(fd, 0o600); err != nil {
		return fail(err)
	}
	for rest := b; len(rest) > 0; {
		n, err := unix.Write(fd, rest)
		if err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}
			return fail(err)
		}
		rest = rest[n:]
	}
	if err := unix.Fsync(fd); err != nil {
		return fail(err)
	}
	if err := unix.Close(fd); err != nil {
		_ = unix.Unlinkat(dfd, tmp, 0)
		return err
	}
	if err := unix.Renameat(dfd, tmp, dfd, StateFileName); err != nil {
		_ = unix.Unlinkat(dfd, tmp, 0)
		return err
	}
	return unix.Fsync(dfd)
}

// retireFinalRecord moves a final record of an earlier epoch aside, to
// PreviousFinalName, replacing an older one. Without a final record it does
// nothing.
func retireFinalRecord(dir string) error {
	dfd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(dfd)
	if err := unix.Renameat(dfd, session.FinalName, dfd, PreviousFinalName); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil
		}
		return err
	}
	return unix.Fsync(dfd)
}

// removeStateFile removes holder.json from dir; a missing file is not an error.
func removeStateFile(dir string) error {
	dfd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(dfd)
	if err := unix.Unlinkat(dfd, StateFileName, 0); err != nil && !errors.Is(err, unix.ENOENT) {
		return err
	}
	return nil
}

// checkPrivateDir checks that path is a real directory (not a link) of this
// user with mode 0700.
func checkPrivateDir(path string) error {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		switch {
		case errors.Is(err, unix.ENOENT):
			return errors.New("does not exist")
		case errors.Is(err, unix.ELOOP), errors.Is(err, unix.ENOTDIR):
			return errors.New("is not a real directory (a link or another kind of file)")
		}
		return fmt.Errorf("cannot be opened: %w", err)
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return errors.New("cannot be looked at")
	}
	if int(st.Uid) != os.Geteuid() {
		return errors.New("is not owned by this user")
	}
	if m := st.Mode & 0o7777; m != 0o700 {
		return fmt.Errorf("has mode %04o, want 0700", m)
	}
	return nil
}
