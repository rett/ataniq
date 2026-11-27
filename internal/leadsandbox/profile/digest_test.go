package profile

import (
	"reflect"
	"regexp"
	"strings"
	"testing"

	"ataniq/internal/leadsandbox/sandboxnet"
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
	in.GitIdentityDir = "/run/ataniq/git"
	in.QargiInstall = "/opt/qargi"
	in.AssignmentDir = "/run/ataniq/assignment"
	in.ProjectionDir = "/run/ataniq/projection"
	in.ResolvConf = "/run/ataniq/resolv.conf"
	in.GitMeta = in.Layout().GitMeta()
	in.NeverMounted = []string{"/home/someone/checkout", "/home/someone/.ssh"}
	in.Term, in.ColorTerm, in.Lang, in.TZ = "screen", "", "en_US.UTF-8", "Asia/Tokyo"
	in.LeadIdentity = "another-project-lead-fedcba9876543210"
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
	goldenClaude = "89b1f8a2eddf83873b6b4b9362786b0ae959b96967b42ae2a5461738bceb6b7f"
	goldenCodex  = "a4f78d7d1ea3b224dce875cebbd55e50dfe53041388adcfdc1ad69396cf8a5c5"
)

func TestVersionIsFour(t *testing.T) {
	// The layout of the lead-hosting design, revision 4: the requests,
	// mailbox, mail and coordinator rows and the records row are in every
	// table, and the git identity is bound as its folder.
	if Version != 4 {
		t.Errorf("Version = %d, want 4", Version)
	}
}

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
		{"state root", func(in *Inputs) { in.StateRoot = "/elsewhere/state"; in.GitMeta = in.Layout().GitMeta() }},
		{"role", func(in *Inputs) { in.Role = "other"; in.GitMeta = in.Layout().GitMeta() }},
		{"project", func(in *Inputs) { in.Project = "other" }},
		{"agent install", func(in *Inputs) { in.AgentInstall = "/a/b" }},
		{"never-mounted paths", func(in *Inputs) { in.NeverMounted = []string{"/x"} }},
		{"TERM", func(in *Inputs) { in.Term = "linux" }},
		{"COLORTERM unset", func(in *Inputs) { in.ColorTerm = "" }},
		{"LANG", func(in *Inputs) { in.Lang = "de_DE.UTF-8" }},
		{"TZ", func(in *Inputs) { in.TZ = "" }},
		{"lead identity", func(in *Inputs) { in.LeadIdentity = "x-lead" }},
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
		{"the inside path of the Qargi records", func(in *Inputs) { in.QargiRecordsInside = "/ataniq/records" }},
		{"another inside path of the Qargi records", func(in *Inputs) { in.QargiRecordsInside = "/home/lead/.local/qargi" }},
		{"an environment name", func(in *Inputs) { in.ExtraEnv = []EnvVar{{"GOFLAGS", "-mod=mod"}} }},
		{"the managed-settings digest", func(in *Inputs) { in.ManagedSettingsSHA256 = strings.Repeat("d", 64) }},
		{"PATH", func(in *Inputs) { in.Path = "/home/lead/.local/bin:/usr/bin" }},
		{"SHELL", func(in *Inputs) { in.Shell = "/home/lead/sh" }},
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
	// An environment name changes it, and so does the value of an added
	// name: the profile defines the value.
	d, e := base, base
	d.ExtraEnv = []EnvVar{{"GOFLAGS", "-mod=mod"}}
	e.ExtraEnv = []EnvVar{{"GOFLAGS", "-mod=vendor"}}
	if mustDigest(t, d) == mustDigest(t, e) {
		t.Error("the value of an added variable does not change the digest")
	}
	// The values of the variables a lead's boundary depends on: a changed
	// PATH, SHELL or proxy is a changed profile. The default value is the
	// value the profile defines, so naming it is no change.
	for _, tt := range []struct {
		name   string
		mutate func(*Inputs)
		same   bool
	}{
		{"PATH with a lead-writable folder first", func(in *Inputs) { in.Path = "/home/lead/.local/bin:" + DefaultPath }, false},
		{"PATH as the default", func(in *Inputs) { in.Path = DefaultPath }, true},
		{"SHELL", func(in *Inputs) { in.Shell = "/bin/sh" }, false},
		{"SHELL as the default", func(in *Inputs) { in.Shell = DefaultShell }, true},
		{"a proxy", func(in *Inputs) { in.ExtraEnv = []EnvVar{{"HTTPS_PROXY", "http://a"}} }, false},
	} {
		x := base
		tt.mutate(&x)
		if same := mustDigest(t, x) == baseDigest; same != tt.same {
			t.Errorf("%s: same digest = %v, want %v", tt.name, same, tt.same)
		}
	}
	p1, p2 := base, base
	p1.ExtraEnv = []EnvVar{{"HTTPS_PROXY", "http://a"}}
	p2.ExtraEnv = []EnvVar{{"HTTPS_PROXY", "http://b"}}
	if mustDigest(t, p1) == mustDigest(t, p2) {
		t.Error("a proxy value does not change the digest")
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
		{"a bind is optional", func(d *definition) { d.Binds[6].Optional = !d.Binds[6].Optional }},
		{"a link is optional", func(d *definition) { d.Binds[4].Optional = !d.Binds[4].Optional }},
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
		{"an environment value", func(d *definition) { d.EnvValues[0].Value += "x" }},
		{"an environment value's name", func(d *definition) { d.EnvValues[0].Name += "X" }},
		{"an environment value added", func(d *definition) { d.EnvValues = append(d.EnvValues, EnvVar{"ZZZ", "1"}) }},
		{"an environment value removed", func(d *definition) { d.EnvValues = d.EnvValues[1:] }},
		{"environment values reordered", func(d *definition) { d.EnvValues[0], d.EnvValues[1] = d.EnvValues[1], d.EnvValues[0] }},
		{"seccomp digest", func(d *definition) { d.Seccomp = strings.Repeat("c", 64) }},
		{"managed-settings digest", func(d *definition) { d.ManagedSettings = strings.Repeat("d", 64) }},
		{"a network argument", func(d *definition) { d.Network[0] = "-F" }},
		{"a network argument added", func(d *definition) { d.Network = append(d.Network, "--extra") }},
		{"a network argument removed", func(d *definition) { d.Network = d.Network[:len(d.Network)-1] }},
		{"network arguments reordered", func(d *definition) { d.Network[0], d.Network[1] = d.Network[1], d.Network[0] }},
		{"no network arguments", func(d *definition) { d.Network = nil }},
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
	a = fixture(Claude).definition()
	b = fixture(Claude).definition()
	a.EnvValues = []EnvVar{{"AB", "C"}}
	b.EnvValues = []EnvVar{{"A", "BC"}}
	if a.digest() == b.digest() {
		t.Error("moving a boundary between a name and its value keeps the digest")
	}
	a.EnvValues = []EnvVar{{"A", "B"}, {"C", "D"}}
	b.EnvValues = []EnvVar{{"A", "B"}, {"C", ""}, {"D", ""}}
	if a.digest() == b.digest() {
		t.Error("moving a boundary between two variables keeps the digest")
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

func TestDigestWithRecordsIsIndependentOfTheProject(t *testing.T) {
	for _, rt := range Runtimes() {
		for _, inside := range []string{"/home/lead/.qargi", "/ataniq/records"} {
			a, b := fixture(rt), fixture(rt)
			a.QargiRecordsInside, b.QargiRecordsInside = inside, inside
			b.Project = "another-project"
			if mustDigest(t, a) != mustDigest(t, b) {
				t.Errorf("%s: two projects, two digests, with the records at %s", rt, inside)
			}
		}
	}
}

// Every lead has the records row: it is in the table and in the digest for both
// runtimes, from the layout's folder, at the inside path the caller gives.
func TestTheRecordsRowIsInEveryTableAndDigest(t *testing.T) {
	for _, rt := range Runtimes() {
		for name, in := range map[string]Inputs{"plain": fixture(rt), "full": fullInputs(rt)} {
			def := in.definition()
			n, at := 0, -1
			for i, b := range def.Binds {
				if b.SourceRole == RoleRecords {
					n++
					at = i
				}
			}
			if n != 1 || def.Binds[at].Inside != in.QargiRecordsInside || def.Binds[at].Kind != KindRwBind {
				t.Fatalf("%s %s: %d records rows in the definition", rt, name, n)
			}
			for _, b := range mustBinds(t, in) {
				if b.SourceRole == RoleRecords && b.Source != "/srv/state/leads/arc-lead/qargi" {
					t.Errorf("%s %s: the records row comes from %s", rt, name, b.Source)
				}
			}
			base := def.digest()
			def.Binds = append(def.Binds[:at:at], def.Binds[at+1:]...)
			if def.digest() == base {
				t.Errorf("%s %s: the digest is the same without the records row", rt, name)
			}
		}
	}
}

func TestDigestEnvironmentValues(t *testing.T) {
	// Which values the digest carries: those the profile defines. The host
	// terminal's values, the lead identity and, in owner mode, the user name
	// are names only.
	has := func(d definition, name string) (string, bool) {
		for _, e := range d.EnvValues {
			if e.Name == name {
				return e.Value, true
			}
		}
		return "", false
	}
	root := fixture(Claude)
	root.Path, root.Shell = "/usr/bin", "/bin/sh"
	root.ExtraEnv = []EnvVar{{"GOFLAGS", "-mod=mod"}}
	d := root.definition()
	for name, want := range map[string]string{
		"PATH": "/usr/bin", "SHELL": "/bin/sh", "HOME": HomeInside, "USER": "root", "LOGNAME": "root",
		"TMPDIR": TmpInside, "XDG_CONFIG_HOME": HomeInside + "/.config", "XDG_DATA_HOME": HomeInside + "/.local/share",
		"XDG_STATE_HOME": HomeInside + "/.local/state", "XDG_CACHE_HOME": HomeInside + "/.cache",
		"CLAUDE_CONFIG_DIR": ConfigInside(Claude), "GIT_CEILING_DIRECTORIES": WorkRootInside,
		"GIT_CONFIG_GLOBAL": GitIdentityInside, "GOCACHE": GoCacheInside, "GOMODCACHE": GoModCacheInside,
		"GOFLAGS": "-mod=mod",
	} {
		if got, ok := has(d, name); !ok || got != want {
			t.Errorf("%s = %q (present %v), want %q", name, got, ok, want)
		}
	}
	if got, ok := has(d, EnvQargiLead); !ok || got != "<project>-lead-<authority-prefix-16hex>" {
		t.Errorf("digest QARGI_LEAD rule = %q", got)
	}
	for _, name := range []string{"TERM", "COLORTERM", "LANG", "TZ"} {
		if _, ok := has(d, name); ok {
			t.Errorf("the digest carries the value of %s", name)
		}
		if !contains(d.EnvNames, name) {
			t.Errorf("the digest lacks the name %s", name)
		}
	}
	if len(d.EnvValues) != len(d.EnvNames)-4 {
		t.Errorf("%d values for %d names", len(d.EnvValues), len(d.EnvNames))
	}
	// Sorted.
	for i := 1; i < len(d.EnvValues); i++ {
		if d.EnvValues[i-1].Name >= d.EnvValues[i].Name {
			t.Errorf("values not sorted at %s", d.EnvValues[i].Name)
		}
	}
	// Owner mode: the user and login names are the owner's.
	o := fixture(Claude)
	o.Identity, o.OwnerUID, o.OwnerGID, o.OwnerUser = IdentityOwner, 1000, 1000, "alice"
	od := o.definition()
	for _, name := range []string{"USER", "LOGNAME"} {
		if _, ok := has(od, name); ok {
			t.Errorf("owner mode: the digest carries the value of %s", name)
		}
		if !contains(od.EnvNames, name) {
			t.Errorf("owner mode: the digest lacks the name %s", name)
		}
	}
	// The digest holds none of the host values.
	if enc := string(o.definition().encode()); strings.Contains(enc, "alice") || strings.Contains(enc, "xterm-256color") ||
		strings.Contains(enc, o.LeadIdentity) || strings.Contains(enc, "C.UTF-8") {
		t.Error("the encoding holds a host or lead value")
	}
}

func TestDigestCoversTheManagedSettingsAndTheNetwork(t *testing.T) {
	for _, rt := range Runtimes() {
		in := fixture(rt)
		d := in.definition()
		if d.ManagedSettings != in.ManagedSettingsSHA256 {
			t.Errorf("%s: the definition holds %q, want the caller's digest", rt, d.ManagedSettings)
		}
		if !reflect.DeepEqual(d.Network, sandboxnet.PastaArgs()) || len(d.Network) == 0 {
			t.Errorf("%s: network = %v, want sandboxnet.PastaArgs()", rt, d.Network)
		}
		// Two managed-settings contents, two digests; the same content on
		// two hosts, one.
		a, b := in, in
		b.ManagedSettingsSHA256 = strings.Repeat("e", 64)
		if mustDigest(t, a) == mustDigest(t, b) {
			t.Errorf("%s: the managed-settings digest does not change the digest", rt)
		}
		if mustDigest(t, in) != mustDigest(t, otherHost(rt)) {
			t.Errorf("%s: the same managed-settings content on another host changes the digest", rt)
		}
	}
	// The network arguments are copied: a caller cannot change the shared list.
	d := fixture(Claude).definition()
	d.Network[0] = "changed"
	if sandboxnet.PastaArgs()[0] == "changed" {
		t.Error("the definition shares the pasta argument slice")
	}
}

func TestDigestIsIndependentOfThePlaceholderOrder(t *testing.T) {
	a, b := fixture(Claude), fixture(Claude)
	a.ExtraPlaceholders = []Placeholder{{Rel: ".a", Source: "/s/a"}, {Rel: ".b", Source: "/s/b", Dir: true}, {Rel: "sub/c", Source: "/s/c"}}
	b.ExtraPlaceholders = []Placeholder{a.ExtraPlaceholders[2], a.ExtraPlaceholders[0], a.ExtraPlaceholders[1]}
	if mustDigest(t, a) != mustDigest(t, b) {
		t.Error("the order of the extra placeholders changes the digest")
	}
	// The caller's slice is not reordered.
	if b.ExtraPlaceholders[0].Rel != "sub/c" {
		t.Error("Digest reordered the caller's placeholders")
	}
	// The table the plan gets keeps the caller's order.
	rowsB := rows(mustBinds(t, b))
	rowsA := rows(mustBinds(t, a))
	if reflect.DeepEqual(rowsA, rowsB) {
		t.Error("Binds sorted the placeholders: the digest should, not the table")
	}
	// A different set of placeholders still differs.
	c := a
	c.ExtraPlaceholders = a.ExtraPlaceholders[:2]
	if mustDigest(t, a) == mustDigest(t, c) {
		t.Error("dropping a placeholder keeps the digest")
	}
	// Overlapping placeholders stay refused, so sorting cannot hide an
	// order that bubblewrap needs.
	d := fixture(Claude)
	d.ExtraPlaceholders = []Placeholder{{Rel: "x/y", Source: "/s/y"}, {Rel: "x", Source: "/s/x", Dir: true}}
	if _, err := d.Digest(); err == nil {
		t.Error("a placeholder below another one is accepted")
	}
}

// fullInputs are the fixture with every optional input given (the owner
// identity, a seccomp file, extra placeholders and an extra variable) and the
// Qargi records at another inside path, outside the home.
func fullInputs(rt Runtime) Inputs {
	in := fixture(rt)
	in.Identity, in.OwnerUID, in.OwnerGID, in.OwnerUser = IdentityOwner, 1000, 1000, "owner"
	in.SeccompFile, in.SeccompSHA256 = "/srv/owned/seccomp/reviewed.bpf", strings.Repeat("ab", 32)
	in.QargiRecordsInside = "/ataniq/records"
	in.ExtraPlaceholders = []Placeholder{
		{Rel: ".mcp.json", Source: "/srv/owned/placeholders/mcp.json"},
		{Rel: ".vscode", Source: "/srv/owned/placeholders/vscode", Dir: true},
	}
	in.ExtraEnv = []EnvVar{{"GOFLAGS", "-mod=mod"}}
	return in
}

// The four folders are fixed at the lead's start, because a bind cannot be
// added to a running sandbox: the rows are in the table and in the digest for
// both runtimes, with and without every optional input, and the digest shows
// each of them.
func TestTheFourLayoutRowsAreInEveryTableAndDigest(t *testing.T) {
	want := []Bind{
		{Kind: KindRwBind, Inside: "/ataniq/requests", Source: "/srv/state/leads/arc-lead/requests", SourceRole: RoleRequests, IsDir: true},
		{Kind: KindRoBind, Inside: "/ataniq/mailbox", Source: "/srv/state/mailbox/arc-lead", SourceRole: RoleMailbox, IsDir: true},
		{Kind: KindRwBind, Inside: "/ataniq/mail", Source: "/srv/state/leads/arc-lead/mail", SourceRole: RoleMail, IsDir: true},
		{Kind: KindRoBind, Inside: "/coord", Source: "/srv/state/pub/ataniq", SourceRole: RolePublished, IsDir: true},
	}
	find := func(t []Bind, inside string) (int, bool) {
		for i, b := range t {
			if b.Inside == inside {
				return i, true
			}
		}
		return 0, false
	}
	for _, rt := range Runtimes() {
		for name, in := range map[string]Inputs{"plain": fixture(rt), "full": fullInputs(rt)} {
			table := mustBinds(t, in)
			def := in.definition()
			base := def.digest()
			if got := mustDigest(t, in); got != base {
				t.Fatalf("%s %s: Digest = %s, the definition's is %s", rt, name, got, base)
			}
			for _, w := range want {
				i, ok := find(table, w.Inside)
				if !ok {
					t.Errorf("%s %s: the table has no row %s", rt, name, w.Inside)
					continue
				}
				if table[i] != w {
					t.Errorf("%s %s: row %s = %+v, want %+v", rt, name, w.Inside, table[i], w)
				}
				// The definition has the row, with its role in place of the
				// host path, and the digest changes when the row is taken out,
				// when it changes its kind and when it changes its role.
				j, ok := find(def.Binds, w.Inside)
				inDigest := w
				inDigest.Source = ""
				if !ok || def.Binds[j] != inDigest {
					t.Errorf("%s %s: the definition lacks the row %s", rt, name, w.Inside)
					continue
				}
				for change, mutate := range map[string]func(*definition){
					"taken out": func(d *definition) { d.Binds = append(d.Binds[:j:j], d.Binds[j+1:]...) },
					"another kind": func(d *definition) {
						d.Binds = append([]Bind(nil), d.Binds...)
						if d.Binds[j].Kind = KindRoBind; w.Kind == KindRoBind {
							d.Binds[j].Kind = KindRwBind
						}
					},
					"another role": func(d *definition) {
						d.Binds = append([]Bind(nil), d.Binds...)
						d.Binds[j].SourceRole = RoleExtra
					},
				} {
					d := in.definition()
					mutate(&d)
					if d.digest() == base {
						t.Errorf("%s %s: the row %s %s keeps the digest", rt, name, w.Inside, change)
					}
				}
			}
		}
	}
	// The same four rows for another role, project and state root: other
	// sources, the same digest.
	for _, rt := range Runtimes() {
		table := mustBinds(t, otherHost(rt))
		for inside, source := range map[string]string{
			"/ataniq/requests": "/var/lib/other/state/leads/second-lead/requests",
			"/ataniq/mailbox":  "/var/lib/other/state/mailbox/second-lead",
			"/ataniq/mail":     "/var/lib/other/state/leads/second-lead/mail",
			"/coord":           "/var/lib/other/state/pub/another-project",
		} {
			if i, ok := find(table, inside); !ok || table[i].Source != source {
				t.Errorf("%s: the row %s of another lead does not come from %s", rt, inside, source)
			}
		}
	}
}
