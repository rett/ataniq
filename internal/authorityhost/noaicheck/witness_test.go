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
