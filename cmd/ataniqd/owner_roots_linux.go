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
// scratch root of the settings and the drop folder must not be, lie inside or
// contain one either (nor the drop folder the state folder); and the mirror must
// lie under a scratch root. A path that cannot be resolved
// is refused. It looks at the file system once; it cannot stop a folder from
// being replaced afterwards, and a bind mount is not caught.
func checkScratchRoots(stateDir string, sc hostadapter.Scratch, getenv func(string) string) error {
	defaults, cands, state, err := checkScratchState(stateDir, getenv)
	if err != nil {
		return err
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
	underRoot := func(p string) bool {
		for _, r := range roots {
			if under(p, r) {
				return true
			}
		}
		return false
	}
	if !underRoot(mirror) {
		return fmt.Errorf("the scratch owner channel refuses the mirror %q: it is not under a scratch root of the settings", sc.Mirror)
	}
	// The drop folder is the one place another process is expected to write,
	// and the collector writes its replies and the taken requests there and
	// beside it: it is not, lies not inside and does not contain a default
	// folder or the state folder (whose owner/ folder is part of it).
	drop, err := resolvePath(sc.Drop)
	if err != nil {
		return fmt.Errorf("the scratch owner channel cannot resolve the drop folder: %v", err)
	}
	for i, c := range cands {
		if related(drop, c) {
			return fmt.Errorf("the scratch owner channel refuses the drop folder %q: it is, lies inside or contains the default folder %q", sc.Drop, defaults[i])
		}
	}
	if related(drop, state) {
		return fmt.Errorf("the scratch owner channel refuses the drop folder %q: it is, lies inside or contains the state folder (or its owner folder)", sc.Drop)
	}
	// The sandbox template and the work folder of the suite are scratch
	// paths too. Neither is in the state folder, which is kept for the host's
	// records and journal and free of configuration (an owner's mistake there
	// would be hard to see); the work folder and the mirror are apart, as
	// package verify asks.
	var tpl, work string
	for _, f := range []struct{ name, path string }{{"sandbox template", sc.Sandbox}, {"work folder", sc.Work}} {
		if f.path == "" {
			continue
		}
		p, err := resolvePath(f.path)
		if err != nil {
			return fmt.Errorf("the scratch owner channel cannot resolve the %s: %v", f.name, err)
		}
		if !underRoot(p) {
			return fmt.Errorf("the scratch owner channel refuses the %s %q: it is not under a scratch root of the settings", f.name, f.path)
		}
		if related(p, state) {
			return fmt.Errorf("the scratch owner channel refuses the %s %q: it is, lies inside or contains the state folder", f.name, f.path)
		}
		if f.path == sc.Work {
			work = p
			if related(p, mirror) {
				return fmt.Errorf("the scratch owner channel refuses the work folder %q: it is, lies inside or contains the mirror", f.path)
			}
		} else {
			tpl = p
		}
	}
	// The template decides which command wraps the gates, and the drop
	// folder, the mirror and the work folder hold what others write or run
	// in: the template is in none of them, and the work folder does not hold
	// the drop folder.
	if tpl != "" && (under(tpl, drop) || under(tpl, mirror)) {
		return fmt.Errorf("the scratch owner channel refuses the sandbox template %q: it lies inside the drop folder or the mirror", sc.Sandbox)
	}
	if tpl != "" && work != "" && under(tpl, work) {
		return fmt.Errorf("the scratch owner channel refuses the sandbox template %q: it lies inside the work folder", sc.Sandbox)
	}
	if work != "" && related(work, drop) {
		return fmt.Errorf("the scratch owner channel refuses the work folder %q: it is, lies inside or contains the drop folder", sc.Work)
	}
	return nil
}

// checkScratchState is the first guard of the scratch owner channel and of
// the phone trial: the state folder must not be, lie inside or contain a
// default folder of the account. It returns the default folders, the same
// resolved, and the state folder resolved. A path that cannot be resolved is
// refused.
func checkScratchState(stateDir string, getenv func(string) string) (defaults, cands []string, state string, err error) {
	defaults, err = defaultFolders(getenv)
	if err != nil {
		return nil, nil, "", err
	}
	for _, d := range defaults {
		c, err := resolvePath(d)
		if err != nil {
			return nil, nil, "", fmt.Errorf("cannot resolve the default folder %q: %v", d, err)
		}
		cands = append(cands, c)
	}
	state, err = resolvePath(stateDir)
	if err != nil {
		return nil, nil, "", fmt.Errorf("the scratch owner channel cannot resolve the state folder: %v", err)
	}
	for i, c := range cands {
		if related(state, c) {
			return nil, nil, "", fmt.Errorf("the scratch owner channel refuses the state folder %q: it is, lies inside or contains the default folder %q", stateDir, defaults[i])
		}
	}
	return defaults, cands, state, nil
}

// scratchLoad reads the owner channel's settings: the settings file of the
// state folder ([stateScratch]), or, in a build with both tags, the one of the
// folder that run --owner-settings names (scratch_settings_linux.go).
type scratchLoad func() (hostadapter.Scratch, error)

// stateScratch reads the settings file of the state folder,
// owner/scratch.json, with the strict reader of the host adapter.
func stateScratch(stateDir string) scratchLoad {
	return func() (hostadapter.Scratch, error) {
		return hostadapter.LoadScratch(filepath.Join(stateDir, hostadapter.OwnerDirName, hostadapter.ScratchName))
	}
}

// checkOwnerScratch reads the settings of the state folder and applies the
// guards of the scratch owner channel: the settings select the stand-in door
// (LoadScratch) and are not a phone trial's, and the roots are the scratch
// ones.
func checkOwnerScratch(stateDir string, getenv func(string) string) (hostadapter.Scratch, error) {
	return checkOwnerScratchFrom(stateDir, stateScratch(stateDir), getenv)
}

// checkOwnerScratchFrom is [checkOwnerScratch] on the settings load reads.
func checkOwnerScratchFrom(stateDir string, load scratchLoad, getenv func(string) string) (hostadapter.Scratch, error) {
	sc, err := load()
	if err != nil {
		return hostadapter.Scratch{}, err
	}
	if sc.Trial {
		return hostadapter.Scratch{}, errors.New(`the scratch settings are a phone trial's ("trial": true), which serves sample cards only: run it with --trial`)
	}
	if err := checkScratchRoots(stateDir, sc, getenv); err != nil {
		return hostadapter.Scratch{}, err
	}
	return sc, nil
}
