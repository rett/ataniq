//go:build linux && ataniq_canary

package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"ataniq/internal/leadcanary/provision"
	"ataniq/internal/leadcanary/provision/provisiontest"
)

// asBinary makes the test binary run main when it is started with this
// variable, so that the tests run the command itself and need no build.
const asBinary = "ATANIQ_CANARY_TEST_AS_BINARY"

func TestMain(m *testing.M) {
	if os.Getenv(asBinary) == "1" {
		main()
		return
	}
	os.Exit(m.Run())
}

// do runs the command in process and returns its status and both outputs.
func do(t *testing.T, w *provisiontest.World, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := run(context.Background(), args, &out, &errOut, w.Getenv())
	return code, out.String(), errOut.String()
}

func TestVersionNamesTheCommandAndTheTag(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run(context.Background(), []string{"--version"}, &out, &errOut, os.Getenv); code != 0 {
		t.Fatalf("status %d", code)
	}
	if got, want := out.String(), "ataniq-canary (build tag ataniq_canary)\n"; got != want || errOut.Len() != 0 {
		t.Fatalf("--version printed %q (and %q), want %q", got, errOut.String(), want)
	}
}

func TestTheBinaryItselfPrintsItsVersion(t *testing.T) {
	cmd := exec.Command(os.Args[0], "--version")
	cmd.Env = []string{asBinary + "=1"}
	out, err := cmd.Output()
	if err != nil || string(out) != "ataniq-canary (build tag ataniq_canary)\n" {
		t.Fatalf("%q, %v", out, err)
	}
}

func TestUsageErrors(t *testing.T) {
	w := provisiontest.New(t)
	for name, args := range map[string][]string{
		"no arguments":                   {},
		"an unknown subcommand":          {"start"},
		"provision without a config":     {"provision"},
		"provision with an empty path":   {"provision", "--config", ""},
		"replay-on without a config":     {"replay-on"},
		"a further argument":             {"provision", "--config", w.ConfigPath, "extra"},
		"an unknown flag":                {"provision", "--config", w.ConfigPath, "--force"},
		"version with another argument":  {"--version", "provision"},
		"the flag before the subcommand": {"--config", w.ConfigPath, "provision"},
	} {
		t.Run(name, func(t *testing.T) {
			code, out, errOut := do(t, w, args...)
			if code != exitUsage || out != "" || !strings.Contains(errOut, "usage:") {
				t.Fatalf("status %d, out %q, err %q", code, out, errOut)
			}
		})
	}
	// Nothing was made by any of them.
	if _, err := os.Lstat(filepath.Join(w.State, "leads")); err == nil {
		t.Fatal("a usage error made something")
	}
}

func TestProvisionPrintsWhatItMadeAndNoFileContent(t *testing.T) {
	w := provisiontest.New(t)
	code, out, errOut := do(t, w, "provision", "--config", w.ConfigPath)
	if code != 0 || errOut != "" {
		t.Fatalf("status %d: %s", code, errOut)
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) < 30 {
		t.Fatalf("only %d lines:\n%s", len(lines), out)
	}
	if want := "made file " + filepath.Join(w.State, "canary-root.json"); lines[len(lines)-1] != want {
		t.Errorf("last line %q, want %q", lines[len(lines)-1], want)
	}
	if strings.Contains(out, provisiontest.Secret) || strings.Contains(errOut, provisiontest.Secret) {
		t.Error("the output holds the content of the managed settings")
	}
	for _, l := range lines {
		if !strings.HasPrefix(l, "made ") && !strings.HasPrefix(l, "kept ") && !strings.HasPrefix(l, "cloned ") && !strings.HasPrefix(l, "checked ") {
			t.Errorf("a line that does not say what it made: %q", l)
		}
	}
	// A second run is refused, prints nothing to stdout and changes nothing.
	code, out, errOut = do(t, w, "provision", "--config", w.ConfigPath)
	if code != exitFailed || out != "" || !strings.Contains(errOut, "started already") {
		t.Fatalf("second run: status %d, out %q, err %q", code, out, errOut)
	}
}

func TestReplayOnIsTheOnlySubcommandAfterAFirstStart(t *testing.T) {
	w := provisiontest.New(t)
	// Before provisioning there is no marker.
	if code, out, errOut := do(t, w, "replay-on", "--config", w.ConfigPath); code != exitFailed || out != "" || !strings.Contains(errOut, "no-marker") {
		t.Fatalf("before provisioning: status %d, out %q, err %q", code, out, errOut)
	}
	if code, _, errOut := do(t, w, "provision", "--config", w.ConfigPath); code != 0 {
		t.Fatalf("provision: %s", errOut)
	}
	p, _ := provision.PathsOf(w.Cfg)
	// A first start has happened.
	provisiontest.WriteFile(t, filepath.Join(p.Holder, "lead-has-run"), nil, 0o600)
	if code, _, _ := do(t, w, "provision", "--config", w.ConfigPath); code != exitFailed {
		t.Fatalf("provision after a first start: status %d", code)
	}
	code, out, errOut := do(t, w, "replay-on", "--config", w.ConfigPath)
	if code != 0 || errOut != "" || out != "made file "+p.ReplaySwitch+"\n" {
		t.Fatalf("replay-on: status %d, out %q, err %q", code, out, errOut)
	}
	if code, _, _ := do(t, w, "replay-on", "--config", w.ConfigPath); code != exitFailed {
		t.Fatalf("a second replay-on: status %d", code)
	}
}

func TestAConfigThatReadConfigRefusesStopsTheCommand(t *testing.T) {
	w := provisiontest.New(t)
	if err := os.Chmod(w.ConfigPath, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, sub := range []string{"provision", "replay-on"} {
		code, out, errOut := do(t, w, sub, "--config", w.ConfigPath)
		if code != exitFailed || out != "" || !strings.Contains(errOut, "0600") {
			t.Errorf("%s: status %d, out %q, err %q", sub, code, out, errOut)
		}
	}
	if _, err := os.Lstat(filepath.Join(w.State, "leads")); err == nil {
		t.Fatal("something was made")
	}
}

func TestTheDefaultRootsAreRefusedByTheCommand(t *testing.T) {
	w := provisiontest.New(t)
	env := w.Getenv()
	w.Cfg.StateRoot = filepath.Join(env("XDG_STATE_HOME"), "ataniq")
	w.Cfg.Provision.Mirror = filepath.Join(w.Cfg.StateRoot, "mirrors", "throwaway-one.git")
	w.WriteConfig(t)
	code, out, errOut := do(t, w, "provision", "--config", w.ConfigPath)
	if code != exitFailed || out != "" || !strings.Contains(errOut, "default-root") {
		t.Fatalf("status %d, out %q, err %q", code, out, errOut)
	}
	if _, err := os.Lstat(env("XDG_STATE_HOME")); err == nil {
		t.Fatal("the default folder was made")
	}
}

func TestTheBinaryProvisionsInAChildProcess(t *testing.T) {
	w := provisiontest.New(t)
	cmd := exec.Command(os.Args[0], "provision", "--config", w.ConfigPath)
	cmd.Env = []string{asBinary + "=1", "PATH=/usr/bin:/bin"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.HasSuffix(string(out), "made file "+filepath.Join(w.State, "canary-root.json")+"\n") {
		t.Fatalf("output:\n%s", out)
	}
	if strings.Contains(string(out), provisiontest.Secret) {
		t.Fatal("the output holds file content")
	}
}

func TestEveryFileOfTheCommandCarriesTheBuildTag(t *testing.T) {
	const want = "//go:build linux && ataniq_canary"
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("%v %v", files, err)
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if first, _, _ := strings.Cut(string(data), "\n"); first != want {
			t.Errorf("%s starts with %q", f, first)
		}
	}
}
