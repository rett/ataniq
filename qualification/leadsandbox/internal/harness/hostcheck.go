//go:build linux && amd64

package harness

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
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
// state root and the campaign as real paths, the optional records folder, the
// three executables (regular, not group- or other-writable, hashed from the
// open descriptor), the seccomp file and its digest, and the digest of the
// managed-settings file. The state folders of the lead are checked by the
// mountpoints package; nothing here reads a lead-writable folder.
func checkHost(c *Config) (*hostFacts, error) {
	if err := ownedPrivateDir("campaign", c.Campaign, true); err != nil {
		return nil, err
	}
	if err := realDir("state root", c.State.Root); err != nil {
		return nil, err
	}
	if c.Sources.QargiRecordsInside != "" {
		// The records folder lies below the state root, and the mountpoints
		// package looks every folder of the lead up from the state root
		// without following a link; its mode and owner are not checked there,
		// so the harness does, and also refuses a link anywhere in its path.
		if err := ownedPrivateDir("records folder (sources.qargi_records_inside)", c.layout().QargiRecords(), true); err != nil {
			return nil, err
		}
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
