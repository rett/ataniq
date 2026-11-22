package noaicheck

import (
	"os"
	"reflect"
	"strconv"
	"testing"
)

func TestCheckUnits(t *testing.T) {
	unit := func(p string) Hit { return Hit{Kind: KindUnit, Path: p} }
	runCases(t, []checkCase{
		{"clean", nil, nil},
		{"an enabled unit not allowed", func(f *fixture) {
			f.link("etc/systemd/system/multi-user.target.wants/agent.service", "/etc/systemd/system/agent.service")
		}, []Hit{unit("/etc/systemd/system/multi-user.target.wants/agent.service")}},
		{"a timer", func(f *fixture) {
			f.link("etc/systemd/system/timers.target.wants/agent.timer", "/usr/lib/systemd/system/agent.timer")
		}, []Hit{unit("/etc/systemd/system/timers.target.wants/agent.timer")}},
		{"requires and upholds", func(f *fixture) {
			f.link("etc/systemd/system/x.target.requires/a.service", "/x")
			f.link("etc/systemd/system/x.target.upholds/b.service", "/x")
		}, []Hit{unit("/etc/systemd/system/x.target.requires/a.service"), unit("/etc/systemd/system/x.target.upholds/b.service")}},
		{"a template instance not allowed", func(f *fixture) {
			f.link("etc/systemd/system/multi-user.target.wants/agent@1.service", "/x")
		}, []Hit{unit("/etc/systemd/system/multi-user.target.wants/agent@1.service")}},
		{"a global user unit", func(f *fixture) {
			f.link("etc/systemd/user/default.target.wants/agent.service", "/x")
		}, []Hit{unit("/etc/systemd/user/default.target.wants/agent.service")}},
		{"a home's user unit", func(f *fixture) {
			f.link("home/user/.config/systemd/user/default.target.wants/agent.service", "/x")
		}, []Hit{unit("/home/user/.config/systemd/user/default.target.wants/agent.service")}},
		{"an allowed unit in another target", func(f *fixture) {
			f.link("etc/systemd/system/sysinit.target.wants/systemd-timesyncd.service", "/x")
		}, nil},
		{"an alias is not an enablement", func(f *fixture) {
			f.link("etc/systemd/system/dbus-org.example.service", "/x")
		}, nil},
	}, (*Checker).CheckUnits)

	f := cleanFixture(t)
	l := testLists()
	l.UnitsAllow = nil
	c := f.checker(l)
	c.CheckUnits()
	if got := len(c.Report().Hits); got != 2 {
		t.Errorf("an empty units.allow: %d hits, want the 2 enabled units", got)
	}
}

func TestUnitAllowed(t *testing.T) {
	c := &Checker{lists: testLists()}
	for name, want := range map[string]bool{
		"sshd.service": true, "getty@tty1.service": true, "getty@.service": true,
		"getty@tty1.socket": false, "agent.service": false, "sshd@x.service": false, "@.service": false,
	} {
		if got := c.unitAllowed(name); got != want {
			t.Errorf("unitAllowed(%s) = %v", name, got)
		}
	}
}

// release plants a release whose unit sets a provider key, with current
// pointing at it, as the deployment lays it out.
func plantRelease(f *fixture) {
	f.file("opt/authority-host/releases/0007-ab12cd3/etc/systemd/app.service",
		"[Service]\nEnvironment=OPENAI_API_KEY="+secret+"\n")
	f.file("opt/authority-host/releases/0007-ab12cd3/etc/systemd/app.conf",
		"[Service]\nEnvironment=GEMINI_API_KEY="+secret+"\n")
	f.link("opt/authority-host/current", "releases/0007-ab12cd3")
}

const releaseUnit = "/opt/authority-host/releases/0007-ab12cd3/etc/systemd/app.service"

// A unit linked into the release is read where the link leads, once, and
// so are the units runtime enablement and drop-ins name.
func TestCheckEnvFollowsUnitLinks(t *testing.T) {
	env := func(p, n string) Hit { return Hit{Kind: KindEnv, Path: p, Detail: n} }
	runCases(t, []checkCase{
		{"a unit linked into current", func(f *fixture) {
			plantRelease(f)
			f.link("etc/systemd/system/app.service", "/opt/authority-host/current/etc/systemd/app.service")
		}, []Hit{env(releaseUnit, "OPENAI_API_KEY")}},
		{"the same unit linked twice is read once", func(f *fixture) {
			plantRelease(f)
			f.link("etc/systemd/system/app.service", "/opt/authority-host/current/etc/systemd/app.service")
			f.link("etc/systemd/system/multi-user.target.wants/app.service", "/opt/authority-host/current/etc/systemd/app.service")
			f.link("run/systemd/system/multi-user.target.wants/app.service", "../../../../opt/authority-host/current/etc/systemd/app.service")
		}, []Hit{env(releaseUnit, "OPENAI_API_KEY")}},
		{"an enabled link only", func(f *fixture) {
			plantRelease(f)
			f.link("etc/systemd/system/multi-user.target.wants/app.service", "/opt/authority-host/current/etc/systemd/app.service")
		}, []Hit{env(releaseUnit, "OPENAI_API_KEY")}},
		{"a runtime unit", func(f *fixture) {
			f.file("run/systemd/system/run-x.service", "[Service]\nEnvironment=CODEX_HOME=/x\n")
		}, []Hit{env("/run/systemd/system/run-x.service", "CODEX_HOME")}},
		{"a drop-in linked into the release", func(f *fixture) {
			plantRelease(f)
			f.link("etc/systemd/system/sshd.service.d/app.conf", "/opt/authority-host/current/etc/systemd/app.conf")
		}, []Hit{env("/opt/authority-host/releases/0007-ab12cd3/etc/systemd/app.conf", "GEMINI_API_KEY")}},
		{"a drop-in folder that is a link", func(f *fixture) {
			f.file("srv/dropins/x.conf", "[Service]\nEnvironment=ANTHROPIC_API_KEY=x\n")
			f.link("etc/systemd/system/sshd.service.d", "/srv/dropins")
		}, []Hit{env("/srv/dropins/x.conf", "ANTHROPIC_API_KEY")}},
		{"a folder that is not a unit folder is not read", func(f *fixture) {
			f.file("etc/systemd/system/backup/x.service", "Environment=OPENAI_API_KEY=x\n")
		}, nil},
		{"a link that loops", func(f *fixture) {
			f.link("etc/systemd/system/loop.service", "loop.service")
		}, []Hit{{Kind: KindUnitLink, Path: "/etc/systemd/system/loop.service", Detail: DetailLoop}}},
		{"a link that climbs above the root", func(f *fixture) {
			f.link("etc/systemd/system/esc.service", "../../../../x.service")
		}, []Hit{{Kind: KindUnitLink, Path: "/etc/systemd/system/esc.service", Detail: DetailEscape}}},
		{"a masked unit is not read", func(f *fixture) {
			f.link("run/systemd/system/x.service", "/dev/null")
		}, nil},
	}, (*Checker).CheckEnv)
}

func TestCheckUnitsMaskedAndRuntime(t *testing.T) {
	unit := func(p string) Hit { return Hit{Kind: KindUnit, Path: p} }
	cases := []struct {
		name  string
		plant func(f *fixture)
		want  []Hit
		notes []string
	}{
		{"runtime enablement", func(f *fixture) {
			f.link("run/systemd/system/multi-user.target.wants/agent.service", "/run/systemd/system/agent.service")
		}, []Hit{unit("/run/systemd/system/multi-user.target.wants/agent.service")}, nil},
		{"an enabled unit masked by name", func(f *fixture) {
			f.link("etc/systemd/system/agent.service", "/dev/null")
			f.link("etc/systemd/system/multi-user.target.wants/agent.service", "/usr/lib/systemd/system/agent.service")
		}, nil, []string{"unit masked /etc/systemd/system/multi-user.target.wants/agent.service"}},
		{"an enablement link to /dev/null", func(f *fixture) {
			f.link("etc/systemd/system/multi-user.target.wants/agent.service", "/dev/null")
		}, nil, []string{"unit masked /etc/systemd/system/multi-user.target.wants/agent.service"}},
		{"a mask in another folder does not mask", func(f *fixture) {
			f.link("etc/systemd/user/agent.service", "/dev/null")
			f.link("etc/systemd/system/multi-user.target.wants/agent.service", "/x")
		}, []Hit{unit("/etc/systemd/system/multi-user.target.wants/agent.service")}, nil},
		{"an enabled link that loops", func(f *fixture) {
			f.link("etc/systemd/system/multi-user.target.wants/sshd.service", "sshd.service")
		}, []Hit{{Kind: KindUnitLink, Path: "/etc/systemd/system/multi-user.target.wants/sshd.service", Detail: DetailLoop}}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := cleanFixture(t)
			mustRemoveAll(f, "etc/systemd/system/multi-user.target.wants/sshd.service")
			tc.plant(f)
			c := f.checker(testLists())
			within(t, c.CheckUnits)
			r := c.Report()
			if !(len(r.Hits) == 0 && len(tc.want) == 0) && !reflect.DeepEqual(r.Hits, tc.want) {
				t.Errorf("hits %v, want %v", r.Hits, tc.want)
			}
			if !(len(r.Notes) == 0 && len(tc.notes) == 0) && !reflect.DeepEqual(r.Notes, tc.notes) {
				t.Errorf("notes %v, want %v", r.Notes, tc.notes)
			}
			coverage := len(tc.want) > 0 && tc.want[0].Kind == KindUnitLink
			if r.Complete == coverage {
				t.Errorf("complete %v", r.Complete)
			}
		})
	}
}

// The environment check reads each manager's effective unit view: the unit
// of a name in the folder of highest precedence, never one it hides; a
// mask hides the units and drop-ins below it; linked dependency folders
// are followed; and a unit keeps its manager wherever its file is.
func TestCheckEnvUnitView(t *testing.T) {
	env := func(p, n string) Hit { return Hit{Kind: KindEnv, Path: p, Detail: n} }
	inc := func(p, d string) Hit { return Hit{Kind: KindIncomplete, Path: p, Detail: d} }
	runCases(t, []checkCase{
		{"a vendor unit overridden in /etc is not read", func(f *fixture) {
			f.file("usr/lib/systemd/system/app.service", "[Service]\nEnvironment=OPENAI_API_KEY=x\n")
			f.file("etc/systemd/system/app.service", "[Service]\nEnvironment=LANG=C\n")
		}, nil},
		{"a vendor unit and its drop-ins masked in /etc are not read", func(f *fixture) {
			f.file("usr/lib/systemd/system/app.service", "[Service]\nEnvironment=OPENAI_API_KEY=x\n")
			f.file("usr/lib/systemd/system/app.service.d/x.conf", "[Service]\nEnvironment=CODEX_HOME=x\n")
			f.link("etc/systemd/system/app.service", "/dev/null")
		}, nil},
		{"a mask in /run hides the vendor unit", func(f *fixture) {
			f.file("usr/lib/systemd/system/app.service", "[Service]\nEnvironment=OPENAI_API_KEY=x\n")
			f.link("run/systemd/system/app.service", "/dev/null")
		}, nil},
		{"a mask below the unit hides nothing", func(f *fixture) {
			f.link("usr/lib/systemd/system/app.service", "/dev/null")
			f.file("etc/systemd/system/app.service", "[Service]\nEnvironment=OPENAI_API_KEY=x\n")
		}, []Hit{env("/etc/systemd/system/app.service", "OPENAI_API_KEY")}},
		{"a masked drop-in hides the vendor's of its name", func(f *fixture) {
			f.file("usr/lib/systemd/system/sshd.service.d/x.conf", "[Service]\nEnvironment=OPENAI_API_KEY=x\n")
			f.file("usr/lib/systemd/system/sshd.service.d/y.conf", "[Service]\nEnvironment=CODEX_HOME=x\n")
			f.link("etc/systemd/system/sshd.service.d/x.conf", "/dev/null")
		}, []Hit{env("/usr/lib/systemd/system/sshd.service.d/y.conf", "CODEX_HOME")}},
		{"a template's file stands for its instance", func(f *fixture) {
			f.file("usr/lib/systemd/system/app@.service", "[Service]\nEnvironment=GEMINI_API_KEY=x\n")
			f.link("etc/systemd/system/multi-user.target.wants/app@one.service", "/usr/lib/systemd/system/app@.service")
		}, []Hit{env("/usr/lib/systemd/system/app@.service", "GEMINI_API_KEY")}},
		{"a linked dependency folder", func(f *fixture) {
			f.file("srv/units/agent.service", "[Service]\nEnvironment=ANTHROPIC_API_KEY=x\n")
			f.link("srv/wants/agent.service", "/srv/units/agent.service")
			mustRemoveAll(f, "etc/systemd/system/multi-user.target.wants")
			f.link("etc/systemd/system/multi-user.target.wants", "/srv/wants")
		}, []Hit{env("/srv/units/agent.service", "ANTHROPIC_API_KEY")}},
		{"a generated unit", func(f *fixture) {
			f.file("run/systemd/generator/x.service", "[Service]\nEnvironment=CODEX_HOME=x\n")
		}, []Hit{env("/run/systemd/generator/x.service", "CODEX_HOME")}},
		{"a user unit linked into a release keeps the user's specifiers", func(f *fixture) {
			f.file("opt/authority-host/releases/0007-ab12cd3/etc/app.service", "[Service]\nEnvironmentFile=-%h/.config/app.env\nEnvironmentFile=-%t/app.env\n")
			f.link("opt/authority-host/current", "releases/0007-ab12cd3")
			f.link("home/user/.config/systemd/user/app.service", "/opt/authority-host/current/etc/app.service")
			f.file("home/user/.config/app.env", "OPENAI_API_KEY=x\n")
			f.file("run/user/1000/app.env", "CODEX_HOME=x\n")
			f.file("root/.config/app.env", "GEMINI_API_KEY=x\n")
		}, []Hit{env("/home/user/.config/app.env", "OPENAI_API_KEY"), env("/run/user/1000/app.env", "CODEX_HOME")}},
		{"a global user unit is read in each user's context", func(f *fixture) {
			f.file("etc/systemd/user/app.service", "[Service]\nEnvironmentFile=-%h/app.env\n")
			f.file("srv/app/app.env", "OPENAI_API_KEY=x\n")
			f.file("root/app.env", "CODEX_HOME=x\n")
		}, []Hit{env("/root/app.env", "CODEX_HOME"), env("/srv/app/app.env", "OPENAI_API_KEY")}},
		{"a home in /home not in passwd runs as its owner", func(f *fixture) {
			f.file("home/other/.config/systemd/user/app.service", "[Service]\nEnvironmentFile=%t/app.env\n")
			f.file("run/user/"+ownerUID(t)+"/app.env", "CODEX_HOME=x\n")
		}, []Hit{env("/run/user/"+ownerUID(t)+"/app.env", "CODEX_HOME")}},
		{"a required EnvironmentFile that is missing", func(f *fixture) {
			f.file("etc/systemd/system/app.service", "[Service]\nEnvironmentFile=/etc/app/env\nEnvironmentFile=-/etc/app/optional\n"+
				"EnvironmentFile=/etc/app/*.env\nEnvironmentFile=-/etc/none/*.env\n")
		}, []Hit{inc(PathEnvFile, "/etc/app/env"), inc(PathEnvFile, "/etc/app/*.env")}},
		{"a required EnvironmentFile that is there", func(f *fixture) {
			f.file("etc/systemd/system/app.service", "[Service]\nEnvironmentFile=/etc/app/env\nEnvironmentFile=/etc/app/*.env\n")
			f.file("etc/app/env", "LANG=C\n")
			f.file("etc/app/a.env", "LANG=C\n")
		}, nil},
	}, (*Checker).CheckEnv)
}

// ownerUID is the user id the test's files are owned by.
func ownerUID(t *testing.T) string {
	return strconv.Itoa(os.Getuid())
}

func TestCheckUnitsView(t *testing.T) {
	unit := func(p string) Hit { return Hit{Kind: KindUnit, Path: p} }
	cases := []struct {
		name  string
		plant func(f *fixture)
		want  []Hit
		notes []string
	}{
		{"a persistent mask hides a runtime enablement", func(f *fixture) {
			f.link("etc/systemd/system/agent.service", "/dev/null")
			f.link("run/systemd/system/multi-user.target.wants/agent.service", "/run/systemd/system/agent.service")
		}, nil, []string{"unit masked /run/systemd/system/multi-user.target.wants/agent.service"}},
		{"a runtime mask below a persistent unit hides nothing", func(f *fixture) {
			f.file("etc/systemd/system/agent.service", "[Service]\n")
			f.link("run/systemd/system/agent.service", "/dev/null")
			f.link("etc/systemd/system/multi-user.target.wants/agent.service", "/etc/systemd/system/agent.service")
		}, []Hit{unit("/etc/systemd/system/multi-user.target.wants/agent.service")}, nil},
		{"a vendor mask hides an enablement", func(f *fixture) {
			f.link("usr/lib/systemd/system/agent.service", "/dev/null")
			f.link("etc/systemd/system/multi-user.target.wants/agent.service", "/usr/lib/systemd/system/agent.service")
		}, nil, []string{"unit masked /etc/systemd/system/multi-user.target.wants/agent.service"}},
		{"a masked template masks its instance", func(f *fixture) {
			f.link("etc/systemd/system/agent@.service", "/dev/null")
			f.link("etc/systemd/system/multi-user.target.wants/agent@1.service", "/x")
		}, nil, []string{"unit masked /etc/systemd/system/multi-user.target.wants/agent@1.service"}},
		{"a linked wants folder", func(f *fixture) {
			f.link("srv/wants/agent.service", "/x")
			f.link("etc/systemd/system/multi-user.target.wants", "/srv/wants")
		}, []Hit{unit("/srv/wants/agent.service")}, nil},
		{"a linked wants folder that loops", func(f *fixture) {
			f.link("etc/systemd/system/multi-user.target.wants", "multi-user.target.wants")
		}, []Hit{{Kind: KindUnitLink, Path: "/etc/systemd/system/multi-user.target.wants", Detail: DetailLoop}}, nil},
		{"a vendor's static dependency is not an enablement", func(f *fixture) {
			f.link("usr/lib/systemd/system/multi-user.target.wants/agent.service", "../agent.service")
		}, nil, nil},
		{"a user's runtime enablement", func(f *fixture) {
			f.link("run/user/1000/systemd/user/default.target.wants/agent.service", "/x")
		}, []Hit{unit("/run/user/1000/systemd/user/default.target.wants/agent.service")}, nil},
		{"a user's mask hides a global enablement for that user only", func(f *fixture) {
			f.link("home/user/.config/systemd/user/agent.service", "/dev/null")
			f.link("etc/systemd/user/default.target.wants/agent.service", "/x")
		}, []Hit{unit("/etc/systemd/user/default.target.wants/agent.service")},
			[]string{"unit masked /etc/systemd/user/default.target.wants/agent.service"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := cleanFixture(t)
			mustRemoveAll(f, "etc/systemd/system/multi-user.target.wants")
			tc.plant(f)
			c := f.checker(testLists())
			within(t, c.CheckUnits)
			r := c.Report()
			if !(len(r.Hits) == 0 && len(tc.want) == 0) && !reflect.DeepEqual(r.Hits, tc.want) {
				t.Errorf("hits %v, want %v", r.Hits, tc.want)
			}
			if !(len(r.Notes) == 0 && len(tc.notes) == 0) && !reflect.DeepEqual(r.Notes, tc.notes) {
				t.Errorf("notes %v, want %v", r.Notes, tc.notes)
			}
		})
	}
}
