package release

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ataniq/internal/authorityhost/noaicheck"
	"ataniq/internal/authorityhost/switcher"
)

func TestWitnessRelease(t *testing.T) {
	for _, tc := range []struct{ name, path, old, new, want string }{
		{name: "shipped"},
		{"privileged user", noaicheck.WitnessUnit, "User=ataniq-witness", "User=root", CodeWitness},
		{"shared group", noaicheck.WitnessUnit, "Group=ataniq-witness", "Group=seat", CodeWitness},
		{"bind exemption", noaicheck.WitnessUnit, "SocketBindDeny=any", "SocketBindDeny=any\nSocketBindAllow=ipv4:tcp:9000", CodeWitness},
		{"extra preflight", noaicheck.WitnessUnit, "Type=exec", "Type=exec\nExecStartPre=/bin/true", CodeWitness},
		{"wrong executable", noaicheck.WitnessUnit, "/bin/ataniq-witness run", "/bin/ataniq-seat run", CodeWitness},
		{"socket activation", "etc/systemd/system/ataniq-witness.socket", "", "[Socket]\nListenStream=9000\n", CodeWitness},
		{"indirect socket", "etc/systemd/system/other.socket", "", "[Socket]\nListenStream=9000\nService=ataniq-witness.service\n", CodeWitness},
		{"unit override", noaicheck.WitnessUnit + ".d/user.conf", "", "[Service]\nUser=seat\n", CodeWitness},
		{"global override", "etc/systemd/system/service.d/account.conf", "", "[Service]\nUser=seat\n", CodeWitness},
		{"link target unchecked", "share/validators.list", "sh {release}/share/check-witness-unit-link /" + noaicheck.WitnessUnit, "test -L /" + noaicheck.WitnessUnit, CodeWitness},
		{"missing health", "share/health.cmd", "{release}/bin/ataniq-witness status --config " + noaicheck.WitnessConfig, "", CodeWitness},
		{"missing stop", "share/stop.list", "systemctl stop ataniq-witness.service", "", CodeStopNone},
		{"missing restart", "share/reload.list", "systemctl restart ataniq-witness.service", "", CodeReloadNone},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := notifySource(t)
			copyTree(t, filepath.Join(baseTree, "witness"), src)
			o := notifyOptions(t, src)
			o.Bins = append(o.Bins, testBins(t, "ataniq-witness")...)
			switch {
			case tc.path == "":
			case tc.old == "":
				set(t, src, tc.path, tc.new)
			default:
				replace(t, src, tc.path, tc.old, tc.new)
			}
			dir := built(t, o)
			got := Check(dir, noRun)
			if tc.want == "" {
				if len(got) != 0 {
					t.Fatal(got)
				}
				root := t.TempDir()
				if err := os.MkdirAll(filepath.Join(root, switcher.BaseDir, "releases"), 0755); err != nil {
					t.Fatal(err)
				}
				writeTestFile(t, filepath.Join(root, switcher.PolicyDir, switcher.HostStopList), "systemctl stop ataniq-witness.service\n", 0644)
				var log bytes.Buffer
				s := switcher.New(switcher.Config{Root: root, Dry: true, From: dir, Out: &log})
				if code := s.Switch(t.Context(), filepath.Base(dir)); code != 0 {
					t.Fatalf("dry switch: %s", &log)
				}
				return
			}
			if !slices.ContainsFunc(got, func(f Finding) bool { return f.Code == tc.want }) {
				t.Fatal(got)
			}
		})
	}
}

func TestWitnessUnitHardening(t *testing.T) {
	seat := unitKeys(string(mustRead(t, filepath.Join(seatTree, "etc/systemd/system/ataniq-seat.service"))))
	unit := unitKeys(string(mustRead(t, filepath.Join(baseTree, "witness", noaicheck.WitnessUnit))))
	for k, want := range seat {
		switch k {
		case "Unit.Description", "Service.ExecStart", "Service.EnvironmentFile", "Service.User", "Service.Group", "Service.SupplementaryGroups", "Service.Type", "Service.RuntimeDirectory", "Service.RuntimeDirectoryMode", "Service.StateDirectory":
			continue
		}
		if !slices.Equal(unit[k], want) {
			t.Errorf("%s = %q, want %q", k, unit[k], want)
		}
	}
	for _, name := range []string{"units.allow", "ports.allow"} {
		for line := range strings.Lines(string(mustRead(t, filepath.Join(baseTree, "witness", "share", name)))) {
			if s := strings.TrimSpace(line); s != "" && !strings.HasPrefix(s, "#") {
				t.Errorf("unexpected grant in %s", name)
			}
		}
	}
}

func TestWitnessRestartPolicy(t *testing.T) {
	keys := unitKeys(string(mustRead(t, filepath.Join(baseTree, "witness", noaicheck.WitnessUnit))))
	for k, v := range map[string]string{"Unit.StartLimitIntervalSec": "300", "Unit.StartLimitBurst": "5", "Service.RestartPreventExitStatus": "1"} {
		if !slices.Equal(keys[k], []string{v}) {
			t.Errorf("%s = %q", k, keys[k])
		}
	}
	// RestartPreventExitStatus only applies to the main process.
	if len(keys["Service.ExecStartPre"]) != 0 {
		t.Error("preflight refusal bypasses RestartPreventExitStatus")
	}
}

func TestWitnessOverlayLists(t *testing.T) {
	composed := notifySource(t)
	for _, name := range []string{"validators.list", "reload.list", "stop.list", "health.cmd"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join("share", name)
			parent := string(mustRead(t, filepath.Join(composed, path)))
			overlay := string(mustRead(t, filepath.Join(baseTree, "witness", path)))
			if !strings.HasPrefix(overlay, "#") {
				t.Error("missing overlay explanation")
			}
			var remaining []string
			for line := range strings.Lines(parent) {
				line = strings.TrimSpace(line)
				if line != "" && !strings.HasPrefix(line, "#") {
					remaining = append(remaining, line)
				}
			}
			for line := range strings.Lines(overlay) {
				if len(remaining) > 0 && strings.TrimSpace(line) == remaining[0] {
					remaining = remaining[1:]
				}
			}
			if len(remaining) > 0 {
				t.Errorf("first missing or out-of-order command: %s", remaining[0])
			}
		})
	}
}

func TestWitnessInstalledUnitLink(t *testing.T) {
	src := filepath.Join(baseTree, "witness")
	const validator = "sh {release}/share/check-witness-unit-link /" + noaicheck.WitnessUnit
	if !slices.Contains(strings.Split(string(mustRead(t, filepath.Join(src, "share/validators.list"))), "\n"), validator) {
		t.Fatal("missing exact link validator")
	}
	unit := filepath.Join(t.TempDir(), "unit")
	target := "/opt/authority-host/current/" + noaicheck.WitnessUnit
	for _, dest := range []string{target, "/another/release/unit", target + "\n", ""} {
		if dest != "" {
			if err := os.Symlink(dest, unit); err != nil {
				t.Fatal(err)
			}
		}
		err := exec.Command("sh", filepath.Join(src, "share/check-witness-unit-link"), unit).Run()
		if (err == nil) != (dest == target) {
			t.Errorf("target %q: %v", dest, err)
		}
		if dest != "" {
			if err := os.Remove(unit); err != nil {
				t.Fatal(err)
			}
		}
	}
}
