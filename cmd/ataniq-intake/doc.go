// Command ataniq-intake is the owner's command for the reviewed intake of a
// managed lead's work.
//
// A managed lead works in its own clone, inside a sandbox, and its branches
// are untrusted. The only way out of the lead is the exchange mirror, a bare
// repository that Ataniq keeps in its state folder. This command moves work
// from a lead into the owner's upstream branch in steps that the owner types:
// init makes the mirror, fetch brings a lead's branches into it, branches
// lists them, review shows what a branch changes and which paths need their
// own sign-off, verify runs the gates in a sandbox, and integrate moves the
// upstream branch to the reviewed tip, fast-forward only. The sign-off is made
// of arguments. The command never asks a question and never reads a terminal.
//
// Everything a lead wrote is hostile. Every line of output that holds lead
// text is escaped with the leadtext package, sits on an indented line, and is
// under a heading that carries leadtext.Label. A line that starts in the first
// column is the command's own. The one exception is the patch that review
// --diff prints: it follows its own heading, is written unindented through the
// lead-text writer, which keeps its tabs, and every line of it starts with a
// diff marker or is diff content, so none can pass for a heading.
//
// The command only parses its arguments and calls the packages of the
// exchange, the mirror and the intake. It starts no process and opens no
// socket itself, and an import test keeps it that way. Git runs only through
// those packages, in the mirror, never in a lead's tree. Sandbox commands come
// from a template file that the owner wrote; without a usable template the
// commands that need a sandbox refuse, and nothing runs unsandboxed. No
// sandbox is shipped or qualified with this command.
//
// The owner's working checkout is never touched. It gets the commits by its
// own fetch from the mirror.
//
// Before the sign-off, review shows the gates of the verification records of
// the tip when it is given the records folder, and after an integration the
// gates of the verification that allowed it are shown, so that the owner sees
// what a pass stands for.
//
// Exit statuses: 0 success, also when the branch moved and the intake is
// recorded but the update ended unclearly or the records folder could not be
// synced (a warning on standard error); 1 a refusal that the owner can act on
// (sign-off incomplete, not verified, moved, nothing to integrate, a limit, a
// hostile tree, a commit that names an encoding other than UTF-8 or is not in
// the form git writes, a template path that exists on the host, busy, no
// usable sandbox); 2 usage; 3 any other failure, among them an update whose
// outcome is unknown: the owner looks at the upstream branch with branches
// before trying again.
//
// The command is provisional.
package main
