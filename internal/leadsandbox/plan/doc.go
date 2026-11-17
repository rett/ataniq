// Package plan builds and checks the launch plan of the lead sandbox: the
// command line, the environment and the descriptors a launcher needs to start
// one lead under pasta and bubblewrap. It is a proposal that awaits
// qualification and an owner decision; nothing here says the sandbox is
// qualified, safe or supported. It says what is generated and what is checked.
//
// The package is pure. It builds argument lists and data from the profile
// ([profile.Inputs]) and the caller's [Options]. It starts no process, creates
// no namespace, opens no file, reads no environment variable and never looks a
// program up in PATH: the caller gives the absolute paths of pasta and
// bubblewrap, and which optional host rows exist.
//
// # The plan
//
// [Build] returns a [Plan] whose Argv is the whole command:
//
//	PASTA ARGS... -- PROBE_HOST pass-fds [--read FD=PATH]... [--write FD=PATH]... -- BWRAP ARGS... -- /ataniq/probe --spec /ataniq/spec/spec.json --report-fd FD -- AGENT ARGV...
//
// ARGS after pasta are sandboxnet.PastaArgs. The bubblewrap arguments come in a
// fixed order: the profile's flag set (--die-with-parent --unshare-all
// --share-net --unshare-user --uid --gid --cap-drop ALL --clearenv, with the ids
// of the identity mode), one --setenv per allowlist entry in name order, the
// optional --seccomp FD, the rows of the bind table in table order
// (--tmpfs, --dev, --proc, --ro-bind, --bind, --symlink), --chdir, and
// --remount-ro / last. An optional row is emitted, as --ro-bind or --symlink,
// only when the caller says the host has it; otherwise it is left out and the
// plan says so in its notes. --ro-bind-try is never used, so the plan says
// exactly what is mounted.
//
// # Descriptors
//
// pasta closes every descriptor it inherits before it starts its command, so
// nothing the launcher opens reaches bubblewrap. The plan therefore puts a
// pass-through step between them: PROBE_HOST, the host path of the probe
// binary (profile.Inputs.ProbeBinary), in its pass-fds mode. That mode opens
// each PATH read-only (--read) or write-only without creating it (--write) at
// exactly the descriptor FD, leaves every other descriptor above 2 closed,
// and executes the command after "--". The command line of the mode is the
// contract; the mode itself is built elsewhere. [Plan.Files] lists what the
// step opens, and the launcher opens nothing.
//
// The seccomp file, when the inputs name one, is --read FD=PATH, and
// bubblewrap gets --seccomp FD. The report channel is always present:
// [Options.ReportPath] is a path the holder made (a FIFO or a file it owns)
// outside every lead-writable folder, opened --write at [Options.ReportFD],
// and the probe inside gets --report-fd FD. Whether bubblewrap keeps an
// inherited descriptor open for its child is a qualification item; every plan
// says so in a note.
//
// --share-net is there because bubblewrap runs inside the network namespace
// that pasta made: it does not share the host's network. The probe, not
// bubblewrap, gives the agent its session and controlling terminal (variant B
// of the spike), so --new-session is never passed.
//
// # The environment inside
//
// The allowlist ([Plan.Env]) is set with --clearenv and --setenv. bubblewrap
// keeps and sets PWD under --clearenv, so the agent sees the allowlist plus PWD
// with the working folder as its value. [InsideEnvironment] gives that list,
// which a start-up comparison uses; every plan carries a note ([NotePWD]), and
// a profile that puts PWD in the allowlist is refused.
//
// # Nested sandboxes (owner decision R17)
//
// Nested sandboxes are allowed for now: neither --disable-userns nor
// --assert-userns-disabled is passed, so code inside can create user
// namespaces, which is a larger kernel surface. Every plan carries a note that
// says so, and a note that says whether a seccomp file is named. The seccomp
// hook only passes a file the caller names, with its digest, as a descriptor
// the pass-through step opens; no filter is generated or reviewed here, and the plan
// says "no seccomp filter: none has been reviewed" when none is named.
//
// # Checks
//
// [Build] refuses, with a reason, inputs the profile refuses, relative
// executable or agent paths, an agent argument with a NUL, a seccomp
// descriptor or report descriptor that is missing, below 3 or above 1023 (or
// the same twice), a report path that is not a clean absolute path or that
// lies in or below a lead-writable host folder or a never-mounted path, a
// working folder outside the tree and the home, an operand that starts with
// --, and a command line above [MaxArgvBytes]. It asserts that no option of
// [profile.ForbiddenFlags] appears anywhere, and runs [Verify] on its own
// result.
//
// [Verify] takes the plan, the profile inputs and the [Options] the holder
// uses now, and is the check the holder runs before every start. It is written
// apart from the code that builds the arguments: it parses the bubblewrap
// arguments back into flags, environment and mounts, and compares every part
// of the plan with what the inputs and the options give: both executables, the
// pasta arguments, the pass-through step, the list of descriptors with their
// numbers and paths, the bubblewrap arguments (flag set, allowlist, bind
// table, never-mounted paths, the other runtime's config folder, the working
// folder), exactly which optional rows are present, the probe command line
// with the agent argv, the digest, the terminal flag and the exact set of
// notes. A plan that agrees with the inputs and the options says the text of
// the plan is the one they describe; it does not say what bubblewrap, pasta or
// the kernel will do with it. The mutation tests of this package show which
// changes it catches.
//
// # What it does not do
//
// It does not start anything, open or read the seccomp file, check that a
// source exists or has the right owner (mountpoints and the holder do), run the
// start-up assertions (the probe does), or narrow outbound network access. A
// host path is a string: the caller resolves symbolic links first.
package plan
