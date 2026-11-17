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
			in.QargiRecordsInside = "/ataniq/records" // the other goldens have the records in the home
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
		for _, want := range []string{NoteShareNet, NoteNestedUserns, NoteBindSources, NoteSession, NotePWD(p.WorkDir)} {
			if !containsNote(p.Notes, want) {
				t.Errorf("%s: note %q missing", in.Runtime, want)
			}
		}
		for _, w := range []string{"pasta made", "R17", "larger kernel surface", "rule G1", "can write", "symbolic link", "CheckBindSources"} {
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

func TestMounts(t *testing.T) {
	in := fixture(profile.Claude)
	present := allPresent(t, in)
	full := mustBuild(t, in, options(in, present))
	some := mustBuild(t, in, options(in, without(present, absent...)))

	table, _ := in.Binds()
	// One Mount per emitted row of the table, in order, the final remount not
	// included.
	var want []string
	for _, b := range table {
		if b.Kind != profile.KindRemountRO {
			want = append(want, b.Inside)
		}
	}
	var got []string
	for _, m := range full.Mounts {
		got = append(got, m.Inside)
	}
	if !equal(got, want) {
		t.Errorf("Mounts = %q, want the table's rows %q", got, want)
	}
	// A row left out is not listed.
	for _, m := range some.Mounts {
		for _, r := range absent {
			if m.Inside == r {
				t.Errorf("%s listed though left out", r)
			}
		}
	}
	if len(some.Mounts) != len(full.Mounts)-len(absent) {
		t.Errorf("%d rows, %d expected", len(some.Mounts), len(full.Mounts)-len(absent))
	}

	byInside := map[string]Mount{}
	for _, m := range full.Mounts {
		byInside[m.Inside] = m
	}
	// No row is sourced inside a folder the lead writes (rule G1): the three
	// rows of the tree's .git take their sources from Ataniq's copy.
	for _, m := range full.Mounts {
		if m.LeadWritable {
			t.Errorf("%s: LeadWritable = true", m.Inside)
		}
	}
	if m := byInside["/work/demo/.git/hooks"]; m.Kind != profile.KindRoBind || m.Source != "/state/leads/arc-lead/gitmeta/hooks" {
		t.Errorf("hooks row = %+v", m)
	}
	if m := byInside["/work/demo"]; m.Kind != profile.KindRwBind || m.Source != "/state/leads/arc-lead/tree" || m.LeadWritable {
		t.Errorf("tree row = %+v", m)
	}
	if m := byInside["/lib64"]; m.Kind != profile.KindSymlink || m.Source != "" || !m.Optional {
		t.Errorf("lib64 row = %+v", m)
	}
	if m := byInside["/etc/ssl"]; m.Kind != profile.KindRoBind || m.Source != "/etc/ssl" || !m.Optional || m.LeadWritable {
		t.Errorf("ssl row = %+v", m)
	}
	if m := byInside["/"]; m.Kind != profile.KindTmpfs || m.Source != "" || m.Optional {
		t.Errorf("root row = %+v", m)
	}

	// With the records folder and a placeholder the lead-writable rows follow
	// the table, not a fixed list.
	in = fixture(profile.Codex)
	in.ExtraPlaceholders = []profile.Placeholder{{Rel: ".envrc", Source: "/opt/ataniq/placeholders/envrc"}}
	p := mustBuild(t, in, options(in, nil))
	for _, m := range p.Mounts {
		if m.Inside == "/home/lead/.qargi" && (m.Kind != profile.KindRwBind || m.LeadWritable) {
			t.Errorf("records row = %+v", m)
		}
		if m.Inside == "/work/demo/.envrc" && m.LeadWritable {
			t.Errorf("placeholder row = %+v: its source is Ataniq's", m)
		}
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
		{"report path in the records folder", func(_ *profile.Inputs, o *Options) { o.ReportPath = "/state/leads/arc-lead/qargi/report" }, "report path", "can write"},
		{"report path in the requests folder", func(_ *profile.Inputs, o *Options) { o.ReportPath = "/state/leads/arc-lead/requests/report" }, "report path", "can write"},
		{"report path in the mail folder", func(_ *profile.Inputs, o *Options) { o.ReportPath = "/state/leads/arc-lead/mail/a-feature/report" }, "report path", "can write"},
		{"report path in the lead's own mailbox", func(_ *profile.Inputs, o *Options) { o.ReportPath = "/state/mailbox/arc-lead/report" }, "report path", "source of the bind at /ataniq/mailbox"},
		{"report path in another role's mailbox", func(_ *profile.Inputs, o *Options) { o.ReportPath = "/state/mailbox/other-lead/report" }, "report path", "state folder /state/mailbox"},
		{"report path in the project's published folder", func(_ *profile.Inputs, o *Options) { o.ReportPath = "/state/pub/demo/a-feature/out/report" }, "report path", "source of the bind at /coord"},
		{"report path in another project's published folder", func(_ *profile.Inputs, o *Options) { o.ReportPath = "/state/pub/other/report" }, "report path", "state folder /state/pub"},
		{"report path in a coordinator's home", func(_ *profile.Inputs, o *Options) { o.ReportPath = "/state/coords/demo/a-feature/home/report" }, "report path", "state folder /state/coords"},
		{"report path in the coordinators' login", func(_ *profile.Inputs, o *Options) { o.ReportPath = "/state/coord-login/demo/claude/report" }, "report path", "state folder /state/coord-login"},
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
		{"environment value read as an option", func(in *profile.Inputs, _ *Options) { in.Term = "--weird" }, "bubblewrap argument", "starts with --"},
		{"extra variable read as an option", func(in *profile.Inputs, _ *Options) {
			in.ExtraEnv = []profile.EnvVar{{Name: "EXAMPLE", Value: "--x"}}
		}, "bubblewrap argument", "starts with --"},
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
	// A path that only shares a prefix with a lead-writable, never-mounted or
	// state path is not in or below it.
	in := fixture(profile.Claude)
	for _, path := range []string{
		"/state/leadsx/report",
		"/state/mirrorsx/report",
		"/home/owner/.sshd/report",
		"/home/owner/work/demo2/report",
		"/run/ataniq/arc-lead/report",
		"/opt/ataniq/specs/report",
		"/opt/ataniq/binary/report",
	} {
		o := options(in, nil)
		o.ReportPath = path
		o.ReportDir = pathDir(path)
		if _, err := Build(in, o); err != nil {
			t.Errorf("%s: %v", path, err)
		}
	}
}

func pathDir(p string) string { return p[:strings.LastIndex(p, "/")] }

// The step opens the report path write-only. A path that the sandbox sees, or
// that another lead owns, or that is not in the holder's own folder, is
// refused: Build and Verify refuse the same options.
func TestReportPathRefusals(t *testing.T) {
	in := fixture(profile.Claude)
	cases := []struct {
		name string
		path string
		dir  string // "" keeps the default folder
		want string
	}{
		// Each of the paths the second review built a plan with.
		{"inside the spec bind", "/opt/ataniq/spec/arc-lead/spec.json", "/opt/ataniq/spec/arc-lead", "source of the bind"},
		{"the probe binary", "/opt/ataniq/bin/probe", "/opt/ataniq/bin", "source of the bind"},
		{"the managed settings", "/opt/ataniq/managed/arc-lead/settings.json", "/opt/ataniq/managed/arc-lead", "source of the bind"},
		{"the state root", "/state", "/", "source of the bind"},
		{"the lead's own state folder", "/state/leads/arc-lead", "/state/leads", "source of the bind"},
		{"the leads folder", "/state/leads", "/state", "source of the bind"},
		{"another lead's file", "/state/leads/other-lead/home/x", "/state/leads/other-lead/home", "state folder"},
		{"another project's mirror", "/state/mirrors/other.git/report", "/state/mirrors/other.git", "state folder"},
		{"the project's mirror", "/state/mirrors/demo.git/report", "/state/mirrors/demo.git", "source of the bind"},
		{"the mirrors folder", "/state/mirrors", "/state", "source of the bind"},
		{"a system file the sandbox sees", "/etc/passwd", "/etc", "source of the bind"},
		{"a system file outside the folder", "/etc/hostname", "", "not directly inside"},
		// A bind source that is the folder itself, or lies below the path.
		{"a source itself", "/opt/ataniq/qargi", "/opt/ataniq", "source of the bind"},
		{"above a source", "/opt/ataniq/spec", "/opt/ataniq", "source of the bind"},
		{"above a system source", "/usr", "/", "source of the bind"},
		// The folder.
		{"not directly inside the folder", "/opt/ataniq/run/arc-lead/sub/report", "", "not directly inside"},
		{"another folder", "/opt/ataniq/run/other/report", "", "not directly inside"},
		{"the folder itself", "/opt/ataniq/run/arc-lead", "", "not directly inside"},
		{"the folder above", "/opt/ataniq/run/report", "", "not directly inside"},
		{"a folder that only shares a prefix", "/opt/ataniq/run/arc-lead2/report", "", "not directly inside"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o := options(in, allPresent(t, in))
			o.ReportPath = c.path
			if c.dir != "" {
				o.ReportDir = c.dir
			}
			_, err := Build(in, o)
			if err == nil || !strings.Contains(err.Error(), "report path") || !strings.Contains(err.Error(), c.want) {
				t.Errorf("Build(%s): %v, want a report path refusal with %q", c.path, err, c.want)
			}
			// Verify checks the same: a plan built for the default options
			// does not verify against these.
			good := mustBuild(t, in, options(in, allPresent(t, in)))
			if !hasKind(Verify(good, in, o), ProbOptions) {
				t.Errorf("Verify accepted the options with report path %s", c.path)
			}
		})
	}
}

func TestReportDirRefusals(t *testing.T) {
	in := fixture(profile.Claude)
	for name, dir := range map[string]string{
		"missing":   "",
		"relative":  "opt/run",
		"unclean":   "/opt/ataniq/run/../run/arc-lead",
		"a newline": "/opt/ataniq/run/a\nb",
	} {
		o := options(in, nil)
		o.ReportDir = dir
		_, err := Build(in, o)
		if err == nil || !strings.Contains(err.Error(), "report folder") {
			t.Errorf("%s: %v", name, err)
		}
	}
	// The default folder and path are fine.
	if _, err := Build(in, options(in, nil)); err != nil {
		t.Fatal(err)
	}
}

func TestRefusalDoesNotRepeatTheOperand(t *testing.T) {
	in := fixture(profile.Claude)
	in.Term = "--hunter2-term"
	_, err := Build(in, options(in, nil))
	if err == nil || strings.Contains(err.Error(), "hunter2") {
		t.Errorf("error = %v", err)
	}
}

// Build's own assertions refuse on their own, though a step before them
// reaches the same refusal for every input the package can see.
func TestAssertPlanRefusesOnItsOwn(t *testing.T) {
	p, in, o := basePlan(t)
	if err := assertPlan(p, in, o); err != nil {
		t.Fatalf("assertPlan refused a good plan: %v", err)
	}
	for name, c := range map[string]struct {
		field string
		mut   func(p *Plan)
	}{
		"forbidden option in Argv": {"command line", func(p *Plan) { p.Argv = append(p.Argv, "--disable-userns") }},
		"command line too long":    {"command line", func(p *Plan) { p.Argv = append(p.Argv, strings.Repeat("a", MaxArgvBytes)) }},
		"the plan fails Verify":    {"plan", func(p *Plan) { p.Digest = "" }},
		"a git row bound from the tree": {"plan", func(p *Plan) {
			i := find(t, p, "--ro-bind", "/state/leads/arc-lead/gitmeta/hooks")
			p.Bwrap[i+1] = "/state/leads/arc-lead/tree/.git/hooks"
			p.Mounts[indexOfMount(t, p, "/work/demo/.git/hooks")].Source = "/state/leads/arc-lead/tree/.git/hooks"
			resync(p)
		}},
	} {
		t.Run(name, func(t *testing.T) {
			q := clonePlan(p)
			c.mut(q)
			err := assertPlan(q, in, o)
			if err == nil {
				t.Fatal("assertPlan accepted it")
			}
			if r := refusal(t, err); r.Field != c.field {
				t.Errorf("refusal field = %q, want %q", r.Field, c.field)
			}
		})
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

// The note about the report channel states the contract of assert: one
// accepted line is the signal, end-of-file is not awaited, and both
// descriptor questions are qualification items.
func TestReportChannelNote(t *testing.T) {
	in := fixture(profile.Claude)
	p := mustBuild(t, in, options(in, allPresent(t, in)))
	var note string
	for _, n := range p.Notes {
		if strings.HasPrefix(n, "report channel") {
			note = n
		}
	}
	for _, want := range []string{
		"one accepted probe line", "does not wait for end-of-file", "a second line or any further byte",
		"whether the descriptor reaches the probe inside", "which other processes hold it", "qualification items",
	} {
		if !strings.Contains(note, want) {
			t.Errorf("the report channel note does not say %q: %q", want, note)
		}
	}
}

func indexOfMount(t *testing.T, p *Plan, inside string) int {
	t.Helper()
	for i, m := range p.Mounts {
		if m.Inside == inside {
			return i
		}
	}
	t.Fatalf("no mount %s", inside)
	return -1
}

// Rule G1: no source in or below a folder the lead can write.
func TestBuildRefusesASourceTheLeadCanWrite(t *testing.T) {
	// The profile refuses first, with its own refusal, for every way the
	// inputs can put a source there, including the git metadata folder.
	for name, m := range map[string]func(*profile.Inputs){
		"git metadata in the tree":   func(in *profile.Inputs) { in.GitMeta = "/state/leads/arc-lead/tree/.git-meta" },
		"git metadata in the home":   func(in *profile.Inputs) { in.GitMeta = "/state/leads/arc-lead/home/.gitmeta" },
		"git metadata is the tree's": func(in *profile.Inputs) { in.GitMeta = "/state/leads/arc-lead/tree" },
		"a source in the tree":       func(in *profile.Inputs) { in.AssignmentDir = "/state/leads/arc-lead/tree/assignment" },
		"a source in the config":     func(in *profile.Inputs) { in.LeadSkills = "/state/leads/arc-lead/config/claude/skills" },
		"a placeholder in the home": func(in *profile.Inputs) {
			in.ExtraPlaceholders = []profile.Placeholder{{Rel: ".envrc", Source: "/state/leads/arc-lead/home/envrc"}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			in := fixture(profile.Claude)
			m(&in)
			if _, err := Build(in, options(fixture(profile.Claude), nil)); err == nil {
				t.Fatal("Build accepted it")
			} else if _, ok := err.(*profile.Refusal); !ok {
				t.Fatalf("got %v, want the profile's *Refusal", err)
			}
		})
	}
}

func TestCheckSourcesNotWritableOnATable(t *testing.T) {
	in := fixture(profile.Claude)
	good, err := in.Binds()
	if err != nil {
		t.Fatal(err)
	}
	if err := checkSourcesNotWritable(good); err != nil {
		t.Fatalf("a good table: %v", err)
	}
	for name, c := range map[string]struct{ inside, source string }{
		"a git row from the tree": {"/work/demo/.git/config", "/state/leads/arc-lead/tree/.git/config"},
		"a row from the home":     {"/ataniq/qargi", "/state/leads/arc-lead/home/q"},
		"a row from the config":   {"/ataniq/projection", "/state/leads/arc-lead/config/claude/p"},
		"a row that is a folder":  {"/ataniq/assignment", "/state/leads/arc-lead/tree"},
		"a row from the requests": {"/ataniq/agent", "/state/leads/arc-lead/requests/agent"},
		"a row from the mail":     {"/coord", "/state/leads/arc-lead/mail/a-feature"},
		"a row from the records":  {"/ataniq/qargi", "/state/leads/arc-lead/qargi/bin"},
		"the mailbox from mail":   {"/ataniq/mailbox", "/state/leads/arc-lead/mail"},
	} {
		t.Run(name, func(t *testing.T) {
			table := append([]profile.Bind(nil), good...)
			for i := range table {
				if table[i].Inside == c.inside {
					table[i].Source = c.source
				}
			}
			err := checkSourcesNotWritable(table)
			r := refusal(t, err)
			if r.Field != "bind "+c.inside || !strings.Contains(r.Reason, c.source) || !strings.Contains(r.Reason, "can write") {
				t.Errorf("refusal = %+v", r)
			}
		})
	}
}

func TestNoMountOfEitherRuntimeIsLeadWritable(t *testing.T) {
	for _, rt := range profile.Runtimes() {
		in := fixture(rt)
		in.ExtraPlaceholders = []profile.Placeholder{{Rel: ".envrc", Source: "/opt/ataniq/placeholders/envrc"}}
		p := mustBuild(t, in, options(in, allPresent(t, in)))
		var rw []string
		for _, m := range p.Mounts {
			if m.LeadWritable {
				t.Errorf("%s: %s is lead-writable", rt, m.Inside)
			}
			if m.Kind == profile.KindRwBind {
				rw = append(rw, m.Source)
			}
		}
		for _, m := range p.Mounts {
			for _, w := range rw {
				if m.Source != "" && m.Kind != profile.KindRwBind && within(m.Source, w) {
					t.Errorf("%s: the source %s of %s lies in the lead-writable folder %s", rt, m.Source, m.Inside, w)
				}
			}
		}
		if probs := Verify(p, in, options(in, allPresent(t, in))); len(probs) != 0 {
			t.Errorf("%s: %v", rt, probs)
		}
	}
}
