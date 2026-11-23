//go:build linux && ataniq_canary

package leadcanary_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ataniq/internal/leadcanary"
	"ataniq/internal/leadrole"
)

// scratchSample returns a valid scratch config of two leads whose paths lie in
// the fixture.
func (f fixture) scratchSample() leadcanary.ScratchConfig {
	agent := func(runtime string) leadcanary.AgentConfig {
		return leadcanary.AgentConfig{
			Kind: leadcanary.AgentStandIn, Runtime: runtime, RuntimeVersion: "1.2.3",
			FooterMode: "manual", Argv: []string{"/ataniq/agent/sample-standin"},
		}
	}
	limits := leadcanary.Limits{
		MemoryMaxBytes: 4 << 30, SwapMaxBytes: 0, TasksMax: 512,
		MemoryFloorBytes: 1 << 30, MemorySharePercent: 50,
	}
	return leadcanary.ScratchConfig{
		Version: 1, StateRoot: f.state, RuntimeRoot: f.runtime, MaxRunning: 2,
		Holder: leadcanary.ScratchHolder{Executable: "/opt/sample/ataniq-holder", Cols: 120, Rows: 40},
		Sandbox: leadcanary.ScratchSandbox{
			AgentInstall: "/srv/scratch/agent-install", Pasta: "/srv/scratch/bin/pasta",
			Bwrap: "/usr/bin/bwrap", Probe: "/srv/scratch/bin/ataniq-probe",
			ManagedMarker: "guard-hook", NeverMounted: []string{"/srv/checkout"},
			ProviderHost:     "provider.example.invalid",
			Terminal:         leadcanary.ScratchTerminal{Term: "xterm-256color", ColorTerm: "truecolor", Lang: "C.UTF-8"},
			TimeLimitSeconds: 60,
		},
		Provision: leadcanary.ScratchProvision{
			GitName: "Sample Lead", GitEmail: "lead@example.invalid",
			ManagedSettings: "/srv/scratch/conf/managed-settings.json",
			GuardHook:       "/srv/scratch/conf/guard-hook", ReplayScreens: "/srv/scratch/conf/replay-screens",
		},
		Entries: []leadcanary.ScratchLead{
			{Key: "alpha", Source: "/srv/scratch/upstream/alpha.git", UpstreamBranch: "develop",
				Agent: agent("claude"), Inject: true, Limits: limits},
			{Key: "beta-2", Source: "/srv/scratch/upstream/beta.git", UpstreamBranch: "main",
				Agent: agent("claude"), Inject: false, Limits: limits, ProfileDigest: sampleDigest},
		},
	}
}

func scratchTree(t testing.TB, s leadcanary.ScratchConfig) map[string]any {
	t.Helper()
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func (f fixture) writeScratch(t testing.TB, data []byte) string {
	t.Helper()
	p := filepath.Join(f.conf, "scratch.json")
	writeFile(t, p, data, 0o600)
	return p
}

func (f fixture) writeScratchTree(t testing.TB, m map[string]any) string {
	t.Helper()
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return f.writeScratch(t, data)
}

func TestReadScratchConfigAndDerive(t *testing.T) {
	f := newFixture(t)
	want := f.scratchSample()
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	s, err := leadcanary.ReadScratchConfig(f.writeScratch(t, data))
	if err != nil {
		t.Fatal(err)
	}
	leads, err := s.Leads()
	if err != nil {
		t.Fatal(err)
	}
	if len(leads) != 2 {
		t.Fatalf("%d leads", len(leads))
	}
	a, b := leads[0], leads[1]
	if a.Role.String() != "scratch-alpha/lead" || b.Role.String() != "scratch-beta-2/lead" {
		t.Fatalf("roles %v, %v", a.Role, b.Role)
	}
	for _, c := range leads {
		if c.Reserved != leadrole.ReservedScratch || c.Version != 1 {
			t.Errorf("%v: reserved %v version %d", c.Role, c.Reserved, c.Version)
		}
		if c.StateRoot != f.state || c.RuntimeRoot != f.runtime || c.Holder.Executable != "/opt/sample/ataniq-holder" ||
			c.Holder.Cols != 120 || c.Holder.Rows != 40 {
			t.Errorf("%v: shared values %+v", c.Role, c)
		}
		if c.Provision.GitName != "Sample Lead" || c.Provision.GuardHook != "/srv/scratch/conf/guard-hook" {
			t.Errorf("%v: provision %+v", c.Role, c.Provision)
		}
		if err := c.Validate(); err != nil {
			t.Errorf("%v: derived config does not validate: %v", c.Role, err)
		}
	}
	if got, want := a.Holder.SandboxInputs, filepath.Join(f.state, "scratch", "scratch-alpha-lead", "sandbox-inputs.json"); got != want {
		t.Errorf("sandbox inputs %q, want %q", got, want)
	}
	if got, want := a.Provision.Mirror, filepath.Join(f.state, "mirrors", "scratch-alpha.git"); got != want {
		t.Errorf("mirror %q, want %q", got, want)
	}
	if a.Provision.Source != "/srv/scratch/upstream/alpha.git" || a.Provision.UpstreamBranch != "develop" || !a.Inject || b.Inject {
		t.Errorf("per-lead values: %+v / %+v", a.Provision, b.Provision)
	}
	// The pin: absent gives the placeholder and no pin, present is kept.
	if a.Holder.ProfileDigest != leadcanary.UnpinnedDigest {
		t.Errorf("alpha digest %q", a.Holder.ProfileDigest)
	}
	if _, ok := a.PinnedDigest(); ok {
		t.Error("alpha reports a pin")
	}
	if d, ok := b.PinnedDigest(); !ok || d != sampleDigest {
		t.Errorf("beta pin %q, %v", d, ok)
	}
	if _, ok := f.config(t).PinnedDigest(); ok {
		t.Error("a canary config reports a pin")
	}
	// Each lead has its own evidence file.
	if a.EvidencePath() == b.EvidencePath() {
		t.Error("two leads share an evidence file")
	}
}

func TestWithProfileDigest(t *testing.T) {
	f := newFixture(t)
	leads, err := f.scratchSample().Leads()
	if err != nil {
		t.Fatal(err)
	}
	d := strings.Repeat("0123456789abcdef", 4)
	c, err := leads[0].WithProfileDigest(d)
	if err != nil || c.Holder.ProfileDigest != d {
		t.Fatalf("WithProfileDigest: %v, %q", err, c.Holder.ProfileDigest)
	}
	if leads[0].Holder.ProfileDigest != leadcanary.UnpinnedDigest {
		t.Error("WithProfileDigest changed the receiver")
	}
	if p, ok := c.PinnedDigest(); !ok || p != d {
		t.Errorf("after the digest is set the config reports pin %q, %v", p, ok)
	}
	for _, bad := range []string{"", "abc", strings.ToUpper(d), d + "0", strings.Repeat("g", 64)} {
		if _, err := leads[0].WithProfileDigest(bad); err == nil {
			t.Errorf("accepted the digest %q", bad)
		}
	}
}

// jsonLeaves returns the path of every leaf of a JSON tree. An array is a leaf
// unless it holds objects, whose elements are walked as "name[i]".
func jsonLeaves(node any, prefix string, out *[]string) {
	switch v := node.(type) {
	case map[string]any:
		for k, c := range v {
			p := k
			if prefix != "" {
				p = prefix + "." + k
			}
			jsonLeaves(c, p, out)
		}
	case []any:
		if len(v) > 0 {
			if _, ok := v[0].(map[string]any); ok {
				for i, c := range v {
					jsonLeaves(c, prefix+"["+string(rune('0'+i))+"]", out)
				}
				return
			}
		}
		*out = append(*out, prefix)
	default:
		*out = append(*out, prefix)
	}
}

// deletePath removes the leaf at a path made by leafPaths.
func deletePath(t *testing.T, tree map[string]any, path string) {
	t.Helper()
	var cur any = tree
	parts := strings.Split(path, ".")
	for i, p := range parts {
		last := i == len(parts)-1
		name, idx := p, -1
		if open := strings.IndexByte(p, '['); open >= 0 {
			name, idx = p[:open], int(p[open+1]-'0')
		}
		m := cur.(map[string]any)
		if last && idx < 0 {
			delete(m, name)
			return
		}
		next := m[name]
		if idx >= 0 {
			next = next.([]any)[idx]
		}
		cur = next
	}
	t.Fatalf("path %q was not deleted", path)
}

// TestEveryRequiredKeyIsRequired removes each key of the valid config in turn
// and holds that the read refuses it by name, except the optional ones.
func TestEveryRequiredKeyIsRequired(t *testing.T) {
	f := newFixture(t)
	optional := map[string]bool{
		"sandbox.managed_marker": true, "sandbox.terminal.colorterm": true,
		"sandbox.terminal.lang": true, "sandbox.terminal.tz": true,
		"leads[1].profile_digest": true,
	}
	var paths []string
	jsonLeaves(scratchTree(t, f.scratchSample()), "", &paths)
	if len(paths) < 40 {
		t.Fatalf("only %d keys found: %v", len(paths), paths)
	}
	for _, p := range paths {
		t.Run(p, func(t *testing.T) {
			m := scratchTree(t, f.scratchSample())
			deletePath(t, m, p)
			_, err := leadcanary.ReadScratchConfig(f.writeScratchTree(t, m))
			if optional[p] {
				if err != nil {
					t.Fatalf("an optional key is required: %v", err)
				}
				return
			}
			wantConfigError(t, err, p)
		})
	}
}

func TestReadScratchConfigRefusals(t *testing.T) {
	f := newFixture(t)
	type edit func(m map[string]any)
	leadOf := func(m map[string]any, i int) map[string]any { return m["leads"].([]any)[i].(map[string]any) }
	cases := map[string]struct {
		edit  edit
		field string
	}{
		"version":                {func(m map[string]any) { m["version"] = 2 }, "version"},
		"state root relative":    {func(m map[string]any) { m["state_root"] = "state" }, "state_root"},
		"runtime root unclean":   {func(m map[string]any) { m["runtime_root"] = "/srv/a/../b" }, "runtime_root"},
		"max running zero":       {func(m map[string]any) { m["max_running"] = 0 }, "max_running"},
		"max running over":       {func(m map[string]any) { m["max_running"] = 9 }, "max_running"},
		"holder relative":        {func(m map[string]any) { m["holder"].(map[string]any)["executable"] = "holder" }, "holder.executable"},
		"holder cols":            {func(m map[string]any) { m["holder"].(map[string]any)["cols"] = 5 }, "holder.cols"},
		"holder rows":            {func(m map[string]any) { m["holder"].(map[string]any)["rows"] = 500 }, "holder.rows"},
		"two executables equal":  {func(m map[string]any) { m["sandbox"].(map[string]any)["bwrap"] = "/srv/scratch/bin/pasta" }, "sandbox"},
		"probe relative":         {func(m map[string]any) { m["sandbox"].(map[string]any)["probe"] = "probe" }, "sandbox.probe"},
		"never mounted empty":    {func(m map[string]any) { m["sandbox"].(map[string]any)["never_mounted"] = []any{} }, "sandbox.never_mounted"},
		"never mounted relative": {func(m map[string]any) { m["sandbox"].(map[string]any)["never_mounted"] = []any{"x"} }, "sandbox.never_mounted[0]"},
		"provider host space":    {func(m map[string]any) { m["sandbox"].(map[string]any)["provider_host"] = "a b" }, "sandbox.provider_host"},
		"term empty":             {func(m map[string]any) { m["sandbox"].(map[string]any)["terminal"].(map[string]any)["term"] = "" }, "sandbox.terminal.term"},
		"time limit":             {func(m map[string]any) { m["sandbox"].(map[string]any)["time_limit_seconds"] = 3601 }, "sandbox.time_limit_seconds"},
		"git email":              {func(m map[string]any) { m["provision"].(map[string]any)["git_email"] = "nobody" }, "provision.git_email"},
		"settings relative":      {func(m map[string]any) { m["provision"].(map[string]any)["managed_settings"] = "m.json" }, "provision.managed_settings"},
		"no leads":               {func(m map[string]any) { m["leads"] = []any{} }, "leads"},
		"too many leads": {func(m map[string]any) {
			var l []any
			for i := 0; i < 9; i++ {
				c := map[string]any{}
				b, _ := json.Marshal(leadOf(m, 0))
				_ = json.Unmarshal(b, &c)
				c["key"] = "k" + string(rune('a'+i))
				l = append(l, c)
			}
			m["leads"] = l
		}, "leads"},
		"key upper case":    {func(m map[string]any) { leadOf(m, 0)["key"] = "Alpha" }, "leads[0].key"},
		"key empty":         {func(m map[string]any) { leadOf(m, 0)["key"] = "" }, "leads[0].key"},
		"key starts hyphen": {func(m map[string]any) { leadOf(m, 0)["key"] = "-a" }, "leads[0].key"},
		"key too long":      {func(m map[string]any) { leadOf(m, 0)["key"] = strings.Repeat("a", 51) }, "leads[0].key"},
		"key slash":         {func(m map[string]any) { leadOf(m, 0)["key"] = "a/b" }, "leads[0].key"},
		"key repeated":      {func(m map[string]any) { leadOf(m, 1)["key"] = "alpha" }, "leads[1].key"},
		"key ends hyphen":   {func(m map[string]any) { leadOf(m, 0)["key"] = "a-" }, "leads[0].key"},
		"source relative":   {func(m map[string]any) { leadOf(m, 0)["source"] = "up/alpha.git" }, "leads[0].provision.source"},
		"branch":            {func(m map[string]any) { leadOf(m, 0)["upstream_branch"] = "a..b" }, "leads[0].provision.upstream_branch"},
		"agent kind":        {func(m map[string]any) { leadOf(m, 1)["agent"].(map[string]any)["kind"] = "other" }, "leads[1].agent.kind"},
		"limit":             {func(m map[string]any) { leadOf(m, 0)["limits"].(map[string]any)["tasks_max"] = 0 }, "leads[0].limits.tasks_max"},
		"digest short":      {func(m map[string]any) { leadOf(m, 1)["profile_digest"] = "abc" }, "leads[1].profile_digest"},
		"digest upper":      {func(m map[string]any) { leadOf(m, 1)["profile_digest"] = strings.ToUpper(sampleDigest) }, "leads[1].profile_digest"},
		"digest zeros":      {func(m map[string]any) { leadOf(m, 1)["profile_digest"] = leadcanary.UnpinnedDigest }, "leads[1].profile_digest"},
		"live agent no mode": {func(m map[string]any) {
			a := leadOf(m, 0)["agent"].(map[string]any)
			a["kind"], a["argv"], a["footer_mode"] = "live", []any{"/ataniq/agent/claude"}, "some text"
		}, "leads[0].agent.footer_mode"},
		"unknown key":       {func(m map[string]any) { m["extra"] = 1 }, "file"},
		"unknown in lead":   {func(m map[string]any) { leadOf(m, 0)["extra"] = 1 }, "file"},
		"null":              {func(m map[string]any) { m["sandbox"].(map[string]any)["managed_marker"] = nil }, "sandbox.managed_marker"},
		"wrong type":        {func(m map[string]any) { m["max_running"] = "2" }, "file"},
		"lead not object":   {func(m map[string]any) { m["leads"] = []any{"alpha"} }, "file"},
		"digest wrong type": {func(m map[string]any) { leadOf(m, 1)["profile_digest"] = 7 }, "file"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			m := scratchTree(t, f.scratchSample())
			c.edit(m)
			_, err := leadcanary.ReadScratchConfig(f.writeScratchTree(t, m))
			wantConfigError(t, err, c.field)
		})
	}
}

func TestReadScratchConfigRefusesBadText(t *testing.T) {
	f := newFixture(t)
	good, err := json.Marshal(f.scratchSample())
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string][]byte{
		"empty":                 nil,
		"not json":              []byte("version: 1"),
		"trailing data":         append(append([]byte(nil), good...), []byte(" {}")...),
		"a key twice":           []byte(strings.Replace(string(good), `"max_running":2`, `"max_running":2,"max_running":2`, 1)),
		"a key twice in a lead": []byte(strings.Replace(string(good), `"key":"alpha"`, `"key":"alpha","key":"alpha"`, 1)),
		"invalid utf-8":         append(append([]byte(nil), good[:len(good)-1]...), 0xff, '}'),
		"an array":              []byte("[]"),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := leadcanary.ReadScratchConfig(f.writeScratch(t, data))
			wantConfigError(t, err, "")
		})
	}
}

func TestReadScratchConfigFileRules(t *testing.T) {
	f := newFixture(t)
	good, err := json.Marshal(f.scratchSample())
	if err != nil {
		t.Fatal(err)
	}
	t.Run("mode", func(t *testing.T) {
		p := f.writeScratch(t, good)
		if err := os.Chmod(p, 0o640); err != nil {
			t.Fatal(err)
		}
		_, err := leadcanary.ReadScratchConfig(p)
		wantConfigError(t, err, "file")
	})
	t.Run("a link at the name", func(t *testing.T) {
		real := f.writeScratch(t, good)
		link := filepath.Join(f.conf, "link.json")
		if err := os.Symlink(real, link); err != nil {
			t.Fatal(err)
		}
		if _, err := leadcanary.ReadScratchConfig(link); err == nil {
			t.Fatal("followed a symbolic link at the last element")
		}
	})
	t.Run("two links", func(t *testing.T) {
		p := f.writeScratch(t, good)
		other := filepath.Join(f.conf, "other.json")
		if err := os.Link(p, other); err != nil {
			t.Skipf("hard links are not available here: %v", err)
		}
		_, err := leadcanary.ReadScratchConfig(p)
		wantConfigError(t, err, "file")
		if err := os.Remove(other); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("too large", func(t *testing.T) {
		big := append(append([]byte(nil), good...), []byte(strings.Repeat(" ", leadcanary.MaxConfigBytes))...)
		_, err := leadcanary.ReadScratchConfig(f.writeScratch(t, big))
		wantConfigError(t, err, "file")
	})
	t.Run("not a file", func(t *testing.T) {
		_, err := leadcanary.ReadScratchConfig(f.conf)
		if err == nil {
			t.Fatal("read a folder")
		}
	})
	t.Run("missing", func(t *testing.T) {
		_, err := leadcanary.ReadScratchConfig(filepath.Join(f.conf, "absent.json"))
		if !errors.Is(err, leadcanary.ErrConfig) || !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("got %v", err)
		}
	})
	for _, root := range []string{"state", "runtime"} {
		t.Run("inside the "+root+" root", func(t *testing.T) {
			s := f.scratchSample()
			dir := f.state
			if root == "runtime" {
				dir = f.runtime
			}
			data, _ := json.Marshal(s)
			p := filepath.Join(dir, "scratch.json")
			writeFile(t, p, data, 0o600)
			_, err := leadcanary.ReadScratchConfig(p)
			wantConfigError(t, err, "file")
		})
	}
}

// TestScratchConfigErrorsHoldNoValue holds that a refusal never repeats a value
// of the file.
func TestScratchConfigErrorsHoldNoValue(t *testing.T) {
	f := newFixture(t)
	m := scratchTree(t, f.scratchSample())
	m["leads"].([]any)[0].(map[string]any)["source"] = "up/sample-secret-text"
	_, err := leadcanary.ReadScratchConfig(f.writeScratchTree(t, m))
	wantConfigError(t, err, "")
	if strings.Contains(err.Error(), "sample-secret-text") {
		t.Fatalf("the refusal repeats a value: %v", err)
	}
}

// TestScratchConfigCheckRoots holds that the two roots are checked as the
// canary's are, with no marker.
func TestScratchConfigCheckRoots(t *testing.T) {
	f := newFixture(t)
	s := f.scratchSample()
	if err := s.CheckRoots(func(string) string { return "" }); err != nil {
		t.Fatalf("private roots: %v", err)
	}
	s.RuntimeRoot = filepath.Join(f.state, "inside")
	if err := s.CheckRoots(func(string) string { return "" }); err == nil {
		t.Fatal("nested roots were accepted")
	}
	home := t.TempDir()
	s = f.scratchSample()
	s.StateRoot = filepath.Join(home, ".local", "state", "ataniq", "sample")
	err := s.CheckRoots(func(k string) string {
		if k == "HOME" {
			return home
		}
		return ""
	})
	if err == nil {
		t.Fatal("a state root inside the default state folder was accepted")
	}
}

// TestKeyBoundsFit holds that the longest key makes the longest layout name that
// the layout accepts, and no longer one does.
func TestKeyBoundsFit(t *testing.T) {
	f := newFixture(t)
	s := f.scratchSample()
	s.Entries = s.Entries[:1]
	s.Entries[0].Key = strings.Repeat("a", leadcanary.MaxScratchKeyLen)
	leads, err := s.Leads()
	if err != nil {
		t.Fatalf("the longest key: %v", err)
	}
	layoutRole, _, err := leadcanary.NamesFor(leadrole.ReservedScratch, leads[0].Role)
	if err != nil || len(layoutRole) != 63 {
		t.Fatalf("layout role %q (%d bytes), %v", layoutRole, len(layoutRole), err)
	}
	// Eight leads is the most, and each gets its own folder names.
	s = f.scratchSample()
	for len(s.Entries) < leadcanary.MaxScratchLeads {
		l := s.Entries[0]
		l.Key = "lead-" + string(rune('a'+len(s.Entries)))
		l.ProfileDigest = ""
		s.Entries = append(s.Entries, l)
	}
	s.MaxRunning = leadcanary.MaxScratchLeads
	all, err := s.Leads()
	if err != nil || len(all) != leadcanary.MaxScratchLeads {
		t.Fatalf("eight leads: %d, %v", len(all), err)
	}
	seen := map[string]bool{}
	for _, c := range all {
		for _, p := range []string{c.EvidencePath(), c.Holder.SandboxInputs, c.Provision.Mirror} {
			if seen[p] {
				t.Fatalf("two leads share %s", p)
			}
			seen[p] = true
		}
	}
}

// record_screens is an optional boolean of a lead: absent and false derive no
// recording, true derives one; a value that is not a boolean, null and a
// misspelt key are refused as the siblings' are.
func TestScratchRecordScreens(t *testing.T) {
	f := newFixture(t)
	leadOf := func(m map[string]any, i int) map[string]any { return m["leads"].([]any)[i].(map[string]any) }
	read := func(edit func(m map[string]any)) ([]leadcanary.Config, error) {
		m := scratchTree(t, f.scratchSample())
		edit(m)
		s, err := leadcanary.ReadScratchConfig(f.writeScratchTree(t, m))
		if err != nil {
			return nil, err
		}
		return s.Leads()
	}
	for name, c := range map[string]struct {
		edit func(m map[string]any)
		want [2]bool
	}{
		"absent": {func(m map[string]any) {}, [2]bool{false, false}},
		"false":  {func(m map[string]any) { leadOf(m, 0)["record_screens"] = false }, [2]bool{false, false}},
		"true":   {func(m map[string]any) { leadOf(m, 1)["record_screens"] = true }, [2]bool{false, true}},
	} {
		t.Run(name, func(t *testing.T) {
			leads, err := read(c.edit)
			if err != nil {
				t.Fatal(err)
			}
			if got := [2]bool{leads[0].RecordScreens, leads[1].RecordScreens}; got != c.want {
				t.Fatalf("recording %v, want %v", got, c.want)
			}
			for _, l := range leads {
				if err := l.Validate(); err != nil {
					t.Fatalf("%v: %v", l.Role, err)
				}
			}
		})
	}
	if m := scratchTree(t, f.scratchSample()); leadOf(m, 0)["record_screens"] != nil {
		t.Error("a lead that does not record writes the key")
	}
	for name, c := range map[string]struct {
		edit  func(m map[string]any)
		field string
	}{
		"a string":    {func(m map[string]any) { leadOf(m, 0)["record_screens"] = "true" }, "file"},
		"a number":    {func(m map[string]any) { leadOf(m, 0)["record_screens"] = 1 }, "file"},
		"an object":   {func(m map[string]any) { leadOf(m, 0)["record_screens"] = map[string]any{} }, "file"},
		"null":        {func(m map[string]any) { leadOf(m, 0)["record_screens"] = nil }, "leads[0].record_screens"},
		"misspelt":    {func(m map[string]any) { leadOf(m, 0)["record_screen"] = true }, "file"},
		"at the root": {func(m map[string]any) { m["record_screens"] = true }, "file"},
		"in holder":   {func(m map[string]any) { m["holder"].(map[string]any)["record_screens"] = true }, "file"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := read(c.edit)
			wantConfigError(t, err, c.field)
		})
	}
}
