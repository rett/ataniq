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
package verify
