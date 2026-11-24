//go:build linux && ataniq_canary

package leadchat

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"ataniq/internal/leadsandbox/profile"
)

// The bounds and times of the outbox.
const (
	// OutboxName is the outbox folder in the lead's home: on the host
	// <state root>/leads/<layout role>/home/.ataniq-replies, and inside the
	// sandbox /home/lead/.ataniq-replies.
	OutboxName = ".ataniq-replies"
	// MaxDirEntries is the most entries one scan reads from the outbox.
	MaxDirEntries = 256
	// MaxWaiting is the most replies (and folders with a reply name) that
	// wait in the outbox; the excess, in name order, is removed.
	MaxWaiting = 32
	// SettleAge is how old the kernel's change time of a reply must be
	// before it is taken. A lead cannot set the change time, and a rename
	// sets it.
	SettleAge = 2 * time.Second
	// StaleAge is how old a file whose name starts with a dot may grow
	// before it is removed.
	StaleAge = 10 * time.Minute
	// maxClearEntries is the most entries a clear reads; an outbox with
	// more is not cleared and the switch stays off.
	maxClearEntries = 8192
)

// outboxPath are the names from the state root to the outbox of a lead.
func outboxPath(role string) []string {
	return []string{"leads", role, "home", OutboxName}
}

// Reply is one settled reply file that a scan read. Its name is the lead's
// and is never shown; the store keeps only Src.
type Reply struct {
	// Name is the file's name in the outbox.
	Name string
	// Data is the file's bytes, at most [MaxReplyBytes].
	Data []byte
	// Src names the reply for exactly-once: the hex of the first 16 bytes
	// of SHA-256 over the inode number, the name and the bytes.
	Src string
	dev uint64
	ino uint64
}

// replySrc computes Src.
func replySrc(ino uint64, name string, data []byte) string {
	h := sha256.New()
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], ino)
	h.Write(b[:])
	h.Write([]byte(name))
	h.Write([]byte{0})
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// Batch is the result of one [ScanOutbox]: the replies read, what waits and
// what was removed. It holds the outbox's descriptor, so that a reply is
// removed from the folder it was read from; the caller closes it.
type Batch struct {
	fd int
	// Replies are the settled replies read, in name order, at most the
	// scan's limit.
	Replies []Reply
	// Waiting counts the replies left for a later scan (not settled yet,
	// past the limit, or changed while read) and the folders with a reply
	// name.
	Waiting int
	// Dropped counts what the scan removed or left, by reason.
	Dropped map[Reason]int
	// Missing is true when the outbox (or a folder above it) is not there.
	Missing bool

	beforeRemove func(name string) // test hook
}

// Close releases the outbox's descriptor.
func (b *Batch) Close() error {
	if b.fd < 0 {
		return nil
	}
	fd := b.fd
	b.fd = -1
	return unix.Close(fd)
}

// Remove unlinks a reply of the batch, when its name still holds the inode
// that was read: a name that now holds another file keeps it (the lead
// replaced the reply; the new one is read by a later scan), and a name that
// is gone is no error. It reports whether it unlinked. The check and the
// unlink are two calls: a replacement between them is lost, which only the
// lead can cause, in its own folder, against its own reply.
func (b *Batch) Remove(r Reply) (bool, error) {
	if b.fd < 0 {
		return false, ErrClosed
	}
	if b.beforeRemove != nil {
		b.beforeRemove(r.Name)
	}
	st, err := lstatAt(b.fd, r.Name)
	if err == unix.ENOENT {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("leadchat: cannot examine a reply: %w", err)
	}
	if uint64(st.Dev) != r.dev || uint64(st.Ino) != r.ino {
		return false, nil
	}
	if err := unix.Unlinkat(b.fd, r.Name, 0); err != nil && err != unix.ENOENT {
		return false, fmt.Errorf("leadchat: cannot remove a reply: %w", err)
	}
	return true, nil
}

// outbox scans one lead's outbox. The fields other than root and role are
// for tests.
type outbox struct {
	root *Root
	role string
	now  func() time.Time

	afterRead    func(name string) // after a reply is read, before the second fstat
	beforeRemove func(name string) // handed to the batch
}

// ScanOutbox reads the outbox of the lead whose layout role is role, below
// the state root. See the package documentation for the rules. At most limit
// settled replies are read (none when limit is 0); the rest wait. now is the
// clock the settle and stale rules weigh the kernel's change times against.
func ScanOutbox(root *Root, role string, limit int, now time.Time) (*Batch, error) {
	o := &outbox{root: root, role: role, now: func() time.Time { return now }}
	return o.scan(limit)
}

// ClearOutbox removes every entry of the lead's outbox that is not a folder,
// whatever its name, and returns how many it removed. A missing outbox has
// nothing to clear. An outbox with more than 8192 entries is not cleared:
// the error wraps [ErrFull].
func ClearOutbox(root *Root, role string) (int, error) {
	o := &outbox{root: root, role: role, now: time.Now}
	return o.clear()
}

// open walks to the outbox and checks every folder on the way: a folder of
// the caller, not a link, not across a mount point, and not writable by
// group or other. A missing folder is reported with ok false.
func (o *outbox) open() (fd int, ok bool, err error) {
	if !profile.ValidName(o.role) {
		return -1, false, fmt.Errorf("%w: the role is not a layout name", ErrInvalid)
	}
	fd, err = o.root.walk(outboxPath(o.role)...)
	switch {
	case err == nil:
		return fd, true, nil
	case errors.Is(err, unix.ENOENT):
		return -1, false, nil
	case errors.Is(err, ErrInsecure):
		return -1, false, fmt.Errorf("the outbox: %w", err)
	}
	return -1, false, fmt.Errorf("leadchat: cannot open the outbox: %w", err)
}

// irregular reports a type that is removed on sight: anything but a regular
// file and a folder.
func irregular(mode uint32) bool {
	t := mode & unix.S_IFMT
	return t != unix.S_IFREG && t != unix.S_IFDIR
}

func (o *outbox) scan(limit int) (*Batch, error) {
	b := &Batch{fd: -1, Dropped: map[Reason]int{}, beforeRemove: o.beforeRemove}
	fd, ok, err := o.open()
	if err != nil {
		return nil, err
	}
	if !ok {
		b.Missing = true
		return b, nil
	}
	b.fd = fd
	names, more, err := readNames(fd, MaxDirEntries)
	if err != nil {
		_ = b.Close()
		return nil, err
	}
	if more {
		b.Dropped[ReasonOverflow]++
	}
	now := o.now()
	euid := uint32(unix.Geteuid())
	type candidate struct {
		name string
		st   unix.Stat_t
	}
	var cands []candidate
	waitingFolders := 0
	unlink := func(name string, why Reason) {
		if err := unix.Unlinkat(fd, name, 0); err == nil || err == unix.ENOENT {
			b.Dropped[why]++
		}
	}
	for _, name := range sortedCopy(names) {
		st, err := lstatAt(fd, name)
		if err != nil {
			continue // gone since the listing, or not to be examined: left
		}
		switch {
		case st.Mode&unix.S_IFMT == unix.S_IFDIR:
			b.Dropped[ReasonFolder]++
			if ValidReplyName(name) {
				waitingFolders++
			}
		case irregular(st.Mode):
			unlink(name, ReasonRefused)
		case strings.HasPrefix(name, "."):
			if now.Sub(ctimeOf(&st)) > StaleAge {
				unlink(name, ReasonStale)
			}
		case !ValidReplyName(name):
			unlink(name, ReasonName)
		case st.Uid != euid || st.Nlink != 1:
			unlink(name, ReasonRefused)
		case st.Size > MaxReplyBytes:
			unlink(name, ReasonTooLong)
		default:
			cands = append(cands, candidate{name, st})
		}
	}
	room := max(MaxWaiting-waitingFolders, 0)
	if len(cands) > room {
		for _, c := range cands[room:] {
			unlink(c.name, ReasonFull)
		}
		cands = cands[:room]
	}
	b.Waiting = waitingFolders
	for _, c := range cands {
		if len(b.Replies) >= limit || now.Sub(ctimeOf(&c.st)) < SettleAge {
			b.Waiting++
			continue
		}
		r, why, err := o.read(fd, c.name, &c.st, now)
		switch {
		case err != nil:
			_ = b.Close()
			return nil, err
		case why != "":
			unlink(c.name, why)
		case r == nil:
			b.Waiting++
		default:
			b.Replies = append(b.Replies, *r)
		}
	}
	return b, nil
}

// read opens and reads one candidate. It answers the reply, or a reason to
// remove the file, or neither when the file is to be left for a later scan
// (it changed, it is gone, or a lease holds it).
func (o *outbox) read(dirfd int, name string, seen *unix.Stat_t, now time.Time) (*Reply, Reason, error) {
	fd, err := openAt(o.root.openat2, dirfd, name, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOCTTY, 0)
	switch {
	case err == unix.ELOOP || err == unix.ENXIO || err == unix.EXDEV:
		return nil, ReasonRefused, nil
	case err != nil:
		return nil, "", nil
	}
	defer closeFD(fd)
	st, err := fstatFD(fd)
	if err != nil || st.Dev != seen.Dev || st.Ino != seen.Ino {
		return nil, "", nil
	}
	switch {
	case st.Mode&unix.S_IFMT != unix.S_IFREG, int(st.Uid) != unix.Geteuid(), st.Nlink != 1:
		return nil, ReasonRefused, nil
	case st.Size > MaxReplyBytes:
		return nil, ReasonTooLong, nil
	case now.Sub(ctimeOf(&st)) < SettleAge:
		return nil, "", nil
	}
	buf := make([]byte, MaxReplyBytes+1)
	n := 0
	for n < len(buf) {
		k, err := unix.Read(fd, buf[n:])
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return nil, "", nil
		}
		if k == 0 {
			break
		}
		n += k
	}
	if o.afterRead != nil {
		o.afterRead(name)
	}
	again, err := fstatFD(fd)
	if err != nil || !sameFile(&st, &again) || int64(n) != st.Size {
		return nil, "", nil // written while read: a later scan sees it settled
	}
	data := buf[:n]
	return &Reply{Name: name, Data: data, Src: replySrc(uint64(st.Ino), name, data), dev: uint64(st.Dev), ino: uint64(st.Ino)}, "", nil
}

// clear removes every entry that is not a folder.
func (o *outbox) clear() (int, error) {
	fd, ok, err := o.open()
	if err != nil || !ok {
		return 0, err
	}
	defer closeFD(fd)
	names, more, err := readNames(fd, maxClearEntries)
	if err != nil {
		return 0, err
	}
	if more {
		return 0, fmt.Errorf("%w: the outbox holds more than %d entries", ErrFull, maxClearEntries)
	}
	n := 0
	for _, name := range names {
		st, err := lstatAt(fd, name)
		if err == unix.ENOENT {
			continue
		}
		if err != nil {
			return n, fmt.Errorf("leadchat: cannot examine an entry of the outbox: %w", err)
		}
		if st.Mode&unix.S_IFMT == unix.S_IFDIR {
			continue
		}
		switch err := unix.Unlinkat(fd, name, 0); err {
		case nil:
			n++
		case unix.ENOENT:
		default:
			return n, fmt.Errorf("leadchat: cannot clear the outbox: %w", err)
		}
	}
	return n, nil
}
