// Package harness is the logic of the opt-in lead sandbox qualification
// command. It is infrastructure for a proposal that awaits qualification and
// an owner decision: nothing here says the lead sandbox profile is qualified,
// safe or supported. It launches the generated plan once, on a host the
// coordinator chose, and records facts.
//
// # What it does
//
// [LoadConfig] reads one explicit configuration file (mode 0600, owned by the
// caller, strictly decoded, bounded). [Execute] then runs the steps in
// order and records each with its start, end, status and error text:
// validation of the configuration and of the executables (and that the git
// identity folder, which is bound whole, holds the file "config" and nothing
// else), the folder of the
// attempt, the version of each tool (a bubblewrap whose release is older than
// [MinBwrapBindFD] ends the run there: see "Bind sources by descriptor"), the
// own-clone check, the preparation of
// the mount points and of Ataniq's copy of the tree's git files (the git
// metadata folder, profile.Inputs.GitMeta; only when the configuration says no
// lead has run), the mount-point check, the resolv.conf derivation, the resolution of every
// source to its real path, the report channel (a FIFO in a folder of the attempt
// that holds nothing else, whose reader is opened first), the canary, the plan
// ([plan.Build] and [plan.Verify], with the walk of every bind source Ataniq
// supplies from [mountpoints.PlanWalks]), the probe spec (with the identities of the
// owner and holder paths and their ancestors, which assertion 1 needs, and of
// the coordinators' folders under the state root and their ancestors, which
// assertion 11 needs), a
// second check right before the launch, the check of every bind source of the
// plan without following a symbolic link (rule G1, a step of its own after the
// second resolution; a problem, a link or a source of another type than its
// row binds included, refuses the launch), the launch through a
// [Launcher], the
// probe's report line (one line, read with [assert.ReadProbeReport], taken as
// the start without waiting for end-of-file), the wait for exit, the outside
// checks, the stop of anything left (SIGTERM, wait, SIGKILL, with the
// stop-rule constants of package assert), the question whether any byte
// followed the report line ([assert.CheckNoMore]) and the evidence.
//
// It starts nothing unless [Options.Run] is set and the configuration says
// "coordinator_reviewed": true; with a terminal on standard input, unless
// [Options.Unattended] is set. Without [Options.Run] it validates, builds and
// verifies the plan and returns it with its digest: a dry run that starts no
// process and opens no listener or FIFO.
//
// # What it records, and what it does not say
//
// Each attempt has a folder with a random name under the campaign folder
// (0700). It holds plan.json, spec.json, report.json, outside.json,
// terminal.log and summary.json, each written once, exclusively, mode 0600
// and synced. The files state facts: a status and a detail per check, exit
// status, counts. No field says the profile passed, is qualified or is
// supported, and the code that writes them has no such notion; a reader
// draws conclusions from the facts.
//
// For rule G1 (case G1, which is not in the design's table) summary.json lists
// every bind source of the plan with the fact from the check without following
// links (no component of its path is a symbolic link), and the fact from
// [plan.Verify] that no source lies in or below a read-write source. A fact the
// run did not ask is absent, and the case's result for it is not-checked. For
// every row the path the plan binds is the path the check opens, and the
// evidence says so (checked_path equals the source): a source the configuration
// supplies must be given as its real path, so a link on one ends the run at the
// resolution, and a row of the host (/usr, /etc/localtime) that resolves through
// the host's own links is bound, and checked, by its real path, which the harness
// gives the plan. The harness refuses to check a plan whose bound path is not the
// one the check would open.
//
// # Bind sources by descriptor
//
// The plan binds every source Ataniq supplies from a descriptor (rule B1 of
// the design, revision 4, section 3.6): its pass-through step, the probe's
// pass-fds mode on the host between pasta and bubblewrap, walks to each such
// source by handle and bubblewrap gets --bind-fd or --ro-bind-fd with the
// descriptor that walk returned; the rows of the host stay bound by path. The
// harness gives the plan the walks of [mountpoints.PlanWalks], which are the
// walks its own check of the bind sources makes on the same rows, and it
// refuses to check a plan that binds a source Ataniq supplies in any other
// way, or by another walk. plan.json lists the walks and the descriptor of each
// mount. summary.json says per source whether it is bound from a descriptor
// ("bound_by_descriptor"), with the descriptor, the folder the walk starts
// from and the path beneath it; and, from the check before the launch, the
// fact that no component of its path is a link and, as a fact of its own, that
// it is of the type its row binds ("has_expected_type": false for a source the
// check refused as something else).
//
// What these facts are about is the plan and the harness's check before the
// launch. The walk that counts is made by the pass-through step after the
// launch, and the harness does not see it: a source it refuses ends the step
// with status 96 before bubblewrap starts, which the evidence shows as a
// first process that exited before a report. Whether bubblewrap mounts the
// object behind a descriptor, and closes the descriptor before the command
// inside starts, is a qualification item that nothing here records
// [qualify].
//
// bubblewrap must have the two options. The harness reads the release from the
// version line the tool prints (the step tool-versions, in a run) and ends the
// run when it is older than [MinBwrapBindFD], which is an assumption about the
// release that brought the options, to be confirmed at qualification. A version
// line that gives no release decides nothing: a note in summary.json says so
// and the run goes on, and a bubblewrap without the options then refuses the
// plan's arguments and starts nothing. A dry run asks no tool and decides
// nothing about it.
//
// The harness never judges the probe's report: the report's own statuses are
// the record. A failed or refused report, a report that never comes and a
// process that exits first are each recorded as such, and the run goes on to
// stop what is left and to write the evidence.
//
// # What it does not do
//
// It does not create a systemd unit, so the cases that need one (the stop
// rule, limits, S9) belong to the holder's qualification. It has no role
// lock, no lingering check and no qualification record store: the outside
// results for them are "not-checked". The outside half of assertion 7 is made
// from the harness's own check of the mount points right before the launch
// (the step recheck). The design wants that walk made with the previous
// instance proven stopped, and without a unit the harness has no control
// group to prove it with: when the configuration states that no lead has run
// in the scratch folders ("no_lead_has_run"), there is no previous instance,
// by that statement, and the detail of the result says so; without the
// statement a walk that found nothing is "not-checked". It does not check that the probe is a
// static executable, does not review a seccomp filter and does not narrow
// outbound network access. The real [Launcher] ([ExecLauncher], in its own
// file) is the only non-test code here that starts a process, and a test
// guards that. The tests of the harness use [FakeLauncher]; the tests of
// ExecLauncher start only the test binary itself, under a pseudo-terminal and
// as the stand-in for a tool asked for its version (a link to it under another
// name), never a sandbox tool, a script or a shell.
//
// The package builds on Linux/amd64 only; elsewhere it holds this
// documentation alone.
package harness
