// Package sandboxplan is the narrow interface through which the exchange
// mirror and the intake of lead work reach a sandbox.
//
// Two things must run sandboxed and never as the owner. The first is the
// serving half of a git fetch from a lead's clone: git-upload-pack on the
// lead's tree, with no network and the tree read-only, and a measure of that
// clone's object store. The second is the verification of a lead branch: a
// scratch clone taken from the bare mirror and the gates, with its own home,
// no provider login and no lead credential.
//
// A [Planner] answers two questions. [Planner.Serve] returns the argv that
// runs git-upload-pack sandboxed and the command that measures the object
// store. [Planner.Verify] returns the argv prefix that runs an inner command
// sandboxed, with the folders it sees. The caller starts the processes.
//
// # What this package does not do
//
// It starts nothing: no process, no namespace, no service. It reads no file
// and opens no network connection. The real launch plan is built elsewhere; this package only
// defines how a caller asks for one.
//
// A caller that has no planner, or a planner with no sandbox for a request,
// must refuse the request with [ErrNotConfigured] and never run the command
// unsandboxed.
//
// The helpers [ShellQuote], [ParseMeasure] and [ValidName] are the checks the
// callers share: quoting an argv for git's --upload-pack option, reading the
// measure output, and the name rule for roles and projects.
package sandboxplan
