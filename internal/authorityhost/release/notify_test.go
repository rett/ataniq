package release

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"ataniq/internal/authorityhost/switcher"
)

// notifyTree is the notifier's part of the release source: laid over the
// base tree and the seat's part, in that order.
var notifyTree = filepath.Join(baseTree, "notify")

// notifySource is the release source of the seat with its notifier as the
// README has the owner make it: the base tree, the seat's part, then the
// notifier's.
func notifySource(t *testing.T) string {
	t.Helper()
	src := seatSource(t)
	copyTree(t, notifyTree, src)
	return src
}

func notifyOptions(t *testing.T, src string) Options {
	t.Helper()
	return Options{
		Out: t.TempDir(), Counter: 8, Commit: testCommit,
		GoVersion: "go1.27.2", BuiltOn: "dev-linux-amd64", PackageSnapshot: "2026-11-21",
		Bins: testBins(t, "ah-noaicheck", "ah-switch", "ataniq-seat", "ataniq-seat-notify", "ah-pkgstep"),
		Src:  src,
	}
}

// TestNotifyTreeIsAnOverlay: the notifier's part holds only etc/ and share/,
// and adds the unit and replaces the validator, reload and stop lists;
// it leaves the health commands and the Caddyfile
// to the seat's part, and adds nothing to the no-AI check's lists.
func TestNotifyTreeIsAnOverlay(t *testing.T) {
	entries, err := os.ReadDir(notifyTree)
	if err != nil {
		t.Fatal(err)
	}
	var top []string
	for _, e := range entries {
		top = append(top, e.Name())
	}
	if strings.Join(top, " ") != "etc share" {
		t.Errorf("the notifier's part holds %v", top)
	}
	var files []string
	filepath.WalkDir(notifyTree, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(notifyTree, p)
			files = append(files, filepath.ToSlash(rel))
		}
		return err
	})
	sort.Strings(files)
	want := []string{
		"etc/systemd/system/ataniq-seat-notify.service",
		"share/reload.list", "share/stop.list", "share/validators.list",
	}
	if !reflect.DeepEqual(files, want) {
		t.Errorf("the notifier's part:\n%s", strings.Join(files, "\n"))
	}
}

// TestNotifyReleaseBuildsAndChecks: the release made from the base tree with
// both parts over it passes ah-release check (the notifier's unit needs its
// validator, restart and stop), runs the binary of its own
// release, is not static, and the switch's dry run accepts it. The seat's
// unit is covered as before.
func TestNotifyReleaseBuildsAndChecks(t *testing.T) {
	o := notifyOptions(t, notifySource(t))
	dir := built(t, o)
	if f := Check(dir, noRun); len(f) != 0 {
		t.Fatalf("check: %s", lines(f))
	}
	unit := string(mustRead(t, filepath.Join(dir, "etc/systemd/system/ataniq-seat-notify.service")))
	if !strings.Contains(unit, "\nExecStart=/opt/authority-host/releases/0008-0123456/bin/ataniq-seat-notify run ") || strings.Contains(unit, ReleasePlaceholder) {
		t.Errorf("the unit does not run its own release's binary:\n%s", unit)
	}
	seat := string(mustRead(t, filepath.Join(dir, "etc/systemd/system/ataniq-seat.service")))
	if !strings.Contains(seat, "\nExecStart=/opt/authority-host/releases/0008-0123456/bin/ataniq-seat ") {
		t.Error("the seat's unit does not run its own release's binary")
	}
	if strings.Contains(string(mustRead(t, filepath.Join(dir, "MANIFEST"))), "components") {
		t.Error("the release is static")
	}
	// The stop list must cover both services before the next release starts.
	if got := string(mustRead(t, filepath.Join(dir, "share/stop.list"))); !strings.Contains(got, "\nsystemctl stop ataniq-seat-notify.service\n") {
		t.Errorf("missing notifier stop:\n%s", got)
	}

	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, switcher.BaseDir, "releases"), 0o755)
	writeTestFile(t, filepath.Join(root, switcher.PolicyDir, switcher.HostStopList), "systemctl stop ataniq-seat.service\n", 0o644)
	var log bytes.Buffer
	s := switcher.New(switcher.Config{Root: root, Dry: true, From: dir, Out: &log})
	if code := s.Switch(t.Context(), "0008-0123456"); code != switcher.ExitOK {
		t.Fatalf("the switch refuses the release: exit %d\n%s", code, log.String())
	}
}

// TestNotifyReleaseBuildsTwiceTheSame: two builds of both parts are
// byte-identical, so a release is reproducible with the notifier in it.
func TestNotifyReleaseBuildsTwiceTheSame(t *testing.T) {
	src := notifySource(t)
	a := built(t, notifyOptions(t, src))
	b := built(t, notifyOptions(t, src))
	if !reflect.DeepEqual(listing(t, a), listing(t, b)) {
		t.Error("two builds differ")
	}
}

// TestNotifyCoverage: what the check asks of the notifier's unit and what it
// still asks of every other unit.
func TestNotifyCoverage(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(t *testing.T, src string)
		want   []string
	}{
		{name: "both parts", mutate: func(*testing.T, string) {}},
		{name: "no validator of the notifier's unit", mutate: func(t *testing.T, s string) {
			replace(t, s, "share/validators.list", "systemd-analyze verify {release}/etc/systemd/system/ataniq-seat-notify.service\n", "")
		}, want: []string{"validator-missing: etc/systemd/system/ataniq-seat-notify.service"}},
		{name: "no restart of the notifier", mutate: func(t *testing.T, s string) {
			replace(t, s, "share/reload.list", "systemctl restart ataniq-seat-notify.service\n", "")
		}, want: []string{"reload-missing: ataniq-seat-notify"}},
		{name: "a notifier that is only reloaded", mutate: func(t *testing.T, s string) {
			replace(t, s, "share/reload.list", "systemctl restart ataniq-seat-notify.service", "systemctl reload ataniq-seat-notify.service")
		}, want: []string{"reload-missing: ataniq-seat-notify"}},
		{name: "a notifier that runs another release's binary", mutate: func(t *testing.T, s string) {
			replace(t, s, "etc/systemd/system/ataniq-seat-notify.service", "{release}/bin/ataniq-seat-notify", "/usr/bin/ataniq-seat-notify")
		}, want: []string{"decision-exec: etc/systemd/system/ataniq-seat-notify.service"}},
		{name: "the seat still has to be stopped", mutate: func(t *testing.T, s string) {
			set(t, s, "share/stop.list", "# none\n")
		}, want: []string{"list-empty: share/stop.list", "stop-missing: ataniq-seat", "stop-missing: ataniq-seat-notify"}},
		{name: "a unit that is not the notifier's is still stopped", mutate: func(t *testing.T, s string) {
			unit, _ := os.ReadFile(filepath.Join(s, "etc/systemd/system/ataniq-seat-notify.service"))
			set(t, s, "etc/systemd/system/ataniq-seat-notify-two.service", string(unit))
			replace(t, s, "share/validators.list", "caddy validate", "systemd-analyze verify {release}/etc/systemd/system/ataniq-seat-notify-two.service\ncaddy validate")
			replace(t, s, "share/reload.list", "systemctl restart caddy", "systemctl restart ataniq-seat-notify-two.service\nsystemctl restart caddy")
		}, want: []string{"stop-missing: ataniq-seat-notify-two"}},
		{name: "a unit of the notifier's name that runs the seat's binary is stopped", mutate: func(t *testing.T, s string) {
			set(t, s, "share/stop.list", "systemctl stop ataniq-seat.service\n")
			replace(t, s, "etc/systemd/system/ataniq-seat-notify.service", "{release}/bin/ataniq-seat-notify", "{release}/bin/ataniq-seat")
		}, want: []string{"stop-missing: ataniq-seat-notify"}},
		{name: "a drop-in of the notifier's name that runs another binary is stopped", mutate: func(t *testing.T, s string) {
			set(t, s, "share/stop.list", "systemctl stop ataniq-seat.service\n")
			set(t, s, "etc/systemd/system/ataniq-seat-notify.service.d/10-x.conf", "[Service]\nExecStart=\nExecStart={release}/bin/ataniq-seat run\n")
		}, want: []string{"stop-missing: ataniq-seat-notify"}},
		{name: "a second ExecStart of the notifier's unit that runs another binary is stopped", mutate: func(t *testing.T, s string) {
			set(t, s, "share/stop.list", "systemctl stop ataniq-seat.service\n")
			replace(t, s, "etc/systemd/system/ataniq-seat-notify.service", "\nRestart=on-failure", "\nExecStart={release}/bin/ataniq-seat\nRestart=on-failure")
		}, want: []string{"stop-missing: ataniq-seat-notify"}},
		{name: "the notifier alone is no decision service", mutate: func(t *testing.T, s string) {
			os.Remove(filepath.Join(s, "etc/systemd/system/ataniq-seat.service"))
			replace(t, s, "share/validators.list", "systemd-analyze verify {release}/etc/systemd/system/ataniq-seat.service\n", "")
			replace(t, s, "share/reload.list", "systemctl restart ataniq-seat.service\n", "")
			set(t, s, "share/stop.list", "systemctl stop caddy\n")
		}, want: []string{"no-decision-service", "stop-missing: ataniq-seat-notify"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := notifySource(t)
			c.mutate(t, src)
			dir := built(t, notifyOptions(t, src))
			got := codes(Check(dir, noRun))
			if !reflect.DeepEqual(got, c.want) && !(len(got) == 0 && len(c.want) == 0) {
				t.Errorf("findings %v, want %v", got, c.want)
			}
		})
	}
}

// TestNotifyUnit: the notifier's unit runs as its own user, in a group of its
// own (seat-notify, never the seat's), with the arguments the design asks for, no network address of its own
// (the owner's drop-in allows the resolver and the push range), no listener,
// the seat's hardening, and never an [Install] section.
func TestNotifyUnit(t *testing.T) {
	text := string(mustRead(t, filepath.Join(notifyTree, "etc/systemd/system/ataniq-seat-notify.service")))
	keys := unitKeys(text)
	exec := strings.Fields(strings.Join(keys["Service.ExecStart"], " "))
	if len(keys["Service.ExecStart"]) != 1 || len(exec) < 2 || exec[0] != "{release}/bin/ataniq-seat-notify" || exec[1] != "run" {
		t.Fatalf("ExecStart %q", keys["Service.ExecStart"])
	}
	args := map[string]string{}
	for i := 2; i+1 < len(exec); i += 2 {
		args[exec[i]] = exec[i+1]
	}
	for flag, want := range map[string]string{
		"--socket":        "/run/seat-notify/notify.sock",
		"--seat-user":     "seat",
		"--vapid-key":     "/var/lib/seat-notify/vapid.pem",
		"--subscriptions": "/var/lib/seat-notify/subscriptions.json",
		"--vapid-subject": "${NOTIFY_SUBJECT}",
	} {
		if args[flag] != want {
			t.Errorf("%s %q, want %q", flag, args[flag], want)
		}
	}
	// No push host, TTL or other flag: the defaults are the design's.
	if len(args) != 5 || len(exec) != 12 {
		t.Errorf("ExecStart has other arguments: %q", exec)
	}
	for key, want := range map[string]string{
		"Service.User": "seat-notify", "Service.Group": "seat-notify",
		"Service.EnvironmentFile":         "/etc/authority-host/seat-notify/seat-notify.env",
		"Service.RuntimeDirectory":        "seat-notify",
		"Service.RuntimeDirectoryMode":    "0750",
		"Service.StateDirectory":          "seat-notify",
		"Service.StateDirectoryMode":      "0700",
		"Service.UMask":                   "0077",
		"Service.IPAddressDeny":           "any",
		"Service.SocketBindDeny":          "any",
		"Service.RestrictAddressFamilies": "AF_UNIX AF_INET AF_INET6",
		"Service.NoNewPrivileges":         "yes",
		"Service.CapabilityBoundingSet":   "",
		"Service.AmbientCapabilities":     "",
		"Service.ProtectSystem":           "strict",
		"Service.ProtectHome":             "yes",
		"Service.PrivateTmp":              "yes",
		"Service.PrivateDevices":          "yes",
		"Service.ProtectProc":             "invisible",
		"Service.ProcSubset":              "pid",
		"Service.MemoryDenyWriteExecute":  "yes",
		"Service.RestrictNamespaces":      "yes",
		"Service.LockPersonality":         "yes",
		"Service.RemoveIPC":               "yes",
		"Service.SystemCallArchitectures": "native",
		"Service.SystemCallErrorNumber":   "EPERM",
	} {
		if got := keys[key]; len(got) != 1 || got[0] != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	if got := keys["Service.SystemCallFilter"]; !reflect.DeepEqual(got, []string{"@system-service", "~@privileged @resources"}) {
		t.Errorf("SystemCallFilter %q", got)
	}
	// The seat's hardening is the notifier's: every hardening key of the
	// seat's unit is there with the same value, but the sizes, the user, the
	// groups and the network. SupplementaryGroups is the one key the seat's
	// unit has and the notifier's does not: it is how the seat reaches the
	// notifier's group (see TestNotifyGroupIsolation).
	seat := unitKeys(string(mustRead(t, filepath.Join(seatTree, "etc/systemd/system/ataniq-seat.service"))))
	own := map[string]bool{
		"Service.User": true, "Service.Group": true, "Service.ExecStart": true, "Service.EnvironmentFile": true,
		"Service.RuntimeDirectory": true, "Service.StateDirectory": true, "Service.MemoryMax": true, "Service.TasksMax": true,
		"Unit.Description": true, "Service.SupplementaryGroups": true,
	}
	for k, v := range seat {
		if own[k] || k[0] == '[' {
			continue
		}
		if !reflect.DeepEqual(keys[k], v) {
			t.Errorf("%s: the seat has %q, the notifier %q", k, v, keys[k])
		}
	}
	if _, ok := keys["Service.IPAddressAllow"]; ok {
		t.Error("the release allows an address: the resolver's and the push range are the owner's drop-in")
	}
	if _, ok := keys["[Install]"]; ok {
		t.Error("the unit has an [Install] section: only the reload list may start it")
	}
	for k, vs := range keys {
		for _, v := range vs {
			if strings.HasPrefix(k, "Unit.Condition") || strings.HasPrefix(k, "Unit.Assert") {
				t.Errorf("%s=%s gates the start", k, v)
			}
			if strings.HasPrefix(k, "Unit.") && strings.Contains(v, "ah-switch-boot") {
				t.Errorf("%s=%s orders the notifier after the boot unit", k, v)
			}
		}
	}
	// The VAPID subject and any key are the owner's, in an environment file
	// of the host: nothing like one is in the unit.
	for _, bad := range []string{"mailto:", "BEGIN", "PRIVATE KEY"} {
		for _, line := range strings.Split(text, "\n") {
			if l := strings.TrimSpace(line); l != "" && l[0] != '#' && strings.Contains(l, bad) {
				t.Errorf("the unit holds %q", bad)
			}
		}
	}
}

// TestNotifyLists: the notifier's lists are the seat's with the notifier's
// lines added, so laying the notifier's part over the seat's loses nothing of
// the seat's, and the seat's own lists did not change.
func TestNotifyLists(t *testing.T) {
	commands := func(dir, name string) []string {
		var out []string
		for _, line := range strings.Split(string(mustRead(t, filepath.Join(dir, "share", name))), "\n") {
			if line != "" && line[0] != '#' {
				out = append(out, line)
			}
		}
		return out
	}
	for name, want := range map[string][]string{
		"validators.list": {
			"caddy validate --config {release}/etc/caddy/Caddyfile --adapter caddyfile",
			"systemd-analyze verify {release}/etc/systemd/system/ataniq-seat.service",
			"systemd-analyze verify {release}/etc/systemd/system/ataniq-seat-notify.service",
			"test -L /etc/systemd/system/ataniq-seat.service",
			"test -L /etc/systemd/system/ataniq-seat-notify.service",
		},
		"reload.list": {
			"systemctl daemon-reload", "systemctl restart ataniq-seat-notify.service",
			"systemctl restart ataniq-seat.service", "systemctl restart caddy",
		},
	} {
		if got := commands(notifyTree, name); !reflect.DeepEqual(got, want) {
			t.Errorf("%s:\n%s", name, strings.Join(got, "\n"))
		}
		// Every command of the seat's list is in the notifier's.
		have := map[string]bool{}
		for _, c := range commands(notifyTree, name) {
			have[c] = true
		}
		for _, c := range commands(seatTree, name) {
			if !have[c] {
				t.Errorf("%s: the seat's %q is lost", name, c)
			}
		}
	}
	// The seat's lists are as they were: the seat alone still builds.
	if got := commands(seatTree, "reload.list"); len(got) != 3 {
		t.Errorf("the seat's reload list changed: %v", got)
	}
}

// TestNotifySeatUnitTakesTheOptionalFlag: the seat's unit gets the notifier's
// socket through its environment file, empty by default, as a separate word
// ($NAME, not ${NAME}, so an empty value is no argument), and keeps the
// network denial it had.
func TestNotifySeatUnitTakesTheOptionalFlag(t *testing.T) {
	text := string(mustRead(t, filepath.Join(seatTree, "etc/systemd/system/ataniq-seat.service")))
	keys := unitKeys(text)
	exec := strings.Fields(strings.Join(keys["Service.ExecStart"], " "))
	if exec[len(exec)-1] != "$SEAT_NOTIFY_ARGS" {
		t.Errorf("the last word of ExecStart is %q", exec[len(exec)-1])
	}
	if strings.Contains(text, "--notify-socket /run") && regexp.MustCompile(`(?m)^[^#].*--notify-socket`).MatchString(text) {
		t.Error("the unit sets the notifier's socket itself: it is the owner's, in seat.env")
	}
	for key, want := range map[string]string{"Service.IPAddressDeny": "any", "Service.SocketBindDeny": "any", "Service.RestrictAddressFamilies": "AF_UNIX AF_INET AF_INET6"} {
		if got := keys[key]; len(got) != 1 || got[0] != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

// TestNotifyGroupIsolation: the notifier, the one unit with egress, has a
// group of its own and none of the seat's, so it cannot reach the seat's
// socket (/run/seat, group seat); the seat reaches the notifier's socket by
// naming the notifier's group, and only that, as supplementary.
func TestNotifyGroupIsolation(t *testing.T) {
	notifier := unitKeys(string(mustRead(t, filepath.Join(notifyTree, "etc/systemd/system/ataniq-seat-notify.service"))))
	seat := unitKeys(string(mustRead(t, filepath.Join(seatTree, "etc/systemd/system/ataniq-seat.service"))))
	if got := notifier["Service.Group"]; len(got) != 1 || got[0] != "seat-notify" {
		t.Errorf("the notifier's Group = %q, want seat-notify", got)
	}
	if got, ok := notifier["Service.SupplementaryGroups"]; ok {
		t.Errorf("the notifier has supplementary groups %q", got)
	}
	for k, vs := range notifier {
		if !strings.HasPrefix(k, "Service.") || k == "Service.RuntimeDirectory" || k == "Service.StateDirectory" {
			continue
		}
		for _, v := range vs {
			if k == "Service.ExecStart" || k == "Service.User" {
				continue
			}
			for _, f := range strings.Fields(v) {
				if f == "seat" {
					t.Errorf("the notifier's %s names the group seat: %q", k, v)
				}
			}
		}
	}
	if got := seat["Service.SupplementaryGroups"]; len(got) != 1 || got[0] != "seat-notify" {
		t.Errorf("the seat's SupplementaryGroups = %q, want exactly seat-notify", got)
	}
	if got := seat["Service.Group"]; len(got) != 1 || got[0] != "seat" {
		t.Errorf("the seat's Group = %q", got)
	}
}

// TestNotifyTreeHasNoPrivateNames: nothing of the notifier's part names a
// host, a user's home, an address but a documentation one or a placeholder
// name.
func TestNotifyTreeHasNoPrivateNames(t *testing.T) {
	addr := regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b`)
	name := regexp.MustCompile(`\b[a-z0-9-]+\.(example|invalid|test|localhost)\b|\b[a-z0-9-]+(\.[a-z0-9-]+)*\.(com|net|org|io|dev|lan|home|internal|local)\b`)
	filepath.WalkDir(notifyTree, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		text := string(mustRead(t, p))
		if strings.Contains(text, "/ho"+"me/") {
			t.Errorf("%s names a home folder", p)
		}
		for _, a := range addr.FindAllString(text, -1) {
			if !strings.HasPrefix(a, "127.") && !strings.HasPrefix(a, "192.0.2.") && !strings.HasPrefix(a, "198.51.100.") && !strings.HasPrefix(a, "203.0.113.") {
				t.Errorf("%s names the address %s", p, a)
			}
		}
		for _, n := range name.FindAllString(text, -1) {
			if !strings.HasSuffix(n, ".example") && !strings.HasSuffix(n, ".invalid") && !strings.HasSuffix(n, ".test") {
				t.Errorf("%s names %s", p, n)
			}
		}
		return nil
	})
}
