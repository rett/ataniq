package gitenv

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// CheckInfoAttributes refuses a repository whose own attribute file,
// "info/attributes" in gitDir, could change what git prints or merges. It
// returns nil when the file is absent or is an empty regular file, and an
// error for anything else there: a file with any content (a comment or a
// blank line too), a symbolic link (it is not followed, even when it leads
// to an empty file), a FIFO, a device, a socket or a folder, and when the
// place cannot be looked at.
//
// git reads this file whatever the attribute source is, and no option or
// setting of this package stops it; the package comment says more. So the
// caller runs the check before a diff or a merge whose result must not
// depend on attributes, and does not run the command when it fails.
//
// gitDir is the absolute path of a bare repository, whose "info" folder is
// its own. The check looks in the folder it is given and nowhere else: for a
// linked worktree git keeps "info" in the common folder, and naming that
// folder is the caller's task. A missing "info" folder passes, since no file
// can be in it; a missing gitDir is refused.
//
// The check reads the file system once and holds nothing open, so it is a
// check and not a fence: it relies on the repository being one that only the
// host can write.
func CheckInfoAttributes(gitDir string) error {
	if gitDir == "" || strings.ContainsRune(gitDir, 0) {
		return errors.New("gitenv: the repository path is empty or holds a NUL")
	}
	if !filepath.IsAbs(gitDir) {
		return fmt.Errorf("gitenv: the repository path %q is not absolute", gitDir)
	}
	dir, err := os.Stat(gitDir)
	if err != nil {
		return fmt.Errorf("gitenv: the repository cannot be checked for info/attributes: %w", err)
	}
	if !dir.IsDir() {
		return fmt.Errorf("gitenv: the repository path %q is not a folder", gitDir)
	}
	path := filepath.Join(gitDir, "info", "attributes")
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("gitenv: info/attributes cannot be checked: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("gitenv: %q is not a regular file (%s); git reads info/attributes whatever the attribute source is, so the repository is refused", path, info.Mode().Type())
	}
	if info.Size() != 0 {
		return fmt.Errorf("gitenv: %q is not empty (%d bytes); git reads info/attributes whatever the attribute source is, so the repository is refused", path, info.Size())
	}
	return nil
}
