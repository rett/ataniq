//go:build linux && ataniq_canary

package leadchat

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"golang.org/x/sys/unix"

	"ataniq/internal/approval/ownerwire"
)

// The bounds and times of the outbox.
const (
	// OutboxName is the outbox folder in the lead's home: on the host
	// <state root>/leads/<layout role>/home/.ataniq-replies, and inside the
	// sandbox /home/lead/.ataniq-replies. Every turn-on makes it afresh.
	OutboxName = ".ataniq-replies"
	// QuarantineName is the folder, in <state root>/leads/<layout role>,
	// that a turn-on sets an earlier outbox aside in, and a scan a claim
	// that is not a regular file: mode 0700, made by the daemon, bound into
	// no sandbox, on the file system of the lead's home.
	QuarantineName = "quarantine"
	// MaxQuarantine is how many entries the quarantine holds at most, named
	// 1 to 4: a turn-on that would set aside one more is refused
	// ([ErrQuarantined]) until the owner purges it.
	MaxQuarantine = 4
	// StagingName is the folder, in <state root>/leads/<layout role>, that
	// a turn-on makes the new outbox in before it publishes it at the
	// outbox's name: mode 0700, made by the daemon, bound into no sandbox,
	// on the file system of the lead's home.
	StagingName = "staging"
	// stagedName is the new outbox's name in the staging folder.
	stagedName = "outbox"
	// MaxWaiting is the most entries an outbox may hold, whatever they are,
	// with the replies claimed and not yet read: a scan that finds more
	// suspends the lead's chat before it touches anything.
	MaxWaiting = 32
	// SettleAge is how old the kernel's change time of a reply must be
	// before it is claimed. A lead cannot set the change time, and a rename
	// sets it.
	SettleAge = 2 * time.Second
	// UnsettledTries and UnsettledAge bound a claimed reply that cannot be
	// read: one that a lease held, that changed while it was read or whose
	// read failed UnsettledTries times, or that is still unread
	// UnsettledAge after a scan first left it, is dropped
	// ([ReasonUnsettled]).
	UnsettledTries = 3
	UnsettledAge   = 30 * time.Second
	// probeName is the file a turn-on makes in the fresh outbox and claims,
	// to prove it can claim; probeClaim is its name in the claim folder.
	probeName  = ".ataniq-probe"
	probeClaim = "probe"
	// maxClaimEntries is the most entries a turn-on reads from the claim
	// folder an earlier segment left.
	maxClaimEntries = 256
	// maxPurgeEntries and maxTreeDepth bound one purge of the quarantine.
	maxPurgeEntries = 8192
	maxTreeDepth    = 16
)

// outboxPath are the names from the state root to the outbox of a lead.
func outboxPath(role string) []string {
	return []string{"leads", role, "home", OutboxName}
}

// claimsName is the name of a lead's claim folder in the chat folder.
func claimsName(role string) string { return role + ".claims" }

// claimName is the name of a claimed reply in the claim folder.
func claimName(id int64) string { return "r" + strconv.FormatInt(id, 10) }

// grant is what a turn-on verified and holds until the segment ends: the
// fresh outbox's descriptor and identity, and the quarantine's descriptor.
// While the descriptor is held the folder's inode cannot be given to another
// folder, so a folder found at the outbox's name with the same device and
// inode is this one.
type grant struct {
	out      int
	dev, ino uint64
	quar     int
}

func (g *grant) close() {
	if g.out >= 0 {
		closeFD(g.out)
	}
	closeFD(g.quar)
}

// claimState is what the store knows of a reply it claimed in this segment.
type claimState struct {
	seen      unix.Stat_t // the outbox's stat of the entry before the claim
	first     time.Time   // when a scan first left it unread, or zero
	tries     int         // how many reads failed
	committed bool        // its entry is stored; only its removal is left
}

// openGrant makes the lead's outbox afresh for a turn-on and verifies it.
// It answers the grant and how many things it set aside: the claims an
// earlier segment left, what an earlier turn-on left in the staging folder,
// and the earlier outbox.
//
// The grant is bound to a folder the daemon made itself, never to one it
// found at the outbox's name. In order: the lead's folder and home are walked
// to (no link, no mount point, of the daemon's user, not writable by group or
// other); the quarantine and the staging folder ([StagingName], beside the
// quarantine, bound into no sandbox) are made (0700) or verified; every claim
// an earlier segment left is removed, or set aside when it is a folder, and
// whatever the staging folder holds is set aside; the new outbox is made in
// the staging folder with mode 0700, opened there, judged by its descriptor
// (a folder of the daemon's user, empty, with one link from its parent) and
// set to mode 0700; a probe file made in it is claimed and removed, to prove
// the file system renames without replacing; the earlier outbox, whatever it
// is (an empty folder included), is set aside (renamed, never read or
// walked); and the held folder is published at the outbox's name by a rename
// that replaces nothing. The name must then lead to the held folder (the same
// device and inode). A name taken in the moment between, and a name that
// leads elsewhere after the rename, refuse the turn-on ([ErrUnavailable]),
// and what the outbox's name or the staging folder holds is set aside.
// Anything that fails closes what it opened.
func (s *Store) openGrant() (*grant, int, error) {
	call := s.root.openat2
	lead, err := s.root.walk("leads", s.role)
	if err != nil {
		return nil, 0, homeErr(err)
	}
	defer closeFD(lead)
	home, err := openDirAt(call, lead, "home", false)
	if err != nil {
		return nil, 0, homeErr(err)
	}
	defer closeFD(home)
	quar, err := privateDirAt(call, lead, QuarantineName)
	if err != nil {
		return nil, 0, fmt.Errorf("the quarantine: %w", err)
	}
	g := &grant{out: -1, quar: quar}
	fail := func(err error) (*grant, int, error) {
		g.close()
		return nil, 0, err
	}
	stage, err := privateDirAt(call, lead, StagingName)
	if err != nil {
		return fail(fmt.Errorf("the staging folder: %w", err))
	}
	defer closeFD(stage)
	n, err := s.discardClaims(quar)
	if err != nil {
		return fail(err)
	}
	m, err := s.clearStaging(stage, quar)
	if err != nil {
		return fail(err)
	}
	n += m
	// unstage sets the staged folder aside when the turn-on is refused
	// after it was made; a later turn-on sets aside what it cannot.
	unstage := func(err error) (*grant, int, error) {
		_ = s.quarantine(stage, stagedName, quar)
		return fail(err)
	}
	if err := unix.Mkdirat(stage, stagedName, 0o700); err != nil {
		return fail(fmt.Errorf("leadchat: cannot make the outbox: %w", err))
	}
	if s.afterStage != nil {
		s.afterStage()
	}
	out, err := openAt(call, stage, stagedName, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	switch {
	case err == unix.ELOOP || err == unix.ENOTDIR || err == unix.EXDEV || err == unix.ENOENT:
		return unstage(fmt.Errorf("%w: the outbox the daemon made was replaced before it was opened", ErrUnavailable))
	case err != nil:
		return unstage(fmt.Errorf("leadchat: cannot open the outbox: %w", err))
	}
	g.out = out
	if err := checkFresh(out); err != nil {
		return unstage(err)
	}
	// The umask may have cleared bits; the outbox is 0700 exactly. The
	// mode is set on the descriptor of the folder this turn-on made.
	if err := unix.Fchmod(out, 0o700); err != nil {
		return unstage(fmt.Errorf("leadchat: cannot set the mode of the outbox: %w", err))
	}
	if err := checkDirFD(out, true); err != nil {
		return unstage(fmt.Errorf("the outbox: %w", err))
	}
	st, err := fstatFD(out)
	if err != nil {
		return unstage(fmt.Errorf("leadchat: cannot examine the outbox: %w", err))
	}
	g.dev, g.ino = uint64(st.Dev), st.Ino
	if err := s.probe(out); err != nil {
		return unstage(err)
	}
	switch err := s.quarantine(home, OutboxName, quar); {
	case err == nil:
		n++
	case err == unix.ENOENT:
	case errors.Is(err, ErrQuarantined), errors.Is(err, ErrUnavailable):
		return unstage(err)
	default:
		return unstage(fmt.Errorf("leadchat: cannot set the earlier outbox aside: %w", err))
	}
	if s.beforePublish != nil {
		s.beforePublish()
	}
	switch err := renameNoReplace(s.rename, stage, stagedName, home, OutboxName); {
	case err == unix.EEXIST || err == unix.ENOTEMPTY:
		return unstage(fmt.Errorf("%w: the reply folder was made by another while chat was turned on", ErrUnavailable))
	case err == unix.ENOENT:
		return unstage(fmt.Errorf("%w: the outbox the daemon made was moved before it was published", ErrUnavailable))
	case err != nil:
		return unstage(err)
	}
	if s.afterPublish != nil {
		s.afterPublish()
	}
	if same, err := s.published(home, g); err != nil || !same {
		// What the name leads to is not the held folder: it is set aside,
		// and the turn-on is refused.
		_ = s.quarantine(home, OutboxName, quar)
		if err == nil {
			err = fmt.Errorf("%w: the reply folder was replaced while chat was turned on", ErrUnavailable)
		}
		return fail(err)
	}
	return g, n, nil
}

// checkFresh judges the folder a turn-on made in the staging folder, by its
// descriptor: a folder of the daemon's user that holds nothing and has one
// link from its parent. A folder put in its place before it was opened holds
// something or was made by another, and is refused ([ErrUnavailable]).
func checkFresh(fd int) error {
	st, err := fstatFD(fd)
	if err != nil {
		return fmt.Errorf("leadchat: cannot examine the outbox: %w", err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR || int(st.Uid) != unix.Geteuid() || st.Nlink != 2 {
		return fmt.Errorf("%w: the outbox the daemon made was replaced before it was opened", ErrUnavailable)
	}
	names, more, err := readNames(fd, 0)
	if err != nil {
		return err
	}
	if more || len(names) != 0 {
		return fmt.Errorf("%w: the outbox the daemon made was replaced before it was opened", ErrUnavailable)
	}
	return nil
}

// published reports whether the outbox's name, right after the turn-on's
// rename, leads to the held folder: the same device and inode, of the
// daemon's user with mode 0700, reached without a link or a mount point.
func (s *Store) published(home int, g *grant) (bool, error) {
	fd, err := openDirAt(s.root.openat2, home, OutboxName, true)
	switch {
	case errors.Is(err, unix.ENOENT), errors.Is(err, unix.EACCES), errors.Is(err, ErrInsecure):
		return false, nil
	case err != nil:
		return false, err
	}
	defer closeFD(fd)
	st, err := fstatFD(fd)
	if err != nil {
		return false, fmt.Errorf("leadchat: cannot examine the outbox: %w", err)
	}
	return uint64(st.Dev) == g.dev && st.Ino == g.ino, nil
}

// clearStaging sets aside, unread, whatever the staging folder holds: a
// folder an earlier turn-on made and did not publish (the daemon stopped in
// the moment between). It answers how many it set aside.
func (s *Store) clearStaging(stage, quar int) (int, error) {
	names, more, err := readNames(stage, MaxQuarantine)
	if err != nil {
		return 0, err
	}
	if more {
		return 0, ErrQuarantined
	}
	n := 0
	for _, name := range names {
		switch err := s.quarantine(stage, name, quar); {
		case err == nil:
			n++
		case err == unix.ENOENT:
		case errors.Is(err, ErrQuarantined), errors.Is(err, ErrUnavailable):
			return 0, err
		default:
			return 0, fmt.Errorf("leadchat: cannot set aside what the staging folder holds: %w", err)
		}
	}
	return n, nil
}

// homeErr classifies an error of the walk to the lead's home.
func homeErr(err error) error {
	switch {
	case errors.Is(err, unix.ENOENT):
		return fmt.Errorf("%w: the lead's home folder is not there", ErrUnavailable)
	case errors.Is(err, ErrInsecure), errors.Is(err, ErrUnavailable), errors.Is(err, ErrClosed):
		return fmt.Errorf("the lead's home: %w", err)
	}
	return fmt.Errorf("leadchat: cannot open the lead's home: %w", err)
}

// discardClaims removes every entry of the claim folder, which only an
// earlier segment of this store or an earlier run left: a file of any kind
// is unlinked (never opened), and a folder, which unlink refuses, is set
// aside in the quarantine unread. It answers how many it removed or set
// aside.
func (s *Store) discardClaims(quar int) (int, error) {
	names, more, err := readNames(s.claimsFD, maxClaimEntries)
	if err != nil {
		return 0, err
	}
	if more {
		return 0, fmt.Errorf("%w: the claim folder holds more than %d entries", ErrFull, maxClaimEntries)
	}
	n := 0
	for _, name := range names {
		err := s.unlink(s.claimsFD, name, 0)
		if err == unix.EISDIR {
			err = s.quarantine(s.claimsFD, name, quar)
		}
		switch {
		case err == nil:
			n++
		case err == unix.ENOENT:
		case errors.Is(err, ErrQuarantined), errors.Is(err, ErrUnavailable):
			return 0, err
		default:
			return 0, fmt.Errorf("leadchat: cannot remove a claim an earlier segment left: %w", err)
		}
	}
	return n, nil
}

// quarantine renames the entry name below dir into the quarantine, under the
// first of the names 1 to [MaxQuarantine] that is free, by a rename that
// replaces nothing; nothing in it is opened or walked. When every name is
// taken, a name that holds an empty folder (an outbox that held nothing) is
// freed by removing that folder, which removes nothing else; a quarantine
// with no name free then answers [ErrQuarantined], and an entry that is gone
// ENOENT.
func (s *Store) quarantine(dir int, name string, quar int) error {
	for pass := 0; pass < 2; pass++ {
		for slot := 1; slot <= MaxQuarantine; slot++ {
			err := renameNoReplace(s.rename, dir, name, quar, strconv.Itoa(slot))
			if err != unix.EEXIST && err != unix.ENOTEMPTY {
				return err
			}
		}
		if pass == 0 && !s.freeSlot(quar) {
			break
		}
	}
	return ErrQuarantined
}

// freeSlot removes the first entry of the quarantine's names that is an
// empty folder, and reports whether it removed one. The removal of a folder
// fails when it holds anything, so nothing set aside is lost.
func (s *Store) freeSlot(quar int) bool {
	for slot := 1; slot <= MaxQuarantine; slot++ {
		if s.unlink(quar, strconv.Itoa(slot), unix.AT_REMOVEDIR) == nil {
			return true
		}
	}
	return false
}

// probe makes a file in the fresh outbox and claims and removes it as a
// reply is claimed and removed, so that a file system or a filter that
// cannot rename without replacing refuses the turn-on rather than the first
// reply. Every failure is [ErrUnavailable].
func (s *Store) probe(out int) error {
	fd, err := openRetry(func() (int, error) {
		return unix.Openat(out, probeName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	})
	if err != nil {
		return fmt.Errorf("%w: the probe cannot be made in the outbox: %v", ErrUnavailable, err)
	}
	closeFD(fd)
	if err := renameNoReplace(s.rename, out, probeName, s.claimsFD, probeClaim); err != nil {
		_ = s.unlink(out, probeName, 0)
		if errors.Is(err, ErrUnavailable) {
			return err
		}
		return fmt.Errorf("%w: the probe cannot be claimed: %v", ErrUnavailable, err)
	}
	if err := s.unlink(s.claimsFD, probeClaim, 0); err != nil {
		return fmt.Errorf("%w: the claimed probe cannot be removed: %v", ErrUnavailable, err)
	}
	return nil
}

// checkOutbox reports whether the outbox's name still leads to the folder
// the grant holds: the walk from the root reaches it without a link or a
// mount point, it is of the daemon's user with mode 0700 exactly, and its
// device and inode are the held folder's. A missing step, a folder closed to
// the daemon and one that is not private answer false; any other failure is
// an error.
func (s *Store) checkOutbox() (bool, error) {
	home, err := s.root.walk(outboxPath(s.role)[:3]...)
	if err == nil {
		var fd int
		fd, err = openDirAt(s.root.openat2, home, OutboxName, true)
		closeFD(home)
		if err == nil {
			st, err := fstatFD(fd)
			closeFD(fd)
			if err != nil {
				return false, fmt.Errorf("leadchat: cannot examine the outbox: %w", err)
			}
			return uint64(st.Dev) == s.grant.dev && st.Ino == s.grant.ino, nil
		}
	}
	if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.EACCES) || errors.Is(err, ErrInsecure) {
		return false, nil
	}
	return false, err
}

// claim moves the entry name of the held outbox into the claim folder under
// r<id>, the next claim id, by one rename that replaces nothing. From here on
// the entry is the daemon's: the lead's name is not used again. gone reports
// a name that was not there.
func (s *Store) claim(name string) (id int64, gone bool, err error) {
	if s.claimNext > ownerwire.MaxChatSeq {
		return 0, false, fmt.Errorf("%w: the claim ids are spent", ErrFull)
	}
	id = s.claimNext
	s.claimNext++
	if s.beforeClaim != nil {
		s.beforeClaim(name)
	}
	switch err := renameNoReplace(s.rename, s.grant.out, name, s.claimsFD, claimName(id)); {
	case err == unix.ENOENT:
		return 0, true, nil
	case err != nil:
		return 0, false, err
	}
	return id, false, nil
}

// outcome is what became of a claimed reply.
type outcome int

const (
	outPending outcome = iota // not read yet: left for a later scan
	outTaken                  // stored as an entry, the lead's or withheld
	outDone                   // dropped, set aside, or not there: no entry
)

// resolve decides the claimed reply id, which is not committed. The claim is
// judged, never the lead's name: by fstatat in the claim folder, where only
// the daemon renames, first. One that is not a regular file took the
// reply's name before the claim: it is set aside in the quarantine,
// unopened, and the scan stops the lead's chat ([TextStoppedOdd]). A regular
// file is opened with O_NOFOLLOW and O_NONBLOCK and judged on its handle:
// one the daemon may not open, of another user, with more links, that group
// or other may write, or that its owner may not read is dropped (refused);
// one over [MaxReplyBytes] is too long. A claim that is not the entry the
// scan saw settle is read only when its own change time is [SettleAge] old.
// It is read and judged again; one that changed while it was read, whose
// read failed or that a lease holds is left, at most [UnsettledTries] times
// and [UnsettledAge] long, and then dropped. A reply is stored with its
// claim id in a synced append before its claim is removed.
func (s *Store) resolve(id int64, c *claimState, now time.Time, epoch int64, rep *IngestReport) (outcome, string, error) {
	name := claimName(id)
	st, err := lstatAt(s.claimsFD, name)
	if err == unix.ENOENT {
		delete(s.claims, id)
		return outDone, "", nil
	}
	if err != nil {
		return 0, "", fmt.Errorf("leadchat: cannot examine a claimed reply: %w", err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		// Left in the claim folder when it cannot be set aside: the next
		// turn-on sets it aside, or refuses while the quarantine is full.
		_ = s.quarantine(s.claimsFD, name, s.grant.quar)
		delete(s.claims, id)
		return outDone, TextStoppedOdd, nil
	}
	drop := func(why Reason) (outcome, string, error) {
		if err := s.unlink(s.claimsFD, name, 0); err != nil && err != unix.ENOENT {
			return 0, "", fmt.Errorf("leadchat: cannot remove a claimed reply: %w", err)
		}
		delete(s.claims, id)
		rep.Dropped[why]++
		return outDone, "", nil
	}
	// wait leaves the claim for a later scan, within its bounds; counted
	// says that a read failed (an unsettled claim only ages).
	wait := func(counted bool) (outcome, string, error) {
		if c.first.IsZero() {
			c.first = now
		}
		if counted {
			c.tries++
		}
		if c.tries >= UnsettledTries || now.Sub(c.first) >= UnsettledAge {
			return drop(ReasonUnsettled)
		}
		return outPending, "", nil
	}
	fd, err := openAt(s.root.openat2, s.claimsFD, name, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOCTTY, 0)
	switch {
	case err == unix.EAGAIN:
		return wait(true) // a lease holds it; the open asked the holder to let go
	case err == unix.EACCES || err == unix.EPERM:
		return drop(ReasonRefused)
	case err != nil:
		return 0, "", fmt.Errorf("leadchat: cannot open a claimed reply: %w", err)
	}
	defer closeFD(fd)
	fst, err := fstatFD(fd)
	if err != nil {
		return 0, "", fmt.Errorf("leadchat: cannot examine a claimed reply: %w", err)
	}
	switch {
	case fst.Dev != st.Dev || fst.Ino != st.Ino:
		// Only the daemon renames in the claim folder.
		return 0, "", errors.New("leadchat: a claimed reply changed under the daemon")
	case fst.Mode&unix.S_IFMT != unix.S_IFREG, int(fst.Uid) != unix.Geteuid(), fst.Nlink != 1,
		fst.Mode&0o022 != 0, fst.Mode&0o400 == 0:
		return drop(ReasonRefused)
	case fst.Size > MaxReplyBytes:
		return drop(ReasonTooLong)
	}
	seen := &c.seen
	settled := fst.Dev == seen.Dev && fst.Ino == seen.Ino && fst.Size == seen.Size && fst.Mtim == seen.Mtim
	if !settled && now.Sub(ctimeOf(&fst)) < SettleAge {
		return wait(false)
	}
	buf := make([]byte, MaxReplyBytes+1)
	n := 0
	for n < len(buf) {
		k, err := unix.Read(fd, buf[n:])
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return wait(true)
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
		return wait(true) // written while read, by a writer that held it open
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
		return 0, "", err
	}
	c.committed = true
	s.claimHigh = id
	s.taken = append(s.taken, now)
	if v == VerdictWithheld {
		rep.Withheld++
	} else {
		rep.Taken++
	}
	if s.afterAppend != nil {
		if err := s.afterAppend(); err != nil {
			return 0, "", err
		}
	}
	if err := s.removeCommitted(id); err != nil {
		return 0, "", err
	}
	return outTaken, "", nil
}

// removeCommitted removes the claim of a reply whose entry is stored.
func (s *Store) removeCommitted(id int64) error {
	if s.beforeDelete != nil {
		s.beforeDelete(id)
	}
	if err := s.unlink(s.claimsFD, claimName(id), 0); err != nil && err != unix.ENOENT {
		return fmt.Errorf("leadchat: cannot remove a claimed reply: %w", err)
	}
	delete(s.claims, id)
	return nil
}

// scan is one pass over the held outbox and the claims of this segment; it
// fills rep, and answers a suspension text when the lead's chat must stop
// ("" otherwise). In order:
//
//  1. the inventory, before anything is changed: the outbox's names are
//     read (at most [MaxWaiting] and one), and an outbox whose entries and
//     unread claims are more than [MaxWaiting] stops the chat
//     ([TextStoppedFull]); then each name is judged by fstatat without
//     following it, and a folder, a link or a special file stops the chat
//     ([TextStoppedOdd]). Nothing in the outbox is ever walked or removed;
//  2. the claims an earlier scan of the segment left, in id order: one whose
//     entry is stored is only removed (repaired); any other is resolved,
//     within the rate, and while one is left unread nothing newer is
//     resolved or claimed;
//  3. the new replies, in name order: a regular file with a reply name
//     ([ValidReplyName]) whose change time is [SettleAge] old is claimed and
//     resolved, within the rate. Any other name (a draft whose name starts
//     with a dot, or one that is not a reply name) is never claimed and
//     never removed: it waits, and counts toward [MaxWaiting].
func (s *Store) scan(now time.Time, epoch int64, rep *IngestReport) (string, error) {
	names, more, err := readNames(s.grant.out, MaxWaiting)
	if err != nil {
		if errors.Is(err, unix.EACCES) {
			return TextStoppedFolder, nil
		}
		return "", err
	}
	var open, done []int64 // the claims not committed, and those committed
	for id, c := range s.claims {
		if c.committed {
			done = append(done, id)
		} else {
			open = append(open, id)
		}
	}
	slices.Sort(open)
	slices.Sort(done)
	if more || len(names)+len(open) > MaxWaiting {
		return TextStoppedFull, nil
	}
	type cand struct {
		name string
		st   unix.Stat_t
	}
	var cands []cand
	for _, name := range sortedCopy(names) {
		st, err := lstatAt(s.grant.out, name)
		switch {
		case err == unix.ENOENT:
			continue
		case err == unix.EACCES:
			return TextStoppedFolder, nil
		case err != nil:
			return "", fmt.Errorf("leadchat: cannot examine an entry of the outbox: %w", err)
		case st.Mode&unix.S_IFMT != unix.S_IFREG:
			return TextStoppedOdd, nil
		case ValidReplyName(name):
			cands = append(cands, cand{name, st})
		}
	}

	for _, id := range done {
		if err := s.removeCommitted(id); err != nil {
			return "", err
		}
		rep.Repaired++
	}
	budget := s.budget(now)
	blocked := false
	resolve := func(id int64) (string, error) {
		out, stop, err := s.resolve(id, s.claims[id], now, epoch, rep)
		switch {
		case err != nil || stop != "":
			return stop, err
		case out == outPending:
			blocked = true
		case out == outTaken:
			budget--
		}
		return "", nil
	}
	for _, id := range open {
		if blocked || budget == 0 {
			blocked = true
			break
		}
		if stop, err := resolve(id); err != nil || stop != "" {
			return stop, err
		}
	}
	claimed := 0
	for _, c := range cands {
		if blocked || budget == 0 || now.Sub(ctimeOf(&c.st)) < SettleAge {
			continue
		}
		id, gone, err := s.claim(c.name)
		if err != nil {
			return "", err
		}
		if gone {
			continue
		}
		claimed++
		s.claims[id] = &claimState{seen: c.st}
		if s.afterClaim != nil {
			if err := s.afterClaim(id); err != nil {
				return "", err
			}
		}
		if stop, err := resolve(id); err != nil || stop != "" {
			return stop, err
		}
	}
	unread := 0
	for _, c := range s.claims {
		if !c.committed {
			unread++
		}
	}
	rep.Waiting = len(names) - claimed + unread
	return "", nil
}

// PurgeReport is what one [Store.PurgeQuarantine] did.
type PurgeReport struct {
	// Removed counts the entries of the quarantine removed whole, and Left
	// those still there: a folder deeper or larger than one purge removes,
	// one that did not stay empty, or one that could not be removed.
	Removed, Left int
}

// PurgeQuarantine removes what the lead's quarantine holds, for the owner:
// each entry is unlinked, and a folder is removed with what it holds, one
// name at a time below descriptors, never following a link or crossing a
// mount point, at most 16 deep and 8192 entries in all a purge. What it
// cannot remove is left (counted in Left); a later purge goes on from there.
// A lead without a quarantine has nothing to purge.
func (s *Store) PurgeQuarantine() (PurgeReport, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var rep PurgeReport
	if err := s.usable(); err != nil {
		return rep, err
	}
	lead, err := s.root.walk("leads", s.role)
	if errors.Is(err, unix.ENOENT) {
		return rep, nil
	}
	if err != nil {
		return rep, homeErr(err)
	}
	defer closeFD(lead)
	quar, err := openDirAt(s.root.openat2, lead, QuarantineName, true)
	if errors.Is(err, unix.ENOENT) {
		return rep, nil
	}
	if err != nil {
		return rep, fmt.Errorf("the quarantine: %w", err)
	}
	defer closeFD(quar)
	names, more, err := readNames(quar, maxPurgeEntries)
	if err != nil {
		return rep, err
	}
	if more {
		rep.Left++
	}
	left := maxPurgeEntries - len(names)
	for _, name := range names {
		err := s.unlink(quar, name, 0)
		if err == unix.EISDIR {
			err = removeTree(s.root.openat2, s.unlink, quar, name, &left, 0)
		}
		if err == nil || err == unix.ENOENT {
			rep.Removed++
		} else {
			rep.Left++
		}
	}
	return rep, nil
}
