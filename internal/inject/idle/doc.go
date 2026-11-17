// Package idle joins the three readings of a hosted agent session into one
// classification: the last OSC 7501 report (osc7501.Status), the screen
// (package screen) and the window title (package title). It says whether the
// session is idle, working, blocked, ended or unknown, and lists the signals
// that said so.
//
// The package holds the tested table. Lookup returns a Classifier only for the
// runtimes and versions that were recorded: claude 2.1.325, and codex 0.169
// (also spelled 0.169.0, which is how codex reports itself). Any other pair
// has no classifier and nothing is guessed from a neighbouring version. The
// zero Classifier classifies everything as unknown. Tested lists the table for
// the documentation and for the holder's status.
//
// The package is provisional and unqualified: built from recorded sessions of
// one version per runtime, with no live qualification yet.
//
// The rules, for both runtimes:
//
//   - Ended gives ended.
//   - A report with state blocked and the right app (codex, claude-code), or
//     an open dialog on the alternate screen, gives blocked, whatever else is
//     read. A blocked report is told apart from a dialog in the signals.
//   - A stale report status, a report from another app and a report whose
//     state cannot be read are not the agent's state: unknown.
//   - A screen the recogniser does not know gives unknown. So does a snapshot
//     that is not on the alternate screen: every recorded composer, and every
//     recorded dialog, is on the alternate screen, and the recognisers read
//     dialog marks before they look at the screen kind, so the screen kind is
//     checked here.
//   - A background task, a working line on screen or a busy title is not
//     idle.
//   - The state is idle only when every signal required for the runtime says
//     so. When the signals cannot tell, the answer is unknown, never idle.
//
// codex requires the report. Idle or done says idle, working says working,
// and none, clear or error give unknown. The second signal is required for
// idle: a title that is neither busy nor "Action Required", and a screen with
// the composer and no dialog. When the report says idle and the title or the
// screen says otherwise, the state is unknown, not the other signal's state.
//
// claude requires the recogniser: the composer at its place, the cursor in it,
// the footer mode equal to Input.ExpectedMode (an empty ExpectedMode never
// matches, the caller says which mode it expects), no dialog, no background
// task and no working line. A busy title and a working report give working;
// a report of error or clear gives unknown; idle, done or no report at all
// leave the screen's answer. While a collapsed paste is in the composer
// (PastedMarker with an empty Mode, the footer "paste again to expand") the
// screen shows neither the working line nor the shell count and no mode, so
// the state comes from the report: without an idle or done report it is
// unknown.
//
// The state does not say whether the composer is empty. Classification.
// ComposerEmpty is copied from the screen reading and is separate: a composer
// that holds the owner's draft at an otherwise idle prompt is idle with
// ComposerEmpty false. The caller checks emptiness itself.
//
// The package is pure. It reads no file, no terminal and no clock, starts
// nothing, keeps no state and never panics on any input; a Classifier is safe
// for concurrent use. It does not write to any agent, decide delivery or
// persist anything. Titles, screens and reports are text the agent controls,
// so the only text copied into a Signal is a fixed word or a bounded token.
package idle
