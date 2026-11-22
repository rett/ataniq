package noaicheck

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"slices"
	"strings"
	"syscall"
	"time"
)

// maxLinks bounds the links followed while resolving one path, as the kernel
// does, so that a link loop ends with an error instead of a hang.
const maxLinks = 40

// errLoop is returned when resolving a path follows more than maxLinks links.
var errLoop = errors.New("too many levels of symbolic links")

// errNotRegular is returned when a file to read is not a regular file.
var errNotRegular = errors.New("not a regular file")

// errTooLarge is returned when a file is larger than the reader allows.
var errTooLarge = errors.New("file too large")

// errChanged is returned when the file opened is not the one looked at
// before opening it: it was replaced in between.
var errChanged = errors.New("changed while opened")

// errBudget is returned by every read once the run's budget is spent. It is
// never reported where it happens: the report holds one "incomplete budget"
// hit for it (see Checker.Report).
var errBudget = errors.New("budget spent")

// errDirent is returned for a directory listing the kernel gave in a form
// that cannot be read.
var errDirent = errors.New("malformed directory entry")

// limits bound the work of one run, shared by every check: the folder
// entries listed, the bytes of files read, the time since the tree was
// opened, and the memory held for names waiting their turn in a walk and
// for hits.
type limits struct {
	entries int64
	bytes   int64
	time    time.Duration
	memory  int64
}

// defaultLimits are far above what the host holds (a few hundred thousand
// entries) and below the switch's 5-minute limit for the check.
var defaultLimits = limits{entries: 4_000_000, bytes: 1 << 30, time: 4 * time.Minute, memory: 256 << 20}

// The names of the limits, the detail of the "incomplete budget" hit.
const (
	budgetEntries = "entries"
	budgetBytes   = "bytes"
	budgetTime    = "time"
	budgetMemory  = "memory"
)

// budget counts a run's work against its limits. Once one is reached, spent
// names it and every later read fails with errBudget: the run ends early and
// says so, never passes.
type budget struct {
	lim      limits
	deadline time.Time
	entries  int64
	bytes    int64
	memory   int64
	spent    string
}

func newBudget(l limits) *budget {
	return &budget{lim: l, deadline: time.Now().Add(l.time)}
}

// out reports whether the budget is spent, checking the time.
func (b *budget) out() bool {
	if b.spent == "" && !time.Now().Before(b.deadline) {
		b.spent = budgetTime
	}
	return b.spent != ""
}

func (b *budget) exceed(which string) bool {
	if b.spent == "" {
		b.spent = which
	}
	return false
}

// entry counts one listed entry; false when the budget is spent.
func (b *budget) entry() bool {
	if b.out() {
		return false
	}
	b.entries++
	if b.entries > b.lim.entries {
		return b.exceed(budgetEntries)
	}
	return true
}

// read counts n bytes read; false when the budget is spent.
func (b *budget) read(n int64) bool {
	b.bytes += n
	if b.bytes > b.lim.bytes {
		return b.exceed(budgetBytes)
	}
	return !b.out()
}

// hold counts n bytes held until release; false (and nothing held) when
// they do not fit.
func (b *budget) hold(n int64) bool {
	if b.memory+n > b.lim.memory {
		return b.exceed(budgetMemory)
	}
	b.memory += n
	return true
}

func (b *budget) release(n int64) { b.memory -= n }

// tree reads beneath one root folder. Paths are slash-separated and relative
// to the root ("usr/bin", "." for the root itself); a path shown to a person
// is the same path with a leading '/'.
//
// A path is resolved component by component through an [os.Root], so no
// link leads outside the root. A file is opened without following a final
// link and without blocking, and the open descriptor is checked (fstat)
// before a byte is read. A walk opens each folder relative to its parent's
// descriptor, never by path, and lists it in bounded batches.
type tree struct {
	r *os.Root
	b *budget
}

func openTree(dir string) (*tree, error) {
	r, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	return &tree{r: r, b: newBudget(defaultLimits)}, nil
}

func (t *tree) close() error { return t.r.Close() }

// show is the path as a person reads it: absolute within the root.
func show(rel string) string {
	if rel == "." || rel == "" {
		return "/"
	}
	return "/" + rel
}

// clean turns an absolute or relative path into a clean path relative to the
// root; ".." never climbs above the root.
func clean(p string) string {
	p = path.Clean("/" + p)
	if p == "/" {
		return "."
	}
	return p[1:]
}

// resolve follows every link in rel, component by component, within the root:
// an absolute target starts again at the root and ".." stops at it, as for a
// process whose root folder is the tree's root. The result holds no link
// (barring a change while it runs) and names an existing entry.
func (t *tree) resolve(rel string) (string, error) {
	parts := strings.Split(clean(rel), "/")
	done := "."
	links := 0
	for len(parts) > 0 {
		c := parts[0]
		parts = parts[1:]
		switch c {
		case "", ".":
			continue
		case "..":
			done = clean(path.Dir(done))
			continue
		}
		next := path.Join(done, c)
		fi, err := t.r.Lstat(next)
		if err != nil {
			return "", err
		}
		if fi.Mode()&fs.ModeSymlink == 0 {
			done = next
			continue
		}
		links++
		if links > maxLinks {
			return "", &fs.PathError{Op: "resolve", Path: show(rel), Err: errLoop}
		}
		target, err := t.r.Readlink(next)
		if err != nil {
			return "", err
		}
		if strings.HasPrefix(target, "/") {
			done = "."
		}
		parts = append(strings.Split(target, "/"), parts...)
	}
	return done, nil
}

// lstat resolves the folder of rel and stats its last component without
// following it.
func (t *tree) lstat(rel string) (string, fs.FileInfo, error) {
	rel = clean(rel)
	if rel == "." {
		fi, err := t.r.Lstat(".")
		return ".", fi, err
	}
	dir, err := t.resolve(path.Dir(rel))
	if err != nil {
		return "", nil, err
	}
	p := path.Join(dir, path.Base(rel))
	fi, err := t.r.Lstat(p)
	return p, fi, err
}

// readFile resolves rel and reads it when it is a regular file of at most
// max bytes. A FIFO or device is never opened: the entry's type is checked
// first. The open neither follows a final link nor blocks, and the opened
// descriptor must be a regular file and the same file as the one checked,
// so a file swapped in between is refused, not read. The bytes count
// against the run's budget.
func (t *tree) readFile(rel string, max int64) (string, []byte, error) {
	if t.b.out() {
		return "", nil, errBudget
	}
	p, err := t.resolve(rel)
	if err != nil {
		return "", nil, err
	}
	lfi, err := t.r.Lstat(p)
	if err != nil {
		return p, nil, err
	}
	if !lfi.Mode().IsRegular() {
		return p, nil, &fs.PathError{Op: "read", Path: show(p), Err: errNotRegular}
	}
	if testHookBeforeOpen != nil {
		testHookBeforeOpen(p)
	}
	f, err := t.r.OpenFile(p, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW|syscall.O_NOCTTY, 0)
	if err != nil {
		return p, nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return p, nil, err
	}
	if !fi.Mode().IsRegular() {
		return p, nil, &fs.PathError{Op: "read", Path: show(p), Err: errNotRegular}
	}
	if !os.SameFile(lfi, fi) {
		return p, nil, &fs.PathError{Op: "read", Path: show(p), Err: errChanged}
	}
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if !t.b.read(int64(len(data))) {
		return p, nil, errBudget
	}
	if err != nil {
		return p, nil, err
	}
	if int64(len(data)) > max {
		return p, nil, &fs.PathError{Op: "read", Path: show(p), Err: errTooLarge}
	}
	return p, data, nil
}

// testHookBeforeOpen, set by a test only, runs between the type check and
// the open of readFile, where a file can be swapped.
var testHookBeforeOpen func(rel string)

// readlink reads the target of the link rel without following it.
func (t *tree) readlink(rel string) (string, error) {
	return t.r.Readlink(clean(rel))
}

// wentry is one entry a walk meets.
type wentry struct {
	rel   string      // its path from the root
	name  string      // its name in its folder
	typ   fs.FileMode // its type bits: fs.ModeDir, fs.ModeSymlink, 0 for a regular file, ...
	depth int         // 1 for an entry of the folder the walk starts at
	dirfd int         // its folder's descriptor, open while the entry is visited
}

func (e *wentry) isDir() bool     { return e.typ == fs.ModeDir }
func (e *wentry) isLink() bool    { return e.typ == fs.ModeSymlink }
func (e *wentry) isRegular() bool { return e.typ == 0 }

// O_PATH opens an entry to stat it without opening the file itself, so
// that no device or FIFO is ever opened for a stat (Linux).
const oPath = 0x200000

// statAt stats the entry name of the folder dirfd without following it and
// returns its type and permission bits.
func statAt(dirfd int, name string) (fs.FileMode, error) {
	fd, err := syscall.Openat(dirfd, name, oPath|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return 0, err
	}
	defer syscall.Close(fd)
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		return 0, err
	}
	m := fs.FileMode(st.Mode & 0o777)
	switch st.Mode & syscall.S_IFMT {
	case syscall.S_IFDIR:
		m |= fs.ModeDir
	case syscall.S_IFLNK:
		m |= fs.ModeSymlink
	case syscall.S_IFREG:
	case syscall.S_IFIFO:
		m |= fs.ModeNamedPipe
	case syscall.S_IFSOCK:
		m |= fs.ModeSocket
	case syscall.S_IFCHR:
		m |= fs.ModeDevice | fs.ModeCharDevice
	case syscall.S_IFBLK:
		m |= fs.ModeDevice
	default:
		m |= fs.ModeIrregular
	}
	return m, nil
}

// direntType is the file type of a linux_dirent64 d_type, or false for
// DT_UNKNOWN, which needs a stat.
func direntType(t byte) (fs.FileMode, bool) {
	switch t {
	case syscall.DT_DIR:
		return fs.ModeDir, true
	case syscall.DT_REG:
		return 0, true
	case syscall.DT_LNK:
		return fs.ModeSymlink, true
	case syscall.DT_FIFO:
		return fs.ModeNamedPipe, true
	case syscall.DT_SOCK:
		return fs.ModeSocket, true
	case syscall.DT_CHR:
		return fs.ModeDevice | fs.ModeCharDevice, true
	case syscall.DT_BLK:
		return fs.ModeDevice, true
	}
	return 0, false
}

// direntBuf is the size of one getdents read, and dirBatch the most entries
// of one folder held at a time. A folder of up to dirBatch entries is
// visited in name order; a larger one in batches, each in name order.
const (
	direntBuf = 32 << 10
	dirBatch  = 1024
)

// dent is a listed entry before it is visited.
type dent struct {
	name string
	typ  fs.FileMode
}

// dirStream reads one open folder's entries in batches.
type dirStream struct {
	fd   int
	buf  []byte
	n    int
	off  int
	done bool
}

// next returns the folder's next batch, sorted by name, and the memory it
// holds (to release once the batch is visited); an empty batch at the end.
func (t *tree) next(d *dirStream) ([]dent, int64, error) {
	var batch []dent
	var held int64
	for len(batch) < dirBatch {
		if d.off >= d.n {
			if d.done {
				break
			}
			n, err := syscall.ReadDirent(d.fd, d.buf)
			if errors.Is(err, syscall.EINTR) {
				continue
			}
			if err != nil {
				return batch, held, err
			}
			if n <= 0 {
				d.done = true
				break
			}
			d.n, d.off = n, 0
		}
		const head = 19 // d_ino, d_off, d_reclen, d_type
		if d.n-d.off < head {
			return batch, held, errDirent
		}
		rec := d.buf[d.off:d.n]
		reclen := int(binary.NativeEndian.Uint16(rec[16:18]))
		if reclen < head || reclen > len(rec) {
			return batch, held, errDirent
		}
		d.off += reclen
		name := rec[head:reclen]
		if i := bytes.IndexByte(name, 0); i >= 0 {
			name = name[:i]
		}
		if len(name) == 0 || string(name) == "." || string(name) == ".." {
			continue
		}
		e := dent{name: string(name)}
		typ, ok := direntType(rec[18])
		if !ok {
			m, err := statAt(d.fd, e.name)
			if errors.Is(err, syscall.ENOENT) {
				continue // gone since it was listed
			}
			if err != nil {
				m = fs.ModeIrregular
			}
			typ = m.Type()
		}
		e.typ = typ
		n := int64(len(e.name)) + 48
		if !t.b.hold(n) {
			return batch, held, errBudget
		}
		held += n
		batch = append(batch, e)
	}
	slices.SortFunc(batch, func(a, b dent) int { return strings.Compare(a.name, b.name) })
	return batch, held, nil
}

// walk resolves start, opens the folder it names and calls visit for every
// entry, with its path from the root. When visit returns true for a folder,
// the walk goes into it, down to maxDepth levels (1: start's own entries
// only); a folder visit wants entered below that is not entered, and deep is
// true. Each folder is opened relative to its parent's descriptor without
// following a link, so a walk never follows a link and always ends. A
// folder that cannot be read is passed to fail, one that went away is not.
// The start's own error is returned (with the resolved start when there is
// one), not passed to fail. Every entry counts against the budget; once it
// is spent the walk stops and returns errBudget.
func (t *tree) walk(start string, maxDepth int, visit func(e *wentry) bool, fail func(rel string, err error)) (p string, deep bool, err error) {
	if t.b.out() {
		return "", false, errBudget
	}
	p, err = t.resolve(start)
	if err != nil {
		return "", false, err
	}
	f, err := t.r.OpenFile(p, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return p, false, err
	}
	defer f.Close()
	w := walker{t: t, maxDepth: maxDepth, visit: visit, fail: fail}
	if err := w.dir(int(f.Fd()), p, 1); err != nil {
		return p, w.deep, err
	}
	return p, w.deep, nil
}

type walker struct {
	t        *tree
	maxDepth int
	visit    func(e *wentry) bool
	fail     func(rel string, err error)
	deep     bool
}

// dir visits the entries of the open folder fd, at path dir and depth.
// Only errBudget is returned; every other error goes to fail.
func (w *walker) dir(fd int, dir string, depth int) error {
	if !w.t.b.hold(direntBuf) {
		return errBudget
	}
	defer w.t.b.release(direntBuf)
	d := &dirStream{fd: fd, buf: make([]byte, direntBuf)}
	for {
		batch, held, err := w.t.next(d)
		if err != nil {
			w.t.b.release(held)
			if errors.Is(err, errBudget) {
				return err
			}
			w.fail(dir, &fs.PathError{Op: "readdir", Path: show(dir), Err: err})
			return nil
		}
		if len(batch) == 0 {
			return nil
		}
		err = w.batch(fd, dir, depth, batch)
		w.t.b.release(held)
		if err != nil {
			return err
		}
	}
}

func (w *walker) batch(fd int, dir string, depth int, batch []dent) error {
	for _, d := range batch {
		if !w.t.b.entry() {
			return errBudget
		}
		e := wentry{rel: path.Join(dir, d.name), name: d.name, typ: d.typ, depth: depth, dirfd: fd}
		if !w.visit(&e) || !e.isDir() {
			continue
		}
		if depth >= w.maxDepth {
			w.deep = true
			continue
		}
		cfd, err := syscall.Openat(fd, d.name, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
		if errors.Is(err, syscall.EINTR) {
			cfd, err = syscall.Openat(fd, d.name, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
		}
		if err != nil {
			if !errors.Is(err, syscall.ENOENT) {
				w.fail(e.rel, &fs.PathError{Op: "open", Path: show(e.rel), Err: err})
			}
			continue
		}
		err = w.dir(cfd, e.rel, depth+1)
		syscall.Close(cfd)
		if err != nil {
			return err
		}
	}
	return nil
}

// describe is an error's kind without its path, for a person.
func describe(err error) string {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		err = pe.Err
	}
	var le *os.LinkError
	if errors.As(err, &le) {
		err = le.Err
	}
	return fmt.Sprint(err)
}
