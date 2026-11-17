package profile

import (
	"regexp"
	"strings"
	"testing"
)

func mustDigest(t *testing.T, in Inputs) string {
	t.Helper()
	d, err := in.Digest()
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	return d
}

// otherHost is the same profile for another state root, role, project and set
// of host paths.
func otherHost(rt Runtime) Inputs {
	in := fixture(rt)
	in.StateRoot = "/var/lib/other/state"
	in.Role = "second-lead"
	in.Project = "another-project"
	in.AgentInstall = "/opt/agents/" + string(rt)
	in.ProbeBinary = "/opt/ataniq/probe"
	in.SpecDir = "/run/ataniq/spec"
	in.ManagedSettings = "/run/ataniq/managed.json"
	in.LeadSkills = "/opt/ataniq/skills"
	in.InstructionFile = "/opt/ataniq/instructions.md"
	in.GitIdentityFile = "/run/ataniq/gitconfig"
	in.QargiInstall = "/opt/qargi"
	in.AssignmentDir = "/run/ataniq/assignment"
	in.ProjectionDir = "/run/ataniq/projection"
	in.ResolvConf = "/run/ataniq/resolv.conf"
	in.NeverMounted = []string{"/home/someone/checkout", "/home/someone/.ssh"}
	in.Term, in.ColorTerm, in.Lang, in.TZ = "screen", "", "en_US.UTF-8", "Asia/Tokyo"
	in.LeadIdentity = "another-project/lead"
	in.Shell = "/bin/sh"
	in.Path = "/usr/bin:/bin"
	return in
}

func TestDigestShape(t *testing.T) {
	d := mustDigest(t, fixture(Claude))
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(d) {
		t.Errorf("digest = %q, want 64 lower-case hex digits", d)
	}
	if d != mustDigest(t, fixture(Claude)) {
		t.Error("the digest is not deterministic")
	}
}

const (
	goldenClaude = "323942e67fa85e976440e31d603d1b728cab8c3de57a315e72513f6f7f0fd2f4"
	goldenCodex  = "d17eed5ffb037c8fec0c06fe747dbc2fea892847f115d03b123d1dc8a15a71e1"
)

func TestDigestGolden(t *testing.T) {
	// A change here is a change to the profile's data or to the encoding:
	// raise Version when it is meant, and update these.
	tests := []struct {
		rt   Runtime
		want string
	}{
		{Claude, goldenClaude},
		{Codex, goldenCodex},
	}
	for _, tt := range tests {
		if got := mustDigest(t, fixture(tt.rt)); got != tt.want {
			t.Errorf("%s digest = %s\nwant %s", tt.rt, got, tt.want)
		}
	}
}

func TestDigestIsIndependentOfHostAndLead(t *testing.T) {
	for _, rt := range Runtimes() {
		if a, b := mustDigest(t, fixture(rt)), mustDigest(t, otherHost(rt)); a != b {
			t.Errorf("%s: two hosts, two digests: %s and %s", rt, a, b)
		}
	}
	// Each input on its own.
	tests := []struct {
		name   string
		mutate func(*Inputs)
	}{
		{"state root", func(in *Inputs) { in.StateRoot = "/elsewhere/state" }},
		{"role", func(in *Inputs) { in.Role = "other" }},
		{"project", func(in *Inputs) { in.Project = "other" }},
		{"agent install", func(in *Inputs) { in.AgentInstall = "/a/b" }},
		{"never-mounted paths", func(in *Inputs) { in.NeverMounted = []string{"/x"} }},
		{"TERM", func(in *Inputs) { in.Term = "linux" }},
		{"COLORTERM unset", func(in *Inputs) { in.ColorTerm = "" }},
		{"LANG", func(in *Inputs) { in.Lang = "de_DE.UTF-8" }},
		{"TZ", func(in *Inputs) { in.TZ = "" }},
		{"lead identity", func(in *Inputs) { in.LeadIdentity = "x/lead" }},
		{"extra placeholder source", func(in *Inputs) {
			in.ExtraPlaceholders = []Placeholder{{Rel: ".a", Source: "/s/1"}}
		}},
	}
	base := fixture(Claude)
	base.ExtraPlaceholders = []Placeholder{{Rel: ".a", Source: "/s/0"}}
	want := mustDigest(t, base)
	for _, tt := range tests {
		in := base
		tt.mutate(&in)
		if got := mustDigest(t, in); got != want {
			t.Errorf("%s changes the digest", tt.name)
		}
	}
	// The owner's ids are host data: two owners, one digest.
	a, b := fixture(Claude), fixture(Claude)
	a.Identity, a.OwnerUID, a.OwnerGID, a.OwnerUser = IdentityOwner, 1000, 1000, "alice"
	b.Identity, b.OwnerUID, b.OwnerGID, b.OwnerUser = IdentityOwner, 4242, 4343, "bob"
	if mustDigest(t, a) != mustDigest(t, b) {
		t.Error("the owner's ids change the digest")
	}
}

func TestDigestChangesWithTheProfile(t *testing.T) {
	base := fixture(Claude)
	baseDigest := mustDigest(t, base)
	withSeccomp := func(sum string) func(*Inputs) {
		return func(in *Inputs) { in.SeccompFile, in.SeccompSHA256 = "/srv/owned/r.bpf", sum }
	}
	tests := []struct {
		name   string
		mutate func(*Inputs)
	}{
		{"the other runtime", func(in *Inputs) {
			*in = fixture(Codex)
			in.ManagedSettingsInside = ClaudeManagedInside
		}},
		{"the pinned version", func(in *Inputs) { in.RuntimeVersion = "2.1.1" }},
		{"the identity mode", func(in *Inputs) {
			in.Identity, in.OwnerUID, in.OwnerGID, in.OwnerUser = IdentityOwner, 1000, 1000, "owner"
		}},
		{"a seccomp file", withSeccomp(strings.Repeat("a", 64))},
		{"an extra placeholder", func(in *Inputs) { in.ExtraPlaceholders = []Placeholder{{Rel: ".envrc", Source: "/s/envrc"}} }},
		{"an extra placeholder as a folder", func(in *Inputs) { in.ExtraPlaceholders = []Placeholder{{Rel: ".envrc", Source: "/s/envrc", Dir: true}} }},
		{"the Qargi records", func(in *Inputs) { in.QargiRecords, in.QargiRecordsInside = "/srv/state/q", "/home/lead/.qargi" }},
		{"an environment name", func(in *Inputs) { in.ExtraEnv = []EnvVar{{"GOFLAGS", "-mod=mod"}} }},
	}
	seen := map[string]string{baseDigest: "the base"}
	for _, tt := range tests {
		in := base
		tt.mutate(&in)
		got := mustDigest(t, in)
		if prev, dup := seen[got]; dup {
			t.Errorf("%s gives the digest of %s", tt.name, prev)
		}
		seen[got] = tt.name
	}
	// Codex's managed-settings path is part of the profile.
	c1, c2 := fixture(Codex), fixture(Codex)
	c2.ManagedSettingsInside = "/etc/other/managed.toml"
	if mustDigest(t, c1) == mustDigest(t, c2) {
		t.Error("the codex managed-settings path does not change the digest")
	}
	// Two seccomp digests differ; none differs from a file.
	a, b := base, base
	withSeccomp(strings.Repeat("a", 64))(&a)
	withSeccomp(strings.Repeat("b", 64))(&b)
	if mustDigest(t, a) == mustDigest(t, b) {
		t.Error("the seccomp digest does not change the digest")
	}
	// The seccomp file's path is host data.
	c := a
	c.SeccompFile = "/elsewhere/other.bpf"
	if mustDigest(t, a) != mustDigest(t, c) {
		t.Error("the seccomp file's path changes the digest")
	}
	// An environment name changes it; a value of an added name does not.
	d, e := base, base
	d.ExtraEnv = []EnvVar{{"GOFLAGS", "-mod=mod"}}
	e.ExtraEnv = []EnvVar{{"GOFLAGS", "-mod=vendor"}}
	if mustDigest(t, d) != mustDigest(t, e) {
		t.Error("an environment value changes the digest")
	}
	// The order of added names does not matter.
	f, g := base, base
	f.ExtraEnv = []EnvVar{{"A", "1"}, {"B", "1"}}
	g.ExtraEnv = []EnvVar{{"B", "1"}, {"A", "1"}}
	if mustDigest(t, f) != mustDigest(t, g) {
		t.Error("the order of added names changes the digest")
	}
}

func TestDigestRefusesInvalidInputs(t *testing.T) {
	in := fixture(Claude)
	in.Role = "Bad Role"
	if _, err := in.Digest(); err == nil {
		t.Error("Digest accepted invalid inputs")
	}
}

func TestDigestChangesWithEveryPartOfTheDefinition(t *testing.T) {
	def := func() definition { return fixture(Claude).definition() }
	base := def().digest()
	tests := []struct {
		name   string
		mutate func(*definition)
	}{
		{"version", func(d *definition) { d.Version++ }},
		{"runtime", func(d *definition) { d.Runtime = Codex }},
		{"pinned version", func(d *definition) { d.RuntimeVersion += "x" }},
		{"identity", func(d *definition) { d.Identity = IdentityOwner }},
		{"a flag changed", func(d *definition) { d.Flags[len(d.Flags)-1] = "--clearenv=no" }},
		{"a flag added", func(d *definition) { d.Flags = append(d.Flags, "--new-session") }},
		{"a flag removed", func(d *definition) { d.Flags = d.Flags[:len(d.Flags)-1] }},
		{"flags reordered", func(d *definition) { d.Flags[0], d.Flags[1] = d.Flags[1], d.Flags[0] }},
		{"a bind kind", func(d *definition) { d.Binds[1].Kind = KindRwBind }},
		{"a bind inside path", func(d *definition) { d.Binds[1].Inside = "/usr2" }},
		{"a bind source role", func(d *definition) { d.Binds[1].SourceRole = RoleHome }},
		{"a link target", func(d *definition) { d.Binds[2].Target = "usr/sbin" }},
		{"a bind is a folder", func(d *definition) { d.Binds[1].IsDir = !d.Binds[1].IsDir }},
		{"a bind is lead-writable", func(d *definition) { d.Binds[1].LeadWritable = true }},
		{"a bind's holder", func(d *definition) { d.Binds[1].Within = "/home/lead" }},
		{"a bind added", func(d *definition) {
			d.Binds = append(d.Binds, Bind{Kind: KindRoBind, Inside: "/extra", SourceRole: RoleExtra})
		}},
		{"a bind removed", func(d *definition) { d.Binds = d.Binds[:len(d.Binds)-1] }},
		{"binds reordered", func(d *definition) { d.Binds[3], d.Binds[4] = d.Binds[4], d.Binds[3] }},
		{"an environment name", func(d *definition) { d.EnvNames[0] += "X" }},
		{"an environment name added", func(d *definition) { d.EnvNames = append(d.EnvNames, "ZZZ") }},
		{"an environment name removed", func(d *definition) { d.EnvNames = d.EnvNames[1:] }},
		{"seccomp digest", func(d *definition) { d.Seccomp = strings.Repeat("c", 64) }},
	}
	for _, tt := range tests {
		d := def()
		tt.mutate(&d)
		if d.digest() == base {
			t.Errorf("%s does not change the digest", tt.name)
		}
	}
	if def().digest() != base {
		t.Error("definition is not repeatable")
	}
}

func TestDigestEncodingHasNoAmbiguity(t *testing.T) {
	// A field boundary moved must not give the same bytes.
	a := fixture(Claude).definition()
	b := fixture(Claude).definition()
	a.Runtime, a.RuntimeVersion = "ab", "c"
	b.Runtime, b.RuntimeVersion = "a", "bc"
	if a.digest() == b.digest() {
		t.Error("moving a boundary between runtime and version keeps the digest")
	}
	a = fixture(Claude).definition()
	b = fixture(Claude).definition()
	a.EnvNames = []string{"AB", "C"}
	b.EnvNames = []string{"A", "BC"}
	if a.digest() == b.digest() {
		t.Error("moving a boundary between names keeps the digest")
	}
}

func TestDefinitionHoldsNoHostPath(t *testing.T) {
	d := otherHost(Codex).definition()
	for _, b := range d.Binds {
		if b.Source != "" {
			t.Errorf("%s: the definition keeps the host path %s", b.Inside, b.Source)
		}
		for _, s := range []string{b.Inside, b.Within, b.Target} {
			for _, host := range []string{"another-project", "second-lead", "/var/lib/other"} {
				if strings.Contains(s, host) {
					t.Errorf("%s: %q holds %q", b.Inside, s, host)
				}
			}
		}
	}
	if d.Seccomp != "none" {
		t.Errorf("seccomp = %q, want none", d.Seccomp)
	}
	if len(d.Flags) != 11 || d.Flags[5] != "0" {
		t.Errorf("flags = %v", d.Flags)
	}
	o := fixture(Claude)
	o.Identity, o.OwnerUID, o.OwnerGID, o.OwnerUser = IdentityOwner, 1000, 1000, "owner"
	if f := o.definition().Flags; f[5] != ownerUIDToken || f[7] != ownerGIDToken {
		t.Errorf("owner flags = %v", f)
	}
}
