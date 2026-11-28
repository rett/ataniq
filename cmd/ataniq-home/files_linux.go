package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

var errFiles = errors.New("unsafe or missing file or directory")

// Pin each directory component before opening its child. Trusted sticky parents
// allow private temporary trees. The leaf file is checked on its open descriptor.
func directory(p string, private bool) (*os.File, error) {
	if !filepath.IsAbs(p) || filepath.Clean(p) != p {
		return nil, errFiles
	}
	const flags = unix.O_PATH | unix.O_CLOEXEC | unix.O_DIRECTORY | unix.O_NOFOLLOW
	fd, err := unix.Open("/", flags, 0)
	if err != nil {
		return nil, errFiles
	}
	for _, part := range strings.FieldsFunc(p, func(r rune) bool { return r == '/' }) {
		next, err := unix.Openat(fd, part, flags, 0)
		unix.Close(fd)
		if err != nil {
			return nil, errFiles
		}
		fd = next
		var st unix.Stat_t
		if unix.Fstat(fd, &st) != nil || (st.Uid != 0 && st.Uid != uint32(os.Geteuid())) || (st.Mode&0022 != 0 && st.Mode&unix.S_ISVTX == 0) {
			unix.Close(fd)
			return nil, errFiles
		}
	}
	f := os.NewFile(uintptr(fd), p)
	if private {
		var st unix.Stat_t
		if unix.Fstat(fd, &st) != nil || st.Uid != uint32(os.Geteuid()) || st.Mode != unix.S_IFDIR|0700 {
			f.Close()
			return nil, errFiles
		}
	}
	return f, nil
}

func openRegular(dir *os.File, name string, private bool) (*os.File, error) {
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, errFiles
	}
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Nlink != 1 ||
		(st.Uid != 0 && st.Uid != uint32(os.Geteuid())) || st.Mode&0022 != 0 ||
		(private && (st.Uid != uint32(os.Geteuid()) || st.Mode != unix.S_IFREG|0600)) {
		unix.Close(fd)
		return nil, errFiles
	}
	return os.NewFile(uintptr(fd), name), nil
}

func readFile(p string, limit int64, private bool) ([]byte, error) {
	if !absolute(p) {
		return nil, errFiles
	}
	d, err := directory(filepath.Dir(p), private)
	if err != nil {
		return nil, err
	}
	defer d.Close()
	return readRegular(d, filepath.Base(p), limit, private)
}

func readRegular(d *os.File, name string, limit int64, private bool) ([]byte, error) {
	f, err := openRegular(d, name, private)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(b)) > limit {
		return nil, errFiles
	}
	return b, nil
}

func syncDirectory(fd int) error {
	return syncAt(fd, ".", unix.Fsync)
}

// Open relative to the pinned directory. Its own descriptor may be O_PATH.
func syncAt(dir int, name string, fsync func(int) error) error {
	fd, err := unix.Openat(dir, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	return errors.Join(fsync(fd), unix.Close(fd))
}
