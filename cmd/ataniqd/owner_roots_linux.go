//go:build linux && ataniq_ownerchannel

package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"ataniq/internal/approval/hostadapter"
	"ataniq/internal/leaddaemon"
)

// ownerBanner is the line the daemon prints at start when it serves the owner
// channel: the planner's door here is the stand-in, and a decision on it is no
// real approval.
const ownerBanner = "owner channel: STAND-IN door, scratch state: no real approval"

// ownerGetenv reads the environment for the builder, which is not given the
// app's. A test replaces it.
var ownerGetenv = os.Getenv

// ownerAccountHome returns the home folder of the account of the user id, as
// the password database has it. A test replaces it.
var ownerAccountHome = func(uid int) (string, error) {
	u, err := user.LookupId(strconv.Itoa(uid))
	if err != nil {
		return "", err
	}
	return u.HomeDir, nil
}

// defaultFolders returns every default state and runtime folder of the
// account: the environment's, as package leaddaemon works them out, and the
// account's own, whatever the environment says. This is the rule of package
// canaryroot, which the build with this tag cannot import (it is behind the
// canary tag).
func defaultFolders(getenv func(string) string) ([]string, error) {
	var out []string
	if d, err := leaddaemon.DefaultStateDir(getenv); err == nil {
		out = append(out, d)
	}
	if d, err := leaddaemon.DefaultRuntimeDir(getenv); err == nil {
		out = append(out, d)
	}
	if h := getenv("HOME"); filepath.IsAbs(h) {
		out = append(out, filepath.Join(h, ".local", "state", "ataniq"))
	}
	uid := os.Geteuid()
	home, err := ownerAccountHome(uid)
	if err != nil || !filepath.IsAbs(home) {
		return nil, fmt.Errorf("cannot look up the account's home folder, so the default folders are not known: %v", err)
	}
	out = append(out, filepath.Join(home, ".local", "state", "ataniq"), filepath.Join("/run/user", strconv.Itoa(uid), "ataniq"))
	return out, nil
}

// resolvePath makes a clean absolute path comparable: the symbolic links of its
// nearest existing part are resolved and the missing rest is appended.
func resolvePath(p string) (string, error) {
	if !filepath.IsAbs(p) || filepath.Clean(p) != p {
		return "", fmt.Errorf("%q is not a clean absolute path", p)
	}
	cur := p
	var rest []string
	for {
		if _, err := os.Lstat(cur); err == nil {
			break
		} else if !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, syscall.ENOTDIR) {
			return "", err
		}
		if cur == "/" {
			return "", errors.New("no part of the path exists")
		}
		rest = append([]string{filepath.Base(cur)}, rest...)
		cur = filepath.Dir(cur)
	}
	real, err := filepath.EvalSymlinks(cur)
	if err != nil {
		return "", err
	}
	return filepath.Join(append([]string{real}, rest...)...), nil
}

// under reports whether p is q or lies inside q.
func under(p, q string) bool {
	return p == q || q == "/" || strings.HasPrefix(p, q+"/")
}

// related reports whether the two paths are one folder or one lies inside the
// other.
func related(p, q string) bool { return under(p, q) || under(q, p) }

// checkScratchRoots is the guard of the owner-channel builder: the state folder
// must not be, lie inside or contain a default folder of the account; every
// scratch root of the settings must not be, lie inside or contain one either;
// and the mirror must lie under a scratch root. A path that cannot be resolved
// is refused. It looks at the file system once; it cannot stop a folder from
// being replaced afterwards, and a bind mount is not caught.
func checkScratchRoots(stateDir string, sc hostadapter.Scratch, getenv func(string) string) error {
	defaults, err := defaultFolders(getenv)
	if err != nil {
		return err
	}
	var cands []string
	for _, d := range defaults {
		c, err := resolvePath(d)
		if err != nil {
			return fmt.Errorf("cannot resolve the default folder %q: %v", d, err)
		}
		cands = append(cands, c)
	}
	state, err := resolvePath(stateDir)
	if err != nil {
		return fmt.Errorf("the scratch owner channel cannot resolve the state folder: %v", err)
	}
	for i, c := range cands {
		if related(state, c) {
			return fmt.Errorf("the scratch owner channel refuses the state folder %q: it is, lies inside or contains the default folder %q", stateDir, defaults[i])
		}
	}
	var roots []string
	for _, r := range sc.Roots {
		rr, err := resolvePath(r)
		if err != nil {
			return fmt.Errorf("cannot resolve the scratch root %q: %v", r, err)
		}
		for i, c := range cands {
			if related(rr, c) {
				return fmt.Errorf("the scratch owner channel refuses the scratch root %q: it is, lies inside or contains the default folder %q", r, defaults[i])
			}
		}
		roots = append(roots, rr)
	}
	mirror, err := resolvePath(sc.Mirror)
	if err != nil {
		return fmt.Errorf("the scratch owner channel cannot resolve the mirror: %v", err)
	}
	for _, r := range roots {
		if under(mirror, r) {
			return nil
		}
	}
	return fmt.Errorf("the scratch owner channel refuses the mirror %q: it is not under a scratch root of the settings", sc.Mirror)
}

// checkOwnerScratch reads the settings of the state folder and applies the
// guards of the scratch owner channel: the settings select the stand-in door
// (LoadScratch), and the roots are the scratch ones.
func checkOwnerScratch(stateDir string, getenv func(string) string) (hostadapter.Scratch, error) {
	sc, err := hostadapter.LoadScratch(filepath.Join(stateDir, hostadapter.OwnerDirName, hostadapter.ScratchName))
	if err != nil {
		return hostadapter.Scratch{}, err
	}
	if err := checkScratchRoots(stateDir, sc, getenv); err != nil {
		return hostadapter.Scratch{}, err
	}
	return sc, nil
}
