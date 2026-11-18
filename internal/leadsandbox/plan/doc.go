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
// bubblewrap, which optional host rows exist, the real paths of the host rows
// and the walk of every bind source Ataniq supplies.
//
// # The plan
//
// [Build] returns a [Plan] whose Argv is the whole command:
//
//	PASTA ARGS... -- PROBE_HOST pass-fds [--read FD=PATH]... [--write FD=PATH]... [--walk-dir FD ROOT REL]... [--walk-file FD ROOT REL]... -- BWRAP ARGS... -- /ataniq/probe --spec /ataniq/spec/spec.json --report-fd FD -- AGENT ARGV...
//
// ARGS after pasta are sandboxnet.PastaArgs. The bubblewrap arguments come in a
// fixed order: the profile's flag set (--die-with-parent --unshare-all
// --share-net --unshare-user --uid --gid --cap-drop ALL --clearenv, with the ids
// of the identity mode), one --setenv per allowlist entry in name order, the
// optional --seccomp FD, the rows of the bind table in table order
// (--tmpfs, --dev, --proc, --symlink; --ro-bind-fd FD DEST and --bind-fd FD
// DEST for a bind row Ataniq supplies; --ro-bind SRC DEST for a row of the
// host), --chdir, and --remount-ro / last. An optional row is emitted, as
// --ro-bind or --symlink, only when the caller says the host has it; otherwise
// it is left out and the plan says so in its notes. --ro-bind-try is never
// used, so the plan says exactly what is mounted.
//
// # Descriptors
//
// pasta closes every descriptor it inherits before it starts its command, so
// nothing the launcher opens reaches bubblewrap. The plan therefore puts a
// pass-through step between them: PROBE_HOST, the host path of the probe
// binary (profile.Inputs.ProbeBinary), in its pass-fds mode. That mode opens
// each PATH read-only (--read) or write-only without creating it (--write) at
// exactly the descriptor FD, walks to each bind source by handle and places
// the descriptor it checked at exactly FD (--walk-dir, --walk-file; see
// "Bind sources reach bubblewrap as descriptors"), leaves every other
// descriptor above 2 closed, and executes the command after "--". The command
// line of the mode is the contract; the mode itself is built elsewhere.
// [Plan.Files] lists the files the step opens and [Plan.Walks] the sources it
// walks to, and the launcher opens nothing. The step places at most
// [MaxPassFDs] descriptors, and a plan that would need more is refused.
//
// The seccomp file, when the inputs name one, is --read FD=PATH, and
// bubblewrap gets --seccomp FD. The report channel is always present:
// [Options.ReportPath] is a path the holder made (a FIFO or a file it owns),
// directly inside [Options.ReportDir] and outside every lead-writable folder,
// opened --write at [Options.ReportFD],
// and the probe inside gets --report-fd FD. The holder takes one accepted
// probe line (assert.ReadProbeReport) as the positive signal and does not wait
// for end-of-file, because bubblewrap's own processes may keep the descriptor
// open; a second line or any further byte after the line is a failure of the
// start (assert.CheckNoMore). Whether the descriptor reaches the probe inside,
// and which other processes hold it, are qualification items; every plan says
// so in a note.
//
// --share-net is there because bubblewrap runs inside the network namespace
// that pasta made: it does not share the host's network. The probe, not
// bubblewrap, gives the agent its session and controlling terminal (variant B
// of the spike), so --new-session is never passed.
//
// # Bind sources reach bubblewrap as descriptors
//
// Rule B1 of the design (revision 4, section 3.6): host-side code never binds
// a path any of whose components a sandbox can write; it opens the nearest
// folder no sandbox can write by name and walks every further component by
// handle, at the moment of use; and where bubblewrap can take it, the source
// is passed as the opened handle, so that the object checked is the object
// mounted. bubblewrap can: --bind-fd FD DEST and --ro-bind-fd FD DEST bind
// what an open descriptor names.
//
// So every bind row Ataniq supplies (every read-only or read-write bind that
// is not a row of the host) is emitted as --ro-bind-fd or --bind-fd with a
// descriptor number, never as --ro-bind or --bind with its path, and the
// pass-through step gets the matching walk: --walk-dir FD ROOT REL for a
// source that is a folder, --walk-file FD ROOT REL for a regular file. The
// step opens ROOT without following a symbolic link, walks REL beneath that
// handle in one openat2 lookup with RESOLVE_BENEATH and RESOLVE_NO_SYMLINKS,
// checks the type on the descriptor it got and places that descriptor at FD;
// a source its walk refuses ends the step before bubblewrap is started. The
// step is the last host-side code before bubblewrap and the only place whose
// descriptors bubblewrap inherits (pasta closes what it inherits), which is
// why the walk is made there and not by the launcher.
//
// The walks come in through [Options.SourceWalks] as data, one [SourceWalk]
// per such row: the caller takes them from mountpoints.SourceWalks, which
// derives them from the inputs alone (the state root and the whole path below
// it for a source below the state root, else the folder that holds the source
// and its name), and this package does not import mountpoints. [Build] refuses
// options whose walks do not match the table exactly: one walk per such row
// and none for anything else, a walk that leads to the row's source and names
// the type the row binds, a folder and a path that are clean, and no folder in
// or below one the lead writes. The descriptor numbers are the plan's own,
// not the caller's: the lowest numbers from [MinBindFD] up, in the order of the
// table, passing over the seccomp and the report descriptor of the options, so
// they depend on the inputs and the options alone and never collide with the
// other two. [Plan.Walks] lists the walks with their numbers, and
// [Mount.FD] says which number a row is bound from.
//
// The rows of the host (/usr, the /etc files) stay bound by path, from the
// real path the holder resolved (see "The path that is bound is the path that
// is checked"): no sandbox writes them, and their names may be links of the
// host.
//
// What this gives is that the pass-through step hands bubblewrap the object it
// walked to. Whether bubblewrap then mounts the object behind the descriptor,
// and closes the descriptor before the command inside starts, is not shown by
// anything here: both are qualification items, and every plan says so in a
// note ([NoteBindFDs]) [qualify].
//
// # Mounts
//
// [Plan.Mounts] lists each row of the bind table the plan emits, in order, with
// whether its source lies below a host folder the lead can write
// ([Mount.LeadWritable]). It is false for every row (rule G1: never bind a
// source that has a component a sandbox can write). A bind source is resolved
// on the host, outside every sandbox, where a symbolic link would be followed,
// so a source the lead could replace with a link to a file of the owner
// would have that file bound at the next start. The three read-only .git rows
// of the tree take their sources from an Ataniq-owned copy of those files
// (profile.Inputs.GitMeta), not from the tree. The profile refuses a table
// with a source in or below a read-write row's source; [Build] checks the table
// again and refuses it, and [Verify] checks the mounts it parses back from the
// arguments and reports ProbLeadWritable for any mount whose source lies in or
// below the source of a read-write mount, and for a Mounts row that says so.
// Every plan carries [NoteBindSources], which states the rule. The plan treats
// a host path as a string, so it cannot see a symbolic link in one: the holder
// checks every source without following links right before it starts the
// command (mountpoints.CheckBindSources), and the pass-through step walks to
// every source Ataniq supplies once more, by handle, which is the walk that
// counts for those rows.
//
// # The path that is bound is the path that is checked
//
// For a row that is bound by path, bubblewrap binds the path the plan names and
// follows a link in it, so the check of the sources is worth something only if
// it opens the path that is bound. For a row that is bound from a descriptor
// there is no second resolution by name: the path the plan names for the row is
// the path the step walks to. For every row:
//
//   - A row Ataniq supplies (every row but the host's own) has the Source the
//     inputs name, and its walk leads to that Source: [Build] refuses a walk
//     that does not, and [Verify] takes the source of such a mount from the
//     walk it reads back out of the pass-through step. The holder gives real
//     paths in the inputs, and mountpoints.ResolveSources refuses a source
//     whose real path differs from its lexical one (a link anywhere on it), so
//     the Source is its real path; mountpoints.CheckBindSources then opens that
//     Source as it is, with no link allowed on it, as a check before the step's
//     own walk.
//   - A row of the host (a read-only bind whose source role is
//     profile.RoleHost: /usr, the /etc files), whose own name may be a link of
//     the host that no lead writes (/etc/localtime often is), is bound from the
//     real path the holder resolved, which it gives in [Options.HostSources]
//     (mountpoints.Resolution.HostSources makes the map). [Build] requires an
//     entry for every host row it emits and refuses one for any other key or
//     for an optional row the host lacks; the path is checked like any other
//     source (never-mounted paths, the other runtime's config folder, the
//     profile's rules about other state under the state root, the folders a
//     lead writes). [Verify] compares each mount with the table as these options
//     give it, so a plan that binds the name of a host row where the options
//     give a real path is reported ([ProbMountDiffers]), and options that give
//     no real path for an emitted host row are reported ([ProbOptions]).
//     mountpoints.CheckBindSources opens that real path.
//
// The package cannot see the file system, so it cannot tell that a path is the
// real path of anything: it binds what the holder gives and the holder's
// resolution and check are what make the paths real.
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
// the pass-through step opens; no filter is generated or reviewed here, and
// the plan says "no seccomp filter: none has been reviewed" when none is named.
//
// # Checks
//
// [Build] refuses, with a reason, inputs the profile refuses, relative
// executable or agent paths, an agent argument with a NUL, a seccomp
// descriptor or report descriptor that is missing, below 3 or above 1023 (or
// the same twice), a report path that is not a clean absolute path or that
// lies in or below a lead-writable host folder or a never-mounted path, that
// is, lies below or lies above any source of the bind table or the state
// root's leads, mirrors, mailbox, pub, coords or coord-login folder, or that
// is not directly inside
// [Options.ReportDir], the folder the holder names as its own (required), host
// sources that are missing for an emitted host row or given for another key,
// source walks that do not match the bind rows Ataniq supplies one to one (a
// row without a walk, a walk for a row of the host or for no row, two walks for
// one row, a walk that does not lead to the row's source or names the other
// type, a folder or a path that is not clean, a path that starts with --, a
// folder in or below one the lead writes), more descriptors than the
// pass-through step places ([MaxPassFDs]), a
// working folder outside the tree and the home, an operand of bubblewrap that
// starts with --, an agent argument that equals a forbidden option, and a
// command line above [MaxArgvBytes]. It asserts that no option of
// [profile.ForbiddenFlags] appears anywhere, and runs [Verify] on its own
// result.
//
// bubblewrap reads operands by position, so it would take an operand that
// starts with -- as a value, not as an option. The plan refuses such an operand
// so that its parse and its rendering stay unambiguous; that is a choice of
// this package, not something bubblewrap needs. The agent arguments come after
// the probe's -- and bubblewrap never reads them; refusing an agent argument
// that equals a forbidden option is defence in depth, so that no element of
// the whole command line is one, and it refuses more than bubblewrap needs. The
// command-line cap is well under the Linux budget that arguments and the
// environment of a new program share, because the plan cannot see the
// environment the launcher passes.
//
// [Verify] takes the plan, the profile inputs and the [Options] the holder
// uses now, and is the check the holder runs before every start. It is written
// apart from the code that builds the arguments: it parses the bubblewrap
// arguments back into flags, environment and mounts, and compares every part
// of the plan with what the inputs and the options give: both executables, the
// pasta arguments, the pass-through step, the list of descriptors with their
// numbers and paths, the list of walks, the bubblewrap arguments (flag set,
// allowlist, bind table, never-mounted paths, the other runtime's config
// folder, the working folder), the list of mounts, exactly which optional rows
// are present, the probe command line with the agent argv, the digest, the
// terminal flag and the exact set of notes. It reads --bind-fd and
// --ro-bind-fd back and pairs each with the walk of the pass-through step that
// opens its descriptor, which it reads back from the step on its own: the path
// that walk leads to is the mount's source for every check a source gets (the
// table, the never-mounted paths, rule G1), so a step that walks somewhere
// else is caught as a mount of another source. It reports a bind row Ataniq
// supplies that is bound by path ([ProbBindByPath]); a descriptor that two
// mounts or two walks use, a mount without a walk, a walk without a mount, a
// row of the host bound from a descriptor and a bind descriptor that is the
// seccomp or the report descriptor ([ProbBindFD]); and a walk whose
// descriptor, folder, path or type is not the one the inputs and the options
// give, in the step or in [Plan.Walks] ([ProbWalk]).
// A plan that agrees with the inputs and the options says the text of
// the plan is the one they describe; it does not say what bubblewrap, pasta or
// the kernel will do with it. The mutation tests of this package show which
// changes it catches.
//
// # What it does not do
//
// It does not start anything, open or read the seccomp file, check that a
// source exists or has the right owner (mountpoints and the holder do), walk
// to a source (the pass-through step does), run the start-up assertions (the
// probe does), or narrow outbound network access. A host path is a string: the
// caller resolves symbolic links first. It cannot tell that the folder a walk
// starts from is one no sandbox can write: it refuses a folder in or below one
// the lead writes, and the rest is the caller's, who takes the walks from
// mountpoints.SourceWalks. It does not show what bubblewrap does with a
// descriptor it is given.
package plan
