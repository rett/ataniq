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
// supplies from [mountpoints.PlanWalks]), case S21 when it is requested (the
// step planted-bind-paths, see "Case S21"), the probe spec (with the identities of the
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
// [Options.Unattended] is set. A run that requests case S21 also needs
// "scratch_folders_disposable": true. Without [Options.Run] it validates, builds and
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
// with status 96 before bubblewrap starts (the probe's tests show that much).
// What the evidence of a real run then holds has not been observed: no report
// line can come, so the expectation is a first process that exited before a
// report, and how pasta passes the step's status on is not known here.
// Whether bubblewrap mounts the object behind a descriptor, and closes the
// descriptor before the command inside starts, is a qualification item that
// nothing here records [qualify].
//
// bubblewrap must have the two options. The harness reads the release from the
// version line the tool prints (the step tool-versions, in a run) and ends the
// run when it is older than [MinBwrapBindFD], which is an assumption about the
// release that brought the options, to be confirmed at qualification. A version
// line that gives no release decides nothing: a note in summary.json says so
// and the run goes on. A bubblewrap without the options is then expected to
// refuse the plan's arguments as unknown and to start nothing; that has not
// been observed, and the plan has no form that binds such a source by path
// whatever bubblewrap does. A dry run asks no tool and decides nothing about
// it.
//
// The harness never judges the probe's report: the report's own statuses are
// the record. A failed or refused report, a report that never comes and a
// process that exits first are each recorded as such, and the run goes on to
// stop what is left and to write the evidence.
//
// # Case S21: planted bind paths
//
// S21 (the design, section 7.3) is the case of the walk before each start
// (section 3.6): every bind target that lies in a folder the lead writes must
// be absent or of the expected type, and a symbolic link or a wrong type
// refuses the start with the path. The case plants, at each path that walk looks
// at, a symbolic link to a path outside (the attempt folder) and then an entry
// of the wrong type (a regular file where a folder belongs, a folder where a
// file belongs), runs the checks a start makes and records, for each path and
// kind, whether the start was refused, which checks refused, the problem the
// start walk reported and the path it names, that nothing was launched, and
// whether the original entry was put back. Its facts are planted_paths in
// summary.json (evidence format 3), and its results are
// planted-paths-refused, planted-paths-refused-other-runtime and
// planted-paths-restored, each ok, failed or not-checked. A result is ok only if
// every plant of its leg was refused with a problem of the kind of the plant
// that names the planted path; a plant that was not is failed; a plant that
// could not be made, a path that could not be restored and a case that did not
// run are not-checked, never ok.
//
// The paths are the mount points of [mountpoints.List] and the tree's .git
// folder, which the walk looks at too. mountpoints does not export the list its
// walk makes, so the harness derives it the way the walk does; an exported list
// would be better, because a path added to the walk would not be planted until
// the harness is changed. The checks of a start are the own-clone check, the
// walk of the mount points (through [Options.CheckMountPoints], which decides:
// the case needs it to refuse with the planted path), the resolution of the bind
// sources, the plan's own check and the check of every bind source the plan
// binds; the case launches nothing and writes nothing but the plant.
//
// Each plant is made in the folder the path lies in, opened from the state
// root's handle without following a link; the original is set aside by a rename
// in that folder (the name with [PlantAsideSuffix]), the entry is planted beside
// it, and afterwards the plant is removed (only if it is still what was planted)
// and the original renamed back, and the result is looked at: the same type,
// mode, owner, device and inode, or the name absent as it was. The next plant
// starts from a clean state. A path that is left planted ends the case with a
// failure that names it, and the later plants are not made.
//
// The case guards what it writes into: it runs only when it is requested by
// name, with Options.Run, with "coordinator_reviewed" and with the statement
// "scratch_folders_disposable": true, which says the folders under the state
// root are disposable scratch folders and not a live lead's. A run without them
// is refused with a [GateError]. A dry run does nothing for it and says so (the
// step is skipped, and planted_paths says why).
//
// It is its own phase of the attempt: the step runs after the plan is built and
// verified and before the spec and the second check, so the folders are as
// found when the steps that follow, the launch phase included, use them. When S21
// is the only case requested the run launches nothing; with other cases the
// launch phase follows as it would without S21.
//
// Across a runtime switch (the owner's decision Q24(a): S21 covers every start)
// the case also checks a start of the other runtime, by the walk alone, with
// the inputs of the configured runtime changed to the other runtime and its
// managed-settings path taken from the profile (the configured one where the
// profile has none: the walk does not read it). It plants the paths of that
// walk that lie in a folder the configuration names for the configured runtime:
// the .git paths and the place of the config folder in the home. The places of
// the skills and the instruction file are in the other runtime's own config
// folder, which nothing in the configuration names: planted_paths lists them as
// not covered, and they need a second run with the other runtime's
// configuration.
//
// What this case does not cover: the plant is made by the harness from outside,
// a stand-in for a lead that plants from inside (the attended variant, in which
// a lead plants in one run and the next run is refused, is a manual
// procedure); it does not stop the lead and start the next epoch, because the
// harness has no unit; the repeat for a coordinator and the mirror fetch with
// .git replaced by a gitdir: file belong to other features. The case has not
// been run on a host: nothing here says the walk is qualified.
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
