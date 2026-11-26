package noaicheck

import (
	"os"
	"path/filepath"
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
			if got := c.Report().Hits; (len(got) > 0) != tc.hit {
				t.Fatal(got)
			}
		})
	}
}

func TestWitnessListenerOnAllowedPort(t *testing.T) {
	f := cleanFixture(t)
	f.file(WitnessUnit, "[Service]\nUser=ataniq-witness\n")
	f.file("etc/passwd", "ataniq-witness:x:817:817::/:/bin/false\n")
	// The same TCP port is permitted for another UID on the fixture host.
	f.file("proc/net/tcp", tcpHeader+"0: 010200C0:0016 00000000:0000 0A 0:0 0:0 0 817 0 1\n")
	c := f.checker(testLists())
	c.CheckPorts()
	if hits := c.Report().Hits; len(hits) != 1 || hits[0].Kind != KindPort {
		t.Fatal(hits)
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
