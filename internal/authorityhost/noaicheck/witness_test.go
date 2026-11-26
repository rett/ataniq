package noaicheck

import (
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestWitnessReleaseScan(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "deploy", "authority-host", "witness", WitnessUnit))
	if err != nil {
		t.Fatal(err)
	}
	unit := strings.ReplaceAll(string(b), "{release}", "/opt/authority-host/releases/"+filepath.Base(releaseDir))
	for _, tc := range []struct {
		name, unit string
		hit        bool
	}{
		{"shipped", unit, false},
		{"wrong account", strings.Replace(unit, "User=ataniq-witness", "User=seat", 1), true},
		{"IP listener", unit + "\n[Service]\nSocketBindAllow=ipv6:tcp:9000\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := cleanFixture(t)
			f.exe(releaseDir + "/bin/ataniq-witness")
			f.file(releaseDir+"/"+WitnessUnit, tc.unit)
			c := f.checker(testLists())
			if err := c.CheckRelease(f.abs(releaseDir)); err != nil {
				t.Fatal(err)
			}
			want := []Hit{}
			if tc.hit {
				want = append(want, Hit{KindUnit, "/" + releaseDir + "/" + WitnessUnit, "witness-isolation"})
			}
			if got := c.Report().Hits; !slices.Equal(got, want) {
				t.Fatal(got)
			}
		})
	}
}

func TestWitnessListenerOnAllowedPort(t *testing.T) {
	const account = "ataniq-witness:x:817:817::/:/bin/false\n"
	port := Hit{KindPort, "tcp:192.0.2.1:22", ""}
	invalid := Hit{KindUnit, show(WitnessUnit), "witness-account"}
	for _, tc := range []struct {
		name, passwd, uid string
		installed         bool
		hits              []Hit
	}{
		{"installed", account, "817", true, []Hit{port}},
		{"link removed", account, "817", false, []Hit{port}},
		{"other user", account, "818", true, nil},
		{"unused account absent", "", "818", false, nil},
		{"required account absent", "", "818", true, []Hit{invalid}},
		{"root account", strings.Replace(account, ":817:", ":0:", 1), "818", false, []Hit{invalid}},
		{"duplicate", account + account, "818", false, []Hit{invalid}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := cleanFixture(t)
			if tc.installed {
				f.file(WitnessUnit, "[Service]\nUser=ataniq-witness\n")
			}
			f.file("etc/passwd", tc.passwd)
			f.file("proc/net/tcp", tcpHeader+"0: 010200C0:0016 00000000:0000 0A 00000000:00000000 00:00000000 00000000 "+tc.uid+" 0 1\n")
			c := f.checker(testLists())
			c.CheckPorts()
			if hits := c.Report().Hits; !slices.Equal(hits, tc.hits) {
				t.Fatal(hits)
			}
		})
	}
}

func TestWitnessReleaseComponentHits(t *testing.T) {
	b, err := os.ReadFile("../../../deploy/authority-host/witness/" + WitnessUnit)
	if err != nil {
		t.Fatal(err)
	}
	for _, binary := range []bool{true, false} {
		f := cleanFixture(t)
		detail := "witness-binary"
		if binary {
			f.exe(releaseDir + "/bin/ataniq-witness")
			detail = "witness-unit-missing"
		} else {
			f.file(releaseDir+"/"+WitnessUnit, strings.ReplaceAll(string(b), "{release}", "/"+releaseDir))
		}
		c := f.checker(testLists())
		if err := c.CheckRelease(f.abs(releaseDir)); err != nil {
			t.Fatal(err)
		}
		want := []Hit{{KindUnit, "/" + releaseDir + "/" + WitnessUnit, detail}}
		if !binary {
			want = append(want, Hit{KindUnit, "/" + releaseDir + "/" + WitnessUnit, "decision-exec"})
		}
		if hits := c.Report().Hits; !slices.Equal(hits, want) {
			t.Fatalf("got %v want %v", hits, want)
		}
	}
}

func TestWitnessExactUnitPolicy(t *testing.T) {
	data, err := os.ReadFile("../../../deploy/authority-host/witness/" + WitnessUnit)
	if err != nil {
		t.Fatal(err)
	}
	unit := strings.ReplaceAll(string(data), "{release}", "/release")
	for line := range strings.Lines(unit) {
		if !strings.HasPrefix(line, "#") && strings.Contains(line, "=") && WitnessUnitSafe(WitnessUnit, strings.Replace(unit, line, "", 1), "/release") {
			t.Errorf("accepted missing %s", line)
		}
	}
	for _, assignment := range []string{
		"ReadWritePaths=/etc", "ReadOnlyPaths=", "InaccessiblePaths=", "BindPaths=/home", "BindReadOnlyPaths=/", "TemporaryFileSystem=/",
		"RootDirectory=/", "RootImage=/image", "PrivateNetwork=no", "NetworkNamespacePath=/net", "JoinsNamespaceOf=other.service",
		"PrivateUsers=no", "Delegate=yes", "StartLimitIntervalSec=0", "Unrecognised=yes", "IPAddressAllow=any",
	} {
		for _, path := range []string{WitnessUnit, WitnessUnit + ".d/x.conf", "etc/systemd/system/service.d/x.conf", "etc/systemd/system/ataniq-.service.d/x.conf"} {
			candidate := "[Service]\n" + assignment + "\n"
			if path == WitnessUnit {
				candidate = unit + candidate
			}
			if WitnessUnitSafe(path, candidate, "/release") {
				t.Errorf("%s accepted %s", path, assignment)
			}
		}
	}
}

func TestWitnessOtherUnitCommand(t *testing.T) {
	for _, value := range []string{
		"/release/bin/ataniq-witness\trun", "\"/release/bin/ataniq-witness\" run", "-'/release/bin/ataniq-witness'",
		"/release//bin//ataniq-witness", "/opt/current/bin/ataniq-witness",
		"/alternate/bin/../lib/ataniq-witness", "ataniq-witness", "/bin/sh -c 'exec /release/bin/ataniq-witness'",
	} {
		if WitnessUnitSafe("etc/systemd/system/other.service", "[Service]\nExecStart="+value, "/release") {
			t.Errorf("accepted %q", value)
		}
	}
	if !WitnessUnitSafe("etc/systemd/system/other.service", "[Service]\nExecStart=/release/bin/ataniq-seat", "/release") {
		t.Fatal("refused unrelated binary")
	}
}

func TestWitnessHostEgress(t *testing.T) {
	for _, tc := range []struct {
		name, endpoint, grant, extra string
		valid                        bool
	}{
		{"v4", "192.0.2.4:80", "192.0.2.4/32", "", true},
		{"v6", "[2001:db8::5]:443", "2001:db8::5/128", "", true},
		{"bare host", "192.0.2.4:80", "192.0.2.4", "", true},
		{"subnet", "192.0.2.4:80", "192.0.2.0/24", "", false},
		{"reset", "192.0.2.4:80", "", "", false},
		{"wrong host", "192.0.2.4:80", "192.0.2.5/32", "", false},
		{"extra grant", "192.0.2.4:80", "192.0.2.4/32", "IPAddressAllow=192.0.2.4", false},
		{"extra key", "192.0.2.4:80", "192.0.2.4/32", "PrivateNetwork=no", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := cleanFixture(t)
			f.file(WitnessConfig, "{\"home_endpoint\":\""+tc.endpoint+"\"}")
			f.file(WitnessUnit+".d/egress.conf", "[Service]\nIPAddressAllow="+tc.grant+"\n")
			if tc.extra != "" {
				f.file(WitnessUnit+".d/another.conf", "[Service]\n"+tc.extra+"\n")
			}
			c := f.checker(testLists())
			c.CheckUnits()
			hits := c.Report().Hits
			if tc.valid && len(hits) != 0 || !tc.valid && (len(hits) != 1 || hits[0].Detail != "witness-egress") {
				t.Fatal(hits)
			}
		})
	}
}

func TestWitnessMissingEgressNote(t *testing.T) {
	for _, installed := range []bool{true, false} {
		f := cleanFixture(t)
		if installed {
			f.link(WitnessUnit, "/opt/authority-host/current/"+WitnessUnit)
		}
		c := f.checker(testLists())
		c.CheckUnits()
		note := slices.Contains(c.Report().Notes, "witness egress drop-in missing: home unreachable")
		if note != installed || len(c.Report().Hits) != 0 {
			t.Fatalf("installed %v: %v", installed, c.Report())
		}
	}
}

func TestWitnessReleaseExecutableIdentity(t *testing.T) {
	const bin = "/" + releaseDir + "/bin/"
	for _, value := range []string{"${PROGRAM}", bin + "%i", "relative", bin + "../bin/app", "/bin/app", bin + "absent", bin + "app ; /bin/other", bin + "alias"} {
		t.Run(value, func(t *testing.T) {
			f := cleanFixture(t)
			f.exe(releaseDir + "/bin/app")
			f.file(releaseDir+"/etc/systemd/system/app.service", "[Service]\nExecStartPre="+value+"\n")
			if value == bin+"alias" {
				f.link(releaseDir+"/bin/alias", "app")
			}
			c := f.checker(testLists())
			if err := c.CheckRelease(f.abs(releaseDir)); err != nil {
				t.Fatal(err)
			}
			if len(c.Report().Hits) == 0 {
				t.Fatal("accepted executable", value)
			}
		})
	}
}

func TestWitnessReleaseRejectsLinks(t *testing.T) {
	for _, rel := range []string{"etc/systemd/system/alias.service", "etc/caddy/alias", "share/alias", "bin/alias"} {
		f := cleanFixture(t)
		f.file(releaseDir+"/data", "# comment\n")
		target, err := filepath.Rel(filepath.Dir(rel), "data")
		if err != nil {
			t.Fatal(err)
		}
		f.link(releaseDir+"/"+rel, target)
		c := f.checker(testLists())
		if err := c.CheckRelease(f.abs(releaseDir)); err != nil {
			t.Fatal(err)
		}
		if len(c.Report().Hits) == 0 {
			t.Errorf("accepted link %s", rel)
		}
	}
}

func TestWitnessInheritedEgress(t *testing.T) {
	for _, dir := range []string{"etc/systemd/system/service.d", "run/systemd/system/ataniq-.service.d", "usr/lib/systemd/system/ataniq-witness.service.d", "etc/systemd/system.control/ataniq-witness.service.d"} {
		f := cleanFixture(t)
		f.file(WitnessUnit, "[Service]\nIPAddressDeny=any\n")
		f.file(dir+"/override.conf", "[Service]\nIPAddressDeny=\n")
		c := f.checker(testLists())
		c.CheckUnits()
		if !slices.ContainsFunc(c.Report().Hits, func(h Hit) bool { return h.Detail == "witness-egress" }) {
			t.Errorf("accepted deny reset in %s", dir)
		}
	}
}

func TestWitnessEffectiveEgress(t *testing.T) {
	addr := netip.MustParseAddr("192.0.2.4")
	for _, tc := range []struct {
		allow, deny string
		addr        netip.Addr
		valid       bool
	}{
		{"192.0.2.4/32", "::/0 0.0.0.0/0", addr, true},
		{"", "0.0.0.0/0 ::/0", netip.Addr{}, true},
		{"2001:db8::5/128", "any", netip.MustParseAddr("2001:db8::5"), true},
		{"192.0.2.4/32", "", addr, false},
		{"192.0.2.4/32", "0.0.0.0/0", addr, false},
		{"192.0.2.4/32 192.0.2.5/32", "any", addr, false},
		{"192.0.2.0/24", "any", addr, false},
		{"192.0.2.5/32", "any", addr, false},
		{"", "any", addr, false},
	} {
		result := "IPAddressDeny=" + tc.deny + "\nIPAddressAllow=" + tc.allow + "\nLoadState=loaded\n"
		if effectiveWitnessEgress(result, tc.addr, false) != tc.valid {
			t.Errorf("%q for %v", result, tc.addr)
		}
	}
	for _, result := range []string{"", "IPAddressDeny=any\nLoadState=loaded\n", "LoadState=not-found\n", "LoadState=masked\n"} {
		if effectiveWitnessEgress(result, netip.Addr{}, false) {
			t.Errorf("accepted %q for loaded unit", result)
		}
	}
}

func TestWitnessSystemctlQuery(t *testing.T) {
	tool := filepath.Join(t.TempDir(), "systemctl")
	for _, tc := range []struct {
		output               string
		exit                 int
		allowUnloaded, valid bool
	}{
		{"LoadState=loaded\nIPAddressAllow=\nIPAddressDeny=0.0.0.0/0 ::/0", 0, false, true},
		{"LoadState=loaded\nIPAddressAllow=any\nIPAddressDeny=any", 0, false, false},
		{"LoadState=not-found", 4, true, true},
		{"LoadState=not-found", 4, false, false},
		{"LoadState=not-found", 1, true, false},
		{"LoadState=loaded\nIPAddressAllow=\nIPAddressDeny=any", 4, true, false},
	} {
		script := "#!/bin/sh\n" +
			"test \"$*\" = '--system show --all -p IPAddressAllow,IPAddressDeny -p LoadState ataniq-witness.service' || exit 1\n" +
			"printf '%s\\n' '" + tc.output + "'\nexit " + strconv.Itoa(tc.exit) + "\n"
		if err := os.WriteFile(tool, []byte(script), 0755); err != nil {
			t.Fatal(err)
		}
		if err := queryWitnessEgress(tool, netip.Addr{}, tc.allowUnloaded); (err == nil) != tc.valid {
			t.Errorf("reply %q exit %d: %v", tc.output, tc.exit, err)
		}
	}
}
