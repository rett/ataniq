// Package title reads the window title of a hosted agent and says what the
// title shows: whether the agent looks busy and whether it asks for an
// approval.
//
// The title is text the agent controls. It can be empty, very long, not valid
// UTF-8, or made to look like anything. The package therefore reads at most
// MaxTitleBytes bytes, never panics, and never returns an error: the worst a
// hostile title can do is produce a Reading.
//
// A Reading is one signal among several. The package does not decide that an
// agent is idle; it only reports what the title shows. A title that shows no
// busy glyph is not proof of idle, because the rules for claude cannot tell
// an idle agent from one that is waiting at an approval prompt. Whoever
// combines the signals must treat a case the signals cannot settle as not
// idle.
//
// The package knows exactly the versions that were recorded: claude 2.1.325
// and codex 0.169. For any other runtime or version, For reports false and no
// rules exist; nothing is guessed from a neighbouring version.
//
// The package does no input or output, starts no process, and keeps no state.
// It is not a terminal emulator: the holder owns the emulator and passes the
// title it holds.
package title
