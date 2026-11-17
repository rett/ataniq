package assert

import (
	"errors"
	"io/fs"
	"path"
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
	// ErrNoMountID: the kernel gives no mount ID for a path (statx without
	// STATX_MNT_ID, before Linux 5.8, or not at all).
	ErrNoMountID = errors.New("assert: the kernel gives no mount ID")
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
	// MountID returns the ID of the mount that holds the path, the number
	// that the first field of the mount table gives to a mount (statx with
	// STATX_MNT_ID), without following a link at the path. A path under a
	// stack of mounts at one place gets the ID of the one on top, which is the
	// one a process sees. An error that wraps ErrNoMountID says the kernel
	// does not give it.
	MountID(path string) (int, error)
	// DirEmpty says whether the folder at the path holds no entry. It does
	// not follow a link at the path, and it reads at most one entry. An error
	// (the path is not a folder, or cannot be read) is a question that could
	// not be asked.
	DirEmpty(path string) (bool, error)
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

// value returns the field of the mount that relatedMount names.
func (m Mount) value(field string) string {
	switch field {
	case "root":
		return m.Root
	case "source":
		return m.Source
	}
	return m.Point
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

// mountByID returns the mount of the table with the ID.
func mountByID(mounts []Mount, id int) (Mount, bool) {
	for _, m := range mounts {
		if m.ID == id {
			return m, true
		}
	}
	return Mount{}, false
}

// related says the path p is, lies below or above the path q, comparing whole
// components, not text: "/a/bc" is not related to "/a/b". Both are made clean
// first; the root is related to every path, so it is never related here.
func related(p, q string) bool {
	if !strings.HasPrefix(p, "/") || !strings.HasPrefix(q, "/") {
		return false
	}
	p, q = path.Clean(p), path.Clean(q)
	if p == "/" || q == "/" {
		return false
	}
	return within(p, q) || within(q, p)
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
