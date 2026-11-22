package noaicheck

import "testing"

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
