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
// Git starts the upload-pack command with the environment of the process that
// fetches, which may hold owner credentials. The plan has no environment for
// it, so the upload-pack argv must clear the environment itself.
//
// A [Planner] answers two questions. [Planner.Serve] returns the argv that
// runs git-upload-pack sandboxed and the command that measures the object
// store. [Planner.Verify] returns the argv prefix that runs an inner command
// sandboxed, with the folders it sees. The caller starts the processes.
//
// The paths in a request are roots that the caller vouches for: no sandbox
// may write any component of them, and a plan only ever hands them over whole.
// See [ServeRequest] and [VerifyRequest].
//
// # What this package does not do
//
// It starts nothing: no process, no namespace, no service. It reads no file
// and opens no network connection. The real launch plan is built elsewhere;
// this package only defines how a caller asks for one.
//
// [Template] is a planner that fills in an argv the owner wrote. It is not a
// statement that the argv is a sandbox. Nothing here checks that the command
// confines anything: the owner writes the template, and qualification of the
// sandbox is a separate step. A caller that has no planner, or a planner with
// no template for a request, must refuse the request with [ErrNotConfigured]
// and never run the command unsandboxed.
//
// The helpers [ShellQuote], [ParseMeasure] and [ValidName] are the checks the
// callers share: quoting an argv for git's --upload-pack option, reading the
// measure output, and the name rule for roles and projects.
package sandboxplan
