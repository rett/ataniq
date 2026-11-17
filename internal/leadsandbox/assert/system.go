package assert

import (
	"errors"
	"io/fs"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Errors a System's ReadFile reports, in addition to the errors of the file
// system (which satisfy fs.ErrNotExist and the like).
var (
	// ErrTooLarge: the file is larger than the limit given.
	ErrTooLarge = errors.New("assert: file is larger than the limit")
	// ErrNotRegular: the path is not a regular file (or is a symbolic link).
	ErrNotRegular = errors.New("assert: not a regular file")
)

// FileInfo is what the checks need to know about a path: its type and mode
// bits, and its size.
type FileInfo struct {
	Mode fs.FileMode
	Size int64
}

// IsDir says the path is a folder.
func (i FileInfo) IsDir() bool { return i.Mode.IsDir() }

// IsRegular says the path is a regular file.
func (i FileInfo) IsRegular() bool { return i.Mode.IsRegular() }

// IsSymlink says the path is a symbolic link.
func (i FileInfo) IsSymlink() bool { return i.Mode&fs.ModeSymlink != 0 }

// Describe names the type of the path for a detail.
func (i FileInfo) Describe() string {
	switch {
	case i.IsDir():
		return "a folder"
	case i.IsRegular():
		return "a regular file"
	case i.IsSymlink():
		return "a symbolic link"
	}
	return "a special file"
}

// System is what the inside checks may do: ask about files, the mount table
// and the environment, dial and resolve with a deadline, and ask the kernel
// whether a path is writable without writing. It has no way to create a file,
// to start a process or to send a byte on a connection. [HostSystem] is the
// real one (Linux); tests pass a fake.
type System interface {
	// Lstat reports on the path itself: a symbolic link is reported, not
	// followed. Every check refuses a link where it expects a file or a
	// folder, so following one would hide it.
	Lstat(path string) (FileInfo, error)
	// ReadFile returns the content of a regular file, at most limit bytes. A
	// file over the limit is an error that wraps ErrTooLarge, and one that is
	// not regular, or is a link, an error that wraps ErrNotRegular. It never
	// blocks on a FIFO.
	ReadFile(path string, limit int) ([]byte, error)
	// Mounts returns the mount table of the process, parsed and bounded
	// (see ParseMountInfo).
	Mounts() ([]Mount, error)
	// Environ returns the environment in the form of os.Environ.
	Environ() []string
	// Dial connects to the address, with the timeout, and closes the
	// connection at once without sending anything. It returns nil when the
	// connection was made. The network is "tcp" or "unix"; an abstract socket
	// is "@name".
	Dial(network, address string, timeout time.Duration) error
	// LookupHost resolves a name within the timeout.
	LookupHost(host string, timeout time.Duration) ([]string, error)
	// Writable asks the kernel whether the calling process may write the path
	// (access(2) with W_OK), without writing. False with a nil error is a
	// plain refusal (a read-only mount or no permission); the error is for a
	// question that could not be asked.
	Writable(path string) (bool, error)
}

// Bounds on the mount table.
const (
	// MaxMountTableBytes is the most of the mount table that is read.
	MaxMountTableBytes = 4 << 20
	// MaxMounts is the most mounts ParseMountInfo returns.
	MaxMounts = 20000
)

// Mount is one line of the mount table (proc_pid_mountinfo(5)).
type Mount struct {
	ID       int
	ParentID int
	// Root is the path inside the file system that forms the mount's root:
	// for a bind mount, where in the source file system it was taken.
	Root string
	// Point is where the mount is, in the mount namespace of the process.
	Point string
	// Options are the per-mount options (the sixth field), such as "ro".
	Options string
	FSType  string
	Source  string
	// SuperOptions are the per-superblock options (the last field).
	SuperOptions string
}

// ReadOnly says the mount is read-only: its own options or its file system's
// say "ro".
func (m Mount) ReadOnly() bool {
	return hasOption(m.Options, "ro") || hasOption(m.SuperOptions, "ro")
}

func hasOption(opts, want string) bool {
	for _, o := range strings.Split(opts, ",") {
		if o == want {
			return true
		}
	}
	return false
}

// ParseMountInfo parses the content of /proc/self/mountinfo. It refuses more
// than MaxMounts lines and a line that has too few fields, a number that is
// not a number, or an escape that is not valid. Spaces, tabs, newlines and
// backslashes in a root, mount point or source, which the kernel writes as
// \040, \011, \012 and \134, are decoded.
func ParseMountInfo(data []byte) ([]Mount, error) {
	var out []Mount
	for n, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		if len(out) >= MaxMounts {
			return nil, refuse("mount table", "more than %d mounts", MaxMounts)
		}
		m, err := parseMountLine(line)
		if err != nil {
			return nil, refuse("mount table", "line %d: %s", n+1, err)
		}
		out = append(out, m)
	}
	return out, nil
}

func parseMountLine(line string) (Mount, error) {
	f := strings.Split(line, " ")
	sep := -1
	for i := 6; i < len(f); i++ {
		if f[i] == "-" {
			sep = i
			break
		}
	}
	if sep < 0 || len(f) < sep+4 {
		return Mount{}, errors.New("too few fields")
	}
	id, err := strconv.Atoi(f[0])
	if err != nil {
		return Mount{}, errors.New("mount id is not a number")
	}
	parent, err := strconv.Atoi(f[1])
	if err != nil {
		return Mount{}, errors.New("parent id is not a number")
	}
	root, err := unescapeMount(f[3])
	if err != nil {
		return Mount{}, err
	}
	point, err := unescapeMount(f[4])
	if err != nil {
		return Mount{}, err
	}
	src, err := unescapeMount(f[sep+2])
	if err != nil {
		return Mount{}, err
	}
	return Mount{
		ID: id, ParentID: parent, Root: root, Point: point,
		Options: f[5], FSType: f[sep+1], Source: src, SuperOptions: f[sep+3],
	}, nil
}

// unescapeMount decodes the \ooo escapes of a mountinfo field.
func unescapeMount(s string) (string, error) {
	if !strings.Contains(s, `\`) {
		return s, nil
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' {
			b.WriteByte(s[i])
			continue
		}
		if i+3 >= len(s) {
			return "", errors.New("truncated escape")
		}
		v, err := strconv.ParseUint(s[i+1:i+4], 8, 8)
		if err != nil {
			return "", errors.New("invalid escape")
		}
		b.WriteByte(byte(v))
		i += 3
	}
	return b.String(), nil
}

// mountFor returns the mount that holds the path: the one with the longest
// mount point that is the path or one of its parents. Of several mounts at
// the same point, the last in the table is on top.
func mountFor(mounts []Mount, path string) (Mount, bool) {
	var best Mount
	found := false
	for _, m := range mounts {
		if !within(path, m.Point) {
			continue
		}
		if !found || len(m.Point) >= len(best.Point) {
			best, found = m, true
		}
	}
	return best, found
}

// within says p is dir or lies below it. Both are clean absolute paths.
func within(p, dir string) bool {
	if dir == "/" {
		return true
	}
	return p == dir || strings.HasPrefix(p, dir+"/")
}

// absent says the error means the path does not exist, as opposed to a path
// that cannot be asked about.
func absent(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)
}
