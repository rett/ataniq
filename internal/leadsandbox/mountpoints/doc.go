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
//     (home, config folder, tree or Qargi records), the relative path, folder or
//     file, and whether it is a tree placeholder outside .git. The requests
//     and the mail folder are lead-writable too, but no row of the table has a
//     target inside them, so they hold no mount point.
//   - [Prepare] makes them, once, before the first run. It makes a missing
//     folder or empty file through a [safefile.Root] of the folder the mount
//     point lies in, keeps an existing entry of the right type, and refuses a
//     symbolic link or a wrong type. It also fills the git metadata folder
//     (see below). It needs the caller's statement that no lead has run in
//     these folders yet ([PrepareOptions]); without it, it refuses, because
//     nothing is written into a folder a lead has run in. It is idempotent.
//   - [Check] runs on every start: the walk of rule B1 over every bind target
//     in a folder the lead writes (see below). It is read-only: it creates and
//     repairs nothing, and returns every problem it finds, typed and bounded,
//     each naming the path it is about.
//   - [CheckOwnClone] checks that the tree is the lead's own full clone, not a
//     worktree, submodule or folder below another repository, and that the
//     tree, home, config folder, records folder, requests folder and mail
//     folder are 0700 folders of the caller's user. A tree whose .git names
//     alternates (objects of another repository) is refused. The file
//     .git/config is not parsed.
//   - [CheckBindSources] takes the rows [ResolveSources] gave and opens, for
//     each, the path bubblewrap will bind from the root of the file system with
//     openat2 (no symbolic link, no magic link, on any component), refusing a
//     path that has a link anywhere, naming the row by its inside path and the
//     link. The path it opens is [ResolvedSource].Bound: the row's Source for
//     every row that is not a row of the host, the real path for a row of the
//     host. It is the last check before a launch.
//   - [ResolveSources] is where lexical paths meet the file system: it
//     resolves each bind source with symbolic links evaluated (through an
//     injected [Resolver]; [RealResolver] is filepath.EvalSymlinks) and runs
//     the profile's never-mounted and other-runtime checks and its rules
//     about other state (another lead, the holder folder, another project's
//     mirror and published folder, another role's mailbox, the coordinators'
//     trees, homes and logins) again on the real paths, so that a link cannot
//     hide a never-mounted folder. It refuses, for every row that is not a row
//     of the host, a source whose real path differs from its lexical one (a
//     link anywhere on it): a source Ataniq supplies must be given as its real
//     path. A row of the host (/usr, the /etc files) may resolve through the
//     host's own links, and its real path is reported. A never-mounted path
//     that is a dangling link is resolved through the link's target, or
//     reported as unreadable; [ResolveSourcesWith] takes the reader of links
//     where the resolver is not the real file system.
//   - [SourceWalks] gives, for each bind row that Ataniq supplies, the folder
//     to open by name, the path to walk beneath it and the type the source must
//     have ([SourceWalk]): the state root and the whole path below it for a
//     source below the state root, the source's own parent folder and its name
//     for any other. It is data, taken from the inputs alone, for a
//     launcher-side step that walks the source by handle and gives bubblewrap
//     the descriptor it checked; nothing in this package does that step.
//     [ResolveSources] puts the same walk on each row it returns.
//
// # Handles, not names (rule B1)
//
// Rule B1 of the design (revision 4, section 3.6): host-side code never binds
// or reads a path any of whose components a sandbox can write. It opens the
// nearest folder no sandbox can write by name, walks every further component
// from that handle with openat2 (RESOLVE_BENEATH and RESOLVE_NO_SYMLINKS),
// refuses a symbolic link and an unexpected type, and does so at the moment of
// use, every time: an earlier check is never reused.
//
// For [Check], [Prepare] and [CheckOwnClone] the folder opened by name is the
// state root, which the caller owns. Every folder below it that they work in is
// opened once per call, from a handle and never by its name
// ([safefile.Root.SubRoot]: one lookup without links, the type decided on the
// descriptor the lookup returned): the home, the runtime's config folder, the
// tree, the records, requests and mail folders, the lead's state folder and the
// git metadata folder from the state root's handle; the folder the git metadata
// folder is built in from the handle of the lead's state folder; and, for the
// own-clone check, the tree's .git from the tree's handle. The handle is kept
// for the call, and every later step (a look at a mount point, a folder or file
// made, a file read, the mode and owner of the folder itself) is beneath the
// descriptor that one lookup returned. So no folder is looked at and then
// opened again by its name, and what a lead does to the name between two steps
// changes nothing: the folder that was checked is the folder that is used.
// Nothing found by one call is kept for the next.
//
// The walk before each start ([Check]; [Prepare] makes the same walk before the
// first) covers every bind target that lies in a folder the lead writes: the
// runtime's config folder in the home (.claude or .codex), the places of the
// skills and of the instruction file in the config folder, the tree's .git
// itself and its config, hooks and info, the tree placeholders, and the place
// of the records when it lies in the home. Each is looked up beneath the handle
// of the folder it lies in, in one lookup that follows no link in any
// component, and its type is decided on the descriptor the lookup returned. A
// symbolic link (at the path or before it, dangling or not, pointing inside or
// outside) and a wrong type (a file where a folder belongs, a folder where a
// file belongs, a FIFO, a socket, a device) refuse the start with a problem
// that names the path; a component on the way that is refused is named in the
// detail. The design asks that each target be absent or of the expected type;
// this package is stricter and wants each mount point present, as [Prepare]
// made it, because bubblewrap would otherwise make it in a folder the lead
// writes. The tree's .git is no mount point and may be absent (its three mount
// points are then the missing ones). The walk is made for the runtime of the
// inputs, so a start under the other runtime walks that runtime's places. It
// is the unit-level ground of qualification case S21, which is not yet run.
//
// What these three still open by name is the state root alone: it is the
// folder the walk starts from, and a link at or above it is the caller's to
// rule out (see Limits). [ResolveSources] and [CheckBindSources] work on the
// paths of the bind sources, as described above.
//
// # The git metadata folder (rule G1)
//
// bubblewrap resolves a bind source on the host, outside every sandbox, and
// follows a symbolic link in it. The tree's .git/config, .git/hooks and
// .git/info are bound read-only over the tree, so their sources must not be
// the tree's own files: a link a lead planted there would be followed at the
// next start. Their sources are the entries of an Ataniq-owned folder,
// profile.Inputs.GitMeta, which the profile requires to be the layout's folder
// <state>/leads/<role>/gitmeta: outside the home, the config folder and the
// tree, and below the state root, so the lookup from the state root without
// links covers every component of its path. The mount points in the tree are
// still made and checked as before; they are only the targets.
//
// [Prepare] fills the folder once, before the lead's first run. It builds it
// in a temporary sibling in the lead's state folder (<state>/leads/<role>/
// gitmeta.tmp), with mode 0700 (its files 0600, each created exclusively),
// syncs each file and each folder, and then moves it into its place with
// renameat2 and RENAME_NOREPLACE, so the place never holds a folder that is
// only partly written and nothing is ever replaced. The sibling is made with
// MkdirAll, which keeps a folder that exists: a leftover sibling is refused
// before, and a second [Prepare] that races the first is stopped by the
// exclusive creation of the files:
//
//   - config is a copy of the clone's .git/config, read through the tree's
//     handle: a regular file of at most 1 MiB, not hard-linked, and without an
//     include or includeIf section. [Prepare] refuses ([ErrInclude]) a config
//     that has one, found by a bounded scan of the section headers (any case,
//     any subsection); no other key is interpreted, so core.hooksPath,
//     core.fsmonitor and the like are copied as they are;
//   - hooks/ is empty: the clone's own hooks are not copied, because a managed
//     lead's tree has no hooks;
//   - info/ holds a copy of each regular file directly in the clone's
//     .git/info (at most 64 entries, 1 MiB each, 4 MiB in all; a link, a folder
//     or another type is skipped; a hard-linked file is refused), with the
//     placeholder lines of the tree placeholders in exclude, as one anchored
//     line each, now written in this copy.
//
// A folder that exists is kept and only checked, so a second [Prepare] changes
// nothing. A [Prepare] that fails or is interrupted while it builds leaves the
// temporary sibling and no folder in the place, so [Check] refuses the start
// (the folder is missing), and the next [Prepare] refuses the leftover with a
// reason that names it ([ErrIncomplete]), never reusing it: remove it (nothing
// in it is the lead's) and call [Prepare] again before the first run. [Check]
// makes sure on every start that the folder and its three entries
// exist with the right types, modes and no link, and that info/exclude lists
// every placeholder; what the lead does to the clone's own .git/config,
// .git/hooks or .git/info does not change what is bound.
//
// What this changes for the lead: the copies of config and info are bound
// read-only and the lead cannot write the folder they come from, so the lead
// cannot change the files that are bound; there are no hooks in the folder; and
// a change to the clone's git configuration is made by Ataniq, outside, in the
// copy. That is all it says. It does not say which configuration the lead's git
// reads: a git inside the sandbox can still be pointed at configuration the
// lead writes (through its environment, or through a .git it rebuilds in the
// tree), and that lies inside the lead's own authority, as anything the lead
// writes in its own folders does. The refusal of an include section keeps the
// copy itself from naming a file the lead writes. The copy is never read as
// configuration by a tool outside the sandbox (Ataniq writes it and no
// owner-side tool uses a lead's tree: the primary rule of the design, section
// 3.3, which this package does not enforce). The lead-hosting design, section
// 3.3, leaves open
// whether Qargi's flow verbs need to write git configuration (branch
// tracking): qualification case S5 settles it.
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
//     call [Check], [ResolveSources] and [CheckBindSources], not [Prepare].
//  2. Run [Check], [ResolveSources] and [CheckBindSources] only after the
//     previous unit has gone and its control group is empty, and right before
//     the launch, with nothing between them and the launch that runs a lead
//     process: [Check] first, then [ResolveSources], then [CheckBindSources]
//     on the rows it gives (the last step before the launch). Nested
//     sandboxes are allowed for now, so the sandbox's own process tree does
//     not show that no lead process remains; only an empty control group
//     does. A check made while a lead process remains says nothing.
//  3. Make the path that is bound the path that is checked, for every row.
//     bubblewrap binds the path the plan names and follows a link in it, so a
//     source that is checked and a different one that is bound is a check that
//     did not happen. For a row Ataniq supplies (every row that is not a row of
//     the host) the plan binds the Source the inputs name, and
//     [ResolveSources] refuses a Source whose real path differs, so the holder
//     gives real paths in the inputs, the plan binds them, and
//     [CheckBindSources] checks them as they are, lexically, with no link. For
//     a row of the host (/usr, /etc/hosts, /etc/localtime, ...), whose own name
//     may be a link of the host that no lead writes, the plan binds the real
//     path [ResolveSources] reported ([ResolvedSource].Real): the holder passes
//     [Resolution.HostSources] as plan.Options.HostSources (and has
//     plan.Options.HostPresent say which optional rows the resolution found),
//     which plan.Build requires and plan.Verify checks, and [CheckBindSources]
//     checks that real path. Each row is checked by [ResolvedSource].Bound, so
//     the holder can compare it with the source of the matching mount of the
//     plan. The probe's assertions from inside the sandbox stay the final
//     check.
//  4. Make the folders the lead writes, and know what is looked at. Every
//     lead has the records folder, [profile.Layout.QargiRecords],
//     <state>/leads/<role>/qargi, the requests folder and the mail folder, all
//     below the state root, so the lookup from the state root without links
//     covers every component of their paths. The holder makes them with mode
//     0700; [CheckOwnClone] checks their mode and owner with the tree, the
//     home and the config folder. The mailbox and the published folder, which
//     are bound read-only, are sources like any other: [ResolveSources] and
//     [CheckBindSources] look at their paths, and nothing here looks at their
//     mode, owner or content.
//  5. Use one state root for every lead on the host. Rule G1 holds across
//     leads only then: the rules about another lead's state know the state
//     root of their own inputs and no other, so a lead whose inputs name a
//     different state root could be given a source in a folder that another
//     lead writes, and every check here would accept it. Nothing in this
//     package or in the profile checks that the roots are the same; it is the
//     holder's to guarantee, with the sources and everything above them kept
//     Ataniq's.
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
// as un-listing it, and does not evaluate the patterns git knows. A
// .gitignore in the tree, tracked or not, which git ranks above that file,
// can un-ignore a placeholder (a line "!.envrc"), and git then shows the
// placeholder as untracked while [Check] reports it listed. That is cosmetic:
// nothing is disclosed and nothing is mounted differently. The package does
// not start a process, run git, create a namespace, or read any provider
// folder. It reads, bounded, from the clone: .git/config and the regular files
// of .git/info (when it fills the git metadata folder), and from the git
// metadata folder, the one file info/exclude. The copy of config and info is
// taken once, when the lead's first run has not happened: what the clone held
// then is what the lead gets, and the package cannot tell whether a lead wrote
// to the clone before the owner took it over.
//
// A tree placeholder whose path holds a character git treats as a pattern
// (backslash, star, question mark, opening bracket, a trailing space) is
// refused, because an anchored line would then not name only that path.
//
// The package is Linux only: on other systems it has no behaviour. Files are
// made with mode 0755 (folders) and 0644 (files), before the umask; the git
// metadata folder and what Prepare puts in it are made with 0700 and 0600.
package mountpoints
