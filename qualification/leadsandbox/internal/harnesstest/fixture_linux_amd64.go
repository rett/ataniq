//go:build linux && amd64

package harnesstest

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"ataniq/internal/leadsandbox/assert"
	"ataniq/internal/leadsandbox/mountpoints"
	"ataniq/internal/leadsandbox/profile"
	"ataniq/qualification/leadsandbox/internal/harness"
)

// Fixture is a fake world under a temporary folder.
type Fixture struct {
	T *testing.T
	// Base is the real path of the temporary folder.
	Base     string
	Campaign string
	State    string
	Owned    string
	Role     string
	Project  string
	Runtime  profile.Runtime
	// Config is the configuration the fixture's world matches. Tests change
	// it before WriteConfig.
	Config *harness.Config
	// Resolv is the content the fake host /etc/resolv.conf has.
	Resolv string
	// Sysctl is the content of the fake dev.tty.legacy_tiocsti.
	Sysctl string
}

// New builds the world for a runtime.
func New(t *testing.T, rt profile.Runtime) *Fixture {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &Fixture{T: t, Base: base, Campaign: base + "/campaign", State: base + "/state", Owned: base + "/owned",
		Role: "qual-lead", Project: "qualproj", Runtime: rt, Resolv: "nameserver 192.0.2.53\n", Sysctl: "0\n"}
	l := profile.Layout{StateRoot: f.State, Role: f.Role, Project: f.Project}
	cfgDir, _ := l.ConfigDir(rt)
	for _, d := range []string{f.Campaign, l.LeadDir(), l.Home(), cfgDir, l.Tree()} {
		f.Mkdir(d, 0o700)
	}
	f.Mkdir(l.MirrorsDir(), 0o700)
	f.Mkdir(l.Mirror(), 0o700)
	f.FakeClone()

	own := func(p string, dir bool) string {
		p = f.Owned + "/" + p
		if dir {
			f.Mkdir(p, 0o755)
		} else {
			f.Mkdir(filepath.Dir(p), 0o755)
			f.Write(p, "x", 0o644)
		}
		return p
	}
	exe := func(name string) string {
		p := f.Base + "/bin/" + name
		f.Mkdir(filepath.Dir(p), 0o755)
		f.Write(p, name+"-stand-in", 0o755)
		return p
	}
	managed := own("managed/settings.json", false)
	body := `{"hooks":{"PreToolUse":[{"command":"qargi-guard"}]}}`
	f.Write(managed, body, 0o644)
	sum := sha256.Sum256([]byte(body))
	cfg := &harness.Config{
		Version:        harness.ConfigVersion,
		Campaign:       f.Campaign,
		Executables:    harness.Executables{Pasta: exe("pasta"), Bwrap: exe("bwrap"), Probe: exe("ataniq-probe")},
		Runtime:        string(rt),
		RuntimeVersion: "2.1.0",
		State:          harness.State{Root: f.State, Role: f.Role, Project: f.Project},
		Sources: harness.Sources{
			AgentInstall: own("agent", true), ManagedSettings: managed, ManagedSettingsSHA256: hex.EncodeToString(sum[:]),
			ManagedMarker: "qargi-guard",
			LeadSkills:    own("skills", true), InstructionFile: own("instructions/"+rt.InstructionFileName(), false),
			GitIdentityFile: own("git/config", false), QargiInstall: own("qargi", true),
			AssignmentDir: own("assignment", true), ProjectionDir: own("projection", true),
		},
		NeverMounted:     []string{f.Base + "/owner/work", f.Base + "/owner/.claude", f.Base + "/owner/.codex", f.Base + "/owner/.ssh"},
		ProviderHost:     "api.example.test",
		Terminal:         harness.Terminal{Term: "xterm-256color", Lang: "C.UTF-8", Rows: 24, Cols: 80},
		TimeLimitSeconds: 30,
		Cases:            []string{"S1", "S2", "S3", "S4", "S7", "S11"},
	}
	if rt == profile.Codex {
		cfg.Sources.ManagedSettingsInside = "/etc/codex/managed-config.toml"
		cfg.Sources.ManagedMarker = ""
	}
	f.Config = cfg
	return f
}

// Mkdir makes a folder with an exact mode.
func (f *Fixture) Mkdir(p string, mode os.FileMode) {
	f.T.Helper()
	if err := os.MkdirAll(p, mode); err != nil {
		f.T.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		f.T.Fatal(err)
	}
}

// Write writes a file with an exact mode.
func (f *Fixture) Write(p, data string, mode os.FileMode) {
	f.T.Helper()
	if err := os.WriteFile(p, []byte(data), mode); err != nil {
		f.T.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		f.T.Fatal(err)
	}
}

// Layout is the lead's host layout.
func (f *Fixture) Layout() profile.Layout {
	return profile.Layout{StateRoot: f.State, Role: f.Role, Project: f.Project}
}

// FakeClone builds the .git folder of a full clone in the tree, by hand.
func (f *Fixture) FakeClone() {
	f.T.Helper()
	g := f.Layout().Tree() + "/.git"
	if err := os.RemoveAll(g); err != nil {
		f.T.Fatal(err)
	}
	for _, d := range []string{g + "/objects", g + "/refs", g + "/info", g + "/hooks"} {
		f.Mkdir(d, 0o755)
	}
	f.Write(g+"/HEAD", "ref: refs/heads/main\n", 0o644)
	f.Write(g+"/config", "[core]\n\trepositoryformatversion = 0\n", 0o644)
	f.Write(g+"/info/exclude", "# comment\n", 0o644)
}

// WriteConfig writes the configuration to a file with mode 0600 and returns
// its path.
func (f *Fixture) WriteConfig() string {
	f.T.Helper()
	data, err := json.MarshalIndent(f.Config, "", "  ")
	if err != nil {
		f.T.Fatal(err)
	}
	return f.WriteConfigBytes(data)
}

// WriteConfigBytes writes configuration bytes to a new file with mode 0600.
func (f *Fixture) WriteConfigBytes(data []byte) string {
	f.T.Helper()
	dir, err := os.MkdirTemp(f.Base, "cfg")
	if err != nil {
		f.T.Fatal(err)
	}
	p := dir + "/config.json"
	f.Write(p, string(data), 0o600)
	return p
}

// Load writes the configuration and loads it back, as the command does.
func (f *Fixture) Load() *harness.Config {
	f.T.Helper()
	cfg, err := harness.LoadConfig(f.WriteConfig())
	if err != nil {
		f.T.Fatalf("the fixture's configuration is refused: %v", err)
	}
	return cfg
}

// Options are the options of a run against the fake world with the given
// launcher: every seam is hermetic.
func (f *Fixture) Options(l harness.Launcher) harness.Options {
	return harness.Options{
		Launcher:         l,
		StdinIsTerminal:  func() bool { return true },
		NetFS:            fakeNetFS{content: f.Resolv},
		Resolver:         f.resolve,
		CheckBindSources: f.CheckBindSources,
		HostHas:          func(string) bool { return true },
		System:           sysctlSystem{content: f.Sysctl},
	}
}

// resolve resolves a path under the fixture with the real file system and
// returns any other path unchanged, so a host row such as /usr is never looked
// at.
func (f *Fixture) resolve(p string) (string, error) {
	if p == f.Base || strings.HasPrefix(p, f.Base+"/") {
		return filepath.EvalSymlinks(p)
	}
	return p, nil
}

// CheckBindSources is the check of the bind sources against the real file
// system for the sources that lie under the fixture, and nothing for the host's
// own rows (/usr, /etc/localtime and the like), which a test never looks at.
func (f *Fixture) CheckBindSources(sources []string) mountpoints.Report {
	var own []string
	for _, s := range sources {
		if s == f.Base || strings.HasPrefix(s, f.Base+"/") {
			own = append(own, s)
		}
	}
	return mountpoints.CheckBindSources(own)
}

// Attempts lists the attempt folders of the campaign.
func (f *Fixture) Attempts() []string {
	f.T.Helper()
	ents, err := os.ReadDir(f.Campaign)
	if err != nil {
		f.T.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		out = append(out, f.Campaign+"/"+e.Name())
	}
	sort.Strings(out)
	return out
}

// Names lists the entries of a folder, sorted.
func (f *Fixture) Names(dir string) []string {
	f.T.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		f.T.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return out
}

// ProbeLine is the report line of a fake probe: every inside result with the
// status, and the result of the number given (when above 0) with failed.
func ProbeLine(digest string, status assert.Status, failedNumber int) []byte {
	var rs []assert.Result
	for _, a := range assert.Assertions() {
		if !a.HasInside() {
			continue
		}
		st := status
		if a.Number == failedNumber {
			st = assert.StatusFailed
		}
		rs = append(rs, assert.Result{Number: a.Number, ID: a.ID, Status: st, Detail: "a fake probe says so"})
	}
	return assert.EncodeLine(assert.Report{Version: assert.ReportVersion, Digest: digest, Results: rs})
}

type fakeNetFS struct{ content string }

func (n fakeNetFS) ReadFile(name string, limit int) ([]byte, error) {
	if name == "/etc/resolv.conf" {
		return []byte(n.content), nil
	}
	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
}

func (n fakeNetFS) RealPath(name string) (string, error) { return name, nil }

// sysctlSystem answers the one file the outside checks read.
type sysctlSystem struct {
	assert.System
	content string
}

// Identity gives each path an identity made from its name, so that the
// identities the harness collects are deterministic and nothing of the host's
// file system is read. A path under the fixture's base exists as far as the
// real file system says so (an absent path is skipped by the collector).
func (s sysctlSystem) Identity(path string) (assert.FileIdentity, error) {
	if _, err := os.Lstat(path); err != nil {
		return assert.FileIdentity{}, err
	}
	h := sha256.Sum256([]byte(path))
	return assert.FileIdentity{Device: 4242, Inode: binary.BigEndian.Uint64(h[:8]) | 1<<40}, nil
}

func (s sysctlSystem) ReadFile(path string, limit int) ([]byte, error) {
	if strings.HasSuffix(path, "legacy_tiocsti") {
		return []byte(s.content), nil
	}
	return nil, errors.New("harnesstest: only the sysctl may be read")
}

// FailingIdentity wraps a System and makes every identity question fail with a
// reason other than a missing file, as a host that cannot be read would.
type FailingIdentity struct{ assert.System }

// Identity implements assert.System.
func (FailingIdentity) Identity(string) (assert.FileIdentity, error) {
	return assert.FileIdentity{}, errors.New("harnesstest: the identity cannot be read")
}
