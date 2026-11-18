//go:build linux && ataniq_canary

// Package canaryroot is the root check of canary mode. The daemon, the holder
// and provisioning share it. Canary mode runs a throwaway lead with real
// credentials in a real sandbox, so it must never touch the state or the
// runtime folder of a real lead. The check has two parts: the roots it is given
// must not be, lie inside or contain a default folder (CheckRoots), and the
// state root must carry a marker that provisioning wrote for this project and
// nothing else wrote (CheckMarker). [Check] does both, after the project.
//
// # The default folders
//
// The defaults are worked out twice and every result is a candidate:
//
//   - from the environment, where the variables are set, exactly as
//     internal/leaddaemon/paths.go does: the state folder is
//     $XDG_STATE_HOME/ataniq, else $HOME/.local/state/ataniq, and the runtime
//     folder is $XDG_RUNTIME_DIR/ataniq, a variable that is not an absolute
//     path being ignored. paths_agree_test.go shows that the two agree;
//   - always from the account: the home folder in the password database
//     gives <home>/.local/state/ataniq, and the user's id gives
//     /run/user/<uid>/ataniq. The lookup needs no cgo.
//
// One more state candidate comes from the environment: when HOME is an
// absolute path, $HOME/.local/state/ataniq counts even where XDG_STATE_HOME is
// set, because a daemon started without that variable would use it.
//
// The second source is never skipped. A unit's environment may have no HOME
// and no XDG_* variable, or may differ from the daemon's, and a check that
// trusted the environment alone would then accept the real lead's folders.
// Every given root is compared with all the candidates of both kinds.
//
// # What is refused
//
// A given root is refused when it equals a candidate, lies inside one or
// contains one. A candidate counts as a match when the cleaned paths match
// with symbolic links resolved, and also when the device and inode of the two
// folders, or of one folder and an ancestor of the other, are the same, which
// catches a second path to one folder that no symbolic link explains, such as
// a bind mount of a default folder itself or of one of its ancestors. A path
// that does not exist yet is compared by the resolved path of its nearest
// existing parent and the rest, and by the identity of that parent. A relative
// or unclean root is refused, and so is a root or candidate that cannot be
// resolved: the check fails closed.
//
// # What is not caught
//
// A bind mount of a folder strictly inside a default folder is not caught: a
// given root that is that alias, or lies below it, has no ancestor with the
// identity of the default, and its resolved path is not under the default's.
// The runtime root has no marker behind it, and the state root's marker is
// written into whatever root passed this check, so neither covers the case.
// The full fix is to compare a canonical form of each path, the device and the
// path inside that file system, read from the mount table
// (/proc/self/mountinfo). That is not built.
//
// The two given roots must also differ and not nest.
//
// The check reads the file system once and the answer holds for that moment:
// it cannot stop a folder from being replaced afterwards.
package canaryroot

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"ataniq/internal/leadrole"
)

// MarkerName is the file name of the marker, in the state root.
const MarkerName = "canary-root.json"

// WaiverText is the line that says the canary slice runs without a
// qualification record for the sandbox, which assertion 10 would otherwise
// require.
const WaiverText = "assertion 10 waived: canary slice, no qualification record"

// stateName and runtimeName are the folder names under the user's state and
// runtime folders, as in internal/leaddaemon/paths.go.
const (
	stateName   = "ataniq"
	runtimeName = "ataniq"
)

// Code says why a root or a marker was refused.
type Code string

// The codes of a refusal.
const (
	// CodeDefaultRoot: a given root is, lies inside or contains a default
	// state or runtime folder, or is not a clean absolute path, or the
	// defaults could not be worked out.
	CodeDefaultRoot Code = "default-root"
	// CodeNestedRoot: the state root and the runtime root are the same
	// folder or one lies inside the other.
	CodeNestedRoot Code = "nested-root"
	// CodeNoMarker: the state root has no marker.
	CodeNoMarker Code = "no-marker"
	// CodeBadMarker: the marker is not a regular file of the caller with
	// mode 0600, at most 256 bytes and the exact content for the project.
	CodeBadMarker Code = "bad-marker"
	// CodeNotThrowaway: the project does not carry the throwaway prefix.
	CodeNotThrowaway Code = "not-throwaway"
)

// RefusedError is the error of every refusal of this package. A caller tells
// the refusals apart by Code (errors.As).
type RefusedError struct {
	Code   Code
	Detail string
}

// Error returns "canary root refused (<code>): <detail>".
func (e *RefusedError) Error() string {
	return "canary root refused (" + string(e.Code) + "): " + e.Detail
}

func refuse(code Code, format string, args ...any) *RefusedError {
	return &RefusedError{Code: code, Detail: fmt.Sprintf(format, args...)}
}

// env is what the check reads outside its arguments. The zero value is not
// usable; use systemEnv. A test injects the account and the resolver.
type env struct {
	// uid is the caller's user id, for /run/user/<uid> and the owner of the
	// marker.
	uid int
	// home returns the home folder of the account with the given user id, as
	// the password database has it.
	home func(uid int) (string, error)
	// eval resolves the symbolic links of an existing path.
	eval func(path string) (string, error)
	// runBase is the folder that holds the per-user runtime folders,
	// /run/user on a real system. A test points it into a temporary folder.
	runBase string
}

func systemEnv() env {
	return env{uid: unix.Geteuid(), home: accountHome, eval: filepath.EvalSymlinks, runBase: "/run/user"}
}

// accountHome looks the account up by id. The os/user package reads the
// password file when cgo is off, so no cgo is needed.
func accountHome(uid int) (string, error) {
	u, err := user.LookupId(strconv.Itoa(uid))
	if err != nil {
		return "", err
	}
	return u.HomeDir, nil
}

// envCandidates returns the default state and runtime folders the environment
// gives, as internal/leaddaemon/paths.go does. A variable that is not an
// absolute path is ignored; ok is false for a folder that has no default.
func envCandidates(getenv func(string) string) (state, runtime string, stateOK, runtimeOK bool) {
	if getenv == nil {
		return "", "", false, false
	}
	if x := getenv("XDG_STATE_HOME"); filepath.IsAbs(x) {
		state, stateOK = filepath.Join(x, stateName), true
	} else if h := getenv("HOME"); filepath.IsAbs(h) {
		state, stateOK = filepath.Join(h, ".local", "state", stateName), true
	}
	if x := getenv("XDG_RUNTIME_DIR"); filepath.IsAbs(x) {
		runtime, runtimeOK = filepath.Join(x, runtimeName), true
	}
	return state, runtime, stateOK, runtimeOK
}

// candidates returns every default folder: the environment's and the
// account's, state folders and runtime folders together. It also adds
// $HOME/.local/state/ataniq whenever HOME is absolute, which envCandidates
// leaves out where XDG_STATE_HOME is set: a daemon started without that
// variable would use it. envCandidates stays the copy of the daemon's
// derivation.
func (e env) candidates(getenv func(string) string) ([]string, error) {
	var out []string
	state, runtime, stateOK, runtimeOK := envCandidates(getenv)
	if stateOK {
		out = append(out, state)
	}
	if runtimeOK {
		out = append(out, runtime)
	}
	if h := getenvOrEmpty(getenv, "HOME"); filepath.IsAbs(h) {
		if c := filepath.Join(h, ".local", "state", stateName); !stateOK || c != state {
			out = append(out, c)
		}
	}
	home, err := e.home(e.uid)
	if err != nil {
		return nil, refuse(CodeDefaultRoot, "cannot look up the account of user %d: %v", e.uid, err)
	}
	if !filepath.IsAbs(home) {
		return nil, refuse(CodeDefaultRoot, "the account of user %d has no absolute home folder (%q)", e.uid, home)
	}
	out = append(out,
		filepath.Join(home, ".local", "state", stateName),
		filepath.Join(e.runBase, strconv.Itoa(e.uid), runtimeName))
	return out, nil
}

func getenvOrEmpty(getenv func(string) string, key string) string {
	if getenv == nil {
		return ""
	}
	return getenv(key)
}

// identity is the device and inode of a folder.
type identity struct{ dev, ino uint64 }

// place is a path made comparable: its symbolic links resolved as far as it
// exists, and the identity of its nearest existing part.
type place struct {
	given string // the path as given
	path  string // resolved: the nearest existing parent, resolved, plus rest
	// anchor is the identity of the nearest existing ancestor or self, and
	// rest the components below it that do not exist.
	anchor identity
	rest   []string
}

// resolve makes a clean absolute path comparable. It fails when a part cannot
// be examined or resolved.
func (e env) resolve(given string) (place, error) {
	cur := given
	var rest []string
	for {
		_, err := os.Lstat(cur)
		if err == nil {
			break
		}
		if !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, unix.ENOTDIR) {
			return place{}, err
		}
		if cur == "/" {
			return place{}, err
		}
		rest = append([]string{filepath.Base(cur)}, rest...)
		cur = filepath.Dir(cur)
	}
	real, err := e.eval(cur)
	if err != nil {
		return place{}, err
	}
	id, err := identityOf(cur)
	if err != nil {
		return place{}, err
	}
	return place{given: given, path: filepath.Join(append([]string{real}, rest...)...), anchor: id, rest: rest}, nil
}

// identityOf returns the identity of the folder a path names, following
// symbolic links.
func identityOf(path string) (identity, error) {
	var st unix.Stat_t
	if err := unix.Stat(path, &st); err != nil {
		return identity{}, err
	}
	return identity{dev: uint64(st.Dev), ino: uint64(st.Ino)}, nil
}

// inside reports whether p is q or lies inside q, by resolved path and by
// identity.
func inside(p, q place) bool {
	if p.path == q.path || strings.HasPrefix(p.path, q.path+"/") || q.path == "/" {
		return true
	}
	// Identity: some existing ancestor of p, or p itself, is q's nearest
	// existing part, and the parts of p below it start with q's missing ones.
	var comps []string
	if p.path != "/" {
		comps = strings.Split(strings.TrimPrefix(p.path, "/"), "/")
	}
	for n := len(comps); n >= 0; n-- {
		x := "/" + strings.Join(comps[:n], "/")
		id, err := identityOf(x)
		if err != nil {
			continue
		}
		if id == q.anchor && hasPrefix(comps[n:], q.rest) {
			return true
		}
	}
	return false
}

func hasPrefix(s, prefix []string) bool {
	if len(prefix) > len(s) {
		return false
	}
	for i := range prefix {
		if s[i] != prefix[i] {
			return false
		}
	}
	return true
}

// related reports whether p and q are the same place or one lies inside the
// other.
func related(p, q place) bool { return inside(p, q) || inside(q, p) }

// CheckRoots refuses a state root or a runtime root that is, lies inside or
// contains a default state or runtime folder of the account, and refuses the
// two roots when they are the same folder or one lies inside the other. getenv
// reads the environment (a nil getenv means no variable is set); the defaults
// are also worked out from the account, whatever the environment says. Each
// refusal is a *RefusedError with the code default-root or nested-root. See
// the package comment.
func CheckRoots(stateRoot, runtimeRoot string, getenv func(string) string) error {
	return systemEnv().checkRoots(stateRoot, runtimeRoot, getenv)
}

func (e env) checkRoots(stateRoot, runtimeRoot string, getenv func(string) string) error {
	roots := []struct{ name, path string }{{"state root", stateRoot}, {"runtime root", runtimeRoot}}
	var given []place
	for _, r := range roots {
		if !filepath.IsAbs(r.path) || filepath.Clean(r.path) != r.path {
			return refuse(CodeDefaultRoot, "the %s %q is not a clean absolute path", r.name, r.path)
		}
		p, err := e.resolve(r.path)
		if err != nil {
			return refuse(CodeDefaultRoot, "cannot resolve the %s %q: %v", r.name, r.path, err)
		}
		given = append(given, p)
	}
	cands, err := e.candidates(getenv)
	if err != nil {
		return err
	}
	for _, c := range cands {
		cp, err := e.resolve(c)
		if err != nil {
			return refuse(CodeDefaultRoot, "cannot resolve the default folder %q: %v", c, err)
		}
		for i, g := range given {
			if related(g, cp) {
				return refuse(CodeDefaultRoot, "the %s %q is, lies inside or contains the default folder %q", roots[i].name, g.given, c)
			}
		}
	}
	if related(given[0], given[1]) {
		return refuse(CodeNestedRoot, "the state root %q and the runtime root %q are the same folder or one lies inside the other", stateRoot, runtimeRoot)
	}
	return nil
}

// markerContent returns the exact content of the marker for a project.
func markerContent(project string) string {
	return `{"marker":"ataniq-canary-root","version":1,"project":"` + project + `"}` + "\n"
}

// maxMarkerSize is the largest accepted marker, in bytes.
const maxMarkerSize = 256

func requireThrowaway(project string) error {
	if !leadrole.IsThrowaway(project) {
		return refuse(CodeNotThrowaway, "the project %q does not carry the throwaway prefix", project)
	}
	return nil
}

// CheckMarker requires the marker of stateRoot for project: a regular file, not
// a symbolic link, owned by the caller, mode 0600, at most 256 bytes, with
// exactly the content WriteMarker writes for the project. The project must
// carry the throwaway prefix. It does not look at where stateRoot is; use
// Check for that.
func CheckMarker(stateRoot, project string) error {
	return systemEnv().checkMarker(stateRoot, project)
}

func (e env) checkMarker(stateRoot, project string) error {
	if err := requireThrowaway(project); err != nil {
		return err
	}
	path := filepath.Join(stateRoot, MarkerName)
	// O_NONBLOCK so that a named pipe cannot hold the open; the file is
	// judged by its handle, not by a path that could change.
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ENOTDIR) {
			return refuse(CodeNoMarker, "no marker %s: %v", path, err)
		}
		return refuse(CodeBadMarker, "cannot open the marker %s: %v", path, err)
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return refuse(CodeBadMarker, "cannot examine the marker %s: %v", path, err)
	}
	switch {
	case st.Mode&unix.S_IFMT != unix.S_IFREG:
		return refuse(CodeBadMarker, "the marker %s is not a regular file", path)
	case int(st.Uid) != e.uid:
		return refuse(CodeBadMarker, "the marker %s is not owned by the caller", path)
	case st.Mode&07777 != 0o600:
		return refuse(CodeBadMarker, "the marker %s has mode %04o, want 0600", path, st.Mode&07777)
	case st.Size > maxMarkerSize:
		return refuse(CodeBadMarker, "the marker %s is longer than %d bytes", path, maxMarkerSize)
	}
	buf := make([]byte, maxMarkerSize+1)
	n, err := f.Read(buf)
	if err != nil && n == 0 {
		return refuse(CodeBadMarker, "cannot read the marker %s: %v", path, err)
	}
	if string(buf[:n]) != markerContent(project) {
		return refuse(CodeBadMarker, "the marker %s does not hold the marker of project %q", path, project)
	}
	return nil
}

// Check requires, in this order, that the project carries the throwaway prefix,
// that CheckRoots accepts the two roots, and that CheckMarker accepts the
// state root. Each refusal is a *RefusedError.
func Check(stateRoot, runtimeRoot, project string, getenv func(string) string) error {
	return systemEnv().check(stateRoot, runtimeRoot, project, getenv)
}

func (e env) check(stateRoot, runtimeRoot, project string, getenv func(string) string) error {
	if err := requireThrowaway(project); err != nil {
		return err
	}
	if err := e.checkRoots(stateRoot, runtimeRoot, getenv); err != nil {
		return err
	}
	return e.checkMarker(stateRoot, project)
}

// WriteMarker creates the marker of stateRoot for project: mode 0600, the
// exact content CheckMarker requires. The file is created exclusively and
// without following a link, and written by its handle, so WriteMarker fails
// when anything is already at the marker's path. The state root must exist.
// The project must carry the throwaway prefix. Provisioning calls it as its
// last act.
func WriteMarker(stateRoot, project string) error {
	if err := requireThrowaway(project); err != nil {
		return err
	}
	path := filepath.Join(stateRoot, MarkerName)
	fd, err := unix.Open(path, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return fmt.Errorf("create the marker %s: %w", path, err)
	}
	f := os.NewFile(uintptr(fd), path)
	// The umask may have cleared bits of the mode; the marker is 0600 exactly.
	werr := unix.Fchmod(fd, 0o600)
	if werr == nil {
		_, werr = f.WriteString(markerContent(project))
	}
	if werr == nil {
		werr = f.Sync()
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		// The file is ours: this call created it just now.
		_ = os.Remove(path)
		return fmt.Errorf("write the marker %s: %w", path, werr)
	}
	return nil
}
