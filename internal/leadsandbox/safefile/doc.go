// Package safefile is the owner-side way to touch a folder that a lead can
// write: every access starts from a folder handle ([Root]) the caller opened
// once on a path it owns and trusts, or opened beneath such a handle
// ([Root.SubRoot]), and goes through openat2 with RESOLVE_BENEATH,
// RESOLVE_NO_SYMLINKS and RESOLVE_NO_MAGICLINKS. Nothing
// here opens, stats or creates anything by path after the handle exists, and
// nothing follows a link a lead planted: a link is refused with [ErrSymlink]
// even when it points back inside the folder or nowhere. A link
// in a parent component is refused by every method. A link as the last
// component is refused by [Root.ReadFile], [Root.ReadFileInfo],
// [Root.AppendFile], [Root.ReadDirNames], [Root.MkdirAll], [Root.SubRoot],
// [Root.OpenDirHandle] and [Root.OpenFileHandle]; by
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
// [Root.AppendFile] (an existing regular file). Two more make what was written
// durable and put it in its place: [Root.Sync] (fsync of a regular file or
// folder, "." for the handle's folder) and [Root.RenameNoReplace], the only
// rename: one single name to another in the handle's own folder, with
// RENAME_NOREPLACE, so it never replaces and looks nothing up. There is
// deliberately no replacing rename, no remove and no truncate.
//
// # A folder opened beneath the handle
//
// [Root.SubRoot] opens a folder below the handle as a new [Root], in one
// lookup from the handle, and checks on the descriptor it got that it is a
// folder. It is for a caller that looks at a folder and then works in it. A
// [Root.Lstat] followed by [OpenRoot] on the folder's name is two lookups,
// and a lead that can write the parent can change what the name holds
// between them, so what was checked is not provably what is opened. With a
// sub-root the folder is looked up once and every later call is beneath that
// descriptor: renaming the folder, or putting a link at its name, changes
// nothing for the sub-root. Each call of SubRoot is a fresh walk from the
// handle, and nothing an earlier call found is reused. A sub-root has its own
// descriptor and is closed on its own; every method works on it as on a Root
// from OpenRoot, beneath its own folder.
//
// # An entry opened to hand on
//
// [Root.OpenDirHandle] and [Root.OpenFileHandle] open one entry below a Root
// with O_PATH, in one lookup, check on the descriptor that it is a folder or
// a regular file, and return an entry handle ([Handle]) that keeps the
// descriptor. It is for a caller that passes the entry to something else (a
// bind source given to a child process): what it checked is the descriptor
// it passes, so the object checked is the object used, whatever a lead does
// to the name in between. [Handle.Info] is fstat on the descriptor at the
// time of the call, and [Handle.Fd] is the descriptor's number. "." is
// accepted for a folder, so a Root is handed on as it is.
//
// The descriptor is close-on-exec, like every descriptor the package opens:
// no child process gets it by inheritance, and passing it to one is the
// caller's explicit act. The Handle keeps owning it until [Handle.Close]. An
// entry handle on a file refuses neither a size nor a hard-linked file, as it
// reads nothing; Info reports both for the caller to judge.
//
// [Handle.Dup] gives the caller a descriptor of its own on the same object: a
// duplicate, close-on-exec too, that the caller closes or places at the
// number a child process is to inherit. The number Fd returns stays the
// Handle's and ends with its Close; the duplicate does not, so a caller that
// hands the object on takes the duplicate and closes the Handle.
//
// # A folder opened without following a link above it
//
// [OpenRoot] protects the last component of its path only. [OpenRootNoLinks]
// is for a path that is expected to be a real path and is not the caller's own
// choice (the folder a bind source is walked from): it looks the whole path up
// from the root of the file system in one openat2 that follows no symbolic
// link on any component, and the handle is the descriptor that lookup
// returned. Mount points above the folder are crossed, as the folder may be on
// any mount; below the handle the rules are the same as for any Root.
//
// # Rules every call follows
//
// A path is relative to the handle and clean: an absolute path or a ".."
// component is refused with [ErrEscapes], and an empty or unclean name
// ("a//b", "./a", "a/", a NUL byte) with [ErrInvalidArgument], both before
// any system call. A file is checked on the open descriptor with fstat, not
// on its name: a FIFO, device, socket or folder is refused with
// [ErrNotRegular] (the open uses O_NONBLOCK, so a FIFO cannot block it; see
// "Devices, leases and mounts" below for what the open itself can do). A
// size over the cap is refused before reading, and the read itself is capped
// so a file that grows while it is read is refused with [ErrTooLarge] and not
// truncated silently. Every refusal names its reason and wraps one of the
// package's sentinel errors, so a caller tests it with errors.Is.
//
// # Hard links
//
// A regular file whose link count is above one is reported ([Info.Nlink],
// [Info.HardLinked], reached from [FileResult.Info]), and [RefuseHardLinks]
// makes [Root.ReadFile] refuse it with [ErrHardLink]. The reason: a lead can hard-link a file it
// can read into a folder it writes, where it then looks like the lead's own
// file; it cannot link one it cannot read. With the kernel's protected
// hard-link setting off, or a file the lead's user may read and write, that
// puts a file the owner never meant to expose behind a name the lead
// chose. An owner-side read of such a file would disclose it, and an owner-side
// append would change it, so [Root.AppendFile] always refuses a hard-linked
// file.
//
// # Devices, leases and mounts
//
// The type of a file is known only from fstat on its descriptor, so a device
// node is opened before it can be refused: the order is open, then fstat,
// then [ErrNotRegular]. The open of a device runs the driver's code and can
// have side effects, and it can fail with a raw errno (EACCES, ENXIO, EPERM)
// before fstat runs, in which case the error is that errno and not
// [ErrNotRegular]. A lead cannot create a real device node without
// CAP_MKNOD, but it can create the 0:0 whiteout character device, whose open
// fails this way. Keep the owner's state folders on a file system mounted
// nodev, so that no device node there is ever opened; the package does not
// check it. [Root.SubRoot], [Root.OpenDirHandle] and [Root.OpenFileHandle]
// open with O_PATH, as [Root.Lstat] does: that does not open the file itself,
// so the open of a device node never runs. What refuses the node differs.
// [Root.OpenFileHandle] gets a descriptor on it, and fstat on that descriptor
// refuses it with [ErrNotRegular]. The two folder methods, SubRoot and
// OpenDirHandle, open with O_DIRECTORY as well, so the kernel refuses the node
// in the lookup (ENOTDIR, reported as [ErrNotDirectory]) before there is a
// descriptor for fstat to look at; the fstat check of the folder methods is
// there so that the type does not rest on the open alone. [OpenRootNoLinks]
// opens with O_PATH and O_DIRECTORY in the same way.
//
// Opens use O_NONBLOCK. Besides keeping a FIFO from blocking them, it keeps a
// file lease from stalling the owner: a process with the lead's user (the
// owner's, under the design) can take a lease on a file it owns, and a
// blocking open would then wait the lease-break time (tens of seconds). With
// O_NONBLOCK the open of a leased file fails at once with EAGAIN, as a raw
// errno and not a sentinel.
//
// Every lookup carries RESOLVE_NO_XDEV: a mount point below the handle's
// folder (a mount, including a bind mount, on any path component) is refused
// with [ErrEscapes] wrapping EXDEV, and is never read across. The handle's own
// folder may itself be a mount root.
//
// [Root.SubRoot], [Root.OpenDirHandle] and [Root.OpenFileHandle] keep this
// rule and relax nothing: an entry that is a mount point, or lies beyond one,
// is not opened as a sub-root or an entry handle, so both are always on the
// mount of the Root they came from. What is mounted on a path below the handle
// is another file system's folder and not an entry of the folder the caller
// opened; walking into it would make "beneath the handle" mean less than it
// says. A caller that means a mount's root opens it by name with [OpenRoot],
// on a path whose parents no lead can change; it is then a Root like any
// other, and "." hands it on.
//
// # When openat2 is not usable
//
// [OpenRoot] (and [OpenRootNoLinks], whose own lookup is an openat2 too)
// probes openat2 once, on the new handle and with the flags every
// call uses, and fails with [ErrUnsupported] when it does not work (the kernel
// lacks it, a seccomp filter denies it with EPERM, which cannot be told apart
// from a permission failure and is not guessed at, or the flags are refused),
// so a start learns it before it has done other work. A call that later gets
// ENOSYS or EPERM from openat2 is refused with [ErrUnsupported] too, a call of
// [Root.SubRoot], [Root.OpenDirHandle] or [Root.OpenFileHandle] and a call on
// a sub-root included: a sub-root is not probed again, it calls openat2 as the
// Root it came from does. There is no fallback to a path-based open. The
// package's tests need a working openat2 and fail, not skip, where there is
// none.
//
// # What it does not do
//
// [Root.MkdirAll] walks one folder at a time, so each step is beneath the
// folder before it and not beneath the handle. It checks at the end that the
// path, opened again from the handle, is the folder the walk ended on, and
// refuses with [ErrEscapes] when a folder on the path moved while the call
// ran. Folders it made after such a move stay where the moved folder went,
// outside the handle, in a place the lead could already write; it cannot
// undo that. A nil result holds only at the moment of that end check: a
// folder moved afterwards is not seen, so a caller that uses the path later
// (a bind by path) checks it again right before it does.
//
// It does not protect against the folder behind the handle being changed by
// someone with owner rights, and it trusts the path given to [OpenRoot]: only
// the last component is checked (O_NOFOLLOW), so the caller must pass a path
// whose parents it owns. [OpenRootNoLinks] refuses a link on every component,
// and that is all it adds: whoever can write a folder above can still rename
// what the path names, so the parents must be the caller's there too. It does
// not sandbox anything, create a namespace or
// run a process. It is Linux only: on other systems the package has no
// behaviour. It is part of an unqualified proposal; it is generated and
// tested against the real system calls on a temporary folder, and no claim
// is made that the lead sandbox is safe or supported.
package safefile
