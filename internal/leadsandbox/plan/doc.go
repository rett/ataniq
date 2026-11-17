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
//	PASTA ARGS... -- BWRAP ARGS... -- /ataniq/probe --spec /ataniq/spec/spec.json -- AGENT ARGV...
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
// --share-net is there because bubblewrap runs inside the network namespace
// that pasta made: it does not share the host's network. The probe, not
// bubblewrap, gives the agent its session and controlling terminal (variant B
// of the spike), so --new-session is never passed.
//
// # Nested sandboxes (owner decision R17)
//
// Nested sandboxes are allowed for now: neither --disable-userns nor
// --assert-userns-disabled is passed, so code inside can create user
// namespaces, which is a larger kernel surface. Every plan carries a note that
// says so, and a note that says whether a seccomp file is named. The seccomp
// hook only passes a file the caller names, with its digest, as a descriptor
// the launcher opens; no filter is generated or reviewed here, and the plan
// says "no seccomp filter: none has been reviewed" when none is named.
//
// # Checks
//
// [Build] refuses, with a reason, inputs the profile refuses, relative
// executable or agent paths, an agent argument with a NUL, a seccomp
// descriptor that is missing or below 3, a working folder outside the tree and
// the home, an argument that bubblewrap would read as an option, and a command
// line above [MaxArgvBytes]. It asserts that no option of
// [profile.ForbiddenFlags] appears anywhere, and runs [Verify] on its own
// result.
//
// [Verify] is the check the holder runs before every start. It is written
// apart from the code that builds the arguments: it parses the bubblewrap
// arguments back into flags, environment and mounts and compares them with the
// profile (flag set, allowlist, bind table, never-mounted paths, the other
// runtime's config folder, pasta arguments, seccomp descriptor). A parse that
// agrees with the profile says the argument list is the one the profile
// describes; it does not say what bubblewrap, pasta or the kernel will do with
// it. The mutation tests of this package show which changes it catches.
//
// # What it does not do
//
// It does not start anything, open or read the seccomp file, check that a
// source exists or has the right owner (mountpoints and the holder do), run the
// start-up assertions (the probe does), or narrow outbound network access. A
// host path is a string: the caller resolves symbolic links first.
package plan
