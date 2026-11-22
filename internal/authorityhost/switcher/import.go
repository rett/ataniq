package switcher

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"syscall"
)

// importPrefix starts the name of the folder in releases/ an import is made
// in; it is renamed to the release's id once the copy is verified.
const importPrefix = ".import-"

// openIncoming opens the incoming folder, which must be a real folder.
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
	src, err := os.OpenRoot(from)
	if err != nil {
		return nil, err
	}
	if opened, err := src.Stat("."); err != nil || !os.SameFile(info, opened) {
		src.Close()
		return nil, failf(CodeFileChanged, "incoming %s changed while it was opened", q(from))
	}
	return src, nil
}

// importTree copies the incoming folder src into the new folder name of
// releases/ as new inodes: folders and regular files only (a link, device,
// fifo, socket or hard-linked file is refused), folders mode 0700 for now
// and files 0644, or 0755 when the incoming file had any execute bit, with
// no set-id or sticky bit, no extended attribute and no ACL copied, owned by
// root:root when the switch runs as root. Each file is opened without
// following a link and without blocking, checked with fstat (a regular file
// with one link, the one that was listed) and read to its size; one that
// changes while it is read is refused. It returns the number of files.
func (s *Switcher) importTree(src *os.Root, name string) (int, error) {
	if err := s.relRoot.Mkdir(name, 0o700); err != nil {
		return 0, err
	}
	dst, err := s.relRoot.OpenRoot(name)
	if err != nil {
		return 0, err
	}
	defer dst.Close()
	files := 0
	err = walkTree(src, func(rel string, info fs.FileInfo) error {
		if err := entryKind(rel, info); err != nil {
			return err
		}
		if info.IsDir() {
			return dst.Mkdir(rel, 0o700)
		}
		files++
		return s.copyFile(src, dst, rel)
	})
	return files, err
}

func (s *Switcher) copyFile(src, dst *os.Root, rel string) error {
	in, info, err := openFileIn(src, rel)
	if err != nil {
		return err
	}
	defer in.Close()
	if links(info) != 1 {
		return failf(CodeBadEntry, "%s has more than one hard link", q(rel))
	}
	out, err := dst.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()
	n, err := io.Copy(out, io.LimitReader(in, info.Size()+1))
	if err != nil {
		return failf(CodeReadFailed, "%s: %s", q(rel), q(err.Error()))
	}
	after, err := in.Stat()
	if err != nil || n != info.Size() || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) || !after.Mode().IsRegular() {
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

// dropImport removes a folder an import left in releases/, making it
// writable first. It is best effort: what it cannot remove stays, under a
// name no switch uses.
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
