//go:build linux

package sqlitesettlement

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// Metadata observation never opens main/WAL/SHM, and never duplicates or closes
// a SQLite descriptor. Only our owned directory descriptors may be duplicated.
type fileIdentity struct {
	Present       bool
	Device, Inode uint64
	Mode, UID     uint32
	Links         uint64
	Size          int64
}

func identity(s unix.Stat_t) fileIdentity {
	return fileIdentity{true, uint64(s.Dev), s.Ino, s.Mode, s.Uid, uint64(s.Nlink), s.Size}
}

func (a fileIdentity) same(b fileIdentity) bool {
	return a.Present && b.Present && a.Device == b.Device && a.Inode == b.Inode && a.Mode == b.Mode && a.UID == b.UID && a.Links == b.Links
}

func descriptorIdentity(fd int) (fileIdentity, error) {
	var s unix.Stat_t
	err := unix.Fstat(fd, &s)
	return identity(s), err
}

func namedIdentity(fd int, name string) (fileIdentity, error) {
	var s unix.Stat_t
	err := unix.Fstatat(fd, name, &s, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return fileIdentity{}, nil
	}
	return identity(s), err
}

func regularIdentity(s fileIdentity) bool {
	return s.Present && s.Mode == unix.S_IFREG|0600 && s.UID == uint32(os.Geteuid()) && s.Links == 1 && s.Size >= 0 && s.Size <= 2*1024*1024
}

// Walk an existing directory without following any symlink, including ancestors.
// Only the final fixture directory must be private; this is not provisioning.
func openPinnedDirectory(path string) (*os.File, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("directory must be absolute")
	}
	parts := strings.Split(strings.TrimPrefix(filepath.Clean(path), "/"), "/")
	if len(parts) > 128 {
		return nil, errors.New("directory walk bound")
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	for _, part := range parts {
		next, e := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		unix.Close(fd)
		if e != nil {
			return nil, e
		}
		fd = next
	}
	s, err := descriptorIdentity(fd)
	if err != nil || s.Mode != unix.S_IFDIR|0700 || s.UID != uint32(os.Geteuid()) {
		unix.Close(fd)
		return nil, errors.New("private owned directory required")
	}
	return os.NewFile(uintptr(fd), "pinned-directory"), nil
}

type identityObservation struct {
	Stage                                                       string
	Directory, Alias, NamedDirectory, Main, NamedMain, WAL, SHM fileIdentity
	MainFD, WALFD                                               int
	OK                                                          bool
	Live                                                        bool
	SQLiteMain, SQLiteWAL                                       fileIdentity
}

type directoryBoundary struct {
	Stage                            string
	Before, Attempted, Synced, After bool
	Error                            string
}

type checkpointReceipt struct {
	Attempted, Row, Integers, EOF, RowsClosed bool
	Busy, Log, Checkpointed                   int64
	Error, RowsCloseError                     string
}

func (c checkpointReceipt) complete() bool {
	return c.Attempted && c.Row && c.Integers && c.EOF && c.RowsClosed && c.Error == "" && c.RowsCloseError == "" && c.Busy == 0 && c.Log > 0 && c.Log == c.Checkpointed
}

type selectorReceipt struct {
	Target                     string
	FD, OtherFD                int
	Identity                   fileIdentity
	OtherIdentity              fileIdentity
	Revalidated                bool
	Before, Denied, Unaffected [2]string
}

type lifecycleReceipt struct {
	Fresh, WALAbsentBefore, FailStop, CloseAttempted bool
	CloseError                                       string
	MainFD, WALFD, DirectoryFD, AliasFD              int
	BoundMain, BoundDirectory, BoundWAL              fileIdentity
	Observations                                     []identityObservation
	Boundaries                                       []directoryBoundary
	Selector                                         *selectorReceipt `json:",omitempty"`
	Checkpoint                                       checkpointReceipt
}

type lifecycleOwner struct {
	path             string
	directory, alias *os.File
	main, dir, wal   fileIdentity
	mainFD, walFD    int
}

// Root these child-owned wrappers through os.Exit, including receipt encoding.
// A finalizer must not close a selected directory fd and permit number reuse.
var lifecyclePinnedFiles []*os.File

func acquireLifecycle(path string, r *receipt) (*lifecycleOwner, error) {
	o := &lifecycleOwner{path: path, directory: os.NewFile(4, "inherited-directory"), mainFD: -1, walFD: -1}
	lifecyclePinnedFiles = append(lifecyclePinnedFiles, o.directory)
	var err error
	o.main, err = descriptorIdentity(3)
	if err != nil || !regularIdentity(o.main) {
		return nil, errors.New("owned main metadata")
	}
	o.dir, err = descriptorIdentity(4)
	if err != nil || o.dir.Mode != unix.S_IFDIR|0700 || o.dir.UID != uint32(os.Geteuid()) {
		return nil, errors.New("owned directory metadata")
	}
	fd, err := unix.FcntlInt(4, unix.F_DUPFD_CLOEXEC, 5)
	if err != nil {
		return nil, err
	}
	o.alias = os.NewFile(uintptr(fd), "directory-sync-alias")
	lifecyclePinnedFiles = append(lifecyclePinnedFiles, o.alias)
	r.Lifecycle.DirectoryFD, r.Lifecycle.AliasFD = 4, fd
	r.Lifecycle.BoundMain, r.Lifecycle.BoundDirectory = o.main, o.dir
	if !o.check(r, "acquired", false) {
		return nil, errors.New("acquisition identity mismatch")
	}
	return o, nil
}

// Enumerate at most 128 names, close the enumeration, then fstat numbers only.
// Unavailable/ambiguous observation is a required capability gap, never a guess.
func selectSQLiteFD(want fileIdentity, excluded map[int]bool) (int, error) {
	dir, err := os.Open("/proc/self/fd")
	if err != nil {
		return -1, err
	}
	names, readErr := dir.Readdirnames(129)
	closeErr := dir.Close()
	if (readErr != nil && readErr != io.EOF) || closeErr != nil {
		return -1, errors.Join(readErr, closeErr)
	}
	if len(names) > 128 {
		return -1, errors.New("proc descriptor enumeration exceeds 128")
	}
	selected := -1
	for _, name := range names {
		fd, err := strconv.Atoi(name)
		if err != nil || fd < 0 {
			return -1, errors.New("unexpected proc descriptor name")
		}
		if excluded[fd] {
			continue
		}
		got, err := descriptorIdentity(fd)
		if errors.Is(err, unix.EBADF) {
			continue
		} // includes the closed enumeration
		if err != nil {
			return -1, err
		}
		if got.same(want) {
			if selected != -1 {
				return -1, errors.New("ambiguous SQLite descriptor identity")
			}
			selected = fd
		}
	}
	if selected < 0 {
		return -1, errors.New("no eligible SQLite descriptor")
	}
	return selected, nil
}

func (o *lifecycleOwner) bind(r *receipt) bool {
	var err error
	o.wal, err = namedIdentity(4, filepath.Base(o.path)+"-wal")
	if err != nil || !regularIdentity(o.wal) {
		r.Incomplete = "present regular WAL could not be bound"
		return false
	}
	exclude := map[int]bool{3: true, 4: true, int(o.alias.Fd()): true}
	o.mainFD, err = selectSQLiteFD(o.main, exclude)
	if err == nil {
		o.walFD, err = selectSQLiteFD(o.wal, exclude)
	}
	if err != nil {
		r.Incomplete = "procfs descriptor binding: " + err.Error()
		return false
	}
	r.Lifecycle.BoundWAL = o.wal
	r.Lifecycle.MainFD, r.Lifecycle.WALFD = o.mainFD, o.walFD
	return o.check(r, "bind-incarnation", true)
}

func (o *lifecycleOwner) check(r *receipt, stage string, live bool) bool {
	v := identityObservation{Stage: stage, MainFD: o.mainFD, WALFD: o.walFD, Live: live}
	var errs []error
	get := func(dst *fileIdentity, value fileIdentity, err error) {
		*dst = value
		if err != nil {
			errs = append(errs, err)
		}
	}
	a, e := descriptorIdentity(4)
	get(&v.Directory, a, e)
	a, e = descriptorIdentity(int(o.alias.Fd()))
	get(&v.Alias, a, e)
	a, e = descriptorIdentity(3)
	get(&v.Main, a, e)
	a, e = namedIdentity(4, filepath.Base(o.path))
	get(&v.NamedMain, a, e)
	a, e = namedIdentity(4, filepath.Base(o.path)+"-wal")
	get(&v.WAL, a, e)
	a, e = namedIdentity(4, filepath.Base(o.path)+"-shm")
	get(&v.SHM, a, e)
	named, err := openPinnedDirectory(filepath.Dir(o.path))
	if err == nil {
		a, e = descriptorIdentity(int(named.Fd()))
		get(&v.NamedDirectory, a, e)
		errs = append(errs, named.Close())
	} else {
		errs = append(errs, errors.New("named directory acquisition failed"))
	}
	ok := o.dir.same(v.Directory) && o.dir.same(v.Alias) && o.dir.same(v.NamedDirectory) && o.main.same(v.Main) && o.main.same(v.NamedMain) && regularIdentity(v.NamedMain)
	for _, side := range []fileIdentity{v.WAL, v.SHM} {
		if side.Present && !regularIdentity(side) {
			ok = false
		}
	}
	journal, err := namedIdentity(4, filepath.Base(o.path)+"-journal")
	if err != nil || journal.Present {
		ok = false
	}
	if live {
		mainFD, e1 := descriptorIdentity(o.mainFD)
		walFD, e2 := descriptorIdentity(o.walFD)
		v.SQLiteMain, v.SQLiteWAL = mainFD, walFD
		ok = ok && e1 == nil && e2 == nil && o.main.same(mainFD) && o.wal.same(walFD) && o.wal.same(v.WAL)
	}
	v.OK = ok && errors.Join(errs...) == nil
	r.Lifecycle.Observations = append(r.Lifecycle.Observations, v)
	if !v.OK {
		r.record(stage, errors.New("pinned/named/SQLite identity mismatch"))
	} else {
		r.record(stage, nil)
	}
	return v.OK
}

func (o *lifecycleOwner) boundary(r *receipt, stage string, live bool) bool {
	b := directoryBoundary{Stage: stage}
	b.Before = o.check(r, stage+"-before", live)
	if b.Before {
		b.Attempted = true
		err := o.alias.Sync()
		b.Synced = err == nil
		if err != nil {
			b.Error = err.Error()
		}
		r.record(stage, err)
		b.After = o.check(r, stage+"-after", live)
	}
	r.Lifecycle.Boundaries = append(r.Lifecycle.Boundaries, b)
	return b.Before && b.Attempted && b.Synced && b.After && b.Error == ""
}

// Rows must be fully consumed and closed even when Next reports a native error.
func fullCheckpoint(ctx context.Context, c connection) (p checkpointReceipt, err error) {
	p.Attempted = true
	rows, err := c.QueryContext(ctx, "PRAGMA main.wal_checkpoint(FULL)", nil)
	if err != nil {
		p.Error = err.Error()
		return p, err
	}
	defer func() {
		closeErr := rows.Close()
		p.RowsClosed = true
		if closeErr != nil {
			p.RowsCloseError = closeErr.Error()
		}
		err = errors.Join(err, closeErr)
		if err != nil {
			p.Error = err.Error()
		}
	}()
	if len(rows.Columns()) != 3 {
		return p, errors.New("checkpoint column count")
	}
	v := make([]driver.Value, 3)
	if err = rows.Next(v); err != nil {
		return p, err
	}
	p.Row = true
	var a, b, d bool
	p.Busy, a = v[0].(int64)
	p.Log, b = v[1].(int64)
	p.Checkpointed, d = v[2].(int64)
	p.Integers = a && b && d
	if !p.Integers {
		return p, errors.New("checkpoint integer types")
	}
	err = rows.Next(v)
	p.EOF = err == io.EOF
	if !p.EOF {
		return p, fmt.Errorf("checkpoint extra row/read error: %v", err)
	}
	return p, nil
}

func (o *lifecycleOwner) denySelected(r *receipt, target string) bool {
	fd, other, want, wantOther := int(o.alias.Fd()), 4, o.dir, o.dir
	if target == "sqlite-main" {
		fd, other, want, wantOther = o.mainFD, o.walFD, o.main, o.wal
		selected, err := selectSQLiteFD(o.main, map[int]bool{3: true, 4: true, int(o.alias.Fd()): true})
		if err != nil || selected != fd {
			r.Incomplete = "late main selector no longer unique/stable"
			return false
		}
	}
	s := &selectorReceipt{Target: target, FD: fd, OtherFD: other, Identity: want, OtherIdentity: wantOther}
	r.Lifecycle.Selector = s
	s.Before = [2]string{fmt.Sprint(unix.Fsync(fd)), fmt.Sprint(unix.Fsync(other))}
	got, err := descriptorIdentity(fd)
	otherGot, otherErr := descriptorIdentity(other)
	s.Revalidated = err == nil && otherErr == nil && want.same(got) && wantOther.same(otherGot)
	if s.Before != [2]string{"<nil>", "<nil>"} || !s.Revalidated {
		r.Incomplete = "selector identity/unfiltered controls"
		return false
	}
	stage, err := installSelectedSyncDenial(fd)
	r.record(stage, err)
	if err != nil {
		r.Incomplete = "selected sync-denial capability: " + err.Error()
		return false
	}
	s.Denied = [2]string{fmt.Sprint(unix.Fsync(fd)), fmt.Sprint(unix.Fdatasync(fd))}
	// fdatasync on directories is not portable even where fsync is supported.
	s.Unaffected = [2]string{fmt.Sprint(unix.Fsync(other)), fmt.Sprint(unix.Fsync(other))}
	if s.Denied != [2]string{unix.EIO.Error(), unix.EIO.Error()} || s.Unaffected != [2]string{"<nil>", "<nil>"} {
		r.Incomplete = "selected denial controls"
		return false
	}
	return true
}
