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
//     tree, home and config folder are 0700 folders of the caller's user. A
//     tree whose .git names alternates (objects of another repository) is
//     refused. The file .git/config is not parsed.
//   - [ResolveSources] is where lexical paths meet the file system: it
//     resolves each host bind source with symbolic links evaluated (through an
//     injected [Resolver]; [RealResolver] is filepath.EvalSymlinks) and runs
//     the profile's never-mounted, other-runtime, other-lead and
//     other-project checks again on the real paths, so that a link cannot
//     hide a never-mounted folder. A never-mounted path that is a dangling
//     link is resolved through the link's target, or reported as unreadable;
//     [ResolveSourcesWith] takes the reader of links where the resolver is not
//     the real file system.
//
// # What the holder must do
//
// The package checks; the holder of the lead's launch (the probe command, the
// harness, the runner) must make the checks mean something. Its obligations,
// in order:
//
//  1. Call [CheckOwnClone] when the tree is made. Then call [Prepare], only
//     before the lead's first run, with the statement that no lead has run.
//     Prepare does not run [CheckOwnClone] itself. At every start after that
//     call [Check] and [ResolveSources], not [Prepare].
//  2. Run [Check] and [ResolveSources] only after the previous unit has gone
//     and its control group is empty, and right before the launch, with
//     nothing between them and the launch that runs a lead process. Nested
//     sandboxes are allowed for now, so the sandbox's own process tree does
//     not show that no lead process remains; only an empty control group
//     does. A check made while a lead process remains says nothing.
//  3. Pass the resolved real paths ([ResolvedSource].Real, or the source after
//     checking that Real equals Source) to the plan. bubblewrap resolves a
//     source by its path when it binds, so a source that is checked and a
//     different one that is bound is a check that did not happen. The probe's
//     assertions from inside the sandbox stay the final check.
//  4. Know the limits of the records folder. It lies outside the state root,
//     so the lookup from the state root does not reach it: only its last
//     component is opened without following a symbolic link, nothing above it
//     is looked for, and its mode and owner are not checked. The holder must
//     make and check that folder itself.
//
// # Limits
//
// The checks are made at one moment. A lead that runs between a check and the
// start can change what it checked; the folders are the lead's to write while
// it runs. The read-only binds of .git/config, .git/hooks and .git/info are
// defence in depth: they do not stop a lead from renaming .git (renaming the
// parent of a mount point is allowed) and building another, and Check and
// CheckOwnClone cannot tell a rebuilt .git from the original. The primary rule
// is that no owner-side tool uses a lead's tree (the lead-hosting design,
// section 3.3); this package does not enforce it. A symbolic link above
// the state root is not looked for: the caller opens a state root it owns.
// The own-clone check looks for a .git entry in each folder from the tree's
// parent up to the state root, not above it. The exclude check is textual: it
// looks for the anchored line and takes any later line that starts with "!"
// as un-listing it, and does not evaluate the patterns git knows. The package
// does not start a process, run git, create a namespace, or read any provider
// folder. It reads, bounded, the one file .git/info/exclude.
//
// A tree placeholder whose path holds a character git treats as a pattern
// (backslash, star, question mark, opening bracket, a trailing space) is
// refused, because an anchored line would then not name only that path.
//
// The package is Linux only: on other systems it has no behaviour. Files are
// made with mode 0755 (folders) and 0644 (files), before the umask.
package mountpoints
