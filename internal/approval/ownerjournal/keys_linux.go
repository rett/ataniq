package ownerjournal

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"ataniq/internal/approval/record"
)

// This file is the list of the owner's deciding keys, the credentials of the
// phones he paired: a file of its own beside the journal, in the same style
// and under the same trust checks. It is append-only and every append is on
// the disk before the call returns; the file is read, and every record of it
// checked, when it is opened, and the list is what that replay makes. A file
// that does not hold is refused and never repaired or started again, so a
// damaged file can never make the list empty.
//
// The file is lines of text. The first is a header naming the file's kind and
// version. Each later line is the hexadecimal SHA-256 digest of the line
// before it chained with this record (see keySum), a space, and one record in
// the canonical JSON form of package record: a number, a type (added, revoked
// or rejected), a time and the credential's display id, and for an added
// credential its id, its public key, its kind and the six words of its
// fingerprint. A rejected pairing is written with its display id and words
// only.
//
// The digests find damage: a changed, missing, reordered or half-written
// record. They are not a signature: whoever can write the file can write
// digests that hold, and records lost from the end of the file leave a
// shorter list that still holds, like the journal's. What keeps the file from
// being written is the folder's mode, and in the design an owner who accepts
// one privileged step makes it root's and read-only to the daemon.

// The errors of the key list that are not those of the journal's store, found
// with errors.Is. [ErrLocked], [ErrNotPrivate], [ErrDamaged], [ErrClosed] and
// [ErrUncertain] are the store's, with the same meaning.
var (
	// ErrKeysFull says the list holds [MaxKeys] credentials, revoked ones
	// included, or the file holds [MaxKeyRecords] records.
	ErrKeysFull = errors.New("ownerjournal: the list of keys is full")
	// ErrKeyExists says that a credential with this id is on the list.
	ErrKeyExists = errors.New("ownerjournal: the credential is on the list already")
	// ErrKeyUnknown says that no credential on the list has this display id.
	ErrKeyUnknown = errors.New("ownerjournal: no credential has this id")
)

const (
	// KeysFileName is the name the daemon gives the file of the list.
	KeysFileName = "keys.log"
	// MaxKeys is the most credentials the list holds, revoked ones
	// included (they stay listed). ownerwire.MaxKeys is the same number.
	MaxKeys = 64
	// MaxKeyRecords is the most records the file holds.
	MaxKeyRecords = 4096
	// maxKeysFileBytes is the most the file holds: far over what the
	// records' own bounds make, so a larger file is not one of ours.
	maxKeysFileBytes = 4 << 20

	// The bounds of what a record holds.
	minCredentialIDLen = 16
	maxCredentialIDLen = 1024
	maxPublicKeyLen    = 512

	keysHeader = "ataniq-ownerkeys 1"
	// DisplayIDLength is the length of a display id.
	DisplayIDLength = 16
	keysNewSuffix   = ".new"
)

// The types of a record of the file.
const (
	keyAdded    = "added"
	keyRevoked  = "revoked"
	keyRejected = "rejected"
)

// keyRecord is one line of the file after its digest. Every field has its
// own use for the types that use it, and the others are absent.
type keyRecord struct {
	Seq          int64  `json:"seq"`
	Type         string `json:"type"`
	At           string `json:"at"`
	ID           string `json:"id"`
	Kind         string `json:"kind,omitempty"`
	CredentialID []byte `json:"credential_id,omitempty"`
	PublicKey    []byte `json:"public_key,omitempty"`
	Words        string `json:"words,omitempty"`
}

// StoredKey is one credential of the list.
type StoredKey struct {
	// ID is the display id: [KeyDisplayID] of CredentialID.
	ID string
	// CredentialID and PublicKey are as the phone made them.
	CredentialID []byte
	PublicKey    []byte
	// Kind is what kind of authenticator the key is known to be.
	Kind string
	// PairedAt is when the owner confirmed it.
	PairedAt time.Time
	// Words are the six words of its fingerprint, as the desk showed them,
	// one space between.
	Words string
	// Revoked says the credential was revoked, and RevokedAt when. A
	// revoked credential is never made good again.
	Revoked   bool
	RevokedAt time.Time
}

// NewKey is a credential to put on the list.
type NewKey struct {
	CredentialID []byte
	PublicKey    []byte
	// Kind is 1 to 32 lower-case letters, digits and dashes, starting with
	// a letter.
	Kind     string
	PairedAt time.Time
	// Words are the credential's fingerprint words, six, one space between.
	Words string
}

// RejectedPairing is the journal's record of a pairing the owner rejected: the
// credential's display id and its fingerprint words, and no key.
type RejectedPairing struct {
	ID    string
	Words string
	At    time.Time
}

// KeyDisplayID is the name the owner sees for a credential: the first 8 bytes
// of the SHA-256 of the credential id, as 16 lower-case hexadecimal digits. It
// holds nothing of the key.
func KeyDisplayID(credentialID []byte) string {
	sum := sha256.Sum256(credentialID)
	return hex.EncodeToString(sum[:8])
}

// KeyFile is the open list. Make one with [OpenKeys] and end it with
// [KeyFile.Close]. Its methods may be called from several goroutines; they run
// one after the other.
type KeyFile struct {
	mu       sync.Mutex
	path     string
	dir      *os.File
	file     *os.File
	identity os.FileInfo
	closed   bool
	// broken is set when a write failed in a way that leaves it open whether
	// the record is in the file: every later call is refused with
	// [ErrUncertain] until the file is opened again.
	broken bool

	size int64
	prev [sha256.Size]byte
	list keyList
}

// keyList is what the records make: the credentials, and how many records and
// rejected pairings there are. It is a value that can be copied to try a
// record on before it is written.
type keyList struct {
	records int
	keys    []StoredKey
	rejects int
}

// clone returns a copy that can be changed without changing the list.
func (l keyList) clone() keyList {
	l.keys = append([]StoredKey(nil), l.keys...)
	return l
}

// OpenKeys opens the list in the file at path, and makes a new, empty list
// when there is no such file. The path must be absolute and clean, and the
// last part of it and of its folder must not be a symbolic link. The folder
// must be the user's own with mode 0700, the file a plain file of the user
// with mode 0600 and one name ([ErrNotPrivate]); a file that is open elsewhere
// is refused at once ([ErrLocked]); a file that is empty, cut short, of another
// kind, or with a record that is changed, missing, out of order or against the
// rules of the list is refused with [ErrDamaged] and left as it is.
func OpenKeys(path string) (_ *KeyFile, err error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsRune(path, 0) {
		return nil, errors.New("ownerjournal: the path must be absolute and clean")
	}
	k := &KeyFile{path: path}
	defer func() {
		if err != nil {
			err = errors.Join(err, k.release())
		}
	}()
	if err = k.openDir(); err != nil {
		return nil, err
	}
	base := filepath.Base(path)
	for attempt := 0; attempt < 3 && k.file == nil; attempt++ {
		fd, e := unix.Openat(int(k.dir.Fd()), base, unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
		switch {
		case errors.Is(e, unix.ENOENT):
			f, e := k.create()
			if errors.Is(e, errRetry) {
				continue
			}
			if e != nil {
				return nil, e
			}
			k.file = f
		case errors.Is(e, unix.ELOOP):
			return nil, fmt.Errorf("%w: the file is a symbolic link", ErrNotPrivate)
		case e != nil:
			return nil, fmt.Errorf("ownerjournal: the file of keys: %w", e)
		default:
			f := os.NewFile(uintptr(fd), path)
			if e := k.lockExisting(f); e != nil {
				return nil, errors.Join(e, f.Close())
			}
			k.file = f
		}
	}
	if k.file == nil {
		return nil, errRetry
	}
	if k.identity, err = k.file.Stat(); err != nil {
		return nil, err
	}
	if err = k.checkPath(); err != nil {
		return nil, err
	}
	if err = k.replay(); err != nil {
		return nil, err
	}
	return k, nil
}

func (k *KeyFile) openDir() error {
	name := filepath.Dir(k.path)
	fd, err := unix.Open(name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("ownerjournal: the folder of the keys: %w", err)
	}
	k.dir = os.NewFile(uintptr(fd), name)
	info, err := k.dir.Stat()
	if err != nil {
		return err
	}
	if !info.IsDir() || !private(info) || info.Mode().Perm()&0o700 != 0o700 {
		return fmt.Errorf("%w: the folder must be the user's own, with mode 0700", ErrNotPrivate)
	}
	return nil
}

// lockExisting checks a file that was there and takes its lock.
func (k *KeyFile) lockExisting(f *os.File) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || !private(info) || info.Mode().Perm() != 0o600 {
		return fmt.Errorf("%w: the file must be a plain file of the user with mode 0600", ErrNotPrivate)
	}
	if err := flock(f); err != nil {
		return err
	}
	if info, err = f.Stat(); err != nil {
		return err
	}
	if info.Sys().(*syscall.Stat_t).Nlink != 1 {
		return fmt.Errorf("%w: the file has a second name", ErrNotPrivate)
	}
	if info.Size() == 0 {
		return fmt.Errorf("%w: the file is empty", ErrDamaged)
	}
	return nil
}

// create makes a new list: the header is written under another name, synced,
// and the file gets its real name in one step that fails when a file of that
// name is there, so the file at the path is always a whole list.
func (k *KeyFile) create() (_ *os.File, err error) {
	base := filepath.Base(k.path)
	dirfd := int(k.dir.Fd())
	fd, err := unix.Openat(dirfd, base+keysNewSuffix, unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0o600)
	if errors.Is(err, unix.ELOOP) {
		return nil, fmt.Errorf("%w: %s is a symbolic link", ErrNotPrivate, base+keysNewSuffix)
	}
	if err != nil {
		return nil, fmt.Errorf("ownerjournal: the new file of keys: %w", err)
	}
	f := os.NewFile(uintptr(fd), k.path+keysNewSuffix)
	mine := false
	defer func() {
		if err == nil {
			return
		}
		if mine {
			os.Remove(k.path + keysNewSuffix)
		}
		f.Close()
	}()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) {
		return nil, fmt.Errorf("%w: %s is not a plain file of the user", ErrNotPrivate, base+keysNewSuffix)
	}
	if err = flock(f); err != nil {
		return nil, err
	}
	named, e := os.Lstat(k.path + keysNewSuffix)
	if e != nil || !os.SameFile(named, info) {
		return nil, errRetry
	}
	if info, err = f.Stat(); err != nil {
		return nil, err
	}
	if info.Sys().(*syscall.Stat_t).Nlink != 1 {
		return nil, fmt.Errorf("%w: %s has a second name", ErrNotPrivate, base+keysNewSuffix)
	}
	mine = true
	if _, e := os.Lstat(k.path); e == nil {
		return nil, errRetry
	} else if !errors.Is(e, os.ErrNotExist) {
		return nil, e
	}
	if err = f.Chmod(0o600); err != nil {
		return nil, err
	}
	if err = f.Truncate(0); err != nil {
		return nil, err
	}
	if _, err = f.WriteAt([]byte(keysHeader+"\n"), 0); err != nil {
		return nil, err
	}
	if err = f.Sync(); err != nil {
		return nil, err
	}
	err = unix.Renameat2(dirfd, base+keysNewSuffix, dirfd, base, unix.RENAME_NOREPLACE)
	if errors.Is(err, unix.EEXIST) {
		return nil, errRetry
	}
	if err != nil {
		return nil, fmt.Errorf("ownerjournal: naming the new file of keys: %w", err)
	}
	mine = false
	if err = k.dir.Sync(); err != nil {
		return nil, err
	}
	return f, nil
}

// checkPath checks that the path still names the file that was locked, in the
// folder that was opened, and that both are still private.
func (k *KeyFile) checkPath() error {
	info, err := os.Lstat(k.path)
	if err != nil {
		return err
	}
	if k.identity != nil && !os.SameFile(info, k.identity) {
		return errors.New("ownerjournal: the path names another file than the one that was locked")
	}
	if !privateFile(info) {
		return fmt.Errorf("%w: the file must be a plain file of the user with mode 0600 and one name", ErrNotPrivate)
	}
	dir, err := os.Lstat(filepath.Dir(k.path))
	if err != nil {
		return err
	}
	held, err := k.dir.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(dir, held) || !private(dir) {
		return fmt.Errorf("%w: the folder changed", ErrNotPrivate)
	}
	return nil
}

// keySum is the digest of a record: SHA-256 over the digest of the line before
// it and the record's bytes.
func keySum(prev [sha256.Size]byte, rec []byte) [sha256.Size]byte {
	h := sha256.New()
	h.Write(prev[:])
	h.Write(rec)
	var out [sha256.Size]byte
	copy(out[:], h.Sum(nil))
	return out
}

func keysDamaged(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrDamaged, fmt.Sprintf(format, a...))
}

// keyReader is the strict reader of a record: the canonical form of package
// record, which refuses a repeated key, a float, a null, a key the form does
// not name and anything after the object.
var keyReader = record.Reader{MaxDepth: 2, Noun: "record"}

// replay reads the whole file, checks each record against the digest chain and
// the rules of the list, and makes the list.
func (k *KeyFile) replay() error {
	info, err := k.file.Stat()
	if err != nil {
		return err
	}
	if info.Size() > maxKeysFileBytes {
		return keysDamaged("the file is larger than %d bytes", maxKeysFileBytes)
	}
	data, err := io.ReadAll(io.NewSectionReader(k.file, 0, info.Size()))
	if err != nil {
		return err
	}
	if int64(len(data)) != info.Size() {
		return keysDamaged("the file changed while it was read")
	}
	if len(data) == 0 || data[len(data)-1] != '\n' {
		return keysDamaged("the file does not end with a whole line")
	}
	lines := bytes.Split(data[:len(data)-1], []byte{'\n'})
	if string(lines[0]) != keysHeader {
		return keysDamaged("the file is not a list of keys of this version")
	}
	k.prev = sha256.Sum256(lines[0])
	k.list = keyList{}
	for i, line := range lines[1:] {
		seq := int64(i + 1)
		sum, rec, ok := bytes.Cut(line, []byte{' '})
		if !ok || len(sum) != 2*sha256.Size {
			return keysDamaged("record %d has no digest", seq)
		}
		want := keySum(k.prev, rec)
		if !bytes.Equal(sum, []byte(hex.EncodeToString(want[:]))) {
			return keysDamaged("record %d does not follow the one before it", seq)
		}
		var r keyRecord
		if err := keyReader.Unmarshal(rec, "record", &r); err != nil {
			return keysDamaged("record %d is not well formed", seq)
		}
		if err := k.list.apply(&r, seq); err != nil {
			return err
		}
		k.prev = want
		k.list.records++
	}
	k.size = info.Size()
	return nil
}

func validKind(s string) bool {
	if len(s) < 1 || len(s) > 32 {
		return false
	}
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c >= 'a' && c <= 'z':
		case i > 0 && (c >= '0' && c <= '9' || c == '-'):
		default:
			return false
		}
	}
	return true
}

// validWords reports whether s is six words of 3 to 6 lower-case letters with
// one space between.
func validWords(s string) bool {
	words := strings.Split(s, " ")
	if len(words) != 6 {
		return false
	}
	for _, w := range words {
		if len(w) < 3 || len(w) > 6 {
			return false
		}
		for i := 0; i < len(w); i++ {
			if w[i] < 'a' || w[i] > 'z' {
				return false
			}
		}
	}
	return true
}

func (l *keyList) find(id string) int {
	for i := range l.keys {
		if l.keys[i].ID == id {
			return i
		}
	}
	return -1
}

// apply checks one record, which has the number seq, against the rules of the
// list and moves the list by it. The caller counts the record after.
func (l *keyList) apply(r *keyRecord, seq int64) error {
	if r.Seq != seq {
		return keysDamaged("record %d is numbered %d", seq, r.Seq)
	}
	at, err := record.ParseTime(r.At)
	if err != nil {
		return keysDamaged("record %d has no time", seq)
	}
	if !record.IsLowerHex(r.ID, DisplayIDLength) {
		return keysDamaged("record %d has no display id", seq)
	}
	if l.records+1 > MaxKeyRecords {
		return keysDamaged("the file has more than %d records", MaxKeyRecords)
	}
	switch r.Type {
	case keyAdded:
		if len(r.CredentialID) < minCredentialIDLen || len(r.CredentialID) > maxCredentialIDLen ||
			len(r.PublicKey) < 1 || len(r.PublicKey) > maxPublicKeyLen ||
			!validKind(r.Kind) || !validWords(r.Words) {
			return keysDamaged("record %d is not a credential", seq)
		}
		if KeyDisplayID(r.CredentialID) != r.ID {
			return keysDamaged("record %d has a display id that is not its credential's", seq)
		}
		if l.find(r.ID) >= 0 {
			return keysDamaged("record %d adds a credential that is on the list", seq)
		}
		if len(l.keys) >= MaxKeys {
			return keysDamaged("the list has more than %d credentials", MaxKeys)
		}
		l.keys = append(l.keys, StoredKey{
			ID: r.ID, CredentialID: r.CredentialID, PublicKey: r.PublicKey,
			Kind: r.Kind, PairedAt: at, Words: r.Words,
		})
	case keyRevoked:
		if r.Kind != "" || len(r.CredentialID) != 0 || len(r.PublicKey) != 0 || r.Words != "" {
			return keysDamaged("record %d revokes with more than an id", seq)
		}
		i := l.find(r.ID)
		if i < 0 || l.keys[i].Revoked {
			return keysDamaged("record %d revokes a credential that is not on the list or revoked already", seq)
		}
		l.keys[i].Revoked, l.keys[i].RevokedAt = true, at
	case keyRejected:
		if r.Kind != "" || len(r.CredentialID) != 0 || len(r.PublicKey) != 0 || !validWords(r.Words) {
			return keysDamaged("record %d is not a rejected pairing", seq)
		}
		l.rejects++
	default:
		return keysDamaged("record %d is of a type this version does not know", seq)
	}
	return nil
}

// usable returns the error that refuses a call, or nil. The caller holds mu.
func (k *KeyFile) usable() error {
	switch {
	case k.closed:
		return ErrClosed
	case k.broken:
		return ErrUncertain
	}
	return k.checkPath()
}

// Keys returns the list, in the order the credentials were added. Revoked
// credentials are in it, marked. The slices are copies.
func (k *KeyFile) Keys() ([]StoredKey, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if err := k.usable(); err != nil {
		return nil, err
	}
	out := make([]StoredKey, len(k.list.keys))
	for i, s := range k.list.keys {
		s.CredentialID = append([]byte(nil), s.CredentialID...)
		s.PublicKey = append([]byte(nil), s.PublicKey...)
		out[i] = s
	}
	return out, nil
}

// encodeKeyRecord writes a record as a line: its digest, a space, the record and a line
// feed, given the digest of the line before it.
func encodeKeyRecord(prev [sha256.Size]byte, r keyRecord) ([]byte, [sha256.Size]byte, error) {
	rec, err := record.Marshal(&r, "record")
	if err != nil {
		return nil, prev, err
	}
	sum := keySum(prev, rec)
	line := make([]byte, 0, 2*sha256.Size+2+len(rec))
	line = append(line, hex.EncodeToString(sum[:])...)
	line = append(line, ' ')
	line = append(line, rec...)
	line = append(line, '\n')
	return line, sum, nil
}

// write appends the records, which must already pass [KeyFile.apply] in order,
// in one write that is on the disk before it returns, and moves the list. The
// caller holds mu. A failure of the write or of the sync leaves it open whether
// the records are in the file: the list refuses every later call.
func (k *KeyFile) write(rs []keyRecord) error {
	info, err := k.file.Stat()
	if err != nil {
		k.broken = true
		return errors.Join(ErrUncertain, err)
	}
	if info.Size() != k.size {
		k.broken = true
		return fmt.Errorf("%w: the file is not the size it was", ErrUncertain)
	}
	var buf []byte
	prev := k.prev
	for i := range rs {
		line, sum, err := encodeKeyRecord(prev, rs[i])
		if err != nil {
			return err
		}
		buf, prev = append(buf, line...), sum
	}
	if _, err := k.file.WriteAt(buf, k.size); err != nil {
		k.broken = true
		return errors.Join(ErrUncertain, err)
	}
	if err := k.file.Sync(); err != nil {
		k.broken = true
		return errors.Join(ErrUncertain, err)
	}
	k.size += int64(len(buf))
	k.prev = prev
	return nil
}

// stamp makes the record of a type at a time, with the next number.
func (k *KeyFile) stamp(typ string, at time.Time, id string, n int) keyRecord {
	return keyRecord{Seq: int64(k.list.records + n + 1), Type: typ, At: record.FormatTime(at), ID: id}
}

// commit applies records to the list as a check, before they are written: it
// works on a copy, so a record the rules refuse leaves the list as it was, and
// when the write is done it takes the copy.
func (k *KeyFile) commit(rs []keyRecord) error {
	probe := k.list.clone()
	for i := range rs {
		if err := probe.apply(&rs[i], rs[i].Seq); err != nil {
			return errors.New("ownerjournal: the record is against the rules of the list")
		}
		probe.records++
	}
	if err := k.write(rs); err != nil {
		return err
	}
	k.list = probe
	return nil
}

// AddKey puts a credential on the list and returns it. It refuses a credential
// that is on the list already ([ErrKeyExists]) and a full list ([ErrKeysFull]),
// and one whose id, key, kind or words are not of the form a record holds. The
// record is on the disk when it returns.
func (k *KeyFile) AddKey(n NewKey) (StoredKey, error) {
	if len(n.CredentialID) < minCredentialIDLen || len(n.CredentialID) > maxCredentialIDLen ||
		len(n.PublicKey) < 1 || len(n.PublicKey) > maxPublicKeyLen ||
		!validKind(n.Kind) || !validWords(n.Words) || !validTime(n.PairedAt) {
		return StoredKey{}, errors.New("ownerjournal: the credential is not one a record can hold")
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if err := k.usable(); err != nil {
		return StoredKey{}, err
	}
	id := KeyDisplayID(n.CredentialID)
	switch {
	case k.list.find(id) >= 0:
		return StoredKey{}, ErrKeyExists
	case len(k.list.keys) >= MaxKeys || k.list.records >= MaxKeyRecords:
		return StoredKey{}, ErrKeysFull
	}
	r := k.stamp(keyAdded, n.PairedAt, id, 0)
	r.Kind, r.Words = n.Kind, n.Words
	r.CredentialID = append([]byte(nil), n.CredentialID...)
	r.PublicKey = append([]byte(nil), n.PublicKey...)
	if err := k.commit([]keyRecord{r}); err != nil {
		return StoredKey{}, err
	}
	s := k.list.keys[len(k.list.keys)-1]
	s.CredentialID = append([]byte(nil), s.CredentialID...)
	s.PublicKey = append([]byte(nil), s.PublicKey...)
	return s, nil
}

// RevokeKeys revokes the credentials with these display ids, all in one write.
// A credential that is revoked already is left as it is. An id that no
// credential has refuses the whole call with [ErrKeyUnknown], and nothing is
// written. A revoked credential is never made good again: there is no way to.
func (k *KeyFile) RevokeKeys(ids []string, at time.Time) error {
	if !validTime(at) {
		return errNoTime
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if err := k.usable(); err != nil {
		return err
	}
	var rs []keyRecord
	seen := map[string]bool{}
	for _, id := range ids {
		i := k.list.find(id)
		if i < 0 {
			return ErrKeyUnknown
		}
		if k.list.keys[i].Revoked || seen[id] {
			continue
		}
		seen[id] = true
		rs = append(rs, k.stamp(keyRevoked, at, id, len(rs)))
	}
	if len(rs) == 0 {
		return nil
	}
	if k.list.records+len(rs) > MaxKeyRecords {
		// A revocation is never refused for want of room in the file: the
		// room is kept for it by the cap on rejected pairings.
		return ErrKeysFull
	}
	return k.commit(rs)
}

// maxRejected is how many rejected pairings the file keeps room for; the rest
// of the record cap is for credentials and their revocations.
const maxRejected = MaxKeyRecords - 2*MaxKeys

// AppendRejected journals a pairing the owner rejected, with the credential's
// display id and fingerprint words and no key. It refuses with [ErrKeysFull]
// once the file holds as many rejected pairings as it keeps room for.
func (k *KeyFile) AppendRejected(r RejectedPairing) error {
	if !record.IsLowerHex(r.ID, DisplayIDLength) || !validWords(r.Words) || !validTime(r.At) {
		return errors.New("ownerjournal: the rejected pairing is not one a record can hold")
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if err := k.usable(); err != nil {
		return err
	}
	if k.list.rejects >= maxRejected {
		return ErrKeysFull
	}
	rec := k.stamp(keyRejected, r.At, r.ID, 0)
	rec.Words = r.Words
	return k.commit([]keyRecord{rec})
}

// release closes the descriptors, the file first so its lock goes with it.
func (k *KeyFile) release() error {
	var err error
	if k.file != nil {
		err = k.file.Close()
	}
	if k.dir != nil {
		err = errors.Join(err, k.dir.Close())
	}
	return err
}

// Close ends the hold on the file. Calling it again returns what the first
// call returned. Every other method of a closed list returns [ErrClosed].
func (k *KeyFile) Close() error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.closed {
		return nil
	}
	k.closed = true
	return k.release()
}
