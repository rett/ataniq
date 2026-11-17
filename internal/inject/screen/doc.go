// Package screen reads a text snapshot of an agent's terminal screen and says
// what it sees: whether the input box (the composer) is on screen, whether it
// is empty or holds text, whether an approval dialog is open, whether the
// agent is working or has a background task, and which permission mode the
// footer shows.
//
// There is one recogniser for each tested runtime and version, written from
// recorded screens (the fixtures in testdata). Look one up with For. Only
// claude 2.1.325 and codex 0.169 exist; any other runtime or version has no
// recogniser, and nothing here guesses for it.
//
// The package is provisional and unqualified: it is built from recorded
// sessions of one version per runtime, with no live qualification yet.
//
// The package is pure. It takes a Snapshot value, reads no file, no terminal
// and no clock, starts nothing and keeps no state, so the same snapshot always
// gives the same Reading and a Recogniser is safe for concurrent use.
//
// The package is not an idleness classifier. It never says "idle", "working"
// or "blocked" as a verdict: it reports marks on the screen, and the caller
// combines them with the other signals (the agent's own status reports, the
// terminal title, the input state of the holder). It sends no key and decides
// nothing about delivery.
//
// A screen the recogniser cannot place gives Known false, with a note. That
// includes a screen that is empty, cut short, larger than the holder's caps
// (200 rows and 500 columns), not valid text, or laid out in a way the
// recorded screens do not show. Callers must treat an unknown screen as "not
// idle". The text of a screen is controlled by the agent, so the input is
// bounded and no input makes the package panic.
//
// An empty composer of either agent shows hint text ("Ask Codex to do
// anything", "Try ...", the ghost of the last prompt) in faint style, in the
// same characters an owner could type. Plain text cannot tell the two apart, so
// a Snapshot carries the runs of faint characters (Snapshot.Faint), and the
// hint counts as an empty composer only when every character of it is faint
// and the cursor is on the first one. Without that style data a composer that
// shows any text is read as holding text. For codex the hint must also be
// exactly the recorded text; claude's ghost text is the last prompt and cannot
// be pinned. The holder must fill Snapshot.Faint from the cell attributes of
// its emulator, and its offsets count characters in the row of Text, not
// cells; a consumer must test Reading.ComposerEmpty, never
// Reading.ComposerText == "".
//
// Dialog marks are looked for only in the bottom rows of the screen, and only
// at the start of a line, so prose higher up in an answer does not count. Prose
// that wraps into the bottom rows can at worst make a screen look like a
// dialog, which is the safe direction: it only ever makes a screen look less
// idle, never more.
package screen
