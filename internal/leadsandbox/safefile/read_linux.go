//go:build linux

package safefile

import (
	"fmt"
	"slices"
	"sort"

	"golang.org/x/sys/unix"
)

// ReadOption changes how [Root.ReadFile] and [Root.ReadFileInfo] treat a file.
type ReadOption func(*readConfig)

type readConfig struct {
	refuseHardLinks bool
}

// RefuseHardLinks makes a read refuse a regular file with a link count above
// one, with [ErrHardLink], before anything is read. See the package
// documentation for why.
func RefuseHardLinks() ReadOption {
	return func(c *readConfig) { c.refuseHardLinks = true }
}

// FileResult is what [Root.ReadFileInfo] returns.
type FileResult struct {
	Data []byte
	// Info is what fstat reported on the descriptor the data came from.
	// Info.HardLinked() tells a link count above one apart (a name other than
	// the one read points at the same data); there is no separate field.
	Info Info
}

// ReadFile reads the regular file at rel, at most max bytes. See the package
// documentation for the rules; a file over max is refused, never truncated.
// Options: [RefuseHardLinks]. The link count is available from
// [Root.ReadFileInfo].
func (r *Root) ReadFile(rel string, max int64, opts ...ReadOption) ([]byte, error) {
	res, err := r.ReadFileInfo(rel, max, opts...)
	if err != nil {
		return nil, err
	}
	return res.Data, nil
}

// ReadFileInfo is [Root.ReadFile] that also returns what fstat reported on
// the descriptor the data came from (Info.HardLinked reports a hard-linked
// file).
func (r *Root) ReadFileInfo(rel string, max int64, opts ...ReadOption) (*FileResult, error) {
	const op = "read"
	var cfg readConfig
	for _, o := range opts {
		o(&cfg)
	}
	if err := validate(rel, false); err != nil {
		return nil, invalid(op, rel, err)
	}
	if max < 0 {
		return nil, invalid(op, rel, fmt.Errorf("%w: negative cap", ErrInvalidArgument))
	}
	if err := r.begin(op, rel); err != nil {
		return nil, err
	}
	defer r.end()
	// O_NONBLOCK keeps a FIFO from blocking the open, and also keeps a lease
	// a lead holds on the file (a process with the owner's uid can take one)
	// from stalling the owner for the lease-break time: the open fails at
	// once with EAGAIN. Do not drop it because fstat refuses FIFOs anyway.
	// O_NOCTTY keeps a terminal device from becoming the controlling
	// terminal. The file type is decided below, on the descriptor; a device's
	// open has already run by then (see the package documentation).
	fd, err := r.open(r.fd, rel, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOCTTY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, classify(op, rel, err)
	}
	defer unix.Close(fd)
	info, err := fstat(fd)
	if err != nil {
		return nil, &Error{Op: op, Path: rel, Err: err}
	}
	if !info.IsRegular() {
		return nil, &Error{Op: op, Path: rel, Err: fmt.Errorf("%w: %s", ErrNotRegular, info.Mode.Type())}
	}
	if cfg.refuseHardLinks && info.HardLinked() {
		return nil, &Error{Op: op, Path: rel, Err: fmt.Errorf("%w: link count %d", ErrHardLink, info.Nlink)}
	}
	if info.Size > max {
		return nil, &Error{Op: op, Path: rel, Err: fmt.Errorf("%w: %d bytes, cap %d", ErrTooLarge, info.Size, max)}
	}
	data, err := readCapped(fd, info.Size, max)
	if err != nil {
		return nil, &Error{Op: op, Path: rel, Err: err}
	}
	return &FileResult{Data: data, Info: info}, nil
}

// readCapped reads fd to its end, refusing more than max bytes. hint is the
// size fstat reported and is not trusted: a file that grew is caught by the
// count of bytes read, which is what the cap applies to.
func readCapped(fd int, hint, max int64) ([]byte, error) {
	if hint < 0 || hint > max {
		hint = max
	}
	buf := make([]byte, 0, hint+1)
	for {
		if len(buf) == cap(buf) {
			buf = slices.Grow(buf, 4096)
		}
		n, err := unix.Read(fd, buf[len(buf):cap(buf)])
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return nil, err
		}
		if n == 0 {
			return buf, nil
		}
		buf = buf[:len(buf)+n]
		if int64(len(buf)) > max {
			return nil, fmt.Errorf("%w: file grew past the cap of %d bytes while it was read", ErrTooLarge, max)
		}
	}
}

// ReadDirNames lists the names in the folder at rel ("." is the handle's own
// folder), sorted, without "." and "..". A folder with more than max entries
// is refused with [ErrTooLarge]; the list is never cut short. The names are
// the lead's: the caller treats them as untrusted text.
func (r *Root) ReadDirNames(rel string, max int) ([]string, error) {
	const op = "readdir"
	if err := validate(rel, true); err != nil {
		return nil, invalid(op, rel, err)
	}
	if max < 0 {
		return nil, invalid(op, rel, fmt.Errorf("%w: negative cap", ErrInvalidArgument))
	}
	if err := r.begin(op, rel); err != nil {
		return nil, err
	}
	defer r.end()
	// No O_NOFOLLOW here: with O_DIRECTORY a link in the last component would
	// fail with ENOTDIR; the resolve flags refuse it with ELOOP instead.
	fd, err := r.open(r.fd, rel, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NONBLOCK|unix.O_NOCTTY, 0)
	if err != nil {
		return nil, classify(op, rel, err)
	}
	defer unix.Close(fd)
	names := []string{}
	buf := make([]byte, 8192)
	for {
		n, err := unix.ReadDirent(fd, buf)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return nil, &Error{Op: op, Path: rel, Err: err}
		}
		if n <= 0 {
			break
		}
		_, _, names = unix.ParseDirent(buf[:n], -1, names)
		if len(names) > max {
			return nil, &Error{Op: op, Path: rel, Err: fmt.Errorf("%w: more than %d entries", ErrTooLarge, max)}
		}
	}
	sort.Strings(names)
	return names, nil
}
