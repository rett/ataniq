//go:build linux && ataniq_canary

// Package provision makes the folders of the one canary lead, before its first
// start. It is the owner's step P0 of the canary slice: the daemon starts a
// lead whose folders already exist, and nothing in the daemon or the holder
// makes them. Like everything of canary mode it exists only in a build with the
// ataniq_canary tag, and every file of the package, the tests included, starts
// with "//go:build linux && ataniq_canary".
//
// # What Provision does
//
// [Provision] takes the canary config (leadcanary.ReadConfig) and, in this
// order:
//
//  1. refuses the default roots: canaryroot.CheckRoots on the state and runtime
//     root, before anything is looked at. WriteMarker does not check where a
//     root is, so this is the check that keeps the real lead's folders out of
//     reach;
//  2. reads and checks the files it copies, without writing: the managed
//     settings file and the guard hook's executable must each be a regular
//     file of the caller that group and others cannot write (L7), and the
//     replay screens (1.json, 2.json, 1.txt, 2.txt, and report.txt if there)
//     regular files of at most [MaxReplayFileBytes]. Every folder that holds
//     one is opened without following a symbolic link in any component;
//  3. opens the mirror as the exchange mirror it must be (internal/mirror.Open)
//     and looks for the upstream branch. The mirror must be the layout's
//     (<state root>/mirrors/<project>.git, which "ataniq-intake init" makes)
//     and must already hold the branch: provisioning does not put anything in
//     it;
//  4. refuses a state root that carries the marker, and a lead that has
//     started: the holder's state folder must be empty (a first start leaves
//     holder.json, final.json or lead-has-run there), and so must the holder's
//     runtime folder;
//  5. looks at every path it will make: a symbolic link at any of them, or a
//     file or a non-empty tree where it would make one, refuses the run.
//
// Nothing has been written until all of that has passed. Then it makes, with
// mode 0700 and by handle (every lookup is beneath a handle opened once, with
// no symbolic link followed):
//
//   - the layout's folders: leads, the lead's folder, holder (the holder's
//     state folder, left empty), home, config and the runtime's config
//     folder, tree, qargi (the records), requests, mail, mailbox and the
//     lead's mailbox, pub and the project's published folder, and mirrors;
//   - the sources of the bind table the layout does not name, in the lead's
//     folder (see [Paths]): skills and instructions.md (empty), git-identity/config
//     (the name and address of the config), qargi-install/guard-hook (the
//     hook's executable, mode 0500), assignment and projection (empty) and
//     managed-settings.json (the copy of the managed settings, mode 0400);
//   - the tree: the mirror's branch cloned with /usr/bin/git ([GitPath]) into
//     the empty tree folder, by the folder's handle, with an explicit minimal
//     environment ([GitEnvironment]), copying the objects and taking no
//     template, so the tree has no hook. The remote is "origin", and its URL
//     is where the sandbox sees the mirror (profile.MirrorInside), not a host
//     path. The configuration is checked against an allow-list afterwards: a
//     credential, a credential helper, a URL rewrite, an include or a second
//     remote refuses the run;
//   - the mount points in the home, the config folder and the tree, and the
//     git metadata copy (<lead>/gitmeta), by mountpoints.Prepare; then the
//     checks of every start (mountpoints.Check, mountpoints.CheckOwnClone) run
//     on the result;
//   - <home>/canary-replay/ with the replay screens copied in, never the
//     switch file;
//   - the runtime root (if it is not there) and the holder's runtime folder
//     <runtime root>/leads/<layout role>, mode 0700, which must exist before
//     the holder starts;
//   - the marker (canaryroot.WriteMarker), the last act. A failed run leaves
//     none. It also leaves what it made: remove both roots, and run again.
//
// The spec folder is not made: the start sequence makes its own "bound/spec"
// in the runtime folder at each start and refuses leftovers.
//
// The state root must exist (it holds the mirror), with mode 0700; the
// runtime root's parent must exist and the runtime root is made if absent.
// Neither may have a symbolic link in its path.
//
// # After a first start
//
// [Provision] refuses a root that has started or that carries the marker.
// [ReplayOn] is the one operation allowed then: it writes the replay switch
// file and nothing else.
//
// # The sandbox inputs file
//
// Provisioning does not write the sandbox inputs file; the run kit does.
// [PathsOf] gives the paths it must name for the sources, and [CheckInputs]
// checks a file against the config.
//
// # What it prints
//
// One line for each folder or file made or kept, the clone, and the marker, to
// [Options.Log]. It never prints the content of a settings, hook or
// credential file.
package provision
