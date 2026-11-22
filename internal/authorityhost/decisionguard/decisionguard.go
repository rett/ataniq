// Package decisionguard is the decision contract of the authority host as
// code: the one lookup every decision-capable service makes before it
// decides.
//
// The release switch keeps a single guard file, decisions.inhibit, in its
// state folder. While that file exists in any form, no decision may be
// made. A decision-capable service therefore:
//
//   - calls Check before EVERY decision, not once at start and not once
//     per connection or per batch;
//   - refuses the decision whenever Check returns an error, whatever the
//     error is, and decides only when it returns nil;
//   - never caches the answer: the guard can appear between two calls, and
//     a cached "allowed" is a decision made with no guard.
//
// Check fails closed. It allows only when the state folder is a real
// folder (not a link to one, not a file) and the guard path in it
// definitely does not exist. A guard path that exists as a file, a folder,
// a link (even a dangling one), a socket or anything else refuses; so does
// a missing, unreadable or unusable state folder, and any lookup error
// other than "does not exist" (permission denied included). A host whose
// state folder is gone refuses every decision, and so does one the service
// may not open (it must be readable by the service, as the bootstrap's
// mode 0755 makes it). The state folder is pinned
// once (opened, and checked to be the real folder found by its path) and
// the guard is looked up relative to that open folder, never by the path
// again, so the lookup is answered by the folder found, whatever is moved
// or put at its path meanwhile. "Does not exist" for the guard then
// allows only when the folder at the path is still, after that lookup,
// the pinned folder (the same device and inode, still a real folder): a
// folder removed, replaced or swapped for a link refuses. While it is
// pinned its inode cannot be reused by another folder.
//
// The function holds no state across calls, starts no goroutine, reads no
// file content and has no side effect; it holds the state folder open for
// the time of the call only. The error it returns carries a fixed code a
// service can log, and the folder path the caller gave, and nothing taken
// from the file system.
package decisionguard

import (
	"errors"
	"io/fs"
	"os"
)

// GuardFile is the name of the guard file in the state folder. The switch
// writes it (the switcher package's InhibitFile has the same value; a test
// holds the two equal). This package does not import the switch.
const GuardFile = "decisions.inhibit"

// The codes of a refusal. A service may log them; they are fixed.
const (
	// CodeStateDir: the state folder is missing, is not a real folder
	// (a file or a link), cannot be looked up, or is no longer the folder
	// it was when the guard file was looked up in it.
	CodeStateDir = "decision-guard-state-folder"
	// CodeInhibited: the guard file exists, in any form.
	CodeInhibited = "decision-guard-inhibited"
	// CodeUnreadable: the guard path cannot be looked up for a reason
	// other than "does not exist", the state folder the service may not
	// open included.
	CodeUnreadable = "decision-guard-unreadable"
)

// Error is the refusal of Check. Its text is the code and the folder the
// caller gave; no operating system text is in it.
type Error struct {
	Code string // one of CodeStateDir, CodeInhibited, CodeUnreadable
	Dir  string // the state folder, as the caller gave it
}

func (e *Error) Error() string {
	return e.Code + ": " + e.Dir
}

// Check reports whether a decision may be made now: nil allows, a non-nil
// error (always an *Error) refuses and says why by its Code. stateDir is
// the switch's state folder (var/lib/authority-host on the host). Call it
// before every decision and never cache its result; see the package
// comment for the contract.
func Check(stateDir string) error {
	fi, err := os.Lstat(stateDir)
	if err != nil || !fi.IsDir() {
		// Lstat does not follow a link, so a link to a folder is not a
		// folder here: the switch's folders are real folders, and a link
		// is something someone put there.
		return &Error{Code: CodeStateDir, Dir: stateDir}
	}
	// The folder is pinned: held open from here to the last lookup, so
	// the guard file is looked up in this very folder, whatever is moved
	// or put at its path meanwhile, and its inode cannot be reused while
	// it is held.
	pinned, err := os.OpenRoot(stateDir)
	if errors.Is(err, fs.ErrPermission) {
		// A real folder the service may not open: its guard cannot be
		// looked up in it.
		return &Error{Code: CodeUnreadable, Dir: stateDir}
	}
	if err != nil {
		return &Error{Code: CodeStateDir, Dir: stateDir}
	}
	defer pinned.Close()
	held, err := pinned.Stat(".")
	if err != nil || !held.IsDir() || !os.SameFile(fi, held) {
		// What was opened is not the real folder found first (a link
		// or another folder was put at the path between the two).
		return &Error{Code: CodeStateDir, Dir: stateDir}
	}
	if betweenLookups != nil {
		betweenLookups()
	}
	_, err = pinned.Lstat(GuardFile)
	switch {
	case err == nil:
		return &Error{Code: CodeInhibited, Dir: stateDir}
	case !errors.Is(err, fs.ErrNotExist):
		return &Error{Code: CodeUnreadable, Dir: stateDir}
	}
	if beforeRecheck != nil {
		beforeRecheck()
	}
	// "No such file" in the pinned folder allows only when that folder is
	// still the one at the path: a folder removed, renamed away or
	// replaced by another folder or a link is no longer the state folder
	// the caller named, and its missing guard says nothing of the one
	// that is there now.
	again, err := os.Lstat(stateDir)
	if err != nil || !again.IsDir() || !os.SameFile(held, again) {
		return &Error{Code: CodeStateDir, Dir: stateDir}
	}
	return nil
}

// betweenLookups is a test's hook, nil otherwise: it runs between the
// pinning of the state folder and the lookup of the guard file in it.
var betweenLookups func()

// beforeRecheck is a test's hook, nil otherwise: it runs between the
// lookup of the guard file and the last lookup of the state folder.
var beforeRecheck func()
