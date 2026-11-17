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
// # No bind source the lead can write (rule G1)
//
// bubblewrap resolves a bind source on the host, outside every sandbox, and
// follows a symbolic link in it. A source that lies in a folder the lead can
// write could therefore be replaced by a link that brings a folder of the
// owner into the sandbox. So the profile holds one rule: no row of the bind
// table has a source in or below a host folder the lead can write (the home,
// the config folder, the tree, the records folder, the requests folder and the
// mail folder: the sources of the read-write rows). A folder the lead writes
// is bound whole from an Ataniq-owned parent, or its content is copied in.
// [ValidateTable] refuses a table that breaks the rule, and so does
// [Inputs.Validate].
//
// The rule moved three rows. The tree's .git/config, .git/hooks and .git/info
// are still bound read-only at their places inside, over the tree, but their
// sources are no longer the tree's own files: they are the entries config
// (a file), hooks and info (folders) of an Ataniq-owned folder, [Inputs.GitMeta],
// with the source role [RoleGitMeta]. The folder is required and must be
// [Layout.GitMeta], <state>/leads/<role>/gitmeta: beside the lead's folders, below
// the state root, and covered by the rules about another lead's state. Another
// package fills it before the lead's first run (the
// mountpoints package: a copy of the clone's config and of the regular files
// of .git/info, and an empty hooks folder).
//
// What this changes for the lead: the copies of config and info are read-only
// binds of files the lead cannot write, so the lead cannot change the files
// that are bound; a hook of the clone is not there, so a managed lead's tree
// has no hooks; and a change to the clone's git configuration is made by
// Ataniq, outside, in the copy (the lead-hosting design, section 3.3, leaves
// open whether the flow verbs need to write git configuration; qualification
// case S5 settles it). It does not say which configuration the lead's git
// reads: a git inside the sandbox can still be pointed at configuration the
// lead writes (through its environment, or through a .git it rebuilds in the
// tree), which lies inside the lead's own authority; the mountpoints package
// refuses a clone config that has an include or includeIf section when it
// makes the copy. The copy is never read as configuration by a tool outside
// the sandbox. The flag [Bind.LeadWritable]
// is kept for the targets: the mount points of these three rows still lie in
// the tree, and Ataniq still makes them there before the first run.
//
// # The requests, mailbox, mail and coordinator rows
//
// Every table has four rows whose sources are the layout's, not the caller's
// (the lead-hosting design, revision 4, section 3.3):
//
//   - [RequestsInside], /ataniq/requests, read-write, from [Layout.Requests],
//     <state>/leads/<role>/requests. In this version it is only a bound
//     folder: nothing in Ataniq reads it;
//   - [MailboxInside], /ataniq/mailbox, read-only, from [Layout.Mailbox],
//     <state>/mailbox/<role>, which is Ataniq's;
//   - [MailInside], /ataniq/mail, read-write, from [Layout.Mail],
//     <state>/leads/<role>/mail, with one subfolder per feature;
//   - [CoordInside], /coord, read-only, from [Layout.Published],
//     <state>/pub/<project>, which holds only <feature>/qargi, <feature>/out
//     and <feature>/archive of the project's coordinators.
//
// A bind cannot be added to a running sandbox, so the four folders are fixed
// at the lead's start: the rows are in the table and in the digest for both
// runtimes, whether or not the lead has coordinators. The package says where
// the folders lie and how they are bound, and nothing about what is in them.
//
// # The git identity is bound as a folder
//
// GIT_CONFIG_GLOBAL names [GitIdentityInside], /ataniq/git/config. The row
// that brings it in binds the Ataniq-owned folder that holds the file,
// [Inputs.GitIdentityDir], read-only at [GitIdentityDirInside], and the file
// has the fixed name [GitIdentityFileName] in it. A bind of a single file is a
// bind of the file that was there when the sandbox started: when the holder
// replaces the file by writing a new one and renaming it into place, the
// sandbox keeps the old one. A bind of the folder shows the new file. The
// whole folder is readable inside, so it holds the identity file alone.
//
// The managed-settings file and the instruction file stay binds of a single
// file: the design names only the identity as a folder, and the instruction
// file lies over a place inside the config folder, which is itself bound. The
// consequence is the one above: a managed-settings or instruction file that
// Ataniq replaces while the lead runs is seen at the lead's next start, not
// before.
//
// # Other state under the state root
//
// The profile knows the state root, so it refuses, whatever the caller lists
// as never-mounted, a bind source that lies in or above: another lead's state;
// the lead's own holder folder ([Layout.Holder]); another project's mirror;
// another role's mailbox; another project's published folder; and any
// coordinator's tree, home or login ([Layout.CoordsDir],
// [Layout.CoordLoginDir]). The lead's own mailbox and the project's own
// published folder are each the source of exactly one row: a source may be
// that folder only as that row, and no source may lie inside it.
// [StateFolders.Reason] holds these rules, [Inputs.Validate] applies them to
// every row, and [Inputs.CheckSource] applies them to a path that is no row
// of the table. They are lexical, like every check here: the mountpoints
// package runs them again on real paths.
//
// # What the digest covers
//
// Digest is the SHA-256 of a canonical, versioned encoding of:
//
//   - the profile Version, the runtime and its pinned version, and the
//     identity mode;
//   - the bubblewrap flag set;
//   - the bind table: every row's kind, inside path, source role, link target,
//     folder flag, lead-writable flag, holder, and whether the row is applied
//     only if the host has it (Bind.Optional), with the project replaced by a
//     token and a host path by its source role; the extra placeholders in the
//     order of their targets;
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
//     installs, the probe, the spec, the assignment and projection), the
//     content of the git identity file GIT_CONFIG_GLOBAL names (it can set
//     hooks and helpers that run), the derived resolv.conf, and the Qargi
//     records.
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
