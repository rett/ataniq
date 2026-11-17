// Package boundedexec runs one command with hard bounds.
//
// It is for owner-side code that runs git on repositories Ataniq owns, and
// that starts sandbox commands whose output is written by untrusted code. The
// caller sets every bound: a time limit, a cap on standard output, a cap on
// standard error, and optionally a watchdog function.
//
// # What a run guarantees
//
// The command gets exactly the argument vector, environment and working
// folder in [Cmd]. Nothing is looked up in PATH, nothing is inherited from the
// calling process, no shell is involved and no file descriptor except 0, 1 and
// 2 is passed on. Standard input is /dev/null unless the caller gives a
// reader.
//
// The command runs in its own process group. When the time limit passes, the
// context ends, standard output grows past its cap, or the watchdog returns an
// error, the whole group gets SIGKILL. [Run] returns only after the command
// has been reaped. It does not wait for a grandchild that still holds a pipe,
// nor for the caller's reader and writer: the group is killed first, and a
// fixed wait of two seconds for the pipes follows, so Run returns within
// [Cmd.Timeout] plus two seconds in every case. When the command ends by
// itself, the rest of its group is killed too, so a background process cannot
// outlive the run.
//
// The package makes the pipes itself and copies in goroutines of its own. A
// [Cmd.Stdin] that is not an *os.File and a [Cmd.Stdout] are caller code. If
// their Read or Write blocks for ever, that goroutine cannot be stopped: it
// is left behind, blocked in the caller's Read or Write, until the call
// returns. Run does not wait for it, and the [Result] holds what was copied
// so far.
//
// The error says which bound stopped the command: [ErrTimeout],
// [ErrOutputLimit], a [*WatchError], or the context's error. The [Result]
// still carries what was read before the stop. Standard error over its cap is
// drained and dropped, so the command never blocks on it; that is not an
// error, and [Result.StderrCut] says it happened.
//
// # What it does not do
//
// The package is not a sandbox. It does not limit memory, processes, files,
// the network or what the command can read or write, and it cannot stop a
// process that left the group (for example with setsid). It does not parse
// output, retry, or keep a log. Code for systems other than Linux is a stub
// that returns [ErrUnsupported].
package boundedexec
