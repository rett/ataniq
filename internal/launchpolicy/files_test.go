package launchpolicy

import (
	"slices"
	"strings"
	"testing"
)

type fileTrial struct {
	inv   Inventory
	files []FileView
}

func cleanFiles() fileTrial {
	hash := strings.Repeat("a", 64)
	return fileTrial{
		Inventory{{PathRole: "settings", Dest: "a-settings"}, {PathRole: "agent", Dest: "b-agent", Definition: true, AgentName: "reviewer"}},
		[]FileView{{"settings", "a-settings", hash, hash, []Entry{{[]Comp{"items", "0"}, "bool", "true"}}}, {"agent", "b-agent", hash, hash, []Entry{{[]Comp{"name"}, "string", "reviewer"}}}},
	}
}
func TestT53Files(t *testing.T) {
	x := baseline(t, "BC")
	clean := cleanFiles()
	pass(t, JudgeFiles(x.p, x.s, clean.inv, clean.files))
	for _, tc := range []struct {
		name, code, at string
		change         func(*fileTrial)
	}{
		{"a missing key", "file-refused", "file[a-settings]", func(f *fileTrial) { f.files[0].Entries = nil }},
		{"b unlisted key", "file-refused", "file[a-settings]", func(f *fileTrial) {
			f.files[0].Entries = append(f.files[0].Entries, Entry{[]Comp{"extra"}, "bool", "true"})
		}},
		{"c type", "file-refused", "file[a-settings]", func(f *fileTrial) { f.files[0].Entries[0].Type = "string" }},
		{"d text", "file-refused", "file[a-settings]", func(f *fileTrial) { f.files[0].Entries[0].Text = "false" }},
		{"e missing file", "file-refused", "file[a-settings]", func(f *fileTrial) { f.files = f.files[1:] }},
		{"f duplicate destination", "file-refused", "file[a-settings]", func(f *fileTrial) { f.files = append(f.files, f.files[0]) }},
		{"g unknown role", "file-refused", "file[a-settings]", func(f *fileTrial) { f.inv[0].PathRole = "other"; f.files[0].PathRole = "other" }},
		{"h substituted role", "file-refused", "file[a-settings]", func(f *fileTrial) { f.files[0].PathRole = "agent" }},
		{"i missing definition", "file-refused", "file[b-agent]", func(f *fileTrial) { f.inv = f.inv[1:]; f.files = nil }},
		{"j definition name", "file-refused", "file[b-agent]", func(f *fileTrial) { f.files[1].Entries[0].Text = "builder" }},
		{"l installation", "file-binding", "file[a-settings]", func(f *fileTrial) { f.files[0].InstalledSHA256 = strings.Repeat("b", 64) }},
		{"m typed array index", "file-refused", "file[a-settings]", func(f *fileTrial) { f.files[0].Entries[0].Path[1] = 0 }},
		{"duplicate entry", "file-refused", "file[a-settings]", func(f *fileTrial) { f.files[0].Entries = append(f.files[0].Entries, f.files[0].Entries[0]) }},
		{"extra file", "file-refused", "file[z-extra]", func(f *fileTrial) { extra := f.files[0]; extra.Dest = "z-extra"; f.files = append(f.files, extra) }},
		{"unsafe destination", "file-refused", "file[#0]", func(f *fileTrial) { f.inv[0].Dest = "]bad" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := cleanFiles()
			tc.change(&f)
			line(t, JudgeFiles(x.p, x.s, f.inv, f.files), tc.code, "2", tc.at)
		})
	}
	for _, key := range []string{"mode", "tools", "hook", "mcp"} {
		t.Run("k "+key, func(t *testing.T) {
			f := cleanFiles()
			f.files[1].Entries = append(f.files[1].Entries, Entry{[]Comp{key}, "string", "private"})
			line(t, JudgeFiles(x.p, x.s, f.inv, f.files), "agent-key-refused", "2", "file[b-agent]")
		})
	}
	// Same selected name with the built-in inventory: no definition to install.
	pass(t, JudgeFiles(x.p, x.s, nil, nil))
}
func TestT52FirstFile(t *testing.T) {
	x := baseline(t, "BC")
	f := cleanFiles()
	f.files[0].Entries = nil
	f.files[1].InstalledSHA256 = ""
	slices.Reverse(f.inv)
	slices.Reverse(f.files)
	line(t, JudgeFiles(x.p, x.s, f.inv, f.files), "file-refused", "2", "file[a-settings]")
}

func TestFirstFaultInAgentPaths(t *testing.T) {
	for _, tc := range []struct {
		missing []Comp
		bad     []Comp
		code    string
	}{
		{[]Comp{"name"}, []Comp{"zzz"}, "file-refused"},
		{[]Comp{"name"}, []Comp{"aaa"}, "agent-key-refused"},
		{nil, []Comp{"zzz"}, "file-refused"}, // name is required even when only allowed by the row
		{[]Comp{0}, []Comp{"0"}, "file-refused"},
		{[]Comp{"0"}, []Comp{0}, "agent-key-refused"},
	} {
		x := baseline(t, "BC")
		if tc.missing != nil {
			x.policy(t, func(d obj) {
				r := row(d["files"], "path_role", "agent")
				r["allowed"] = []any{}
				r["required"] = []any{obj{"path": tc.missing, "type": "string", "text": "reviewer"}}
			})
		}
		f := cleanFiles()
		f.files[1].Entries = []Entry{{tc.bad, "string", "unused"}}
		line(t, JudgeFiles(x.p, x.s, f.inv, f.files), tc.code, "2", "file[b-agent]")
	}
}

func TestUniqueInventoryRolesAndDefinition(t *testing.T) {
	x := baseline(t, "BC")
	f := cleanFiles()
	extra, view := f.inv[0], f.files[0]
	extra.Dest = "c-settings"
	view.Dest = extra.Dest
	line(t, JudgeFiles(x.p, x.s, append(f.inv, extra), append(f.files, view)), "file-refused", "2", "file[a-settings]")
	// Two individually valid policy rows cannot both be the selected definition.
	x.policy(t, func(d obj) {
		other := row(d["files"], "path_role", "agent")
		d["files"] = append(d["files"].([]any), obj{"path_role": "standby", "allowed": other["allowed"], "required": other["required"]})
	})
	extra, view = f.inv[1], f.files[1]
	extra.PathRole = "standby"
	extra.Dest = "c-agent"
	view.PathRole = extra.PathRole
	view.Dest = extra.Dest
	line(t, JudgeFiles(x.p, x.s, append(f.inv, extra), append(f.files, view)), "file-refused", "2", "file[b-agent]")
}
