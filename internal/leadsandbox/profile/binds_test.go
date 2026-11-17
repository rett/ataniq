package profile

import (
	"reflect"
	"strings"
	"testing"
)

// goldenRows is the bind table of the lead-hosting design (section 3.3), one
// line per row, for a runtime whose config folder is CFG, whose instruction
// file is INSTR and whose managed-settings path is MANAGED.
var goldenRows = []string{
	"tmpfs /",
	"ro-bind /usr src=host",
	"symlink /bin -> usr/bin",
	"symlink /lib -> usr/lib",
	"symlink /lib64 -> usr/lib64 optional",
	"symlink /sbin -> usr/sbin optional",
	"ro-bind /etc/ssl src=host optional",
	"ro-bind /etc/ca-certificates src=host optional",
	"ro-bind /etc/hosts src=host",
	"ro-bind /etc/nsswitch.conf src=host optional",
	"ro-bind /etc/passwd src=host",
	"ro-bind /etc/group src=host",
	"ro-bind /etc/localtime src=host optional",
	"ro-bind /etc/resolv.conf src=resolv-conf",
	"dev /dev",
	"proc /proc",
	"tmpfs /tmp",
	"ro-bind /ataniq/probe src=probe",
	"ro-bind /ataniq/spec src=spec",
	"ro-bind /ataniq/agent src=agent",
	"ro-bind MANAGED src=managed-settings",
	"rw-bind /home/lead src=home",
	"rw-bind /home/lead/CFG src=config writable-in=/home/lead",
	"ro-bind /home/lead/CFG/skills src=skills writable-in=/home/lead/CFG",
	"ro-bind /home/lead/CFG/INSTR src=instructions writable-in=/home/lead/CFG",
	"ro-bind /ataniq/git/config src=git-identity",
	"rw-bind /work/ataniq src=tree",
	"ro-bind /work/ataniq/.git/config src=tree-git writable-in=/work/ataniq",
	"ro-bind /work/ataniq/.git/hooks src=tree-git writable-in=/work/ataniq",
	"ro-bind /work/ataniq/.git/info src=tree-git writable-in=/work/ataniq",
	"ro-bind /ataniq/mirror/ataniq.git src=mirror",
	"ro-bind /ataniq/qargi src=qargi",
	"ro-bind /ataniq/assignment src=assignment",
	"ro-bind /ataniq/projection src=projection",
	"remount-ro /",
}

func golden(rt Runtime, managed string) []string {
	r := strings.NewReplacer("CFG", rt.ConfigDirName(), "INSTR", rt.InstructionFileName(), "MANAGED", managed)
	out := make([]string, len(goldenRows))
	for i, s := range goldenRows {
		out[i] = r.Replace(s)
	}
	return out
}

func TestBindTableEveryRowForEachRuntime(t *testing.T) {
	tests := []struct {
		rt      Runtime
		managed string
	}{
		{Claude, "/etc/claude-code/managed-settings.json"},
		{Codex, "/etc/codex/managed-config.toml"},
	}
	for _, tt := range tests {
		t.Run(string(tt.rt), func(t *testing.T) {
			table := mustBinds(t, fixture(tt.rt))
			if got, want := rows(table), golden(tt.rt, tt.managed); !reflect.DeepEqual(got, want) {
				t.Errorf("bind table differs\n got: %q\nwant: %q", got, want)
			}
			if err := ValidateTable(table); err != nil {
				t.Errorf("ValidateTable: %v", err)
			}
		})
	}
}

func TestBindTableSourcesAndAccess(t *testing.T) {
	in := fixture(Claude)
	table := mustBinds(t, in)
	byInside := map[string]Bind{}
	for _, b := range table {
		byInside[b.Inside] = b
	}
	tests := []struct {
		inside string
		source string
		kind   Kind
		dir    bool
	}{
		{"/usr", "/usr", KindRoBind, true},
		{"/etc/localtime", "/etc/localtime", KindRoBind, false},
		{"/etc/resolv.conf", in.ResolvConf, KindRoBind, false},
		{"/ataniq/probe", in.ProbeBinary, KindRoBind, false},
		{"/ataniq/spec", in.SpecDir, KindRoBind, true},
		{"/ataniq/agent", in.AgentInstall, KindRoBind, true},
		{"/etc/claude-code/managed-settings.json", in.ManagedSettings, KindRoBind, false},
		{"/home/lead", "/srv/state/leads/arc-lead/home", KindRwBind, true},
		{"/home/lead/.claude", "/srv/state/leads/arc-lead/config/claude", KindRwBind, true},
		{"/home/lead/.claude/skills", in.LeadSkills, KindRoBind, true},
		{"/home/lead/.claude/CLAUDE.md", in.InstructionFile, KindRoBind, false},
		{"/ataniq/git/config", in.GitIdentityFile, KindRoBind, false},
		{"/work/ataniq", "/srv/state/leads/arc-lead/tree", KindRwBind, true},
		{"/work/ataniq/.git/config", "/srv/state/leads/arc-lead/tree/.git/config", KindRoBind, false},
		{"/work/ataniq/.git/hooks", "/srv/state/leads/arc-lead/tree/.git/hooks", KindRoBind, true},
		{"/work/ataniq/.git/info", "/srv/state/leads/arc-lead/tree/.git/info", KindRoBind, true},
		{"/ataniq/mirror/ataniq.git", "/srv/state/mirrors/ataniq.git", KindRoBind, true},
		{"/ataniq/qargi", in.QargiInstall, KindRoBind, true},
		{"/ataniq/assignment", in.AssignmentDir, KindRoBind, true},
		{"/ataniq/projection", in.ProjectionDir, KindRoBind, true},
	}
	for _, tt := range tests {
		b, ok := byInside[tt.inside]
		if !ok {
			t.Errorf("%s: no row", tt.inside)
			continue
		}
		if b.Source != tt.source || b.Kind != tt.kind || b.IsDir != tt.dir {
			t.Errorf("%s: source %q kind %s dir %v; want %q %s %v", tt.inside, b.Source, b.Kind, b.IsDir, tt.source, tt.kind, tt.dir)
		}
	}
}

func TestOtherRuntimeFolderNeverAppears(t *testing.T) {
	for _, rt := range Runtimes() {
		in := fixture(rt)
		other := rt.Other()
		for _, b := range mustBinds(t, in) {
			for _, s := range []string{b.Inside, b.Source, b.Target} {
				if strings.Contains(s, other.ConfigDirName()) || strings.Contains(s, "/config/"+string(other)) ||
					strings.Contains(s, other.InstructionFileName()) {
					t.Errorf("%s: row %q holds the %s runtime's folder or file", rt, row(b), other)
				}
			}
		}
		env, err := in.Environment()
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range env {
			if e.Name == other.ConfigEnvName() || strings.Contains(e.Value, other.ConfigDirName()) {
				t.Errorf("%s: the %s runtime's variable or folder is in the environment: %s", rt, other, e.Name)
			}
		}
	}
}

func TestLeadWritableFlags(t *testing.T) {
	in := fixture(Claude)
	in.ExtraPlaceholders = []Placeholder{
		{Rel: ".mcp.json", Source: "/srv/owned/placeholders/mcp.json"},
		{Rel: ".vscode", Source: "/srv/owned/placeholders/vscode", Dir: true},
	}
	in.QargiRecords = "/srv/state/leads/arc-lead/qargi"
	in.QargiRecordsInside = "/home/lead/.qargi"
	table := mustBinds(t, in)
	flagged := map[string]string{}
	for _, b := range table {
		if b.LeadWritable {
			flagged[b.Inside] = b.Within
		}
	}
	want := map[string]string{
		"/home/lead/.claude":           "/home/lead",
		"/home/lead/.claude/skills":    "/home/lead/.claude",
		"/home/lead/.claude/CLAUDE.md": "/home/lead/.claude",
		"/home/lead/.qargi":            "/home/lead",
		"/work/ataniq/.git/config":     "/work/ataniq",
		"/work/ataniq/.git/hooks":      "/work/ataniq",
		"/work/ataniq/.git/info":       "/work/ataniq",
		"/work/ataniq/.mcp.json":       "/work/ataniq",
		"/work/ataniq/.vscode":         "/work/ataniq",
	}
	if !reflect.DeepEqual(flagged, want) {
		t.Errorf("lead-writable rows\n got: %v\nwant: %v", flagged, want)
	}
	// Host side of each mount point.
	hostOf := map[string]string{
		"/home/lead/.claude/skills": "/srv/state/leads/arc-lead/config/claude/skills",
		"/work/ataniq/.git/hooks":   "/srv/state/leads/arc-lead/tree/.git/hooks",
		"/work/ataniq/.mcp.json":    "/srv/state/leads/arc-lead/tree/.mcp.json",
		"/home/lead/.qargi":         "/srv/state/leads/arc-lead/home/.qargi",
		"/home/lead/.claude":        "/srv/state/leads/arc-lead/home/.claude",
	}
	for i, b := range table {
		got, ok := HostMountPoint(table, i)
		if b.LeadWritable != ok {
			t.Errorf("%s: HostMountPoint ok = %v", b.Inside, ok)
		}
		if w, has := hostOf[b.Inside]; has && got != w {
			t.Errorf("%s: HostMountPoint = %q, want %q", b.Inside, got, w)
		}
	}
	if _, ok := HostMountPoint(table, -1); ok {
		t.Error("HostMountPoint(-1) must report false")
	}
	if _, ok := HostMountPoint(table, len(table)); ok {
		t.Error("HostMountPoint(len) must report false")
	}
}

func TestOptionalRecordsOutsideTheHome(t *testing.T) {
	in := fixture(Codex)
	in.QargiRecords = "/srv/state/leads/arc-lead/qargi"
	in.QargiRecordsInside = "/ataniq/records"
	table := mustBinds(t, in)
	for _, b := range table {
		if b.Inside == "/ataniq/records" {
			if b.Kind != KindRwBind || b.LeadWritable || b.SourceRole != RoleRecords {
				t.Errorf("records row = %+v", b)
			}
			return
		}
	}
	t.Error("the records row is missing")
}

func TestExtraPlaceholdersComeAfterTheGitBinds(t *testing.T) {
	in := fixture(Claude)
	in.ExtraPlaceholders = []Placeholder{{Rel: ".envrc", Source: "/srv/owned/placeholders/envrc"}}
	got := rows(mustBinds(t, in))
	var i, j int
	for k, s := range got {
		switch {
		case strings.HasPrefix(s, "ro-bind /work/ataniq/.git/info "):
			i = k
		case strings.HasPrefix(s, "ro-bind /work/ataniq/.envrc "):
			j = k
		}
	}
	if i == 0 || j != i+1 {
		t.Errorf("placeholder order wrong: info at %d, .envrc at %d", i, j)
	}
}

func TestValidateTableRefusals(t *testing.T) {
	good := mustBinds(t, fixture(Claude))
	clone := func() []Bind { return append([]Bind(nil), good...) }
	index := func(inside string) int {
		for i, b := range good {
			if b.Inside == inside {
				return i
			}
		}
		t.Fatalf("no row %s", inside)
		return -1
	}
	swap := func(a, b string) []Bind {
		c := clone()
		i, j := index(a), index(b)
		c[i], c[j] = c[j], c[i]
		return c
	}
	tests := []struct {
		name   string
		table  []Bind
		reason string
	}{
		{"empty", nil, "start with the root tmpfs"},
		{"no root first", good[1:], "start with the root tmpfs"},
		{"no remount last", good[:len(good)-1], "read-only remount"},
		{"child before parent", swap("/home/lead", "/home/lead/.claude"), "before its parent"},
		{"git before tree", swap("/work/ataniq", "/work/ataniq/.git/config"), "before its parent"},
		{"skills before config", swap("/home/lead/.claude", "/home/lead/.claude/skills"), "before its parent"},
		{"duplicate inside path", func() []Bind {
			c := clone()
			c[index("/ataniq/qargi")].Inside = "/ataniq/probe"
			return c
		}(), "already used"},
		{"row below a read-only bind", func() []Bind {
			c := clone()
			c = append(c[:len(c)-1], Bind{Kind: KindRoBind, Inside: "/usr/extra", Source: "/srv/owned/x", SourceRole: RoleExtra}, c[len(c)-1])
			return c
		}(), "below /usr"},
		{"row below a symlink", func() []Bind {
			c := clone()
			c = append(c[:len(c)-1], Bind{Kind: KindRoBind, Inside: "/bin/x", Source: "/srv/owned/x", SourceRole: RoleExtra}, c[len(c)-1])
			return c
		}(), "below /bin"},
		{"relative inside path", func() []Bind {
			c := clone()
			c[index("/ataniq/qargi")].Inside = "ataniq/qargi"
			return c
		}(), "absolute"},
		{"relative source", func() []Bind {
			c := clone()
			c[index("/ataniq/qargi")].Source = "srv/owned/qargi"
			return c
		}(), "absolute"},
		{"bind without a source role", func() []Bind {
			c := clone()
			c[index("/ataniq/qargi")].SourceRole = ""
			return c
		}(), "source role"},
		{"symlink without a target", func() []Bind {
			c := clone()
			c[index("/bin")].Target = ""
			return c
		}(), "link target"},
		{"tmpfs with a source", func() []Bind {
			c := clone()
			c[index("/tmp")].Source = "/srv/x"
			return c
		}(), "no source"},
		{"unknown kind", func() []Bind {
			c := clone()
			c[index("/ataniq/qargi")].Kind = "bind-all"
			return c
		}(), "unknown kind"},
		{"wrong lead-writable flag", func() []Bind {
			c := clone()
			c[index("/work/ataniq/.git/config")].LeadWritable = false
			return c
		}(), "lead-writable flag"},
		{"wrong within", func() []Bind {
			c := clone()
			c[index("/home/lead/.claude/skills")].Within = "/home/lead"
			return c
		}(), "lead-writable flag"},
		{"bind over the root", func() []Bind {
			c := clone()
			c[index("/ataniq/qargi")].Inside = "/"
			return c
		}(), "root"},
		{"an optional row that is not a host row", func() []Bind {
			c := clone()
			c[index("/ataniq/qargi")].Optional = true
			return c
		}(), "optional"},
		{"an optional read-write bind", func() []Bind {
			c := clone()
			c[index("/home/lead")].Optional = true
			return c
		}(), "optional"},
		{"an optional tmpfs", func() []Bind {
			c := clone()
			c[index("/tmp")].Optional = true
			return c
		}(), "optional"},
		{"an optional proc", func() []Bind {
			c := clone()
			c[index("/proc")].Optional = true
			return c
		}(), "optional"},
		{"an optional mount over the probe", func() []Bind {
			c := clone()
			c[index("/ataniq/probe")].Optional = true
			return c
		}(), "optional"},
		{"remount in the middle", func() []Bind {
			c := clone()
			c[index("/ataniq/qargi")] = Bind{Kind: KindRemountRO, Inside: "/"}
			return c
		}(), "end"},
	}
	for _, tt := range tests {
		err := ValidateTable(tt.table)
		if err == nil {
			t.Errorf("%s: ValidateTable = nil, want a refusal", tt.name)
			continue
		}
		if ref := refusalOf(t, err); !strings.Contains(ref.Reason, tt.reason) {
			t.Errorf("%s: reason %q does not mention %q", tt.name, ref.Reason, tt.reason)
		}
	}
	if err := ValidateTable(good); err != nil {
		t.Errorf("the good table is refused: %v", err)
	}
}

func TestReadOnlyOverBindsFollowTheirReadWriteParent(t *testing.T) {
	// "A parent before what is bound over it; read-only over-binds after the
	// read-write bind they cover", checked on the generated table of both runtimes.
	for _, rt := range Runtimes() {
		in := fixture(rt)
		in.ExtraPlaceholders = []Placeholder{{Rel: ".envrc", Source: "/srv/owned/p/envrc"}}
		in.QargiRecords = "/srv/state/leads/arc-lead/q"
		in.QargiRecordsInside = "/home/lead/.qargi"
		table := mustBinds(t, in)
		pos := map[string]int{}
		for i, b := range table {
			pos[b.Inside] = i
		}
		for i, b := range table {
			if !b.LeadWritable {
				continue
			}
			if p := pos[b.Within]; p >= i || table[p].Kind != KindRwBind {
				t.Errorf("%s: its read-write parent %s is at %d, the row at %d", b.Inside, b.Within, p, i)
			}
		}
	}
}

func TestOptionalRows(t *testing.T) {
	// Exactly the host rows that do not exist on every distribution.
	want := []string{
		"/lib64", "/sbin", "/etc/ssl", "/etc/ca-certificates", "/etc/nsswitch.conf", "/etc/localtime",
	}
	for _, rt := range Runtimes() {
		var got []string
		for _, b := range mustBinds(t, fixture(rt)) {
			if b.Optional {
				got = append(got, b.Inside)
				if b.Kind == KindRoBind && (b.SourceRole != RoleHost || b.Source != b.Inside) {
					t.Errorf("%s: optional bind %s has source %q (%s)", rt, b.Inside, b.Source, b.SourceRole)
				}
			}
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: optional rows %v, want %v", rt, got, want)
		}
	}
	// A row that the profile needs stays required.
	for _, b := range mustBinds(t, fixture(Claude)) {
		switch b.Inside {
		case "/usr", "/bin", "/lib", "/etc/hosts", "/etc/passwd", "/etc/group", ResolvConfInside, ProbeInside:
			if b.Optional {
				t.Errorf("%s is optional", b.Inside)
			}
		}
	}
	// A valid optional symlink and host bind pass the table check.
	if err := ValidateTable(mustBinds(t, fixture(Codex))); err != nil {
		t.Error(err)
	}
}
