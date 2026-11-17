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
// variable and imports only the standard library and the network package of
// this feature (sandboxnet), whose PastaArgs is a constant list. Every host
// path is a string the caller gives; the checks on those strings are
// lexical, so the caller resolves symbolic links before it hands a path over,
// and the checks cannot see what a path will be at run time. Other packages of
// the feature turn this data into a launch plan, create the mount points, and
// check the result.
//
// It does not build a bubblewrap or pasta command line, it never starts a
// process, and it does not create, read or verify any file: the digests of the
// seccomp file and of the managed-settings file are taken as given and are not
// compared with the files.
//
// # What the digest covers
//
// Digest is the SHA-256 of a canonical, versioned encoding of:
//
//   - the profile Version, the runtime and its pinned version, and the
//     identity mode;
//   - the bubblewrap flag set;
//   - the bind table: every row's kind, inside path, source role, link target,
//     folder flag, lead-writable flag and holder, with the project replaced by
//     a token and a host path by its source role;
//   - every environment name, and the value of every variable whose value the
//     profile defines: the fixed values, PATH, SHELL, the config variable, the
//     git and Go variables and every extra variable;
//   - the SHA-256 of the seccomp file (or "none"), as the caller gives it;
//   - the SHA-256 of the managed-settings file, as the caller gives it;
//   - the argument vector of sandboxnet.PastaArgs, the network boundary.
//
// # What the digest does not cover
//
//   - Host paths: the state root, every source of a bind, and the path of the
//     seccomp file and of the managed-settings file on the host. A source is
//     covered by its role only, so the digest does not say what the file or
//     folder at a source holds, except for the two files whose SHA-256 the
//     caller gives.
//   - The role and the project, and the owner's ids and login name.
//   - The values the host terminal or the lead gives: TERM, COLORTERM, LANG
//     and TZ, the lead identity (QARGI_LEAD), and USER and LOGNAME in owner
//     mode. Their names are covered.
//   - The content of the lead skills folder and of the instruction file, and
//     of the other folders and files bound from the host (the agent and Qargi
//     installs, the probe, the spec, the assignment and projection).
//   - What the package does not know: the real state of the host, and what the
//     resulting sandbox allows. The network argument vector is covered, not
//     what the network then reaches.
//
// So a digest names a profile, not a host: it is the key a qualification
// record is kept under, and the record says what was checked.
//
// The names a profile may add to the environment are checked against a list
// of refused names, name endings and name starts (see CheckEnvName). The list
// is a starting point for the owner to review and extend: it does not claim
// to name every credential or loader override.
//
// Per the owner decision R17, nested sandboxes are allowed for now: the flag
// set never contains the options that disable user namespaces.
package profile
