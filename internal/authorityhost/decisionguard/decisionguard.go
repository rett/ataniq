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
// state folder is gone refuses every decision.
//
// The function holds no state, starts no goroutine, reads no file content
// and has no side effect. The error it returns carries a fixed code a
// service can log, and the folder path the caller gave, and nothing taken
// from the file system.
package decisionguard

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// GuardFile is the name of the guard file in the state folder. The switch
// writes it (the switcher package's InhibitFile has the same value; a test
// holds the two equal). This package does not import the switch.
const GuardFile = "decisions.inhibit"

// The codes of a refusal. A service may log them; they are fixed.
const (
	// CodeStateDir: the state folder is missing, is not a real folder
	// (a file or a link), or cannot be looked up.
	CodeStateDir = "decision-guard-state-folder"
	// CodeInhibited: the guard file exists, in any form.
	CodeInhibited = "decision-guard-inhibited"
	// CodeUnreadable: the guard path cannot be looked up for a reason
	// other than "does not exist".
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
	_, err = os.Lstat(filepath.Join(stateDir, GuardFile))
	switch {
	case err == nil:
		return &Error{Code: CodeInhibited, Dir: stateDir}
	case errors.Is(err, fs.ErrNotExist):
		return nil
	default:
		return &Error{Code: CodeUnreadable, Dir: stateDir}
	}
}
