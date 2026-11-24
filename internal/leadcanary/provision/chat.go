//go:build linux && ataniq_canary

package provision

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"

	"golang.org/x/sys/unix"

	"ataniq/internal/leadcanary"
	"ataniq/internal/leadchat"
	"ataniq/internal/leadrole"
	"ataniq/internal/leadsandbox/safefile"
)

// This file is what a scratch lead needs for its replies to the owner, beside
// what provisioning makes: the reply folder in the lead's home and the text of
// its instruction file. The daemon calls [EnsureChat] at every lead.define (an
// already provisioned lead included) and at every lead.start, before the
// lead's unit starts.

// ErrChat is wrapped when the reply folder or the instruction file of a lead
// is not as [EnsureChat] makes it: the reply folder is not a folder of the
// caller with mode 0700 exactly, or the instruction file is not a regular
// file of the caller with mode 0600 and one link.
var ErrChat = errors.New("provision: the lead's reply folder or instruction file is not as the daemon makes it")

// maxInstructionsBytes is the largest instruction file that is read to judge
// it. A larger one is the owner's and is kept.
const maxInstructionsBytes = 64 << 10

// instructionsTmp is the temporary name the instruction text is written under
// in the lead's folder, and renamed from. The lead's folder is not bound into
// the sandbox, so only the daemon writes in it.
const instructionsTmp = "." + InstructionsName + ".tmp"

// Instructions says what [EnsureChat] found or did with the lead's
// instruction file.
type Instructions string

// The values of [Instructions].
const (
	// InstructionsWritten: the file was the empty one provisioning makes,
	// the lead was proven stopped, and the text of [LeadInstructions]
	// replaced it.
	InstructionsWritten Instructions = "written"
	// InstructionsCurrent: the file holds the text of [LeadInstructions].
	InstructionsCurrent Instructions = "current"
	// InstructionsLegacy: the file is the empty one provisioning makes and
	// the lead was not proven stopped: it is kept as it is, because a lead
	// that runs sees the file it started with (the bind is of the file),
	// and is replaced at a later define or start of the stopped lead.
	InstructionsLegacy Instructions = "legacy-empty"
	// InstructionsOwner: the file holds something else, which is the
	// owner's: it is never overwritten.
	InstructionsOwner Instructions = "owner"
)

// ChatOptions are what the caller of [EnsureChat] knows of the lead.
type ChatOptions struct {
	// Stopped is true when the caller proved the lead stopped (the
	// registry shows it defined and never started, stopped or ended, with
	// no launch pending, or does not know it) and holds the role's
	// operation lock, so that no start runs beside: only then is an empty
	// instruction file replaced.
	Stopped bool
}

// ChatResult is what [EnsureChat] did.
type ChatResult struct {
	// OutboxMade is true when the reply folder was not there and was made.
	OutboxMade bool
	// Instructions is what became of the instruction file.
	Instructions Instructions
}

// EnsureChat makes the reply folder of a provisioned scratch lead and writes
// the text of its instruction file. Both are reached from the state root's
// handle, one name at a time, without following a link or crossing a mount
// point:
//
//   - the reply folder <state root>/leads/<layout role>/home/.ataniq-replies
//     ([OutboxInside] in the sandbox) is made with mode 0700 when it is not
//     there, in the lead's home (a folder of the caller that group and other
//     cannot write), and is then opened by its descriptor and must be a
//     folder of the caller with mode 0700 exactly, on the home's file system.
//     One that is there with another mode, of another user, a link or not a
//     folder is refused ([ErrChat]) and left as it is: the daemon never
//     changes the mode of a folder the lead may have changed;
//   - the instruction file <state root>/leads/<layout role>/instructions.md,
//     which must be a regular file of the caller with mode 0600 and one link
//     ([ErrChat] otherwise), is judged by its handle: a file that holds the
//     text of [LeadInstructions] is kept; the empty file provisioning makes
//     is replaced by that text only when opts.Stopped says the lead is proven
//     stopped, and kept otherwise; a file that holds anything else is the
//     owner's and is never overwritten. The text is written by handle to a
//     temporary file in the lead's folder (made exclusively, mode 0600),
//     synced, and renamed over the file, and the folder is synced; a file
//     that changed between the judgement and the rename is not replaced.
//
// The lead must be provisioned: its home and its instruction file are
// provisioning's. EnsureChat looks at no reply in the folder.
func EnsureChat(cfg leadcanary.Config, opts ChatOptions) (ChatResult, error) {
	if cfg.Reserved != leadrole.ReservedScratch {
		return ChatResult{}, fmt.Errorf("%w: EnsureChat takes a scratch lead", ErrScratchKind)
	}
	p, err := PathsOf(cfg)
	if err != nil {
		return ChatResult{}, err
	}
	root, err := safefile.OpenRootNoLinks(p.StateRoot)
	if err != nil {
		return ChatResult{}, fmt.Errorf("state root %s: %w", p.StateRoot, err)
	}
	defer root.Close()
	var res ChatResult
	if res.OutboxMade, err = ensureOutbox(root, p); err != nil {
		return ChatResult{}, err
	}
	if res.Instructions, err = ensureInstructions(root, p, opts.Stopped); err != nil {
		return ChatResult{}, err
	}
	return res, nil
}

// dirFD opens the folder rel beneath the root and returns a descriptor the
// caller owns, with what fstat says of it.
func dirFD(root *safefile.Root, rel string) (int, safefile.Info, error) {
	h, err := root.OpenDirHandle(rel)
	if err != nil {
		return -1, safefile.Info{}, err
	}
	defer h.Close()
	info, err := h.Info()
	if err != nil {
		return -1, safefile.Info{}, err
	}
	fd, err := h.Dup()
	if err != nil {
		return -1, safefile.Info{}, err
	}
	return fd, info, nil
}

// ensureOutbox makes the reply folder when it is not there and checks it.
func ensureOutbox(root *safefile.Root, p Paths) (made bool, err error) {
	home, info, err := dirFD(root, p.stateRel(p.Home))
	if err != nil {
		return false, fmt.Errorf("the lead's home %s: %w", p.Home, err)
	}
	defer unix.Close(home)
	if !owned(info) || info.Mode&0o022 != 0 {
		return false, fmt.Errorf("%w: the lead's home %s is not a folder of the caller closed to group and other writes", ErrChat, p.Home)
	}
	at := p.Home + "/" + leadchat.OutboxName
	switch err := unix.Mkdirat(home, leadchat.OutboxName, dirMode); {
	case err == nil:
		made = true
	case err == unix.EEXIST:
	default:
		return false, fmt.Errorf("%s: %w", at, err)
	}
	fd, err := unix.Openat(home, leadchat.OutboxName, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		// A link answers ELOOP or ENOTDIR here, and a file ENOTDIR.
		return made, fmt.Errorf("%w: %s cannot be opened as a folder: %w", ErrChat, at, err)
	}
	defer unix.Close(fd)
	if made {
		// The umask may have narrowed the mode; 0700 is set exactly on the
		// folder this call made, and on no other.
		if err := unix.Fchmod(fd, dirMode); err != nil {
			return made, fmt.Errorf("%s: %w", at, err)
		}
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return made, fmt.Errorf("%s: %w", at, err)
	}
	switch {
	case st.Mode&unix.S_IFMT != unix.S_IFDIR:
		return made, fmt.Errorf("%w: %s is not a folder", ErrChat, at)
	case int(st.Uid) != unix.Geteuid():
		return made, fmt.Errorf("%w: %s is not the caller's", ErrChat, at)
	case st.Mode&0o7777 != dirMode:
		return made, fmt.Errorf("%w: %s has mode %04o; it must stay 0700", ErrChat, at, st.Mode&0o7777)
	case uint64(st.Dev) != info.Dev:
		return made, fmt.Errorf("%w: %s is on another file system than the lead's home", ErrChat, at)
	}
	if made {
		if err := syncDirFD(home); err != nil {
			return made, fmt.Errorf("%s: %w", p.Home, err)
		}
	}
	return made, nil
}

// syncDirFD syncs the folder that the descriptor (which may be O_PATH) names.
func syncDirFD(fd int) error {
	d, err := unix.Openat(fd, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(d)
	return unix.Fsync(d)
}

// ensureInstructions judges the instruction file and writes the text when it
// is the empty one and the lead is proven stopped.
func ensureInstructions(root *safefile.Root, p Paths, stopped bool) (Instructions, error) {
	lead, _, err := dirFD(root, p.stateRel(p.LeadDir))
	if err != nil {
		return "", fmt.Errorf("the lead's folder %s: %w", p.LeadDir, err)
	}
	defer unix.Close(lead)
	at := p.InstructionFile
	fd, err := unix.Openat(lead, InstructionsName, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		if err == unix.ENOENT {
			return "", fmt.Errorf("%s: %w", at, fs.ErrNotExist)
		}
		return "", fmt.Errorf("%w: %s cannot be opened as a file: %w", ErrChat, at, err)
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return "", fmt.Errorf("%s: %w", at, err)
	}
	switch {
	case st.Mode&unix.S_IFMT != unix.S_IFREG:
		return "", fmt.Errorf("%w: %s is not a regular file", ErrChat, at)
	case int(st.Uid) != unix.Geteuid(), st.Mode&0o7777 != 0o600, st.Nlink != 1:
		return "", fmt.Errorf("%w: %s is not a file of the caller with mode 0600 and one link", ErrChat, at)
	case st.Size > maxInstructionsBytes:
		return InstructionsOwner, nil
	}
	text := []byte(LeadInstructions())
	if st.Size != 0 {
		if st.Size != int64(len(text)) {
			return InstructionsOwner, nil
		}
		got, err := readAllFD(fd, st.Size)
		if err != nil {
			return "", fmt.Errorf("%s: %w", at, err)
		}
		if bytes.Equal(got, text) {
			return InstructionsCurrent, nil
		}
		return InstructionsOwner, nil
	}
	if !stopped {
		return InstructionsLegacy, nil
	}
	if err := replaceFile(lead, InstructionsName, &st, text); err != nil {
		return "", fmt.Errorf("%s: %w", at, err)
	}
	return InstructionsWritten, nil
}

// readAllFD reads exactly n bytes from fd, from its start.
func readAllFD(fd int, n int64) ([]byte, error) {
	buf := make([]byte, n)
	got := 0
	for got < len(buf) {
		k, err := unix.Pread(fd, buf[got:], int64(got))
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return nil, err
		}
		if k == 0 {
			return nil, errors.New("the file changed while it was read")
		}
		got += k
	}
	return buf, nil
}

// replaceFile writes data to a temporary file of the folder dir (made
// exclusively, mode 0600), syncs it, and renames it over name when name is
// still the file judged (seen); then it syncs the folder. A temporary file a
// crash left is removed first.
func replaceFile(dir int, name string, seen *unix.Stat_t, data []byte) error {
	if err := unix.Unlinkat(dir, instructionsTmp, 0); err != nil && err != unix.ENOENT {
		return fmt.Errorf("a leftover temporary file cannot be removed: %w", err)
	}
	tmp, err := unix.Openat(dir, instructionsTmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return err
	}
	done := false
	defer func() {
		if !done {
			_ = unix.Unlinkat(dir, instructionsTmp, 0)
		}
	}()
	err = func() error {
		defer unix.Close(tmp)
		if err := unix.Fchmod(tmp, 0o600); err != nil {
			return err
		}
		for off := 0; off < len(data); {
			k, err := unix.Write(tmp, data[off:])
			if err == unix.EINTR {
				continue
			}
			if err != nil {
				return err
			}
			off += k
		}
		return unix.Fsync(tmp)
	}()
	if err != nil {
		return err
	}
	var now unix.Stat_t
	if err := unix.Fstatat(dir, name, &now, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if now.Dev != seen.Dev || now.Ino != seen.Ino || now.Size != seen.Size || now.Mtim != seen.Mtim || now.Ctim != seen.Ctim {
		return fmt.Errorf("%w: the file changed while it was judged; it is left as it is", ErrChat)
	}
	if err := unix.Renameat(dir, instructionsTmp, dir, name); err != nil {
		return err
	}
	done = true
	return syncDirFD(dir)
}
