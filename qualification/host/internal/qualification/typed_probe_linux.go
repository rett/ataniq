//go:build linux && amd64

package qualification

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
	"unsafe"
)

func fakeTypedFD(o *Observation, r Request, cfg Config) {
	if !filepath.IsAbs(cfg.FixtureRoot) {
		o.Status, o.Reason = "error", "explicit absolute fixture root required"
		return
	}
	root, err := os.OpenRoot(cfg.FixtureRoot)
	if err != nil {
		incompleteOperation(o, "fixture-root", err)
		return
	}
	defer root.Close()
	// The launcher must freeze this trusted metadata outside workload write
	// reach. The probe is running, so it cannot assert collector stop proof.
	data, err := readFrozenFlat(root, r.Payload.TypedFD.Name, MaxFD1BindingBytes)
	if err != nil {
		incompleteOperation(o, "binding-read", err)
		return
	}
	b, err := DecodeFD1Bindings(bytes.NewReader(data))
	if err != nil {
		incompleteOperation(o, "binding-validate", err)
		return
	}
	if !b.matches(r) {
		incompleteOperation(o, "binding-context", errors.New("binding context mismatch"))
		return
	}
	object, kind := b.Directory.FD1Object, uint32(syscall.S_IFDIR)
	switch r.Case {
	case CaseFakePipeFD:
		object, kind = b.Pipe.FD1Object, syscall.S_IFIFO
	case CaseFakeSocketFD:
		object, kind = b.Socket.FD1Object, syscall.S_IFSOCK
	}
	var before syscall.Stat_t
	if err := syscall.Fstat(object.FD, &before); err != nil {
		operationError(o, "fstat", err)
		return
	}
	match := fd1Identity(before, object.Device, object.Inode, kind)
	o.Facts = append(o.Facts, known("identity_match", strconv.FormatBool(match)))
	if !match {
		incompleteOperation(o, "fd-identity", errors.New("unexpected typed descriptor"))
		return
	}
	if err := fd1Flags(object); err != nil {
		incompleteOperation(o, "fd-flags", err)
		return
	}
	if r.Case == CaseFakeDirFD {
		fd1Directory(o, r, b.Directory)
		return
	}
	channel := b.Pipe
	if r.Case == CaseFakeSocketFD {
		if err := fd1Socket(object.FD, syscall.GetsockoptInt); err != nil {
			incompleteOperation(o, "socket-type", err)
			return
		}
		channel = b.Socket
	}
	readFD1Channel(o, r, channel, before, syscall.Read, syscall.Fstat, time.Now)
	if o.Status == "observed" {
		if err := fd1Flags(object); err != nil {
			incompleteOperation(o, "postread-flags", err)
		}
	}
}

func fd1Identity(s syscall.Stat_t, device, inode uint64, kind uint32) bool {
	return uint64(s.Dev) == device && s.Ino == inode && s.Mode&syscall.S_IFMT == kind
}

func fd1Flags(o FD1Object) error {
	flags, _, err := syscall.Syscall(syscall.SYS_FCNTL, uintptr(o.FD), syscall.F_GETFL, 0)
	if err != 0 {
		return err
	}
	access := syscall.O_RDONLY
	if o.Access == "read-write" {
		access = syscall.O_RDWR
	}
	// Reject O_PATH as well: its O_ACCMODE bits look read-only.
	if int(flags)&(syscall.O_ACCMODE|0x200000) != access || (int(flags)&syscall.O_NONBLOCK != 0) != *o.Nonblock {
		return errors.New("unexpected access or nonblock flags")
	}
	return nil
}

func fd1Socket(fd int, get func(int, int, int) (int, error)) error {
	domain, err := get(fd, syscall.SOL_SOCKET, syscall.SO_DOMAIN)
	if err != nil {
		return err
	}
	kind, err := get(fd, syscall.SOL_SOCKET, syscall.SO_TYPE)
	if err != nil {
		return err
	}
	if domain != syscall.AF_UNIX || kind != syscall.SOCK_STREAM {
		return errors.New("unexpected socket domain or type")
	}
	return nil
}

// Metadata lookup relative to the validated directory, with no symlink follow.
// This prevents opening a wrong-type or substituted leaf in the frozen tree.
func fd1LeafStat(fd int, name string) (syscall.Stat_t, error) {
	var st syscall.Stat_t
	p, err := syscall.BytePtrFromString(name)
	if err != nil {
		return st, err
	}
	_, _, errno := syscall.Syscall6(syscall.SYS_NEWFSTATAT, uintptr(fd), uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&st)), 0x100, 0, 0)
	if errno != 0 {
		return st, errno
	}
	return st, nil
}

func fd1LeafMatches(s syscall.Stat_t, l *FD1Leaf, limit int) bool {
	return fd1Identity(s, l.Device, l.Inode, syscall.S_IFREG) && s.Nlink == 1 && s.Size >= 0 && s.Size <= int64(limit) && s.Size == *l.Size
}

func fd1Directory(o *Observation, r Request, d *FD1Directory) {
	l := d.Leaf
	before, err := fd1LeafStat(d.FD, l.Name)
	if err != nil {
		incompleteOperation(o, "leaf-lstat", err)
		return
	}
	if !fd1LeafMatches(before, l, r.MaxBytes) {
		incompleteOperation(o, "leaf-identity", errors.New("unexpected leaf"))
		return
	}
	fd, err := syscall.Openat(d.FD, l.Name, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		operationError(o, "openat", err)
		return
	}
	o.Facts = append(o.Facts, known("opened", "true"))
	defer func() {
		if err := syscall.Close(fd); err != nil && o.Status == "observed" {
			incompleteOperation(o, "close", err)
		}
	}()
	readFD1Leaf(o, r, l, fd, before, syscall.Fstat, syscall.Pread)
}

func readFD1Leaf(o *Observation, r Request, l *FD1Leaf, fd int, before syscall.Stat_t,
	stat func(int, *syscall.Stat_t) error, pread func(int, []byte, int64) (int, error)) {
	var opened syscall.Stat_t
	if err := stat(fd, &opened); err != nil {
		incompleteOperation(o, "fstat-leaf", err)
		return
	}
	if !fd1LeafMatches(opened, l, r.MaxBytes) || !stableFD(before, opened) {
		incompleteOperation(o, "leaf-identity", errors.New("leaf changed before open"))
		return
	}
	buf := make([]byte, int(*l.Size)+1)
	n, err := pread(fd, buf, 0)
	if n > 0 {
		o.Facts = append(o.Facts, known("read_success", "true"))
	}
	if err != nil {
		incompleteOperation(o, "pread", err)
		return
	}
	var after syscall.Stat_t
	if err := stat(fd, &after); err != nil {
		incompleteOperation(o, "postread-fstat", err)
		return
	}
	if !stableFD(opened, after) || int64(n) != *l.Size {
		incompleteOperation(o, "postread-identity", errors.New("leaf changed or short read"))
		return
	}
	match := syntheticDigest(buf[:n]) == l.SHA256
	o.Facts = append(o.Facts, known("digest_match", strconv.FormatBool(match)))
	if !match {
		incompleteOperation(o, "leaf-compare", errors.New("unexpected synthetic content"))
	}
}

const maxFD1ReadCalls = 128

// Raw nonblocking reads only. The extra byte proves trailing data; exact EOF
// remains mandatory. Both syscall count and monotonic time bound EINTR. Test
// seams exercise interruptions/deadlines/drift without a live producer.
func readFD1Channel(o *Observation, r Request, c *FD1Channel, before syscall.Stat_t,
	read func(int, []byte) (int, error), stat func(int, *syscall.Stat_t) error, now func() time.Time) {
	deadline := now().Add(time.Duration(r.MaxDurationMS) * time.Millisecond)
	buf := make([]byte, c.Length+1)
	total, readKnown := 0, false
	for calls := 0; calls < maxFD1ReadCalls; calls++ {
		if !now().Before(deadline) {
			incompleteOperation(o, "channel-deadline", os.ErrDeadlineExceeded)
			return
		}
		n, err := read(c.FD, buf[total:])
		if n > 0 {
			total += n
			if !readKnown {
				o.Facts = append(o.Facts, known("read_success", "true"))
				readKnown = true
			}
		}
		if !now().Before(deadline) {
			incompleteOperation(o, "channel-deadline", os.ErrDeadlineExceeded)
			return
		}
		if total > c.Length {
			incompleteOperation(o, "channel-frame", errors.New("trailing frame bytes"))
			return
		}
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			incompleteOperation(o, "channel-read", err)
			return
		}
		if n != 0 {
			continue
		}
		o.Facts = append(o.Facts, known("eof", "true"))
		match := total == c.Length && syntheticDigest(buf[:total]) == c.SHA256
		o.Facts = append(o.Facts, known("frame_match", strconv.FormatBool(match)))
		if !match {
			incompleteOperation(o, "channel-frame", errors.New("incomplete or wrong frame"))
			return
		}
		var after syscall.Stat_t
		if err := stat(c.FD, &after); err != nil {
			incompleteOperation(o, "postread-fstat", err)
			return
		}
		if !stableFD(before, after) {
			incompleteOperation(o, "postread-identity", errors.New("channel metadata changed"))
		}
		return
	}
	incompleteOperation(o, "channel-budget", errors.New("read syscall budget exhausted"))
}
