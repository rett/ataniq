// Package verify runs the gates of a lead branch in a verification sandbox.
//
// It is step 4 of the reviewed intake of lead work. The gates (the project's
// checks and suite) never run as the owner. They run in a scratch clone taken
// from the exchange mirror, with a home of their own, no provider login and
// no lead credential, and the scratch folders are deleted afterwards.
//
// # The script
//
// [Script] builds the fixed text that /bin/sh -c runs inside the sandbox. It
// makes a repository in the scratch folder, fetches the tip ref from the
// mirror into refs/verify/tip (no tags, no hardlinks, no alternates), checks
// that the ref is exactly the tip the review was made for, checks it out
// detached, and then runs each gate in order from the tree's root. Every
// part that varies is one single-quoted word, so a gate argument with quotes,
// spaces or $(...) stays one word and is never run by the shell.
//
// Each gate runs as a program, in a subshell that changes to the tree's root
// and execs it. A gate therefore cannot change the script's own shell (its
// folder, variables or exit), and a failed gate ends the script. For the same
// reason Gate.Argv[0] must be an absolute path or a plain command name
// without a slash, and not a shell builtin or reserved word such as cd, exit,
// exec or eval; a request with such a gate is refused when it is validated.
//
// Before each gate the script prints a line "ataniq-verify: gate <name>".
// Gate output can contain the same line, so these lines can be imitated and
// prove nothing; they only help a human read the output.
//
// Exit statuses 90 to 99 are reserved for the script's own failures: 90 means
// the tip ref no longer resolves to the reviewed tip, 91 that the scratch
// repository could not be made or the ref could not be fetched, 92 that the
// checkout failed. A gate can exit with one of these statuses too, and it is
// reported as it is. The status alone does not say who produced it.
//
// # What a pass means
//
// A pass means the gates ran inside the sandbox on the tip's tree and each
// exited 0, with the output inside its cap and no time-out. The tip's own
// build files decide what a gate does, so a pass is evidence for the
// reviewer and not a proof. The review and the sign-off are the boundary.
// A pass does not say that the code is correct, safe or free of hostile
// behaviour.
//
// [PlanDigest] names the gates that were run, so a result can be tied to the
// plan it came from.
//
// # The run
//
// [Run] checks the request first. It then makes two new folders in
// Request.WorkRoot, a scratch folder and a home folder (mode 0700, unique
// names), asks the planner for the verify plan ([sandboxplan.Planner.Verify]),
// and starts the plan's prefix followed by /bin/sh -c and the script. The
// command gets the plan's environment and nothing from the caller's. Standard
// output and standard error come back together, capped. The runner is an
// interface; ordinary tests use a fake planner and a fake runner and never
// start a namespace.
//
// A failed gate, a time-out, an output cap and a moved tip are results with
// Passed false. An error means the run could not be attempted: a refused
// request, a planner that has no sandbox ([sandboxplan.ErrNotConfigured]), or
// folders that could not be made. Nothing is run without a sandbox.
//
// Owner-side code reads nothing from the scratch or home folder. What comes
// back is the exit status and the captured output. The output is raw text a
// lead influenced; a caller shows it only through leadtext.
//
// # Cleanup
//
// Both folders are removed afterwards, also after an error, a time-out or a
// panic in the runner. The sandbox may leave symlinks to anywhere, FIFOs,
// very deep trees, and files and folders that belong to the caller but have
// no write bit. The removal works from folder descriptors opened without
// following symlinks, makes a folder writable before it enters it, unlinks
// a symlink and never its target, and moves a too-deep folder up so that it
// holds only a few hundred descriptors. It relies on WorkRoot being an
// owner-only folder, on /proc being mounted, and on the caller owning what is
// removed; see workdir_linux.go. If a folder cannot be removed, Run returns
// an error that names it, with Passed false. A leftover is never ignored.
//
// # What it does not do
//
// It builds no sandbox, runs no gate by itself outside the plan, and does not
// keep the result. It does not read the scratch clone, and it does not decide
// whether a lead's work is accepted.
package verify
