package noaicheck

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// notifyOverlay is the notifier's part of the release source tree.
var notifyOverlay = filepath.Join("..", "..", "..", "deploy", "authority-host", "notify")

// The owner's files, as the README shows them: placeholders only.
const (
	seatEnvWithNotify = "SEAT_GATE=203.0.113.20:7443\nSEAT_NOTIFY_ARGS=--notify-socket /run/seat-notify/notify.sock\n"
	notifyEnv         = "NOTIFY_SUBJECT=mailto:owner@example.org\n"
)

// TestShippedNotifierUnitPassesTheReleaseScan: the notifier's unit, as a
// release holds it next to the seat's, names no variable like a provider key;
// its environment file is the owner's, read on the host like any other, and
// while it is missing the check says so (the unit would not start, and the
// check cannot know what the file would set). The seat's environment file
// with the notifier's optional flag in it is read as before.
func TestShippedNotifierUnitPassesTheReleaseScan(t *testing.T) {
	notifier, err := os.ReadFile(filepath.Join(notifyOverlay, "etc/systemd/system/ataniq-seat-notify.service"))
	if err != nil {
		t.Fatal(err)
	}
	seat, err := os.ReadFile(filepath.Join(seatOverlay, "etc/systemd/system/ataniq-seat.service"))
	if err != nil {
		t.Fatal(err)
	}
	caddyfile, err := os.ReadFile(filepath.Join(seatOverlay, "etc/caddy/Caddyfile"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name      string
		seatEnv   string // the seat's environment file, "" for none
		notifyEnv string // the notifier's, "" for none
		want      []Hit
	}{
		{"with the owner's files", seatEnvWithNotify, notifyEnv, nil},
		{"the seat without the notifier's flag", "SEAT_GATE=203.0.113.20:7443\n", notifyEnv, nil},
		{"without the notifier's environment file", seatEnvWithNotify, "",
			[]Hit{{Kind: KindIncomplete, Path: PathEnvFile, Detail: "/etc/authority-host/seat-notify/seat-notify.env"}}},
		{"with a provider key in the notifier's", seatEnvWithNotify, notifyEnv + "OPENAI_API_KEY=x\n",
			[]Hit{{Kind: KindEnv, Path: "/etc/authority-host/seat-notify/seat-notify.env", Detail: "OPENAI_API_KEY"}}},
		{"with a provider key in the seat's", seatEnvWithNotify + "ANTHROPIC_API_KEY=x\n", notifyEnv,
			[]Hit{{Kind: KindEnv, Path: "/etc/authority-host/seat/seat.env", Detail: "ANTHROPIC_API_KEY"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := cleanFixture(t)
			f.file(releaseDir+"/etc/systemd/system/ataniq-seat.service", string(seat))
			f.file(releaseDir+"/etc/systemd/system/ataniq-seat-notify.service", string(notifier))
			f.file(releaseDir+"/etc/caddy/Caddyfile", string(caddyfile))
			f.file("etc/authority-host/seat/site.caddy", seatSite)
			if tc.seatEnv != "" {
				f.file("etc/authority-host/seat/seat.env", tc.seatEnv)
			}
			if tc.notifyEnv != "" {
				f.file("etc/authority-host/seat-notify/seat-notify.env", tc.notifyEnv)
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

// TestNotifierBinaryNamesAreNotAgentTools: the binaries a release with both
// parts puts on the host, in the release's own bin folder under /opt, are not
// named like an agent tool or an installer by the shipped list of names, and
// the check still finds one that is.
func TestNotifierBinaryNamesAreNotAgentTools(t *testing.T) {
	lists, err := LoadLists(filepath.Join("..", "..", "..", "deploy", "authority-host"))
	if err != nil {
		t.Fatal(err)
	}
	bin := "opt/authority-host/releases/0008-0123456/bin/"
	scan := func(extra ...string) []Hit {
		f := cleanFixture(t)
		for _, n := range append([]string{"ah-noaicheck", "ah-switch", "ataniq-seat", "ataniq-seat-notify"}, extra...) {
			f.exe(bin + n)
		}
		c := f.checker(lists)
		within(t, func() { c.CheckBinaries(); c.CheckTools() })
		return c.Report().Hits
	}
	if got := scan(); len(got) != 0 {
		t.Errorf("hits %v", got)
	}
	if got := scan("claude"); len(got) != 1 || got[0].Kind != KindBinary {
		t.Errorf("a binary named like an agent: %v", got)
	}
}
