package noaicheck

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// seatOverlay is the seat app's part of the release source tree.
var seatOverlay = filepath.Join("..", "..", "..", "deploy", "authority-host", "seat")

// TestShippedSeatFilesPassTheReleaseScan: the seat's unit and Caddyfile, as
// a release holds them, name no variable like a provider key; the unit's
// environment file is the owner's, read on the host like any other, and
// while it is missing the check says so (the unit would not start, and the
// check cannot know what the file would set).
func TestShippedSeatFilesPassTheReleaseScan(t *testing.T) {
	unit, err := os.ReadFile(filepath.Join(seatOverlay, "etc/systemd/system/ataniq-seat.service"))
	if err != nil {
		t.Fatal(err)
	}
	caddyfile, err := os.ReadFile(filepath.Join(seatOverlay, "etc/caddy/Caddyfile"))
	if err != nil {
		t.Fatal(err)
	}
	plant := func(f *fixture) {
		f.file(releaseDir+"/etc/systemd/system/ataniq-seat.service", string(unit))
		f.file(releaseDir+"/etc/caddy/Caddyfile", string(caddyfile))
	}
	for _, tc := range []struct {
		name string
		env  string // the owner's environment file, "" for none
		want []Hit
	}{
		{"with the owner's environment file", "SEAT_GATE=203.0.113.20:7443\n", nil},
		{"without it", "", []Hit{{Kind: KindIncomplete, Path: PathEnvFile, Detail: "/etc/authority-host/seat/seat.env"}}},
		{"with a provider key in it", "SEAT_GATE=203.0.113.20:7443\nOPENAI_API_KEY=x\n",
			[]Hit{{Kind: KindEnv, Path: "/etc/authority-host/seat/seat.env", Detail: "OPENAI_API_KEY"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := cleanFixture(t)
			plant(f)
			if tc.env != "" {
				f.file("etc/authority-host/seat/seat.env", tc.env)
			}
			c := f.checker(testLists())
			var err error
			within(t, func() { err = c.CheckRelease(f.abs(releaseDir)) })
			if err != nil {
				t.Fatal(err)
			}
			if got := c.Report().Hits; !(len(got) == 0 && len(tc.want) == 0) && !reflect.DeepEqual(got, tc.want) {
				t.Errorf("hits %v, want %v", got, tc.want)
			}
		})
	}
}
