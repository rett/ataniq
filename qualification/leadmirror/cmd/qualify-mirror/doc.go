// Command qualify-mirror is the opt-in harness that records what a real
// sandbox template does with a hostile lead clone: the exchange mirror fetch
// and the intake review of lead work.
//
//	qualify-mirror --config FILE --out FILE
//
// FILE of --config is a JSON config that the person who runs the command
// writes (see package leadmirror and docs/qualification-lead-mirror.md). The
// command refuses to run without the config, without the acknowledgement
// "i_run_this_on_purpose": true in it, or without a readable sandbox template.
// It builds a scratch state root, runs the cases through the template and
// writes one JSON document of facts to the file of --out, which must not exist.
//
// The command is for a coordinator, by hand. It is not part of any ordinary
// test run, is not built by the Makefile, never touches a real lead's state,
// and its report holds no pass, supported or qualified field.
package main
