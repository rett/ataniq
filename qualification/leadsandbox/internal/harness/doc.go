// Package harness is the logic of the opt-in lead sandbox qualification
// command. It is infrastructure for a proposal that awaits qualification and
// an owner decision: nothing here says the lead sandbox profile is qualified,
// safe or supported.
//
// This part holds the launcher: the [Launcher] interface, the real
// [ExecLauncher], which starts a command line with a pseudo-terminal in its own
// process group, the [FakeLauncher] of the tests, the report channel (a FIFO
// whose reader is opened first) and the stop rule applied to a process group.
// The real launcher is the only non-test code here that imports os/exec, and a
// test guards that. It is never run against pasta or bubblewrap by a test.
//
// The package builds on Linux/amd64 only; elsewhere it holds this
// documentation alone.
package harness
