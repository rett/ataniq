//go:build linux && ataniq_canary

// Package provisiontest builds the temporary world that the tests of
// provisioning and of its command share: a state root that holds a mirror with
// one branch, a runtime root that is not made yet, the files the config names,
// and a config file that reads. It makes no namespace, no unit and no agent. It
// is a test helper and no production package imports it.
package provisiontest

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"ataniq/internal/exchange/gitenv"
	"ataniq/internal/leadcanary"
	"ataniq/internal/leadrole"
	"ataniq/internal/leadsandbox/profile"
	"ataniq/internal/mirror"
)

// Secret is a text the managed-settings file holds. A test looks for it in
// everything the code prints, to show that no file content is printed.
const Secret = "sample-secret-content-0123456789"

// Marker is the text of the guard hook that a managed-settings file holds in
// the tests of the sandbox inputs: the inputs name it as
// sources.managed_marker.
const Marker = "sample-guard-hook"

// Branch is the branch the mirror holds and the tree starts from.
const Branch = "main"

// World is one temporary world. Every path is real (no symbolic link in it).
type World struct {
	// Dir holds everything; State, Runtime and Conf lie in it.
	Dir     string
	State   string
	Runtime string
	Conf    string
	// ConfigPath is the config file, in Conf.
	ConfigPath string
	// Settings, Hook and Screens are the files and the folder the config
	// names under provision.
	Settings string
	Hook     string
	Screens  string
	// Mirror is the layout's mirror, in State.
	Mirror string
	// Head is the commit the mirror's branch points to.
	Head string
	// Cfg is the config; WriteConfig writes it.
	Cfg leadcanary.Config
}

// Role returns the canary role.
func Role(t testing.TB) leadrole.Role {
	t.Helper()
	r, err := leadrole.Parse("throwaway-one/lead")
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// New makes a world. The state root exists with mode 0700 and holds the
// mirror; the runtime root does not exist.
func New(t testing.TB) *World {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w := &World{
		Dir: dir, State: filepath.Join(dir, "state-root"), Runtime: filepath.Join(dir, "runtime-root"),
		Conf: filepath.Join(dir, "conf"),
	}
	for _, d := range []string{w.State, w.Conf, filepath.Join(w.State, "mirrors")} {
		mkdir(t, d)
	}
	srcs := filepath.Join(dir, "src")
	mkdir(t, srcs)
	w.Settings = filepath.Join(srcs, "managed.json")
	WriteFile(t, w.Settings, []byte(`{"note":"`+Secret+`"}`+"\n"), 0o600)
	w.Hook = filepath.Join(srcs, "guard")
	WriteFile(t, w.Hook, []byte("#!/bin/sh\nexit 0\n"), 0o700)
	w.Screens = filepath.Join(srcs, "screens")
	mkdir(t, w.Screens)
	for name, body := range map[string]string{
		"1.json": `{"cols":80,"rows":24}` + "\n", "1.txt": "first screen\n",
		"2.json": `{"cols":80,"rows":24}` + "\n", "2.txt": "second screen\n",
	} {
		WriteFile(t, filepath.Join(w.Screens, name), []byte(body), 0o600)
	}
	w.makeMirror(t)
	w.Cfg = leadcanary.Config{
		Version: 1, Role: Role(t), StateRoot: w.State, RuntimeRoot: w.Runtime,
		Holder: leadcanary.HolderConfig{
			Executable: "/opt/sample/holder", Cols: 120, Rows: 40,
			SandboxInputs: filepath.Join(dir, "inputs", "sandbox.json"),
			ProfileDigest: strings.Repeat("ab", 32),
		},
		Agent: leadcanary.AgentConfig{
			Kind: leadcanary.AgentStandIn, Runtime: string(profile.Runtimes()[0]), RuntimeVersion: "1.2.3",
			FooterMode: "sample mode on", Argv: []string{"/opt/sample/agent"},
		},
		Inject: true,
		Limits: leadcanary.Limits{
			MemoryMaxBytes: 4 << 30, SwapMaxBytes: 0, TasksMax: 512,
			MemoryFloorBytes: 1 << 30, MemorySharePercent: 50,
		},
		Provision: leadcanary.ProvisionConfig{
			Mirror: w.Mirror, UpstreamBranch: Branch,
			GitName: `Sample "Quoted" \ Name #1`, GitEmail: "sample@example.invalid",
			ManagedSettings: w.Settings, GuardHook: w.Hook, ReplayScreens: w.Screens,
		},
	}
	w.WriteConfig(t)
	return w
}

// WriteConfig writes Cfg to ConfigPath with mode 0600, replacing the file.
func (w *World) WriteConfig(t testing.TB) {
	t.Helper()
	data, err := json.Marshal(w.Cfg)
	if err != nil {
		t.Fatal(err)
	}
	w.ConfigPath = filepath.Join(w.Conf, "canary.json")
	_ = os.Remove(w.ConfigPath)
	WriteFile(t, w.ConfigPath, data, 0o600)
}

// Getenv is the environment of the default-root check: HOME and the XDG
// variables point into the world, at folders that do not exist.
func (w *World) Getenv() func(string) string {
	env := map[string]string{
		"HOME":            filepath.Join(w.Dir, "home"),
		"XDG_STATE_HOME":  filepath.Join(w.Dir, "xdg-state"),
		"XDG_RUNTIME_DIR": filepath.Join(w.Dir, "xdg-run"),
	}
	return func(k string) string { return env[k] }
}

func mkdir(t testing.TB, p string) {
	t.Helper()
	if err := os.Mkdir(p, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0o700); err != nil {
		t.Fatal(err)
	}
}

// WriteFile writes data to path with exactly the mode, whatever the umask.
func WriteFile(t testing.TB, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

// WriteManagedSettings writes a managed-settings file at path, the one the
// inputs name as sources.managed_settings, that holds text (Marker is the
// marker the inputs of the tests name). The folder is made when it is not
// there.
func WriteManagedSettings(t testing.TB, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	WriteFile(t, path, []byte(`{"hooks":{"PreToolUse":[{"command":"`+text+`"}]}}`+"\n"), 0o600)
}

// Git runs /usr/bin/git in dir with an explicit environment, for the setup of
// a test and the checks after it, and returns its output without the final
// line break.
func Git(t testing.TB, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("/usr/bin/git", append([]string{
		"-c", "protocol.file.allow=always",
		"-c", "user.name=Sample", "-c", "user.email=sample@example.invalid",
	}, args...)...)
	cmd.Dir = dir
	cmd.Env = []string{
		"PATH=/usr/bin:/bin", "HOME=/dev/null", "LC_ALL=C",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimRight(string(out), "\n")
}

// makeMirror makes the layout's mirror with the branch in it: a work
// repository with one commit, fetched into the mirror's upstream namespace.
func (w *World) makeMirror(t testing.TB) {
	t.Helper()
	work := filepath.Join(w.Dir, "work")
	mkdir(t, work)
	Git(t, work, "init", "--quiet", "--initial-branch="+Branch)
	WriteFile(t, filepath.Join(work, "README"), []byte("sample\n"), 0o644)
	Git(t, work, "add", "README")
	Git(t, work, "commit", "--quiet", "--message", "sample: first")
	w.Head = Git(t, work, "rev-parse", "HEAD")

	git, err := gitenv.New("/usr/bin/git")
	if err != nil {
		t.Fatal(err)
	}
	w.Mirror = filepath.Join(w.State, "mirrors", Role(t).Project()+".git")
	if _, err := mirror.Create(context.Background(), git, w.Mirror); err != nil {
		t.Fatal(err)
	}
	Git(t, w.Mirror, "--git-dir=.", "fetch", "--quiet", filepath.Join(work, ".git"), "refs/heads/"+Branch+":refs/heads/"+Branch)
}
