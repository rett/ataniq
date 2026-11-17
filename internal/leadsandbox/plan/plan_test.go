package plan

import (
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ataniq/internal/leadsandbox/profile"
	"ataniq/internal/leadsandbox/sandboxnet"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata")

// absent lists the optional rows the host lacks in the "optional absent" cases.
var absent = []string{"/lib64", "/etc/ssl", "/etc/nsswitch.conf"}

func without(m map[string]bool, rows ...string) map[string]bool {
	out := map[string]bool{}
	for k, v := range m {
		out[k] = v
	}
	for _, r := range rows {
		delete(out, r)
	}
	return out
}

func TestGolden(t *testing.T) {
	cases := []struct {
		name  string
		build func(t *testing.T) *Plan
	}{
		{"claude-root-plain", func(t *testing.T) *Plan {
			in := fixture(profile.Claude)
			return mustBuild(t, in, options(in, allPresent(t, in)))
		}},
		{"claude-root-optional-absent", func(t *testing.T) *Plan {
			in := fixture(profile.Claude)
			return mustBuild(t, in, options(in, without(allPresent(t, in), absent...)))
		}},
		{"claude-owner-seccomp", func(t *testing.T) *Plan {
			in := withSeccomp(ownerMode(fixture(profile.Claude)))
			return mustBuild(t, in, options(in, allPresent(t, in)))
		}},
		{"codex-root-plain", func(t *testing.T) *Plan {
			in := fixture(profile.Codex)
			o := options(in, allPresent(t, in))
			o.WorkDir = "/work/demo/internal"
			return mustBuild(t, in, o)
		}},
		{"codex-root-seccomp", func(t *testing.T) *Plan {
			in := withSeccomp(fixture(profile.Codex))
			in.QargiRecords, in.QargiRecordsInside = "/state/leads/arc-lead/records", "/home/lead/.qargi"
			in.ExtraPlaceholders = []profile.Placeholder{{Rel: ".envrc", Source: "/opt/ataniq/placeholders/envrc"}}
			return mustBuild(t, in, options(in, allPresent(t, in)))
		}},
		{"codex-owner-optional-absent", func(t *testing.T) *Plan {
			in := ownerMode(fixture(profile.Codex))
			o := options(in, nil)
			o.WorkDir = "/home/lead"
			return mustBuild(t, in, o)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := c.build(t)
			got := render(p)
			file := filepath.Join("testdata", c.name+".golden")
			if *update {
				if err := os.WriteFile(file, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(file)
			if err != nil {
				t.Fatalf("%v (run with -update to write it)", err)
			}
			if got != string(want) {
				t.Errorf("plan differs from %s (run with -update after checking the change):\n%s", file, got)
			}
		})
	}
}

func TestEveryPlanVerifiesAndHasNoForbiddenFlag(t *testing.T) {
	for _, rt := range profile.Runtimes() {
		for _, owner := range []bool{false, true} {
			for _, sec := range []bool{false, true} {
				in := fixture(rt)
				if owner {
					in = ownerMode(in)
				}
				if sec {
					in = withSeccomp(in)
				}
				for _, present := range []map[string]bool{allPresent(t, in), nil} {
					o := options(in, present)
					p := mustBuild(t, in, o)
					if probs := Verify(p, in, o); len(probs) > 0 {
						t.Errorf("%s owner=%v seccomp=%v: %v", rt, owner, sec, probs)
					}
					for _, a := range p.Argv {
						for _, f := range profile.ForbiddenFlags() {
							if a == f {
								t.Errorf("%s: argv holds %s", rt, f)
							}
						}
					}
				}
			}
		}
	}
}

func TestPartsAndOrder(t *testing.T) {
	p, in, _ := basePlan(t)
	if got := join(p.Pasta, p.PassFDs, p.Bwrap, p.Inner); !equal(got, p.Argv) {
		t.Fatalf("Argv is not Pasta, PassFDs, Bwrap and Inner joined")
	}
	wantPass := []string{"/opt/ataniq/bin/probe", "pass-fds", "--write", "3=/opt/ataniq/run/arc-lead/report.fifo", "--"}
	if !equal(p.PassFDs, wantPass) {
		t.Errorf("pass-through step = %q", p.PassFDs)
	}
	if p.Pasta[0] != "/usr/bin/pasta" || !equal(p.Pasta[1:], sandboxnet.PastaArgs()) {
		t.Errorf("pasta part = %q", p.Pasta)
	}
	if p.Bwrap[0] != "/usr/bin/bwrap" || p.Bwrap[len(p.Bwrap)-1] != "--" {
		t.Errorf("bwrap part = %q ... %q", p.Bwrap[0], p.Bwrap[len(p.Bwrap)-1])
	}
	wantInner := []string{"/ataniq/probe", "--spec", "/ataniq/spec/spec.json", "--report-fd", "3", "--", "/ataniq/agent/bin/claude", "--model", "example"}
	if !equal(p.Inner, wantInner) {
		t.Errorf("inner = %q", p.Inner)
	}
	if p.SetControllingTerminal {
		t.Error("SetControllingTerminal must be false")
	}
	flags, _ := in.Flags()
	if !equal(p.Bwrap[1:1+len(flags)], flags) {
		t.Errorf("bwrap does not start with the profile's flag set: %q", p.Bwrap[1:1+len(flags)])
	}
	// --chdir, --remount-ro / and -- are the end, in that order.
	n := len(p.Bwrap)
	if !equal(p.Bwrap[n-5:], []string{"--chdir", "/work/demo", "--remount-ro", "/", "--"}) {
		t.Errorf("tail = %q", p.Bwrap[n-5:])
	}
	if i, j := indexOf(p.Bwrap, "--setenv"), indexOf(p.Bwrap, "--tmpfs"); i < 0 || j < i {
		t.Errorf("--setenv at %d, first --tmpfs at %d", i, j)
	}
	env, _ := in.Environment()
	if len(p.Env) != len(env) || len(env) == 0 {
		t.Errorf("Env = %v", p.Env)
	}
	d, _ := in.Digest()
	if p.Digest != d {
		t.Errorf("digest = %s, want %s", p.Digest, d)
	}
	wantReport := File{FD: 3, Path: "/opt/ataniq/run/arc-lead/report.fifo", Mode: FileWrite, Purpose: PurposeReport}
	if len(p.Files) != 1 || p.Files[0] != wantReport {
		t.Errorf("Files = %+v without a seccomp file, want the report channel only", p.Files)
	}
	for _, o := range []string{"--ro-bind-try", "--new-session", "--disable-userns", "--assert-userns-disabled"} {
		if indexOf(p.Argv, o) >= 0 {
			t.Errorf("argv holds %s", o)
		}
	}
}

func TestIdentityModes(t *testing.T) {
	in := fixture(profile.Claude)
	p := mustBuild(t, in, options(in, nil))
	i := indexOf(p.Bwrap, "--uid")
	if p.Bwrap[i+1] != "0" || p.Bwrap[i+3] != "0" {
		t.Errorf("root mode ids = %q %q", p.Bwrap[i+1], p.Bwrap[i+3])
	}
	in = ownerMode(in)
	p = mustBuild(t, in, options(in, nil))
	i = indexOf(p.Bwrap, "--uid")
	if p.Bwrap[i+1] != "1000" || p.Bwrap[i+3] != "1001" || p.Bwrap[i+2] != "--gid" {
		t.Errorf("owner mode ids = %q", p.Bwrap[i:i+4])
	}
}

func TestSeccompHook(t *testing.T) {
	in := withSeccomp(fixture(profile.Claude))
	p := mustBuild(t, in, options(in, nil))
	i := indexOf(p.Bwrap, "--seccomp")
	if i < 0 || p.Bwrap[i+1] != "7" {
		t.Fatalf("--seccomp not passed with descriptor 7: %q", p.Bwrap)
	}
	// After the last --setenv, before the first mount.
	if j := indexOf(p.Bwrap, "--tmpfs"); i > j {
		t.Errorf("--seccomp at %d after the first mount at %d", i, j)
	}
	want := File{FD: 7, Path: in.SeccompFile, Mode: FileRead, Purpose: PurposeSeccomp, SHA256: in.SeccompSHA256}
	if len(p.Files) != 2 || p.Files[0] != want || p.Files[1].Purpose != PurposeReport {
		t.Errorf("Files = %+v", p.Files)
	}
	wantPass := []string{"/opt/ataniq/bin/probe", "pass-fds", "--read", "7=/opt/ataniq/seccomp/lead.bpf", "--write", "3=/opt/ataniq/run/arc-lead/report.fifo", "--"}
	if !equal(p.PassFDs, wantPass) {
		t.Errorf("pass-through step = %q", p.PassFDs)
	}
	if !containsNote(p.Notes, "descriptor 7") || containsNote(p.Notes, NoteNoSeccomp) {
		t.Errorf("notes = %q", p.Notes)
	}

	in = fixture(profile.Claude)
	p = mustBuild(t, in, options(in, nil))
	if indexOf(p.Bwrap, "--seccomp") >= 0 || len(p.Files) != 1 || indexOf(p.PassFDs, "--read") >= 0 || !containsNote(p.Notes, NoteNoSeccomp) {
		t.Errorf("no-seccomp plan: files %v notes %q", p.Files, p.Notes)
	}
}

func containsNote(notes []string, sub string) bool {
	for _, n := range notes {
		if strings.Contains(n, sub) {
			return true
		}
	}
	return false
}

func TestNotesInEveryPlan(t *testing.T) {
	for _, in := range []profile.Inputs{fixture(profile.Claude), withSeccomp(fixture(profile.Codex))} {
		p := mustBuild(t, in, options(in, nil))
		for _, want := range []string{NoteShareNet, NoteNestedUserns, NoteSession, NotePWD(p.WorkDir)} {
			if !containsNote(p.Notes, want) {
				t.Errorf("%s: note %q missing", in.Runtime, want)
			}
		}
		for _, w := range []string{"pasta made", "R17", "larger kernel surface"} {
			if !containsNote(p.Notes, w) {
				t.Errorf("notes do not say %q", w)
			}
		}
		for _, n := range p.Notes {
			l := strings.ToLower(n)
			if strings.Contains(l, "qualified") && !strings.Contains(l, "qualification") {
				t.Errorf("a note claims qualification: %q", n)
			}
		}
	}
}

func TestInsideEnvironmentHasPWD(t *testing.T) {
	in := fixture(profile.Claude)
	for _, dir := range []string{"", "/work/demo/sub", "/home/lead"} {
		o := options(in, nil)
		o.WorkDir = dir
		p := mustBuild(t, in, o)
		want := dir
		if dir == "" {
			want = "/work/demo"
		}
		env := InsideEnvironment(p)
		if len(env) != len(p.Env)+1 {
			t.Fatalf("workdir %q: %d variables, the allowlist has %d", dir, len(env), len(p.Env))
		}
		var seen, sorted = 0, true
		for i, e := range env {
			if e.Name == "PWD" {
				seen++
				if e.Value != want {
					t.Errorf("PWD = %q, want %q", e.Value, want)
				}
			}
			if i > 0 && env[i-1].Name >= e.Name {
				sorted = false
			}
		}
		if seen != 1 || !sorted {
			t.Errorf("PWD seen %d times, sorted %v: %v", seen, sorted, env)
		}
		if !containsNote(p.Notes, "PWD="+want) {
			t.Errorf("no note for PWD=%s: %q", want, p.Notes)
		}
		// The caller's list is not changed, and the start-up comparison
		// agrees with exactly this environment.
		for _, e := range p.Env {
			if e.Name == "PWD" {
				t.Fatal("PWD added to Plan.Env")
			}
		}
		observed := make([]string, 0, len(env))
		for _, e := range env {
			observed = append(observed, e.Name+"="+e.Value)
		}
		if d, err := profile.DiffEnvironment(env, observed); err != nil || len(d) > 0 {
			t.Errorf("diff against the inside environment: %v %v", d, err)
		}
		// Against the allowlist alone, PWD is the one difference.
		d, err := profile.DiffEnvironment(p.Env, observed)
		if err != nil || len(d) != 1 || d[0].Kind != profile.EnvUnexpected || d[0].Name != "PWD" {
			t.Errorf("diff against the allowlist alone: %v %v", d, err)
		}
	}
}

func TestPWDInTheAllowlistIsRefused(t *testing.T) {
	in := fixture(profile.Claude)
	in.ExtraEnv = []profile.EnvVar{{Name: "PWD", Value: "/elsewhere"}}
	_, err := Build(in, options(in, nil))
	if r := refusal(t, err); r.Field != "environment" || !strings.Contains(r.Reason, "PWD") {
		t.Errorf("refusal = %v", r)
	}
}

func TestOptionalRows(t *testing.T) {
	in := fixture(profile.Claude)
	present := allPresent(t, in)
	if !present["/lib64"] || !present["/etc/ssl"] {
		t.Fatalf("fixture has no optional rows: %v", present)
	}
	full := mustBuild(t, in, options(in, present))
	some := mustBuild(t, in, options(in, without(present, absent...)))

	// has reports whether the plan mounts or links the inside path.
	has := func(p *Plan, inside string) bool {
		for i := 3; i < len(p.Bwrap); i++ {
			if p.Bwrap[i] == inside && (p.Bwrap[i-2] == "--ro-bind" || p.Bwrap[i-2] == "--symlink") {
				return true
			}
		}
		return false
	}
	for _, r := range absent {
		if !has(full, r) {
			t.Errorf("%s missing when present", r)
		}
		if has(some, r) {
			t.Errorf("%s emitted when absent", r)
		}
		if !containsNote(some.Notes, r+" left out") {
			t.Errorf("no note for %s: %q", r, some.Notes)
		}
		if containsNote(full.Notes, r+" left out") {
			t.Errorf("note for present row %s", r)
		}
	}
	// A required host row is never dropped, whatever the map says.
	none := mustBuild(t, in, options(in, nil))
	if !has(none, "/usr") || !has(none, "/etc/passwd") {
		t.Error("a required host row is missing when no optional row is present")
	}
	// Optional rows keep the table's relative order.
	binds, _ := in.Binds()
	if len(full.Bwrap) < len(binds) {
		t.Errorf("bwrap shorter than the table")
	}
}

func TestDeterministic(t *testing.T) {
	in := withSeccomp(fixture(profile.Codex))
	a := render(mustBuild(t, in, options(in, nil)))
	for i := 0; i < 5; i++ {
		if b := render(mustBuild(t, in, options(in, nil))); a != b {
			t.Fatal("two builds of the same inputs differ")
		}
	}
}

func TestBuildDoesNotAliasInputs(t *testing.T) {
	in := fixture(profile.Claude)
	o := options(in, nil)
	p := mustBuild(t, in, o)
	o.AgentArgv[1] = "--changed"
	if p.Inner[7] != "--model" {
		t.Errorf("plan shares the caller's agent argv: %q", p.Inner)
	}
	p.Pasta[1] = "x"
	if sandboxnet.PastaArgs()[0] != "-f" {
		t.Error("plan changed the pasta arguments")
	}
}

func TestWorkDir(t *testing.T) {
	in := fixture(profile.Claude)
	for _, dir := range []string{"", "/work/demo", "/work/demo/sub/dir", "/home/lead", "/home/lead/x"} {
		o := options(in, nil)
		o.WorkDir = dir
		p := mustBuild(t, in, o)
		want := dir
		if dir == "" {
			want = "/work/demo"
		}
		i := indexOf(p.Bwrap, "--chdir")
		if p.Bwrap[i+1] != want || p.WorkDir != want {
			t.Errorf("workdir %q: chdir %q, plan %q", dir, p.Bwrap[i+1], p.WorkDir)
		}
	}
}

func refusal(t *testing.T, err error) *Refusal {
	t.Helper()
	var r *Refusal
	if !errors.As(err, &r) {
		t.Fatalf("got %v, want a *plan.Refusal", err)
	}
	return r
}

func TestRefusals(t *testing.T) {
	long := strings.Repeat("a", 60<<10)
	cases := []struct {
		name   string
		mutate func(in *profile.Inputs, o *Options)
		field  string
		reason string
	}{
		{"relative pasta", func(_ *profile.Inputs, o *Options) { o.PastaPath = "pasta" }, "pasta executable", "absolute"},
		{"empty pasta", func(_ *profile.Inputs, o *Options) { o.PastaPath = "" }, "pasta executable", "empty"},
		{"unclean pasta", func(_ *profile.Inputs, o *Options) { o.PastaPath = "/usr/bin/../bin/pasta" }, "pasta executable", "clean"},
		{"relative bwrap", func(_ *profile.Inputs, o *Options) { o.BwrapPath = "./bwrap" }, "bubblewrap executable", "absolute"},
		{"empty bwrap", func(_ *profile.Inputs, o *Options) { o.BwrapPath = "" }, "bubblewrap executable", "empty"},
		{"no agent", func(_ *profile.Inputs, o *Options) { o.AgentArgv = nil }, "agent argv", "empty"},
		{"relative agent", func(_ *profile.Inputs, o *Options) { o.AgentArgv[0] = "claude" }, "agent executable", "absolute"},
		{"NUL in agent argument", func(_ *profile.Inputs, o *Options) { o.AgentArgv[1] = "a\x00b" }, "agent argv", "NUL"},
		{"NUL in agent executable", func(_ *profile.Inputs, o *Options) { o.AgentArgv[0] = "/a\x00b" }, "agent executable", "control"},
		{"forbidden option as agent argument", func(_ *profile.Inputs, o *Options) { o.AgentArgv[1] = "--new-session" }, "agent argv", "forbidden"},
		{"seccomp descriptor missing", func(in *profile.Inputs, o *Options) { *in = withSeccomp(*in); o.SeccompFD = 0 }, "seccomp descriptor", "missing"},
		{"seccomp descriptor 2", func(in *profile.Inputs, o *Options) { *in = withSeccomp(*in); o.SeccompFD = 2 }, "seccomp descriptor", "outside"},
		{"seccomp descriptor negative", func(in *profile.Inputs, o *Options) { *in = withSeccomp(*in); o.SeccompFD = -1 }, "seccomp descriptor", "outside"},
		{"seccomp descriptor huge", func(in *profile.Inputs, o *Options) { *in = withSeccomp(*in); o.SeccompFD = 1 << 20 }, "seccomp descriptor", "outside"},
		{"descriptor without a seccomp file", func(_ *profile.Inputs, o *Options) { o.SeccompFD = 9 }, "seccomp descriptor", "no seccomp file"},
		{"report descriptor missing", func(_ *profile.Inputs, o *Options) { o.ReportFD = 0 }, "report descriptor", "missing"},
		{"report descriptor 2", func(_ *profile.Inputs, o *Options) { o.ReportFD = 2 }, "report descriptor", "outside"},
		{"report descriptor negative", func(_ *profile.Inputs, o *Options) { o.ReportFD = -4 }, "report descriptor", "outside"},
		{"report descriptor 1024", func(_ *profile.Inputs, o *Options) { o.ReportFD = 1024 }, "report descriptor", "outside"},
		{"report descriptor is the seccomp one", func(in *profile.Inputs, o *Options) { *in = withSeccomp(*in); o.SeccompFD = 5; o.ReportFD = 5 }, "report descriptor", "differ"},
		{"report path missing", func(_ *profile.Inputs, o *Options) { o.ReportPath = "" }, "report path", "empty"},
		{"report path relative", func(_ *profile.Inputs, o *Options) { o.ReportPath = "run/report" }, "report path", "absolute"},
		{"report path unclean", func(_ *profile.Inputs, o *Options) { o.ReportPath = "/opt/ataniq/run/../report" }, "report path", "clean"},
		{"report path with a newline", func(_ *profile.Inputs, o *Options) { o.ReportPath = "/opt/ataniq/run/a\nb" }, "report path", "control"},
		{"report path in the lead's home", func(_ *profile.Inputs, o *Options) { o.ReportPath = "/state/leads/arc-lead/home/report" }, "report path", "can write"},
		{"report path in the lead's tree", func(_ *profile.Inputs, o *Options) { o.ReportPath = "/state/leads/arc-lead/tree/.git/report" }, "report path", "can write"},
		{"report path in the lead's config", func(_ *profile.Inputs, o *Options) { o.ReportPath = "/state/leads/arc-lead/config/claude/report" }, "report path", "can write"},
		{"report path is a writable folder", func(_ *profile.Inputs, o *Options) { o.ReportPath = "/state/leads/arc-lead/home" }, "report path", "can write"},
		{"report path in the records folder", func(in *profile.Inputs, o *Options) {
			in.QargiRecords, in.QargiRecordsInside = "/state/leads/arc-lead/records", "/home/lead/.qargi"
			o.ReportPath = "/state/leads/arc-lead/records/report"
		}, "report path", "can write"},
		{"report path never mounted", func(_ *profile.Inputs, o *Options) { o.ReportPath = "/home/owner/.ssh/report" }, "report path", "never-mounted"},
		{"report path is a never-mounted file", func(_ *profile.Inputs, o *Options) { o.ReportPath = "/home/owner/.claude.json" }, "report path", "never-mounted"},
		{"report path in the other runtime's folder", func(_ *profile.Inputs, o *Options) { o.ReportPath = "/state/leads/arc-lead/config/codex/report" }, "report path", "never-mounted"},
		{"workdir relative", func(_ *profile.Inputs, o *Options) { o.WorkDir = "work/demo" }, "working folder", "absolute"},
		{"workdir unclean", func(_ *profile.Inputs, o *Options) { o.WorkDir = "/work/demo/../x" }, "working folder", "clean"},
		{"workdir outside", func(_ *profile.Inputs, o *Options) { o.WorkDir = "/etc" }, "working folder", "outside"},
		{"workdir beside the tree", func(_ *profile.Inputs, o *Options) { o.WorkDir = "/work/demo2" }, "working folder", "outside"},
		{"workdir work root", func(_ *profile.Inputs, o *Options) { o.WorkDir = "/work" }, "working folder", "outside"},
		{"workdir tmp", func(_ *profile.Inputs, o *Options) { o.WorkDir = "/tmp" }, "working folder", "outside"},
		{"workdir beside the home", func(_ *profile.Inputs, o *Options) { o.WorkDir = "/home/lead2" }, "working folder", "outside"},
		{"environment value read as an option", func(in *profile.Inputs, _ *Options) { in.Term = "--weird" }, "bubblewrap argument", "option"},
		{"extra variable read as an option", func(in *profile.Inputs, _ *Options) {
			in.ExtraEnv = []profile.EnvVar{{Name: "EXAMPLE", Value: "--x"}}
		}, "bubblewrap argument", "option"},
		{"agent too long", func(_ *profile.Inputs, o *Options) {
			o.AgentArgv = append(o.AgentArgv, long, long, long)
		}, "agent argv", "cap"},
		{"command line too long", func(_ *profile.Inputs, o *Options) {
			o.AgentArgv = append(o.AgentArgv, strings.Repeat("a", MaxArgvBytes-300))
		}, "command line", "cap"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := fixture(profile.Claude)
			o := options(in, nil)
			o.AgentArgv = append([]string(nil), o.AgentArgv...)
			c.mutate(&in, &o)
			p, err := Build(in, o)
			if err == nil {
				t.Fatalf("Build succeeded: %q", p.Argv)
			}
			r := refusal(t, err)
			if r.Field != c.field || !strings.Contains(r.Reason, c.reason) {
				t.Errorf("refusal = %q / %q, want %q / ...%q...", r.Field, r.Reason, c.field, c.reason)
			}
			if p != nil {
				t.Error("a plan came back with the error")
			}
		})
	}
}

func TestReportPathBoundaries(t *testing.T) {
	// A path that only shares a prefix with a lead-writable or never-mounted
	// path is not in or below it.
	in := fixture(profile.Claude)
	for _, path := range []string{
		"/state/leads/arc-lead/homework",
		"/state/leads/arc-lead/run/report",
		"/state/leads/arc-lead2/home/report",
		"/home/owner/.sshd/report",
		"/home/owner/work/demo2/report",
		"/run/ataniq/arc-lead/report",
	} {
		o := options(in, nil)
		o.ReportPath = path
		if _, err := Build(in, o); err != nil {
			t.Errorf("%s: %v", path, err)
		}
	}
}

func TestInvalidInputsAreTheProfilesRefusal(t *testing.T) {
	mutations := map[string]func(*profile.Inputs){
		"role":                func(in *profile.Inputs) { in.Role = "Bad Name" },
		"relative state root": func(in *profile.Inputs) { in.StateRoot = "state" },
		"no never-mounted":    func(in *profile.Inputs) { in.NeverMounted = nil },
		"a source never mounted": func(in *profile.Inputs) {
			in.QargiInstall = "/home/owner/.ssh/keys"
		},
		"the other runtime's folder": func(in *profile.Inputs) {
			in.QargiInstall = "/state/leads/arc-lead/config/codex"
		},
		"unknown runtime": func(in *profile.Inputs) { in.Runtime = "other" },
	}
	for name, m := range mutations {
		t.Run(name, func(t *testing.T) {
			in := fixture(profile.Claude)
			m(&in)
			_, err := Build(in, options(fixture(profile.Claude), nil))
			var r *profile.Refusal
			if !errors.As(err, &r) {
				t.Fatalf("got %v, want the profile's *Refusal", err)
			}
		})
	}
}

func TestVerifyReportsInvalidInputs(t *testing.T) {
	p, in, o := basePlan(t)
	in.Role = ""
	probs := Verify(p, in, o)
	if len(probs) != 1 || probs[0].Kind != ProbInputs {
		t.Errorf("problems = %v", probs)
	}
	if got := Verify(nil, fixture(profile.Claude), Options{}); len(got) != 1 || got[0].Kind != ProbShape {
		t.Errorf("nil plan: %v", got)
	}
}
