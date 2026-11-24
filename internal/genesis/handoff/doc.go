// Package handoff turns the stored plan of a finished guided interview into a
// hand-off for Qargi: a few comment lines and one command line, which the
// owner reads and runs himself, at his desk. It is a pure library: no input
// or output, no clock, no program run and no network. Nothing here runs Qargi
// or anything else, and nothing here is an approval: the command it writes
// only prints Qargi's own plan and token, and creating the project needs the
// owner's own words on Qargi's side.
//
// # What it emits
//
// [Emit] takes a plan and the id of the thread it was stored in and returns a
// [Handoff]. Only a plan of the software template is served today; any other
// is refused with a [*Refusal] whose text is fixed ([TextNotAvailable]).
//
// The command is one line of a closed grammar ([ValidateCommand]):
//
//	qargi project new NAME --methodology gitflow
//
// NAME is made by the host, never taken from the owner's words: a prefix from
// a closed map of the plan's title (a catalog label: "A full web app" is
// webapp, "A one-page tool" is webtool, anything else software) and the first
// six digits of the thread's id, held to Qargi's grammar of a project name,
// ^[a-z][a-z0-9]{0,23}$. Qargi fills its methodology's required parameter
// project with NAME itself, so the command does not repeat it, and the
// generated Makefile's stub targets (bootstrap and doctor) need no test
// command, so none is given.
//
// The command never carries: a repository URL, a provider, a folder (the
// owner runs it in the folder where he keeps projects), declared gates, a
// plan token, approval words, a request for JSON, a deployment, or any text of
// the plan but what the closed map above reads. Above it, comment lines (each
// starting "# ") say which plan it is for (the plan's digest, short, its
// revision and the thread's tag), that nothing has been run, that creating the
// project needs the owner's own words, that the plan's tasks are not
// imported, and, in fixed words from closed maps, what the plan names that the
// command does not carry: where the files live, the external services by
// name, and the monthly budget band, which is advisory.
//
// The owner's own words (the plan's notes) are never read: two plans that
// differ only in them, and in the brief digest they make differ, give the
// same command and the same lines but the digest in the first.
//
// The package imports the standard library and the genesis packages only.
package handoff
