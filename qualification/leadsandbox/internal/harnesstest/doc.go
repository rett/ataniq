// Package harnesstest builds, for the tests of the lead sandbox qualification
// harness, a fake world under a test's temporary folder: a hand-made clone
// (no git binary), the folders and files of every source the profile needs,
// stand-ins for the three executables (plain files, never run), a campaign
// folder and a configuration, together with hermetic replacements for the
// seams of harness.Options (the resolv.conf source, the sysctl and the path
// resolver), so that no test reads the host's files that matter or starts
// anything.
//
// It is used by tests only. Nothing here starts a process, creates a
// namespace or touches a folder outside the test's own. It does not say
// anything about the sandbox: it is unqualified test support.
package harnesstest
