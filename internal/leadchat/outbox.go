//go:build linux && ataniq_canary

package leadchat

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"ataniq/internal/approval/ownerwire"
)

// The bounds and times of the outbox.
const (
	// OutboxName is the outbox folder in the lead's home: on the host
	// <state root>/leads/<layout role>/home/.ataniq-replies, and inside the
	// sandbox /home/lead/.ataniq-replies.
	OutboxName = ".ataniq-replies"
	// MaxDirEntries is the most entries one scan reads from the outbox: an
	// outbox that holds more suspends the lead's chat.
	MaxDirEntries = 256
	// MaxWaiting is the most replies and folders an outbox may hold: one
	// that holds more suspends the lead's chat. Folders are never removed,
	// so each counts for good.
	MaxWaiting = 32
	// SettleAge is how old the kernel's change time of a reply must be
	// before it is claimed. A lead cannot set the change time, and a rename
	// sets it.
	SettleAge = 2 * time.Second
	// StaleAge is how old a file whose name starts with a dot may grow
	// before it is removed.
	StaleAge = 10 * time.Minute
	// maxClearEntries is the most entries a clear reads; an outbox with
	// more is not cleared and the switch stays off.
	maxClearEntries = 8192
	// clearRounds is how many listings a clear makes at most before it
	// gives up on an outbox that does not stay empty.
	clearRounds = 8
	// maxTreeDepth is how deep a claimed folder is removed at most.
	maxTreeDepth = 16
)

// outboxPath are the names from the state root to the home of a lead; the
// outbox is [OutboxName] in it.
func outboxPath(role string) []string {
	return []string{"leads", role, "home", OutboxName}
}

// claimsName is the name of a lead's claim folder in the chat folder.
func claimsName(role string) string { return role + ".claims" }

// The kinds of claim: a reply is read and judged, a discard is removed.
const (
	claimReply   = 'r'
	claimDiscard = 'x'
)

func claimName(kind byte, id int64) string { return string(kind) + strconv.FormatInt(id, 10) }

// parseClaim reads a claim name: its kind and its id.
func parseClaim(name string) (kind byte, id int64, ok bool) {
	if len(name) < 2 || (name[0] != claimReply && name[0] != claimDiscard) || name[1] == '0' {
		return 0, 0, false
	}
	id, err := strconv.ParseInt(name[1:], 10, 64)
	if err != nil || id < 1 || id > ownerwire.MaxChatSeq || claimName(name[0], id) != name {
		return 0, 0, false
	}
	return name[0], id, true
}

// outboxID is the identity of the outbox folder a turn-on verified: its
// device and inode.
type outboxID struct{ dev, ino uint64 }

func (id outboxID) String() string { return fmt.Sprintf("%d:%d", id.dev, id.ino) }

// parseOutboxID reads the form String writes.
func parseOutboxID(s string) (outboxID, bool) {
	a, b, ok := strings.Cut(s, ":")
	if !ok {
		return outboxID{}, false
	}
	dev, err1 := strconv.ParseUint(a, 10, 64)
	ino, err2 := strconv.ParseUint(b, 10, 64)
	id := outboxID{dev, ino}
	if err1 != nil || err2 != nil || id.String() != s {
		return outboxID{}, false
	}
	return id, true
}

func idOf(st *unix.Stat_t) outboxID { return outboxID{uint64(st.Dev), st.Ino} }

// openHome walks to the lead's home folder: every folder of the caller, not
// a link, not across a mount point, not writable by group or other.
func (s *Store) openHome() (int, error) {
	return s.root.walk(outboxPath(s.role)[:3]...)
}

// openOutbox opens the lead's outbox for a scan: the home as [Store.openHome]
// reaches it, and the outbox in it a folder of the caller with mode 0700
// exactly. A missing step answers an error that wraps ENOENT.
func (s *Store) openOutbox() (int, outboxID, error) {
	home, err := s.openHome()
	if err != nil {
		return -1, outboxID{}, err
	}
	defer closeFD(home)
	fd, err := openDirAt(s.root.openat2, home, OutboxName, true)
	if err != nil {
		return -1, outboxID{}, err
	}
	st, err := fstatFD(fd)
	if err != nil {
		closeFD(fd)
		return -1, outboxID{}, fmt.Errorf("leadchat: cannot examine the outbox: %w", err)
	}
	return fd, idOf(&st), nil
}

// ensureOutbox opens the lead's outbox for a turn-on, making it with mode
// 0700 when it is not there. The home must be there.
func (s *Store) ensureOutbox() (int, outboxID, error) {
	home, err := s.openHome()
	switch {
	case errors.Is(err, unix.ENOENT):
		return -1, outboxID{}, fmt.Errorf("%w: the lead's home folder is not there", ErrUnavailable)
	case errors.Is(err, ErrInsecure), errors.Is(err, ErrUnavailable):
		return -1, outboxID{}, fmt.Errorf("the lead's home: %w", err)
	case err != nil:
		return -1, outboxID{}, fmt.Errorf("leadchat: cannot open the lead's home: %w", err)
	}
	defer closeFD(home)
	fd, err := privateDirAt(s.root.openat2, home, OutboxName)
	if err != nil {
		return -1, outboxID{}, fmt.Errorf("the outbox: %w", err)
	}
	st, err := fstatFD(fd)
	if err != nil {
		closeFD(fd)
		return -1, outboxID{}, fmt.Errorf("leadchat: cannot examine the outbox: %w", err)
	}
	return fd, idOf(&st), nil
}

// claim moves the entry name of the outbox into the claim folder, under a
// claim name of kind with the next claim id, by one rename that replaces
// nothing. From here on the entry is the daemon's: the lead's name is not
// used again. gone reports a name that was not there.
func (s *Store) claim(outFD int, name string, kind byte) (id int64, gone bool, err error) {
	if s.claimNext > ownerwire.MaxChatSeq {
		return 0, false, fmt.Errorf("%w: the claim ids are spent", ErrFull)
	}
	id = s.claimNext
	s.claimNext++
	if s.beforeClaim != nil {
		s.beforeClaim(name)
	}
	switch err := renameNoReplace(outFD, name, s.claimsFD, claimName(kind, id)); {
	case err == unix.ENOENT:
		return 0, true, nil
	case err != nil:
		return 0, false, err
	}
	return id, false, nil
}

// removeClaim removes an entry of the claim folder, a claimed folder with
// what it holds. A name that is gone is no error.
func (s *Store) removeClaim(name string) error {
	err := s.unlink(s.claimsFD, name, 0)
	if err == unix.EISDIR {
		left := maxClearEntries
		err = removeTree(s.root.openat2, s.unlink, s.claimsFD, name, &left, 0)
	}
	if err == nil || err == unix.ENOENT {
		return nil
	}
	if errors.Is(err, ErrFull) {
		return err
	}
	return fmt.Errorf("leadchat: cannot remove a claimed entry: %w", err)
}

// discard claims the entry name of the outbox and removes it, and counts it
// by why.
func (s *Store) discard(outFD int, name string, why Reason, dropped map[Reason]int) error {
	id, gone, err := s.claim(outFD, name, claimDiscard)
	if err != nil || gone {
		return err
	}
	if err := s.removeClaim(claimName(claimDiscard, id)); err != nil {
		return err
	}
	dropped[why]++
	return nil
}

// outcome is what became of a claimed reply.
type outcome int

const (
	outGone    outcome = iota // the claim was not there
	outPending                // not settled, or changed while read: left for a later scan
	outTaken                  // stored as an entry, the lead's or withheld
	outDropped                // removed without an entry
)

// resolve reads the claimed reply id by its handle and decides it. seen is
// the outbox's stat of the entry before the claim, or nil for a claim an
// earlier scan left: a claim that is the entry seen (the same inode, size
// and modification time) settled before it was claimed; any other claim is
// read only when its own change time is [SettleAge] old.
//
// A claim that is not a regular file (by fstatat in the claim folder, where
// only the daemon renames) is removed and never opened; a regular file is
// opened with O_NOFOLLOW and O_NONBLOCK and judged on the handle (the
// caller's, one link, no write bit for group or other, at most
// [MaxReplyBytes]), read, and judged again: one that changed while it was
// read, or that a lease holds, is left for a later scan. A reply is stored
// with its claim id in a synced append before its claim is removed.
func (s *Store) resolve(id int64, seen *unix.Stat_t, now time.Time, epoch int64, rep *IngestReport) (outcome, error) {
	name := claimName(claimReply, id)
	st, err := lstatAt(s.claimsFD, name)
	if err == unix.ENOENT {
		return outGone, nil
	}
	if err != nil {
		return 0, fmt.Errorf("leadchat: cannot examine a claimed reply: %w", err)
	}
	drop := func(why Reason) (outcome, error) {
		if err := s.removeClaim(name); err != nil {
			return 0, err
		}
		rep.Dropped[why]++
		return outDropped, nil
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		return drop(ReasonRefused)
	}
	fd, err := openAt(s.root.openat2, s.claimsFD, name, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOCTTY, 0)
	switch {
	case err == unix.EAGAIN:
		return outPending, nil // a lease holds it; the open asked the holder to let go
	case err != nil:
		return 0, fmt.Errorf("leadchat: cannot open a claimed reply: %w", err)
	}
	defer closeFD(fd)
	fst, err := fstatFD(fd)
	if err != nil {
		return 0, fmt.Errorf("leadchat: cannot examine a claimed reply: %w", err)
	}
	switch {
	case fst.Dev != st.Dev || fst.Ino != st.Ino:
		// Only the daemon renames in the claim folder.
		return 0, errors.New("leadchat: a claimed reply changed under the daemon")
	case fst.Mode&unix.S_IFMT != unix.S_IFREG, int(fst.Uid) != unix.Geteuid(), fst.Nlink != 1, fst.Mode&0o022 != 0:
		return drop(ReasonRefused)
	case fst.Size > MaxReplyBytes:
		return drop(ReasonTooLong)
	}
	settled := seen != nil && fst.Dev == seen.Dev && fst.Ino == seen.Ino && fst.Size == seen.Size && fst.Mtim == seen.Mtim
	if !settled && now.Sub(ctimeOf(&fst)) < SettleAge {
		return outPending, nil
	}
	buf := make([]byte, MaxReplyBytes+1)
	n := 0
	for n < len(buf) {
		k, err := unix.Read(fd, buf[n:])
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return 0, fmt.Errorf("leadchat: cannot read a claimed reply: %w", err)
		}
		if k == 0 {
			break
		}
		n += k
	}
	if s.afterRead != nil {
		s.afterRead(name)
	}
	again, err := fstatFD(fd)
	if err != nil || !sameFile(&fst, &again) || int64(n) != fst.Size {
		return outPending, nil // written while read, by a writer that held it open
	}
	text, v := ReplyText(buf[:n])
	e := Entry{claim: id}
	switch v {
	case VerdictTooLong:
		return drop(ReasonTooLong)
	case VerdictNotPlain:
		return drop(ReasonNotPlain)
	case VerdictWithheld:
		e.Who, e.Text = ownerwire.ChatWhoSystem, TextWithheld
	default:
		e.Who, e.Text = ownerwire.ChatWhoLead, text
	}
	if err := s.addEntries(now, epoch, e); err != nil {
		return 0, err
	}
	s.claimHigh = id
	s.taken = append(s.taken, now)
	if v == VerdictWithheld {
		rep.Withheld++
	} else {
		rep.Taken++
	}
	if s.afterAppend != nil {
		if err := s.afterAppend(); err != nil {
			return 0, err
		}
	}
	if s.beforeDelete != nil {
		s.beforeDelete(id)
	}
	if err := s.removeClaim(name); err != nil {
		return 0, err
	}
	return outTaken, nil
}

// holdsClaim reports whether an entry held now was stored from claim id.
func (s *Store) holdsClaim(id int64) bool {
	for i := len(s.entries) - 1; i >= 0; i-- {
		if s.entries[i].claim == id {
			return true
		}
	}
	return false
}

// scan is one pass over the claim folder and the outbox, with the outbox's
// descriptor; it fills rep, and answers a suspension text when the lead's
// chat must stop ("" otherwise).
func (s *Store) scan(outFD int, now time.Time, epoch int64, rep *IngestReport) (string, error) {
	budget := s.budget(now)

	// The claims an earlier scan left: a discard, or a reply whose entry is
	// stored (its id at most the high-water), is removed; any other reply is
	// read now, in id order, before anything new is claimed.
	names, more, err := readNames(s.claimsFD, MaxDirEntries)
	if err != nil {
		return "", err
	}
	if more {
		return TextStoppedFull, nil
	}
	var left []int64
	for _, name := range names {
		kind, id, ok := parseClaim(name)
		switch {
		case !ok || kind == claimDiscard:
			if err := s.removeClaim(name); err != nil {
				return "", err
			}
		case id <= s.claimHigh:
			if err := s.removeClaim(name); err != nil {
				return "", err
			}
			if s.holdsClaim(id) {
				rep.Repaired++
			}
		default:
			left = append(left, id)
		}
	}
	slices.Sort(left)
	blocked := false
	for _, id := range left {
		if blocked || budget == 0 {
			rep.Waiting++
			blocked = true
			continue
		}
		out, err := s.resolve(id, nil, now, epoch, rep)
		if err != nil {
			return "", err
		}
		switch out {
		case outPending:
			rep.Waiting++
			blocked = true
		case outTaken:
			budget--
		}
	}

	// The outbox: every name is judged first, without a change; an outbox
	// over its bounds is left as it is and the chat stops.
	names, more, err = readNames(outFD, MaxDirEntries)
	if err != nil {
		return "", err
	}
	if more {
		return TextStoppedFull, nil
	}
	type entry struct {
		name string
		st   unix.Stat_t
		why  Reason
	}
	var cands, junk []entry
	folders := 0
	for _, name := range sortedCopy(names) {
		st, err := lstatAt(outFD, name)
		if err != nil {
			continue // gone since the listing, or not to be examined: left
		}
		switch {
		case st.Mode&unix.S_IFMT == unix.S_IFDIR:
			folders++
		case ctimeOf(&st).Before(s.since):
			junk = append(junk, entry{name, st, ReasonBefore})
		case strings.HasPrefix(name, "."):
			if now.Sub(ctimeOf(&st)) > StaleAge {
				junk = append(junk, entry{name, st, ReasonStale})
			}
		case !ValidReplyName(name):
			junk = append(junk, entry{name, st, ReasonName})
		default:
			cands = append(cands, entry{name: name, st: st})
		}
	}
	rep.Dropped[ReasonFolder] += folders
	if folders+len(cands)+rep.Waiting > MaxWaiting {
		return TextStoppedFull, nil
	}
	for _, j := range junk {
		if err := s.discard(outFD, j.name, j.why, rep.Dropped); err != nil {
			return "", err
		}
	}
	for _, c := range cands {
		if blocked || budget == 0 || now.Sub(ctimeOf(&c.st)) < SettleAge {
			rep.Waiting++
			continue
		}
		id, gone, err := s.claim(outFD, c.name, claimReply)
		if err != nil {
			return "", err
		}
		if gone {
			continue
		}
		if s.afterClaim != nil {
			if err := s.afterClaim(id); err != nil {
				return "", err
			}
		}
		out, err := s.resolve(id, &c.st, now, epoch, rep)
		if err != nil {
			return "", err
		}
		switch out {
		case outPending:
			rep.Waiting++
			blocked = true
		case outTaken:
			budget--
		}
	}
	rep.Waiting += folders
	return "", nil
}

// clearOutbox empties the lead's outbox for a turn-on: it makes the outbox
// when it is not there, removes every claim an earlier segment left, then
// claims and removes every entry of the outbox that is not a folder, listing
// it again until a listing finds none. It answers how many it removed, the
// outbox's identity and the moment the outbox was found empty, by the
// kernel's coarse clock (the clock of the change times).
func (s *Store) clearOutbox() (n int, id outboxID, since time.Time, err error) {
	fd, id, err := s.ensureOutbox()
	if err != nil {
		return 0, outboxID{}, time.Time{}, err
	}
	defer closeFD(fd)
	claims, more, err := readNames(s.claimsFD, maxClearEntries)
	if err != nil {
		return 0, outboxID{}, time.Time{}, err
	}
	if more {
		return 0, outboxID{}, time.Time{}, fmt.Errorf("%w: the claim folder holds more than %d entries", ErrFull, maxClearEntries)
	}
	for _, name := range claims {
		if err := s.removeClaim(name); err != nil {
			return 0, outboxID{}, time.Time{}, err
		}
	}
	for round := 0; round < clearRounds; round++ {
		names, more, err := readNames(fd, maxClearEntries)
		if err != nil {
			return n, outboxID{}, time.Time{}, err
		}
		if more {
			return n, outboxID{}, time.Time{}, fmt.Errorf("%w: the outbox holds more than %d entries", ErrFull, maxClearEntries)
		}
		removed, folders := 0, 0
		for _, name := range names {
			st, err := lstatAt(fd, name)
			if err == unix.ENOENT {
				continue
			}
			if err != nil {
				return n, outboxID{}, time.Time{}, fmt.Errorf("leadchat: cannot examine an entry of the outbox: %w", err)
			}
			if st.Mode&unix.S_IFMT == unix.S_IFDIR {
				folders++
				continue
			}
			cid, gone, err := s.claim(fd, name, claimDiscard)
			if err != nil {
				return n, outboxID{}, time.Time{}, err
			}
			if gone {
				continue
			}
			if err := s.removeClaim(claimName(claimDiscard, cid)); err != nil {
				return n, outboxID{}, time.Time{}, err
			}
			n++
			removed++
		}
		if removed == 0 {
			if folders > MaxWaiting {
				return n, outboxID{}, time.Time{}, fmt.Errorf("%w: the outbox holds more than %d folders", ErrFull, MaxWaiting)
			}
			return n, id, s.kernelNow(), nil
		}
	}
	return n, outboxID{}, time.Time{}, fmt.Errorf("%w: the outbox did not stay empty", ErrFull)
}

// coarseNow is the kernel's coarse real-time clock: the clock the kernel
// stamps change times with, so that a file changed after it has a change
// time no earlier than it.
func coarseNow() time.Time {
	var ts unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_REALTIME_COARSE, &ts); err != nil {
		return time.Now()
	}
	return time.Unix(ts.Unix())
}
