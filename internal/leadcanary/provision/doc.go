//go:build linux && ataniq_canary

// Package provision makes the folders of the one canary lead (and of a scratch
// lead, see below), before its first
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
//     file where it would make one, refuses the run. The git metadata copy
//     and the temporary name it is built under are among them, and nothing
//     may be at either. The replay switch file, which only [ReplayOn] writes,
//     refuses the run too, and so does a folder that is there already and
//     holds something ([ErrNotEmpty]): a folder is kept only when it is
//     empty, the tree included. The exceptions are the folders that hold
//     other things by design: leads, the lead's own folder, mailbox, pub,
//     roles and mirrors.
//
// Nothing has been written until all of that has passed. Then it makes, with
// mode 0700 and by handle (every lookup is beneath a handle opened once, with
// no symbolic link followed):
//
//   - the layout's folders: leads, the lead's folder, holder (the holder's
//     state folder, left empty), home, config and the runtime's config
//     folder, tree, qargi (the records), requests, mail, mailbox and the
//     lead's mailbox, pub and the project's published folder, roles and the
//     project's folder of the role lock (<state root>/roles/<project>, left
//     empty: the holder makes the lock file in it at its first start, with an
//     open that makes the file and not the folder), and mirrors;
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
//     none. It also leaves what it made, and a second run refuses that: remove
//     <state root>/leads/<layout role>, the lead's mailbox
//     (<state root>/mailbox/<layout role>), its published folder
//     (<state root>/pub/<project>), the folder of its role lock
//     (<state root>/roles/<project>) and the runtime root, and run again. Do not
//     remove the state root: it holds the mirror, which provisioning needs
//     and does not make, so the mirror stays.
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
// [Provision] does not write the sandbox inputs file; the run kit does.
// [PathsOf] gives the paths it must name for the sources, and [CheckInputs]
// checks a file against the config. [ProvisionScratch] does write it
// ([WriteInputs]), from the scratch config's sandbox values.
//
// # Scratch leads
//
// [ProvisionScratch] is the daemon's define for a lead of a scratch place
// (leadcanary.ScratchConfig): the same folders, clone, files, mount points and
// checks, with these differences. The root check is canaryroot.Check with the
// scratch root marker, which only the owner's command writes, and the lead has
// no marker in the state root: its last act is the record
// <state root>/scratch/<layout role>/provisioned.json, which holds the profile
// digest. The mirror is made here from the lead's source when it is not there
// (mirror.Mirror.ImportUpstream), and kept when a later step fails. The sandbox
// inputs file is made here, the digest is computed with the holder's own code
// (sandboxstart.ProfileDigest) before anything is written, and a profile_digest
// pin in the scratch config must equal it ([ErrDigestPin]).
//
// A define that is cut short leaves its folders and no record. [ScratchStateOf]
// says "incomplete" for it, [ProvisionScratch] refuses it with [ErrIncomplete]
// and changes nothing, and the owner's command "ataniq-canary scratch-clean"
// ([CleanScratch]) removes what that lead left, and only that, after which the
// next define starts again. No define deletes anything. A lead that has
// started, or whose record is there, is never cleaned.
//
// # Lead replies
//
// Provisioning itself leaves the instruction file empty and makes no reply
// folder. [EnsureChat], which the daemon calls at every define and every
// start of a scratch lead, makes the reply folder
// <state root>/leads/<layout role>/home/.ataniq-replies ([OutboxInside] in the
// sandbox, in the lead's home, which is bound already) with mode 0700, and
// writes the text of [LeadInstructions] in place of the empty instruction
// file, by a temporary file that is synced and renamed, only while the lead
// is proven stopped: a running lead's sandbox binds the file it started with.
// A file that holds anything else is the owner's and is never overwritten. A
// reply folder that is not a folder of the caller with mode 0700 exactly is
// refused and left as it is ([ErrChat]). No environment variable and no bind
// changes, so the profile digest is the same.
//
// # What it prints
//
// One line for each folder or file made or kept, the clone, and the marker, to
// [Options.Log]. It never prints the content of a settings, hook or
// credential file.
package provision
