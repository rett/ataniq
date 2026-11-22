package switcher

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"syscall"
	"time"
)

// readDirBatch is how many entries of a folder are read at a time.
const readDirBatch = 256

// walkTree calls fn for every entry below the folder r (not r itself), with
// its path relative to r and what Lstat returned, a folder before what it
// holds. It reads each folder in batches and holds one folder open at a time;
// a folder that is replaced while it is opened is an error. fn sees links,
// devices and the rest as they are: refusing them is its job.
func walkTree(r *os.Root, fn func(rel string, info fs.FileInfo) error) error {
	dirs := []string{"."}
	for len(dirs) > 0 {
		dir := dirs[len(dirs)-1]
		dirs = dirs[:len(dirs)-1]
		subs, err := walkDir(r, dir, fn)
		if err != nil {
			return err
		}
		dirs = append(dirs, subs...)
	}
	return nil
}

// walkDir calls fn for each entry of the folder dir of r and returns its
// folders.
func walkDir(r *os.Root, dir string, fn func(rel string, info fs.FileInfo) error) ([]string, error) {
	before, err := r.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !before.IsDir() {
		return nil, failf(CodeBadEntry, "%s is not a folder", q(dir))
	}
	f, err := r.OpenFile(dir, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if after, err := f.Stat(); err != nil || !os.SameFile(before, after) {
		return nil, failf(CodeFileChanged, "%s changed while it was read", q(dir))
	}
	var subs []string
	for {
		ents, err := f.ReadDir(readDirBatch)
		for _, ent := range ents {
			rel := path.Join(dir, ent.Name())
			info, err := r.Lstat(rel)
			if err != nil {
				return nil, err
			}
			if err := fn(rel, info); err != nil {
				return nil, err
			}
			if info.IsDir() {
				subs = append(subs, rel)
			}
		}
		if errors.Is(err, io.EOF) {
			return subs, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

// links is the number of hard links of what info describes; 0 when it cannot
// be read.
func links(info fs.FileInfo) uint64 {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Nlink)
	}
	return 0
}

// entryKind refuses what a release may not hold: anything but a folder or a
// regular file with one link.
func entryKind(rel string, info fs.FileInfo) error {
	switch m := info.Mode(); {
	case m&fs.ModeSymlink != 0:
		return failf(CodeBadEntry, "%s is a symbolic link; a release holds only folders and regular files", q(rel))
	case m.IsDir():
		return nil
	case !m.IsRegular():
		return failf(CodeBadEntry, "%s is not a regular file", q(rel))
	case links(info) != 1:
		return failf(CodeBadEntry, "%s has more than one hard link", q(rel))
	}
	return nil
}

// specialBits are the mode bits a release never holds.
const specialBits = fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky

// entryTrusted refuses an entry of a release that could have been changed
// since it was imported, or that runs with other rights: one writable by its
// group or others, one with a set-user-id, set-group-id or sticky bit, or one
// whose owner the owner check refuses.
func (s *Switcher) entryTrusted(rel string, info fs.FileInfo) error {
	switch {
	case info.Mode().Perm()&0o022 != 0:
		return failf(CodeUntrusted, "%s is writable by its group or by others", q(rel))
	case info.Mode()&specialBits != 0:
		return failf(CodeUntrusted, "%s has a set-id or sticky bit", q(rel))
	}
	if err := s.cfg.Owner(info); err != nil {
		return failf(CodeUntrusted, "%s: %s", q(rel), q(err.Error()))
	}
	return nil
}

// Limits bound the work of reading a release: the import and the hashing.
// A zero field takes the default.
type Limits struct {
	Entries   int           // folders and files of one release
	Bytes     int64         // bytes of all the files of one release
	FileBytes int64         // bytes of one file
	Time      time.Duration // the import and the hashing together
}

// DefaultLimits are the limits of a switch.
var DefaultLimits = Limits{
	Entries:   20000,
	Bytes:     2 << 30,
	FileBytes: 512 << 20,
	Time:      10 * time.Minute,
}

func (l Limits) withDefaults() Limits {
	if l.Entries == 0 {
		l.Entries = DefaultLimits.Entries
	}
	if l.Bytes == 0 {
		l.Bytes = DefaultLimits.Bytes
	}
	if l.FileBytes == 0 {
		l.FileBytes = DefaultLimits.FileBytes
	}
	if l.Time == 0 {
		l.Time = DefaultLimits.Time
	}
	return l
}

// budget is the time left for the import and the hashing of one switch.
type budget struct {
	lim      Limits
	deadline time.Time
	now      func() time.Time
}

func (b *budget) check() error {
	if b == nil || b.now().Before(b.deadline) {
		return nil
	}
	return failf(CodeTimeBudget, "reading the release took longer than %s", b.lim.Time)
}

// tally counts the entries and bytes of one walk of a release.
type tally struct {
	b       *budget
	entries int
	bytes   int64
}

// entry counts one entry of size bytes (0 for a folder).
func (t *tally) entry(rel string, size int64) error {
	if t.b == nil {
		return nil
	}
	t.entries++
	if t.entries > t.b.lim.Entries {
		return failf(CodeTooManyEntries, "the release holds more than %d entries", t.b.lim.Entries)
	}
	if size > t.b.lim.FileBytes {
		return failf(CodeFileTooLarge, "%s is larger than %d bytes", q(rel), t.b.lim.FileBytes)
	}
	t.bytes += size
	if t.bytes > t.b.lim.Bytes {
		return failf(CodeTooLarge, "the release holds more than %d bytes", t.b.lim.Bytes)
	}
	return t.b.check()
}

// charge counts n bytes written to the total; a total over the limit is an
// error.
func (t *tally) charge(n int64) error {
	if t.b == nil {
		return nil
	}
	t.bytes += n
	if t.bytes > t.b.lim.Bytes {
		return failf(CodeTooLarge, "the release holds more than %d bytes", t.b.lim.Bytes)
	}
	return nil
}

// budgetReader checks the time budget before each read.
type budgetReader struct {
	r io.Reader
	b *budget
}

func (br budgetReader) Read(p []byte) (int, error) {
	if err := br.b.check(); err != nil {
		return 0, err
	}
	if len(p) > 1<<20 {
		p = p[:1<<20]
	}
	return br.r.Read(p)
}
