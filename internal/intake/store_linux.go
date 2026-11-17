//go:build linux

package intake

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"

	"ataniq/internal/exchange/safefs"
)

// folder is the records folder held by a descriptor, for the writes and the
// listing. Reads go through safefs.
type folder struct{ fd int }

// openFolder opens dir for writing and checks it: a real folder, owned by the
// caller, mode 0700, and the same folder that root holds.
func openFolder(dir string, root *safefs.Root) (folder, error) {
	fi, err := root.Lstat(".")
	if err != nil {
		return folder{}, err
	}
	want, ok := fi.Sys().(*unix.Stat_t)
	if !ok {
		return folder{}, errors.New("the folder cannot be examined")
	}
	if want.Mode&unix.S_IFMT != unix.S_IFDIR {
		return folder{}, errors.New("the path is not a folder")
	}
	if int(want.Uid) != os.Geteuid() {
		return folder{}, errors.New("the folder is not owned by the caller")
	}
	if perm := want.Mode & 0o7777; perm != 0o700 {
		return folder{}, fmt.Errorf("the folder has mode %04o, not 0700", perm)
	}
	var fd int
	for {
		fd, err = unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != unix.EINTR {
			break
		}
	}
	if err != nil {
		return folder{}, err
	}
	var got unix.Stat_t
	if err := unix.Fstat(fd, &got); err != nil {
		unix.Close(fd)
		return folder{}, err
	}
	if got.Dev != want.Dev || got.Ino != want.Ino {
		unix.Close(fd)
		return folder{}, errors.New("the folder was replaced while it was opened")
	}
	return folder{fd: fd}, nil
}

func (f folder) close() error { return unix.Close(f.fd) }

// create writes data to a new file called name in the folder. The data goes
// to a temporary name first (created exclusively, mode 0600, no symlink
// followed), is synced, and gets its name without replacing anything that is
// there: renameat2 with RENAME_NOREPLACE, or link and unlink where the
// filesystem has no renameat2. The folder is synced afterwards. On an error
// the temporary file, which this call made, is removed; a file called name is
// never touched.
func (f folder) create(name string, data []byte) (err error) {
	var rnd [8]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return err
	}
	tmp := fmt.Sprintf(".tmp-%x", rnd)
	var fd int
	for {
		fd, err = unix.Openat(f.fd, tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
		if err != unix.EINTR {
			break
		}
	}
	if err != nil {
		return err
	}
	made := true
	defer func() {
		if made {
			unix.Unlinkat(f.fd, tmp, 0)
		}
	}()
	if err := writeAll(fd, data); err != nil {
		unix.Close(fd)
		return err
	}
	if err := unix.Fchmod(fd, 0o600); err != nil {
		unix.Close(fd)
		return err
	}
	if err := unix.Fsync(fd); err != nil {
		unix.Close(fd)
		return err
	}
	if err := unix.Close(fd); err != nil {
		return err
	}
	err = unix.Renameat2(f.fd, tmp, f.fd, name, unix.RENAME_NOREPLACE)
	switch err {
	case nil:
		made = false
	case unix.ENOSYS, unix.EINVAL:
		// No RENAME_NOREPLACE here: link fails if the name exists.
		if err := unix.Linkat(f.fd, tmp, f.fd, name, 0); err != nil {
			return err
		}
		made = false
		unix.Unlinkat(f.fd, tmp, 0)
	default:
		return err
	}
	return unix.Fsync(f.fd)
}

func writeAll(fd int, data []byte) error {
	for len(data) > 0 {
		n, err := unix.Write(fd, data)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return err
		}
		data = data[n:]
	}
	return nil
}

// names returns the names in the folder, at most max.
func (f folder) names(max int) ([]string, error) {
	var fd int
	var err error
	for {
		fd, err = unix.Openat(f.fd, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
		if err != unix.EINTR {
			break
		}
	}
	if err != nil {
		return nil, err
	}
	dir := os.NewFile(uintptr(fd), "records")
	defer dir.Close()
	var names []string
	for {
		batch, err := dir.Readdirnames(256)
		names = append(names, batch...)
		if len(names) > max {
			return nil, errors.New("too many names in the folder")
		}
		if err == io.EOF {
			return names, nil
		}
		if err != nil {
			return nil, err
		}
	}
}
