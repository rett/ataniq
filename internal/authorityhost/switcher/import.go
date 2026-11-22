package switcher

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

// importPrefix starts the name of the folder in releases/ an import is made
// in, .import-<id>-<random>, new for each import; it is renamed to the
// release's id once the copy is verified.
const importPrefix = ".import-"

// maxLeftReported bounds the folders of earlier imports a switch names.
const maxLeftReported = 8

// newStaging makes the staging folder of an import of id in releases/: a
// name no other import uses (16 random hex digits after the id), created
// with mkdir, which fails on a name that is there, mode 0700. Only a
// folder this returned is ever removed by the import that made it.
func (s *Switcher) newStaging(id ReleaseID) (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	name := importPrefix + id.Name + "-" + hex.EncodeToString(b[:])
	if err := s.relRoot.Mkdir(name, 0o700); err != nil {
		return "", err
	}
	return name, nil
}

// reportLeftImports names the folders of releases/ an earlier import left
// (a switch that died while it imported): they are never used or removed
// by another import; the owner looks at them and removes them by hand.
func (s *Switcher) reportLeftImports() {
	d, err := s.relRoot.Open(".")
	if err != nil {
		return
	}
	defer d.Close()
	names, _ := d.Readdirnames(-1)
	sort.Strings(names)
	n := 0
	for _, name := range names {
		if !strings.HasPrefix(name, importPrefix) {
			continue
		}
		if n++; n <= maxLeftReported {
			s.printf("warning: releases/%s was left by an earlier import; it is not used or removed: look at it, then remove it by hand", q(name))
		}
	}
	if n > maxLeftReported {
		s.printf("warning: %d more folders of earlier imports are in releases/", n-maxLeftReported)
	}
}

// incomingApart refuses an incoming folder from (opened as src) that is,
// contains or lies beneath releases/ or anything in it (a staging folder
// of an import, a release): copying it would read what the import writes,
// and removing a failed copy could remove the input. Both the real paths,
// every symbolic link resolved, and the folders themselves (device and
// inode, which a bind mount keeps) are compared: no folder from src up to
// the root may be releases/, and no folder from releases/ up to the root
// may be src. A path that cannot be resolved or read is refused too.
func (s *Switcher) incomingApart(from string, src *os.Root) error {
	relPath := filepath.Join(s.base(), "releases")
	overlap := func() error {
		return failf(CodeIncomingOverlaps, "incoming %s is, contains or lies beneath %s; copy the release to an incoming folder outside it", q(from), q(relPath))
	}
	unreadable := func(err error) error {
		return failf(CodeIncomingOverlaps, "incoming %s cannot be told apart from %s: %s", q(from), q(relPath), q(err.Error()))
	}
	fromReal, err := filepath.EvalSymlinks(from)
	if err != nil {
		return unreadable(err)
	}
	relReal, err := filepath.EvalSymlinks(relPath)
	if err != nil {
		return unreadable(err)
	}
	if within(fromReal, relReal) || within(relReal, fromReal) {
		return overlap()
	}
	srcInfo, err := src.Stat(".")
	if err != nil {
		return unreadable(err)
	}
	relInfo, err := s.relRoot.Stat(".")
	if err != nil {
		return unreadable(err)
	}
	for _, w := range []struct {
		start string
		other fs.FileInfo
	}{{fromReal, relInfo}, {relReal, srcInfo}} {
		for p := w.start; ; p = filepath.Dir(p) {
			info, err := os.Stat(p)
			if err != nil {
				return unreadable(err)
			}
			if os.SameFile(info, w.other) {
				return overlap()
			}
			if p == filepath.Dir(p) {
				break
			}
		}
	}
	return nil
}

// within reports whether the clean absolute path p is base or lies
// beneath it.
func within(p, base string) bool {
	return p == base || base == "/" || strings.HasPrefix(p, base+"/")
}

// openIncoming opens the incoming folder, which must be a real folder. The
// folder belongs to an unprivileged owner, who can swap it for anything
// between the look and the open, and the switch holds its lock: it is
// opened once, with O_DIRECTORY, O_NOFOLLOW and O_NONBLOCK, so the kernel
// refuses a FIFO, a device or a link before opening it (never waiting for
// a writer), the opened descriptor must be a folder and the very one the
// look saw, and the root is made from that checked descriptor (through its
// /proc/self/fd entry, as the checker does), never from the name again.
// Without /proc the folder is refused: opening by name a second time is
// what could block.
func openIncoming(from string) (*os.Root, error) {
	info, err := os.Lstat(from)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, failf(CodeReleaseMissing, "no incoming folder %s", q(from))
		}
		return nil, err
	}
	if !info.IsDir() {
		return nil, failf(CodeReleaseNotFolder, "incoming %s is not a folder (or is a symbolic link)", q(from))
	}
	if incomingHook != nil {
		incomingHook(from)
	}
	d, err := os.OpenFile(from, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_NOCTTY, 0)
	if err != nil {
		if errors.Is(err, syscall.ENOTDIR) || errors.Is(err, syscall.ELOOP) {
			return nil, failf(CodeReleaseNotFolder, "incoming %s is not a folder (or is a symbolic link)", q(from))
		}
		return nil, err
	}
	defer d.Close()
	opened, err := d.Stat()
	switch {
	case err != nil:
		return nil, err
	case !opened.IsDir():
		return nil, failf(CodeReleaseNotFolder, "incoming %s is not a folder (or is a symbolic link)", q(from))
	case !os.SameFile(info, opened):
		return nil, failf(CodeFileChanged, "incoming %s changed while it was opened", q(from))
	}
	src, err := os.OpenRoot("/proc/self/fd/" + strconv.FormatUint(uint64(d.Fd()), 10))
	if err != nil {
		return nil, failf(CodeReadFailed, "incoming %s cannot be opened from its checked descriptor", q(from))
	}
	if again, err := src.Stat("."); err != nil || !os.SameFile(opened, again) {
		src.Close()
		return nil, failf(CodeFileChanged, "incoming %s changed while it was opened", q(from))
	}
	return src, nil
}

// importTree copies the incoming folder src into the staging folder name of
// releases/, which newStaging made, as new inodes: folders and regular files only (a link, device,
// fifo, socket or hard-linked file is refused), folders mode 0700 for now
// and files 0644, or 0755 when the incoming file had any execute bit, with
// no set-id or sticky bit, no extended attribute and no ACL copied, owned by
// root:root when the switch runs as root. Each file is opened without
// following a link and without blocking, checked with fstat (a regular file
// with one link, the very file the walk listed, of the size it listed) and
// read to that size; one that changes while it is read is refused. It
// returns the number of files.
//
// The walk counts the entries against the switch's budget, and the copy
// charges every byte it writes to the budget's total as it writes it: the
// copy stops at the limit, and the caller removes the partial copy.
func (s *Switcher) importTree(src *os.Root, name string) (int, error) {
	dst, err := s.relRoot.OpenRoot(name)
	if err != nil {
		return 0, err
	}
	defer dst.Close()
	files := 0
	t := tally{b: s.budget}
	err = walkTree(src, func(rel string, info fs.FileInfo) error {
		if err := entryKind(rel, info); err != nil {
			return err
		}
		if err := t.entry(rel, 0); err != nil {
			return err
		}
		if info.IsDir() {
			return dst.Mkdir(rel, 0o700)
		}
		files++
		return s.copyFile(src, dst, rel, info, &t)
	})
	return files, err
}

// copyFile copies the file rel of src, which the walk saw as listed, into
// dst, charging the bytes written to t.
func (s *Switcher) copyFile(src, dst *os.Root, rel string, listed fs.FileInfo, t *tally) error {
	if importHook != nil {
		importHook("open", rel)
	}
	in, info, err := openFileIn(src, rel)
	if err != nil {
		return err
	}
	defer in.Close()
	if !os.SameFile(listed, info) || info.Size() != listed.Size() {
		return failf(CodeFileChanged, "%s changed between the walk and the copy", q(rel))
	}
	if links(info) != 1 {
		return failf(CodeBadEntry, "%s has more than one hard link", q(rel))
	}
	if info.Size() > s.budget.lim.FileBytes {
		return failf(CodeFileTooLarge, "%s is larger than %d bytes", q(rel), s.budget.lim.FileBytes)
	}
	out, err := dst.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()
	if importHook != nil {
		importHook("copy", rel)
	}
	n, err := io.Copy(chargedWriter{out, t}, budgetReader{io.LimitReader(in, info.Size()+1), s.budget})
	if err != nil {
		switch code, _ := codeOf(err); code {
		case CodeTimeBudget, CodeTooLarge:
			return err
		}
		return failf(CodeReadFailed, "%s: %s", q(rel), q(err.Error()))
	}
	after, err := in.Stat()
	if err != nil || n != info.Size() || !sameContent(info, after) {
		return failf(CodeFileChanged, "%s changed while it was copied", q(rel))
	}
	mode := fs.FileMode(0o644)
	if info.Mode()&0o111 != 0 {
		mode = 0o755
	}
	if os.Geteuid() == 0 {
		if err := out.Chown(0, 0); err != nil {
			return err
		}
	}
	if err := out.Chmod(mode); err != nil {
		return err
	}
	return out.Sync()
}

// incomingHook, when a test sets it, is called with the incoming folder
// between the switch's look at it and its open.
var incomingHook func(from string)

// importHook, when a test sets it, is called before a file of an import is
// opened ("open") and before it is copied ("copy").
var importHook func(stage, rel string)

// chargedWriter charges what it is asked to write to a tally before it
// writes it, and writes nothing once the tally's total is over its limit.
type chargedWriter struct {
	w io.Writer
	t *tally
}

func (cw chargedWriter) Write(p []byte) (int, error) {
	if err := cw.t.charge(int64(len(p))); err != nil {
		return 0, err
	}
	return cw.w.Write(p)
}

// sealTree makes the imported folder name of releases/ read-only: it removes
// every write bit (folders 0555, files 0444 or 0555), sets root:root as the
// owner of the folders when the switch runs as root, and syncs every folder.
func (s *Switcher) sealTree(name string) error {
	dst, err := s.relRoot.OpenRoot(name)
	if err != nil {
		return err
	}
	defer dst.Close()
	var dirs []string
	err = walkTree(dst, func(rel string, info fs.FileInfo) error {
		if info.IsDir() {
			dirs = append(dirs, rel)
			return nil
		}
		return dst.Chmod(rel, info.Mode().Perm()&^0o222)
	})
	if err != nil {
		return err
	}
	// The deepest folders last in dirs; seal from there up to ".".
	dirs = append([]string{"."}, dirs...)
	for i := len(dirs) - 1; i >= 0; i-- {
		if os.Geteuid() == 0 {
			if err := dst.Lchown(dirs[i], 0, 0); err != nil {
				return err
			}
		}
		if err := dst.Chmod(dirs[i], 0o555); err != nil {
			return err
		}
		d, err := dst.Open(dirs[i])
		if err != nil {
			return err
		}
		err = d.Sync()
		d.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// dropImport removes the staging folder name that this very import made
// (see newStaging), making it writable first; it is never given another
// folder. It is best effort: what it cannot remove stays, under a name no
// switch uses, and later switches name it (see reportLeftImports).
func (s *Switcher) dropImport(name string) {
	if _, err := s.relRoot.Lstat(name); err != nil {
		return
	}
	if dst, err := s.relRoot.OpenRoot(name); err == nil {
		dst.Chmod(".", 0o700)
		walkTree(dst, func(rel string, info fs.FileInfo) error {
			if info.IsDir() {
				dst.Chmod(rel, 0o700)
			}
			return nil
		})
		dst.Close()
	}
	s.relRoot.RemoveAll(name)
}
