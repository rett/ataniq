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
	magic           = "ataniq-instance-v1\n"
	incarnationSize = 16
	maxLabel        = 128
	fixedSize       = len(magic) + ed25519.SeedSize + incarnationSize + sha256.Size
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
		return nil, fmt.Errorf("%w: expected a clean absolute state directory", ErrUnsafe)
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
			return nil, ErrMissing
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
		return nil, err
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

// Load reads the existing identity without creating a root or a key.
func Load(path string) (Identity, error) {
	root, err := OpenRoot(path, false)
	if err != nil {
		return Identity{}, err
	}
	defer root.Close()
	id, err := LoadAt(int(root.Fd()))
	if err != nil {
		return Identity{}, fmt.Errorf("%s: %w", path, err)
	}
	return id, nil
}

// LoadAt reads the key relative to a pinned state root.
func LoadAt(root int) (Identity, error) {
	fd, err := unix.Openat(root, FileName, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err == unix.ENOENT {
		return Identity{}, ErrMissing
	}
	if err != nil {
		if err == unix.ELOOP {
			return Identity{}, fmt.Errorf("%w: %s is a symlink", ErrUnsafe, FileName)
		}
		return Identity{}, fmt.Errorf("%w: open %s: %w", ErrUnsafe, FileName, err)
	}
	f := os.NewFile(uintptr(fd), FileName)
	defer f.Close()
	if err := checkRoot(root); err != nil {
		return Identity{}, err
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return Identity{}, err
	}
	if err := checkFile(st, os.Geteuid()); err != nil {
		return Identity{}, err
	}
	b, err := io.ReadAll(io.LimitReader(f, int64(fixedSize+maxLabel+1)))
	defer clear(b)
	if err != nil {
		return Identity{}, fmt.Errorf("%w: read key: %w", ErrInvalid, err)
	}
	if len(b) < fixedSize || len(b) > fixedSize+maxLabel || !bytes.HasPrefix(b, []byte(magic)) {
		return Identity{}, ErrInvalid
	}
	body := b[:len(b)-sha256.Size]
	sum := sha256.Sum256(body)
	if !bytes.Equal(sum[:], b[len(body):]) {
		return Identity{}, ErrInvalid
	}
	seedEnd := len(magic) + ed25519.SeedSize
	label := string(body[seedEnd+incarnationSize:])
	if !validLabel(label) {
		return Identity{}, ErrInvalid
	}
	key := ed25519.NewKeyFromSeed(body[len(magic):seedEnd])
	defer clear(key)
	pub := sha256.Sum256(append([]byte("ataniq-instance-authority/1"), key.Public().(ed25519.PublicKey)...))
	return Identity{
		Binding: Binding{
			Authority:   hex.EncodeToString(pub[:]),
			Incarnation: hex.EncodeToString(body[seedEnd : seedEnd+incarnationSize]),
		},
		Label: label,
	}, nil
}

// InitAt creates the identity once. A retry loads the original identity.
// label is used only on creation.
func InitAt(root *os.File, label string) (Identity, error) {
	if !validLabel(label) {
		return Identity{}, errors.New("display label must be valid UTF-8, at most 128 bytes, without control characters")
	}
	fd := int(root.Fd())
	if err := checkRoot(fd); err != nil {
		return Identity{}, err
	}
	id, err := LoadAt(fd)
	if err == nil {
		return id, root.Sync()
	}
	if !errors.Is(err, ErrMissing) {
		return Identity{}, err
	}
	b := make([]byte, fixedSize+len(label))
	defer clear(b)
	copy(b, magic)
	end := len(magic) + ed25519.SeedSize + incarnationSize
	rand.Read(b[len(magic):end])
	copy(b[end:], label)
	body := b[:len(b)-sha256.Size]
	sum := sha256.Sum256(body)
	copy(b[len(body):], sum[:])

	name := ".instance-" + rand.Text()
	tmp, err := unix.Openat(fd, name, unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY|unix.O_CLOEXEC, 0600)
	if err != nil {
		return Identity{}, err
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
		return Identity{}, err
	}
	err = unix.Renameat2(fd, name, fd, FileName, unix.RENAME_NOREPLACE)
	if err != nil && err != unix.EEXIST {
		return Identity{}, err
	}
	if err := root.Sync(); err != nil {
		return Identity{}, err
	}
	return LoadAt(fd)
}
