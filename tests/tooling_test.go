package tests

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func root(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func fakePath(t *testing.T, goScript string) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"git", "make", "sh", "dirname", "cat", "grep", "wc"} {
		path, err := exec.LookPath(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(path, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	if goScript != "" {
		if err := os.WriteFile(filepath.Join(dir, "go"), []byte("#!/bin/sh\n"+goScript), 0755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func fixture(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "scripts"), 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"scripts/go-tool.sh", ".go-version"} {
		b, err := os.ReadFile(filepath.Join(root(t), name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), b, 0644); err != nil {
			t.Fatal(err)
		}
	}
	pin, err := os.ReadFile(filepath.Join(dir, ".go-version"))
	if err != nil {
		t.Fatal(err)
	}
	return dir, strings.TrimSpace(string(pin))
}

func runHelper(t *testing.T, dir, path, action string) (string, error) {
	t.Helper()
	cmd := exec.Command("/bin/sh", filepath.Join(dir, "scripts/go-tool.sh"), action)
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(), "PATH="+path, "CDPATH=/", "GO111MODULE=off", "GOENV=hostile", "GOWORK=hostile", "GOTOOLCHAIN=auto", "GOPROXY=direct", "GOSUMDB=hostile", "GOFLAGS=-mod=mod", "GOPRIVATE=*", "GONOPROXY=*", "GONOSUMDB=*")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestToolchainResolution(t *testing.T) {
	// These expectations are independent of the helper under test.
	guard := `[ "${GO111MODULE-unset}" = on ] && [ "${GOENV-unset}" = off ] &&
[ "${GOWORK-unset}" = off ] && [ "${GOFLAGS-unset}" = '' ] &&
[ "${GOPRIVATE-unset}" = '' ] && [ "${GONOPROXY-unset}" = none ] &&
[ "${GONOSUMDB-unset}" = none ] || exit 9
`
	clean := []string{"GO111MODULE=on", "GOENV=off", "GOWORK=off", "GOFLAGS=", "GOPRIVATE=", "GONOPROXY=none", "GONOSUMDB=none"}
	for i, hostile := range []string{"GO111MODULE=off", "GOENV=hostile", "GOWORK=hostile", "GOFLAGS=-mod=mod", "GOPRIVATE=*", "GONOPROXY=*", "GONOSUMDB=*"} {
		env := append([]string(nil), clean...)
		env[i] = hostile
		cmd := exec.Command("/bin/sh", "-c", guard)
		cmd.Env = env
		if err := cmd.Run(); err == nil {
			t.Fatalf("environment guard accepted %s", hostile)
		}
	}
	cmd := exec.Command("/bin/sh", "-c", guard)
	cmd.Env = clean
	if err := cmd.Run(); err != nil {
		t.Fatalf("environment guard rejected clean environment: %v", err)
	}
	dir, version := fixture(t)
	selectedRoot := filepath.Join(t.TempDir(), "selected toolchain")
	if err := os.MkdirAll(filepath.Join(selectedRoot, "bin"), 0755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "calls")
	selected := "#!/bin/sh\n" + guard + "printf 'selected:%s:%s:%s:%s\\n' \"$1\" \"$GOTOOLCHAIN\" \"$GOPROXY\" \"$GOSUMDB\" >> \"$MARKER\"\ncase \"$1\" in version) printf 'go version go" + version + " linux/amd64\\n';; mod|list|test|build) :;; *) exit 3;; esac\n"
	if err := os.WriteFile(filepath.Join(selectedRoot, "bin", "go"), []byte(selected), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MARKER", marker)
	t.Setenv("SELECTED_ROOT", selectedRoot)
	driver := guard + "printf 'driver:%s:%s\\n' \"$1\" \"$GOTOOLCHAIN\" >> \"$MARKER\"\ncase \"$1:$GOTOOLCHAIN\" in env:go" + version + ") [ \"$2\" = GOROOT ] && [ \"$GOPROXY\" = https://proxy.golang.org,direct ] && [ \"$GOSUMDB\" = sum.golang.org ] && printf '%s\\n' \"$SELECTED_ROOT\";; version:local) printf 'go version go1.22.0 linux/amd64\\n';; *) exit 4;; esac\n"
	path := fakePath(t, driver)
	for _, name := range []string{"mkdir", "mktemp", "mv", "rm"} {
		p, err := exec.LookPath(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(p, filepath.Join(path, name)); err != nil {
			t.Fatal(err)
		}
	}
	if out, err := runHelper(t, dir, path, "doctor"); err == nil || !strings.Contains(out, "run make bootstrap") {
		t.Fatalf("missing state: %s %v", out, err)
	}
	if out, err := runHelper(t, dir, path, "bootstrap"); err != nil {
		t.Fatalf("bootstrap: %s %v", out, err)
	}
	for _, action := range []string{"doctor", "test", "build"} {
		if out, err := runHelper(t, dir, path, action); err != nil {
			t.Fatalf("%s: %s %v", action, out, err)
		}
	}
	calls, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"driver:env:go" + version, "driver:version:local", "selected:mod:local:https://proxy.golang.org,direct:sum.golang.org", "selected:test:local:off:off", "selected:build:local:off:off"} {
		if !strings.Contains(string(calls), want) {
			t.Fatalf("missing %q in %s", want, calls)
		}
	}
	if strings.Contains(string(calls), "driver:version:go") || strings.Contains(string(calls), "driver:test:") {
		t.Fatalf("automatic selection in offline path: %s", calls)
	}
	record := filepath.Join(dir, "build/go-toolchain")
	good, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"stale\n" + filepath.Join(selectedRoot, "bin/go") + "\n", string(good) + "extra\n", version + "\n/does/not/exist/bin/go\n", version + "\nrelative/bin/go\n"} {
		if err := os.WriteFile(record, []byte(bad), 0644); err != nil {
			t.Fatal(err)
		}
		for _, action := range []string{"doctor", "test", "build"} {
			if out, err := runHelper(t, dir, path, action); err == nil || !strings.Contains(out, "run make bootstrap") {
				t.Fatalf("bad record %q %s: %s %v", bad, action, out, err)
			}
		}
	}
	if err := os.WriteFile(record, good, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".go-version"), []byte("1.99.0\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if out, err := runHelper(t, dir, path, "doctor"); err == nil || !strings.Contains(out, "run make bootstrap") {
		t.Fatalf("changed pin: %s %v", out, err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".go-version"), []byte(version+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(selectedRoot, "bin", "go"), []byte("#!/bin/sh\nprintf 'go version go1.2.0 linux/amd64\\n'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if out, err := runHelper(t, dir, path, "doctor"); err == nil || !strings.Contains(out, "run make bootstrap") {
		t.Fatalf("mismatched executable: %s %v", out, err)
	}
	if out, err := runHelper(t, dir, path, "bootstrap"); err == nil || !strings.Contains(out, "resolved Go version mismatched") {
		t.Fatalf("mismatched bootstrap: %s %v", out, err)
	}
	exact := fakePath(t, guard+"[ \"$GOTOOLCHAIN\" = local ] && [ \"$GOPROXY\" = off ] && [ \"$GOSUMDB\" = off ] || exit 8\nprintf 'go version go"+version+" linux/amd64\\n'\n")
	if out, err := runHelper(t, dir, exact, "doctor"); err != nil {
		t.Fatalf("exact driver: %s %v", out, err)
	}
	if out, err := runHelper(t, dir, fakePath(t, ""), "doctor"); err == nil || !strings.Contains(out, "missing: go") {
		t.Fatalf("missing driver: %s %v", out, err)
	}
}

// Doctor must run both module checks offline and report each failure with the
// diagnostic of the failing Go command.
func TestDoctorModuleChecks(t *testing.T) {
	dir, pin := fixture(t)
	for _, tc := range []struct{ failing, want string }{
		{"none", "ok: module dependencies available and unmodified offline"},
		{"list", "fail: packages or module dependencies do not load offline (diagnostic above); for a missing module run make bootstrap, for a checksum mismatch restore go.sum or remove the named module from the module cache"},
		{"mod", "fail: cached module dependencies modified"},
	} {
		path := fakePath(t, `[ "$GOTOOLCHAIN" = local ] && [ "$GOPROXY" = off ] && [ "$GOSUMDB" = off ] || exit 8
[ "$1" != list ] || [ "$*" = "list -deps -test ./..." ] || exit 11
[ "$1" != mod ] || [ "$*" = "mod verify" ] || exit 12
case "$1" in
version) printf 'go version go`+pin+` linux/amd64\n';;
`+tc.failing+`) printf 'go: downloading example.test/mod v1.0.0\ngo: example.test/mod@v1.0.0: diagnostic\n' >&2; exit 1;;
list|mod) :;;
*) exit 10;;
esac
`)
		out, err := runHelper(t, dir, path, "doctor")
		failed := tc.failing != "none"
		if (err != nil) != failed || !strings.Contains(out, tc.want) || strings.Contains(out, "\n  go: example.test/mod@v1.0.0: diagnostic\n") != failed || strings.Contains(out, "go: downloading") {
			t.Fatalf("%s: %s %v", tc.failing, out, err)
		}
	}
}

func TestBootstrapPreview(t *testing.T) {
	dir := fakePath(t, "exit 99\n")
	cmd := exec.Command(filepath.Join(dir, "make"), "-n", "bootstrap")
	cmd.Dir = root(t)
	cmd.Env = append(os.Environ(), "PATH="+dir)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "sh scripts/go-tool.sh bootstrap") {
		t.Fatalf("preview %s; error %v", out, err)
	}
}

// Invalid pins must fail before the driver can execute, even through make.
func TestInvalidPin(t *testing.T) {
	for _, pin := range []string{"1.27.2; printf x > injected", "1.27.2\n1.27.3", "1.027.2", "1.27", "1.27.02", "1.20.1", "1.27.2$(printf x > injected)"} {
		t.Run(pin, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Mkdir(filepath.Join(dir, "scripts"), 0755); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"Makefile", "scripts/go-tool.sh"} {
				b, err := os.ReadFile(filepath.Join(root(t), name))
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, name), b, 0644); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(dir, ".go-version"), []byte(pin+"\n"), 0644); err != nil {
				t.Fatal(err)
			}
			driver := fakePath(t, "printf x > invoked\nexit 0\n")
			positive := exec.Command(filepath.Join(driver, "go"))
			positive.Dir = dir
			if out, err := positive.CombinedOutput(); err != nil {
				t.Fatalf("positive driver: %s: %v", out, err)
			}
			if err := os.Remove(filepath.Join(dir, "invoked")); err != nil {
				t.Fatalf("positive driver did not create marker: %v", err)
			}
			for _, action := range []string{"bootstrap", "doctor", "test-dev"} {
				cmd := exec.Command(filepath.Join(driver, "make"), action)
				cmd.Dir = dir
				cmd.Env = append(os.Environ(), "PATH="+driver)
				out, err := cmd.CombinedOutput()
				if err == nil || !strings.Contains(string(out), "error: invalid Go toolchain pin") {
					t.Fatalf("%s: %s; %v", action, out, err)
				}
			}
			for _, name := range []string{"invoked", "injected"} {
				if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
					t.Fatalf("marker %s: expected not-exist, got %v", name, err)
				}
			}
		})
	}
}

func TestMissingBootstrapDriver(t *testing.T) {
	dir, _ := fixture(t)
	if out, err := runHelper(t, dir, fakePath(t, ""), "bootstrap"); err == nil || !strings.Contains(out, "missing: Go bootstrap driver") {
		t.Fatalf("%s: %v", out, err)
	}
}

func TestBuildForwardsArgumentsWithOfflineSelection(t *testing.T) {
	dir, pin := fixture(t)
	path := fakePath(t, `[ "$GOTOOLCHAIN" = local ] && [ "$GOPROXY" = off ] && [ "$GOSUMDB" = off ] || exit 8
case "$1" in
version) printf 'go version go`+pin+` linux/amd64\n';;
build) shift; [ "$#" = 4 ] && [ "$1" = -trimpath ] && [ "$2" = -o ] && [ "$3" = 'some output' ] && [ "$4" = ./qualification/host/cmd/probe ] || exit 9;;
*) exit 10;;
esac
`)
	cmd := exec.Command("/bin/sh", filepath.Join(dir, "scripts/go-tool.sh"), "build", "-trimpath", "-o", "some output", "./qualification/host/cmd/probe")
	cmd.Env = append(os.Environ(), "PATH="+path, "GOTOOLCHAIN=auto", "GOPROXY=direct", "GOSUMDB=hostile")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %s %v", out, err)
	}
}
