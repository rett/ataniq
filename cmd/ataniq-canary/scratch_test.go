//go:build linux && ataniq_canary

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ataniq/internal/leadcanary/provision"
	"ataniq/internal/leadcanary/provision/provisiontest"
	"ataniq/internal/leadholder/canaryroot"
)

// doScratch runs the command in process on a scratch world.
func doScratch(t *testing.T, w *provisiontest.ScratchWorld, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := run(context.Background(), args, &out, &errOut, w.Getenv())
	return code, out.String(), errOut.String()
}

// freshPlace removes the state root's marker, as the world of a place that the
// owner has not initialised yet.
func freshPlace(t *testing.T, w *provisiontest.ScratchWorld) {
	t.Helper()
	if err := os.Remove(filepath.Join(w.State, canaryroot.ScratchMarkerName)); err != nil {
		t.Fatal(err)
	}
}

func TestScratchInitMakesThePlaceAndItsMarker(t *testing.T) {
	w := provisiontest.NewScratch(t)
	if err := os.RemoveAll(w.State); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := doScratch(t, w, "scratch-init", "--config", w.ConfigPath)
	if code != exitOK || errOut != "" {
		t.Fatalf("status %d, err %q", code, errOut)
	}
	marker := filepath.Join(w.State, canaryroot.ScratchMarkerName)
	for _, want := range []string{"made folder " + w.State, "made file " + marker} {
		if !strings.Contains(out, want) {
			t.Errorf("output %q lacks %q", out, want)
		}
	}
	fi, err := os.Lstat(w.State)
	if err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
		t.Fatalf("the state root: %v, %v", fi, err)
	}
	fi, err = os.Lstat(marker)
	if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 {
		t.Fatalf("the marker: %v, %v", fi, err)
	}
	got, _ := os.ReadFile(marker)
	if string(got) != `{"marker":"ataniq-scratch-root","version":1}`+"\n" {
		t.Errorf("the marker holds %q", got)
	}
	// The place gate passes for every lead of the config.
	for _, c := range w.Leads(t) {
		if err := canaryroot.Check(w.State, w.Runtime, c.Role.Project(), w.Getenv()); err != nil {
			t.Errorf("%v: %v", c.Role, err)
		}
	}
	// It made nothing else: no lead, mirror or runtime root.
	names, _ := os.ReadDir(w.State)
	if len(names) != 1 || names[0].Name() != canaryroot.ScratchMarkerName {
		t.Errorf("the state root holds %v", names)
	}
	if _, err := os.Lstat(w.Runtime); err == nil {
		t.Error("the runtime root was made")
	}
}

func TestScratchInitKeepsAPlaceThatIsInitialised(t *testing.T) {
	w := provisiontest.NewScratch(t)
	marker := filepath.Join(w.State, canaryroot.ScratchMarkerName)
	before, _ := os.Stat(marker)
	code, out, errOut := doScratch(t, w, "scratch-init", "--config", w.ConfigPath)
	if code != exitOK || errOut != "" || !strings.Contains(out, "kept file "+marker) || !strings.Contains(out, "kept folder "+w.State) {
		t.Fatalf("status %d, out %q, err %q", code, out, errOut)
	}
	after, _ := os.Stat(marker)
	if !after.ModTime().Equal(before.ModTime()) {
		t.Error("the marker was written again")
	}
}

func TestScratchInitInAnExistingEmptyRoot(t *testing.T) {
	w := provisiontest.NewScratch(t)
	freshPlace(t, w)
	if code, _, errOut := doScratch(t, w, "scratch-init", "--config", w.ConfigPath); code != exitOK {
		t.Fatalf("status %d: %s", code, errOut)
	}
	if err := canaryroot.CheckMarker(w.State, "scratch-alpha"); err != nil {
		t.Fatal(err)
	}
}

// TestScratchInitRefusals holds that init refuses a place that is not safe or
// not its own and writes no marker in it.
func TestScratchInitRefusals(t *testing.T) {
	cases := map[string]func(t *testing.T, w *provisiontest.ScratchWorld){
		"the canary's marker is there": func(t *testing.T, w *provisiontest.ScratchWorld) {
			freshPlace(t, w)
			provisiontest.WriteFile(t, filepath.Join(w.State, canaryroot.MarkerName), []byte("x"), 0o600)
		},
		"a marker with other content": func(t *testing.T, w *provisiontest.ScratchWorld) {
			freshPlace(t, w)
			provisiontest.WriteFile(t, filepath.Join(w.State, canaryroot.ScratchMarkerName), []byte("{}\n"), 0o600)
		},
		"a marker with another mode": func(t *testing.T, w *provisiontest.ScratchWorld) {
			freshPlace(t, w)
			provisiontest.WriteFile(t, filepath.Join(w.State, canaryroot.ScratchMarkerName),
				[]byte(`{"marker":"ataniq-scratch-root","version":1}`+"\n"), 0o644)
		},
		"a state root open to others": func(t *testing.T, w *provisiontest.ScratchWorld) {
			freshPlace(t, w)
			if err := os.Chmod(w.State, 0o755); err != nil {
				t.Fatal(err)
			}
		},
		"a state root that is a link": func(t *testing.T, w *provisiontest.ScratchWorld) {
			real := filepath.Join(w.Dir, "real-state")
			if err := os.Rename(w.State, real); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(real, w.State); err != nil {
				t.Fatal(err)
			}
			freshPlace2(t, real)
		},
		"a state root that is a file": func(t *testing.T, w *provisiontest.ScratchWorld) {
			if err := os.RemoveAll(w.State); err != nil {
				t.Fatal(err)
			}
			provisiontest.WriteFile(t, w.State, nil, 0o600)
		},
		"a state root whose parent is not there": func(t *testing.T, w *provisiontest.ScratchWorld) {
			if err := os.RemoveAll(w.State); err != nil {
				t.Fatal(err)
			}
			w.Config.StateRoot = filepath.Join(w.Dir, "absent", "state")
			w.WriteConfig(t)
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			w := provisiontest.NewScratch(t)
			setup(t, w)
			code, out, errOut := doScratch(t, w, "scratch-init", "--config", w.ConfigPath)
			if code != exitFailed || errOut == "" {
				t.Fatalf("status %d, out %q, err %q", code, out, errOut)
			}
			if strings.Contains(out, "made file") {
				t.Errorf("a marker was written: %q", out)
			}
		})
	}
}

func freshPlace2(t *testing.T, dir string) {
	t.Helper()
	if err := os.Remove(filepath.Join(dir, canaryroot.ScratchMarkerName)); err != nil {
		t.Fatal(err)
	}
}

func TestScratchInitRefusesADefaultRoot(t *testing.T) {
	w := provisiontest.NewScratch(t)
	home := filepath.Join(w.Dir, "home")
	inside := filepath.Join(home, ".local", "state", "ataniq", "scratch")
	if err := os.MkdirAll(filepath.Dir(inside), 0o700); err != nil {
		t.Fatal(err)
	}
	w.Config.StateRoot = inside
	w.WriteConfig(t)
	code, _, errOut := doScratch(t, w, "scratch-init", "--config", w.ConfigPath)
	if code != exitFailed || !strings.Contains(errOut, "default-root") {
		t.Fatalf("status %d, err %q", code, errOut)
	}
	if _, err := os.Lstat(inside); err == nil {
		t.Error("a folder was made in the default root")
	}
}

func TestScratchInitRefusesABadConfig(t *testing.T) {
	w := provisiontest.NewScratch(t)
	if err := os.RemoveAll(w.State); err != nil {
		t.Fatal(err)
	}
	// A canary config is not a scratch config.
	canary := provisiontest.New(t)
	code, _, errOut := doScratch(t, w, "scratch-init", "--config", canary.ConfigPath)
	if code != exitFailed || errOut == "" {
		t.Fatalf("status %d, err %q", code, errOut)
	}
	// A file that is open to others.
	if err := os.Chmod(w.ConfigPath, 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, errOut = doScratch(t, w, "scratch-init", "--config", w.ConfigPath)
	if code != exitFailed || !strings.Contains(errOut, "0600") {
		t.Fatalf("status %d, err %q", code, errOut)
	}
	if _, err := os.Lstat(w.State); err == nil {
		t.Error("a bad config made the state root")
	}
}

func TestTheCanaryCommandsRefuseAScratchConfig(t *testing.T) {
	w := provisiontest.NewScratch(t)
	for _, sub := range []string{"provision", "replay-on", "show"} {
		code, _, errOut := doScratch(t, w, sub, "--config", w.ConfigPath)
		if code != exitFailed || errOut == "" {
			t.Errorf("%s of a scratch config: status %d, err %q", sub, code, errOut)
		}
	}
}

func TestScratchUsageErrors(t *testing.T) {
	w := provisiontest.NewScratch(t)
	for name, args := range map[string][]string{
		"init without a config":          {"scratch-init"},
		"init with an empty config":      {"scratch-init", "--config", ""},
		"init with a lead":               {"scratch-init", "--config", w.ConfigPath, "--lead", "alpha"},
		"init with a further argument":   {"scratch-init", "--config", w.ConfigPath, "extra"},
		"clean without a config":         {"scratch-clean", "--lead", "alpha"},
		"clean without a lead":           {"scratch-clean", "--config", w.ConfigPath},
		"clean with an empty lead":       {"scratch-clean", "--config", w.ConfigPath, "--lead", ""},
		"clean with a further argument":  {"scratch-clean", "--config", w.ConfigPath, "--lead", "alpha", "x"},
		"clean with an unknown flag":     {"scratch-clean", "--config", w.ConfigPath, "--lead", "alpha", "--force"},
		"the flag before the subcommand": {"--config", w.ConfigPath, "scratch-init"},
	} {
		t.Run(name, func(t *testing.T) {
			code, out, errOut := doScratch(t, w, args...)
			if code != exitUsage || out != "" || !strings.Contains(errOut, "usage:") {
				t.Fatalf("status %d, out %q, err %q", code, out, errOut)
			}
		})
	}
	if _, err := os.Lstat(filepath.Join(w.State, "leads")); err == nil {
		t.Fatal("a usage error made something")
	}
}

func TestTheUsageNamesTheScratchCommands(t *testing.T) {
	for _, want := range []string{"scratch-init --config", "scratch-clean --config"} {
		if !strings.Contains(usage, want) {
			t.Errorf("the usage lacks %q", want)
		}
	}
}

// TestInitThenDefine holds that the marker the command writes is the one a
// define accepts, end to end, and that clean then refuses the lead that is done.
func TestInitThenDefineThenCleanRefuses(t *testing.T) {
	w := provisiontest.NewScratch(t)
	freshPlace(t, w)
	if code, _, errOut := doScratch(t, w, "scratch-init", "--config", w.ConfigPath); code != exitOK {
		t.Fatalf("init: %d %s", code, errOut)
	}
	cfg := w.Leads(t)[0]
	if _, err := provision.ProvisionScratch(context.Background(), cfg, w.Config.Sandbox, provision.Options{Getenv: w.Getenv()}); err != nil {
		t.Fatalf("define after init: %v", err)
	}
	code, _, errOut := doScratch(t, w, "scratch-clean", "--config", w.ConfigPath, "--lead", "alpha")
	if code != exitFailed || !strings.Contains(errOut, "provisioned") {
		t.Fatalf("clean of a done lead: status %d, err %q", code, errOut)
	}
	p, _ := provision.PathsOf(cfg)
	if _, err := os.Lstat(p.LeadRecord); err != nil {
		t.Errorf("the record is gone: %v", err)
	}
}

func TestScratchCleanRemovesAnIncompleteLead(t *testing.T) {
	w := provisiontest.NewScratch(t)
	cfg := w.Leads(t)[0]
	p, err := provision.PathsOf(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// What a cut-short define leaves: some folders, no record.
	for _, d := range []string{p.LeadDir, p.ScratchDir, p.Mailbox, p.RoleDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	other, _ := provision.PathsOf(w.Leads(t)[1])
	if err := os.MkdirAll(other.LeadDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if st, err := provision.ScratchStateOf(cfg); err != nil || st != provision.StateIncomplete {
		t.Fatalf("state %v, %v", st, err)
	}

	code, out, errOut := doScratch(t, w, "scratch-clean", "--config", w.ConfigPath, "--lead", "alpha")
	if code != exitOK || errOut != "" {
		t.Fatalf("status %d, err %q", code, errOut)
	}
	if !strings.Contains(out, "removed folder "+p.LeadDir) {
		t.Errorf("output %q", out)
	}
	for _, d := range []string{p.LeadDir, p.ScratchDir, p.Mailbox, p.RoleDir} {
		if _, err := os.Lstat(d); err == nil {
			t.Errorf("%s is still there", d)
		}
	}
	if _, err := os.Lstat(other.LeadDir); err != nil {
		t.Errorf("the other lead's folder was removed: %v", err)
	}
	// Run again: nothing to remove, still success.
	code, out, _ = doScratch(t, w, "scratch-clean", "--config", w.ConfigPath, "--lead", "alpha")
	if code != exitOK || !strings.Contains(out, "nothing to remove") {
		t.Fatalf("second run: status %d, out %q", code, out)
	}
}

func TestScratchCleanRefusals(t *testing.T) {
	w := provisiontest.NewScratch(t)
	// A key the config does not list.
	code, _, errOut := doScratch(t, w, "scratch-clean", "--config", w.ConfigPath, "--lead", "gamma")
	if code != exitFailed || !strings.Contains(errOut, "no lead with that key") {
		t.Fatalf("status %d, err %q", code, errOut)
	}
	// A key that is not a key at all is not found, and never reaches a path.
	for _, key := range []string{"../alpha", "alpha/../beta", "ALPHA", "scratch-alpha"} {
		code, _, errOut := doScratch(t, w, "scratch-clean", "--config", w.ConfigPath, "--lead", key)
		if code != exitFailed || !strings.Contains(errOut, "no lead with that key") {
			t.Errorf("key %q: status %d, err %q", key, code, errOut)
		}
	}
}
