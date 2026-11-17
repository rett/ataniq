package plan

import (
	"strconv"
	"strings"
	"testing"

	"ataniq/internal/leadsandbox/assert"
	"ataniq/internal/leadsandbox/profile"
)

// The two packages are written against one contract and share no code, so a
// test joins them: a spec made for a plan's inputs and its inside environment
// expects what the plan makes, and the probe command line of the plan carries
// the options that the assert contract names (the spec file, the report
// descriptor that the pass-through step opens write-only, then the agent).
func TestPlanAndSpecAgree(t *testing.T) {
	for _, rt := range profile.Runtimes() {
		t.Run(string(rt), func(t *testing.T) {
			in := fixture(rt)
			o := options(in, allPresent(t, in))
			o.WorkDir = "/work/demo/internal"
			p := mustBuild(t, in, o)

			h := assert.HostFacts{
				HolderPaths:    []string{"/run/example/holder.sock", "/var/lib/example/holder"},
				Identities:     []assert.FileIdentity{{Device: 7, Inode: 70}},
				LoopbackTCP:    "127.0.0.1:40123",
				AbstractSocket: "example-canary-1",
				ProviderHost:   "api.example.org",
				ManagedMarker:  "example-guard-hook",
			}
			spec, err := assert.NewSpec(in, InsideEnvironment(p), h)
			if err != nil {
				t.Fatalf("NewSpec: %v", err)
			}

			// The environment the spec expects is the allowlist plus PWD.
			want := append([]profile.EnvVar(nil), p.Env...)
			want = append(want, profile.EnvVar{Name: "PWD", Value: p.WorkDir})
			if len(spec.Env) != len(want) {
				t.Fatalf("the spec expects %d variables, the plan's allowlist and PWD are %d", len(spec.Env), len(want))
			}
			got := map[string]string{}
			for _, e := range spec.Env {
				got[e.Name] = e.Value
			}
			for _, e := range want {
				if v, ok := got[e.Name]; !ok || v != e.Value {
					t.Errorf("the spec expects %s=%q, the plan gives %q", e.Name, v, e.Value)
				}
			}
			if got["PWD"] != "/work/demo/internal" {
				t.Errorf("PWD = %q", got["PWD"])
			}
			// The same through the comparison the probe makes: no difference
			// for the environment the plan makes, and a difference for the
			// allowlist alone (the spec of an earlier version).
			observed := func(env []profile.EnvVar) []string {
				var out []string
				for _, e := range env {
					out = append(out, e.Name+"="+e.Value)
				}
				return out
			}
			specEnv := make([]profile.EnvVar, len(spec.Env))
			for i, e := range spec.Env {
				specEnv[i] = profile.EnvVar{Name: e.Name, Value: e.Value}
			}
			if d, err := profile.DiffEnvironment(specEnv, observed(want)); err != nil || len(d) != 0 {
				t.Errorf("the environment the plan makes differs from the spec's: %v %v", d, err)
			}
			if d, err := profile.DiffEnvironment(specEnv, observed(p.Env)); err != nil || len(d) == 0 {
				t.Errorf("the allowlist without PWD was taken for the spec's environment: %v %v", d, err)
			}

			// The facts both sides name.
			if spec.Digest != p.Digest {
				t.Errorf("digest: the spec has %s, the plan %s", spec.Digest, p.Digest)
			}
			if spec.Tree != profile.TreeInside(in.Project) || !strings.HasPrefix(p.WorkDir, spec.Tree) {
				t.Errorf("tree: the spec has %s, the working folder is %s", spec.Tree, p.WorkDir)
			}
			if spec.SeccompExpected != in.SeccompEnabled() {
				t.Errorf("seccomp expected: %v", spec.SeccompExpected)
			}

			// The probe command line: the probe, the spec file, the report
			// descriptor, "--", then the agent.
			if len(p.Inner) < 7 {
				t.Fatalf("inner: %v", p.Inner)
			}
			fd := strconv.Itoa(o.ReportFD)
			head := []string{profile.ProbeInside, "--spec", profile.SpecFileInside, "--report-fd", fd, "--"}
			for i, w := range head {
				if p.Inner[i] != w {
					t.Errorf("inner[%d] = %q, want %q (inner: %v)", i, p.Inner[i], w, p.Inner)
				}
			}
			if strings.Join(p.Inner[len(head):], " ") != strings.Join(o.AgentArgv, " ") {
				t.Errorf("the agent after the probe options: %v", p.Inner[len(head):])
			}
			// The spec file is where the plan binds the spec folder, and the
			// descriptor the probe writes the report to is the one the
			// pass-through step opens write-only at that number.
			if profile.SpecFileInside != profile.SpecDirInside+"/"+profile.SpecFileName {
				t.Errorf("the spec file %s is not in the spec folder %s", profile.SpecFileInside, profile.SpecDirInside)
			}
			var mounted bool
			for _, m := range p.Mounts {
				mounted = mounted || m.Inside == profile.SpecDirInside
			}
			if !mounted {
				t.Errorf("no row binds the spec folder %s", profile.SpecDirInside)
			}
			var report *File
			for i := range p.Files {
				if p.Files[i].Purpose == PurposeReport {
					report = &p.Files[i]
				}
			}
			if report == nil || report.Mode != FileWrite || strconv.Itoa(report.FD) != fd || report.Path != o.ReportPath {
				t.Errorf("the report descriptor is %+v, want %s opened write-only at %s", report, o.ReportPath, fd)
			}
			if !strings.Contains(strings.Join(p.PassFDs, " "), "--write "+fd+"="+o.ReportPath) {
				t.Errorf("the pass-through step does not open the report path write-only at %s: %v", fd, p.PassFDs)
			}

		})
	}
}
