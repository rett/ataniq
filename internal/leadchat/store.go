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
	// SrcWindow is how many of the newest entries a reply's src is matched
	// against, to remove a reply that was stored and not yet removed.
	SrcWindow = 64
	// RatePerMinute and RatePerHour bound the replies taken from one lead.
	// The excess waits in the outbox.
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

	gen int64
	src string
}

// Status is what the store says of its lead.
type Status struct {
	// Chat is ownerwire.ChatOff or ownerwire.ChatRead.
	Chat string
	// Gen is the generation of the switch: it grows by one with every
	// change, and is 0 for a lead whose chat was never turned on.
	Gen int64
	// Head is the highest sequence number ever given, 0 when none was. It
	// never goes down: not by a switch, a compaction or a restart.
	Head int64
	// Entries is how many entries the store holds now.
	Entries int
	// Dropped counts, by reason, what the scans of this store removed or
	// left since it was opened.
	Dropped map[Reason]int
}

// IngestReport is what one [Store.Ingest] did.
type IngestReport struct {
	// Taken counts the replies stored as the lead's entries, Withheld those
	// stored as [TextWithheld], and Repaired the replies that were already
	// stored and only removed now.
	Taken, Withheld, Repaired int
	// Dropped counts, by reason, what the scan removed or left.
	Dropped map[Reason]int
	// Waiting counts what is left in the outbox for a later scan.
	Waiting int
	// Missing is true when the outbox is not there.
	Missing bool
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
	mu     sync.Mutex
	dirFD  int
	f      *os.File
	name   string // <layout role>.jsonl
	now    func() time.Time
	ob     *outbox
	closed bool
	broken error

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

	taken    []time.Time // when replies were taken, the last hour
	lastDrop map[Reason]time.Time
	dropped  map[Reason]int

	// Test hooks.
	write       func(f *os.File, data []byte) (int, error)
	sync        func(f *os.File) error
	afterAppend func() error // after an entry of a reply is stored, before its file is removed
	beforeSwap  func() error // in a compaction, after the temporary file is synced
}

// OpenStore opens the chat of the lead whose layout role is role, below the
// state root. It makes the folder <state root>/chat (mode 0700) and the
// file <layout role>.jsonl (mode 0600) when they are not there, refuses a
// folder or file that is not private ([ErrInsecure]) and a file another
// holder has locked ([ErrLocked]), and replays the file: a last line without
// its newline is cut off, any other damage is [ErrCorrupt] and nothing is
// repaired. A temporary file a compaction left is removed.
//
// Every lead's chat opens off, whatever the file says: a switch is never
// restored from disk. When the file holds a turned-on switch or entries (the
// daemon stopped while chat was on), they are dropped by a compaction, as a
// switch to off would, so the next time the desk turns chat on the lead
// starts an empty segment. The sequence numbers go on from where they were.
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
	s := &Store{
		dirFD: dirFD, name: role + ".jsonl", now: now,
		ob:   &outbox{root: root, role: role, now: now},
		chat: ownerwire.ChatOff, lastDrop: map[Reason]time.Time{}, dropped: map[Reason]int{},
		write: func(f *os.File, data []byte) (int, error) { return f.Write(data) },
		sync:  func(f *os.File) error { return f.Sync() },
	}
	if err := s.start(); err != nil {
		if s.f != nil {
			_ = s.f.Close()
		}
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
	created := false
	switch err := unix.Mkdirat(rootFD, ChatDirName, 0o700); err {
	case nil:
		created = true
	case unix.EEXIST:
	default:
		return -1, fmt.Errorf("leadchat: cannot make the chat folder: %w", err)
	}
	fd, err := openAt(root.openat2, rootFD, ChatDirName, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	switch {
	case err == unix.ELOOP || err == unix.ENOTDIR || err == unix.EXDEV:
		return -1, fmt.Errorf("%w: the chat folder is a symbolic link, a mount point or not a folder", ErrInsecure)
	case err != nil:
		return -1, fmt.Errorf("leadchat: cannot open the chat folder: %w", err)
	}
	if created {
		// The umask may have cleared bits; the folder is 0700 exactly.
		if err := unix.Fchmod(fd, 0o700); err != nil {
			closeFD(fd)
			return -1, fmt.Errorf("leadchat: cannot set the mode of the chat folder: %w", err)
		}
		if err := syncFD(rootFD); err != nil {
			closeFD(fd)
			return -1, fmt.Errorf("leadchat: cannot sync the state root: %w", err)
		}
	}
	if err := checkDirFD(fd, true); err != nil {
		closeFD(fd)
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
	if empty {
		return s.appendData(mustLine(openLine{Kind: kindOpen, V: fileVersion, Next: s.last + 1}))
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
		if l.V != fileVersion || l.Next < 1 || l.Next > ownerwire.MaxChatSeq+1 {
			return corrupt(n, "the open line has a version or a floor that is not valid")
		}
		s.last = l.Next - 1
	case switchLine:
		at, ok := parseTime(l.At)
		switch {
		case l.Chat != ownerwire.ChatOff && l.Chat != ownerwire.ChatRead:
			return corrupt(n, "the switch is not off or read")
		case l.Gen <= s.gen:
			return corrupt(n, "the generation does not grow")
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
		case l.Src != "" && !validSrc(l.Src), l.Who == ownerwire.ChatWhoLead && l.Src == "":
			return corrupt(n, "the source is not valid")
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
	return entryLine{Kind: kindEntry, Seq: e.Seq, Gen: e.gen, Epoch: e.Epoch, Who: e.Who, At: formatTime(e.At), Text: e.Text, Src: e.src}
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

// turnOff sets the switch off in memory and drops every entry: from here on
// nothing is read, whatever happens to the file.
func (s *Store) turnOff(now time.Time, epoch int64) {
	s.chat, s.gen, s.switchAt, s.switchEpoch, s.discarded = ownerwire.ChatOff, s.gen+1, now.UTC().Truncate(time.Second), epoch, 0
	s.entries = nil
	s.taken = nil
	s.lastDrop = map[Reason]time.Time{}
}

// snapshot is the content of a compacted file: the open line with the
// floor, the switch, and the entries held.
func (s *Store) snapshot() ([]byte, error) {
	var buf bytes.Buffer
	buf.Write(mustLine(openLine{Kind: kindOpen, V: fileVersion, Next: s.last + 1}))
	if s.gen > 0 {
		data, err := encodeLine(switchLine{
			Kind: kindSwitch, Chat: s.chat, Gen: s.gen, At: formatTime(s.switchAt),
			Epoch: s.switchEpoch, Discarded: s.discarded,
		})
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
	err := s.f.Close()
	closeFD(s.dirFD)
	return err
}

// Status reports the lead's switch, generation, head and counts. A broken
// store answers its error: the lead's chat fails closed.
func (s *Store) Status() (Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.usable(); err != nil {
		return Status{}, err
	}
	s.retain(s.now())
	d := make(map[Reason]int, len(s.dropped))
	for k, v := range s.dropped {
		d[k] = v
	}
	return Status{Chat: s.chat, Gen: s.gen, Head: s.last, Entries: len(s.entries), Dropped: d}, nil
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
func (s *Store) Read(after int64) (entries []Entry, reset bool, head int64, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.usable(); err != nil {
		return nil, false, 0, err
	}
	if after < 0 || after > ownerwire.MaxChatSeq {
		return nil, false, 0, fmt.Errorf("%w: the cursor is out of range", ErrInvalid)
	}
	if s.chat != ownerwire.ChatRead {
		return nil, false, 0, ErrOff
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
// the status. Setting the switch it already has changes nothing. send is
// never accepted here ([ErrInvalid]).
//
// Off to read first clears the outbox, so nothing written before is taken,
// then appends the switch line (with how many files the clear removed) and
// the entry [TextTurnedOn] in one synced write. A clear that fails leaves
// the switch off.
//
// Read to off turns the switch off in memory first, so nothing is read from
// that moment, then drops every entry of the lead by a compaction that keeps
// the floor of the sequence numbers and the switch line. A compaction that
// fails breaks the store, with the switch off.
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
	err := s.setSwitch(chat, epoch)
	s.mu.Unlock()
	if err != nil {
		return Status{}, err
	}
	return s.Status()
}

func (s *Store) setSwitch(chat string, epoch int64) error {
	if err := s.usable(); err != nil {
		return err
	}
	if chat == s.chat {
		return nil
	}
	now := s.now()
	if chat == ownerwire.ChatOff {
		s.turnOff(now, epoch)
		return s.compact()
	}
	n, err := s.ob.clear()
	if err != nil {
		return err
	}
	if s.last+1 > ownerwire.MaxChatSeq {
		return fmt.Errorf("%w: the sequence numbers are spent", ErrFull)
	}
	at := now.UTC().Truncate(time.Second)
	gen := s.gen + 1
	sw := mustLine(switchLine{Kind: kindSwitch, Chat: ownerwire.ChatRead, Gen: gen, At: formatTime(at), Epoch: epoch, Discarded: int64(n)})
	e := Entry{Seq: s.last + 1, Who: ownerwire.ChatWhoSystem, At: at, Text: TextTurnedOn, Epoch: epoch, gen: gen}
	data, err := encodeLine(s.entryLine(e))
	if err != nil {
		return err
	}
	data = append(sw, data...)
	if err := s.room(len(data)); err != nil {
		return err
	}
	if err := s.appendData(data); err != nil {
		return err
	}
	s.chat, s.gen, s.switchAt, s.switchEpoch, s.discarded = ownerwire.ChatRead, gen, at, epoch, int64(n)
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

// stored reports whether a reply's src is among the newest [SrcWindow]
// entries.
func (s *Store) stored(src string) bool {
	for i, n := len(s.entries)-1, 0; i >= 0 && n < SrcWindow; i, n = i-1, n+1 {
		if s.entries[i].src == src {
			return true
		}
	}
	return false
}

// Ingest scans the lead's outbox once and takes what it may. A lead whose
// switch is off answers [ErrOff] and nothing is read. epoch is the lead's
// epoch, recorded with each entry.
//
// Each settled reply is judged by [ReplyText]. One that passes is stored as
// the lead's entry, one that looks like a credential as the system's
// [TextWithheld], each with its src, in a synced append; only then is its
// file removed, and only when its name still holds the inode that was read.
// A reply whose src is among the newest [SrcWindow] entries was stored by an
// earlier scan that stopped before it removed the file: it is removed and not
// stored again. A reply that is too long or not plain text is removed.
// Taken replies (stored or withheld) count against [RatePerMinute] and
// [RatePerHour]; the excess waits. For the drops the owner is told about, a
// fixed system entry is written at most once per reason a minute.
//
// An error from the outbox leaves the store as it is; a write or sync that
// fails breaks it.
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
	now := s.now()
	s.retain(now)
	if s.stale && now.Sub(s.lastCompact) >= compactEvery {
		if err := s.compact(); err != nil {
			return rep, err
		}
	}
	b, err := s.ob.scan(s.budget(now))
	if err != nil {
		return rep, err
	}
	defer b.Close()
	for k, v := range b.Dropped {
		rep.Dropped[k] += v
	}
	rep.Waiting, rep.Missing = b.Waiting, b.Missing
	defer func() {
		for k, v := range rep.Dropped {
			s.dropped[k] += v
		}
	}()
	for _, r := range b.Replies {
		if s.stored(r.Src) {
			if _, err := b.Remove(r); err != nil {
				return rep, err
			}
			rep.Repaired++
			continue
		}
		text, v := ReplyText(r.Data)
		e := Entry{src: r.Src}
		switch v {
		case VerdictTooLong, VerdictNotPlain:
			why := ReasonTooLong
			if v == VerdictNotPlain {
				why = ReasonNotPlain
			}
			removed, err := b.Remove(r)
			if err != nil {
				return rep, err
			}
			if removed {
				rep.Dropped[why]++
			}
			continue
		case VerdictWithheld:
			e.Who, e.Text = ownerwire.ChatWhoSystem, TextWithheld
		default:
			e.Who, e.Text = ownerwire.ChatWhoLead, text
		}
		if err := s.addEntries(now, epoch, e); err != nil {
			return rep, err
		}
		s.taken = append(s.taken, now)
		if v == VerdictWithheld {
			rep.Withheld++
		} else {
			rep.Taken++
		}
		if s.afterAppend != nil {
			if err := s.afterAppend(); err != nil {
				return rep, err
			}
		}
		if _, err := b.Remove(r); err != nil {
			return rep, err
		}
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
