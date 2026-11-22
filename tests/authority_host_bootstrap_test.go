package tests

// This file checks the authority host's bootstrap script and, in the same
// test, its deploy driver (authority_host_deploy_test.go). Nothing here
// touches a real host: the bootstrap runs against a scratch folder through
// ROOT_PREFIX (every absolute path of the script lies below it), the deploy
// driver gets a PATH whose ssh, rsync and ah-release record being called and
// do nothing else, and no test needs root, a network or a service.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const (
	ahBootstrapScript = "deploy/authority-host/bootstrap/host-bootstrap.sh"
	ahDeployScript    = "deploy/authority-host/deploy.sh"
)

func TestAuthorityHostDeployScripts(t *testing.T) {
	t.Run("bootstrap", testAuthorityHostBootstrap)
	t.Run("deploy", testAuthorityHostDeploy)
	t.Run("manual", testAuthorityHostDeployDoc)
}

// ahRun runs a script with sh in dir, with exactly the environment given,
// and returns its combined output and exit status.
func ahRun(t *testing.T, script, dir string, env []string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command("/bin/sh", append([]string{filepath.Join(root(t), script)}, args...)...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdin = strings.NewReader("")
	out, err := cmd.CombinedOutput()
	if err != nil {
		exit, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("%s did not run: %v", script, err)
		}
		return string(out), exit.ExitCode()
	}
	return string(out), 0
}

// ahSnapshot describes every path below dir: kind, mode and size, with the
// content hash of a regular file and the target of a link.
func ahSnapshot(t *testing.T, dir string) []string {
	t.Helper()
	var rows []string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		row := fmt.Sprintf("%s %s", p, info.Mode())
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			row += " -> " + target
		case info.Mode().IsRegular():
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(data)
			row += fmt.Sprintf(" %d %s", len(data), hex.EncodeToString(sum[:]))
		}
		rows = append(rows, row)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// ahStubs makes a folder of recording stubs for the named programs. A stub
// appends its name and arguments (each in brackets) to the returned calls file
// and exits 0, or 1 when the environment variable STUB_FAIL names it.
func ahStubs(t *testing.T, names ...string) (bin, calls string) {
	t.Helper()
	dir := t.TempDir()
	bin = filepath.Join(dir, "stubs")
	calls = filepath.Join(dir, "calls")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		body := "#!/bin/sh\n" +
			"printf '%s' '" + name + "' >>'" + calls + "'\n" +
			"for a in \"$@\"; do printf ' [%s]' \"$a\" >>'" + calls + "'; done\n" +
			"printf '\\n' >>'" + calls + "'\n" +
			"[ \"${STUB_FAIL-}\" != '" + name + "' ] || exit 1\n" +
			"exit 0\n"
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return bin, calls
}

// ahCalls returns the lines the stubs recorded (none when nothing was called).
func ahCalls(t *testing.T, calls string) []string {
	t.Helper()
	data, err := os.ReadFile(calls)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
}

// ahFakeRoot makes the folders of a host below a new scratch folder (mode
// 0755, as ls shows them to the script) and returns the scratch root.
func ahFakeRoot(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "root")
	for _, d := range []string{"", "opt", "etc", "etc/caddy", "var", "var/lib", "usr", "usr/local", "usr/local/sbin", "srv", "srv/authority", "srv/authority/public"} {
		p := filepath.Join(dir, d)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "srv/authority/public/index.html"), []byte("<p>status</p>\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func testAuthorityHostBootstrap(t *testing.T) {
	// A fixed PATH of the real tools, with recording stubs of the programs a
	// bootstrap must never run in front of it.
	stubBin, stubCalls := ahStubs(t, "systemctl", "service", "caddy", "ssh", "rsync", "curl")
	realPath := stubBin + ":/usr/bin:/bin"

	switchBin := filepath.Join(t.TempDir(), "ah-switch")
	switchData := []byte("#!/bin/sh\necho a static switch stands here\n")
	if err := os.WriteFile(switchBin, switchData, 0o755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(switchData)
	switchSum := hex.EncodeToString(sum[:])
	wrongSum := strings.Repeat("0", 64)

	env := func(rootDir string) []string {
		return []string{"PATH=" + realPath, "HOME=" + filepath.Join(rootDir, "home"), "ROOT_PREFIX=" + rootDir}
	}
	run := func(rootDir string, args ...string) (string, int) {
		t.Helper()
		out, code := ahRun(t, ahBootstrapScript, rootDir, env(rootDir), args...)
		if calls := ahCalls(t, stubCalls); calls != nil {
			t.Fatalf("the bootstrap started a program: %v", calls)
		}
		return out, code
	}
	good := []string{"--switch-bin", switchBin, "--switch-sha256", switchSum}
	with := func(extra ...string) []string { return append(slices.Clone(good), extra...) }

	t.Run("self-test", func(t *testing.T) {
		out, code := ahRun(t, ahBootstrapScript, t.TempDir(), []string{"PATH=" + realPath}, "--self-test")
		if code != 0 || !strings.Contains(out, "nothing was run or changed") || strings.Contains(out, "FAILED") {
			t.Fatalf("self-test: exit %d\n%s", code, out)
		}
	})

	t.Run("dry changes nothing", func(t *testing.T) {
		rootDir := ahFakeRoot(t)
		before := ahSnapshot(t, rootDir)
		for _, args := range [][]string{with(), with("--dry"), with("--dry", "--link-caddy")} {
			out, code := run(rootDir, args...)
			if code != 0 {
				t.Fatalf("%v: exit %d\n%s", args, code, out)
			}
			for _, want := range []string{
				"mode dry", "would: create", "/opt/authority-host/releases", "/etc/authority-host",
				"/var/lib/authority-host", "stop.list", "/usr/bin/true", "ah-switch", switchSum,
				"switch-policy.json is not written", "manual mode", "Signed mode", "nothing was changed",
			} {
				if !strings.Contains(out, want) {
					t.Errorf("%v: the output lacks %q:\n%s", args, want, out)
				}
			}
			if strings.Contains(out, "\ndo: ") {
				t.Errorf("%v: a dry run did something:\n%s", args, out)
			}
			if after := ahSnapshot(t, rootDir); !slices.Equal(before, after) {
				t.Fatalf("%v: the dry run changed the folder:\nbefore %v\nafter  %v", args, before, after)
			}
		}
	})

	t.Run("apply makes the layout and is idempotent", func(t *testing.T) {
		rootDir := ahFakeRoot(t)
		public := ahSnapshot(t, filepath.Join(rootDir, "srv"))
		out, code := run(rootDir, with("--apply")...)
		if code != 0 {
			t.Fatalf("apply: exit %d\n%s", code, out)
		}
		for _, d := range []string{"opt/authority-host", "opt/authority-host/releases", "etc/authority-host", "var/lib/authority-host"} {
			info, err := os.Lstat(filepath.Join(rootDir, d))
			if err != nil || !info.IsDir() || info.Mode().Perm() != 0o755 {
				t.Errorf("%s: %v %v", d, info, err)
			}
		}
		list, err := os.ReadFile(filepath.Join(rootDir, "etc/authority-host/stop.list"))
		if err != nil {
			t.Fatal(err)
		}
		var commands []string
		for _, line := range strings.Split(string(list), "\n") {
			if line != "" && !strings.HasPrefix(line, "#") {
				commands = append(commands, line)
			}
		}
		if !slices.Equal(commands, []string{"/usr/bin/true"}) || !strings.HasPrefix(string(list), "#") {
			t.Errorf("stop.list: %q", list)
		}
		if info, err := os.Stat(filepath.Join(rootDir, "etc/authority-host/stop.list")); err != nil || info.Mode().Perm() != 0o644 {
			t.Errorf("stop.list mode: %v %v", info, err)
		}
		installed := filepath.Join(rootDir, "usr/local/sbin/ah-switch")
		if data, err := os.ReadFile(installed); err != nil || string(data) != string(switchData) {
			t.Errorf("ah-switch: %q %v", data, err)
		}
		if info, err := os.Stat(installed); err != nil || info.Mode().Perm() != 0o755 {
			t.Errorf("ah-switch mode: %v %v", info, err)
		}
		if _, err := os.Lstat(filepath.Join(rootDir, "etc/authority-host/switch-policy.json")); !os.IsNotExist(err) {
			t.Errorf("switch-policy.json was written: %v", err)
		}
		if _, err := os.Lstat(filepath.Join(rootDir, "etc/caddy/Caddyfile")); !os.IsNotExist(err) {
			t.Errorf("the Caddyfile was touched without --link-caddy: %v", err)
		}
		if got := ahSnapshot(t, filepath.Join(rootDir, "srv")); !slices.Equal(public, got) {
			t.Errorf("/srv was touched:\n%v\n%v", public, got)
		}
		entries, _ := os.ReadDir(filepath.Join(rootDir, "usr/local/sbin"))
		if len(entries) != 1 {
			t.Errorf("a temporary file was left in sbin: %v", entries)
		}

		again := ahSnapshot(t, rootDir)
		out, code = run(rootDir, with("--apply")...)
		if code != 0 || !strings.Contains(out, "already installed") || strings.Contains(out, "do: ") {
			t.Fatalf("second apply: exit %d\n%s", code, out)
		}
		if got := ahSnapshot(t, rootDir); !slices.Equal(again, got) {
			t.Fatalf("a second apply changed the folder:\n%v\n%v", again, got)
		}
	})

	t.Run("refuses a switch hash mismatch", func(t *testing.T) {
		for _, mode := range []string{"--dry", "--apply"} {
			rootDir := ahFakeRoot(t)
			before := ahSnapshot(t, rootDir)
			out, code := run(rootDir, "--switch-bin", switchBin, "--switch-sha256", wrongSum, mode)
			if code != 1 || !strings.Contains(out, "refused: sha256") || !strings.Contains(out, switchSum) {
				t.Errorf("%s: exit %d\n%s", mode, code, out)
			}
			if after := ahSnapshot(t, rootDir); !slices.Equal(before, after) {
				t.Errorf("%s: a refused run changed the folder:\n%v\n%v", mode, before, after)
			}
		}
	})

	t.Run("requires the switch binary and its hash", func(t *testing.T) {
		rootDir := ahFakeRoot(t)
		before := ahSnapshot(t, rootDir)
		for _, args := range [][]string{
			{"--apply"},
			{"--apply", "--switch-bin", switchBin},
			{"--apply", "--switch-sha256", switchSum},
			{"--apply", "--switch-bin", switchBin, "--switch-sha256", "abc"},
			{"--apply", "--switch-bin", filepath.Join(rootDir, "missing"), "--switch-sha256", switchSum},
			{"--apply", "--dry", "--switch-bin", switchBin, "--switch-sha256", switchSum},
		} {
			if out, code := run(rootDir, args...); code == 0 {
				t.Errorf("%v: accepted\n%s", args, out)
			}
		}
		if after := ahSnapshot(t, rootDir); !slices.Equal(before, after) {
			t.Errorf("a refused run changed the folder")
		}
	})

	t.Run("refuses an unsafe parent folder", func(t *testing.T) {
		cases := []struct {
			name   string
			break_ func(t *testing.T, rootDir string)
			want   string
		}{
			{"group-writable /opt", func(t *testing.T, r string) { ahChmod(t, filepath.Join(r, "opt"), 0o775) }, "writable by its group or by others"},
			{"world-writable /etc", func(t *testing.T, r string) { ahChmod(t, filepath.Join(r, "etc"), 0o757) }, "writable by its group or by others"},
			{"group-writable /var/lib", func(t *testing.T, r string) { ahChmod(t, filepath.Join(r, "var/lib"), 0o775) }, "writable by its group or by others"},
			{"group-writable root", func(t *testing.T, r string) { ahChmod(t, r, 0o775) }, "writable by its group or by others"},
			{"group-writable sbin", func(t *testing.T, r string) { ahChmod(t, filepath.Join(r, "usr/local/sbin"), 0o775) }, "writable by its group or by others"},
			{"linked /opt", func(t *testing.T, r string) {
				if err := os.Rename(filepath.Join(r, "opt"), filepath.Join(r, "opt-real")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("opt-real", filepath.Join(r, "opt")); err != nil {
					t.Fatal(err)
				}
			}, "is a symbolic link"},
		}
		for _, c := range cases {
			for _, mode := range []string{"--dry", "--apply"} {
				rootDir := ahFakeRoot(t)
				c.break_(t, rootDir)
				before := ahSnapshot(t, rootDir)
				out, code := run(rootDir, with(mode)...)
				if code != 1 || !strings.Contains(out, "refused:") || !strings.Contains(out, c.want) {
					t.Errorf("%s %s: exit %d\n%s", c.name, mode, code, out)
				}
				if after := ahSnapshot(t, rootDir); !slices.Equal(before, after) {
					t.Errorf("%s %s: a refused run changed the folder:\n%v\n%v", c.name, mode, before, after)
				}
			}
		}
	})

	t.Run("keeps the owner's stop list", func(t *testing.T) {
		rootDir := ahFakeRoot(t)
		dir := filepath.Join(rootDir, "etc/authority-host")
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		ahChmod(t, dir, 0o755)
		own := filepath.Join(dir, "stop.list")
		if err := os.WriteFile(own, []byte("/usr/bin/systemctl stop seat\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if out, code := run(rootDir, with("--apply")...); code != 0 || !strings.Contains(out, "kept:") {
			t.Fatalf("exit %d\n%s", code, out)
		}
		if data, _ := os.ReadFile(own); string(data) != "/usr/bin/systemctl stop seat\n" {
			t.Errorf("the owner's stop list was changed: %q", data)
		}
	})

	t.Run("caddy is linked only on request, with one backup", func(t *testing.T) {
		const target = "/opt/authority-host/current/etc/caddy/Caddyfile"
		rootDir := ahFakeRoot(t)
		caddyfile := filepath.Join(rootDir, "etc/caddy/Caddyfile")
		backup := caddyfile + ".pre-authority-host"
		if err := os.WriteFile(caddyfile, []byte("old config\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if out, code := run(rootDir, with("--apply")...); code != 0 {
			t.Fatalf("exit %d\n%s", code, out)
		}
		if data, _ := os.ReadFile(caddyfile); string(data) != "old config\n" {
			t.Fatalf("the Caddyfile changed without --link-caddy: %q", data)
		}
		out, code := run(rootDir, with("--apply", "--link-caddy")...)
		if code != 0 || !strings.Contains(out, "does not exist until the first ah-switch") || !strings.Contains(out, "never restarts") {
			t.Fatalf("exit %d\n%s", code, out)
		}
		if got, err := os.Readlink(caddyfile); err != nil || got != target {
			t.Fatalf("Caddyfile link: %q %v", got, err)
		}
		if data, err := os.ReadFile(backup); err != nil || string(data) != "old config\n" {
			t.Fatalf("backup: %q %v", data, err)
		}
		again := ahSnapshot(t, rootDir)
		if out, code := run(rootDir, with("--apply", "--link-caddy")...); code != 0 || !strings.Contains(out, "already points at") {
			t.Fatalf("second run: exit %d\n%s", code, out)
		}
		if got := ahSnapshot(t, rootDir); !slices.Equal(again, got) {
			t.Fatalf("a second run changed the folder")
		}

		// An existing backup is never overwritten: a regular Caddyfile that would
		// need it is refused, and nothing changes.
		other := ahFakeRoot(t)
		file := filepath.Join(other, "etc/caddy/Caddyfile")
		if err := os.WriteFile(file, []byte("config two\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file+".pre-authority-host", []byte("first backup\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		before := ahSnapshot(t, other)
		out, code = run(other, with("--apply", "--link-caddy")...)
		if code != 1 || !strings.Contains(out, "never overwritten") {
			t.Fatalf("exit %d\n%s", code, out)
		}
		if got := ahSnapshot(t, other); !slices.Equal(before, got) {
			t.Fatalf("a refused run changed the folder")
		}

		// A link to somewhere else is not touched either.
		third := ahFakeRoot(t)
		if err := os.Symlink("/somewhere/else", filepath.Join(third, "etc/caddy/Caddyfile")); err != nil {
			t.Fatal(err)
		}
		before = ahSnapshot(t, third)
		if out, code := run(third, with("--apply", "--link-caddy")...); code != 1 || !strings.Contains(out, "is a link to") {
			t.Fatalf("exit %d\n%s", code, out)
		}
		if got := ahSnapshot(t, third); !slices.Equal(before, got) {
			t.Fatalf("a refused run changed the folder")
		}
	})
}

func ahChmod(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}
