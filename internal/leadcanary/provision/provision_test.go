//go:build linux && ataniq_canary

package provision

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"

	"ataniq/internal/leadcanary/provision/provisiontest"
	"ataniq/internal/leadcanary/standin"
	"ataniq/internal/leadholder/canaryroot"
	"ataniq/internal/leadsandbox/mountpoints"
	"ataniq/internal/leadsandbox/profile"
)

// provisioned runs Provision in a new world and returns the world, the paths
// and the log lines.
func provisioned(t *testing.T) (*provisiontest.World, Paths, []string) {
	t.Helper()
	w := provisiontest.New(t)
	lines, err := provisionWorld(w)
	if err != nil {
		t.Fatalf("Provision: %v\nlog: %q", err, lines)
	}
	p, err := PathsOf(w.Cfg)
	if err != nil {
		t.Fatal(err)
	}
	return w, p, lines
}

func provisionWorld(w *provisiontest.World) ([]string, error) {
	var lines []string
	err := Provision(context.Background(), w.Cfg, Options{Getenv: w.Getenv(), Log: func(s string) { lines = append(lines, s) }})
	return lines, err
}

func lstat(t testing.TB, p string) fs.FileInfo {
	t.Helper()
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// folderPaths are the host paths of every folder that provisioning makes, the
// roots and the mount points excluded.
func folderPaths(p Paths) []string {
	return []string{
		p.LeadsDir, p.LeadDir, p.Holder, p.Home, p.ConfigRoot, p.ConfigDir, p.Tree, p.GitMeta,
		p.Records, p.Requests, p.Mail, p.MailboxesDir, p.Mailbox, p.PublishedDir, p.Published, p.MirrorsDir,
		p.LeadSkills, p.GitIdentityDir, p.QargiInstall, p.AssignmentDir, p.ProjectionDir,
		p.ReplayDir, p.RuntimeRoot, p.RuntimeLeads, p.RuntimeFolder,
	}
}

func TestEveryFolderOfTheLayoutExistsWithMode0700(t *testing.T) {
	w, p, _ := provisioned(t)
	for _, d := range folderPaths(p) {
		fi := lstat(t, d)
		if !fi.IsDir() || fi.Mode().Perm() != 0o700 {
			t.Errorf("%s: %v, want a folder of mode 0700", d, fi.Mode())
		}
		if st := fi.Sys().(*syscall.Stat_t); int(st.Uid) != os.Geteuid() {
			t.Errorf("%s: owned by %d", d, st.Uid)
		}
	}
	// The layout's own paths are the ones the profile names.
	l := p.Layout
	if p.Tree != l.Tree() || p.Mirror != l.Mirror() || p.Mailbox != l.Mailbox() || p.RuntimeFolder != filepath.Join(w.Runtime, "leads", "throwaway-one-lead") {
		t.Errorf("paths differ from the layout: %+v", p)
	}
	// The git metadata folder holds config, hooks and info.
	for _, n := range []string{profile.GitMetaConfig, profile.GitMetaHooks, profile.GitMetaInfo} {
		if !exists(filepath.Join(p.GitMeta, n)) {
			t.Errorf("the git metadata folder lacks %s", n)
		}
	}
	// The holder's state folder is made and left empty.
	if names, err := os.ReadDir(p.Holder); err != nil || len(names) != 0 {
		t.Errorf("holder folder: %v, %v", names, err)
	}
}

func TestMountPointsOwnChecksPassOnTheResult(t *testing.T) {
	w, p, _ := provisioned(t)
	in := profileInputs(w.Cfg, p, hashHex([]byte(readString(t, w.Settings))))
	rep, err := mountpoints.Check(in)
	if err != nil || !rep.OK() {
		t.Fatalf("Check: %v, %v", err, rep.Err())
	}
	own, err := mountpoints.CheckOwnClone(in, uint32(os.Geteuid()))
	if err != nil || !own.OK() {
		t.Fatalf("CheckOwnClone: %v, %v", err, own.Err())
	}
	// A second Prepare, which every start runs first, changes nothing and passes.
	if err := mountpoints.Prepare(in, mountpoints.PrepareOptions{NoLeadHasRun: true}); err != nil {
		t.Fatalf("Prepare again: %v", err)
	}
}

func TestFilesAreCopiedWithTheirModes(t *testing.T) {
	w, p, _ := provisioned(t)
	for _, c := range []struct {
		path string
		mode fs.FileMode
		want string
	}{
		{p.ManagedSettings, 0o400, readString(t, w.Settings)},
		{p.GuardHook, 0o500, readString(t, w.Hook)},
		{p.InstructionFile, 0o600, ""},
	} {
		fi := lstat(t, c.path)
		if !fi.Mode().IsRegular() || fi.Mode().Perm() != c.mode {
			t.Errorf("%s: %v, want regular %04o", c.path, fi.Mode(), c.mode)
		}
		if got := readString(t, c.path); got != c.want {
			t.Errorf("%s: content differs from its source", c.path)
		}
	}
	// The git identity folder holds the identity file and nothing else.
	entries, err := os.ReadDir(p.GitIdentityDir)
	if err != nil || len(entries) != 1 || entries[0].Name() != profile.GitIdentityFileName {
		t.Errorf("git identity folder: %v, %v", entries, err)
	}
	// The placeholders for the skills, assignment and projection are empty.
	for _, d := range []string{p.LeadSkills, p.AssignmentDir, p.ProjectionDir, p.Records, p.Requests, p.Mail, p.Mailbox, p.Published} {
		if entries, err := os.ReadDir(d); err != nil || len(entries) != 0 {
			t.Errorf("%s holds %v (%v)", d, entries, err)
		}
	}
	// The Qargi install holds the hook and nothing else.
	if entries, err := os.ReadDir(p.QargiInstall); err != nil || len(entries) != 1 || entries[0].Name() != GuardHookName {
		t.Errorf("qargi install: %v, %v", entries, err)
	}
}

func readString(t testing.TB, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestReplayScreensAreCopiedAndTheSwitchIsNot(t *testing.T) {
	w := provisiontest.New(t)
	// A switch file in the source folder is not copied; a report is.
	provisiontest.WriteFile(t, filepath.Join(w.Screens, standin.SwitchFile), nil, 0o600)
	provisiontest.WriteFile(t, filepath.Join(w.Screens, standin.ReportFile), []byte("state=idle\n"), 0o600)
	provisiontest.WriteFile(t, filepath.Join(w.Screens, "other.txt"), []byte("not wanted"), 0o600)
	if _, err := provisionWorld(w); err != nil {
		t.Fatal(err)
	}
	p, _ := PathsOf(w.Cfg)
	var got []string
	entries, err := os.ReadDir(p.ReplayDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		got = append(got, e.Name())
		fi, _ := e.Info()
		if fi.Mode().Perm() != 0o600 || !fi.Mode().IsRegular() {
			t.Errorf("%s: %v", e.Name(), fi.Mode())
		}
		if readString(t, filepath.Join(p.ReplayDir, e.Name())) != readString(t, filepath.Join(w.Screens, e.Name())) {
			t.Errorf("%s differs from its source", e.Name())
		}
	}
	sort.Strings(got)
	if want := "1.json 1.txt 2.json 2.txt report.txt"; strings.Join(got, " ") != want {
		t.Fatalf("replay folder holds %v, want %s", got, want)
	}
	if exists(p.ReplaySwitch) {
		t.Fatal("the switch file was copied")
	}
}

func TestTheMarkerIsTheLastActAndTheLastLine(t *testing.T) {
	w, p, lines := provisioned(t)
	if got := lines[len(lines)-1]; got != "made file "+p.Marker {
		t.Fatalf("last line %q, want the marker", got)
	}
	if err := canaryroot.Check(w.State, w.Runtime, "throwaway-one", w.Getenv()); err != nil {
		t.Fatalf("canaryroot.Check: %v", err)
	}
	// Every line says what it made or kept, and none holds a file's content.
	for _, l := range lines {
		if strings.Contains(l, provisiontest.Secret) || !(strings.HasPrefix(l, "made ") || strings.HasPrefix(l, "kept ") || strings.HasPrefix(l, "cloned ") || strings.HasPrefix(l, "checked ")) {
			t.Errorf("unexpected line %q", l)
		}
	}
	// Every path the run made is older than, or as old as, the marker.
	mfi := lstat(t, p.Marker)
	err := filepath.WalkDir(w.State, func(path string, d fs.DirEntry, err error) error {
		if err != nil || path == p.Marker || strings.Contains(path, "/mirrors/") || strings.Contains(path, "/.git/") {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		if fi.ModTime().After(mfi.ModTime()) && !fi.IsDir() {
			t.Errorf("%s is newer than the marker", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestAFailedRunLeavesNoMarker(t *testing.T) {
	w := provisiontest.New(t)
	// A git that does everything but clone.
	old := GitPath
	t.Cleanup(func() { GitPath = old })
	GitPath = fakeGit(t, "clone")
	lines, err := provisionWorld(w)
	if err == nil {
		t.Fatalf("a failing clone did not fail the run\n%q", lines)
	}
	p, _ := PathsOf(w.Cfg)
	if exists(p.Marker) {
		t.Fatal("a failed run left the marker")
	}
	// It got as far as the folders, so the failure was after they were made.
	if !exists(p.Tree) {
		t.Fatal("the run failed before the tree was made; the test shows nothing")
	}
	for _, l := range lines {
		if strings.Contains(l, "marker") || strings.Contains(l, canaryroot.MarkerName) {
			t.Errorf("a marker line after a failure: %q", l)
		}
	}
}

func TestASecondRunAfterTheFirstIsRefused(t *testing.T) {
	w, p, _ := provisioned(t)
	before := snapshot(t, w.State)
	lines, err := provisionWorld(w)
	if !errors.Is(err, ErrFirstStart) {
		t.Fatalf("second run: %v, want ErrFirstStart", err)
	}
	if len(lines) != 0 || snapshot(t, w.State) != before {
		t.Fatal("the refused second run changed something")
	}
	// ReplayOn is allowed; Provision is still refused after it.
	if err := ReplayOn(w.Cfg, Options{Getenv: w.Getenv()}); err != nil {
		t.Fatal(err)
	}
	if _, err := provisionWorld(w); !errors.Is(err, ErrFirstStart) {
		t.Fatalf("after replay-on: %v", err)
	}
	_ = p
}

// snapshot is the names, types and modes of everything below dir, as one text.
func snapshot(t testing.TB, dir string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		b.WriteString(strings.TrimPrefix(path, dir) + " " + fi.Mode().String() + " " + string(rune('0'+fi.Size()%10)) + "\n")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestAFirstStartInTheHoldersStateFolderRefusesAndMakesNothing(t *testing.T) {
	for _, mark := range []string{"holder.json", "final.json", "lead-has-run", "something-else"} {
		t.Run(mark, func(t *testing.T) {
			w := provisiontest.New(t)
			p, _ := PathsOf(w.Cfg)
			for _, d := range []string{p.LeadsDir, p.LeadDir, p.Holder} {
				if err := os.Mkdir(d, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			provisiontest.WriteFile(t, filepath.Join(p.Holder, mark), []byte("{}"), 0o600)
			before := snapshot(t, w.State)
			lines, err := provisionWorld(w)
			if !errors.Is(err, ErrFirstStart) {
				t.Fatalf("Provision: %v, want ErrFirstStart", err)
			}
			if len(lines) != 0 || snapshot(t, w.State) != before || exists(w.Runtime) {
				t.Fatal("the refused run made something")
			}
		})
	}
}

func TestAMarkerAloneRefusesTheRun(t *testing.T) {
	w := provisiontest.New(t)
	if err := canaryroot.WriteMarker(w.State, "throwaway-one"); err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, w.State)
	if _, err := provisionWorld(w); !errors.Is(err, ErrFirstStart) {
		t.Fatalf("Provision: %v, want ErrFirstStart", err)
	}
	if snapshot(t, w.State) != before || exists(w.Runtime) {
		t.Fatal("the refused run made something")
	}
}

func TestTheRuntimeFolderIsMadeAndAnOldOneIsRefused(t *testing.T) {
	_, p, _ := provisioned(t)
	for _, d := range []string{p.RuntimeRoot, p.RuntimeLeads, p.RuntimeFolder} {
		if fi := lstat(t, d); !fi.IsDir() || fi.Mode().Perm() != 0o700 {
			t.Errorf("%s: %v", d, fi.Mode())
		}
	}
	// A runtime folder that holds something shows a start.
	w2 := provisiontest.New(t)
	p2, _ := PathsOf(w2.Cfg)
	for _, d := range []string{p2.RuntimeRoot, p2.RuntimeLeads, p2.RuntimeFolder, filepath.Join(p2.RuntimeFolder, "report")} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := provisionWorld(w2); !errors.Is(err, ErrFirstStart) {
		t.Fatalf("a used runtime folder: %v", err)
	}
	// A runtime root with the wrong mode is refused, before anything is made.
	w3 := provisiontest.New(t)
	if err := os.Mkdir(w3.Runtime, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(w3.Runtime, 0o755); err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, w3.State)
	if _, err := provisionWorld(w3); err == nil || !strings.Contains(err.Error(), "0700") {
		t.Fatalf("a runtime root of mode 0755: %v", err)
	}
	if snapshot(t, w3.State) != before {
		t.Fatal("the refused run made something")
	}
}

func TestTheStateRootMustExistWithMode0700(t *testing.T) {
	w := provisiontest.New(t)
	if err := os.Chmod(w.State, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := provisionWorld(w); err == nil || !strings.Contains(err.Error(), "0700") {
		t.Fatalf("a state root of mode 0755: %v", err)
	}
	if err := os.Chmod(w.State, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(w.State); err != nil {
		t.Fatal(err)
	}
	if _, err := provisionWorld(w); err == nil {
		t.Fatal("a missing state root was accepted")
	}
	if exists(w.State) || exists(w.Runtime) {
		t.Fatal("a refused run made a root")
	}
}

func TestATreeThatIsNotEmptyIsRefused(t *testing.T) {
	w := provisiontest.New(t)
	p, _ := PathsOf(w.Cfg)
	for _, d := range []string{p.LeadsDir, p.LeadDir, p.Tree} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	provisiontest.WriteFile(t, filepath.Join(p.Tree, "left-over"), nil, 0o600)
	if _, err := provisionWorld(w); err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Fatalf("Provision: %v", err)
	}
	if exists(p.Marker) {
		t.Fatal("marker")
	}
}

func TestAnExistingFileAtAMadePathIsRefused(t *testing.T) {
	w := provisiontest.New(t)
	p, _ := PathsOf(w.Cfg)
	for _, d := range []string{p.LeadsDir, p.LeadDir, p.GitIdentityDir} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	provisiontest.WriteFile(t, p.GitIdentityFile, []byte("[user]\n\tname = x\n"), 0o600)
	if _, err := provisionWorld(w); err == nil || !strings.Contains(err.Error(), "exists already") {
		t.Fatalf("Provision: %v", err)
	}
	if got := readString(t, p.GitIdentityFile); got != "[user]\n\tname = x\n" {
		t.Fatal("the existing file was replaced")
	}
}
