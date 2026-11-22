package noaicheck

import (
	"reflect"
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
			if !r.Complete {
				t.Error("a masked unit made the run incomplete")
			}
		})
	}
}
