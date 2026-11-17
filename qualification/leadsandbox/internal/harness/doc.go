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
// validation of the configuration and of the executables, the folder of the
// attempt, the version of each tool, the own-clone check, the preparation of
// the mount points (only when the configuration says no lead has run), the
// mount-point check, the resolv.conf derivation, the resolution of every
// source to its real path, the report channel (a FIFO in a folder of the attempt
// that holds nothing else, whose reader is opened first), the canary, the plan
// ([plan.Build] and [plan.Verify]), the probe spec (with the identities of the
// owner and holder paths and their ancestors, which assertion 1 needs), a
// second check right before the launch, the launch through a [Launcher], the
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
// results for them are "not-checked". It does not check that the probe is a
// static executable, does not review a seccomp filter and does not narrow
// outbound network access. The real [Launcher] ([ExecLauncher], in its own
// file) is the only non-test code here that starts a process, and a test
// guards that. The tests of the harness use [FakeLauncher]; the tests of
// ExecLauncher start the test binary itself under a pseudo-terminal, never a
// sandbox tool.
//
// The package builds on Linux/amd64 only; elsewhere it holds this
// documentation alone.
package harness
