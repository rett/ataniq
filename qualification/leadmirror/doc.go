// Package leadmirror is the opt-in harness that records what a real sandbox
// template does with a hostile lead clone. It checks the exchange mirror
// fetch (design case S16) and the intake review (design case S19) against the
// sandbox that the person who runs it names.
//
// # What it is
//
// A coordinator, by hand, writes a config file, names a sandbox template (the
// sandboxplan format) and runs the command qualify-mirror. The command builds
// a scratch state root with a hostile lead clone and an exchange mirror, runs
// the cases through [mirror.Mirror.Fetch], [intake.Inspect] and
// [verify.Run] with the given [sandboxplan.Planner], and writes one JSON
// document of facts: what the calls returned and what was seen on the host
// afterwards.
//
// # What it records, and what it does not
//
// The report holds facts only. No field says pass, supported or qualified,
// and none may be added: a fact is something that was observed in one run.
// Whether the facts are enough to rely on the sandbox is a decision for a
// reader, outside this package. The manual in docs/qualification-lead-mirror.md
// says who reads the facts and what a fact does and does not show.
//
// # How it is built
//
// [Run] takes the planner as a parameter and reaches a sandbox only through
// it. The command builds the planner from the template; the tests of this
// package give it [plantest.Fake], which runs plain git-upload-pack with no
// namespace. A planner that has no section for a request ends that case with
// [sandboxplan.ErrNotConfigured]; nothing runs without a sandbox.
//
// The harness makes its own fixtures (an upstream repository and a lead's
// clone) with git, in a folder of its own and with an explicit environment,
// before any hostile file is put in place. It moves the finished clone to
// leads/<role>/tree. It never runs git in that clone afterwards, never checks
// anything out of it, and names no path inside it. Everything that touches
// the mirror goes through the mirror, intake and verify packages, so the
// owner-side rules of those packages hold here too. Text that a lead wrote
// (ref names, paths, commit subjects, gate output) is shown in the report
// only after leadtext.Escape.
//
// # What it does not do
//
// It does not start a sandbox itself, does not read a real lead's state, does
// not touch any folder outside the scratch state root and the output file, and
// does not delete anything: the person who runs it removes the scratch root.
// It is not part of an ordinary test run. Its own tests use the fake planner
// and start no namespace, no service and no network connection.
package leadmirror
