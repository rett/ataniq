//go:build linux && ataniq_canary

package provision

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"

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
// into the sandbox, so only the daemon writes in it.
const instructionsTmp = "." + InstructionsName + ".tmp"

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
//     Otherwise the empty file is removed and the folder is synced;
//   - a file that holds anything else is the owner's and is never
//     overwritten.
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
	var res ChatResult
	if res.Instructions, err = ensureInstructions(root, p, opts.Stopped); err != nil {
		return ChatResult{}, err
	}
	return res, nil
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
func ensureInstructions(root *safefile.Root, p Paths, stopped bool) (Instructions, error) {
	lead, err := dirFD(root, p.stateRel(p.LeadDir))
	if err != nil {
		return "", fmt.Errorf("the lead's folder %s: %w", p.LeadDir, err)
	}
	defer unix.Close(lead)
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
// the instruction file: it is kept at its name and the text is not written.
var errDisplaced = fmt.Errorf("%w: the file changed while its empty form was being replaced; the version found is kept and the text was not written", ErrChat)

// exchangeHook, a test's hook, runs in replaceEmpty after the empty file is
// checked again and before the exchange.
var exchangeHook func()

// replaceEmpty puts data at name below dir in place of the empty file the
// handle fd holds (seen is its stat). data is written to a temporary file of
// dir (made exclusively, mode 0600) and synced; the empty file is checked
// again, by its handle (still empty, one link) and by its name (the same
// file); then the temporary file and name are exchanged. The file the
// exchange took out must be the empty file checked: when it is not, the
// exchange is undone and the call is refused ([errDisplaced]), and the file
// found is at its name again. A temporary file a crash left is removed first.
func replaceEmpty(dir int, name string, fd int, seen *unix.Stat_t, data []byte) error {
	if err := unix.Unlinkat(dir, instructionsTmp, 0); err != nil && err != unix.ENOENT {
		return fmt.Errorf("a leftover temporary file cannot be removed: %w", err)
	}
	tmp, err := unix.Openat(dir, instructionsTmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return err
	}
	// keepTmp is set when the temporary name holds a file that is not the
	// daemon's text (an exchange that could not be undone).
	keepTmp := false
	defer func() {
		if !keepTmp {
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
	var now, named unix.Stat_t
	if err := unix.Fstat(fd, &now); err != nil {
		return err
	}
	if err := unix.Fstatat(dir, name, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if now.Size != 0 || now.Nlink != 1 || now.Dev != seen.Dev || now.Ino != seen.Ino ||
		named.Dev != seen.Dev || named.Ino != seen.Ino {
		return errDisplaced
	}
	if exchangeHook != nil {
		exchangeHook()
	}
	if err := unix.Renameat2(dir, instructionsTmp, dir, name, unix.RENAME_EXCHANGE); err != nil {
		return fmt.Errorf("the text cannot be exchanged for the empty file: %w", err)
	}
	// The temporary name now holds what the name held at the exchange.
	var out unix.Stat_t
	if err := unix.Fstatat(dir, instructionsTmp, &out, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		keepTmp = true
		return fmt.Errorf("the file exchanged out cannot be examined: %w", err)
	}
	if err := unix.Fstat(fd, &now); err != nil {
		keepTmp = true
		return err
	}
	if out.Dev != seen.Dev || out.Ino != seen.Ino || out.Mode&unix.S_IFMT != unix.S_IFREG || now.Size != 0 {
		// Another version took the name in the moment before the
		// exchange, or the empty file was written: put it back.
		if err := unix.Renameat2(dir, instructionsTmp, dir, name, unix.RENAME_EXCHANGE); err != nil {
			keepTmp = true
			return fmt.Errorf("%w; the version found is kept as %s, because the exchange could not be undone: %v", ErrChat, instructionsTmp, err)
		}
		return errDisplaced
	}
	if err := unix.Unlinkat(dir, instructionsTmp, 0); err != nil {
		return err
	}
	return syncDirFD(dir)
}
