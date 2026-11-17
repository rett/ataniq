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
// and codex 0.169, which codex reports as 0.169.0 and For accepts in either
// spelling. For any other runtime or version, For reports false and no rules
// exist; nothing is guessed from a neighbouring version. The zero Rules has no
// rules either: it reads every title as busy, with a note.
//
// The claude title will rarely veto anything in production. When the holder
// answered claude's OSC 7501 query (the Q13 runs, as production will), claude
// 2.1.325 put no glyph in its title even while working (q13/t1-claude:
// "Numbers 1 to 80 listing" while the answer streamed). A claude title with
// no glyph is therefore not evidence of idle, and nothing may count on the
// claude title.
//
// The package does no input or output, starts no process, and keeps no state.
// It is not a terminal emulator: the holder owns the emulator and passes the
// title it holds.
//
// The package is provisional and unqualified: built from recorded sessions of
// one version per runtime, with no live qualification yet.
package title
