package release

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"syscall"
	"testing"
)

var noRun = CheckOptions{NoNoAICheck: true}

func TestCheckPasses(t *testing.T) {
	for name, o := range map[string]Options{
		"static":  testOptions(t, staticSrc(t)),
		"dynamic": dynamicOptions(t, dynamicSrc(t)),
	} {
		dir := built(t, o)
		if f := Check(dir, noRun); len(f) != 0 {
			t.Errorf("%s: %s", name, lines(f))
		}
	}
}

// replace rewrites the file rel of src, which must hold old.
func replace(t *testing.T, src, rel, old, new string) {
	t.Helper()
	p := filepath.Join(src, rel)
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), old) {
		t.Fatalf("%s lacks %q", rel, old)
	}
	writeTestFile(t, p, strings.Replace(string(data), old, new, 1), 0o644)
}

func set(t *testing.T, src, rel, content string) {
	t.Helper()
	writeTestFile(t, filepath.Join(src, rel), content, 0o644)
}

// TestCheckCoverage gives each rule of the coverage a passing and a failing
// source. mutate changes the source tree; want is the exact findings.
func TestCheckCoverage(t *testing.T) {
	type tc struct {
		name    string
		static  bool
		mutate  func(t *testing.T, src string)
		want    []string // empty: passes
		options func(o *Options)
	}
	cases := []tc{
		// validators
		{name: "caddy validator missing", static: true, mutate: func(t *testing.T, s string) {
			set(t, s, "share/validators.list", "# none\n")
		}, want: []string{"validator-missing: etc/caddy/Caddyfile"}},
		{name: "caddy validator with its adapter", static: true, mutate: func(t *testing.T, s string) {
			set(t, s, "share/validators.list", "caddy validate --config {release}/etc/caddy/Caddyfile --adapter caddyfile\n")
		}},
		{name: "caddy validator with --config=", static: true, mutate: func(t *testing.T, s string) {
			set(t, s, "share/validators.list", "/usr/bin/caddy validate --config={release}/etc/caddy/Caddyfile\n")
		}},
		{name: "caddy validator twice", static: true, mutate: func(t *testing.T, s string) {
			set(t, s, "share/validators.list", testValidators+testValidators)
		}, want: []string{"validator-twice: etc/caddy/Caddyfile"}},
		{name: "caddy validator of the live file", static: true, mutate: func(t *testing.T, s string) {
			set(t, s, "share/validators.list", "caddy validate --config /opt/authority-host/current/etc/caddy/Caddyfile\n")
		}, want: []string{"validator-missing: etc/caddy/Caddyfile"}},
		{name: "validator of a file that is not shipped", static: true, mutate: func(t *testing.T, s string) {
			set(t, s, "share/validators.list", testValidators+"caddy validate --config {release}/etc/caddy/Other\n")
		}, want: []string{"validator-unknown-file: etc/caddy/Other"}},
		{name: "the wrong tool for a file", static: true, mutate: func(t *testing.T, s string) {
			set(t, s, "share/validators.list", "nft -c -f {release}/etc/caddy/Caddyfile\n")
		}, want: []string{"validator-missing: etc/caddy/Caddyfile", "validator-unknown-file: etc/caddy/Caddyfile"}},
		{name: "nft without -c is no validator", mutate: func(t *testing.T, s string) {
			replace(t, s, "share/validators.list", "nft -c -f", "nft -f")
		}, want: []string{"validator-missing: etc/nftables/rules.nft"}},
		{name: "nft validator missing", mutate: func(t *testing.T, s string) {
			replace(t, s, "share/validators.list", "nft -c -f {release}/etc/nftables/rules.nft\n", "")
		}, want: []string{"validator-missing: etc/nftables/rules.nft"}},
		{name: "nft validator twice", mutate: func(t *testing.T, s string) {
			replace(t, s, "share/validators.list", "nft -c -f {release}/etc/nftables/rules.nft\n", "nft -c -f {release}/etc/nftables/rules.nft\nnft --check --file {release}/etc/nftables/rules.nft\n")
		}, want: []string{"validator-twice: etc/nftables/rules.nft"}},
		{name: "systemd validator missing", mutate: func(t *testing.T, s string) {
			replace(t, s, "share/validators.list", "systemd-analyze verify {release}/etc/systemd/system/ataniq-seat.service\n", "")
		}, want: []string{"validator-missing: etc/systemd/system/ataniq-seat.service"}},
		{name: "systemd validator with options", mutate: func(t *testing.T, s string) {
			replace(t, s, "share/validators.list", "systemd-analyze verify", "systemd-analyze --man=no verify")
		}},
		{name: "systemd validator for two files in one line", mutate: func(t *testing.T, s string) {
			set(t, s, "etc/systemd/system/b.timer", "[Timer]\nOnBootSec=1min\n")
			replace(t, s, "share/validators.list", "ataniq-seat.service\n", "ataniq-seat.service {release}/etc/systemd/system/b.timer\n")
		}},
		{name: "a second unit without a validator", mutate: func(t *testing.T, s string) {
			set(t, s, "etc/systemd/system/b.timer", "[Timer]\nOnBootSec=1min\n")
		}, want: []string{"validator-missing: etc/systemd/system/b.timer"}},
		{name: "a unit runs its own release's binary", mutate: func(t *testing.T, s string) {
			replace(t, s, "etc/systemd/system/ataniq-seat.service", "/opt/authority-host/current/bin/", "{release}/bin/")
		}},
		{name: "a drop-in names its release", mutate: func(t *testing.T, s string) {
			set(t, s, "etc/systemd/system/ataniq-seat.service.d/10-env.conf", "[Service]\nEnvironment=RELEASE={release}\n")
		}},
		{name: "a drop-in needs no validator of its own", mutate: func(t *testing.T, s string) {
			set(t, s, "etc/systemd/system/ataniq-seat.service.d/10-hardening.conf", "[Service]\nNoNewPrivileges=yes\n")
		}},
		{name: "a validator that is not one of the three is allowed", mutate: func(t *testing.T, s string) {
			replace(t, s, "share/validators.list", "caddy", "true\ncaddy")
		}},

		// reload
		{name: "reload lacks caddy", mutate: func(t *testing.T, s string) {
			replace(t, s, "share/reload.list", "systemctl restart caddy\n", "")
		}, want: []string{"reload-missing: caddy"}},
		{name: "reload of caddy by reload", mutate: func(t *testing.T, s string) {
			replace(t, s, "share/reload.list", "systemctl restart caddy", "systemctl reload caddy.service")
		}},
		{name: "reload that only status-es caddy", mutate: func(t *testing.T, s string) {
			replace(t, s, "share/reload.list", "systemctl restart caddy", "systemctl status caddy")
		}, want: []string{"reload-missing: caddy"}},
		{name: "reload lacks daemon-reload", mutate: func(t *testing.T, s string) {
			replace(t, s, "share/reload.list", "systemctl daemon-reload\n", "")
		}, want: []string{"reload-missing: daemon-reload"}},
		{name: "reload only reloads the service", mutate: func(t *testing.T, s string) {
			replace(t, s, "share/reload.list", "systemctl restart ataniq-seat", "systemctl reload ataniq-seat")
		}, want: []string{"reload-missing: ataniq-seat"}},
		{name: "reload restarts another service", mutate: func(t *testing.T, s string) {
			replace(t, s, "share/reload.list", "systemctl restart ataniq-seat", "systemctl restart ataniq-seat-other")
		}, want: []string{"reload-missing: ataniq-seat"}},
		{name: "reload by systemctl with a path and options", mutate: func(t *testing.T, s string) {
			replace(t, s, "share/reload.list", "systemctl restart ataniq-seat", "/usr/bin/systemctl --no-block restart ataniq-seat.service")
		}},
		{name: "reload lacks the firewall", mutate: func(t *testing.T, s string) {
			replace(t, s, "share/reload.list", "nft -f /opt/authority-host/current/etc/nftables/rules.nft\n", "")
		}, want: []string{"reload-missing: nftables"}},
		{name: "reload loads another firewall file", mutate: func(t *testing.T, s string) {
			replace(t, s, "share/reload.list", "etc/nftables/rules.nft", "etc/nftables/other.nft")
		}, want: []string{"reload-missing: nftables"}},
		{name: "reload restarts nftables", mutate: func(t *testing.T, s string) {
			replace(t, s, "share/reload.list", "nft -f /opt/authority-host/current/etc/nftables/rules.nft", "systemctl restart nftables")
		}},
		{name: "a template service is restarted by an instance", mutate: func(t *testing.T, s string) {
			set(t, s, "etc/systemd/system/w@.service", "[Service]\nExecStart=/opt/authority-host/current/bin/ataniq-seat %i\n")
			replace(t, s, "share/validators.list", "caddy", "systemd-analyze verify {release}/etc/systemd/system/w@.service\ncaddy")
			replace(t, s, "share/reload.list", "nft", "systemctl restart w@1.service\nnft")
			replace(t, s, "share/stop.list", "systemctl stop ataniq-seat", "systemctl stop ataniq-seat\nsystemctl stop w@1")
		}},

		// health, stop, static and not static
		{name: "health holds no command", static: true, mutate: func(t *testing.T, s string) {
			set(t, s, "share/health.cmd", "# none\n")
		}, want: []string{"list-empty: share/health.cmd"}},
		{name: "health holds no command, not static", mutate: func(t *testing.T, s string) {
			set(t, s, "share/health.cmd", "")
		}, want: []string{"list-empty: share/health.cmd"}},
		{name: "stop holds no command, not static", mutate: func(t *testing.T, s string) {
			set(t, s, "share/stop.list", "# none\n")
		}, want: []string{"list-empty: share/stop.list", "stop-missing: ataniq-seat"}},
		{name: "stop stops another service", mutate: func(t *testing.T, s string) {
			replace(t, s, "share/stop.list", "ataniq-seat", "caddy")
		}, want: []string{"stop-missing: ataniq-seat"}},
		{name: "stop only restarts the service", mutate: func(t *testing.T, s string) {
			replace(t, s, "share/stop.list", "systemctl stop", "systemctl restart")
		}, want: []string{"stop-missing: ataniq-seat"}},
		{name: "stop of a static release with a command", static: true, mutate: func(t *testing.T, s string) {
			set(t, s, "share/stop.list", "systemctl stop caddy\n")
		}},
		{name: "a release that is not static with no service", mutate: func(t *testing.T, s string) {
			os.Remove(filepath.Join(s, "etc/systemd/system/ataniq-seat.service"))
			replace(t, s, "share/validators.list", "systemd-analyze verify {release}/etc/systemd/system/ataniq-seat.service\n", "")
			replace(t, s, "share/reload.list", "systemctl daemon-reload\nsystemctl restart ataniq-seat\n", "")
			set(t, s, "share/stop.list", "systemctl stop caddy\n")
			os.Remove(filepath.Join(s, "etc/systemd/system"))
			os.Remove(filepath.Join(s, "etc/systemd"))
		}, want: []string{"no-decision-service"}},
		{name: "a static release with a service", static: true, mutate: func(t *testing.T, s string) {
			set(t, s, "etc/systemd/system/ataniq-seat.service", testUnit)
			set(t, s, "share/validators.list", testValidators+"systemd-analyze verify {release}/etc/systemd/system/ataniq-seat.service\n")
			set(t, s, "share/reload.list", testReload+"systemctl daemon-reload\n")
		}, want: []string{"static-has-service: etc/systemd/system/ataniq-seat.service", "decision-exec: etc/systemd/system/ataniq-seat.service"}},
		{name: "a static release with a service in a drop-in", static: true, mutate: func(t *testing.T, s string) {
			set(t, s, "etc/systemd/system/x.service.d/10.conf", "[Service]\nExecStart=\nExecStart=/bin/sh\n")
			set(t, s, "share/reload.list", testReload+"systemctl daemon-reload\n")
		}, want: []string{"static-has-service: etc/systemd/system/x.service.d/10.conf", "decision-exec: etc/systemd/system/x.service.d/10.conf"}},
		{name: "a static release with a timer and a oneshot-less service section", static: true, mutate: func(t *testing.T, s string) {
			set(t, s, "etc/systemd/system/x.timer", "[Timer]\nOnBootSec=1min\n")
			set(t, s, "etc/systemd/system/x.service", "[Service]\nType=oneshot\n")
			set(t, s, "share/validators.list", testValidators+"systemd-analyze verify {release}/etc/systemd/system/x.timer\nsystemd-analyze verify {release}/etc/systemd/system/x.service\n")
			set(t, s, "share/reload.list", testReload+"systemctl daemon-reload\n")
		}},
		{name: "an ExecStart outside [Service] is not a service", static: true, mutate: func(t *testing.T, s string) {
			set(t, s, "etc/systemd/system/x.timer", "[Unit]\nExecStart=/bin/sh\n")
			set(t, s, "share/validators.list", testValidators+"systemd-analyze verify {release}/etc/systemd/system/x.timer\n")
			set(t, s, "share/reload.list", testReload+"systemctl daemon-reload\n")
		}},
		{name: "an ExecStart reset in a drop-in is no service", static: true, mutate: func(t *testing.T, s string) {
			set(t, s, "etc/systemd/system/x.service.d/10.conf", "[Service]\nExecStart=\n")
			set(t, s, "share/reload.list", testReload+"systemctl daemon-reload\n")
		}},

		// decision services
		{name: "decision service runs a system binary", mutate: func(t *testing.T, s string) {
			replace(t, s, "etc/systemd/system/ataniq-seat.service", "/opt/authority-host/current/bin/ataniq-seat", "/usr/bin/sleep")
		}, want: []string{"decision-exec: etc/systemd/system/ataniq-seat.service"}},
		{name: "decision service runs a binary the release lacks", mutate: func(t *testing.T, s string) {
			replace(t, s, "etc/systemd/system/ataniq-seat.service", "bin/ataniq-seat", "bin/absent")
		}, want: []string{"decision-exec: etc/systemd/system/ataniq-seat.service"}},
		{name: "decision service runs a binary through a dot-dot", mutate: func(t *testing.T, s string) {
			replace(t, s, "etc/systemd/system/ataniq-seat.service", "bin/ataniq-seat", "bin/../bin/ataniq-seat")
		}, want: []string{"decision-exec: etc/systemd/system/ataniq-seat.service"}},
		{name: "decision service runs a binary of another release", mutate: func(t *testing.T, s string) {
			replace(t, s, "etc/systemd/system/ataniq-seat.service", "current/bin", "releases/0001-abcdef0/bin")
		}, want: []string{"decision-exec: etc/systemd/system/ataniq-seat.service"}},
		{name: "decision service runs a binary of its own release by id", mutate: func(t *testing.T, s string) {
			replace(t, s, "etc/systemd/system/ataniq-seat.service", "ExecStart=/opt/authority-host/current/bin", "ExecStart=-/opt/authority-host/releases/0007-0123456/bin")
		}},
		{name: "decision service with ExecStartPre from the system", mutate: func(t *testing.T, s string) {
			replace(t, s, "etc/systemd/system/ataniq-seat.service", "Restart=", "ExecStartPre=/usr/bin/true\nRestart=")
		}, want: []string{"decision-exec: etc/systemd/system/ataniq-seat.service"}},
		{name: "a second ExecStart runs a system binary", mutate: func(t *testing.T, s string) {
			replace(t, s, "etc/systemd/system/ataniq-seat.service", "Restart=", "ExecStart=/usr/bin/sleep 1\nRestart=")
		}, want: []string{"decision-exec: etc/systemd/system/ataniq-seat.service"}},
		{name: "an ExecStart continued over two lines", mutate: func(t *testing.T, s string) {
			replace(t, s, "etc/systemd/system/ataniq-seat.service", "ExecStart=/opt", "ExecStart=\\\n  /opt")
		}},
		{name: "a unit gated on the inhibit", mutate: func(t *testing.T, s string) {
			replace(t, s, "etc/systemd/system/ataniq-seat.service", "[Service]", "ConditionPathExists=!/var/lib/authority-host/decisions.inhibit\n\n[Service]")
		}, want: []string{"inhibit-start-condition: etc/systemd/system/ataniq-seat.service"}},
		{name: "a unit that only names the inhibit in a comment", mutate: func(t *testing.T, s string) {
			replace(t, s, "etc/systemd/system/ataniq-seat.service", "[Service]", "# the app refuses decisions while decisions.inhibit is there\n[Service]")
		}},

		// the lists themselves
		{name: "a list with a shell character", mutate: func(t *testing.T, s string) {
			set(t, s, "share/health.cmd", "curl http://127.0.0.1/healthz | cat\n")
		}, want: []string{"list-invalid: share/health.cmd"}},
		{name: "a list with {release} in the arguments of a reload", mutate: func(t *testing.T, s string) {
			set(t, s, "share/reload.list", dynReload+"systemctl restart {release}/x\n")
		}, want: []string{"list-invalid: share/reload.list"}},
		{name: "a list with a relative program", mutate: func(t *testing.T, s string) {
			set(t, s, "share/stop.list", dynStop+"bin/x\n")
		}, want: []string{"list-invalid: share/stop.list"}},
		{name: "a list that is missing", mutate: func(t *testing.T, s string) {
			os.Remove(filepath.Join(s, "share/stop.list"))
		}, want: []string{"list-missing: share/stop.list"}},
		{name: "a program of the release that is not there", mutate: func(t *testing.T, s string) {
			set(t, s, "share/health.cmd", dynHealth+"{release}/bin/absent\n")
		}, want: []string{"program-missing: bin/absent"}},
		{name: "a program of the release without an execute bit", mutate: func(t *testing.T, s string) {
			set(t, s, "share/health.cmd", dynHealth+"{release}/share/health.cmd\n")
		}, want: []string{"program-not-executable: share/health.cmd"}},
		{name: "a program of the release that is a folder", mutate: func(t *testing.T, s string) {
			set(t, s, "share/health.cmd", dynHealth+"{release}/etc\n")
		}, want: []string{"program-missing: etc"}},
		{name: "a program of the release that is there", mutate: func(t *testing.T, s string) {
			set(t, s, "share/health.cmd", dynHealth+"{release}/bin/ataniq-seat --ready\n")
			replace(t, s, "share/stop.list", "systemctl stop ataniq-seat\n", "systemctl stop ataniq-seat\n{release}/bin/ah-noaicheck\n")
		}},
		{name: "an empty source for validators of nothing", static: true, mutate: func(t *testing.T, s string) {
			os.RemoveAll(filepath.Join(s, "etc/caddy"))
			set(t, s, "share/validators.list", "# nothing shipped\n")
			set(t, s, "share/reload.list", "# nothing shipped\n")
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var src string
			var o Options
			if c.static {
				src = staticSrc(t)
				c.mutate(t, src)
				o = testOptions(t, src)
			} else {
				src = dynamicSrc(t)
				c.mutate(t, src)
				o = dynamicOptions(t, src)
			}
			got := codes(Check(built(t, o), noRun))
			want := append([]string(nil), c.want...)
			sort.Strings(want)
			if len(want) == 0 {
				want = nil
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("findings\n got: %q\nwant: %q", got, want)
			}
		})
	}
}

// TestCheckFolder gives a built folder a flaw after the build.
func TestCheckFolder(t *testing.T) {
	type tc struct {
		name   string
		mutate func(t *testing.T, dir string) string // returns the folder to check
		want   []string
	}
	cases := []tc{
		{"a file changed", func(t *testing.T, d string) string {
			writeTestFile(t, filepath.Join(d, "share/health.cmd"), "true\n", 0o644)
			return d
		}, []string{"sums-mismatch: share/health.cmd"}},
		{"a file added", func(t *testing.T, d string) string {
			writeTestFile(t, filepath.Join(d, "share/extra"), "x", 0o644)
			return d
		}, []string{"sums-unlisted: share/extra"}},
		{"a file removed", func(t *testing.T, d string) string {
			os.Remove(filepath.Join(d, "share/ai-names.list"))
			return d
		}, []string{"sums-absent: share/ai-names.list"}},
		{"SHA256SUMS missing", func(t *testing.T, d string) string {
			os.Remove(filepath.Join(d, "SHA256SUMS"))
			return d
		}, []string{"sums-missing"}},
		{"SHA256SUMS garbage", func(t *testing.T, d string) string {
			writeTestFile(t, filepath.Join(d, "SHA256SUMS"), "not sums\n", 0o644)
			return d
		}, []string{"sums-invalid"}},
		{"SHA256SUMS lists itself", func(t *testing.T, d string) string {
			data, _ := os.ReadFile(filepath.Join(d, "SHA256SUMS"))
			writeTestFile(t, filepath.Join(d, "SHA256SUMS"), string(data)+strings.Repeat("0", 64)+"  SHA256SUMS\n", 0o644)
			return d
		}, []string{"sums-invalid"}},
		{"SHA256SUMS out of order", func(t *testing.T, d string) string {
			data, _ := os.ReadFile(filepath.Join(d, "SHA256SUMS"))
			ls := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
			ls[0], ls[1] = ls[1], ls[0]
			writeTestFile(t, filepath.Join(d, "SHA256SUMS"), strings.Join(ls, "\n")+"\n", 0o644)
			return d
		}, []string{"sums-not-canonical"}},
		{"SHA256SUMS in binary mode", func(t *testing.T, d string) string {
			data, _ := os.ReadFile(filepath.Join(d, "SHA256SUMS"))
			writeTestFile(t, filepath.Join(d, "SHA256SUMS"), strings.Replace(string(data), "  ", " *", 1), 0o644)
			return d
		}, []string{"sums-not-canonical"}},
		{"SHA256SUMS without the final newline", func(t *testing.T, d string) string {
			data, _ := os.ReadFile(filepath.Join(d, "SHA256SUMS"))
			writeTestFile(t, filepath.Join(d, "SHA256SUMS"), strings.TrimSuffix(string(data), "\n"), 0o644)
			return d
		}, []string{"sums-not-canonical"}},
		{"a signature beside the sums is allowed", func(t *testing.T, d string) string {
			writeTestFile(t, filepath.Join(d, "SHA256SUMS.sig"), "sig", 0o644)
			return d
		}, nil},
		{"a symbolic link", func(t *testing.T, d string) string {
			os.Symlink("/etc/passwd", filepath.Join(d, "share/link"))
			resum(t, d)
			return d
		}, []string{"not-regular: share/link"}},
		{"a symbolic link in place of a file", func(t *testing.T, d string) string {
			p := filepath.Join(d, "etc/caddy/Caddyfile")
			os.Remove(p)
			os.Symlink("/etc/passwd", p)
			resum(t, d)
			return d
		}, []string{"not-regular: etc/caddy/Caddyfile", "validator-unknown-file: etc/caddy/Caddyfile"}},
		{"a fifo", func(t *testing.T, d string) string {
			if err := syscall.Mkfifo(filepath.Join(d, "share/pipe"), 0o644); err != nil {
				t.Skip(err)
			}
			return d
		}, []string{"not-regular: share/pipe"}},
		{"a hard link", func(t *testing.T, d string) string {
			if err := os.Link(filepath.Join(d, "share/health.cmd"), filepath.Join(d, "share/health.copy")); err != nil {
				t.Skip(err)
			}
			resum(t, d)
			return d
		}, []string{"hard-link: share/health.cmd", "hard-link: share/health.copy"}},
		{"a file writable by others", func(t *testing.T, d string) string {
			os.Chmod(filepath.Join(d, "share/health.cmd"), 0o666)
			return d
		}, []string{"unsafe-mode: share/health.cmd"}},
		{"a file set-user-id", func(t *testing.T, d string) string {
			os.Chmod(filepath.Join(d, "bin/ah-switch"), os.ModeSetuid|0o755)
			return d
		}, []string{"unsafe-mode: bin/ah-switch"}},
		{"a folder writable by the group", func(t *testing.T, d string) string {
			os.Chmod(filepath.Join(d, "share"), 0o775)
			return d
		}, []string{"unsafe-mode: share"}},
		{"a binary without an execute bit", func(t *testing.T, d string) string {
			os.Chmod(filepath.Join(d, "bin/ah-switch"), 0o644)
			return d
		}, []string{"not-executable: bin/ah-switch"}},
		{"a required binary missing", func(t *testing.T, d string) string {
			os.Remove(filepath.Join(d, "bin/ah-switch"))
			resum(t, d)
			return d
		}, []string{"bin-missing: bin/ah-switch"}},
		{"another top-level folder", func(t *testing.T, d string) string {
			writeTestFile(t, filepath.Join(d, "extra/x"), "x", 0o644)
			resum(t, d)
			return d
		}, []string{"top-level: extra"}},
		{"a name with a newline", func(t *testing.T, d string) string {
			writeTestFile(t, filepath.Join(d, "share/a\nb"), "x", 0o644)
			return d
		}, []string{"bad-name"}},
		{"MANIFEST missing", func(t *testing.T, d string) string {
			os.Remove(filepath.Join(d, "MANIFEST"))
			resum(t, d)
			return d
		}, []string{"manifest-invalid"}},
		{"MANIFEST without a key", func(t *testing.T, d string) string {
			data, _ := os.ReadFile(filepath.Join(d, "MANIFEST"))
			writeTestFile(t, filepath.Join(d, "MANIFEST"), strings.Replace(string(data), "built_on: dev-linux-amd64\n", "", 1), 0o644)
			resum(t, d)
			return d
		}, []string{"manifest-invalid"}},
		{"MANIFEST of another release", func(t *testing.T, d string) string {
			data, _ := os.ReadFile(filepath.Join(d, "MANIFEST"))
			writeTestFile(t, filepath.Join(d, "MANIFEST"), strings.Replace(string(data), "id: 0007-0123456", "id: 0008-0123456", 1), 0o644)
			resum(t, d)
			return d
		}, []string{"manifest-mismatch"}},
		{"MANIFEST with another commit", func(t *testing.T, d string) string {
			data, _ := os.ReadFile(filepath.Join(d, "MANIFEST"))
			writeTestFile(t, filepath.Join(d, "MANIFEST"), strings.Replace(string(data), "commit: 0123456", "commit: 7654321", 1), 0o644)
			resum(t, d)
			return d
		}, []string{"manifest-mismatch"}},
		{"MANIFEST with components other than static", func(t *testing.T, d string) string {
			data, _ := os.ReadFile(filepath.Join(d, "MANIFEST"))
			writeTestFile(t, filepath.Join(d, "MANIFEST"), strings.Replace(string(data), "components: static", "components: more", 1), 0o644)
			resum(t, d)
			return d
		}, []string{"manifest-invalid"}},
		{"MANIFEST with a built_on the builder would refuse", func(t *testing.T, d string) string {
			data, _ := os.ReadFile(filepath.Join(d, "MANIFEST"))
			writeTestFile(t, filepath.Join(d, "MANIFEST"), strings.Replace(string(data), "dev-linux-amd64", "a host", 1), 0o644)
			resum(t, d)
			return d
		}, []string{"manifest-invalid"}},
		{"a folder not named as a release", func(t *testing.T, d string) string {
			n := filepath.Join(filepath.Dir(d), "release")
			os.Rename(d, n)
			return n
		}, []string{"folder-invalid"}},
		{"a folder that does not exist", func(t *testing.T, d string) string {
			return filepath.Join(filepath.Dir(d), "0009-aaaaaaa")
		}, []string{"folder-invalid"}},
		{"a symbolic link to the folder", func(t *testing.T, d string) string {
			n := filepath.Join(filepath.Dir(d), "0009-aaaaaaa")
			os.Symlink(d, n)
			return n
		}, []string{"folder-invalid"}},
		{"a trailing slash", func(t *testing.T, d string) string { return d + "/" }, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := built(t, testOptions(t, staticSrc(t)))
			dir = c.mutate(t, dir)
			got := codes(Check(dir, noRun))
			want := append([]string(nil), c.want...)
			sort.Strings(want)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("findings\n got: %q\nwant: %q", got, want)
			}
			for _, f := range Check(dir, noRun) {
				if s := f.String(); strings.ContainsAny(s, "\n\x00") || strings.Contains(s, "passwd") {
					t.Errorf("line %q", s)
				}
			}
		})
	}
}

// TestCheckOrderIsStable: the same folder gives the same findings in the same
// order, whatever the map order of the run.
func TestCheckOrderIsStable(t *testing.T) {
	src := dynamicSrc(t)
	set(t, src, "share/validators.list", "# none\n")
	set(t, src, "share/reload.list", "# none\n")
	dir := built(t, dynamicOptions(t, src))
	first := lines(Check(dir, noRun))
	if first == "" {
		t.Fatal("no findings")
	}
	for i := 0; i < 20; i++ {
		if again := lines(Check(dir, noRun)); again != first {
			t.Fatalf("order changed:\n%s\n%s", first, again)
		}
	}
}

// TestCheckFindsAPlaceholderLeftInAUnit: a folder not made by the builder
// (or changed after it) that still holds {release} in a unit is a finding:
// systemd would take the words as they are.
func TestCheckFindsAPlaceholderLeftInAUnit(t *testing.T) {
	dir := built(t, dynamicOptions(t, dynamicSrc(t)))
	writeTestFile(t, filepath.Join(dir, "etc/systemd/system/ataniq-seat.service"), testUnit+"Environment=HOME={release}\n", 0o644)
	resum(t, dir)
	if got, want := codes(Check(dir, noRun)), []string{"unit-placeholder: etc/systemd/system/ataniq-seat.service"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}
