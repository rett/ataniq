package profile

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestValidFixtures(t *testing.T) {
	for _, rt := range Runtimes() {
		if err := fixture(rt).Validate(); err != nil {
			t.Errorf("%s: %v", rt, err)
		}
	}
	// The owner identity fallback, a seccomp file, records and placeholders.
	in := fixture(Claude)
	in.Identity, in.OwnerUID, in.OwnerGID, in.OwnerUser = IdentityOwner, 1000, 1000, "owner"
	in.SeccompFile = "/srv/owned/seccomp/reviewed.bpf"
	in.SeccompSHA256 = strings.Repeat("ab", 32)
	in.QargiRecords, in.QargiRecordsInside = "/srv/state/leads/arc-lead/records", "/home/lead/.qargi"
	in.ExtraPlaceholders = []Placeholder{{Rel: "sub/.envrc", Source: "/srv/owned/p/envrc"}}
	if err := in.Validate(); err != nil {
		t.Errorf("full inputs: %v", err)
	}
}

func TestInputsRefusals(t *testing.T) {
	tests := []struct {
		name   string
		rt     Runtime
		mutate func(*Inputs)
		field  string // the refused field
		reason string // a word the reason holds
	}{
		{"relative state root", Claude, func(in *Inputs) { in.StateRoot = "state" }, "state root", "absolute"},
		{"unclean state root", Claude, func(in *Inputs) { in.StateRoot = "/srv/state/." }, "state root", "clean"},
		{"bad role", Claude, func(in *Inputs) { in.Role = "Arc_Lead" }, "role", "match"},
		{"bad project", Claude, func(in *Inputs) { in.Project = "a/b" }, "project", "match"},
		{"empty runtime", Claude, func(in *Inputs) { in.Runtime = "" }, "runtime", "claude"},
		{"unknown runtime", Claude, func(in *Inputs) { in.Runtime = "gemini" }, "runtime", "codex"},
		{"empty version", Claude, func(in *Inputs) { in.RuntimeVersion = "" }, "runtime version", "characters"},
		{"version with a space", Claude, func(in *Inputs) { in.RuntimeVersion = "2.1 0" }, "runtime version", "letters"},
		{"version with a slash", Claude, func(in *Inputs) { in.RuntimeVersion = "../2" }, "runtime version", "letters"},
		{"version starts with a dot", Claude, func(in *Inputs) { in.RuntimeVersion = ".2" }, "runtime version", "letters"},
		{"long version", Claude, func(in *Inputs) { in.RuntimeVersion = strings.Repeat("1", 65) }, "runtime version", "characters"},
		{"no identity", Claude, func(in *Inputs) { in.Identity = "" }, "identity", "root"},
		{"unknown identity", Claude, func(in *Inputs) { in.Identity = "wheel" }, "identity", "owner"},
		{"root mode with ids", Claude, func(in *Inputs) { in.OwnerUID = 1000 }, "identity", "owner ids"},
		{"owner mode without ids", Claude, func(in *Inputs) { in.Identity, in.OwnerUser = IdentityOwner, "owner" }, "identity", "non-zero"},
		{"owner mode with uid 0", Claude, func(in *Inputs) {
			in.Identity, in.OwnerUser, in.OwnerGID = IdentityOwner, "owner", 1000
		}, "identity", "non-zero"},
		{"owner mode without a login", Claude, func(in *Inputs) {
			in.Identity, in.OwnerUID, in.OwnerGID = IdentityOwner, 1000, 1000
		}, "identity", "login"},
		{"owner mode bad login", Claude, func(in *Inputs) {
			in.Identity, in.OwnerUID, in.OwnerGID, in.OwnerUser = IdentityOwner, 1000, 1000, "Own er"
		}, "identity", "login"},
		{"codex without a managed-settings path", Codex, func(in *Inputs) { in.ManagedSettingsInside = "" },
			"managed-settings path", "names no managed-settings path"},
		{"relative managed-settings path", Codex, func(in *Inputs) { in.ManagedSettingsInside = "etc/m.toml" }, "managed-settings path", "absolute"},
		{"unclean managed-settings path", Claude, func(in *Inputs) { in.ManagedSettingsInside = "/etc//m.json" }, "managed-settings path", "clean"},
		{"claude managed settings elsewhere", Claude, func(in *Inputs) { in.ManagedSettingsInside = "/etc/other/managed.json" },
			"managed-settings path", "only at /etc/claude-code/managed-settings.json"},
		{"claude managed settings outside /etc", Claude, func(in *Inputs) { in.ManagedSettingsInside = "/tmp/managed.json" },
			"managed-settings path", "only at"},
		{"codex managed settings in the tmpfs", Codex, func(in *Inputs) { in.ManagedSettingsInside = "/tmp/managed.toml" },
			"managed-settings path", "under /etc"},
		{"codex managed settings in the lead's home", Codex, func(in *Inputs) { in.ManagedSettingsInside = "/home/lead/managed.toml" },
			"managed-settings path", "under /etc"},
		{"codex managed settings in the tree", Codex, func(in *Inputs) { in.ManagedSettingsInside = "/work/ataniq/managed.toml" },
			"managed-settings path", "under /etc"},
		{"codex managed settings are /etc", Codex, func(in *Inputs) { in.ManagedSettingsInside = "/etc" },
			"managed-settings path", "under /etc"},
		{"codex managed settings lookalike of /etc", Codex, func(in *Inputs) { in.ManagedSettingsInside = "/etcetera/m.toml" },
			"managed-settings path", "under /etc"},
		{"codex managed settings below a read-only host row", Codex, func(in *Inputs) { in.ManagedSettingsInside = "/etc/ssl/m.toml" },
			"bind /etc/ssl/m.toml", "below"},
		{"codex managed settings in a writable folder under /etc", Codex, func(in *Inputs) {
			in.ManagedSettingsInside = "/etc/lead/m.toml"
			in.QargiRecords, in.QargiRecordsInside = "/srv/state/leads/arc-lead/records", "/etc/lead"
		}, "bind /etc/lead/m.toml", "before its parent /etc/lead"}, // the table puts the policy before every writable bind, so none can hold it
		{"records without an inside path", Claude, func(in *Inputs) { in.QargiRecords = "/srv/state/leads/arc-lead/records" }, "Qargi records inside path", "empty"},
		{"records inside path without a folder", Claude, func(in *Inputs) { in.QargiRecordsInside = "/home/lead/q" }, "Qargi records folder", "empty"},
		{"relative records folder", Claude, func(in *Inputs) {
			in.QargiRecords, in.QargiRecordsInside = "q", "/home/lead/q"
		}, "Qargi records folder", "absolute"},
		{"no managed-settings digest", Claude, func(in *Inputs) { in.ManagedSettingsSHA256 = "" }, "managed-settings digest", "hex"},
		{"short managed-settings digest", Claude, func(in *Inputs) { in.ManagedSettingsSHA256 = "abc" }, "managed-settings digest", "hex"},
		{"long managed-settings digest", Claude, func(in *Inputs) { in.ManagedSettingsSHA256 = strings.Repeat("a", 65) }, "managed-settings digest", "hex"},
		{"upper-case managed-settings digest", Claude, func(in *Inputs) { in.ManagedSettingsSHA256 = strings.Repeat("A", 64) }, "managed-settings digest", "hex"},
		{"non-hex managed-settings digest", Claude, func(in *Inputs) { in.ManagedSettingsSHA256 = strings.Repeat("g", 64) }, "managed-settings digest", "hex"},
		{"managed-settings digest with a newline", Codex, func(in *Inputs) { in.ManagedSettingsSHA256 = strings.Repeat("a", 63) + "\n" }, "managed-settings digest", "hex"},
		{"seccomp file without a digest", Claude, func(in *Inputs) { in.SeccompFile = "/srv/owned/a.bpf" }, "seccomp digest", "hex"},
		{"seccomp digest without a file", Claude, func(in *Inputs) { in.SeccompSHA256 = strings.Repeat("a", 64) }, "seccomp file", "empty"},
		{"short seccomp digest", Claude, func(in *Inputs) {
			in.SeccompFile, in.SeccompSHA256 = "/srv/owned/a.bpf", "abc"
		}, "seccomp digest", "hex"},
		{"upper-case seccomp digest", Claude, func(in *Inputs) {
			in.SeccompFile, in.SeccompSHA256 = "/srv/owned/a.bpf", strings.Repeat("A", 64)
		}, "seccomp digest", "hex"},
		{"relative seccomp file", Claude, func(in *Inputs) {
			in.SeccompFile, in.SeccompSHA256 = "a.bpf", strings.Repeat("a", 64)
		}, "seccomp file", "absolute"},
		{"placeholder in .git", Claude, func(in *Inputs) {
			in.ExtraPlaceholders = []Placeholder{{Rel: ".git/hooks/pre-commit", Source: "/srv/owned/p"}}
		}, "extra placeholder .git/hooks/pre-commit", ".git"},
		{"placeholder .git", Claude, func(in *Inputs) {
			in.ExtraPlaceholders = []Placeholder{{Rel: ".git", Source: "/srv/owned/p"}}
		}, "extra placeholder .git", ".git"},
		{"placeholder above the tree", Claude, func(in *Inputs) {
			in.ExtraPlaceholders = []Placeholder{{Rel: "../x", Source: "/srv/owned/p"}}
		}, "extra placeholder ../x", "clean"},
		{"absolute placeholder", Claude, func(in *Inputs) {
			in.ExtraPlaceholders = []Placeholder{{Rel: "/etc/x", Source: "/srv/owned/p"}}
		}, "extra placeholder /etc/x", "relative"},
		{"unclean placeholder", Claude, func(in *Inputs) {
			in.ExtraPlaceholders = []Placeholder{{Rel: "a//b", Source: "/srv/owned/p"}}
		}, "extra placeholder a//b", "clean"},
		{"empty placeholder", Claude, func(in *Inputs) {
			in.ExtraPlaceholders = []Placeholder{{Rel: "", Source: "/srv/owned/p"}}
		}, "extra placeholder", "bytes"},
		{"dot placeholder", Claude, func(in *Inputs) {
			in.ExtraPlaceholders = []Placeholder{{Rel: ".", Source: "/srv/owned/p"}}
		}, "extra placeholder .", "clean"},
		{"placeholder with a newline", Claude, func(in *Inputs) {
			in.ExtraPlaceholders = []Placeholder{{Rel: "a\nb", Source: "/srv/owned/p"}}
		}, "extra placeholder a\nb", "control"},
		{"duplicate placeholder", Claude, func(in *Inputs) {
			in.ExtraPlaceholders = []Placeholder{{Rel: "a", Source: "/srv/owned/p"}, {Rel: "a", Source: "/srv/owned/q"}}
		}, "extra placeholder a", "twice"},
		{"relative placeholder source", Claude, func(in *Inputs) {
			in.ExtraPlaceholders = []Placeholder{{Rel: "a", Source: "p"}}
		}, "extra placeholder a source", "absolute"},
		{"nested placeholders", Claude, func(in *Inputs) {
			in.ExtraPlaceholders = []Placeholder{{Rel: "a", Source: "/srv/owned/p", Dir: true}, {Rel: "a/b", Source: "/srv/owned/q"}}
		}, "bind /work/ataniq/a/b", "below"},
		{"no never-mounted paths", Claude, func(in *Inputs) { in.NeverMounted = nil }, "never-mounted paths", "none given"},
		{"relative never-mounted path", Claude, func(in *Inputs) { in.NeverMounted = []string{".ssh"} }, "never-mounted path 0", "absolute"},
		{"unclean never-mounted path", Claude, func(in *Inputs) {
			in.NeverMounted = []string{"/home/owner/.ssh", "/home/owner/../.ssh"}
		}, "never-mounted path 1", "clean"},
		{"records inside the work tree", Claude, func(in *Inputs) {
			in.QargiRecords, in.QargiRecordsInside = "/srv/state/leads/arc-lead/records", "/work/ataniq/.qargi"
		}, "Qargi records inside path", "work tree"},
		{"records inside the work folder", Claude, func(in *Inputs) {
			in.QargiRecords, in.QargiRecordsInside = "/srv/state/leads/arc-lead/records", "/work"
		}, "Qargi records inside path", "work tree"},
		{"records inside another project's tree", Claude, func(in *Inputs) {
			in.QargiRecords, in.QargiRecordsInside = "/srv/state/leads/arc-lead/records", "/work/other/.qargi"
		}, "Qargi records inside path", "work tree"},
		{"records inside path with the project token", Claude, func(in *Inputs) {
			in.QargiRecords, in.QargiRecordsInside = "/srv/state/leads/arc-lead/records", "/home/lead/<project>/q"
		}, "Qargi records inside path", "'<'"},
		{"records inside the other runtime's folder (claude)", Claude, func(in *Inputs) {
			in.QargiRecords, in.QargiRecordsInside = "/srv/state/leads/arc-lead/records", "/home/lead/.codex"
		}, "Qargi records inside path", "codex config folder"},
		{"records below the other runtime's folder (claude)", Claude, func(in *Inputs) {
			in.QargiRecords, in.QargiRecordsInside = "/srv/state/leads/arc-lead/records", "/home/lead/.codex/records"
		}, "Qargi records inside path", "codex config folder"},
		{"records inside the other runtime's folder (codex)", Codex, func(in *Inputs) {
			in.QargiRecords, in.QargiRecordsInside = "/srv/state/leads/arc-lead/records", "/home/lead/.claude/q"
		}, "Qargi records inside path", "claude config folder"},
		{"records over the home", Codex, func(in *Inputs) {
			in.QargiRecords, in.QargiRecordsInside = "/srv/state/leads/arc-lead/records", "/home/lead"
		}, "bind /home/lead", "already used"},
		{"relative records inside", Claude, func(in *Inputs) {
			in.QargiRecords, in.QargiRecordsInside = "/srv/state/leads/arc-lead/records", "q"
		}, "Qargi records inside path", "absolute"},
		{"records inside path used twice", Claude, func(in *Inputs) {
			in.QargiRecords, in.QargiRecordsInside = "/srv/state/leads/arc-lead/records", "/ataniq/probe"
		}, "bind /ataniq/probe", "already used"},
		{"records bound over a parent of the home", Claude, func(in *Inputs) {
			in.QargiRecords, in.QargiRecordsInside = "/srv/state/leads/arc-lead/records", "/home"
		}, "bind /home/lead", "before its parent"},
		{"records below a read-only bind", Claude, func(in *Inputs) {
			in.QargiRecords, in.QargiRecordsInside = "/srv/state/leads/arc-lead/records", "/ataniq/agent/records"
		}, "bind /ataniq/agent/records", "below"},
		// Overlaps with what the lead can write.
		{"skills inside the lead's home", Claude, func(in *Inputs) { in.LeadSkills = "/srv/state/leads/arc-lead/home/skills" },
			"bind /home/lead/.claude/skills", "overlaps"},
		{"instruction file inside the config folder", Claude, func(in *Inputs) {
			in.InstructionFile = "/srv/state/leads/arc-lead/config/claude/CLAUDE.md"
		}, "bind /home/lead/.claude/CLAUDE.md", "overlaps"},
		{"assignment folder inside the tree", Claude, func(in *Inputs) { in.AssignmentDir = "/srv/state/leads/arc-lead/tree/a" },
			"bind /ataniq/assignment", "overlaps"},
		// The records folder is the layout's own (rule G1 across leads).
		{"records inside the home folder", Claude, func(in *Inputs) {
			in.QargiRecords, in.QargiRecordsInside = "/srv/state/leads/arc-lead/home/q", "/home/lead/.qargi"
		}, "Qargi records folder", "must be /srv/state/leads/arc-lead/records"},
		{"records folder outside the state root", Claude, func(in *Inputs) {
			in.QargiRecords, in.QargiRecordsInside = "/srv/state/q", "/home/lead/.qargi"
		}, "Qargi records folder", "must be /srv/state/leads/arc-lead/records"},
		{"records folder is another source of the same lead", Claude, func(in *Inputs) {
			in.QargiRecords, in.QargiRecordsInside = in.AgentInstall, "/home/lead/.qargi"
		}, "Qargi records folder", "must be /srv/state/leads/arc-lead/records"},
		{"records folder above a read-only source", Claude, func(in *Inputs) {
			in.QargiRecords, in.QargiRecordsInside = "/opt/ataniq", "/home/lead/.qargi"
		}, "Qargi records folder", "must be /srv/state/leads/arc-lead/records"},
		{"records folder in another lead's state", Claude, func(in *Inputs) {
			in.QargiRecords, in.QargiRecordsInside = "/srv/state/leads/other-lead/records", "/home/lead/.qargi"
		}, "Qargi records folder", "must be /srv/state/leads/arc-lead/records"},
		{"records folder in the lead's own tree", Claude, func(in *Inputs) {
			in.QargiRecords, in.QargiRecordsInside = "/srv/state/leads/arc-lead/tree/records", "/home/lead/.qargi"
		}, "Qargi records folder", "must be /srv/state/leads/arc-lead/records"},
		{"records folder is the lead's state folder", Claude, func(in *Inputs) {
			in.QargiRecords, in.QargiRecordsInside = "/srv/state/leads/arc-lead", "/home/lead/.qargi"
		}, "Qargi records folder", "must be /srv/state/leads/arc-lead/records"},
		// The git metadata folder.
		{"no git metadata folder", Claude, func(in *Inputs) { in.GitMeta = "" }, "git metadata folder", "empty"},
		{"relative git metadata folder", Claude, func(in *Inputs) { in.GitMeta = "gitmeta" }, "git metadata folder", "absolute"},
		{"unclean git metadata folder", Claude, func(in *Inputs) { in.GitMeta = "/srv/state/leads/arc-lead/gitmeta/" }, "git metadata folder", "clean"},
		{"git metadata folder is the home", Claude, func(in *Inputs) { in.GitMeta = "/srv/state/leads/arc-lead/home" },
			"git metadata folder", "must be /srv/state/leads/arc-lead/gitmeta"},
		{"git metadata folder in the home", Claude, func(in *Inputs) { in.GitMeta = "/srv/state/leads/arc-lead/home/gitmeta" },
			"git metadata folder", "must be /srv/state/leads/arc-lead/gitmeta"},
		{"git metadata folder in the config folder", Claude, func(in *Inputs) {
			in.GitMeta = "/srv/state/leads/arc-lead/config/claude/gitmeta"
		}, "git metadata folder", "must be /srv/state/leads/arc-lead/gitmeta"},
		{"git metadata folder in the tree", Codex, func(in *Inputs) { in.GitMeta = "/srv/state/leads/arc-lead/tree/.git-meta" },
			"git metadata folder", "must be /srv/state/leads/arc-lead/gitmeta"},
		{"git metadata folder is the tree's .git", Claude, func(in *Inputs) { in.GitMeta = "/srv/state/leads/arc-lead/tree/.git" },
			"git metadata folder", "must be /srv/state/leads/arc-lead/gitmeta"},
		{"git metadata folder above the tree", Claude, func(in *Inputs) { in.GitMeta = "/srv/state/leads/arc-lead" },
			"git metadata folder", "must be /srv/state/leads/arc-lead/gitmeta"},
		{"git metadata folder above the home", Claude, func(in *Inputs) { in.GitMeta = "/srv/state" },
			"git metadata folder", "must be /srv/state/leads/arc-lead/gitmeta"},
		{"git metadata folder outside the state root", Claude, func(in *Inputs) { in.GitMeta = "/var/lib/other/gitmeta" },
			"git metadata folder", "must be /srv/state/leads/arc-lead/gitmeta"},
		{"git metadata folder beside the lead's folders under a sibling name", Claude, func(in *Inputs) {
			in.GitMeta = "/srv/state/leads/arc-lead/gitmeta2"
		}, "git metadata folder", "must be /srv/state/leads/arc-lead/gitmeta"},
		{"git metadata folder in the owner's checkout", Claude, func(in *Inputs) { in.GitMeta = "/home/owner/work/ataniq/gitmeta" },
			"git metadata folder", "must be /srv/state/leads/arc-lead/gitmeta"},
		{"git metadata folder in another lead's state", Claude, func(in *Inputs) { in.GitMeta = "/srv/state/leads/other-lead/gitmeta" },
			"git metadata folder", "must be /srv/state/leads/arc-lead/gitmeta"},
		{"git metadata folder in another project's mirror", Claude, func(in *Inputs) { in.GitMeta = "/srv/state/mirrors/other.git" },
			"git metadata folder", "must be /srv/state/leads/arc-lead/gitmeta"},
		{"git metadata folder under a never-mounted path", Claude, func(in *Inputs) {
			in.NeverMounted = append(in.NeverMounted, "/srv/state/leads/arc-lead/gitmeta")
		}, "bind /work/ataniq/.git/config", "never-mounted"},
		// Another lead's or another project's state.
		{"agent install in another lead's claude login", Claude, func(in *Inputs) {
			in.AgentInstall = "/srv/state/leads/other-lead/config/claude"
		}, "bind /ataniq/agent", "another lead's state"},
		{"agent install in another lead's codex login", Codex, func(in *Inputs) {
			in.AgentInstall = "/srv/state/leads/other-lead/config/codex/auth.json"
		}, "bind /ataniq/agent", "another lead's state"},
		{"spec folder is another lead's home", Claude, func(in *Inputs) { in.SpecDir = "/srv/state/leads/other-lead/home" },
			"bind /ataniq/spec", "another lead's state"},
		{"qargi install is another lead's folder", Codex, func(in *Inputs) { in.QargiInstall = "/srv/state/leads/other-lead" },
			"bind /ataniq/qargi", "another lead's state"},
		{"qargi install is a lead whose name starts like this one", Claude, func(in *Inputs) {
			in.QargiInstall = "/srv/state/leads/arc-lead2/home"
		}, "bind /ataniq/qargi", "another lead's state"},
		{"projection folder is the folder of all leads", Claude, func(in *Inputs) { in.ProjectionDir = "/srv/state/leads" },
			"bind /ataniq/projection", "above the never-mounted"},
		{"assignment folder is another project's mirror", Claude, func(in *Inputs) {
			in.AssignmentDir = "/srv/state/mirrors/other.git"
		}, "bind /ataniq/assignment", "another project's mirror"},
		{"assignment folder is below another project's mirror", Codex, func(in *Inputs) {
			in.AssignmentDir = "/srv/state/mirrors/other.git/refs"
		}, "bind /ataniq/assignment", "another project's mirror"},
		{"assignment folder is the mirrors folder", Claude, func(in *Inputs) { in.AssignmentDir = "/srv/state/mirrors" },
			"bind /ataniq/assignment", "another project's mirror"},
		// Never-mounted paths.
		{"agent install is the owner's .ssh", Claude, func(in *Inputs) { in.AgentInstall = "/home/owner/.ssh" },
			"bind /ataniq/agent", "never-mounted"},
		{"qargi install below the owner's checkout", Claude, func(in *Inputs) { in.QargiInstall = "/home/owner/work/ataniq/qargi" },
			"bind /ataniq/qargi", "below the never-mounted"},
		{"probe above the owner's real provider folders", Claude, func(in *Inputs) { in.ProbeBinary = "/home/owner" },
			"bind /ataniq/probe", "above the never-mounted"},
		{"resolv.conf is the file system root", Claude, func(in *Inputs) { in.ResolvConf = "/" },
			"bind /etc/resolv.conf", "above the never-mounted"},
		{"state root inside the owner's checkout", Claude, func(in *Inputs) {
			in.StateRoot = "/home/owner/work/ataniq/state"
			in.GitMeta = in.Layout().GitMeta()
		}, "bind /home/lead", "below the never-mounted"},
		{"never-mounted path inside a state folder", Claude, func(in *Inputs) {
			in.NeverMounted = append(in.NeverMounted, "/srv/state/leads/arc-lead/tree/.git/hooks/x")
		}, "bind /work/ataniq", "above the never-mounted"},
		{"the other runtime's config folder (claude)", Claude, func(in *Inputs) { in.AgentInstall = "/srv/state/leads/arc-lead/config/codex" },
			"bind /ataniq/agent", "never-mounted"},
		{"the other runtime's config folder (codex)", Codex, func(in *Inputs) { in.GitIdentityFile = "/srv/state/leads/arc-lead/config/claude/x" },
			"bind /ataniq/git/config", "below the never-mounted"},
		{"a folder above the other runtime's config folder", Codex, func(in *Inputs) { in.SpecDir = "/srv/state/leads/arc-lead/config" },
			"bind /ataniq/spec", "above the never-mounted"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := fixture(tt.rt)
			tt.mutate(&in)
			err := in.Validate()
			if err == nil {
				t.Fatal("Validate = nil, want a refusal")
			}
			ref := refusalOf(t, err)
			if ref.Field != tt.field || !strings.Contains(ref.Reason, tt.reason) {
				t.Errorf("refusal = {%q, %q}, want field %q and a reason with %q", ref.Field, ref.Reason, tt.field, tt.reason)
			}
			// Every accessor refuses the same inputs.
			if _, err := in.Binds(); err == nil {
				t.Error("Binds accepted the inputs")
			}
			if _, err := in.Environment(); err == nil {
				t.Error("Environment accepted the inputs")
			}
			if _, err := in.Flags(); err == nil {
				t.Error("Flags accepted the inputs")
			}
		})
	}
}

func TestManagedSettingsPaths(t *testing.T) {
	// Claude: empty means the fixed path, and the fixed path may be given.
	in := fixture(Claude)
	if got := in.ManagedInside(); got != ClaudeManagedInside {
		t.Errorf("claude default = %q", got)
	}
	in.ManagedSettingsInside = ClaudeManagedInside
	if err := in.Validate(); err != nil {
		t.Errorf("the fixed claude path: %v", err)
	}
	// Codex: any clean path under /etc that no other bind covers.
	for _, p := range []string{"/etc/codex/managed-config.toml", "/etc/codex.toml", "/etc/a/b/c/m.toml"} {
		c := fixture(Codex)
		c.ManagedSettingsInside = p
		if err := c.Validate(); err != nil {
			t.Errorf("codex %s: %v", p, err)
		}
	}
}

func TestListBounds(t *testing.T) {
	placeholders := func(n int) []Placeholder {
		var ph []Placeholder
		for i := 0; i < n; i++ {
			ph = append(ph, Placeholder{Rel: fmt.Sprintf("p%d", i), Source: fmt.Sprintf("/srv/owned/ph/p%d", i)})
		}
		return ph
	}
	never := func(n int) []string {
		list := []string{"/home/owner/.ssh"}
		for i := 1; i < n; i++ {
			list = append(list, fmt.Sprintf("/home/owner/n%d", i))
		}
		return list
	}
	// The limits themselves are accepted, one more is refused.
	for _, tt := range []struct {
		name   string
		mutate func(*Inputs, int)
		limit  int
		field  string
	}{
		{"extra placeholders", func(in *Inputs, n int) { in.ExtraPlaceholders = placeholders(n) }, maxExtras, "extra placeholders"},
		{"never-mounted paths", func(in *Inputs, n int) { in.NeverMounted = never(n) }, maxNever, "never-mounted paths"},
		{"extra environment names", func(in *Inputs, n int) {
			in.ExtraEnv = nil
			for i := 0; i < n; i++ {
				in.ExtraEnv = append(in.ExtraEnv, EnvVar{Name: fmt.Sprintf("V%d", i), Value: "1"})
			}
		}, maxExtraEnv, "extra environment"},
	} {
		in := fixture(Claude)
		tt.mutate(&in, tt.limit)
		if err := in.Validate(); err != nil {
			t.Errorf("%s: %d accepted? %v", tt.name, tt.limit, err)
		}
		in = fixture(Claude)
		tt.mutate(&in, tt.limit+1)
		err := in.Validate()
		if err == nil {
			t.Errorf("%s: %d accepted", tt.name, tt.limit+1)
			continue
		}
		if ref := refusalOf(t, err); ref.Field != tt.field || !strings.Contains(ref.Reason, "more than") {
			t.Errorf("%s: refusal {%q, %q}", tt.name, ref.Field, ref.Reason)
		}
	}
}

func TestEveryRequiredPathIsRequired(t *testing.T) {
	fields := map[string]func(*Inputs) *string{
		"agent install folder":  func(in *Inputs) *string { return &in.AgentInstall },
		"probe binary":          func(in *Inputs) *string { return &in.ProbeBinary },
		"spec folder":           func(in *Inputs) *string { return &in.SpecDir },
		"managed-settings file": func(in *Inputs) *string { return &in.ManagedSettings },
		"lead skills folder":    func(in *Inputs) *string { return &in.LeadSkills },
		"instruction file":      func(in *Inputs) *string { return &in.InstructionFile },
		"git identity file":     func(in *Inputs) *string { return &in.GitIdentityFile },
		"Qargi install":         func(in *Inputs) *string { return &in.QargiInstall },
		"assignment folder":     func(in *Inputs) *string { return &in.AssignmentDir },
		"projection folder":     func(in *Inputs) *string { return &in.ProjectionDir },
		"resolv.conf file":      func(in *Inputs) *string { return &in.ResolvConf },
	}
	for name, field := range fields {
		for _, bad := range []struct{ value, reason string }{
			{"", "empty"}, {"relative/path", "absolute"}, {"/srv/owned/x/", "clean"}, {"/srv/owned/../x", "clean"},
		} {
			in := fixture(Claude)
			*field(&in) = bad.value
			ref := refusalOf(t, in.Validate())
			if ref.Field != name || !strings.Contains(ref.Reason, bad.reason) {
				t.Errorf("%s = %q: refusal {%q, %q}", name, bad.value, ref.Field, ref.Reason)
			}
		}
	}
}

func TestManagedSettingsDefaults(t *testing.T) {
	claude := fixture(Claude)
	if got := claude.ManagedInside(); got != "/etc/claude-code/managed-settings.json" {
		t.Errorf("claude default = %q", got)
	}
	claude.ManagedSettingsInside = "/etc/other/managed.json"
	if got := claude.ManagedInside(); got != "/etc/other/managed.json" {
		t.Errorf("claude given = %q", got)
	}
	if got := fixture(Codex).ManagedInside(); got != "/etc/codex/managed-config.toml" {
		t.Errorf("codex given = %q", got)
	}
}

func TestFlags(t *testing.T) {
	root, err := fixture(Claude).Flags()
	if err != nil {
		t.Fatal(err)
	}
	want := "--die-with-parent --unshare-all --share-net --unshare-user --uid 0 --gid 0 --cap-drop ALL --clearenv"
	if got := strings.Join(root, " "); got != want {
		t.Errorf("root flags = %q\nwant %q", got, want)
	}
	in := fixture(Codex)
	in.Identity, in.OwnerUID, in.OwnerGID, in.OwnerUser = IdentityOwner, 1234, 5678, "owner"
	owner, err := in.Flags()
	if err != nil {
		t.Fatal(err)
	}
	want = "--die-with-parent --unshare-all --share-net --unshare-user --uid 1234 --gid 5678 --cap-drop ALL --clearenv"
	if got := strings.Join(owner, " "); got != want {
		t.Errorf("owner flags = %q\nwant %q", got, want)
	}
	// R17: nested sandboxes are allowed; the probe makes the session.
	for _, f := range append(root, owner...) {
		for _, bad := range ForbiddenFlags() {
			if f == bad {
				t.Errorf("the flag set holds %s", bad)
			}
		}
	}
	if got := ForbiddenFlags(); !reflect.DeepEqual(got, []string{"--disable-userns", "--assert-userns-disabled", "--new-session"}) {
		t.Errorf("ForbiddenFlags = %v", got)
	}
	// --seccomp is the plan's, and only with a named file.
	if fixture(Claude).SeccompEnabled() {
		t.Error("seccomp enabled without a file")
	}
	in = fixture(Claude)
	in.SeccompFile, in.SeccompSHA256 = "/srv/owned/a.bpf", strings.Repeat("0", 64)
	if !in.SeccompEnabled() {
		t.Error("seccomp not enabled with a file")
	}
	if f, _ := in.Flags(); contains(f, "--seccomp") {
		t.Error("the flag set holds --seccomp")
	}
}

func TestFlagsAreNotShared(t *testing.T) {
	a, _ := fixture(Claude).Flags()
	a[0] = "changed"
	b, _ := fixture(Claude).Flags()
	if b[0] != "--die-with-parent" {
		t.Error("Flags returns a shared slice")
	}
}

// Rule G1 across leads: every folder a lead writes lies in that lead's own
// state, so the rules about another lead's state keep each lead's read-only
// sources out of every other lead's reach. The review's case: lead B names a
// records folder that is a read-only source of lead A.
func TestRecordsFolderOfAnotherLeadCannotBeASourceOfThisOne(t *testing.T) {
	for _, rt := range Runtimes() {
		a := fixture(rt)
		b := fixture(rt)
		b.Role = "second-lead"
		b.GitMeta = b.Layout().GitMeta()
		b.QargiRecordsInside = "/home/lead/.qargi"
		b.AgentInstall = "/opt/ataniq/second-agent"

		// B's records folder is a source of A: refused for B, whatever the
		// place of A's source.
		b.QargiRecords = a.AgentInstall
		if err := b.Validate(); err == nil || !strings.Contains(err.Error(), "must be "+b.Layout().Records()) {
			t.Errorf("%s: lead B with A's agent install as its records folder: %v", rt, err)
		}
		// B's own records folder is accepted, and it is in B's state.
		b.QargiRecords = b.Layout().Records()
		if err := b.Validate(); err != nil {
			t.Errorf("%s: lead B with its own records folder: %v", rt, err)
		}
		if !strings.HasPrefix(b.QargiRecords, b.Layout().LeadDir()+"/") {
			t.Errorf("%s: B's records folder %s is not in B's state", rt, b.QargiRecords)
		}
		// A source of A in B's records folder is refused for A: the folder is
		// in another lead's state.
		a.AgentInstall = b.Layout().Records()
		if err := a.Validate(); err == nil || !strings.Contains(err.Error(), "another lead's state") {
			t.Errorf("%s: lead A with a source in B's records folder: %v", rt, err)
		}
	}
}
