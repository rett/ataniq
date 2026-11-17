// Package profile is the lead sandbox profile as data. It is a proposal that
// awaits qualification and an owner decision; nothing here says the sandbox is
// qualified, safe or supported.
//
// The package describes what a sandboxed lead would see and what it would not:
// the two runtimes (claude and codex), the fixed inside paths, the host layout
// of one lead under a state root, the bind table of the lead-hosting design
// (section 3.3) as ordered data, the environment allowlist (section 3.4) with
// its values, the never-mounted check, and a digest of what defines the
// profile independent of the host and the lead.
//
// It is pure. It performs no I/O, makes no system call, reads no environment
// variable and imports only the standard library. Every host path is a string
// the caller gives; the checks on those strings are lexical, so the caller
// resolves symbolic links before it hands a path over, and the checks cannot
// see what a path will be at run time. Other packages of the feature turn this
// data into a launch plan, create the mount points, and check the result.
//
// It does not build a bubblewrap or pasta command line, it never starts a
// process, and it does not create, read or verify any file: a seccomp file's
// digest is taken as given and is not compared with the file.
//
// The names a profile may add to the environment are checked against a list
// of refused names, name endings and name starts (see CheckEnvName). The list
// is a starting point for the owner to review and extend: it does not claim
// to name every credential or loader override.
//
// Per the owner decision R17, nested sandboxes are allowed for now: the flag
// set never contains the options that disable user namespaces.
package profile
