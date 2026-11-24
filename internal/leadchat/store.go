//go:build linux && ataniq_canary

package leadchat

import (
	"bufio"
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/leadsandbox/profile"
)

// The bounds of a store.
const (
	// ChatDirName is the store's folder below the state root, mode 0700.
	ChatDirName = "chat"
	// RetainEntries and RetainAge bound what a lead's store keeps: the
	// newest 500 entries, none older than 14 days, whichever keeps fewer.
	// The latest entry that says chat was turned on is never expired.
	RetainEntries = 500
	RetainAge     = 14 * 24 * time.Hour
	// RatePerMinute and RatePerHour bound the replies taken from one lead.
	// The excess waits in the outbox, where it counts toward [MaxWaiting].
	RatePerMinute = 6
	RatePerHour   = 60
	// MaxJournalBytes bounds a lead's chat file: a replay refuses a larger
	// file as corrupt, and an append that would pass it compacts first and
	// is refused ([ErrFull]) when the file would still pass it. What the
	// store keeps (500 entries of at most [MaxLineBytes]) is half of it.
	MaxJournalBytes = 8 << 20
	// compactEvery is how often the file is compacted at most to drop
	// expired entries; reads drop them at once in memory.
	compactEvery = time.Hour
	// dropEvery is how often one reason's drop entry is written at most.
	dropEvery = time.Minute
	// maxGen is the highest generation of the switch. A read switch is at
	// most maxGen-1, so the turn-off after it always has a generation: a
	// replay refuses a read switch at maxGen, and a turn-on that would leave
	// no generation for its turn-off is refused ([ErrFull]).
	maxGen = ownerwire.MaxChatSeq
)

// Entry is one entry of a lead's conversation, as the store holds it.
type Entry struct {
	// Seq is the entry's sequence number, 1 to [ownerwire.MaxChatSeq].
	Seq int64
	// Who is ownerwire.ChatWhoLead or ownerwire.ChatWhoSystem.
	Who string
	// At is when the daemon stored the entry, in UTC to the second.
	At time.Time
	// Text is the entry's text: plain text by [ownerwire.ValidChatText].
	Text string
	// Epoch is the lead's epoch the caller gave when the entry was stored.
	Epoch int64

	gen   int64
	claim int64 // the claim id of the reply it was stored from, or 0
}

// Status is what the store says of its lead.
type Status struct {
	// Chat is ownerwire.ChatOff or ownerwire.ChatRead.
	Chat string
	// Suspended is true when a scan stopped the lead's chat (Chat is still
	// read, and [Store.Read] still answers what it holds): only a fresh
	// turn-on takes replies again, and [Store.ReadSeat] answers
	// [ErrSuspended] meanwhile. Read and suspended is not read.
	Suspended bool
	// Gen is the generation of the switch: it grows by one with every
	// change, and is 0 for a lead whose chat was never turned on.
	Gen int64
	// Head is the highest sequence number ever given, 0 when none was. It
	// never goes down: not by a switch, a compaction or a restart.
	Head int64
	// Entries is how many entries the store holds now.
	Entries int
	// Dropped counts, by reason, the replies the scans of this store
	// dropped since it was opened.
	Dropped map[Reason]int
}

// IngestReport is what one [Store.Ingest] did.
type IngestReport struct {
	// Taken counts the replies stored as the lead's entries, Withheld those
	// stored as [TextWithheld], and Repaired the claims of replies an
	// earlier scan of the segment stored that are only removed now.
	Taken, Withheld, Repaired int
	// Dropped counts, by reason, the replies the scan dropped.
	Dropped map[Reason]int
	// Waiting counts what is left for a later scan: every entry left in the
	// outbox (replies not settled or past the rate, drafts, other names) and
	// the replies claimed and not yet read.
	Waiting int
	// Suspended is true when this scan stopped the lead's chat.
	Suspended bool
}

// Options are the settings of [OpenStore].
type Options struct {
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// Store is one lead's chat: its file <state root>/chat/<layout role>.jsonl,
// the copy of it in memory, its switch and its outbox. It is safe for
// concurrent use.
type Store struct {
	mu       sync.Mutex
	root     *Root
	role     string
	dirFD    int
	claimsFD int // <state root>/chat/<layout role>.claims
	f        *os.File
	name     string // <layout role>.jsonl
	now      func() time.Time
	closed   bool
	broken   error

	chat        string
	gen         int64
	switchAt    time.Time
	switchEpoch int64
	discarded   int64
	last        int64 // the highest sequence number given, or the open floor less one
	entries     []Entry
	stale       bool  // the file holds entries that memory dropped
	size        int64 // the file's size in bytes

	replayedEntries bool // the replay read an entry of a segment
	lastCompact     time.Time

	claimHigh int64                 // the highest claim id stored, or the open line's
	claimNext int64                 // the next claim id to give
	suspended bool                  // a scan stopped the chat; a fresh turn-on is needed
	grant     *grant                // the outbox the turn-on made and holds, while chat is read
	claims    map[int64]*claimState // the claims of this segment not yet removed

	taken    []time.Time // when replies were taken, the last hour
	lastDrop map[Reason]time.Time
	dropped  map[Reason]int

	// Test hooks.
	write         func(f *os.File, data []byte) (int, error)
	sync          func(f *os.File) error
	unlink        unlinkFunc
	rename        renameFunc
	afterStage    func()               // in a turn-on, after the new outbox is made in the staging folder, before it is opened
	beforePublish func()               // in a turn-on, after the earlier outbox is set aside, before the new one is published
	afterPublish  func()               // in a turn-on, after the new outbox is published, before its name is checked
	beforeClaim   func(name string)    // before an entry of the outbox is claimed
	afterClaim    func(id int64) error // after a reply is claimed, before it is read
	afterRead     func(name string)    // after a claimed reply is read, before the second fstat
	afterAppend   func() error         // after an entry of a reply is stored, before its claim is removed
	beforeDelete  func(id int64)       // before the claim of a stored reply is removed
	beforeSwap    func() error         // in a compaction, after the temporary file is synced

	// fstat is the turn-on's stat of the folder it made.
	fstat func(fd int) (unix.Stat_t, error)
	// purgeBudget is how many removals one purge makes (maxPurgeEntries;
	// a test makes it smaller), and purgeMoved the counter of the names of
	// the folders purges moved up.
	purgeBudget int
	purgeMoved  int64
}

// OpenStore opens the chat of the lead whose layout role is role, below the
// state root. It makes the folder <state root>/chat (mode 0700), the file
// <layout role>.jsonl (mode 0600) and the lead's claim folder
// <layout role>.claims (mode 0700) when they are not there, opens the
// folders by openat2 ([ErrUnavailable] without it), refuses a folder or file
// that is not private ([ErrInsecure]) and a file another
// holder has locked ([ErrLocked]), and replays the file: a last line without
// its newline is cut off, any other damage is [ErrCorrupt] and nothing is
// repaired. A temporary file a compaction left is removed.
//
// Every lead's chat opens off, whatever the file says: a switch is never
// restored from disk. When the file holds a turned-on switch or entries (the
// daemon stopped while chat was on), they are dropped by a compaction, as a
// switch to off would, so the next time the desk turns chat on the lead
// starts an empty segment. The sequence numbers and the claim floor go on
// from where they were; the claims and the outbox an earlier run left are
// discarded by the next turn-on.
//
// An error leaves only this lead without chat; other leads' stores are
// files of their own.
func OpenStore(root *Root, role string, opts Options) (*Store, error) {
	if !profile.ValidName(role) {
		return nil, fmt.Errorf("%w: the role is not a layout name", ErrInvalid)
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	dirFD, err := openChatDir(root)
	if err != nil {
		return nil, err
	}
	claimsFD, err := privateDirAt(root.openat2, dirFD, claimsName(role))
	if err != nil {
		closeFD(dirFD)
		return nil, fmt.Errorf("the claim folder: %w", err)
	}
	s := &Store{
		root: root, role: role, dirFD: dirFD, claimsFD: claimsFD, name: role + ".jsonl",
		now:  now,
		chat: ownerwire.ChatOff, lastDrop: map[Reason]time.Time{}, dropped: map[Reason]int{},
		claims: map[int64]*claimState{},
		write:  func(f *os.File, data []byte) (int, error) { return f.Write(data) },
		sync:   func(f *os.File) error { return f.Sync() },
		unlink: unix.Unlinkat,
		rename: unix.Renameat2,
		fstat:  fstatFD,

		purgeBudget: maxPurgeEntries,
	}
	if err := s.start(); err != nil {
		if s.f != nil {
			_ = s.f.Close()
		}
		closeFD(claimsFD)
		closeFD(dirFD)
		return nil, err
	}
	return s, nil
}

// openChatDir makes the chat folder when it is not there and opens it: a
// folder of the caller with mode 0700 exactly.
func openChatDir(root *Root) (int, error) {
	rootFD, err := root.dup()
	if err != nil {
		return -1, err
	}
	defer closeFD(rootFD)
	fd, err := privateDirAt(root.openat2, rootFD, ChatDirName)
	if err != nil {
		return -1, fmt.Errorf("the chat folder: %w", err)
	}
	return fd, nil
}

const fileFlags = unix.O_RDWR | unix.O_APPEND

// checkHandle judges an open file by its handle: a regular file of the
// caller with mode 0600 and one link.
func checkHandle(fd int) error {
	st, err := fstatFD(fd)
	if err != nil {
		return fmt.Errorf("leadchat: cannot examine the chat file: %w", err)
	}
	switch {
	case st.Mode&unix.S_IFMT != unix.S_IFREG:
		return fmt.Errorf("%w: the chat file is not a regular file", ErrInsecure)
	case int(st.Uid) != unix.Geteuid():
		return fmt.Errorf("%w: the chat file is not owned by the caller", ErrInsecure)
	case st.Mode&07777 != 0o600:
		return fmt.Errorf("%w: the chat file has mode %04o, want 0600", ErrInsecure, st.Mode&07777)
	case st.Nlink != 1:
		return fmt.Errorf("%w: the chat file has more than one link", ErrInsecure)
	}
	return nil
}

func lockFD(fd int) error {
	for {
		switch err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err {
		case unix.EINTR:
			continue
		case nil:
			return nil
		case unix.EWOULDBLOCK:
			return ErrLocked
		default:
			return fmt.Errorf("leadchat: cannot lock the chat file: %w", err)
		}
	}
}

// openFile opens the chat file, creating it with mode 0600 when it is not
// there, locks it and judges it by its handle. A new file is made durable in
// its folder before it is used.
func (s *Store) openFile() (created bool, err error) {
	created = true
	fd, err := openRetry(func() (int, error) {
		return unix.Openat(s.dirFD, s.name, fileFlags|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	})
	if err == unix.EEXIST {
		created = false
		fd, err = openRetry(func() (int, error) {
			return unix.Openat(s.dirFD, s.name, fileFlags|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		})
	}
	switch {
	case err == unix.ELOOP:
		return false, fmt.Errorf("%w: the chat file is a symbolic link", ErrInsecure)
	case err == unix.ENXIO || err == unix.EISDIR:
		return false, fmt.Errorf("%w: the chat file is not a regular file", ErrInsecure)
	case err != nil:
		return false, fmt.Errorf("leadchat: cannot open the chat file: %w", err)
	}
	f := os.NewFile(uintptr(fd), s.name)
	fail := func(err error) (bool, error) {
		_ = f.Close()
		return false, err
	}
	if err := checkHandle(fd); err != nil && !created {
		return fail(err)
	}
	if err := lockFD(fd); err != nil {
		return fail(err)
	}
	if created {
		if err := unix.Fchmod(fd, 0o600); err != nil {
			return fail(fmt.Errorf("leadchat: cannot set the mode of the chat file: %w", err))
		}
		if err := syncFD(s.dirFD); err != nil {
			return fail(fmt.Errorf("leadchat: cannot sync the chat folder: %w", err))
		}
	}
	if err := checkHandle(fd); err != nil {
		return fail(err)
	}
	s.f = f
	return created, nil
}

func (s *Store) tmpName() string { return s.name + ".tmp" }

// start opens and replays the file, and turns a lead that was on off.
func (s *Store) start() error {
	if _, err := s.openFile(); err != nil {
		return err
	}
	// A temporary file that an earlier run left is of no use.
	if err := unix.Unlinkat(s.dirFD, s.tmpName(), 0); err != nil && err != unix.ENOENT {
		return fmt.Errorf("leadchat: cannot remove the old temporary file: %w", err)
	}
	empty, err := s.replay()
	if err != nil {
		return err
	}
	now := s.now()
	s.lastCompact = now
	s.claimNext = s.claimHigh + 1
	if empty {
		return s.appendData(mustLine(openLine{Kind: kindOpen, V: fileVersion, Next: s.last + 1, Claim: s.claimHigh}))
	}
	if s.chat == ownerwire.ChatRead || s.replayedEntries {
		s.turnOff(now, s.switchEpoch)
		return s.compact()
	}
	return nil
}

// corrupt is the error of a damaged file: a fixed reason and the line's
// number, never a byte of the file.
func corrupt(n int, reason string) error {
	return fmt.Errorf("%w: line %d: %s", ErrCorrupt, n, reason)
}

// replayState is what a replay keeps while it reads: the order of the
// sequence numbers and whether a turned-on entry is due. It keeps no entry:
// the store opens off, so what a file holds of a segment is dropped.
type replayState struct {
	lastSeq    int64 // the sequence number of the last entry read
	lastClaim  int64 // the claim id of the last entry read with one
	wantMarker bool  // a read switch line was read and no entry yet
	entries    bool  // an entry of the current segment was read
}

// replay reads the file, line by line, and keeps only the floor of the
// sequence numbers and the switch. It reports a file that holds no line. A
// file over [MaxJournalBytes] or a line over [MaxLineBytes] (its newline
// included) is corrupt.
func (s *Store) replay() (empty bool, err error) {
	st, err := fstatFD(int(s.f.Fd()))
	if err != nil {
		return false, fmt.Errorf("leadchat: cannot examine the chat file: %w", err)
	}
	if st.Size > MaxJournalBytes {
		return false, corrupt(0, "the file is larger than the bound")
	}
	if _, err := s.f.Seek(0, io.SeekStart); err != nil {
		return false, fmt.Errorf("leadchat: cannot read the chat file: %w", err)
	}
	rd := bufio.NewReaderSize(io.LimitReader(s.f, MaxJournalBytes+1), MaxLineBytes+1)
	var offset int64
	var rs replayState
	n := 0
	for {
		raw, err := rd.ReadSlice('\n')
		if err == nil {
			n++
			if len(raw) > MaxLineBytes {
				return false, corrupt(n, "the line is longer than the bound")
			}
			if offset+int64(len(raw)) > MaxJournalBytes {
				return false, corrupt(n, "the file is larger than the bound")
			}
			if err := s.replayLine(n, raw[:len(raw)-1], &rs); err != nil {
				return false, err
			}
			offset += int64(len(raw))
			continue
		}
		if err == bufio.ErrBufferFull {
			return false, corrupt(n+1, "the line is longer than the bound")
		}
		if err != io.EOF {
			return false, fmt.Errorf("leadchat: cannot read the chat file: %w", err)
		}
		if len(raw) > 0 {
			// An append that never returned: nothing acted on it.
			if err := s.f.Truncate(offset); err != nil {
				return false, fmt.Errorf("leadchat: cannot cut the unfinished line: %w", err)
			}
			if err := s.sync(s.f); err != nil {
				return false, fmt.Errorf("leadchat: cannot sync the cut: %w", err)
			}
		}
		if _, err := s.f.Seek(0, io.SeekEnd); err != nil {
			return false, fmt.Errorf("leadchat: cannot read the chat file: %w", err)
		}
		s.size = offset
		s.replayedEntries = rs.entries
		return n == 0, nil
	}
}

// replayLine applies one line, or says why the file is corrupt. Entries are
// checked in their own increasing order: a compacted file holds the
// retained entries below the open line's floor, and appends after it.
func (s *Store) replayLine(n int, raw []byte, rs *replayState) error {
	v, err := decodeLine(raw)
	if err != nil {
		return corrupt(n, err.Error())
	}
	if _, isOpen := v.(openLine); isOpen != (n == 1) {
		return corrupt(n, "the open line is not the first line, or the first line is not the open line")
	}
	switch l := v.(type) {
	case openLine:
		if l.V != fileVersion || l.Next < 1 || l.Next > ownerwire.MaxChatSeq+1 || l.Claim < 0 || l.Claim > ownerwire.MaxChatSeq {
			return corrupt(n, "the open line has a version or a floor that is not valid")
		}
		s.last = l.Next - 1
		s.claimHigh = l.Claim
	case switchLine:
		at, ok := parseTime(l.At)
		switch {
		case l.Chat != ownerwire.ChatOff && l.Chat != ownerwire.ChatRead:
			return corrupt(n, "the switch is not off or read")
		case l.Gen <= s.gen:
			return corrupt(n, "the generation does not grow")
		case l.Gen > maxGen || l.Chat == ownerwire.ChatRead && l.Gen >= maxGen:
			return corrupt(n, "the generation is out of range")
		case !ok:
			return corrupt(n, "the time is not UTC to the second")
		case l.Epoch < 0 || l.Epoch > ownerwire.MaxChatSeq || l.Discarded < 0:
			return corrupt(n, "the epoch or the count is out of range")
		}
		rs.entries = false
		rs.wantMarker = l.Chat == ownerwire.ChatRead
		s.chat, s.gen, s.switchAt, s.switchEpoch, s.discarded = l.Chat, l.Gen, at, l.Epoch, l.Discarded
	case entryLine:
		_, ok := parseTime(l.At)
		reply := l.Who == ownerwire.ChatWhoLead || l.Text == TextWithheld
		switch {
		case s.chat != ownerwire.ChatRead || l.Gen != s.gen:
			return corrupt(n, "an entry while chat is off, or of another generation")
		case l.Seq <= rs.lastSeq || l.Seq > ownerwire.MaxChatSeq:
			return corrupt(n, "the sequence number does not grow")
		case l.Who != ownerwire.ChatWhoLead && l.Who != ownerwire.ChatWhoSystem:
			return corrupt(n, "the writer is not lead or system")
		case !ok:
			return corrupt(n, "the time is not UTC to the second")
		case l.Epoch < 0 || l.Epoch > ownerwire.MaxChatSeq:
			return corrupt(n, "the epoch is out of range")
		case !ownerwire.ValidChatText(l.Text):
			return corrupt(n, "the text is not plain text")
		case l.Who == ownerwire.ChatWhoSystem && !systemText(l.Text):
			return corrupt(n, "a system entry has a text that is not one of the fixed texts")
		case rs.wantMarker != (l.Who == ownerwire.ChatWhoSystem && l.Text == TextTurnedOn):
			return corrupt(n, "a segment does not start with its turned-on entry, or has a second one")
		case l.hasClaim != reply:
			return corrupt(n, "a reply's entry lacks its claim id, or another entry has one")
		case reply && (l.Claim < 1 || l.Claim > ownerwire.MaxChatSeq || l.Claim <= rs.lastClaim):
			return corrupt(n, "the claim id is out of range or does not grow")
		}
		if l.Claim != 0 {
			rs.lastClaim = l.Claim
			s.claimHigh = max(s.claimHigh, l.Claim)
		}
		rs.lastSeq = l.Seq
		rs.wantMarker = false
		rs.entries = true
		s.last = max(s.last, l.Seq)
	}
	return nil
}

func mustLine(v any) []byte {
	data, err := encodeLine(v)
	if err != nil {
		panic(err) // the fixed lines always encode
	}
	return data
}

// room makes room for an append of n bytes within [MaxJournalBytes]: when
// the file would pass it, the expired entries are dropped and the file is
// compacted first, and an append that would pass it still is refused.
func (s *Store) room(n int) error {
	if s.size+int64(n) <= MaxJournalBytes {
		return nil
	}
	s.retain(s.now())
	if err := s.compact(); err != nil {
		return err
	}
	if s.size+int64(n) <= MaxJournalBytes {
		return nil
	}
	return fmt.Errorf("%w: the chat file would pass its bound", ErrFull)
}

// appendData writes data at the end of the file and syncs it. An append
// that would pass [MaxJournalBytes] is refused and writes nothing. A failure
// breaks the store for good: what reached the disk is unknown.
func (s *Store) appendData(data []byte) error {
	if s.size+int64(len(data)) > MaxJournalBytes {
		return fmt.Errorf("%w: the chat file would pass its bound", ErrFull)
	}
	n, err := s.write(s.f, data)
	if err == nil && n != len(data) {
		err = errors.New("short write")
	}
	if err == nil {
		err = s.sync(s.f)
	}
	if err != nil {
		s.broken = fmt.Errorf("%w: %v", ErrBroken, err)
		return s.broken
	}
	s.size += int64(len(data))
	return nil
}

// usable is the check every call makes first.
func (s *Store) usable() error {
	switch {
	case s.closed:
		return ErrClosed
	case s.broken != nil:
		return s.broken
	}
	return nil
}

func (s *Store) entryLine(e Entry) entryLine {
	return entryLine{Kind: kindEntry, Seq: e.Seq, Gen: e.gen, Epoch: e.Epoch, Who: e.Who, At: formatTime(e.At), Text: e.Text, Claim: e.claim}
}

// addEntries gives the next sequence numbers to new entries of the current
// generation, writes them in one append and keeps them. The texts are plain
// text: the caller's fixed texts or a text [ReplyText] passed.
func (s *Store) addEntries(at time.Time, epoch int64, es ...Entry) error {
	if s.last+int64(len(es)) > ownerwire.MaxChatSeq {
		return fmt.Errorf("%w: the sequence numbers are spent", ErrFull)
	}
	var buf bytes.Buffer
	out := make([]Entry, len(es))
	for i, e := range es {
		e.Seq, e.At, e.Epoch, e.gen = s.last+int64(i)+1, at.UTC().Truncate(time.Second), epoch, s.gen
		data, err := encodeLine(s.entryLine(e))
		if err != nil {
			return err
		}
		buf.Write(data)
		out[i] = e
	}
	if err := s.room(buf.Len()); err != nil {
		return err
	}
	if err := s.appendData(buf.Bytes()); err != nil {
		return err
	}
	s.entries = append(s.entries, out...)
	s.last += int64(len(es))
	return nil
}

// turnOff sets the switch off in memory, ends the segment and drops every
// entry: from here on nothing is read, whatever happens to the file. The
// outbox is let go of; its claims stay in the claim folder until the next
// turn-on discards them. The generation has room: a read switch is at most
// maxGen-1, by the replay and by setSwitch.
func (s *Store) turnOff(now time.Time, epoch int64) {
	s.chat, s.gen, s.switchAt, s.switchEpoch, s.discarded = ownerwire.ChatOff, s.gen+1, now.UTC().Truncate(time.Second), epoch, 0
	s.suspended = false
	s.endGrant()
	s.entries = nil
	s.taken = nil
	s.lastDrop = map[Reason]time.Time{}
}

// snapshot is the content of a compacted file: the open line with the
// high-waters, the switch, and the entries held.
func (s *Store) snapshot() ([]byte, error) {
	var buf bytes.Buffer
	buf.Write(mustLine(openLine{Kind: kindOpen, V: fileVersion, Next: s.last + 1, Claim: s.claimNext - 1}))
	if s.gen > 0 {
		data, err := encodeLine(s.switchLine())
		if err != nil {
			return nil, err
		}
		buf.Write(data)
	}
	for _, e := range s.entries {
		data, err := encodeLine(s.entryLine(e))
		if err != nil {
			return nil, err
		}
		buf.Write(data)
	}
	return buf.Bytes(), nil
}

// endGrant lets go of the outbox and forgets the segment's claims.
func (s *Store) endGrant() {
	if s.grant != nil {
		s.grant.close()
		s.grant = nil
	}
	s.claims = map[int64]*claimState{}
}

// switchLine is the line of the switch as memory holds it.
func (s *Store) switchLine() switchLine {
	return switchLine{
		Kind: kindSwitch, Chat: s.chat, Gen: s.gen, At: formatTime(s.switchAt),
		Epoch: s.switchEpoch, Discarded: s.discarded,
	}
}

// compact writes what memory holds to a temporary file, syncs it, renames
// it over the chat file and syncs the folder. The new file is locked before
// the rename, so no other holder can take the path at any moment. A failure
// breaks the store: memory may already say more (or less) than the file.
func (s *Store) compact() error {
	err := s.compactFile()
	if err != nil {
		s.broken = fmt.Errorf("%w: %v", ErrBroken, err)
		return s.broken
	}
	s.stale = false
	s.lastCompact = s.now()
	return nil
}

func (s *Store) compactFile() error {
	data, err := s.snapshot()
	if err != nil {
		return err
	}
	if len(data) > MaxJournalBytes {
		return errors.New("the snapshot passes the bound")
	}
	tmp := s.tmpName()
	if err := unix.Unlinkat(s.dirFD, tmp, 0); err != nil && err != unix.ENOENT {
		return fmt.Errorf("cannot remove the old temporary file: %w", err)
	}
	fd, err := openRetry(func() (int, error) {
		return unix.Openat(s.dirFD, tmp, fileFlags|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	})
	if err != nil {
		return fmt.Errorf("cannot create the temporary file: %w", err)
	}
	nf := os.NewFile(uintptr(fd), s.name)
	fail := func(err error) error {
		_ = nf.Close()
		_ = unix.Unlinkat(s.dirFD, tmp, 0)
		return err
	}
	if err := unix.Fchmod(fd, 0o600); err != nil {
		return fail(fmt.Errorf("cannot set the mode of the temporary file: %w", err))
	}
	if err := lockFD(fd); err != nil {
		return fail(err)
	}
	if err := checkHandle(fd); err != nil {
		return fail(err)
	}
	if n, err := s.write(nf, data); err != nil || n != len(data) {
		if err == nil {
			err = errors.New("short write")
		}
		return fail(fmt.Errorf("cannot write the temporary file: %w", err))
	}
	if err := s.sync(nf); err != nil {
		return fail(fmt.Errorf("cannot sync the temporary file: %w", err))
	}
	if s.beforeSwap != nil {
		if err := s.beforeSwap(); err != nil {
			_ = nf.Close() // left behind, as a crash would leave it
			return err
		}
	}
	if err := unix.Renameat(s.dirFD, tmp, s.dirFD, s.name); err != nil {
		return fail(fmt.Errorf("cannot rename the temporary file: %w", err))
	}
	old := s.f
	s.f = nf
	s.size = int64(len(data))
	_ = old.Close()
	if err := syncFD(s.dirFD); err != nil {
		return fmt.Errorf("cannot sync the chat folder: %w", err)
	}
	return nil
}

// retain drops from memory the entries past [RetainEntries] or older than
// [RetainAge], keeping the latest turned-on entry whatever its age.
func (s *Store) retain(now time.Time) {
	pin := -1
	for i := len(s.entries) - 1; i >= 0; i-- {
		if e := s.entries[i]; e.Who == ownerwire.ChatWhoSystem && e.Text == TextTurnedOn {
			pin = i
			break
		}
	}
	keep := make([]Entry, 0, len(s.entries))
	pinned := false
	for i, e := range s.entries {
		if i == pin || now.Sub(e.At) <= RetainAge {
			keep = append(keep, e)
			pinned = pinned || i == pin
		}
	}
	if len(keep) > RetainEntries {
		cut := keep[len(keep)-RetainEntries:]
		if pinned && cut[0].Seq > s.entries[pin].Seq {
			cut = append([]Entry{s.entries[pin]}, cut[1:]...)
		}
		keep = cut
	}
	if len(keep) != len(s.entries) {
		s.entries = keep
		s.stale = true
	}
}

// Close releases the file and the folder. A second Close returns
// [ErrClosed].
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	s.closed = true
	s.endGrant()
	err := s.f.Close()
	closeFD(s.claimsFD)
	closeFD(s.dirFD)
	return err
}

// Status reports the lead's switch, generation, head and counts. A broken
// store answers its error: the lead's chat fails closed.
func (s *Store) Status() (Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status()
}

// status is Status; the caller holds s.mu.
func (s *Store) status() (Status, error) {
	if err := s.usable(); err != nil {
		return Status{}, err
	}
	s.retain(s.now())
	d := make(map[Reason]int, len(s.dropped))
	for k, v := range s.dropped {
		d[k] = v
	}
	return Status{Chat: s.chat, Suspended: s.suspended, Gen: s.gen, Head: s.last, Entries: len(s.entries), Dropped: d}, nil
}

// Read answers the entries after the sequence number after, from memory: it
// reads no file. after 0 reads from the start of what is held. Any other
// after must be the sequence number of an entry held now, the last one a
// reader was given: a cursor of an earlier segment, one past the head, and
// one whose entry expired (after the pinned turned-on entry too) answer
// everything held with reset set, so the reader drops what it shows. A
// reader that pages on from an entry it was given, the turned-on entry
// included, is never reset. head is [Status].Head. A lead whose chat is off
// answers [ErrOff].
//
// Read is the retained history, for the daemon's own callers: it answers a
// suspended lead's entries too. What the seat is shown is [Store.ReadSeat].
func (s *Store) Read(after int64) (entries []Entry, reset bool, head int64, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.read(after, false)
}

// ReadSeat answers what [Store.Read] answers, for the seat: under the one
// lock that copies the page, it checks that the lead's entries may be shown
// to the seat, so that no page holds an entry written after a check passed.
// A lead whose chat is off answers [ErrOff], and one whose chat a scan
// stopped [ErrSuspended], until a fresh turn-on.
func (s *Store) ReadSeat(after int64) (entries []Entry, reset bool, head int64, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.read(after, true)
}

// read is Read and ReadSeat; the caller holds s.mu.
func (s *Store) read(after int64, seat bool) (entries []Entry, reset bool, head int64, err error) {
	if err := s.usable(); err != nil {
		return nil, false, 0, err
	}
	if after < 0 || after > ownerwire.MaxChatSeq {
		return nil, false, 0, fmt.Errorf("%w: the cursor is out of range", ErrInvalid)
	}
	if s.chat != ownerwire.ChatRead {
		return nil, false, 0, ErrOff
	}
	if seat && s.suspended {
		return nil, false, 0, ErrSuspended
	}
	s.retain(s.now())
	from := 0
	if after != 0 {
		i, held := slices.BinarySearchFunc(s.entries, after, func(e Entry, seq int64) int { return cmp.Compare(e.Seq, seq) })
		if held {
			from = i + 1
		} else {
			reset = true
		}
	}
	return append([]Entry(nil), s.entries[from:]...), reset, s.last, nil
}

// SetSwitch sets the lead's chat switch to off or read, at once, and answers
// the status the switch left, taken under the same lock: its Chat, Suspended
// and Gen are the effective switch, and no scan comes between. A call that
// fails answers the effective status too, when the store can still say it
// (a failed turn-on of a suspended lead leaves it off), and a zero Status
// otherwise. Setting the switch it already has changes nothing, except that
// read on a suspended lead turns it on afresh, with a new generation. send is
// never accepted here ([ErrInvalid]).
//
// Off to read starts a segment with an outbox made for it. The lead's home
// must be there ([ErrUnavailable]) and reached without a link or a mount
// point ([ErrInsecure], or [ErrUnavailable] when the kernel cannot reach it
// so). The claims an earlier segment left are discarded; a new outbox is
// made in the lead's staging folder with mode 0700, opened and judged there,
// and held open until the segment ends; a probe file made in it is claimed
// and removed, so a file system that cannot rename without replacing refuses
// the turn-on ([ErrUnavailable]); the earlier outbox, an empty one included,
// is set aside, unread, in the lead's quarantine ([ErrQuarantined] when its
// [MaxQuarantine] names all hold something); and the held folder is
// published at the outbox's name, which must then lead to it
// ([ErrUnavailable] otherwise). Then the switch line (with how many things
// the turn-on set aside) and the entry [TextTurnedOn] are appended in one
// synced write. Anything that fails leaves the switch off. The package
// documentation tells the order.
//
// Read to off turns the switch off in memory first, so nothing is read from
// that moment, lets go of the outbox, then drops every entry of the lead by a
// compaction that keeps the high-waters and the switch line. A compaction
// that fails breaks the store, with the switch off. A suspended lead turned
// on again is turned off so first: the fresh turn-on starts a new segment.
//
// epoch is the lead's epoch, recorded with the switch.
func (s *Store) SetSwitch(chat string, epoch int64) (Status, error) {
	if chat != ownerwire.ChatOff && chat != ownerwire.ChatRead {
		return Status{}, fmt.Errorf("%w: the switch is not off or read", ErrInvalid)
	}
	if epoch < 0 || epoch > ownerwire.MaxChatSeq {
		return Status{}, fmt.Errorf("%w: the epoch is out of range", ErrInvalid)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.setSwitch(chat, epoch)
	st, serr := s.status()
	switch {
	case err != nil && serr != nil:
		return Status{}, err
	case err != nil:
		return st, err
	}
	return st, serr
}

func (s *Store) setSwitch(chat string, epoch int64) error {
	if err := s.usable(); err != nil {
		return err
	}
	if chat == s.chat && (chat != ownerwire.ChatRead || !s.suspended) {
		return nil
	}
	if chat == ownerwire.ChatRead {
		// The turn-on and the turn-off after it, and before them the
		// turn-off of a suspended lead.
		need := int64(2)
		if s.suspended {
			need = 3
		}
		if s.gen > maxGen-need {
			return fmt.Errorf("%w: the generations of the switch are spent", ErrFull)
		}
	}
	now := s.now()
	if chat == ownerwire.ChatOff || s.suspended {
		s.turnOff(now, epoch)
		if err := s.compact(); err != nil {
			return err
		}
		if chat == ownerwire.ChatOff {
			return nil
		}
	}
	if s.last+1 > ownerwire.MaxChatSeq {
		return fmt.Errorf("%w: the sequence numbers are spent", ErrFull)
	}
	g, n, err := s.openGrant()
	if err != nil {
		return err
	}
	at := now.UTC().Truncate(time.Second)
	gen := s.gen + 1
	sw := mustLine(switchLine{Kind: kindSwitch, Chat: ownerwire.ChatRead, Gen: gen, At: formatTime(at), Epoch: epoch, Discarded: int64(n)})
	e := Entry{Seq: s.last + 1, Who: ownerwire.ChatWhoSystem, At: at, Text: TextTurnedOn, Epoch: epoch, gen: gen}
	data, err := encodeLine(s.entryLine(e))
	if err == nil {
		data = append(sw, data...)
		err = s.room(len(data))
	}
	if err == nil {
		err = s.appendData(data)
	}
	if err != nil {
		g.close()
		return err
	}
	s.chat, s.gen, s.switchAt, s.switchEpoch, s.discarded = ownerwire.ChatRead, gen, at, epoch, int64(n)
	s.suspended = false
	s.endGrant()
	s.grant = g
	s.entries = []Entry{e}
	s.last = e.Seq
	s.taken = nil
	s.lastDrop = map[Reason]time.Time{}
	return nil
}

// budget is how many replies the rate allows now.
func (s *Store) budget(now time.Time) int {
	kept := s.taken[:0]
	minute := 0
	for _, t := range s.taken {
		if now.Sub(t) < time.Hour {
			kept = append(kept, t)
			if now.Sub(t) < time.Minute {
				minute++
			}
		}
	}
	s.taken = kept
	return max(min(RatePerMinute-minute, RatePerHour-len(kept)), 0)
}

// suspend stops the lead's chat: the scan's fixed system entry text is
// written, the only one of the suspension, the outbox is let go of, and
// nothing more is taken until a fresh turn-on.
func (s *Store) suspend(now time.Time, epoch int64, text string, rep *IngestReport) error {
	s.suspended = true
	rep.Suspended = true
	s.endGrant()
	return s.addEntries(now, epoch, Entry{Who: ownerwire.ChatWhoSystem, Text: text})
}

// Ingest scans the lead's outbox once and takes what it may. A lead whose
// switch is off answers [ErrOff], a suspended lead [ErrSuspended], and
// nothing is read. epoch is the lead's epoch, recorded with each entry.
//
// The outbox's name must still lead to the folder the turn-on made and
// holds (the same device and inode, which cannot be reused while it is
// held), of the daemon's user with mode 0700, reached without a link or a
// mount point; an outbox that is gone, replaced, not private or closed to
// the daemon, and a kernel that no longer lets the daemon reach or claim
// from it, suspends the chat with the entry [TextStoppedFolder]. Every scan
// works from the held descriptor: it never reaches the outbox by name.
//
// The scan ([Store.scan] tells the order) stops the chat before it changes
// anything when the outbox holds more than [MaxWaiting] entries with the
// claims not yet read ([TextStoppedFull]), or holds a folder, a link or a
// special file ([TextStoppedOdd]). Otherwise a settled reply is claimed:
// renamed, by one rename that replaces nothing, into the lead's claim folder
// (<state root>/chat/<layout role>.claims, mode 0700, which no sandbox
// binds) under a name the daemon chooses from a claim counter; only the
// claim is examined, read and removed, never the lead's name. It is judged
// by [ReplyText]: one that passes is stored as the lead's entry, one that
// looks like a credential as the system's [TextWithheld], each with its
// claim id, in a synced append; only then is its claim removed. A reply that
// is refused, too long, not plain text or that cannot be read within its
// bounds is dropped. Drafts and other names are never claimed or removed.
// Taken replies (stored or withheld) count against [RatePerMinute] and
// [RatePerHour]; the excess waits. For each reason of drop, a fixed system
// entry is written at most once a minute.
//
// An error leaves the store as it is, with a claim it made kept for the
// next scan of the segment; a write or sync that fails breaks it.
func (s *Store) Ingest(epoch int64) (IngestReport, error) {
	rep := IngestReport{Dropped: map[Reason]int{}}
	if epoch < 0 || epoch > ownerwire.MaxChatSeq {
		return rep, fmt.Errorf("%w: the epoch is out of range", ErrInvalid)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.usable(); err != nil {
		return rep, err
	}
	if s.chat != ownerwire.ChatRead {
		return rep, ErrOff
	}
	if s.suspended {
		return rep, ErrSuspended
	}
	now := s.now()
	s.retain(now)
	if s.stale && now.Sub(s.lastCompact) >= compactEvery {
		if err := s.compact(); err != nil {
			return rep, err
		}
	}
	defer func() {
		for k, v := range rep.Dropped {
			s.dropped[k] += v
		}
	}()
	same, err := s.checkOutbox()
	stop := ""
	switch {
	case err == nil && !same:
		stop = TextStoppedFolder
	case err == nil:
		stop, err = s.scan(now, epoch, &rep)
	}
	if errors.Is(err, ErrUnavailable) {
		stop, err = TextStoppedFolder, nil
	}
	if err != nil {
		return rep, err
	}
	if stop != "" {
		return rep, s.suspend(now, epoch, stop, &rep)
	}
	var drops []Entry
	for _, why := range dropOrder {
		if rep.Dropped[why] == 0 {
			continue
		}
		if last, ok := s.lastDrop[why]; ok && now.Sub(last) < dropEvery {
			continue
		}
		s.lastDrop[why] = now
		drops = append(drops, Entry{Who: ownerwire.ChatWhoSystem, Text: dropTexts[why]})
	}
	if len(drops) > 0 {
		if err := s.addEntries(now, epoch, drops...); err != nil {
			return rep, err
		}
	}
	return rep, nil
}
