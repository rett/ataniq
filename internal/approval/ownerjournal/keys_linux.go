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
// the canonical JSON form of package record: a number, a type (added, revoked,
// rejected or counter), a time and the credential's display id, and for an
// added credential its id, its public key, its kind and the eight words of its
// fingerprint, and what its authenticator claimed at the pairing: its AAGUID,
// its backup eligibility and backup state, and its sign counter. A rejected
// pairing is written with its display id and words only, and a counter record
// with the display id and the new highest sign counter only.
//
// The claims of an added credential are optional: a record written before
// they were kept has none, and reads as a credential with no AAGUID, neither
// backup flag and a counter of 0, of the kind it was written with
// ("unverified" for every pairing before the kinds), with its revocations.
// Nothing is migrated: such a credential cannot log in or decide, and is paired
// again.
//
// The digests find damage: a changed, missing, reordered or half-written
// record. They are not a signature: whoever can write the file can write
// digests that hold. Two things beside them keep the chain honest. While the
// list is open it is read again and checked whole before every append, and by
// Keys when the file's size, times or inode are not what they were when it was
// last checked, so damage in place, of the same length, is found and the list
// is marked damaged and refuses everything. And a second small file in the
// same folder, keys.head ([KeysHeadFileName]), keeps the head of the chain:
// the number of records and the digest of the last, written crash-consistently
// (a temporary name, synced, renamed over, the folder synced) after each
// record is appended and synced. At open a file with fewer records than the
// head, or whose digest at the head's count is not the head's, is damaged
// ("suffix lost"), so records lost from the end, a revocation among them, are
// found. A file ahead of the head by records that chain from it is the crash
// between the two writes (one record, or the several of one revocation of many
// credentials): it is accepted and the head is made right. The head has the
// trust checks of the list. What remains: whoever can write both files can
// write both consistently, and a head that is lost is not made again for a
// list with records. What keeps the files from being written is the folder's
// mode, and in the design an owner who accepts one privileged step makes it
// root's and read-only to the daemon.

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
	// FingerprintWords is the number of words of a credential's fingerprint
	// that a record holds. It is this package's own copy of
	// seatpair.FingerprintWords (this package imports only record), and a
	// test of the service holds the two equal. A list written when a
	// fingerprint was shorter is not read: nothing was paired in production
	// before the change, so there is no migration.
	FingerprintWords = 8
	// MaxKeyRecords is the most records the file holds.
	MaxKeyRecords = 4096
	// maxKeysFileBytes is the most the file holds: far over what the
	// records' own bounds make, so a larger file is not one of ours.
	maxKeysFileBytes = 4 << 20

	// The bounds of what a record holds. A credential id is at most 1023
	// bytes, as WebAuthn level 3, section 4, says (the wire's
	// MaxCredentialIDBytes; a test of the service holds the two equal).
	minCredentialIDLen = 16
	maxCredentialIDLen = 1023
	maxPublicKeyLen    = 512

	keysHeader = "ataniq-ownerkeys 1"
	// keysHeadHeader starts the line of the head file.
	keysHeadHeader = "ataniq-ownerkeys-head 1"
	// maxKeysHeadBytes is the most the head file holds.
	maxKeysHeadBytes = 256
	// KeysHeadFileName is the name of the head file the list keeps beside its
	// own, when the list is KeysFileName: the list's name without ".log"
	// and ".head". Another name for the list gives its name and ".head".
	KeysHeadFileName = "keys.head"
	// DisplayIDLength is the length of a display id.
	DisplayIDLength = 16
	keysNewSuffix   = ".new"
)

// The types of a record of the file.
const (
	keyAdded    = "added"
	keyRevoked  = "revoked"
	keyRejected = "rejected"
	keyCounter  = "counter"
)

// maxCounter is the largest sign counter: WebAuthn's counter is 32 bits.
const maxCounter = 1<<32 - 1

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
	// AAGUID, BackupEligible and BackupState are an added credential's
	// claims at the pairing, the AAGUID as 32 lower-case hexadecimal digits
	// and left out when it is all zero. Counter is its sign counter at the
	// pairing, for an added credential, and the new highest sign counter, for
	// a counter record.
	AAGUID         string `json:"aaguid,omitempty"`
	BackupEligible bool   `json:"backup_eligible,omitempty"`
	BackupState    bool   `json:"backup_state,omitempty"`
	Counter        int64  `json:"counter,omitempty"`
}

// StoredKey is one credential of the list.
type StoredKey struct {
	// ID is the display id: [KeyDisplayID] of CredentialID.
	ID string
	// CredentialID and PublicKey are as the phone made them.
	CredentialID []byte
	PublicKey    []byte
	// Kind is the kind of the credential, a claim: the kind its
	// authenticator's AAGUID claims (package credkind's name), or
	// "unverified" for a credential paired before kinds were recorded.
	Kind string
	// AAGUID is the authenticator's claimed model, all zero when it claimed
	// none or the record holds none.
	AAGUID [16]byte
	// BackupEligible and BackupState are the BE and BS flags the
	// authenticator claimed at the pairing. BackupState is never set without
	// BackupEligible.
	BackupEligible bool
	BackupState    bool
	// Counter is the highest sign counter seen: the pairing's, raised by
	// [KeyFile.RaiseCounter] and never lowered.
	Counter uint32
	// PairedAt is when the owner confirmed it.
	PairedAt time.Time
	// Words are the eight words of its fingerprint, as the desk showed them,
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
	Kind string
	// AAGUID, BackupEligible, BackupState and Counter are what the
	// authenticator claimed at the pairing (see [StoredKey]). BackupState
	// without BackupEligible is refused.
	AAGUID         [16]byte
	BackupEligible bool
	BackupState    bool
	Counter        uint32
	PairedAt       time.Time
	// Words are the credential's fingerprint words, eight, one space between.
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
	// closeErr is what the first Close returned.
	closeErr error

	// damaged is set when a check found the file or the head not what the list
	// holds; every later call is refused with [ErrDamaged].
	damaged error

	size int64
	prev [sha256.Size]byte
	list keyList
	// digests are the digests of the chain as the file had them at open; they
	// are for the check of the head, and are not kept up.
	digests [][sha256.Size]byte
	// stamp and headStamp are what the stat of the list and of the head
	// showed when each was last checked.
	fstamp, hstamp fileStamp
}

// fileStamp is what a file's stat shows that a change in place moves: its
// size, its modification and status-change times, and its inode.
type fileStamp struct {
	size       int64
	mtime, ctm int64
	dev, ino   uint64
}

func stampOf(info os.FileInfo) fileStamp {
	st := info.Sys().(*syscall.Stat_t)
	return fileStamp{size: info.Size(), mtime: st.Mtim.Nano(), ctm: st.Ctim.Nano(), dev: uint64(st.Dev), ino: st.Ino}
}

// keyList is what the records make: the credentials, and how many records,
// rejected pairings and counter records there are. It is a value that can be
// copied to try a record on before it is written.
type keyList struct {
	records  int
	keys     []StoredKey
	rejects  int
	counters int
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
	if err = k.checkHeadAtOpen(); err != nil {
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

// parseKeys checks data, the whole file, against the digest chain and the rules
// of the list, and returns the list it makes and the digests of the chain: the
// first is that of the header, and digest i is that of the line after i
// records.
func parseKeys(data []byte) (keyList, [][sha256.Size]byte, error) {
	if len(data) > maxKeysFileBytes {
		return keyList{}, nil, keysDamaged("the file is larger than %d bytes", maxKeysFileBytes)
	}
	if len(data) == 0 || data[len(data)-1] != '\n' {
		return keyList{}, nil, keysDamaged("the file does not end with a whole line")
	}
	lines := bytes.Split(data[:len(data)-1], []byte{'\n'})
	if string(lines[0]) != keysHeader {
		return keyList{}, nil, keysDamaged("the file is not a list of keys of this version")
	}
	var list keyList
	prev := sha256.Sum256(lines[0])
	digests := [][sha256.Size]byte{prev}
	for i, line := range lines[1:] {
		seq := int64(i + 1)
		sum, rec, ok := bytes.Cut(line, []byte{' '})
		if !ok || len(sum) != 2*sha256.Size {
			return keyList{}, nil, keysDamaged("record %d has no digest", seq)
		}
		want := keySum(prev, rec)
		if !bytes.Equal(sum, []byte(hex.EncodeToString(want[:]))) {
			return keyList{}, nil, keysDamaged("record %d does not follow the one before it", seq)
		}
		var r keyRecord
		if err := keyReader.Unmarshal(rec, "record", &r); err != nil {
			return keyList{}, nil, keysDamaged("record %d is not well formed", seq)
		}
		if err := list.apply(&r, seq); err != nil {
			return keyList{}, nil, err
		}
		prev = want
		list.records++
		digests = append(digests, prev)
	}
	return list, digests, nil
}

// readKeys reads the whole file from the descriptor that was locked, and
// returns what it holds with the stat it was read under.
func (k *KeyFile) readKeys() (keyList, [][sha256.Size]byte, fileStamp, error) {
	info, err := k.file.Stat()
	if err != nil {
		return keyList{}, nil, fileStamp{}, err
	}
	if info.Size() > maxKeysFileBytes {
		return keyList{}, nil, fileStamp{}, keysDamaged("the file is larger than %d bytes", maxKeysFileBytes)
	}
	data, err := io.ReadAll(io.NewSectionReader(k.file, 0, info.Size()))
	if err != nil {
		return keyList{}, nil, fileStamp{}, err
	}
	if int64(len(data)) != info.Size() {
		return keyList{}, nil, fileStamp{}, keysDamaged("the file changed while it was read")
	}
	list, digests, err := parseKeys(data)
	if err != nil {
		return keyList{}, nil, fileStamp{}, err
	}
	return list, digests, stampOf(info), nil
}

// replay reads the whole file, checks each record against the digest chain and
// the rules of the list, and makes the list.
func (k *KeyFile) replay() error {
	list, digests, stamp, err := k.readKeys()
	if err != nil {
		return err
	}
	k.list, k.prev, k.size, k.fstamp = list, digests[len(digests)-1], stamp.size, stamp
	k.digests = digests
	return nil
}

// hasClaims reports whether a record holds any of an added credential's
// claims.
func (r *keyRecord) hasClaims() bool {
	return r.AAGUID != "" || r.BackupEligible || r.BackupState || r.Counter != 0
}

// parseAAGUID reads the AAGUID of a record: absent, which is all zero, or 32
// lower-case hexadecimal digits that are not all zero (an all-zero AAGUID is
// written by being left out, so that each AAGUID has one form).
func parseAAGUID(s string) ([16]byte, bool) {
	var id [16]byte
	if s == "" {
		return id, true
	}
	if !record.IsLowerHex(s, 32) {
		return id, false
	}
	if _, err := hex.Decode(id[:], []byte(s)); err != nil || id == ([16]byte{}) {
		return id, false
	}
	return id, true
}

// formatAAGUID is the form of an AAGUID in a record: "" when it is all zero.
func formatAAGUID(id [16]byte) string {
	if id == ([16]byte{}) {
		return ""
	}
	return hex.EncodeToString(id[:])
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

// validWords reports whether s is [FingerprintWords] words of 3 to 6
// lower-case letters with one space between.
func validWords(s string) bool {
	words := strings.Split(s, " ")
	if len(words) != FingerprintWords {
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
		aaguid, aaguidOK := parseAAGUID(r.AAGUID)
		if len(r.CredentialID) < minCredentialIDLen || len(r.CredentialID) > maxCredentialIDLen ||
			len(r.PublicKey) < 1 || len(r.PublicKey) > maxPublicKeyLen ||
			!validKind(r.Kind) || !validWords(r.Words) || !aaguidOK ||
			(r.BackupState && !r.BackupEligible) || r.Counter < 0 || r.Counter > maxCounter {
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
			Kind: r.Kind, AAGUID: aaguid, BackupEligible: r.BackupEligible, BackupState: r.BackupState,
			Counter: uint32(r.Counter), PairedAt: at, Words: r.Words,
		})
	case keyRevoked:
		if r.Kind != "" || len(r.CredentialID) != 0 || len(r.PublicKey) != 0 || r.Words != "" || r.hasClaims() {
			return keysDamaged("record %d revokes with more than an id", seq)
		}
		i := l.find(r.ID)
		if i < 0 || l.keys[i].Revoked {
			return keysDamaged("record %d revokes a credential that is not on the list or revoked already", seq)
		}
		l.keys[i].Revoked, l.keys[i].RevokedAt = true, at
	case keyRejected:
		if r.Kind != "" || len(r.CredentialID) != 0 || len(r.PublicKey) != 0 || !validWords(r.Words) || r.hasClaims() {
			return keysDamaged("record %d is not a rejected pairing", seq)
		}
		if l.rejects+l.counters >= maxSpare {
			return keysDamaged("the file has more than %d rejected pairings and counter records", maxSpare)
		}
		l.rejects++
	case keyCounter:
		if r.Kind != "" || len(r.CredentialID) != 0 || len(r.PublicKey) != 0 || r.Words != "" ||
			r.AAGUID != "" || r.BackupEligible || r.BackupState || r.Counter < 1 || r.Counter > maxCounter {
			return keysDamaged("record %d is not a counter record", seq)
		}
		i := l.find(r.ID)
		if i < 0 || l.keys[i].Revoked || r.Counter <= int64(l.keys[i].Counter) {
			return keysDamaged("record %d raises no counter of a credential on the list", seq)
		}
		if l.rejects+l.counters >= maxSpare {
			return keysDamaged("the file has more than %d rejected pairings and counter records", maxSpare)
		}
		l.keys[i].Counter = uint32(r.Counter)
		l.counters++
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
	case k.damaged != nil:
		return k.damaged
	}
	return k.checkPath()
}

// damage marks the list damaged, for good, and returns the error that says so.
func (k *KeyFile) damage(err error) error {
	k.damaged = err
	return err
}

// changed reports whether the list's file or its head is not what the stat
// showed when each was last checked: another size, time or inode, or no head
// where there was one.
func (k *KeyFile) changed() bool {
	info, err := k.file.Stat()
	if err != nil || stampOf(info) != k.fstamp {
		return true
	}
	head, err := os.Lstat(k.headPath())
	return err != nil || stampOf(head) != k.hstamp
}

// chainOf checks the digest chain of data, the whole file, and returns the
// number of records and the digest of the last. It checks the bytes and not
// the rules of the list, which were checked when the records were read or
// written: a chain that holds with the count and last digest the list has
// means the file is the one the list was made from.
func chainOf(data []byte) (int, [sha256.Size]byte, error) {
	var none [sha256.Size]byte
	if len(data) == 0 || data[len(data)-1] != '\n' {
		return 0, none, keysDamaged("the file does not end with a whole line")
	}
	rest := data
	line, rest, _ := bytes.Cut(rest, []byte{'\n'})
	if string(line) != keysHeader {
		return 0, none, keysDamaged("the file is not a list of keys of this version")
	}
	prev := sha256.Sum256(line)
	n := 0
	for len(rest) > 0 {
		line, rest, _ = bytes.Cut(rest, []byte{'\n'})
		sum, rec, ok := bytes.Cut(line, []byte{' '})
		if !ok || len(sum) != 2*sha256.Size {
			return 0, none, keysDamaged("record %d has no digest", n+1)
		}
		var got [sha256.Size]byte
		if _, err := hex.Decode(got[:], sum); err != nil || keySum(prev, rec) != got {
			return 0, none, keysDamaged("record %d does not follow the one before it", n+1)
		}
		prev = got
		n++
	}
	return n, prev, nil
}

// verify reads the file again from the descriptor that was locked and checks
// the whole chain against the list as it was read, and the head against both.
// A failure that is damage marks the list damaged. The caller holds mu.
func (k *KeyFile) verify() error {
	info, err := k.file.Stat()
	if err != nil {
		return err
	}
	if info.Size() > maxKeysFileBytes {
		return k.damage(keysDamaged("the file is larger than %d bytes", maxKeysFileBytes))
	}
	data, err := io.ReadAll(io.NewSectionReader(k.file, 0, info.Size()))
	if err != nil {
		return err
	}
	if int64(len(data)) != info.Size() {
		return k.damage(keysDamaged("the file changed while it was read"))
	}
	n, last, err := chainOf(data)
	if err != nil {
		return k.damage(err)
	}
	if n != k.list.records || last != k.prev {
		return k.damage(keysDamaged("the file is not the list that was read"))
	}
	h, ok, err := k.readHead()
	if err != nil {
		if errors.Is(err, ErrDamaged) {
			return k.damage(err)
		}
		return err
	}
	if !ok || h.count != k.list.records || h.digest != k.prev {
		return k.damage(keysDamaged("the head of the list is not the list's"))
	}
	k.fstamp = stampOf(info)
	return nil
}

// Keys returns the list, in the order the credentials were added. Revoked
// credentials are in it, marked. The slices are copies. When the file's size,
// times or inode, or the head's, are not what they were when they were last
// checked, the file is read again and checked whole first, and a list that
// fails the check is marked damaged and refuses this call and every later one
// with [ErrDamaged].
func (k *KeyFile) Keys() ([]StoredKey, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if err := k.usable(); err != nil {
		return nil, err
	}
	if k.changed() {
		if err := k.verify(); err != nil {
			return nil, err
		}
	}
	out := make([]StoredKey, len(k.list.keys))
	for i, s := range k.list.keys {
		s.CredentialID = append([]byte(nil), s.CredentialID...)
		s.PublicKey = append([]byte(nil), s.PublicKey...)
		out[i] = s
	}
	return out, nil
}

// head is what the head file says: the number of records and the digest of the
// last one.
type head struct {
	count  int
	digest [sha256.Size]byte
}

// headName is the name of the head file: the list's name without ".log", and
// ".head".
func (k *KeyFile) headName() string {
	return strings.TrimSuffix(filepath.Base(k.path), ".log") + ".head"
}

func (k *KeyFile) headPath() string { return filepath.Join(filepath.Dir(k.path), k.headName()) }

// readHead reads the head file under the checks of the list's own: a plain
// file of the user with mode 0600 and one name, not a link. It returns false
// when there is no such file. A file that is not of the form is damage.
func (k *KeyFile) readHead() (h head, present bool, err error) {
	fd, err := unix.Openat(int(k.dir.Fd()), k.headName(), unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	switch {
	case errors.Is(err, unix.ENOENT):
		return head{}, false, nil
	case errors.Is(err, unix.ELOOP):
		return head{}, false, fmt.Errorf("%w: the head of the list is a symbolic link", ErrNotPrivate)
	case err != nil:
		return head{}, false, fmt.Errorf("ownerjournal: the head of the list: %w", err)
	}
	f := os.NewFile(uintptr(fd), k.headPath())
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return head{}, false, err
	}
	if !privateFile(info) {
		return head{}, false, fmt.Errorf("%w: the head of the list must be a plain file of the user with mode 0600 and one name", ErrNotPrivate)
	}
	if info.Size() > maxKeysHeadBytes {
		return head{}, false, keysDamaged("the head of the list is larger than %d bytes", maxKeysHeadBytes)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxKeysHeadBytes+1))
	if err != nil {
		return head{}, false, err
	}
	h, ok := parseHead(data)
	if !ok {
		return head{}, false, keysDamaged("the head of the list is not of the form")
	}
	k.hstamp = stampOf(info)
	return h, true, nil
}

func parseHead(data []byte) (head, bool) {
	line, rest, ok := bytes.Cut(data, []byte{'\n'})
	if !ok || len(rest) != 0 {
		return head{}, false
	}
	fields := strings.Split(string(line), " ")
	if len(fields) != 4 || fields[0]+" "+fields[1] != keysHeadHeader {
		return head{}, false
	}
	n := 0
	if len(fields[2]) < 1 || len(fields[2]) > 5 {
		return head{}, false
	}
	for _, c := range fields[2] {
		if c < '0' || c > '9' {
			return head{}, false
		}
		n = n*10 + int(c-'0')
	}
	if n > MaxKeyRecords || !record.IsLowerHex(fields[3], 2*sha256.Size) {
		return head{}, false
	}
	var h head
	h.count = n
	raw, err := hex.DecodeString(fields[3])
	if err != nil {
		return head{}, false
	}
	copy(h.digest[:], raw)
	return h, true
}

// writeHead makes the head file say count records and digest, crash-consistently:
// the line is written under another name in the folder, synced, renamed over the
// head, and the folder synced, so the head is always the old one or the new one
// whole.
func (k *KeyFile) writeHead(count int, digest [sha256.Size]byte) error {
	dirfd := int(k.dir.Fd())
	name := k.headName()
	tmp := name + ".new"
	fd, err := unix.Openat(dirfd, tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0o600)
	if err != nil {
		return fmt.Errorf("ownerjournal: the new head of the list: %w", err)
	}
	f := os.NewFile(uintptr(fd), filepath.Join(filepath.Dir(k.path), tmp))
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || !private(info) || info.Sys().(*syscall.Stat_t).Nlink != 1 {
		return fmt.Errorf("%w: %s is not a plain file of the user with one name", ErrNotPrivate, tmp)
	}
	// Opened without O_TRUNC: a name that is another file's second name must
	// be refused before a byte of that file is touched. What a crash left
	// under the temporary name, now known to be the user's with one name, is
	// emptied here.
	if err := f.Chmod(0o600); err != nil {
		return err
	}
	if err := f.Truncate(0); err != nil {
		return err
	}
	line := fmt.Sprintf("%s %d %s\n", keysHeadHeader, count, hex.EncodeToString(digest[:]))
	if _, err := f.WriteAt([]byte(line), 0); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := unix.Renameat(dirfd, tmp, dirfd, name); err != nil {
		return fmt.Errorf("ownerjournal: naming the new head of the list: %w", err)
	}
	if err := k.dir.Sync(); err != nil {
		return err
	}
	if info, err = f.Stat(); err != nil {
		return err
	}
	k.hstamp = stampOf(info)
	return nil
}

// checkHeadAtOpen compares the head with the list that was just read. A list
// with fewer records than the head, or whose digest at the head's count is not
// the head's, has lost its suffix or is another list: damaged. A list ahead of
// the head by records that chain from it (at most one write's worth) is the
// crash between the append and the head: accepted, and the head is made right.
// A head that is missing is damage for a list with records, and is made for one
// without.
func (k *KeyFile) checkHeadAtOpen() error {
	digests := k.digests
	k.digests = nil
	h, ok, err := k.readHead()
	if err != nil {
		return err
	}
	n := k.list.records
	if !ok {
		if n != 0 {
			return keysDamaged("the head of the list (%s) is missing", k.headName())
		}
		return k.writeHead(0, k.prev)
	}
	switch {
	case n < h.count:
		return keysDamaged("suffix lost: the list has %d records and its head says %d", n, h.count)
	case digests[h.count] != h.digest:
		return keysDamaged("the list does not chain from its head (suffix lost, or another list)")
	case n == h.count:
		return nil
	case n-h.count > MaxKeys:
		return keysDamaged("the list is %d records ahead of its head", n-h.count)
	}
	return k.writeHead(n, k.prev)
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
	// Before anything is added the whole chain is read again and checked, so
	// that a record is never appended to a chain that was damaged in place.
	if err := k.verify(); err != nil {
		return err
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
	// The head follows the record, which is on the disk. A failure here leaves
	// the file one write ahead of the head, which the next open takes for what
	// it is; this list refuses everything until then.
	if err := k.writeHead(k.list.records+len(rs), prev); err != nil {
		k.broken = true
		return errors.Join(ErrUncertain, err)
	}
	info, err = k.file.Stat()
	if err != nil {
		k.broken = true
		return errors.Join(ErrUncertain, err)
	}
	k.fstamp = stampOf(info)
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
		!validKind(n.Kind) || !validWords(n.Words) || !validTime(n.PairedAt) ||
		(n.BackupState && !n.BackupEligible) {
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
	r.AAGUID, r.BackupEligible, r.BackupState, r.Counter = formatAAGUID(n.AAGUID), n.BackupEligible, n.BackupState, int64(n.Counter)
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

// maxSpare is how many rejected pairings and counter records, together, the
// file keeps room for; the rest of the record cap is for credentials and their
// revocations, so that a revocation always has room.
const maxSpare = MaxKeyRecords - 2*MaxKeys

// AppendRejected journals a pairing the owner rejected, with the credential's
// display id and fingerprint words and no key. It refuses with [ErrKeysFull]
// once the file holds as many rejected pairings and counter records as it
// keeps room for.
func (k *KeyFile) AppendRejected(r RejectedPairing) error {
	if !record.IsLowerHex(r.ID, DisplayIDLength) || !validWords(r.Words) || !validTime(r.At) {
		return errors.New("ownerjournal: the rejected pairing is not one a record can hold")
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if err := k.usable(); err != nil {
		return err
	}
	if k.list.rejects+k.list.counters >= maxSpare {
		return ErrKeysFull
	}
	rec := k.stamp(keyRejected, r.At, r.ID, 0)
	rec.Words = r.Words
	return k.commit([]keyRecord{rec})
}

// RaiseCounter records a sign counter of the credential with this display id
// that a verified login or decision reported: when it is higher than the
// highest the list holds, a counter record is written and is on the disk when
// it returns. A counter that is not higher writes nothing and returns nil, so
// the counter is never lowered; so does any counter of a revoked credential,
// whose counter is not kept up. A credential that reports 0, as a synced
// passkey does on every use, never needs a record. An id that no credential
// has is [ErrKeyUnknown]. The records share the room kept for rejected
// pairings: once it is used up the raise is refused with [ErrKeysFull], and
// the highest counter stays the last one written (the owner channel's service
// then tells the operator once and keeps the counter in memory).
func (k *KeyFile) RaiseCounter(id string, counter uint32, at time.Time) error {
	if !record.IsLowerHex(id, DisplayIDLength) || !validTime(at) {
		return errors.New("ownerjournal: the counter is not one a record can hold")
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if err := k.usable(); err != nil {
		return err
	}
	i := k.list.find(id)
	switch {
	case i < 0:
		return ErrKeyUnknown
	case k.list.keys[i].Revoked || counter <= k.list.keys[i].Counter:
		return nil
	case k.list.rejects+k.list.counters >= maxSpare || k.list.records >= MaxKeyRecords:
		return ErrKeysFull
	}
	rec := k.stamp(keyCounter, at, id, 0)
	rec.Counter = int64(counter)
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
		return k.closeErr
	}
	k.closed = true
	k.closeErr = k.release()
	return k.closeErr
}
