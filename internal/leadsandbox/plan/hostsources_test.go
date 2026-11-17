package plan

import (
	"strings"
	"testing"

	"ataniq/internal/leadsandbox/profile"
)

// Rule G1, bound path equals checked path: a row of the host (a read-only bind
// whose source is its own inside path) is bound from the real path the caller
// resolved, given in Options.HostSources, and Verify checks it.

// linkedHost is a host whose /etc/localtime and /usr go through links: the real
// paths the caller resolved.
func linkedHost(in profile.Inputs, present map[string]bool) map[string]string {
	m := hostSources(in, present)
	m["/etc/localtime"] = "/usr/share/zoneinfo/Europe/Berlin"
	m["/usr"] = "/sysroot/usr"
	return m
}

func TestHostRowsAreBoundFromTheRealPathsTheCallerResolved(t *testing.T) {
	for _, rt := range profile.Runtimes() {
		in := fixture(rt)
		present := allPresent(t, in)
		o := options(in, present)
		o.HostSources = linkedHost(in, present)
		p := mustBuild(t, in, o)

		// The arguments, the mounts and the plan's own check agree.
		bound := map[string]string{}
		for _, m := range p.Mounts {
			bound[m.Inside] = m.Source
		}
		for inside, want := range map[string]string{
			"/etc/localtime": "/usr/share/zoneinfo/Europe/Berlin",
			"/usr":           "/sysroot/usr",
			"/etc/hosts":     "/etc/hosts",
			"/etc/ssl":       "/etc/ssl",
		} {
			if bound[inside] != want {
				t.Errorf("%s: %s is bound from %q, want %q", rt, inside, bound[inside], want)
			}
			i := find(t, p, "--ro-bind", want)
			if p.Bwrap[i+2] != inside {
				t.Errorf("%s: --ro-bind %s is bound at %s, want %s", rt, want, p.Bwrap[i+2], inside)
			}
		}
		if probs := Verify(p, in, o); len(probs) != 0 {
			t.Errorf("%s: %v", rt, probs)
		}
		// The profile's table is not changed by it, and the digest is the
		// same as for a host without links: it names roles, not paths.
		plain := mustBuild(t, in, options(in, present))
		if plain.Digest != p.Digest {
			t.Errorf("%s: the real paths of the host change the digest", rt)
		}
		// Rows Ataniq supplies keep the sources of the inputs.
		if bound["/ataniq/agent"] != in.AgentInstall {
			t.Errorf("%s: the agent is bound from %s", rt, bound["/ataniq/agent"])
		}
	}
}

func TestVerifyChecksTheBoundPathOfAHostRow(t *testing.T) {
	in := fixture(profile.Claude)
	present := allPresent(t, in)
	o := options(in, present)
	o.HostSources = linkedHost(in, present)
	p := mustBuild(t, in, o)

	// The plan binds the name of the row, as a plan built without the real
	// path would: the path that is bound is then not the one that was checked.
	t.Run("the plan binds the name, the options give the real path", func(t *testing.T) {
		q := clonePlan(p)
		i := find(t, q, "--ro-bind", "/usr/share/zoneinfo/Europe/Berlin")
		q.Bwrap[i+1] = "/etc/localtime"
		resync(q)
		if !hasKind(Verify(q, in, o), ProbMountDiffers) {
			t.Errorf("a host row bound by its name was accepted: %v", Verify(q, in, o))
		}
	})
	t.Run("the plan binds the real path, the options give another", func(t *testing.T) {
		o2 := o
		o2.HostSources = linkedHost(in, present)
		o2.HostSources["/etc/localtime"] = "/usr/share/zoneinfo/UTC"
		if !hasKind(Verify(p, in, o2), ProbMountDiffers) {
			t.Errorf("a plan with another real path was accepted: %v", Verify(p, in, o2))
		}
	})
	t.Run("a plan built for the name, checked against the real path", func(t *testing.T) {
		plain := mustBuild(t, in, options(in, present))
		if !hasKind(Verify(plain, in, o), ProbMountDiffers) {
			t.Errorf("a plan that binds names was accepted for options with real paths: %v", Verify(plain, in, o))
		}
	})
	t.Run("Plan.Mounts says another source", func(t *testing.T) {
		q := clonePlan(p)
		for i := range q.Mounts {
			if q.Mounts[i].Inside == "/usr" {
				q.Mounts[i].Source = "/usr"
			}
		}
		if !hasKind(Verify(q, in, o), ProbMountList) {
			t.Errorf("a Mounts row with another source was accepted: %v", Verify(q, in, o))
		}
	})
}

func TestHostSourcesRefusals(t *testing.T) {
	in := fixture(profile.Claude)
	absent := allPresent(t, in)
	delete(absent, "/etc/ssl")
	tests := []struct {
		name    string
		present map[string]bool
		change  func(m map[string]string)
		want    string
	}{
		{"a host row without a source", nil, func(m map[string]string) { delete(m, "/usr") }, "/usr has no resolved source"},
		{"an optional host row that is present, without a source", nil, func(m map[string]string) { delete(m, "/etc/localtime") }, "/etc/localtime has no resolved source"},
		{"a source for a row that is not a host row", nil, func(m map[string]string) { m["/ataniq/agent"] = "/opt/agent/real" }, "not the inside path of a row of the host"},
		{"a source for a path that is no row", nil, func(m map[string]string) { m["/etc/shadow"] = "/etc/shadow" }, "not the inside path of a row of the host"},
		{"a source for a symlink row", nil, func(m map[string]string) { m["/bin"] = "/usr/bin" }, "not the inside path of a row of the host"},
		{"a source for an optional row the host lacks", absent, func(m map[string]string) { m["/etc/ssl"] = "/etc/ssl" }, "lacks"},
		{"a relative real path", nil, func(m map[string]string) { m["/usr"] = "usr" }, "absolute"},
		{"an unclean real path", nil, func(m map[string]string) { m["/usr"] = "/usr/../usr" }, "clean"},
		{"an empty real path", nil, func(m map[string]string) { m["/usr"] = "" }, "empty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			present := tt.present
			if present == nil {
				present = allPresent(t, in)
			}
			o := options(in, present)
			tt.change(o.HostSources)
			_, err := Build(in, o)
			if err == nil {
				t.Fatal("Build accepted it")
			}
			if r := refusal(t, err); !strings.Contains(r.Reason, tt.want) {
				t.Errorf("refusal %+v does not say %q", r, tt.want)
			}
			probs := Verify(mustBuildPlain(t, in, present), in, o)
			if !hasKind(probs, ProbOptions) {
				t.Errorf("Verify does not report the options: %v", probs)
			}
		})
	}
}

// mustBuildPlain builds the plan for a host without links.
func mustBuildPlain(t *testing.T, in profile.Inputs, present map[string]bool) *Plan {
	t.Helper()
	return mustBuild(t, in, options(in, present))
}

// A real path is checked like any other source: never-mounted paths, the
// folders a lead writes, and the other runtime's config folder.
func TestHostSourcesAreCheckedLikeAnySource(t *testing.T) {
	in := fixture(profile.Claude)
	present := allPresent(t, in)
	for name, real := range map[string]string{
		"a never-mounted path":               "/home/owner/.ssh/id",
		"above a never-mounted path":         "/home/owner",
		"in the lead's home":                 "/state/leads/arc-lead/home/hosts",
		"in the lead's tree":                 "/state/leads/arc-lead/tree/hosts",
		"in the other runtime's config":      "/state/leads/arc-lead/config/codex/hosts",
		"in another lead's state":            "/state/leads/other/home/hosts",
		"in another project's mirror folder": "/state/mirrors/other.git/hosts",
	} {
		t.Run(name, func(t *testing.T) {
			o := options(in, present)
			o.HostSources["/etc/hosts"] = real
			if _, err := Build(in, o); err == nil {
				t.Fatal("Build accepted it")
			}
			// A plan built for a clean host and verified against these
			// options says what is wrong.
			probs := Verify(mustBuildPlain(t, in, present), in, o)
			if len(probs) == 0 {
				t.Error("Verify accepted it")
			}
		})
	}
	// Verify also reports a tampered plan that binds such a path.
	q := clonePlan(mustBuildPlain(t, in, present))
	i := find(t, q, "--ro-bind", "/etc/hosts")
	q.Bwrap[i+1] = "/state/leads/arc-lead/home/hosts"
	resync(q)
	probs := Verify(q, in, options(in, present))
	if !hasKind(probs, ProbLeadWritable) || !hasKind(probs, ProbMountDiffers) {
		t.Errorf("a host row bound from the lead's home: %v", probs)
	}
}

// Without options for them, no host row is a problem when the plan emits none:
// there is always /usr, so the map is never empty, and a nil map is refused.
func TestNoHostSourcesIsRefused(t *testing.T) {
	in := fixture(profile.Claude)
	o := options(in, nil)
	o.HostSources = nil
	_, err := Build(in, o)
	if r := refusal(t, err); !strings.Contains(r.Reason, "/usr has no resolved source") {
		t.Errorf("refusal %+v", r)
	}
}
