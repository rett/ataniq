//go:build linux && amd64

package harness

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"ataniq/internal/leadsandbox/profile"
)

// Bounds on the files the harness hashes.
const (
	maxExecutableBytes = 256 << 20
	maxSeccompBytes    = 64 << 10
	maxManagedBytes    = 1 << 20
)

// ExeRecord is one executable of the plan with the SHA-256 the harness took.
type ExeRecord struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// hostFacts are what the host checks of the configuration found.
type hostFacts struct {
	Executables []ExeRecord
}

// checkHost looks at the host for what the configuration names, before
// anything is created: the campaign folder (a 0700 folder of the caller), the
// state root and the campaign as real paths, the records folder, the git
// identity file in its folder and that the folder holds nothing else, the
// three executables (regular, not group- or
// other-writable, hashed from the open descriptor), the seccomp file and its
// digest, and the digest of the managed-settings file. The state folders of
// the lead are checked by the mountpoints package; nothing here reads a
// lead-writable folder.
func checkHost(c *Config) (*hostFacts, error) {
	if err := ownedPrivateDir("campaign", c.Campaign, true); err != nil {
		return nil, err
	}
	if err := realDir("state root", c.State.Root); err != nil {
		return nil, err
	}
	// The records folder, which every lead has, lies below the state root. The
	// mountpoints package looks at it with the lead's other folders (the
	// own-clone step); the harness looks first, before anything is created, and
	// refuses a link anywhere in its path.
	if err := ownedPrivateDir("records folder (sources.qargi_records_inside)", c.layout().QargiRecords(), true); err != nil {
		return nil, err
	}
	// The git identity is bound as its folder, so the resolution of the
	// sources looks at the folder only: the file GIT_CONFIG_GLOBAL names must
	// be in it, as a regular file. The folder is bound whole, so whatever else
	// lies in it would be visible inside too: it must hold that file and
	// nothing else.
	if err := regularFile("sources.git_identity_dir", filepath.Join(c.Sources.GitIdentityDir, profile.GitIdentityFileName)); err != nil {
		return nil, err
	}
	if err := onlyEntry("sources.git_identity_dir", c.Sources.GitIdentityDir, profile.GitIdentityFileName); err != nil {
		return nil, err
	}
	var f hostFacts
	for _, e := range []struct{ name, p string }{
		{"pasta", c.Executables.Pasta}, {"bwrap", c.Executables.Bwrap}, {"probe", c.Executables.Probe},
	} {
		sum, size, err := digestFile("executables."+e.name, e.p, maxExecutableBytes, true)
		if err != nil {
			return nil, err
		}
		f.Executables = append(f.Executables, ExeRecord{Name: e.name, Path: e.p, SHA256: sum, Size: size})
	}
	if s := c.Sources.Seccomp; s != nil {
		sum, _, err := digestFile("sources.seccomp.file", s.File, maxSeccompBytes, true)
		if err != nil {
			return nil, err
		}
		if sum != s.SHA256 {
			return nil, configErr("sources.seccomp.sha256", "the file's SHA-256 is %s, not the one given", sum)
		}
	}
	sum, _, err := digestFile("sources.managed_settings", c.Sources.ManagedSettings, maxManagedBytes, false)
	if err != nil {
		return nil, err
	}
	if sum != c.Sources.ManagedSettingsSHA256 {
		return nil, configErr("sources.managed_settings_sha256", "the file's SHA-256 is %s, not the one given", sum)
	}
	return &f, nil
}

// ownedPrivateDir checks a folder: not a symbolic link, owned by the caller,
// mode 0700 and, when real is set, equal to its own real path.
func ownedPrivateDir(field, p string, real bool) error {
	var st unix.Stat_t
	if err := unix.Lstat(p, &st); err != nil {
		return configErr(field, "cannot look at %s: %v (it must exist)", p, err)
	}
	switch {
	case st.Mode&unix.S_IFMT != unix.S_IFDIR:
		return configErr(field, "%s is not a folder (a symbolic link is refused)", p)
	case st.Uid != uint32(unix.Geteuid()):
		return configErr(field, "%s is not owned by the calling user", p)
	case st.Mode&0o7777 != 0o700:
		return configErr(field, "%s has mode %04o, must be 0700", p, st.Mode&0o7777)
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
		return configErr(field, "cannot look at the file %s: %v (it must exist)", p, err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		return configErr(field, "%s is not a regular file (a symbolic link is refused)", p)
	}
	return nil
}

// realDir checks that a path names a folder and has no symbolic link in it.
func realDir(field, p string) error {
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		return configErr(field, "cannot resolve %s: %v", p, err)
	}
	if r != p {
		return configErr(field, "%s is not a real path (it resolves to %s): give the real path", p, r)
	}
	var st unix.Stat_t
	if err := unix.Stat(p, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFDIR {
		return configErr(field, "%s is not a folder", p)
	}
	return nil
}

// digestFile opens the regular file at p without following a final link, takes
// its SHA-256 from the open descriptor, and refuses a file above max bytes. A
// file that group or others can write is refused when strict is set.
func digestFile(field, p string, max int64, strict bool) (string, int64, error) {
	fd, err := unix.Open(p, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC|unix.O_NOCTTY, 0)
	if err != nil {
		return "", 0, configErr(field, "cannot open %s: %v", p, err)
	}
	f := os.NewFile(uintptr(fd), p)
	defer f.Close()
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return "", 0, configErr(field, "cannot stat %s: %v", p, err)
	}
	switch {
	case st.Mode&unix.S_IFMT != unix.S_IFREG:
		return "", 0, configErr(field, "%s is not a regular file", p)
	case strict && st.Mode&0o022 != 0:
		return "", 0, configErr(field, "%s is writable by its group or by others (mode %04o)", p, st.Mode&0o7777)
	case st.Size > max:
		return "", 0, configErr(field, "%s is larger than %d bytes", p, max)
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, max+1))
	if err != nil {
		return "", 0, configErr(field, "cannot read %s: %v", p, err)
	}
	if n > max {
		return "", 0, configErr(field, "%s grew past %d bytes while it was read", p, max)
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// maxFolderEntries bounds how many names of a folder onlyEntry reads: a folder
// that must hold one entry is refused long before.
const maxFolderEntries = 16

// onlyEntry checks that the folder at dir holds the entry name and nothing
// else, hidden entries included. The folder is opened without following a
// final link and its names are read from that descriptor; at most a few names
// are read, and the reason names at most one of the others.
func onlyEntry(field, dir, name string) error {
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return configErr(field, "cannot open the folder %s: %v (a symbolic link is refused)", dir, err)
	}
	f := os.NewFile(uintptr(fd), dir)
	defer f.Close()
	names, err := f.Readdirnames(maxFolderEntries)
	if err != nil && err != io.EOF {
		return configErr(field, "cannot list the folder %s: %v", dir, err)
	}
	found := false
	for _, n := range names {
		if n == name {
			found = true
			continue
		}
		return configErr(field, "the folder %s holds %s besides %s: the folder is bound whole, so it must hold the file %s and nothing else", dir, strconv.Quote(clip(n)), name, name)
	}
	if !found {
		return configErr(field, "the folder %s does not hold the file %s", dir, name)
	}
	return nil
}

// MinBwrapBindFD is the oldest bubblewrap release the harness launches a plan
// with: the first one taken to have --bind-fd and --ro-bind-fd, which the plan
// uses for every bind source Ataniq supplies.
//
// It is an assumption, to be confirmed at qualification [qualify]: the two
// options are taken to have come with bubblewrap 0.10.0, and that could not be
// confirmed offline when this was written. The holder's start sequence
// (internal/leadholder/sandboxstart) declares the same assumption in its own
// MinBwrapBindFD, so this constant is not the one place to change: if the
// release is another one, both copies change together, and
// TestMinBwrapBindFDMatchesTheHolder fails while they differ. A bubblewrap
// that is older than this and does have the options is refused wrongly; one
// that is at least this and lacks them is launched, and is expected to refuse
// the plan's arguments as unknown options and to start nothing (not
// observed). The plan has no form that binds such a source by path, so no
// weaker plan is started in either case.
const MinBwrapBindFD = "0.10.0"

// parseBwrapVersion reads the release out of the first line bubblewrap prints
// for --version, "bubblewrap MAJOR.MINOR.PATCH": the three numbers, and false
// when the line is not of that form (a patch number may be missing and counts
// as zero; anything after the numbers that is not a digit or a dot ends them).
func parseBwrapVersion(line string) (v [3]int, ok bool) {
	rest, found := strings.CutPrefix(line, "bubblewrap ")
	if !found {
		return v, false
	}
	end := 0
	for end < len(rest) && (rest[end] == '.' || rest[end] >= '0' && rest[end] <= '9') {
		end++
	}
	parts := strings.Split(rest[:end], ".")
	if len(parts) < 2 || len(parts) > 3 {
		return v, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || len(p) > 6 {
			return v, false
		}
		v[i] = n
	}
	return v, true
}

// checkBwrapBindFD decides from the version line of bubblewrap whether the
// plan can be launched with it. A release older than [MinBwrapBindFD] is
// refused with an error. A line that does not give a release decides nothing:
// the note says so, and the launch goes on, where a bubblewrap without the
// options is expected to refuse the plan's arguments itself.
func checkBwrapBindFD(line string) (note string, err error) {
	min, _ := parseBwrapVersion("bubblewrap " + MinBwrapBindFD)
	got, ok := parseBwrapVersion(line)
	if !ok {
		return fmt.Sprintf("the release of bubblewrap could not be read from its version line, so it is not known whether it has --bind-fd and --ro-bind-fd (release %s is assumed to be the first that has them): a bubblewrap without them is expected to refuse the plan's arguments and to start nothing, which has not been observed", MinBwrapBindFD), nil
	}
	for i := range got {
		if got[i] != min[i] {
			if got[i] < min[i] {
				return "", fmt.Errorf("bubblewrap %d.%d.%d is older than %s, the first release assumed to have --bind-fd and --ro-bind-fd: the plan binds every source Ataniq supplies from a descriptor and has no form that binds one by path, so nothing is launched", got[0], got[1], got[2], MinBwrapBindFD)
			}
			break
		}
	}
	return "", nil
}
