// Package gitenv builds the git command line and the environment that
// owner-side code uses whenever it touches content a lead wrote.
//
// A managed lead works in its own clone inside a sandbox. Owner-side code
// never runs git in that clone. It runs git only with an explicit --git-dir
// on bare repositories that Ataniq owns, and it fetches the lead's branches
// into them. The objects, ref names and commit text are untrusted. Nothing the
// lead wrote may be executed or read as configuration by the owner's git.
//
// The package is a pure builder. [Git.Argv] returns an argument vector and
// [Git.Env] returns a complete environment. Neither starts anything. The
// caller runs the result, for example with os/exec, and must set
// exec.Cmd.Env to the environment as returned: nothing is inherited.
//
// What the builder does:
//   - It names the git executable by an absolute path and never searches
//     $PATH ([New], [Find]).
//   - It reads no user or system configuration, no attributes outside the
//     repository, and no credentials. It switches off hooks, file system
//     monitors, pagers, editors, askpass programs, credential helpers,
//     external diff programs, submodule recursion and every transport except
//     the ones the caller names.
//   - It turns on object checking (fsck) for fetches.
//   - It accepts only a short list of git subcommands, refuses options before
//     the subcommand, and refuses a relative or missing repository path.
//   - The list holds "merge-tree", so that the host can build a merge itself
//     ("merge-tree --write-tree") in a bare repository it owns. No working
//     tree is involved and no merge driver from the configuration is named.
//   - It accepts "hash-object" in one form only, "hash-object -tcommit -w
//     --stdin", with exactly these arguments in this order, and refuses every
//     other form. The host writes the commit of a merge with it, from a text
//     it wrote in full and passes on standard input, so the parents, the
//     author, the committer, the dates and the message are the host's. git
//     refuses a malformed text, but it accepts a header line it does not
//     know, so the text must not hold anything a lead wrote. "commit-tree"
//     is not allowed.
//
// The file "info/attributes" of a repository is the one attribute source no
// option or setting switches off: git reads it for every diff and merge,
// whatever else it is told. A line there can turn a text file into a binary
// one in a diff, or make a merge join both sides where it should report a
// conflict. [CheckInfoAttributes] is therefore a check the caller runs before
// such a command: it refuses a bare repository whose "info/attributes" exists
// and is not an empty regular file. It is a check and not a fence, and it
// relies on the repository being one that only the host can write.
//
// What it does not do:
//   - It does not make git safe to run in a repository an attacker controls.
//     The repository's own config file is still read, and the settings here
//     only override the keys they name. Callers use repositories that Ataniq
//     wrote, never a lead's clone. The settings are defence in depth.
//   - A fetch from a local path starts git-upload-pack in the source
//     repository, which reads that repository's config. Fetch from a
//     repository Ataniq owns, not from a path the lead can change while the
//     fetch runs.
//   - It checks the options of a command, and nothing else in the arguments.
//     Before the first "--" or "--end-of-options" every argument that starts
//     with "-" must be written "--name=value" or be one of a short list of
//     flags that take no value in that subcommand ("-m" takes one in
//     "update-ref" and "symbolic-ref", "--count" in "for-each-ref", and are
//     refused there). Any other option is refused: git lets an option such
//     as "-o" take the next argument as its value, even the end-of-options
//     marker, and would then read what follows as an option. There is no
//     exception: "git config" takes "--file=PATH", not "--file PATH". After
//     the marker git reads no options, so the rest is not checked, with one
//     exception: "git config" does not end its options at "--" and reads
//     the next argument as an option ("config -- --file=PATH --list" lists
//     the file). Argv refuses "--" for "config"; a caller writes
//     "--end-of-options" there. A test asks the installed git about every
//     subcommand and marker, so a pair that is not safe fails there. A
//     caller that builds arguments from untrusted text (a ref name, for
//     example) places "--end-of-options" or a fully qualified ref before
//     it. The options that name a program to run ("--upload-pack",
//     "--receive-pack", "--exec", "--ext-diff") are refused in every form,
//     also abbreviated as git accepts. The one way to name an upload-pack
//     program is [Cmd.UploadPack], for a fetch from a local path; Argv
//     quotes it, and the field comment says what the program receives.
//   - It does not confine the process: no namespace, no file system limit, no
//     network limit.
package gitenv
