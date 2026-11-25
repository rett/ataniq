package ownerjournal

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// KeysSnapshot is what [VerifyKeys] read and passed: the bytes of the list's
// file, as read from the descriptor it checked, and its number of records.
type KeysSnapshot struct {
	Data    []byte
	Records int
}

// VerifyKeys checks the list in the file at path and its head, read only:
// the checks [OpenKeys] makes, and no write. The folder is opened once
// without following its last part, and the file and the head are opened in
// it, by the folder's descriptor, without following a link; the folder must
// be the user's own with mode 0700, the file and the head plain files of the
// user with mode 0600 and one name ([ErrNotPrivate]), and the path must
// still name the file and the folder that were checked. The file's bytes,
// read from the descriptor that was checked, must hold under the replay of
// OpenKeys: the header, each record by the strict reader, the numbers in
// sequence, the digest chain and the rules of the list ([ErrDamaged]). The
// head must be exact: it counts every record and holds the last digest.
//
// It creates, repairs, locks and writes nothing. Where OpenKeys would make
// something, VerifyKeys refuses: no file ([ErrNoKeys], where OpenKeys makes a
// new, empty list), no head (OpenKeys makes one for an empty list), and a
// list ahead of its head by records that chain from it ([ErrHeadBehind],
// where OpenKeys makes the head right). It takes no lock, so it can be run
// while a daemon holds the list: a record appended between its reads is
// refused (as [ErrHeadBehind] or as a list that changed while it was
// checked), and a check run again afterwards reads the list whole.
func VerifyKeys(path string) (_ KeysSnapshot, err error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsRune(path, 0) {
		return KeysSnapshot{}, errors.New("ownerjournal: the path must be absolute and clean")
	}
	k := &KeyFile{path: path}
	defer func() { err = errors.Join(err, k.release()) }()
	if err := k.openDir(); err != nil {
		return KeysSnapshot{}, err
	}
	fd, err := unix.Openat(int(k.dir.Fd()), filepath.Base(path), unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	switch {
	case errors.Is(err, unix.ENOENT):
		return KeysSnapshot{}, fmt.Errorf("%w (the daemon would make a new, empty one)", ErrNoKeys)
	case errors.Is(err, unix.ELOOP):
		return KeysSnapshot{}, fmt.Errorf("%w: the file is a symbolic link", ErrNotPrivate)
	case err != nil:
		return KeysSnapshot{}, fmt.Errorf("ownerjournal: the file of keys: %w", err)
	}
	k.file = os.NewFile(uintptr(fd), path)
	if k.identity, err = k.file.Stat(); err != nil {
		return KeysSnapshot{}, err
	}
	if err := keysFileProblem(k.identity); err != nil {
		return KeysSnapshot{}, err
	}
	if err := k.checkPath(); err != nil {
		return KeysSnapshot{}, err
	}
	data, stamp, err := k.readData()
	if err != nil {
		return KeysSnapshot{}, err
	}
	list, digests, err := parseKeys(data)
	if err != nil {
		return KeysSnapshot{}, err
	}
	h, ok, err := k.readHead()
	if err != nil {
		return KeysSnapshot{}, err
	}
	if !ok {
		return KeysSnapshot{}, keysDamaged("the head of the list (%s) is missing", k.headName())
	}
	if err := headAgainst(h, digests); err != nil {
		return KeysSnapshot{}, err
	}
	// The file read is the file now: the same size, times and inode, at
	// the same path in the same folder.
	info, err := k.file.Stat()
	if err != nil {
		return KeysSnapshot{}, err
	}
	if stampOf(info) != stamp {
		return KeysSnapshot{}, errors.New("ownerjournal: the file of keys changed while it was checked; check it again")
	}
	if err := k.checkPath(); err != nil {
		return KeysSnapshot{}, err
	}
	return KeysSnapshot{Data: data, Records: list.records}, nil
}
