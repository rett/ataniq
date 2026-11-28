//go:build linux && ataniq_canary

package provision

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"strconv"

	"golang.org/x/sys/unix"

	"ataniq/internal/leadcanary"
	"ataniq/internal/leadrole"
	"ataniq/internal/leadsandbox/safefile"
)

// This file is what a scratch lead needs for its replies to the owner, beside
// what provisioning makes: the text of its instruction file. The daemon calls
// [EnsureChat] at every lead.define (an already provisioned lead included)
// and at every lead.start, before the lead's unit starts. It never makes,
// opens or judges the lead's reply folder: the desk's turn-on of the lead's
// chat makes that folder afresh (package leadchat), so a lead whose chat is
// off starts whatever its home holds at that name.

// ErrChat is wrapped when the instruction file of a lead is not as
// [EnsureChat] needs it: not a regular file of the caller with mode 0600 and
// one link, reached without a link or a mount point, or one that changed
// while the empty file was being replaced.
var ErrChat = errors.New("provision: the lead's instruction file is not as the daemon makes it")

// maxInstructionsBytes is the largest instruction file that is read to judge
// it. A larger one is the owner's and is kept.
const maxInstructionsBytes = 64 << 10

// instructionsTmp is the temporary name the instruction text is written under
// in the lead's folder, and exchanged from. The lead's folder is not bound
// into the sandbox, so only the daemon and the owner write in it, and only
// the daemon uses this name.
const instructionsTmp = "." + InstructionsName + ".tmp"

// KeptPrefix starts the recovery names of the lead's folder: a version of the
// instruction file that an upgrade displaced, or that a crash left at the
// temporary name, is kept as instructions.md.displaced-<n>, n from 1 to
// [MaxKept], the first name that is free. The daemon never deletes, reads or
// overwrites one; the owner looks at it and deletes it.
const KeptPrefix = InstructionsName + ".displaced-"

// MaxKept is the most recovery names (KeptPrefix 1 to MaxKept) the daemon
// uses. When every one is taken, a version to keep stays where it is and the
// call is refused ([ErrChat]).
const MaxKept = 64

// keptName is the n-th recovery name.
func keptName(n int) string { return KeptPrefix + strconv.Itoa(n) }

// resolveFlags is what the lookup of the instruction file below the lead's
// folder uses: beneath the folder, no symbolic or magic link, and no mount
// point crossed (a file bound over the name is refused).
const resolveFlags = unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_XDEV

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
	// Instructions is what became of the instruction file.
	Instructions Instructions
	// Leftover is true when the call found a file at the temporary name,
	// which a crash of an earlier upgrade left, and kept it under a
	// recovery name ([KeptPrefix]).
	Leftover bool
	// Kept is how many recovery names ([KeptPrefix]) hold a version after
	// the call. The daemon never deletes them; the owner does.
	Kept int
}

// EnsureChat writes the text of the instruction file of a provisioned scratch
// lead, <state root>/leads/<layout role>/instructions.md. The lead's folder is
// reached from the state root's handle, one name at a time, and the file
// below it by openat2, without following a link or crossing a mount point.
// The file must be a regular file of the caller with mode 0600 and one link
// ([ErrChat] otherwise), and is judged by its handle:
//
//   - a file that holds the text of [LeadInstructions] is kept; it is
//     recognised by a read of one byte more than the text, compared whole,
//     with its size unchanged after the read;
//   - the empty file provisioning makes is replaced by that text only when
//     opts.Stopped says the lead is proven stopped, and kept otherwise. The
//     text is written to a temporary file in the lead's folder (made
//     exclusively, mode 0600) and synced; the empty file is checked again by
//     its handle and by its name, and the temporary file and the name are
//     exchanged (RENAME_EXCHANGE). The file that the exchange took out of
//     the name must be the empty file that was checked: when it is not (an
//     owner's edit came in the moment between), the exchange is undone, the
//     owner's file is back at its name, and the call is refused ([ErrChat]).
//     Otherwise the empty file is removed and the folder is synced. Only the
//     daemon's own files are ever removed; a version that would be lost
//     otherwise is kept under a recovery name ([KeptPrefix]);
//   - a file that holds anything else is the owner's and is never
//     overwritten.
//
// Before it judges the file, whatever the file holds, it keeps under a
// recovery name what an upgrade cut short by a crash left at the temporary
// name, and reports it ([ChatResult].Leftover); the result counts the
// recovery names that hold a version ([ChatResult].Kept), on a refusal too.
//
// The lead must be provisioned: its folder and its instruction file are
// provisioning's. EnsureChat makes, opens and judges no reply folder.
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
	lead, err := dirFD(root, p.stateRel(p.LeadDir))
	if err != nil {
		return ChatResult{}, fmt.Errorf("the lead's folder %s: %w", p.LeadDir, err)
	}
	defer unix.Close(lead)
	var res ChatResult
	if res.Leftover, err = keepLeftover(lead); err != nil {
		return ChatResult{}, fmt.Errorf("%s: %w", p.LeadDir, err)
	}
	res.Instructions, err = ensureInstructions(lead, p, opts.Stopped)
	res.Kept = countKept(lead)
	if err != nil {
		return res, err
	}
	return res, nil
}

// keepLeftover keeps under a recovery name whatever is at the temporary name:
// only an upgrade that a crash cut short leaves something there (the text it
// wrote, or the version it exchanged out of the name). It is never deleted.
func keepLeftover(dir int) (bool, error) {
	var st unix.Stat_t
	switch err := unix.Fstatat(dir, instructionsTmp, &st, unix.AT_SYMLINK_NOFOLLOW); {
	case err == unix.ENOENT:
		return false, nil
	case err != nil:
		return false, err
	}
	if _, err := keep(dir, instructionsTmp); err != nil {
		return false, err
	}
	return true, nil
}

// keep renames from, in dir, to the first free recovery name, by a rename
// that replaces nothing, and answers its number. A regular file of the
// caller with one link is set to mode 0600; anything else is kept as it is.
func keep(dir int, from string) (int, error) {
	for n := 1; n <= MaxKept; n++ {
		err := unix.Renameat2(dir, from, dir, keptName(n), unix.RENAME_NOREPLACE)
		switch err {
		case unix.EEXIST, unix.ENOTEMPTY:
			continue
		case nil:
			privateKept(dir, keptName(n))
			if err := syncDirFD(dir); err != nil {
				return n, err
			}
			return n, nil
		}
		return 0, fmt.Errorf("%w: a version cannot be kept beside the instruction file: %v", ErrChat, err)
	}
	return 0, fmt.Errorf("%w: every recovery name %s1 to %s%d is taken; a version is left at %s until the owner deletes some", ErrChat, KeptPrefix, KeptPrefix, MaxKept, from)
}

// privateKept sets the kept file name to mode 0600 when it is a regular file
// of the caller with one link (a mode change of a file with a second link
// would change it at the other name too). It never fails the keep.
func privateKept(dir int, name string) {
	fd, err := openBeneath(dir, name, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOCTTY)
	if err != nil {
		return
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || int(st.Uid) != unix.Geteuid() || st.Nlink != 1 {
		return
	}
	_ = unix.Fchmod(fd, 0o600)
}

// countKept counts the recovery names that hold something.
func countKept(dir int) int {
	n := 0
	for i := 1; i <= MaxKept; i++ {
		var st unix.Stat_t
		if unix.Fstatat(dir, keptName(i), &st, unix.AT_SYMLINK_NOFOLLOW) == nil {
			n++
		}
	}
	return n
}

// dirFD opens the folder rel beneath the root and returns a descriptor the
// caller owns.
func dirFD(root *safefile.Root, rel string) (int, error) {
	h, err := root.OpenDirHandle(rel)
	if err != nil {
		return -1, err
	}
	defer h.Close()
	return h.Dup()
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

// ErrBusy is wrapped when the instruction file cannot be opened for now:
// a nonblocking open met a file lease, or a lookup kept meeting a rename or a
// mount beside it. Nothing was changed; the caller tries again later.
var ErrBusy = errors.New("provision: the lead's instruction file is busy; nothing was changed, try again later")

// openat2 is the system call; a test replaces it to make an open answer
// EAGAIN.
var openat2 = unix.Openat2

// lookupTries bounds the opens of one name that a protected lookup's EAGAIN
// (a rename or a mount beside it) runs again.
const lookupTries = 4

// openBeneath opens the one name below dir by openat2 with [resolveFlags],
// as package leadchat's openAt does. An open interrupted by a signal (EINTR)
// is run again. EAGAIN is a lease on the file when flags hold O_NONBLOCK,
// and is answered at once; otherwise it is a lookup that met a rename or a
// mount, and the open is run again at most [lookupTries] times in all. An
// EAGAIN that is left is [ErrBusy].
func openBeneath(dir int, name string, flags int) (int, error) {
	how := unix.OpenHow{Flags: uint64(flags | unix.O_CLOEXEC | unix.O_NOFOLLOW), Resolve: resolveFlags}
	var fd int
	var err error
	for try := 0; try < lookupTries; try++ {
		for {
			fd, err = openat2(dir, name, &how)
			if err != unix.EINTR {
				break
			}
		}
		if err != unix.EAGAIN || flags&unix.O_NONBLOCK != 0 {
			break
		}
	}
	if err == unix.EAGAIN {
		return -1, ErrBusy
	}
	return fd, err
}

// checkInstructions judges the instruction file by the stat of its handle.
func checkInstructions(st *unix.Stat_t, at string) error {
	switch {
	case st.Mode&unix.S_IFMT != unix.S_IFREG:
		return fmt.Errorf("%w: %s is not a regular file", ErrChat, at)
	case int(st.Uid) != unix.Geteuid(), st.Mode&0o7777 != 0o600, st.Nlink != 1:
		return fmt.Errorf("%w: %s is not a file of the caller with mode 0600 and one link", ErrChat, at)
	}
	return nil
}

// ensureInstructions judges the instruction file and writes the text when it
// is the empty one and the lead is proven stopped.
func ensureInstructions(lead int, p Paths, stopped bool) (Instructions, error) {
	at := p.InstructionFile
	fd, err := openBeneath(lead, InstructionsName, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOCTTY)
	if err != nil {
		switch {
		case err == unix.ENOENT:
			return "", fmt.Errorf("%s: %w", at, fs.ErrNotExist)
		case errors.Is(err, ErrBusy):
			return "", fmt.Errorf("%s: %w", at, err)
		}
		return "", fmt.Errorf("%w: %s cannot be opened as a file beneath the lead's folder: %w", ErrChat, at, err)
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return "", fmt.Errorf("%s: %w", at, err)
	}
	if err := checkInstructions(&st, at); err != nil {
		return "", err
	}
	if st.Size > maxInstructionsBytes {
		return InstructionsOwner, nil
	}
	text := []byte(LeadInstructions())
	if st.Size != 0 {
		same, err := holdsExactly(fd, text, st.Size)
		switch {
		case err != nil:
			return "", fmt.Errorf("%s: %w", at, err)
		case same:
			return InstructionsCurrent, nil
		}
		return InstructionsOwner, nil
	}
	if !stopped {
		return InstructionsLegacy, nil
	}
	if err := replaceEmpty(lead, InstructionsName, fd, &st, text); err != nil {
		return "", fmt.Errorf("%s: %w", at, err)
	}
	return InstructionsWritten, nil
}

// holdsExactly reports whether the file of fd, whose size was size, holds
// exactly text: one byte more than the text is read from its start, the
// bytes read are the text, and the size is the same after the read. A file
// that grew or shrank while it was read is not the text.
func holdsExactly(fd int, text []byte, size int64) (bool, error) {
	if size != int64(len(text)) {
		return false, nil
	}
	buf := make([]byte, len(text)+1)
	got := 0
	for got < len(buf) {
		k, err := unix.Pread(fd, buf[got:], int64(got))
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return false, err
		}
		if k == 0 {
			break
		}
		got += k
	}
	var after unix.Stat_t
	if err := unix.Fstat(fd, &after); err != nil {
		return false, err
	}
	return got == len(text) && bytes.Equal(buf[:got], text) && after.Size == size, nil
}

// errDisplaced is the fixed note of an upgrade that met another version of
// the instruction file before it exchanged anything: it is kept at its name
// and the text is not written.
var errDisplaced = fmt.Errorf("%w: the file changed while its empty form was being replaced; the version found is kept and the text was not written", ErrChat)

// keptNote is the fixed note of an upgrade that met another version of the
// instruction file and kept a version under the recovery name n.
func keptNote(n int) error {
	return fmt.Errorf("%w: the file changed while its empty form was being replaced; the text was not written, and a version is kept beside it as %s, which the daemon never deletes", ErrChat, keptName(n))
}

// exchangeHook and undoHook, a test's hooks, run in replaceEmpty: the first
// after the empty file is checked again and before the exchange, the second
// after the exchange found another version and before it is undone.
var exchangeHook, undoHook func()

// replaceEmpty puts data at name below dir in place of the empty file the
// handle fd holds (seen is its stat). data is written to the temporary file
// of dir (made exclusively, mode 0600) and synced; the empty file is checked
// again, by its handle (still empty, one link) and by its name (the same
// file); then the temporary file and name are exchanged.
//
// Nothing but the daemon's own files is ever deleted: the empty file the
// exchange took out, when it is the one checked and still empty, and the
// text the daemon wrote, recognised by its inode. When the exchange took out
// another version (an owner's save came in the moment before, or the empty
// file was written), the exchange is undone, so that version is at its name
// again, and what the undo took out of the name is the daemon's text, which
// is deleted, or a later save, which is kept under a recovery name
// ([KeptPrefix]); a version that cannot be put back is kept under a recovery
// name too. Either way the call is refused with a fixed note. A failure that
// leaves a version at the temporary name leaves it there, for the next call
// to keep ([keepLeftover]).
func replaceEmpty(dir int, name string, fd int, seen *unix.Stat_t, data []byte) error {
	tmp, err := unix.Openat(dir, instructionsTmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return err
	}
	var own unix.Stat_t
	err = func() error {
		defer unix.Close(tmp)
		if err := unix.Fstat(tmp, &own); err != nil {
			return err
		}
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
		dropOwn(dir, &own)
		return err
	}
	var now, named unix.Stat_t
	if err := unix.Fstat(fd, &now); err != nil {
		dropOwn(dir, &own)
		return err
	}
	if err := unix.Fstatat(dir, name, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		dropOwn(dir, &own)
		return err
	}
	if now.Size != 0 || now.Nlink != 1 || now.Dev != seen.Dev || now.Ino != seen.Ino ||
		named.Dev != seen.Dev || named.Ino != seen.Ino {
		dropOwn(dir, &own)
		return errDisplaced
	}
	if exchangeHook != nil {
		exchangeHook()
	}
	if err := unix.Renameat2(dir, instructionsTmp, dir, name, unix.RENAME_EXCHANGE); err != nil {
		dropOwn(dir, &own)
		return fmt.Errorf("the text cannot be exchanged for the empty file: %w", err)
	}
	// The temporary name now holds what the name held at the exchange.
	var out unix.Stat_t
	if err := unix.Fstatat(dir, instructionsTmp, &out, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return fmt.Errorf("the file exchanged out cannot be examined: %w", err)
	}
	if err := unix.Fstat(fd, &now); err != nil {
		return err
	}
	if out.Dev == seen.Dev && out.Ino == seen.Ino && out.Mode&unix.S_IFMT == unix.S_IFREG && now.Size == 0 {
		// The empty file the daemon checked, still empty: nothing of the
		// owner's is in it.
		if err := unix.Unlinkat(dir, instructionsTmp, 0); err != nil {
			return err
		}
		return syncDirFD(dir)
	}
	// Another version took the name in the moment before the exchange, or
	// the empty file was written: put it back at its name.
	if undoHook != nil {
		undoHook()
	}
	if err := unix.Renameat2(dir, instructionsTmp, dir, name, unix.RENAME_EXCHANGE); err != nil {
		n, kerr := keep(dir, instructionsTmp)
		if kerr != nil {
			return fmt.Errorf("%w; the version found is left at %s, because the exchange could not be undone (%v) and %v", ErrChat, instructionsTmp, err, kerr)
		}
		return keptNote(n)
	}
	// The temporary name now holds what the name held at the undo: the
	// daemon's text, or a save that came after the exchange.
	var back unix.Stat_t
	if err := unix.Fstatat(dir, instructionsTmp, &back, unix.AT_SYMLINK_NOFOLLOW); err == nil &&
		back.Dev == own.Dev && back.Ino == own.Ino {
		dropOwn(dir, &own)
		_ = syncDirFD(dir)
		return errDisplaced
	}
	n, err := keep(dir, instructionsTmp)
	if err != nil {
		return err
	}
	return keptNote(n)
}

// dropOwn deletes the temporary name when it holds the daemon's own text, the
// file own (its stat; a zero stat matches nothing). Anything else there is
// left, for the next call to keep.
func dropOwn(dir int, own *unix.Stat_t) {
	var st unix.Stat_t
	if own.Ino == 0 || unix.Fstatat(dir, instructionsTmp, &st, unix.AT_SYMLINK_NOFOLLOW) != nil ||
		st.Dev != own.Dev || st.Ino != own.Ino {
		return
	}
	_ = unix.Unlinkat(dir, instructionsTmp, 0)
}
