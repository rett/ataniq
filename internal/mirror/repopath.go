package mirror

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// ErrRepositoryPath is returned by a fetch when the plan's repository path
// exists on the host. The path is meant to name the clone as the sandbox sees
// it, so on the host it must not exist. The refusal is a second layer. The
// first is that the fetch names the path to the owner's git as a "file://"
// URL ([gitenv.Cmd.SourceFileURL]): git reads a bundle itself, as the owner,
// only when its source is a path to a file, so for the URL it starts the
// sandboxed upload-pack whatever is at the path, also a bundle that appears
// after the checks. See checkRepositoryPath for the exact rule.
var ErrRepositoryPath = errors.New("mirror: the template's repository path exists on the host")

// checkRepositoryPath refuses a repository path that exists on the host,
// without following a symlink at its last name, with two cases that it lets
// pass:
//   - the path does not exist (the answer "no such file or directory" and
//     no other): it is a path inside the sandbox, and the owner's git finds
//     nothing there to read;
//   - the path is the lead's clone itself, tree, written the same. The owner
//     has just checked it with [checkLeadTree]: it is a real folder, and a
//     folder is never read by the owner's git as a bundle. A plan that gives
//     the clone's own host path, as the plans of the tests do, is not a bundle
//     door.
//
// Everything else is refused, whatever it is: a regular file (a bundle or
// not), a symlink (also a dangling one), a folder (a bare repository, a
// provisioned mirror, a clone, an empty one), a device or a socket. So is a
// path whose state cannot be told (a permission error, a file where a
// folder should be on the way, a name too long): the rule fails closed. The
// message holds the path, which is the owner's template text, and the kind
// of what was found. The check reads nothing of the entry.
//
// The check cannot close a race: something may create the path right after
// it. It runs twice, before any command of the fetch starts and again right
// before the owner's git is started, to keep the window small. A path that
// appears after the second check is not read as a bundle, because the source
// is a "file://" URL (see [ErrRepositoryPath]); the test of that case is
// TestFetchRefusesARepositoryPathThatAppearsLater. The owner's choice of a
// path that no sandbox can write a folder on the way to, in the documentation
// of the template, is the same protection for a plan whose source is a path.
func checkRepositoryPath(repository, tree string) error {
	if repository == tree {
		return nil
	}
	fi, err := os.Lstat(repository)
	switch {
	case err == nil:
		return fmt.Errorf("%w: %q is %s; choose a path that does not exist on the host", ErrRepositoryPath, repository, kindOf(fi.Mode()))
	case errors.Is(err, fs.ErrNotExist):
		return nil
	}
	// The path is in the message already: keep the cause only.
	var pe *fs.PathError
	if errors.As(err, &pe) {
		err = pe.Err
	}
	return fmt.Errorf("%w: %q cannot be checked on the host: %w", ErrRepositoryPath, repository, err)
}

// kindOf says what a file mode is, in words for the message.
func kindOf(m fs.FileMode) string {
	switch {
	case m.IsRegular():
		return "a regular file"
	case m&fs.ModeSymlink != 0:
		return "a symbolic link"
	case m.IsDir():
		return "a folder"
	}
	return "a file of another kind"
}
