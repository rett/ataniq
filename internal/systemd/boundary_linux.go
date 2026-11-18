package systemd

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

// cgroupRoot is the unified-hierarchy mount. Tests replace it with a
// temporary tree; nothing else does.
var cgroupRoot = "/sys/fs/cgroup"

// FileIdentity names a file object by device, inode and owner. It is the same
// object as before only while all three are equal, and a number can be reused
// after the object is removed.
type FileIdentity struct {
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
	UID    uint32 `json:"uid"`
}

// IdentityOf returns the identity recorded in fi, which must come from a
// stat call on Linux. It panics when fi carries no raw stat result.
func IdentityOf(fi os.FileInfo) FileIdentity {
	s := fi.Sys().(*syscall.Stat_t)
	return FileIdentity{uint64(s.Dev), uint64(s.Ino), s.Uid}
}

// Boundary is a control group by path below the unified hierarchy root, such
// as "/user.slice/unit.service", with the identity of its directory. Path
// begins with "/" and is never "/".
type Boundary struct {
	Path     string       `json:"path"`
	Identity FileIdentity `json:"identity"`
}

// absolute reports whether path is a clean absolute path of printable text
// without characters that the qualification subjects forbid.
func absolute(path string) bool {
	if !utf8.ValidString(path) || len(path) > 511 || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return false
	}
	if strings.ContainsAny(path, " %\\\"'") {
		return false
	}
	return strings.IndexFunc(path, func(r rune) bool { return unicode.IsControl(r) || r == unicode.ReplacementChar }) < 0
}

// openAbsolute opens each component without following links; flags apply to
// the last component.
func openAbsolute(path string, flags int) (*os.File, error) {
	if !absolute(path) {
		return nil, errors.New("invalid absolute path")
	}
	d, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for i, p := range parts {
		f := unix.O_RDONLY | unix.O_DIRECTORY
		if i == len(parts)-1 {
			f = flags
		}
		n, e := unix.Openat(d, p, f|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		unix.Close(d)
		if e != nil {
			return nil, e
		}
		d = n
	}
	return os.NewFile(uintptr(d), path), nil
}

// readKernel reads a small kernel file and fails when it is longer than cap.
func readKernel(path string, cap int64) ([]byte, error) {
	f, err := openAbsolute(path, unix.O_RDONLY)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, cap+1))
	if err != nil || int64(len(b)) > cap {
		return nil, errors.New("kernel read failed or excessive")
	}
	return b, nil
}

// BoundaryOf opens the cgroup directory at path below the unified hierarchy
// without following links and returns its identity. The path must be clean,
// absolute, not "/", and free of NUL, newline and carriage return; the root
// is refused on purpose, because cleanup authority never covers it. BoundaryOf
// does not prove which unit, if any, owns the directory.
func BoundaryOf(path string) (Boundary, error) {
	if !strings.HasPrefix(path, "/") || filepath.Clean(path) != path || path == "/" || strings.ContainsAny(path, "\x00\n\r") {
		return Boundary{}, errors.New("invalid cgroup path")
	}
	f, err := openAbsolute(cgroupRoot+path, unix.O_RDONLY|unix.O_DIRECTORY)
	if err != nil {
		return Boundary{}, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return Boundary{}, err
	}
	return Boundary{path, IdentityOf(fi)}, nil
}

// Populated reports whether the control group has any process, from the
// "populated" line of cgroup.events. The directory identity must equal want
// before and after the read, and exactly one populated line with the value 0
// or 1 must be present; anything else is an error and never an answer. The
// answer is true or false only for the instant of the read: a process may
// enter or leave afterwards, so callers that need an emptiness proof read it
// together with the unit state.
func Populated(want Boundary) (bool, error) {
	before, err := BoundaryOf(want.Path)
	if err != nil || before != want {
		return false, errors.New("cgroup identity missing or changed")
	}
	b, err := readKernel(cgroupRoot+want.Path+"/cgroup.events", 4096)
	if err != nil {
		return false, err
	}
	seen := false
	value := false
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			return false, errors.New("invalid cgroup events")
		}
		if f[0] == "populated" {
			if seen || f[1] != "0" && f[1] != "1" {
				return false, errors.New("invalid populated value")
			}
			seen = true
			value = f[1] == "1"
		}
	}
	after, err := BoundaryOf(want.Path)
	if err != nil || after != want || !seen {
		return false, errors.New("unproved boundary population")
	}
	return value, nil
}
