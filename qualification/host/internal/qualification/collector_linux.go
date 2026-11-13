//go:build linux && amd64

package qualification

import (
	"errors"
	"io"
	"os"
	"syscall"
)

const MaxCollectBytes = 65536

// CollectFlat adapts the reviewed source proposal. The trusted caller must have
// independently established emptiness of the workload and frozen this fresh
// owned tree outside its reach. externallyStopped is an assertion of that proof,
// never proof itself. This reader cannot establish actual termination or defend
// against a concurrent writer. An error means no evidence was collected.
func CollectFlat(root *os.Root, name string, limit int64, externallyStopped bool) ([]byte, error) {
	if !externallyStopped {
		return nil, errors.New("boundary-empty and frozen-tree proof required")
	}
	if root == nil || !validName(name) || limit < 1 || limit > MaxCollectBytes {
		return nil, errors.New("bad root, flat name or limit")
	}
	before, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if err := checkRegular(before, limit); err != nil {
		return nil, err
	}
	f, err := openFlat(root, name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if err := checkRegular(opened, limit); err != nil {
		return nil, err
	}
	if !same(before, opened) {
		return nil, errors.New("identity changed before open")
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("file exceeds independent read cap")
	}
	after, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if err := checkRegular(after, limit); err != nil {
		return nil, err
	}
	if !same(opened, after) || int64(len(data)) != after.Size() {
		return nil, errors.New("file changed during read")
	}
	return data, nil
}

// openFlat pins the os.Root directory, then opens only a flat final component.
// The selected Go Root.OpenFile resolves in-root symlinks even with O_NOFOLLOW;
// passing that flag alone is not final-component protection. Openat provides
// the kernel semantics here, with no untrusted intermediate components.
func openFlat(root *os.Root, name string) (*os.File, error) {
	if root == nil || !validName(name) {
		return nil, errors.New("bad root or flat name")
	}
	dir, err := root.OpenFile(".", os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	fd, err := syscall.Openat(int(dir.Fd()), name, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

func checkRegular(fi os.FileInfo, limit int64) error {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || !fi.Mode().IsRegular() || st.Nlink != 1 || fi.Size() < 0 || fi.Size() > limit {
		return errors.New("expected singly linked bounded regular file")
	}
	return nil
}

func same(a, b os.FileInfo) bool {
	sa, oka := a.Sys().(*syscall.Stat_t)
	sb, okb := b.Sys().(*syscall.Stat_t)
	return oka && okb && os.SameFile(a, b) && a.Mode() == b.Mode() && a.Size() == b.Size() &&
		a.ModTime().Equal(b.ModTime()) && sa.Nlink == sb.Nlink && sa.Ctim == sb.Ctim
}
