//go:build linux && ataniq_canary

package provision

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ataniq/internal/leadcanary/provision/provisiontest"
	"ataniq/internal/leadsandbox/mountpoints"
)

// plantable is one path provisioning makes, and whether a link there points
// at a folder or at a file.
type plantable struct {
	path string
	dir  bool
	// root is the root the path lies under: its ancestors are made as real
	// folders by the test.
	root string
}

// madePaths lists every path provisioning makes by itself, as the world's
// config gives them: the folders, the files, the mount points in the home and
// the config folder, the runtime folders and the marker.
func madePaths(t *testing.T, w *provisiontest.World) []plantable {
	t.Helper()
	p, err := PathsOf(w.Cfg)
	if err != nil {
		t.Fatal(err)
	}
	var out []plantable
	for _, d := range []string{
		p.LeadsDir, p.LeadDir, p.Holder, p.Home, p.ConfigRoot, p.ConfigDir, p.Tree, p.GitMeta,
		p.Records, p.Requests, p.Mail, p.MailboxesDir, p.Mailbox, p.PublishedDir, p.Published,
		p.LeadSkills, p.GitIdentityDir, p.QargiInstall, p.AssignmentDir, p.ProjectionDir, p.ReplayDir,
	} {
		out = append(out, plantable{d, true, p.StateRoot})
	}
	for _, f := range []string{
		p.GitIdentityFile, p.InstructionFile, p.ManagedSettings, p.GuardHook, p.Marker,
		filepath.Join(p.ReplayDir, "1.json"), filepath.Join(p.ReplayDir, "1.txt"),
		filepath.Join(p.ReplayDir, "2.json"), filepath.Join(p.ReplayDir, "2.txt"),
	} {
		out = append(out, plantable{f, false, p.StateRoot})
	}
	in := profileInputs(w.Cfg, p, hashHex([]byte("x")))
	pts, err := mountpoints.List(in)
	if err != nil {
		t.Fatal(err)
	}
	for _, pt := range pts {
		if pt.Root != mountpoints.RootTree {
			out = append(out, plantable{pt.HostPath, pt.Dir, p.StateRoot})
		}
	}
	for _, d := range []string{p.RuntimeLeads, p.RuntimeFolder} {
		out = append(out, plantable{d, true, p.RuntimeRoot})
	}
	out = append(out, plantable{p.RuntimeRoot, true, filepath.Dir(p.RuntimeRoot)})
	return out
}

func TestAPlantedSymlinkAtAnyMadePathRefusesTheRun(t *testing.T) {
	probe := madePaths(t, provisiontest.New(t))
	if len(probe) < 35 {
		t.Fatalf("only %d paths listed; the test would show little", len(probe))
	}
	for i := range probe {
		i := i
		name := strings.TrimPrefix(probe[i].path, filepath.Dir(filepath.Dir(probe[i].path)))
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			w := provisiontest.New(t)
			// Every world has the same layout below its own folder, so the
			// i-th path of this world is the i-th of the probe.
			pl := madePaths(t, w)[i]
			target, root := pl.path, pl.root
			outside := filepath.Join(w.Dir, "outside")
			sentinel := []byte("outside content\n")
			if pl.dir {
				if err := os.Mkdir(outside, 0o700); err != nil {
					t.Fatal(err)
				}
			} else {
				provisiontest.WriteFile(t, outside, sentinel, 0o600)
			}
			// Real folders of mode 0700 on the way, so that the symbolic link
			// is the one thing wrong.
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				t.Fatal(err)
			}
			for d := filepath.Dir(target); d != root && strings.HasPrefix(d, root); d = filepath.Dir(d) {
				if err := os.Chmod(d, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(outside, target); err != nil {
				t.Fatal(err)
			}
			lines, err := provisionWorld(w)
			if err == nil {
				t.Fatalf("a symbolic link at %s did not refuse the run\n%q", target, lines)
			}
			marker := mustPaths(t, w).Marker
			if target != marker && !strings.Contains(err.Error(), "symbolic link") {
				t.Errorf("refused, but not for the link: %v", err)
			}
			// Nothing was written through the link.
			if pl.dir {
				if entries, _ := os.ReadDir(outside); len(entries) != 0 {
					t.Errorf("something was made through the link: %v", entries)
				}
			} else if got := readString(t, outside); got != string(sentinel) {
				t.Errorf("the file behind the link changed: %q", got)
			}
			// The link is still a link, and there is no marker of the run's.
			if fi, err := os.Lstat(target); err != nil || fi.Mode()&os.ModeSymlink == 0 {
				t.Errorf("the link at %s is gone: %v", target, err)
			}
			if target != marker && exists(marker) {
				t.Error("a marker was written")
			}
		})
	}
}

func mustPaths(t testing.TB, w *provisiontest.World) Paths {
	t.Helper()
	p, err := PathsOf(w.Cfg)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAPlantedSymlinkAsTheStateRootRefusesTheRun(t *testing.T) {
	w := provisiontest.New(t)
	real := filepath.Join(w.Dir, "state-real")
	if err := os.Rename(w.State, real); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, w.State); err != nil {
		t.Fatal(err)
	}
	if _, err := provisionWorld(w); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("a state root that is a link: %v", err)
	}
	if exists(filepath.Join(real, "leads")) || exists(w.Runtime) {
		t.Fatal("something was made")
	}
}

func TestASymlinkAboveTheStateRootRefusesTheRun(t *testing.T) {
	w := provisiontest.New(t)
	above := filepath.Join(w.Dir, "above")
	if err := os.Mkdir(above, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(w.State, filepath.Join(above, "state")); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(w.Dir, "link")
	if err := os.Symlink(above, link); err != nil {
		t.Fatal(err)
	}
	w.Cfg.StateRoot = filepath.Join(link, "state")
	w.Cfg.Provision.Mirror = filepath.Join(link, "state", "mirrors", "throwaway-one.git")
	w.WriteConfig(t)
	if _, err := provisionWorld(w); err == nil {
		t.Fatal("a link above the state root was followed")
	}
	if exists(filepath.Join(above, "state", "leads")) {
		t.Fatal("something was made")
	}
}
