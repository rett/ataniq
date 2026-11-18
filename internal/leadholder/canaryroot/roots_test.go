//go:build linux && ataniq_canary

package canaryroot

import (
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"testing"
)

// fakeUID is the user id of the injected account. Its runtime folder is under
// the fixture's own base folder, never under the host's /run/user.
const fakeUID = 4242

// fixture is a private tree with the layout of a user's folders, and an
// account that points into it.
type fixture struct {
	base       string // resolved
	home       string
	stateHome  string // <home>/.local/state
	stateDef   string // <stateHome>/ataniq
	runDir     string // <base>/run/user
	runBase    string // <base>/runbase, the env's base of the per-user runtime folders
	acctRun    string // <runBase>/<fakeUID>/ataniq, the account's runtime default
	runtimeDef string // <runDir>/ataniq
	env        env
	getenv     func(string) string
	good       [2]string // two unrelated roots that no check refuses
}

func realpath(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func mkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o700); err != nil {
		t.Fatal(err)
	}
}

func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

// newFixture makes the tree. With existing true the default folders exist.
func newFixture(t *testing.T, existing bool) *fixture {
	t.Helper()
	base := realpath(t, t.TempDir())
	f := &fixture{base: base}
	f.home = filepath.Join(base, "home")
	f.stateHome = filepath.Join(f.home, ".local", "state")
	f.stateDef = filepath.Join(f.stateHome, "ataniq")
	f.runDir = filepath.Join(base, "run", "user")
	f.runtimeDef = filepath.Join(f.runDir, "ataniq")
	f.runBase = filepath.Join(base, "runbase")
	f.acctRun = filepath.Join(f.runBase, strconv.Itoa(fakeUID), "ataniq")
	mkdir(t, f.home)
	mkdir(t, f.runDir)
	mkdir(t, f.runBase)
	if existing {
		mkdir(t, f.stateDef)
		mkdir(t, f.runtimeDef)
	}
	f.good = [2]string{filepath.Join(base, "canary", "state"), filepath.Join(base, "canary", "run")}
	f.env = env{
		uid:     fakeUID,
		home:    func(int) (string, error) { return f.home, nil },
		eval:    filepath.EvalSymlinks,
		runBase: f.runBase,
	}
	f.getenv = envOf(map[string]string{"HOME": f.home, "XDG_RUNTIME_DIR": f.runDir})
	return f
}

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func wantRefusal(t *testing.T, err error, code Code) {
	t.Helper()
	var re *RefusedError
	if !errors.As(err, &re) {
		t.Fatalf("got %v, want a *RefusedError with code %q", err, code)
	}
	if re.Code != code {
		t.Fatalf("code %q (%v), want %q", re.Code, err, code)
	}
}

func (f *fixture) state(root string) error {
	return f.env.checkRoots(root, f.good[1], f.getenv)
}

func (f *fixture) runtime(root string) error {
	return f.env.checkRoots(f.good[0], root, f.getenv)
}

func TestRootsUnrelatedAccepted(t *testing.T) {
	f := newFixture(t, true)
	if err := f.env.checkRoots(f.good[0], f.good[1], f.getenv); err != nil {
		t.Fatal(err)
	}
	// Names that only share a string prefix with a default are not nested in it.
	if err := f.env.checkRoots(f.stateDef+"-canary", f.runtimeDef+"x", f.getenv); err != nil {
		t.Fatal(err)
	}
	// Roots that do not exist yet are fine.
	if err := f.env.checkRoots(filepath.Join(f.base, "no", "such", "s"), filepath.Join(f.base, "no", "such", "r"), f.getenv); err != nil {
		t.Fatal(err)
	}
}

func TestRootsEquality(t *testing.T) {
	f := newFixture(t, true)
	wantRefusal(t, f.state(f.stateDef), CodeDefaultRoot)
	wantRefusal(t, f.runtime(f.runtimeDef), CodeDefaultRoot)
	// A state root that is the default runtime folder, and the reverse, are
	// refused too: every root is compared with every candidate.
	wantRefusal(t, f.state(f.runtimeDef), CodeDefaultRoot)
	wantRefusal(t, f.runtime(f.stateDef), CodeDefaultRoot)
}

func TestRootsInsideDefault(t *testing.T) {
	f := newFixture(t, true)
	wantRefusal(t, f.state(filepath.Join(f.stateDef, "canary")), CodeDefaultRoot)
	wantRefusal(t, f.state(filepath.Join(f.stateDef, "a", "b")), CodeDefaultRoot)
	wantRefusal(t, f.runtime(filepath.Join(f.runtimeDef, "canary")), CodeDefaultRoot)
}

func TestRootsContainingDefault(t *testing.T) {
	f := newFixture(t, true)
	wantRefusal(t, f.state(f.stateHome), CodeDefaultRoot)
	wantRefusal(t, f.state(f.home), CodeDefaultRoot)
	wantRefusal(t, f.state(f.base), CodeDefaultRoot)
	wantRefusal(t, f.state("/"), CodeDefaultRoot)
	wantRefusal(t, f.runtime(f.runDir), CodeDefaultRoot)
	wantRefusal(t, f.runtime(filepath.Dir(f.runDir)), CodeDefaultRoot)
}

func TestRootsThroughSymlink(t *testing.T) {
	f := newFixture(t, true)
	// The root is a link to the default.
	link := filepath.Join(f.base, "link")
	symlink(t, f.stateDef, link)
	wantRefusal(t, f.state(link), CodeDefaultRoot)
	// A link below the root's parent.
	linkDir := filepath.Join(f.base, "linkdir")
	symlink(t, f.stateHome, linkDir)
	wantRefusal(t, f.state(filepath.Join(linkDir, "ataniq")), CodeDefaultRoot)
	wantRefusal(t, f.state(filepath.Join(linkDir, "ataniq", "sub")), CodeDefaultRoot)
	// A link to a folder that contains the default.
	homeLink := filepath.Join(f.base, "homelink")
	symlink(t, f.home, homeLink)
	wantRefusal(t, f.state(homeLink), CodeDefaultRoot)
	// The runtime default through a link.
	rlink := filepath.Join(f.base, "rlink")
	symlink(t, f.runtimeDef, rlink)
	wantRefusal(t, f.runtime(rlink), CodeDefaultRoot)
	// A default given through a link, a root given by its real path.
	viaLink := envOf(map[string]string{"HOME": homeLink, "XDG_RUNTIME_DIR": f.runDir})
	wantRefusal(t, f.env.checkRoots(f.stateDef, f.good[1], viaLink), CodeDefaultRoot)
	// A link elsewhere does not refuse a root.
	other := filepath.Join(f.base, "other")
	mkdir(t, other)
	otherLink := filepath.Join(f.base, "otherlink")
	symlink(t, other, otherLink)
	if err := f.env.checkRoots(filepath.Join(otherLink, "s"), f.good[1], f.getenv); err != nil {
		t.Fatal(err)
	}
}

// A second path to one folder that no symbolic link explains, as a bind mount
// gives, is caught by identity. A symlink with a resolver that does not follow
// it stands in for the mount.
func TestRootsByIdentity(t *testing.T) {
	f := newFixture(t, true)
	f.env.eval = func(p string) (string, error) { return p, nil }

	alias := filepath.Join(f.base, "alias")
	symlink(t, f.stateDef, alias)
	pr, err := f.env.resolve(alias)
	if err != nil {
		t.Fatal(err)
	}
	cr, err := f.env.resolve(f.stateDef)
	if err != nil {
		t.Fatal(err)
	}
	if pr.path == cr.path {
		t.Fatalf("the paths already agree (%q): the test would not reach the identity check", pr.path)
	}
	wantRefusal(t, f.state(alias), CodeDefaultRoot)

	// Inside the default, below the alias.
	wantRefusal(t, f.state(filepath.Join(alias, "sub")), CodeDefaultRoot)
	wantRefusal(t, f.state(filepath.Join(alias, "a", "b")), CodeDefaultRoot)

	// Containing the default: an alias of an ancestor.
	homeAlias := filepath.Join(f.base, "homealias")
	symlink(t, f.home, homeAlias)
	wantRefusal(t, f.state(homeAlias), CodeDefaultRoot)
	wantRefusal(t, f.state(f.home), CodeDefaultRoot)

	// A default reached by an alias, the root by its real path.
	viaAlias := envOf(map[string]string{"XDG_STATE_HOME": filepath.Join(homeAlias, ".local", "state"), "XDG_RUNTIME_DIR": f.runDir})
	wantRefusal(t, f.env.checkRoots(f.stateDef, f.good[1], viaAlias), CodeDefaultRoot)

	// The runtime folder, too.
	rAlias := filepath.Join(f.base, "ralias")
	symlink(t, f.runtimeDef, rAlias)
	wantRefusal(t, f.runtime(rAlias), CodeDefaultRoot)

	// Unrelated folders are still accepted.
	otherAlias := filepath.Join(f.base, "otheralias")
	mkdir(t, f.good[0])
	symlink(t, f.good[0], otherAlias)
	if err := f.env.checkRoots(otherAlias, f.good[1], f.getenv); err != nil {
		t.Fatal(err)
	}
}

func TestRootsDefaultNotExistingYet(t *testing.T) {
	f := newFixture(t, false)
	// Only home and the runtime parent exist.
	wantRefusal(t, f.state(f.stateDef), CodeDefaultRoot)
	wantRefusal(t, f.state(filepath.Join(f.stateDef, "canary")), CodeDefaultRoot)
	wantRefusal(t, f.state(filepath.Join(f.home, ".local")), CodeDefaultRoot)
	wantRefusal(t, f.state(f.stateHome), CodeDefaultRoot)
	wantRefusal(t, f.runtime(f.runtimeDef), CodeDefaultRoot)
	wantRefusal(t, f.runtime(filepath.Join(f.runtimeDef, "canary")), CodeDefaultRoot)
	// The account's runtime default for the fake user does not exist either.
	wantRefusal(t, f.runtime(f.acctRun), CodeDefaultRoot)
	wantRefusal(t, f.runtime(filepath.Join(f.acctRun, "x")), CodeDefaultRoot)
	if err := f.state(filepath.Join(f.home, ".local", "state", "other")); err != nil {
		t.Fatalf("a sibling of a default that does not exist yet: %v", err)
	}

	// The default reaches its nearest existing parent through a link; the
	// root does not.
	homeLink := filepath.Join(f.base, "homelink")
	symlink(t, f.home, homeLink)
	viaLink := envOf(map[string]string{"HOME": homeLink})
	wantRefusal(t, f.env.checkRoots(f.stateDef, f.good[1], viaLink), CodeDefaultRoot)

	// The same by identity alone.
	f.env.eval = func(p string) (string, error) { return p, nil }
	wantRefusal(t, f.env.checkRoots(f.stateDef, f.good[1], viaLink), CodeDefaultRoot)
	wantRefusal(t, f.env.checkRoots(filepath.Join(f.stateDef, "x"), f.good[1], viaLink), CodeDefaultRoot)
	wantRefusal(t, f.env.checkRoots(filepath.Join(f.home, ".local"), f.good[1], viaLink), CodeDefaultRoot)
	if err := f.env.checkRoots(filepath.Join(f.home, ".local", "state", "other"), f.good[1], viaLink); err != nil {
		t.Fatalf("a sibling, by identity: %v", err)
	}
}

// The environment says one thing and the account another. Each gives a
// candidate, and both count.
func TestRootsSourcesDisagree(t *testing.T) {
	f := newFixture(t, true)
	changed := filepath.Join(f.base, "changed-state")
	mkdir(t, filepath.Join(changed, "ataniq"))
	getenv := envOf(map[string]string{
		"HOME": f.home, "XDG_STATE_HOME": changed, "XDG_RUNTIME_DIR": f.runDir,
	})
	// The given root is the account's default; the environment names another.
	wantRefusal(t, f.env.checkRoots(f.stateDef, f.good[1], getenv), CodeDefaultRoot)
	// The given root is the environment's; the account's default differs.
	wantRefusal(t, f.env.checkRoots(filepath.Join(changed, "ataniq"), f.good[1], getenv), CodeDefaultRoot)

	// The runtime folder: the environment's variable points elsewhere.
	changedRun := filepath.Join(f.base, "changed-run")
	mkdir(t, filepath.Join(changedRun, "ataniq"))
	getenv = envOf(map[string]string{"HOME": f.home, "XDG_RUNTIME_DIR": changedRun})
	wantRefusal(t, f.env.checkRoots(f.good[0], filepath.Join(changedRun, "ataniq"), getenv), CodeDefaultRoot)
	wantRefusal(t, f.env.checkRoots(f.good[0], f.acctRun, getenv), CodeDefaultRoot)

	// A HOME that differs from the account's gives a second state candidate;
	// the account's stays. With XDG_STATE_HOME set, see
	// TestRootsHomeDiffersWithXDGStateHome.
	otherHome := filepath.Join(f.base, "otherhome")
	mkdir(t, otherHome)
	getenv = envOf(map[string]string{"HOME": otherHome})
	wantRefusal(t, f.env.checkRoots(filepath.Join(otherHome, ".local", "state", "ataniq"), f.good[1], getenv), CodeDefaultRoot)
	wantRefusal(t, f.env.checkRoots(f.stateDef, f.good[1], getenv), CodeDefaultRoot)
}

// A unit's environment has no HOME and no XDG_* variable. The account's
// defaults are still refused.
func TestRootsUnitLikeEnvironment(t *testing.T) {
	f := newFixture(t, true)
	for name, getenv := range map[string]func(string) string{
		"empty":     func(string) string { return "" },
		"nil":       nil,
		"unrelated": envOf(map[string]string{"PATH": "/usr/bin", "LANG": "C"}),
		"relative":  envOf(map[string]string{"HOME": "h", "XDG_STATE_HOME": "s", "XDG_RUNTIME_DIR": "r"}),
	} {
		t.Run(name, func(t *testing.T) {
			wantRefusal(t, f.env.checkRoots(f.stateDef, f.good[1], getenv), CodeDefaultRoot)
			wantRefusal(t, f.env.checkRoots(f.good[0], f.acctRun, getenv), CodeDefaultRoot)
			wantRefusal(t, f.env.checkRoots(filepath.Join(f.stateDef, "x"), f.good[1], getenv), CodeDefaultRoot)
			wantRefusal(t, f.env.checkRoots(f.home, f.good[1], getenv), CodeDefaultRoot)
			if err := f.env.checkRoots(f.good[0], f.good[1], getenv); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// The same with the real account lookup: the default folders of whoever runs
// the test are refused when the environment is empty. Nothing is created.
func TestCheckRootsUsesTheAccount(t *testing.T) {
	u, err := user.Current()
	if err != nil || !filepath.IsAbs(u.HomeDir) {
		t.Skipf("no account with a home folder here: %v", err)
	}
	empty := func(string) string { return "" }
	good := t.TempDir()
	wantRefusal(t, CheckRoots(filepath.Join(u.HomeDir, ".local", "state", "ataniq"), filepath.Join(good, "r"), empty), CodeDefaultRoot)
	wantRefusal(t, CheckRoots(filepath.Join(good, "s"), filepath.Join("/run/user", u.Uid, "ataniq"), nil), CodeDefaultRoot)
	if err := CheckRoots(filepath.Join(good, "s"), filepath.Join(good, "r"), empty); err != nil {
		t.Fatal(err)
	}
}

func TestRootsMustBeCleanAbsolute(t *testing.T) {
	f := newFixture(t, true)
	for _, bad := range []string{
		"", "state", "./state", "../state", "relative/path",
		f.good[0] + "/", f.good[0] + "/.", f.good[0] + "/../x", f.base + "//canary", "/a/./b",
	} {
		wantRefusal(t, f.env.checkRoots(bad, f.good[1], f.getenv), CodeDefaultRoot)
		wantRefusal(t, f.env.checkRoots(f.good[0], bad, f.getenv), CodeDefaultRoot)
	}
}

func TestRootsRefusedWhenUnresolvable(t *testing.T) {
	f := newFixture(t, true)
	dangling := filepath.Join(f.base, "dangling")
	symlink(t, filepath.Join(f.base, "nowhere"), dangling)
	wantRefusal(t, f.state(dangling), CodeDefaultRoot)
	wantRefusal(t, f.state(filepath.Join(dangling, "x")), CodeDefaultRoot)

	// A candidate that cannot be resolved refuses too: the check fails closed.
	getenv := envOf(map[string]string{"XDG_STATE_HOME": dangling})
	wantRefusal(t, f.env.checkRoots(f.good[0], f.good[1], getenv), CodeDefaultRoot)
}

func TestRootsAccountFaults(t *testing.T) {
	f := newFixture(t, true)
	f.env.home = func(int) (string, error) { return "", errors.New("no such user") }
	wantRefusal(t, f.env.checkRoots(f.good[0], f.good[1], f.getenv), CodeDefaultRoot)
	f.env.home = func(int) (string, error) { return "", nil }
	wantRefusal(t, f.env.checkRoots(f.good[0], f.good[1], f.getenv), CodeDefaultRoot)
	f.env.home = func(int) (string, error) { return "relative/home", nil }
	wantRefusal(t, f.env.checkRoots(f.good[0], f.good[1], f.getenv), CodeDefaultRoot)
}

func TestRootsTheTwoGivenRoots(t *testing.T) {
	f := newFixture(t, true)
	root := f.good[0]
	check := func(state, runtime string) error { return f.env.checkRoots(state, runtime, f.getenv) }

	wantRefusal(t, check(root, root), CodeNestedRoot)
	wantRefusal(t, check(root, filepath.Join(root, "run")), CodeNestedRoot)
	wantRefusal(t, check(filepath.Join(root, "state"), root), CodeNestedRoot)

	// Through a link.
	mkdir(t, root)
	link := filepath.Join(f.base, "canarylink")
	symlink(t, root, link)
	wantRefusal(t, check(root, link), CodeNestedRoot)
	wantRefusal(t, check(link, filepath.Join(root, "run")), CodeNestedRoot)

	// By identity.
	f.env.eval = func(p string) (string, error) { return p, nil }
	wantRefusal(t, check(root, link), CodeNestedRoot)
	wantRefusal(t, check(filepath.Join(link, "state"), root), CodeNestedRoot)

	// Siblings with a shared string prefix are not nested.
	if err := check(root+"-a", root+"-b"); err != nil {
		t.Fatal(err)
	}
	if err := check(root+"-a", root); err != nil {
		t.Fatal(err)
	}
}

func TestRefusedErrorText(t *testing.T) {
	err := error(refuse(CodeNoMarker, "x %d", 1))
	if got := err.Error(); got != "canary root refused (no-marker): x 1" {
		t.Fatalf("Error() = %q", got)
	}
}
