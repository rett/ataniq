// Package instanceid owns the private identity of a daemon state root.
package instanceid

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

const FileName = "instance.key"

const (
	magic            = "ataniq-instance-key/2\n"
	incarnationMagic = "ataniq-store-incarnation/1\n"
	IncarnationFile  = "store.incarnation"
	incarnationSize  = 16
	maxLabel         = 128
)

// Refusal identifies a key-loading failure through errors.Is or errors.As.
type Refusal string

func (r Refusal) Error() string { return string(r) }

const (
	ErrMissing Refusal = "instance identity missing"
	ErrInvalid Refusal = "instance identity damaged"
	ErrUnsafe  Refusal = "unsafe instance identity"
)

// Binding is the public identity recorded in the journal.
type Binding struct {
	Authority   string
	Incarnation string
}

// Identity pairs the comparable public binding with its display-only label.
type Identity struct {
	Binding
	Label string
}

// OpenRoot pins an owner-only state directory. With create set it creates
// missing components and confirms each new directory entry on disk.
func OpenRoot(path string, create bool) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
		return nil, fmt.Errorf("%w: expected a clean absolute state directory, got %q", ErrUnsafe, path)
	}
	const flags = unix.O_DIRECTORY | unix.O_CLOEXEC
	fd, err := unix.Open("/", unix.O_PATH|flags, 0)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(path[1:], "/")
	for i, part := range parts {
		how := unix.O_PATH | flags
		if i == len(parts)-1 {
			how = unix.O_RDONLY | flags | unix.O_NOFOLLOW
		}
		next, err := unix.Openat(fd, part, how, 0)
		if err == unix.ENOENT && create {
			err = unix.Mkdirat(fd, part, 0700)
			if err == nil || err == unix.EEXIST {
				next, err = unix.Openat(fd, part, how, 0)
			}
		}
		if err == nil && create {
			// On retry an empty component may have been created just
			// before a crash. Confirm its name before using it.
			child := FileName
			if i+1 < len(parts) {
				child = parts[i+1]
			}
			var st unix.Stat_t
			if err = unix.Fstatat(next, child, &st, unix.AT_SYMLINK_NOFOLLOW); err == unix.ENOENT {
				err = syncParent(fd)
			}
			if err != nil {
				unix.Close(next)
			}
		}
		unix.Close(fd)
		if err == unix.ENOENT {
			return nil, fmt.Errorf("%s: %w", path, ErrMissing)
		}
		if err != nil {
			partPath := "/" + strings.Join(parts[:i+1], "/")
			if err == unix.ENOTDIR || err == unix.ELOOP {
				return nil, fmt.Errorf("%w: %s is a symlink or not a directory", ErrUnsafe, partPath)
			}
			return nil, fmt.Errorf("%w: open %s: %w", ErrUnsafe, partPath, err)
		}
		fd = next
	}
	f := os.NewFile(uintptr(fd), path)
	if err := checkRoot(fd); err != nil {
		f.Close()
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return f, nil
}

func syncParent(fd int) error {
	parent, err := unix.Openat(fd, ".", unix.O_DIRECTORY|unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(parent)
	return unix.Fsync(parent)
}

func checkRoot(fd int) error {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return err
	}
	if st.Mode&(unix.S_IFMT|0777) != unix.S_IFDIR|0700 || int(st.Uid) != os.Geteuid() {
		return fmt.Errorf("%w: expected a user-owned 0700 state directory", ErrUnsafe)
	}
	return nil
}

func checkFile(st unix.Stat_t, uid int) error {
	if st.Mode != unix.S_IFREG|0600 || int(st.Uid) != uid || st.Nlink != 1 {
		return fmt.Errorf("%w: expected a singly linked user-owned 0600 regular file", ErrUnsafe)
	}
	return nil
}

func validLabel(s string) bool {
	return len(s) <= maxLabel && utf8.ValidString(s) && strings.IndexFunc(s, unicode.IsControl) < 0
}

// ValidateLabel checks a proposed display label before init touches the root.
func ValidateLabel(label string) error {
	if !validLabel(label) {
		return errors.New("display label must be valid UTF-8, at most 128 bytes, without control characters")
	}
	return nil
}

// Load reads the existing identity without creating a root or a key.
func Load(path string) (Identity, error) {
	root, err := OpenRoot(path, false)
	if err != nil {
		return Identity{}, err
	}
	defer root.Close()
	return LoadRoot(root)
}

// LoadRoot reads the identity from a pinned root and names its files in errors.
func LoadRoot(root *os.File) (Identity, error) {
	return readIdentity(int(root.Fd()), root.Name(), existing)
}

// LoadAt reads both identity files relative to a pinned state root.
func LoadAt(root int) (Identity, error) {
	return readIdentity(root, "", existing)
}

type readMode int

const (
	existing readMode = iota
	firstInit
	restore
)

// CheckInit validates both existing files before the command creates a
// journal or takes the daemon lock. An empty authority means init needs a new
// key. A restored root must already have its key and may lack an incarnation.
func CheckInit(root *os.File, restored bool) (Identity, error) {
	mode := firstInit
	if restored {
		mode = restore
	}
	return readIdentity(int(root.Fd()), root.Name(), mode)
}

func readIdentity(root int, dir string, mode readMode) (Identity, error) {
	id, err := loadKey(root)
	if err != nil && !(mode == firstInit && errors.Is(err, ErrMissing)) {
		return Identity{}, &os.PathError{Op: "load", Path: filepath.Join(dir, FileName), Err: err}
	}
	id.Incarnation, err = loadIncarnation(root)
	if errors.Is(err, ErrInvalid) {
		err = fmt.Errorf("%w; delete the damaged %s, then run init --restored with the original key", err, IncarnationFile)
	}
	if errors.Is(err, ErrMissing) {
		if id.Authority == "" || mode == restore {
			return id, nil
		}
		err = fmt.Errorf("%w: missing store incarnation; after restoring a key use init --restored", ErrInvalid)
	}
	if err != nil {
		return Identity{}, &os.PathError{Op: "load", Path: filepath.Join(dir, IncarnationFile), Err: err}
	}
	return id, nil
}

func loadKey(root int) (Identity, error) {
	b, err := readPayload(root, FileName, magic, ed25519.SeedSize, maxLabel)
	if err != nil {
		return Identity{}, err
	}
	defer clear(b)
	label := string(b[ed25519.SeedSize:])
	if !validLabel(label) {
		return Identity{}, fmt.Errorf("%w: invalid label", ErrInvalid)
	}
	key := ed25519.NewKeyFromSeed(b[:ed25519.SeedSize])
	defer clear(key)
	pub := sha256.Sum256(append([]byte("ataniq-instance-authority/1"), key.Public().(ed25519.PublicKey)...))
	return Identity{Binding: Binding{Authority: hex.EncodeToString(pub[:])}, Label: label}, nil
}

func loadIncarnation(root int) (string, error) {
	b, err := readPayload(root, IncarnationFile, incarnationMagic, incarnationSize, 0)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func readPayload(root int, name, prefix string, size, extra int) ([]byte, error) {
	fd, err := unix.Openat(root, name, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err == unix.ENOENT {
		return nil, ErrMissing
	}
	if err == unix.ELOOP {
		return nil, fmt.Errorf("%w: symlink", ErrUnsafe)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnsafe, err)
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	if err := checkRoot(root); err != nil {
		return nil, err
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return nil, err
	}
	if err := checkFile(st, os.Geteuid()); err != nil {
		return nil, err
	}
	minSize := len(prefix) + size + sha256.Size
	b, err := io.ReadAll(io.LimitReader(f, int64(minSize+extra+1)))
	if err != nil || len(b) < minSize || len(b) > minSize+extra || !bytes.HasPrefix(b, []byte(prefix)) {
		clear(b)
		return nil, ErrInvalid
	}
	body := b[:len(b)-sha256.Size]
	sum := sha256.Sum256(body)
	if !bytes.Equal(sum[:], b[len(body):]) {
		clear(b)
		return nil, fmt.Errorf("%w: checksum mismatch", ErrInvalid)
	}
	return body[len(prefix):], nil
}

// InitAt publishes the store incarnation first. An interruption before the
// key is published leaves an incarnation that a retry can safely reuse.
func InitAt(root *os.File, label string) (Identity, error) {
	if err := ValidateLabel(label); err != nil {
		return Identity{}, err
	}
	fd := int(root.Fd())
	if err := checkRoot(fd); err != nil {
		return Identity{}, err
	}
	id, err := CheckInit(root, false)
	if err != nil {
		return Identity{}, err
	}
	if id.Authority != "" {
		return id, root.Sync()
	}
	if id.Incarnation == "" {
		if err := writeIncarnation(root, unix.RENAME_NOREPLACE); err != nil {
			return Identity{}, err
		}
	}
	payload := make([]byte, ed25519.SeedSize+len(label))
	defer clear(payload)
	rand.Read(payload[:ed25519.SeedSize])
	copy(payload[ed25519.SeedSize:], label)
	if err := publish(root, FileName, magic, payload, unix.RENAME_NOREPLACE); err != nil {
		return Identity{}, err
	}
	return readIdentity(int(root.Fd()), root.Name(), existing)
}

// RestoreAt gives a restored key a fresh store incarnation. The caller
// excludes daemon starts and other initializers during the restore.
func RestoreAt(root *os.File) (Identity, error) {
	if _, err := CheckInit(root, true); err != nil {
		return Identity{}, err
	}
	if err := writeIncarnation(root, 0); err != nil {
		return Identity{}, err
	}
	return readIdentity(int(root.Fd()), root.Name(), existing)
}

func writeIncarnation(root *os.File, flags uint) error {
	var value [incarnationSize]byte
	rand.Read(value[:])
	return publish(root, IncarnationFile, incarnationMagic, value[:], flags)
}

func publish(root *os.File, dest, prefix string, payload []byte, flags uint) error {
	body := make([]byte, len(prefix)+len(payload), len(prefix)+len(payload)+sha256.Size)
	copy(body, prefix)
	copy(body[len(prefix):], payload)
	sum := sha256.Sum256(body)
	b := append(body, sum[:]...)
	defer clear(b)
	fd := int(root.Fd())
	name := ".instance-" + rand.Text()
	tmp, err := unix.Openat(fd, name, unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY|unix.O_CLOEXEC, 0600)
	if err != nil {
		return err
	}
	defer unix.Unlinkat(fd, name, 0)
	f := os.NewFile(uintptr(tmp), name)
	err = f.Chmod(0600)
	if err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	if err = errors.Join(err, f.Close()); err != nil {
		return err
	}
	err = unix.Renameat2(fd, name, fd, dest, flags)
	if err != nil && err != unix.EEXIST {
		return err
	}
	return root.Sync()
}
