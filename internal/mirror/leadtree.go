package mirror

import (
	"errors"
	"fmt"
	"io/fs"

	"ataniq/internal/exchange/safefs"
)

// hostile returns an error that matches [ErrHostileTree] and carries a reason
// in fixed words.
func hostile(reason string) error { return fmt.Errorf("%w: %s", ErrHostileTree, reason) }

// checkLeadTree is the owner-side check of a lead's clone, before any
// sandbox or git is started. It starts from a handle on the state root, which
// the owner owns, and follows no symlink. It returns an error that matches
// [ErrHostileTree] when:
//   - "leads", "leads/<role>" and "leads/<role>/tree", the clone's folder,
//     are not each a real folder;
//   - ".git" in the clone is not a real folder (a ".git" file would point git
//     somewhere else) or ".git/objects" is not;
//   - ".git/objects/info/alternates" or ".git/objects/info/http-alternates"
//     exists, as a file, symlink or anything else;
//   - ".git/commondir", ".git/gitdir" or ".git/shallow" exists.
//
// Nothing in the clone is read by content, and no path inside the clone is
// returned or handed on: the only lead path that goes anywhere is the clone's
// root folder.
//
// A running lead can race this check: it can write the clone at any time, so
// a file may appear or a folder may be swapped right after the check. It is
// a refusal of the obvious cases and a way to fail early with a clear reason.
// What the owner relies on is the sandbox, which serves the clone with no
// network and read-only, and the checks on what arrives in the staging
// repository: git copies the objects into the pack, whatever alternates the
// clone had, and the staging repository is checked before the mirror is
// touched.
func checkLeadTree(stateRoot, role string) error {
	root, err := safefs.OpenRoot(stateRoot)
	if err != nil {
		return fmt.Errorf("mirror: the state root: %w", err)
	}
	defer root.Close()
	base := "leads/" + role + "/tree"
	for _, f := range []struct{ rel, what string }{
		{"leads", "the folder leads"},
		{"leads/" + role, "the lead's folder"},
		{base, "the lead's clone"},
		{base + "/.git", "the .git folder of the clone"},
		{base + "/.git/objects", "the object folder of the clone"},
	} {
		fi, err := root.Lstat(f.rel)
		if err != nil {
			return hostile(f.what + " is missing or cannot be checked")
		}
		if !fi.IsDir() {
			return hostile(f.what + " is not a real folder")
		}
	}
	for _, f := range []struct{ rel, has, unknown string }{
		{base + "/.git/objects/info/alternates", "the clone has an alternates file", "the clone cannot be checked for an alternates file"},
		{base + "/.git/objects/info/http-alternates", "the clone has an http-alternates file", "the clone cannot be checked for an http-alternates file"},
		{base + "/.git/commondir", "the clone has a commondir file", "the clone cannot be checked for a commondir file"},
		{base + "/.git/gitdir", "the clone has a gitdir file", "the clone cannot be checked for a gitdir file"},
		{base + "/.git/shallow", "the clone is shallow", "the clone cannot be checked for a shallow file"},
	} {
		_, err := root.Lstat(f.rel)
		switch {
		case err == nil:
			return hostile(f.has)
		case !errors.Is(err, fs.ErrNotExist):
			// A symlink on the way, a file where a folder should be, or a
			// folder that cannot be read: the absence cannot be shown.
			return hostile(f.unknown)
		}
	}
	return nil
}
