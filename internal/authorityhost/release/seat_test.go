package release

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"ataniq/internal/authorityhost/switcher"
)

// The source trees this repository ships: the first (static) release's, and
// the seat app's part, laid over it to make the seat's release.
var (
	baseTree = filepath.Join("..", "..", "..", "deploy", "authority-host")
	seatTree = filepath.Join(baseTree, "seat")
)

// copyTree copies the regular files of the folders etc/ and share/ of from
// into to, replacing a file that is there, as cp -R from/. to/ does for
// those two folders.
func copyTree(t *testing.T, from, to string) {
	t.Helper()
	for _, top := range []string{"etc", "share"} {
		err := filepath.WalkDir(filepath.Join(from, top), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			rel, _ := filepath.Rel(from, p)
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			writeTestFile(t, filepath.Join(to, rel), string(data), 0o644)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

// seatSource is the seat release's source as the README has the owner make
// it: the base tree, then the seat's part over it.
func seatSource(t *testing.T) string {
	t.Helper()
	src := t.TempDir()
	copyTree(t, baseTree, src)
	copyTree(t, seatTree, src)
	return src
}

// TestSeatTreeIsAnOverlay: the seat's part holds only etc/ and share/, and
// replaces only the four command lists and the Caddyfile of the base tree;
// its Caddyfile keeps the base's status site as it is. The no-AI check's
// lists are the base tree's alone.
func TestSeatTreeIsAnOverlay(t *testing.T) {
	entries, err := os.ReadDir(seatTree)
	if err != nil {
		t.Fatal(err)
	}
	var top []string
	for _, e := range entries {
		top = append(top, e.Name())
	}
	if strings.Join(top, " ") != "etc share" {
		t.Errorf("the seat's part holds %v", top)
	}
	var files []string
	filepath.WalkDir(seatTree, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(seatTree, p)
			files = append(files, filepath.ToSlash(rel))
		}
		return err
	})
	sort.Strings(files)
	want := []string{
		"etc/caddy/Caddyfile",
		"etc/systemd/system/ataniq-seat.service",
		"share/health.cmd", "share/reload.list", "share/stop.list", "share/validators.list",
	}
	if strings.Join(files, "\n") != strings.Join(want, "\n") {
		t.Errorf("the seat's part:\n%s", strings.Join(files, "\n"))
	}
	base, _ := os.ReadFile(filepath.Join(baseTree, "etc/caddy/Caddyfile"))
	seat, _ := os.ReadFile(filepath.Join(seatTree, "etc/caddy/Caddyfile"))
	i := bytes.Index(base, []byte("\n:80 {"))
	if i < 0 || !bytes.Contains(seat, base[i:]) {
		t.Error("the seat's Caddyfile does not keep the base's :80 site as it is")
	}
}

// TestSeatReleaseBuildsAndChecks: the seat release, built from the base
// tree with the seat's part over it and not static, passes ah-release
// check (the coverage of its lists: a validator of the unit, the reload and
// the stop of the service), runs the binary of its own release, and the
// switch's dry run accepts it.
func TestSeatReleaseBuildsAndChecks(t *testing.T) {
	o := Options{
		Out: t.TempDir(), Counter: 7, Commit: testCommit,
		GoVersion: "go1.27.2", BuiltOn: "dev-linux-amd64", PackageSnapshot: "2026-11-21",
		Bins: testBins(t, "ah-noaicheck", "ah-switch", "ataniq-seat", "ah-pkgstep"),
		Src:  seatSource(t),
	}
	dir := built(t, o)
	if f := Check(dir, noRun); len(f) != 0 {
		t.Fatalf("check: %s", lines(f))
	}
	unit, err := os.ReadFile(filepath.Join(dir, "etc/systemd/system/ataniq-seat.service"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(unit), "\nExecStart=/opt/authority-host/releases/0007-0123456/bin/ataniq-seat ") || strings.Contains(string(unit), ReleasePlaceholder) {
		t.Errorf("the unit does not run its own release's binary:\n%s", unit)
	}
	if strings.Contains(string(mustRead(t, filepath.Join(dir, "MANIFEST"))), "components") {
		t.Error("the seat release is static")
	}

	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, switcher.BaseDir, "releases"), 0o755)
	writeTestFile(t, filepath.Join(root, switcher.PolicyDir, switcher.HostStopList), "systemctl stop ataniq-seat.service\n", 0o644)
	var log bytes.Buffer
	s := switcher.New(switcher.Config{Root: root, Dry: true, From: dir, Out: &log})
	if code := s.Switch(t.Context(), "0007-0123456"); code != switcher.ExitOK {
		t.Fatalf("the switch refuses the seat release: exit %d\n%s", code, log.String())
	}
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// unitKeys reads a unit file into its sections' keys, each with its values
// in order; comments and blank lines are skipped.
func unitKeys(text string) map[string][]string {
	keys := map[string][]string{}
	section := ""
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "" || line[0] == '#' || line[0] == ';':
		case line[0] == '[':
			section = strings.Trim(line, "[]")
			keys["["+section+"]"] = nil
		default:
			k, v, _ := strings.Cut(line, "=")
			keys[section+"."+k] = append(keys[section+"."+k], v)
		}
	}
	return keys
}

// TestSeatUnit: the seat's unit runs as its own user, with the arguments
// the design asks for (the origin and the gate's pin from the owner's
// files, the socket the proxy reaches, the client address header the
// Caddyfile sets, the gate from the owner's environment file, the key in
// its state folder, the decision guard of the switch's state folder), and
// no network but what the owner allows; it is never enabled, never gates
// its start on the inhibit and is not ordered after the boot unit.
func TestSeatUnit(t *testing.T) {
	text := string(mustRead(t, filepath.Join(seatTree, "etc/systemd/system/ataniq-seat.service")))
	keys := unitKeys(text)
	exec := strings.Fields(strings.Join(keys["Service.ExecStart"], " "))
	if len(keys["Service.ExecStart"]) != 1 || len(exec) == 0 || exec[0] != "{release}/bin/ataniq-seat" {
		t.Fatalf("ExecStart %q", keys["Service.ExecStart"])
	}
	args := map[string]string{}
	for i := 1; i+1 < len(exec); i += 2 {
		args[exec[i]] = exec[i+1]
	}
	for flag, want := range map[string]string{
		"--origin-file":        "/etc/authority-host/seat/origin",
		"--socket":             "/run/seat/http.sock",
		"--socket-mode":        "0660",
		"--client-addr-header": "X-Seat-Client-Addr",
		"--gate":               "${SEAT_GATE}",
		"--gate-pin-file":      "/etc/authority-host/seat/gate.pin",
		"--identity":           "/var/lib/seat/identity",
		"--decision-guard":     "/var/lib/authority-host",
	} {
		if args[flag] != want {
			t.Errorf("%s %q, want %q", flag, args[flag], want)
		}
	}
	if len(args) != 8 || len(exec) != 17 {
		t.Errorf("ExecStart has other arguments: %q", exec)
	}
	for key, want := range map[string]string{
		"Service.User": "seat", "Service.Group": "seat",
		"Service.EnvironmentFile":         "/etc/authority-host/seat/seat.env",
		"Service.RuntimeDirectory":        "seat",
		"Service.RuntimeDirectoryMode":    "0750",
		"Service.StateDirectory":          "seat",
		"Service.StateDirectoryMode":      "0700",
		"Service.UMask":                   "0077",
		"Service.IPAddressDeny":           "any",
		"Service.SocketBindDeny":          "any",
		"Service.RestrictAddressFamilies": "AF_UNIX AF_INET AF_INET6",
		"Service.NoNewPrivileges":         "yes",
		"Service.CapabilityBoundingSet":   "",
		"Service.ProtectSystem":           "strict",
		"Service.ProtectHome":             "yes",
		"Service.PrivateTmp":              "yes",
		"Service.PrivateDevices":          "yes",
		"Service.MemoryDenyWriteExecute":  "yes",
		"Service.SystemCallArchitectures": "native",
	} {
		if got := keys[key]; len(got) != 1 || got[0] != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	if _, ok := keys["Service.IPAddressAllow"]; ok {
		t.Error("the release allows an address: the gate's is the owner's drop-in")
	}
	if _, ok := keys["[Install]"]; ok {
		t.Error("the unit has an [Install] section: only the reload list may start it")
	}
	for k, vs := range keys {
		for _, v := range vs {
			if (strings.HasPrefix(k, "Unit.Condition") || strings.HasPrefix(k, "Unit.Assert")) && strings.Contains(v, "decisions.inhibit") {
				t.Errorf("%s=%s gates the start on the inhibit", k, v)
			}
			if strings.HasPrefix(k, "Unit.") && strings.Contains(v, "ah-switch-boot") {
				t.Errorf("%s=%s orders the seat after the boot unit, whose reload would wait on it", k, v)
			}
		}
	}
}

// TestSeatCaddyfile: the seat's site goes to the app's socket with the
// proxy rules of the design: the Host passed as it came, Origin and
// Sec-Fetch-Site untouched, the client address header the unit names set on
// every request, nothing cached; the seat's name is the owner's site file,
// imported, and is not in the release.
func TestSeatCaddyfile(t *testing.T) {
	text := string(mustRead(t, filepath.Join(seatTree, "etc/caddy/Caddyfile")))
	var directives []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && line[0] != '#' {
			directives = append(directives, line)
		}
	}
	body := strings.Join(directives, "\n")
	for _, want := range []string{
		"(seat) {",
		"reverse_proxy unix//run/seat/http.sock {",
		"header_up Host {hostport}",
		"header_up X-Seat-Client-Addr {client_ip}",
		"header_down Cache-Control no-store",
		"trusted_proxies static private_ranges",
		"trusted_proxies_strict",
		"import /etc/authority-host/seat/site.caddy",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the Caddyfile lacks %q", want)
		}
	}
	for _, line := range directives {
		low := strings.ToLower(line)
		if (strings.Contains(low, "origin") || strings.Contains(low, "sec-fetch")) && strings.HasPrefix(low, "header") {
			t.Errorf("the Caddyfile touches a header the app checks: %s", line)
		}
		if first := strings.Fields(low)[0]; first == "cache" || strings.HasPrefix(first, "cache_") {
			t.Errorf("the Caddyfile caches: %s", line)
		}
		if strings.HasPrefix(line, "http://") || strings.HasPrefix(line, "https://") {
			t.Errorf("the Caddyfile names a site: %s", line)
		}
	}
	if strings.Count(body, "reverse_proxy") != 1 {
		t.Error("more than one proxy")
	}
}

// TestSeatLists: the seat release's lists validate its two files, reload
// systemd and restart the seat and caddy, check the front end and the seat
// on its socket, and stop the seat.
func TestSeatLists(t *testing.T) {
	commands := func(name string) []string {
		var out []string
		for _, line := range strings.Split(string(mustRead(t, filepath.Join(seatTree, "share", name))), "\n") {
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
		},
		"reload.list": {"systemctl daemon-reload", "systemctl restart ataniq-seat.service", "systemctl restart caddy"},
		"health.cmd": {
			"curl -fsS --max-time 5 http://127.0.0.1/healthz",
			"curl -fsS --max-time 5 --unix-socket /run/seat/http.sock http://localhost/healthz",
		},
		"stop.list": {"systemctl stop ataniq-seat.service"},
	} {
		if got := commands(name); strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Errorf("%s:\n%s", name, strings.Join(got, "\n"))
		}
	}
}

// TestSeatTreeHasNoPrivateNames: nothing of the seat's part names a host, a
// user's home or an address but loopback, a documentation one (192.0.2.x,
// 198.51.100.x, 203.0.113.x) or a placeholder name (seat.example).
func TestSeatTreeHasNoPrivateNames(t *testing.T) {
	addr := regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b`)
	name := regexp.MustCompile(`\b[a-z0-9-]+\.(example|invalid|test|localhost)\b|\b[a-z0-9-]+(\.[a-z0-9-]+)*\.(com|net|org|io|dev|lan|home|internal|local)\b`)
	filepath.WalkDir(seatTree, func(p string, d fs.DirEntry, err error) error {
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
