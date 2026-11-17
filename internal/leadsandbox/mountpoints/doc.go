// Package mountpoints is the host-side half of the bind targets that lie in
// folders a lead can write (the lead-hosting design, sections 3.3 and 3.6, and
// acceptance case S17). It is a proposal that awaits qualification and an
// owner decision; nothing here says the sandbox is qualified, safe or
// supported. It describes what is generated and checked, on the real file
// system, and nothing else.
//
// bubblewrap makes a missing mount point itself, and it would do so inside a
// folder the lead writes, where a symbolic link planted at the target could
// send the bind somewhere else. So Ataniq makes every such mount point itself,
// before the lead's first run and outside any sandbox, and every later start
// only checks them.
//
// # What it offers
//
//   - [List] turns the bind table of the profile (package profile: the
//     LeadWritable flag, Within, HostMountPoint) into the mount points that
//     lie in a lead-writable host folder: the host path, the folder it lies in
//     (home, config folder, tree or records), the relative path, folder or
//     file, and whether it is a tree placeholder outside .git.
//   - [Prepare] makes them, once, before the first run. It makes a missing
//     folder or empty file through a [safefile.Root] of the folder the mount
//     point lies in, keeps an existing entry of the right type, and refuses a
//     symbolic link or a wrong type. It lists each tree placeholder outside
//     .git in .git/info/exclude as one anchored line. It needs the caller's
//     statement that no lead has run in these folders yet ([PrepareOptions]);
//     without it, it refuses, because nothing is written into a folder a
//     lead has run in. It is idempotent.
//   - [Check] runs on every start. It is read-only: it creates and repairs
//     nothing, and returns every problem it finds, typed and bounded.
//   - [CheckOwnClone] checks that the tree is the lead's own full clone, not a
//     worktree, submodule or folder below another repository, and that the
//     tree, home and config folder are 0700 folders of the caller's user.
//   - [ResolveSources] is where lexical paths meet the file system: it
//     resolves each host bind source with symbolic links evaluated (through an
//     injected [Resolver]; [RealResolver] is filepath.EvalSymlinks) and runs
//     the profile's never-mounted and other-runtime checks again on the real
//     paths, so that a link cannot hide a never-mounted folder.
//
// # Limits
//
// The checks are made at one moment. A lead that runs between a check and the
// start can change what it checked; the folders are the lead's to write while
// it runs, and the read-only binds of .git/config, .git/hooks and .git/info
// are what protect them afterwards, not this package. A symbolic link above
// the state root is not looked for: the caller opens a state root it owns.
// The own-clone check looks for a .git entry in each folder from the tree's
// parent up to the state root, not above it. The exclude check is textual: it
// looks for the anchored line and for a later negation of the same line, and
// does not evaluate every pattern git knows. The package does not start a
// process, run git, create a namespace, or read any provider folder. It
// reads, bounded, the one file .git/info/exclude.
//
// A tree placeholder whose path holds a character git treats as a pattern
// (backslash, star, question mark, opening bracket, a trailing space) is
// refused, because an anchored line would then not name only that path.
//
// The package is Linux only: on other systems it has no behaviour. Files are
// made with mode 0755 (folders) and 0644 (files), before the umask.
package mountpoints
