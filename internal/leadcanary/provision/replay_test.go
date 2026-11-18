//go:build linux && ataniq_canary

package provision

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ataniq/internal/leadcanary/provision/provisiontest"
	"ataniq/internal/leadcanary/standin"
	"ataniq/internal/leadholder/canaryroot"
)

func TestReplayOnWritesTheSwitchFileAndNothingElse(t *testing.T) {
	w, p, _ := provisioned(t)
	before := snapshot(t, w.State)
	beforeRuntime := snapshot(t, w.Runtime)
	var lines []string
	if err := ReplayOn(w.Cfg, Options{Getenv: w.Getenv(), Log: func(s string) { lines = append(lines, s) }}); err != nil {
		t.Fatal(err)
	}
	fi := lstat(t, p.ReplaySwitch)
	if !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 || fi.Size() != 0 {
		t.Errorf("the switch file is %v, %d bytes", fi.Mode(), fi.Size())
	}
	if filepath.Base(p.ReplaySwitch) != standin.SwitchFile {
		t.Errorf("the switch file is %s", p.ReplaySwitch)
	}
	// The only difference is that file (and the folder's own entry list).
	after := snapshot(t, w.State)
	diff := lineDiff(before, after)
	if len(diff) != 1 || !strings.Contains(diff[0], "/"+standin.SwitchFile) {
		t.Errorf("replay-on changed more than the switch file: %q", diff)
	}
	if snapshot(t, w.Runtime) != beforeRuntime {
		t.Error("replay-on changed the runtime root")
	}
	if len(lines) != 1 || lines[0] != "made file "+p.ReplaySwitch {
		t.Errorf("log %q", lines)
	}
	// Exclusive: a second call fails and leaves the file as it is.
	if err := ReplayOn(w.Cfg, Options{Getenv: w.Getenv()}); err == nil {
		t.Fatal("a second replay-on succeeded")
	}
}

// lineDiff returns the lines that are in after and not in before.
func lineDiff(before, after string) []string {
	seen := map[string]bool{}
	for _, l := range strings.Split(before, "\n") {
		seen[l] = true
	}
	var out []string
	for _, l := range strings.Split(after, "\n") {
		if l != "" && !seen[l] {
			out = append(out, l)
		}
	}
	return out
}

func TestReplayOnRequiresTheMarker(t *testing.T) {
	w := provisiontest.New(t)
	err := ReplayOn(w.Cfg, Options{Getenv: w.Getenv()})
	if got := refusedCode(t, err); got != canaryroot.CodeNoMarker {
		t.Fatalf("ReplayOn: %v (code %q)", err, got)
	}
	// A marker for another project does not do.
	if err := os.WriteFile(filepath.Join(w.State, canaryroot.MarkerName), []byte(`{"marker":"ataniq-canary-root","version":1,"project":"throwaway-two"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ReplayOn(w.Cfg, Options{Getenv: w.Getenv()}); err == nil || refusedCode(t, err) != canaryroot.CodeBadMarker {
		t.Fatalf("ReplayOn with another project's marker: %v", err)
	}
}

func TestReplayOnWorksAfterAFirstStart(t *testing.T) {
	w, p, _ := provisioned(t)
	// The lead has run: the holder's folder holds its marks, and the lead has
	// written in its home.
	for _, n := range []string{"holder.json", "final.json", "lead-has-run"} {
		provisiontest.WriteFile(t, filepath.Join(p.Holder, n), []byte("{}"), 0o600)
	}
	provisiontest.WriteFile(t, filepath.Join(p.Home, "from-the-lead"), []byte("x"), 0o644)
	if err := ReplayOn(w.Cfg, Options{Getenv: w.Getenv()}); err != nil {
		t.Fatal(err)
	}
	if !exists(p.ReplaySwitch) {
		t.Fatal("no switch file")
	}
}

func TestReplayOnFollowsNoSymbolicLink(t *testing.T) {
	cases := map[string]func(t *testing.T, p Paths, outside string){
		"the replay folder": func(t *testing.T, p Paths, outside string) {
			if err := os.RemoveAll(p.ReplayDir); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, p.ReplayDir); err != nil {
				t.Fatal(err)
			}
		},
		"the home": func(t *testing.T, p Paths, outside string) {
			if err := os.Rename(p.Home, p.Home+"-moved"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, p.Home); err != nil {
				t.Fatal(err)
			}
		},
		"the switch file": func(t *testing.T, p Paths, outside string) {
			if err := os.Symlink(filepath.Join(outside, "target"), p.ReplaySwitch); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, plant := range cases {
		t.Run(name, func(t *testing.T) {
			w, p, _ := provisioned(t)
			outside := filepath.Join(w.Dir, "outside")
			if err := os.Mkdir(outside, 0o700); err != nil {
				t.Fatal(err)
			}
			plant(t, p, outside)
			if err := ReplayOn(w.Cfg, Options{Getenv: w.Getenv()}); err == nil {
				t.Fatal("replay-on followed a link")
			}
			if entries, _ := os.ReadDir(outside); len(entries) != 0 {
				t.Fatalf("something was made through the link: %v", entries)
			}
		})
	}
}

func TestReplayOnDoesNotMakeTheReplayFolder(t *testing.T) {
	w, p, _ := provisioned(t)
	if err := os.RemoveAll(p.ReplayDir); err != nil {
		t.Fatal(err)
	}
	err := ReplayOn(w.Cfg, Options{Getenv: w.Getenv()})
	if err == nil || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ReplayOn: %v", err)
	}
	if exists(p.ReplayDir) {
		t.Fatal("the folder was made")
	}
}
