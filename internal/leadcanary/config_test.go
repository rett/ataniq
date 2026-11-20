//go:build linux && ataniq_canary

package leadcanary_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"ataniq/internal/leadcanary"
)

func TestReadConfigGood(t *testing.T) {
	f := newFixture(t)
	want := f.config(t)
	got, err := leadcanary.ReadConfig(f.writeConfig(t, want))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("read %+v, want %+v", got, want)
	}
	// agent.env may be absent.
	want.Agent.Env = nil
	got, err = leadcanary.ReadConfig(f.writeConfig(t, want))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Agent.Env) != 0 {
		t.Fatalf("env %v, want none", got.Agent.Env)
	}
	// A relative path to the file is read too.
	cwd, _ := os.Getwd()
	rel, err := filepath.Rel(cwd, filepath.Join(f.conf, "canary.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := leadcanary.ReadConfig(rel); err != nil {
		t.Fatalf("relative path: %v", err)
	}
}

func wantConfigError(t *testing.T, err error, field string) {
	t.Helper()
	if err == nil {
		t.Fatalf("no error, want a refusal of %q", field)
	}
	if !errors.Is(err, leadcanary.ErrConfig) {
		t.Fatalf("error %v does not wrap ErrConfig", err)
	}
	var ce *leadcanary.ConfigError
	if !errors.As(err, &ce) {
		t.Fatalf("error %v is not a ConfigError", err)
	}
	if field != "" && ce.Field != field {
		t.Fatalf("refused field %q (%v), want %q", ce.Field, err, field)
	}
}

func TestReadConfigFileFaults(t *testing.T) {
	f := newFixture(t)
	cfg := f.config(t)
	good, _ := json.Marshal(cfg)

	t.Run("mode", func(t *testing.T) {
		for _, mode := range []os.FileMode{0o644, 0o640, 0o660, 0o700, 0o400, 0o602, 0o600 | os.ModeSticky} {
			p := filepath.Join(f.conf, "mode.json")
			writeFile(t, p, good, mode)
			_, err := leadcanary.ReadConfig(p)
			wantConfigError(t, err, "file")
			os.Remove(p)
		}
	})
	t.Run("symlink at the last element", func(t *testing.T) {
		real := filepath.Join(f.conf, "real.json")
		writeFile(t, real, good, 0o600)
		link := filepath.Join(f.conf, "link.json")
		if err := os.Symlink(real, link); err != nil {
			t.Fatal(err)
		}
		_, err := leadcanary.ReadConfig(link)
		if err == nil || !errors.Is(err, leadcanary.ErrConfig) {
			t.Fatalf("a symbolic link was read: %v", err)
		}
	})
	t.Run("symlink in a folder on the way is followed", func(t *testing.T) {
		real := filepath.Join(f.conf, "real.json")
		writeFile(t, real, good, 0o600)
		dirLink := filepath.Join(f.dir, "conf-link")
		if err := os.Symlink(f.conf, dirLink); err != nil {
			t.Fatal(err)
		}
		if _, err := leadcanary.ReadConfig(filepath.Join(dirLink, "real.json")); err != nil {
			t.Fatalf("a folder link on the way should be fine: %v", err)
		}
	})
	t.Run("not a regular file", func(t *testing.T) {
		dir := filepath.Join(f.conf, "adir")
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		_, err := leadcanary.ReadConfig(dir)
		wantConfigError(t, err, "file")
		fifo := filepath.Join(f.conf, "fifo.json")
		if err := unix.Mkfifo(fifo, 0o600); err != nil {
			t.Fatal(err)
		}
		_, err = leadcanary.ReadConfig(fifo)
		wantConfigError(t, err, "file")
	})
	t.Run("missing", func(t *testing.T) {
		_, err := leadcanary.ReadConfig(filepath.Join(f.conf, "nothing.json"))
		if !errors.Is(err, leadcanary.ErrConfig) || !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("got %v, want ErrConfig wrapping ErrNotExist", err)
		}
	})
	t.Run("size", func(t *testing.T) {
		pad := func(n int) []byte {
			b := append([]byte(nil), good...)
			return append(b, bytes.Repeat([]byte{' '}, n-len(b))...)
		}
		p := filepath.Join(f.conf, "size.json")
		writeFile(t, p, pad(leadcanary.MaxConfigBytes), 0o600)
		if _, err := leadcanary.ReadConfig(p); err != nil {
			t.Fatalf("a file of exactly 64 KiB: %v", err)
		}
		writeFile(t, p, pad(leadcanary.MaxConfigBytes+1), 0o600)
		_, err := leadcanary.ReadConfig(p)
		wantConfigError(t, err, "file")
	})
}

func TestReadConfigContentFaults(t *testing.T) {
	f := newFixture(t)
	good, _ := json.Marshal(f.config(t))
	p := filepath.Join(f.conf, "canary.json")
	cases := []struct {
		name string
		data []byte
	}{
		{"empty", nil},
		{"not an object", []byte(`[]`)},
		{"not JSON", []byte(`{"version":`)},
		{"invalid UTF-8", append(append([]byte(nil), good[:len(good)-1]...), []byte(",\"x\":\"\xff\"}")...)},
		{"trailing data", append(append([]byte(nil), good...), []byte(` {}`)...)},
		{"trailing garbage", append(append([]byte(nil), good...), []byte(` x`)...)},
		{"unknown top-level field", bytes.Replace(good, []byte(`{"version"`), []byte(`{"extra":1,"version"`), 1)},
		{"unknown nested field", bytes.Replace(good, []byte(`"holder":{`), []byte(`"holder":{"extra":1,`), 1)},
		{"repeated key", bytes.Replace(good, []byte(`{"version":1,`), []byte(`{"version":1,"version":1,`), 1)},
		{"repeated nested key", bytes.Replace(good, []byte(`"limits":{`), []byte(`"limits":{"tasks_max":600,`), 1)},
		{"null", bytes.Replace(good, []byte(`"inject":true`), []byte(`"inject":null`), 1)},
		{"null in a nested object", bytes.Replace(good, []byte(`"upstream_branch":"main"`), []byte(`"upstream_branch":null`), 1)},
		{"version 2", bytes.Replace(good, []byte(`"version":1`), []byte(`"version":2`), 1)},
		{"version as text", bytes.Replace(good, []byte(`"version":1`), []byte(`"version":"1"`), 1)},
		{"version as fraction", bytes.Replace(good, []byte(`"version":1`), []byte(`"version":1.5`), 1)},
		{"lone surrogate in a text", bytes.Replace(good, []byte(`"main"`), []byte(`"ma\ud800in"`), 1)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if bytes.Equal(c.data, good) {
				t.Fatal("the case did not change the config")
			}
			writeFile(t, p, c.data, 0o600)
			_, err := leadcanary.ReadConfig(p)
			wantConfigError(t, err, "")
		})
	}
}

// leafPaths lists the paths of the leaves of a generic tree that lie in
// objects only (an array is a leaf).
func leafPaths(prefix string, m map[string]any) []string {
	var out []string
	for k, v := range m {
		p := k
		if prefix != "" {
			p = prefix + "." + k
		}
		if sub, ok := v.(map[string]any); ok {
			out = append(out, leafPaths(p, sub)...)
		} else {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

func deleteAt(m map[string]any, path string) {
	parts := strings.Split(path, ".")
	for _, k := range parts[:len(parts)-1] {
		m = m[k].(map[string]any)
	}
	delete(m, parts[len(parts)-1])
}

func TestReadConfigEveryKeyIsRequired(t *testing.T) {
	f := newFixture(t)
	paths := leafPaths("", tree(t, f.config(t)))
	if len(paths) < 25 {
		t.Fatalf("only %d leaves: %v", len(paths), paths)
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			m := tree(t, f.config(t))
			deleteAt(m, path)
			_, err := leadcanary.ReadConfig(f.writeTree(t, m))
			if path == "agent.env" {
				if err != nil {
					t.Fatalf("agent.env may be absent: %v", err)
				}
				return
			}
			wantConfigError(t, err, path)
		})
	}
}

func setAt(m map[string]any, path string, v any) {
	parts := strings.Split(path, ".")
	for _, k := range parts[:len(parts)-1] {
		m = m[k].(map[string]any)
	}
	m[parts[len(parts)-1]] = v
}

func TestReadConfigValueFaults(t *testing.T) {
	f := newFixture(t)
	long := strings.Repeat("x", 65)
	cases := []struct {
		field string
		value any
	}{
		{"role", "demo/lead"},
		{"role", "supervisor"},
		{"role", "throwaway-one/coord/feature"},
		{"role", "throwaway-/lead"},
		{"role", "Throwaway-one/lead"},
		{"role", 7},
		{"role", "throwaway-" + strings.Repeat("a", 53) + "/lead"}, // layout name over 63 bytes
		{"state_root", "relative/state"},
		{"state_root", "/unclean/../state"},
		{"state_root", "/trailing/"},
		{"state_root", "/"},
		{"state_root", "/with\ncontrol"},
		{"state_root", "/" + strings.Repeat("a", 4096)},
		{"runtime_root", "runtime"},
		{"holder.executable", "holder"},
		{"holder.executable", "/opt/./holder"},
		{"holder.cols", 19},
		{"holder.cols", 501},
		{"holder.cols", -1},
		{"holder.rows", 4},
		{"holder.rows", 201},
		{"holder.sandbox_inputs", "inputs.json"},
		{"holder.profile_digest", strings.Repeat("AB", 32)},
		{"holder.profile_digest", strings.Repeat("ab", 31)},
		{"holder.profile_digest", strings.Repeat("zz", 32)},
		{"agent.kind", "real"},
		{"agent.kind", ""},
		{"agent.runtime", ""},
		{"agent.runtime", long},
		{"agent.runtime", "--"},
		{"agent.runtime", "has\nnewline"},
		{"agent.runtime_version", ""},
		{"agent.runtime_version", long},
		{"agent.footer_mode", ""},
		{"agent.footer_mode", "--"},
		{"agent.footer_mode", "tab\there"},
		{"agent.argv", []string{}},
		{"agent.argv", []string{"agent"}},
		{"agent.argv", []string{"/opt/sample/agent", "bad\x00arg"}},
		{"agent.env", []string{"NOEQUALS"}},
		{"agent.env", []string{"1BAD=x"}},
		{"agent.env", []string{"A=1", "A=2"}},
		{"agent.env", []string{"A=bad\nvalue"}},
		{"limits.memory_max_bytes", leadcanary.MinMemoryMax - 1},
		{"limits.memory_max_bytes", leadcanary.MaxMemoryMax + 1},
		{"limits.memory_max_bytes", 0},
		{"limits.swap_max_bytes", leadcanary.MaxSwapMax + 1},
		{"limits.swap_max_bytes", -1}, // a negative number is not a uint64: refused as a file fault
		{"limits.tasks_max", leadcanary.MinTasksMax - 1},
		{"limits.tasks_max", leadcanary.MaxTasksMax + 1},
		{"limits.tasks_max", 0},
		{"limits.memory_floor_bytes", leadcanary.MinMemoryMax - 1},
		{"limits.memory_floor_bytes", (4 << 30) + 1}, // above the ceiling of the sample
		{"limits.memory_floor_bytes", 0},
		{"limits.memory_share_percent", 0},
		{"limits.memory_share_percent", 101},
		{"limits.memory_share_percent", -5},
		{"provision.mirror", "mirror.git"},
		{"provision.upstream_branch", ""},
		{"provision.upstream_branch", "-leading"},
		{"provision.upstream_branch", "a..b"},
		{"provision.upstream_branch", "has space"},
		{"provision.upstream_branch", "trailing/"},
		{"provision.git_name", ""},
		{"provision.git_name", "A <B>"},
		{"provision.git_email", "no-at-sign"},
		{"provision.git_email", "two@@signs"},
		{"provision.git_email", "has space@example.invalid"},
		{"provision.managed_settings", "settings.json"},
		{"provision.guard_hook", "guard"},
		{"provision.replay_screens", "screens"},
	}
	for _, c := range cases {
		name := c.field + "=" + toJSON(c.value)
		if len(name) > 80 {
			name = name[:80]
		}
		t.Run(name, func(t *testing.T) {
			m := tree(t, f.config(t))
			setAt(m, c.field, c.value)
			_, err := leadcanary.ReadConfig(f.writeTree(t, m))
			field := c.field
			if strings.HasPrefix(field, "agent.argv") {
				field = "" // the failing element is named, not the array
			}
			if strings.HasPrefix(field, "agent.env") {
				field = ""
			}
			if strings.HasPrefix(c.field, "limits.") && c.field != "limits.memory_share_percent" && isNegative(c.value) {
				field = "file"
			}
			wantConfigError(t, err, field)
		})
	}
}

func isNegative(v any) bool {
	n, ok := v.(int)
	return ok && n < 0
}

func toJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestLimitsRequiredBoundedNotDefaulted(t *testing.T) {
	f := newFixture(t)
	// The bounds, as the task states them.
	if leadcanary.MinMemoryMax != 256<<20 || leadcanary.MaxMemoryMax != 64<<30 ||
		leadcanary.MinTasksMax != 16 || leadcanary.MaxTasksMax != 65536 ||
		leadcanary.MinSwapMax != 0 || leadcanary.MaxSwapMax != 64<<30 {
		t.Fatal("a unit bound is not the stated number")
	}
	// A zero Config has no limits: nothing is defaulted in code.
	var zero leadcanary.Limits
	cfg := f.config(t)
	cfg.Limits = zero
	if err := cfg.Validate(); err == nil {
		t.Fatal("a config with zero limits is valid")
	}
	// Each bound is accepted at its edge.
	edge := func(mut func(*leadcanary.Limits)) {
		t.Helper()
		c := f.config(t)
		mut(&c.Limits)
		if err := c.Validate(); err != nil {
			t.Fatalf("%+v: %v", c.Limits, err)
		}
	}
	edge(func(l *leadcanary.Limits) {
		l.MemoryMaxBytes, l.MemoryFloorBytes = leadcanary.MinMemoryMax, leadcanary.MinMemoryMax
	})
	edge(func(l *leadcanary.Limits) { l.MemoryMaxBytes = leadcanary.MaxMemoryMax })
	edge(func(l *leadcanary.Limits) { l.TasksMax = leadcanary.MinTasksMax })
	edge(func(l *leadcanary.Limits) { l.TasksMax = leadcanary.MaxTasksMax })
	edge(func(l *leadcanary.Limits) { l.SwapMaxBytes = 0 })
	edge(func(l *leadcanary.Limits) { l.SwapMaxBytes = leadcanary.MaxSwapMax })
	edge(func(l *leadcanary.Limits) { l.MemorySharePercent = 1 })
	edge(func(l *leadcanary.Limits) { l.MemorySharePercent = 100 })
	edge(func(l *leadcanary.Limits) { l.MemoryFloorBytes = l.MemoryMaxBytes })
	// A file that omits one limit is refused, and the swap limit of 0 is a
	// value that must be written.
	for _, k := range []string{"memory_max_bytes", "swap_max_bytes", "tasks_max", "memory_floor_bytes", "memory_share_percent"} {
		m := tree(t, f.config(t))
		delete(m["limits"].(map[string]any), k)
		_, err := leadcanary.ReadConfig(f.writeTree(t, m))
		wantConfigError(t, err, "limits."+k)
	}
	m := tree(t, f.config(t))
	delete(m, "limits")
	_, err := leadcanary.ReadConfig(f.writeTree(t, m))
	wantConfigError(t, err, "limits.memory_max_bytes")
}

func TestReadConfigRefusesALocationInsideARoot(t *testing.T) {
	f := newFixture(t)
	cfg := f.config(t)

	t.Run("inside the state root", func(t *testing.T) {
		data, _ := json.Marshal(cfg)
		p := filepath.Join(f.state, "canary.json")
		writeFile(t, p, data, 0o600)
		_, err := leadcanary.ReadConfig(p)
		wantConfigError(t, err, "file")
	})
	t.Run("inside the runtime root, deeper", func(t *testing.T) {
		data, _ := json.Marshal(cfg)
		deep := filepath.Join(f.runtime, "a", "b")
		if err := os.MkdirAll(deep, 0o700); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(deep, "canary.json")
		writeFile(t, p, data, 0o600)
		_, err := leadcanary.ReadConfig(p)
		wantConfigError(t, err, "file")
	})
	t.Run("reached through a link into a root", func(t *testing.T) {
		data, _ := json.Marshal(cfg)
		writeFile(t, filepath.Join(f.state, "via-link.json"), data, 0o600)
		dirLink := filepath.Join(f.dir, "state-link")
		if err := os.Symlink(f.state, dirLink); err != nil {
			t.Fatal(err)
		}
		_, err := leadcanary.ReadConfig(filepath.Join(dirLink, "via-link.json"))
		wantConfigError(t, err, "file")
	})
	t.Run("a root that is a link to the config's folder", func(t *testing.T) {
		rootLink := filepath.Join(f.dir, "root-link")
		if err := os.Symlink(f.conf, rootLink); err != nil {
			t.Fatal(err)
		}
		c := cfg
		c.StateRoot = rootLink
		_, err := leadcanary.ReadConfig(f.writeConfig(t, c))
		wantConfigError(t, err, "file")
	})
	t.Run("a root that does not exist yet", func(t *testing.T) {
		c := cfg
		c.StateRoot = filepath.Join(f.conf, "not-yet", "state")
		if _, err := leadcanary.ReadConfig(f.writeConfig(t, c)); err != nil {
			t.Fatalf("a root that is not made yet, below the config's folder: %v", err)
		}
		// The config's own folder as a root that is made later is refused.
		c.StateRoot = f.conf
		_, err := leadcanary.ReadConfig(f.writeConfig(t, c))
		wantConfigError(t, err, "file")
	})
	t.Run("a sibling whose name starts like a root", func(t *testing.T) {
		sib := f.state + "-more"
		if err := os.Mkdir(sib, 0o700); err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(cfg)
		p := filepath.Join(sib, "canary.json")
		writeFile(t, p, data, 0o600)
		if _, err := leadcanary.ReadConfig(p); err != nil {
			t.Fatalf("a folder beside the root is outside it: %v", err)
		}
	})
}

func TestConfigErrorsRepeatNoValue(t *testing.T) {
	f := newFixture(t)
	const secret = "sentinel-value-that-must-not-be-echoed"
	m := tree(t, f.config(t))
	setAt(m, "agent.kind", secret)
	_, err := leadcanary.ReadConfig(f.writeTree(t, m))
	wantConfigError(t, err, "agent.kind")
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("the error repeats a value: %v", err)
	}
	m = tree(t, f.config(t))
	m[secret] = 1
	_, err = leadcanary.ReadConfig(f.writeTree(t, m))
	wantConfigError(t, err, "")
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("the error repeats a key: %v", err)
	}
}

func TestAgentKinds(t *testing.T) {
	f := newFixture(t)
	for _, k := range []leadcanary.AgentKind{leadcanary.AgentStandIn, leadcanary.AgentLive} {
		c := f.config(t)
		c.Agent.Kind = k
		if k == leadcanary.AgentLive {
			c.Agent.FooterMode = "manual"
			c.Agent.Argv = []string{"/ataniq/agent/claude"}
		}
		if err := c.Validate(); err != nil {
			t.Fatalf("%s: %v", k, err)
		}
	}
	if leadcanary.AgentStandIn != "stand-in" || leadcanary.AgentLive != "live" {
		t.Fatal("the kinds are stand-in and live")
	}
}

// A live agent with inject on is held to the recogniser's names of the mode.
func TestLiveFooterModeValues(t *testing.T) {
	f := newFixture(t)
	live := func(mode string, inject bool) leadcanary.Config {
		c := f.config(t)
		c.Agent.Kind = leadcanary.AgentLive
		c.Agent.FooterMode = mode
		c.Agent.Argv = []string{"/ataniq/agent/claude"}
		c.Inject = inject
		return c
	}
	for _, ok := range []string{"manual", "auto"} {
		if err := live(ok, true).Validate(); err != nil {
			t.Fatalf("%s: %v", ok, err)
		}
	}
	for _, mode := range []string{"manual mode on", "sample mode on", "Manual", "auto mode on (shift+tab to cycle)", "plan"} {
		err := live(mode, true).Validate()
		wantConfigError(t, err, "agent.footer_mode")
		if !strings.Contains(err.Error(), `"manual"`) || !strings.Contains(err.Error(), `"auto"`) {
			t.Fatalf("%q: the message does not name the two values: %v", mode, err)
		}
	}
	// With inject off nothing is injected, so no mode is read; the stand-in is
	// never held to it.
	if err := live("sample mode on", false).Validate(); err != nil {
		t.Fatalf("inject off: %v", err)
	}
	c := f.config(t)
	c.Agent.FooterMode = "sample mode on"
	if err := c.Validate(); err != nil {
		t.Fatalf("stand-in: %v", err)
	}
}

// A live agent's command is the file in the install folder, under the bind.
func TestLiveArgv0UnderTheBind(t *testing.T) {
	f := newFixture(t)
	for _, argv0 := range []string{"/usr/bin/claude", "/ataniq/agent", "/ataniq/agent/", "/ataniq/agentx/claude", "/ataniq/agent/../x"} {
		c := f.config(t)
		c.Agent.Kind = leadcanary.AgentLive
		c.Agent.FooterMode = "manual"
		c.Agent.Argv = []string{argv0}
		wantConfigError(t, c.Validate(), "agent.argv[0]")
	}
	c := f.config(t)
	c.Agent.Kind = leadcanary.AgentLive
	c.Agent.FooterMode = "manual"
	c.Agent.Argv = []string{"/ataniq/agent/bin/claude", "--model", "x"}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestConfigHelpers(t *testing.T) {
	f := newFixture(t)
	c := f.config(t)
	l, err := c.Layout()
	if err != nil {
		t.Fatal(err)
	}
	if l.Role != "throwaway-one-lead" || l.Project != "throwaway-one" || l.StateRoot != f.state {
		t.Fatalf("layout %+v", l)
	}
	d, err := c.HolderRuntimeDir()
	if err != nil || d != filepath.Join(f.runtime, "leads", "throwaway-one-lead") {
		t.Fatalf("runtime dir %q, %v", d, err)
	}
}
