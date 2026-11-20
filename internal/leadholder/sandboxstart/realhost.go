//go:build linux && ataniq_canary

package sandboxstart

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"golang.org/x/sys/unix"

	"ataniq/internal/leadsandbox/profile"
)

// Bounds on the files that are hashed.
const (
	maxExecutableBytes = 256 << 20
	maxSeccompBytes    = 64 << 10
	maxManagedBytes    = 1 << 20
)

// CheckHost implements System. It looks at the host for what the inputs name,
// before anything is made: the state root as a real folder, the records folder
// of the layout and the runtime folder of the role as 0700 folders of the
// caller with no link in their path, the git identity file in its folder and
// that the folder holds nothing else (it is bound whole), the three executables
// (regular, not group- or other-writable, hashed from the open descriptor), the
// seccomp file and its digest, and the digest of the managed-settings file. The
// folders of the lead are checked by the mountpoints package; nothing here
// reads a lead-writable folder.
func (*realSystem) CheckHost(in Inputs) ([]ExeRecord, error) {
	if err := realDir("state_root", in.StateRoot); err != nil {
		return nil, err
	}
	l := in.layout()
	// The records folder, which every lead has, lies below the state root. The
	// mountpoints package looks at it with the lead's other folders (the step
	// own-clone); this looks first and refuses a link anywhere in its path.
	if err := ownedPrivateDir("the records folder (sources.qargi_records_inside)", l.QargiRecords(), true); err != nil {
		return nil, err
	}
	if err := ownedPrivateDir("the runtime folder of the role (runtime_root)", RuntimeFolder(in.RuntimeRoot, in.LayoutRole), true); err != nil {
		return nil, err
	}
	// The git identity is bound as its folder, so the resolution of the sources
	// looks at the folder only: the file GIT_CONFIG_GLOBAL names must be in it,
	// as a regular file, and the folder is bound whole, so whatever else lies in
	// it would be visible inside too.
	if err := regularFile("sources.git_identity_dir", filepath.Join(in.Sources.GitIdentityDir, profile.GitIdentityFileName)); err != nil {
		return nil, err
	}
	if err := onlyEntry("sources.git_identity_dir", in.Sources.GitIdentityDir, profile.GitIdentityFileName); err != nil {
		return nil, err
	}
	var exes []ExeRecord
	for _, e := range []struct{ name, p string }{
		{"pasta", in.Executables.Pasta}, {"bwrap", in.Executables.Bwrap}, {"probe", in.Executables.Probe},
	} {
		sum, size, err := digestFile("executables."+e.name, e.p, maxExecutableBytes, true)
		if err != nil {
			return nil, err
		}
		exes = append(exes, ExeRecord{Name: e.name, Path: e.p, SHA256: sum, Size: size})
	}
	if s := in.Sources.Seccomp; s != nil {
		sum, _, err := digestFile("sources.seccomp.file", s.File, maxSeccompBytes, true)
		if err != nil {
			return nil, err
		}
		if sum != s.SHA256 {
			return nil, inputsErr("sources.seccomp.sha256", "the file's SHA-256 is %s, not the one given", sum)
		}
	}
	sum, _, err := digestFile("sources.managed_settings", in.Sources.ManagedSettings, maxManagedBytes, false)
	if err != nil {
		return nil, err
	}
	if sum != in.Sources.ManagedSettingsSHA256 {
		return nil, inputsErr("sources.managed_settings_sha256", "the file's SHA-256 is %s, not the one given", sum)
	}
	return exes, nil
}

// ownedPrivateDir checks a folder: not a symbolic link, owned by the caller,
// mode 0700 and, when real is set, equal to its own real path.
func ownedPrivateDir(field, p string, real bool) error {
	var st unix.Stat_t
	if err := unix.Lstat(p, &st); err != nil {
		return inputsErr(field, "cannot look at %s: %v (it must exist)", p, err)
	}
	switch {
	case st.Mode&unix.S_IFMT != unix.S_IFDIR:
		return inputsErr(field, "%s is not a folder (a symbolic link is refused)", p)
	case st.Uid != uint32(unix.Geteuid()):
		return inputsErr(field, "%s is not owned by the calling user", p)
	case st.Mode&0o7777 != 0o700:
		return inputsErr(field, "%s has mode %04o, must be 0700", p, st.Mode&0o7777)
	}
	if real {
		return realDir(field, p)
	}
	return nil
}

// regularFile checks that a path names a regular file, without following a
// final symbolic link.
func regularFile(field, p string) error {
	var st unix.Stat_t
	if err := unix.Lstat(p, &st); err != nil {
		return inputsErr(field, "cannot look at the file %s: %v (it must exist)", p, err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		return inputsErr(field, "%s is not a regular file (a symbolic link is refused)", p)
	}
	return nil
}

// realDir checks that a path names a folder and has no symbolic link in it.
func realDir(field, p string) error {
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		return inputsErr(field, "cannot resolve %s: %v", p, err)
	}
	if r != p {
		return inputsErr(field, "%s is not a real path (it resolves to %s): give the real path", p, r)
	}
	var st unix.Stat_t
	if err := unix.Stat(p, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFDIR {
		return inputsErr(field, "%s is not a folder", p)
	}
	return nil
}

// digestFile opens the regular file at p without following a final link, takes
// its SHA-256 from the open descriptor, and refuses a file above max bytes. A
// file that group or others can write is refused when strict is set.
func digestFile(field, p string, max int64, strict bool) (string, int64, error) {
	fd, err := unix.Open(p, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC|unix.O_NOCTTY, 0)
	if err != nil {
		return "", 0, inputsErr(field, "cannot open %s: %v", p, err)
	}
	f := os.NewFile(uintptr(fd), p)
	defer f.Close()
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return "", 0, inputsErr(field, "cannot stat %s: %v", p, err)
	}
	switch {
	case st.Mode&unix.S_IFMT != unix.S_IFREG:
		return "", 0, inputsErr(field, "%s is not a regular file", p)
	case strict && st.Mode&0o022 != 0:
		return "", 0, inputsErr(field, "%s is writable by its group or by others (mode %04o)", p, st.Mode&0o7777)
	case st.Size > max:
		return "", 0, inputsErr(field, "%s is larger than %d bytes", p, max)
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, max+1))
	if err != nil {
		return "", 0, inputsErr(field, "cannot read %s: %v", p, err)
	}
	if n > max {
		return "", 0, inputsErr(field, "%s grew past %d bytes while it was read", p, max)
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// maxFolderEntries bounds how many names of a folder onlyEntry reads: a folder
// that must hold one entry is refused long before.
const maxFolderEntries = 16

// onlyEntry checks that the folder at dir holds the entry name and nothing
// else, hidden entries included. The folder is opened without following a
// final link and its names are read from that descriptor.
func onlyEntry(field, dir, name string) error {
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return inputsErr(field, "cannot open the folder %s: %v (a symbolic link is refused)", dir, err)
	}
	f := os.NewFile(uintptr(fd), dir)
	defer f.Close()
	names, err := f.Readdirnames(maxFolderEntries)
	if err != nil && err != io.EOF {
		return inputsErr(field, "cannot list the folder %s: %v", dir, err)
	}
	found := false
	for _, n := range names {
		if n == name {
			found = true
			continue
		}
		return inputsErr(field, "the folder %s holds %s besides %s: the folder is bound whole, so it must hold the file %s and nothing else", dir, strconv.Quote(clipText(n)), name, name)
	}
	if !found {
		return inputsErr(field, "the folder %s does not hold the file %s", dir, name)
	}
	return nil
}
