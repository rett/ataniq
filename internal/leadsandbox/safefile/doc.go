// Package safefile is the owner-side way to touch a folder that a lead can
// write: every access starts from a folder handle ([Root]) the caller opened
// once on a path it owns and trusts, and goes through openat2 with
// RESOLVE_BENEATH, RESOLVE_NO_SYMLINKS and RESOLVE_NO_MAGICLINKS. Nothing
// here opens, stats or creates anything by path after the handle exists, and
// nothing follows a link a lead planted: a link is refused with [ErrSymlink]
// even when it points back inside the folder or nowhere. A link
// in a parent component is refused by every method. A link as the last
// component is refused by [Root.ReadFile], [Root.ReadFileInfo],
// [Root.AppendFile], [Root.ReadDirNames] and [Root.MkdirAll]; by
// [Root.WriteFileExclusive], which refuses to create anything there and
// reports [ErrSymlink] (the error also matches fs.ErrExist); and it is
// reported, not refused, by [Root.Lstat], so a caller can see it. This is the
// symlink-safe access of section 3.6 of the lead-hosting design, the code
// behind acceptance case S17.
//
// # What it offers
//
// Reads: [Root.ReadFile] and [Root.ReadFileInfo] read a regular file up to a
// cap, [Root.ReadDirNames] lists a folder up to a count, and [Root.Lstat]
// reports the type and mode of an entry without following it. Writes, for
// the mount-point work that runs before a lead's first run: [Root.MkdirAll],
// [Root.WriteFileExclusive] (create only, never replace) and
// [Root.AppendFile] (an existing regular file). There is deliberately no
// rename, no remove and no truncate.
//
// # Rules every call follows
//
// A path is relative to the handle and clean: an absolute path or a ".."
// component is refused with [ErrEscapes], and an empty or unclean name
// ("a//b", "./a", "a/", a NUL byte) with [ErrInvalidArgument], both before
// any system call. A file is checked on the open descriptor with fstat, not
// on its name: a FIFO, device, socket or folder is refused with
// [ErrNotRegular] (the open uses O_NONBLOCK, so a FIFO cannot block it). A
// size over the cap is refused before reading, and the read itself is capped
// so a file that grows while it is read is refused with [ErrTooLarge] and not
// truncated silently. Every refusal names its reason and wraps one of the
// package's sentinel errors, so a caller tests it with errors.Is.
//
// # Hard links
//
// A regular file whose link count is above one is reported ([Info.Nlink],
// [FileResult.HardLinked]), and [RefuseHardLinks] makes [Root.ReadFile]
// refuse it with [ErrHardLink]. The reason: a lead can hard-link a file it
// can read into a folder it writes, where it then looks like the lead's own
// file; it cannot link one it cannot read. With the kernel's protected
// hard-link setting off, or a file the lead's user may read and write, that
// puts a file the owner never meant to expose behind a name the lead
// chose. An owner-side read of such a file would disclose it, and an owner-side
// append would change it, so [Root.AppendFile] always refuses a hard-linked
// file.
//
// # When openat2 is not usable
//
// If the kernel lacks openat2 (ENOSYS), or a seccomp filter denies it (the
// call fails with EPERM, which cannot be told apart from a permission
// failure, so it is not guessed at), the call is refused with
// [ErrUnsupported]. There is no fallback to a path-based open.
//
// # What it does not do
//
// [Root.MkdirAll] walks one folder at a time, so each step is beneath the
// folder before it and not beneath the handle. It checks at the end that the
// path, opened again from the handle, is the folder the walk ended on, and
// refuses with [ErrEscapes] when a folder on the path moved while the call
// ran. Folders it made after such a move stay where the moved folder went,
// outside the handle, in a place the lead could already write; it cannot
// undo that.
//
// It does not protect against the folder behind the handle being changed by
// someone with owner rights, and it trusts the path given to [OpenRoot]: only
// the last component is checked (O_NOFOLLOW), so the caller must pass a path
// whose parents it owns. It does not sandbox anything, create a namespace or
// run a process. It is Linux only: on other systems the package has no
// behaviour. It is part of an unqualified proposal; it is generated and
// tested against the real system calls on a temporary folder, and no claim
// is made that the lead sandbox is safe or supported.
package safefile
