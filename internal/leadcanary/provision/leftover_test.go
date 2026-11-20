//go:build linux && ataniq_canary

package provision

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ataniq/internal/leadcanary/provision/provisiontest"
)

// mkdirs makes every folder on the way to dir below the state root, and dir,
// each with mode 0700 exactly: what an earlier run would have left.
func mkdirs(t *testing.T, w *provisiontest.World, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for d := dir; d != w.State && strings.HasPrefix(d, w.State); d = filepath.Dir(d) {
		if err := os.Chmod(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
}

func TestALeftoverInAFolderThatWouldBeKeptRefusesTheRun(t *testing.T) {
	// Each case leaves one thing in a folder that provisioning makes and
	// would otherwise keep, and names the folder the refusal must name: the
	// outermost kept folder that holds something. So a leftover in the replay
	// folder is refused at the home, which holds the replay folder, and one in
	// the runtime's config folder at the config root.
	type place struct {
		in     func(p Paths) string // the folder the leftover is put in
		named  func(p Paths) string // the folder the refusal names
		folder bool                 // the leftover is a folder, not a file
	}
	same := func(f func(p Paths) string) place { return place{in: f, named: f} }
	cases := map[string]place{
		"a skill":                   same(func(p Paths) string { return p.LeadSkills }),
		"a file in the home":        same(func(p Paths) string { return p.Home }),
		"an assignment":             same(func(p Paths) string { return p.AssignmentDir }),
		"a projection":              same(func(p Paths) string { return p.ProjectionDir }),
		"a record":                  same(func(p Paths) string { return p.Records }),
		"a request":                 same(func(p Paths) string { return p.Requests }),
		"a mail":                    same(func(p Paths) string { return p.Mail }),
		"a letter in the mailbox":   same(func(p Paths) string { return p.Mailbox }),
		"a published file":          same(func(p Paths) string { return p.Published }),
		"a lock in the role folder": same(func(p Paths) string { return p.RoleDir }),
		"a second identity file":    same(func(p Paths) string { return p.GitIdentityDir }),
		"a file in the tree":        same(func(p Paths) string { return p.Tree }),
		"a folder in the install":   {in: func(p Paths) string { return p.QargiInstall }, named: func(p Paths) string { return p.QargiInstall }, folder: true},
		"another config folder":     {in: func(p Paths) string { return p.ConfigRoot }, named: func(p Paths) string { return p.ConfigRoot }, folder: true},
		"a file in the config":      {in: func(p Paths) string { return p.ConfigDir }, named: func(p Paths) string { return p.ConfigRoot }},
		"a screen":                  {in: func(p Paths) string { return p.ReplayDir }, named: func(p Paths) string { return p.Home }},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			w := provisiontest.New(t)
			p := mustPaths(t, w)
			const leftoverName = "left-over-entry"
			leftover, folder := filepath.Join(c.in(p), leftoverName), c.named(p)
			mkdirs(t, w, c.in(p))
			if c.folder {
				mkdirs(t, w, leftover)
			} else {
				provisiontest.WriteFile(t, leftover, []byte("left over\n"), 0o600)
			}
			before := snapshot(t, w.State)
			lines, err := provisionWorld(w)
			if !errors.Is(err, ErrNotEmpty) {
				t.Fatalf("Provision: %v, want ErrNotEmpty\n%q", err, lines)
			}
			if !strings.HasSuffix(err.Error(), ": "+folder) {
				t.Errorf("the refusal does not name %s: %v", folder, err)
			}
			if strings.Contains(err.Error(), leftoverName) {
				t.Errorf("the refusal names what the folder holds: %v", err)
			}
			if len(lines) != 0 || snapshot(t, w.State) != before || exists(w.Runtime) {
				t.Fatalf("the refused run made something\n%q", lines)
			}
		})
	}
}

func TestAReplaySwitchThatIsThereAlreadyRefusesTheRun(t *testing.T) {
	// With the emptiness check of the replay folder alone, the switch would be
	// refused as any leftover; it is refused by its own name first, as a file
	// or as a folder, and whatever else the replay folder holds.
	for _, name := range []string{"a file", "a folder"} {
		t.Run(name, func(t *testing.T) {
			w := provisiontest.New(t)
			p := mustPaths(t, w)
			mkdirs(t, w, p.ReplayDir)
			if name == "a file" {
				provisiontest.WriteFile(t, p.ReplaySwitch, nil, 0o600)
			} else {
				mkdirs(t, w, p.ReplaySwitch)
			}
			before := snapshot(t, w.State)
			lines, err := provisionWorld(w)
			if err == nil || !strings.Contains(err.Error(), p.ReplaySwitch+": exists already") {
				t.Fatalf("Provision: %v, want a refusal of the switch file\n%q", err, lines)
			}
			if len(lines) != 0 || snapshot(t, w.State) != before || exists(w.Runtime) {
				t.Fatalf("the refused run made something\n%q", lines)
			}
			if exists(p.Marker) {
				t.Fatal("a marker was written")
			}
		})
	}
}

func TestTheSharedParentsMayHoldOtherThingsAndEmptyFoldersAreKept(t *testing.T) {
	w := provisiontest.New(t)
	p := mustPaths(t, w)
	// What other leads and projects leave beside this lead's folders.
	others := []string{
		filepath.Join(p.LeadsDir, "throwaway-two-lead", "home"),
		filepath.Join(p.MailboxesDir, "throwaway-two-lead"),
		filepath.Join(p.PublishedDir, "throwaway-two"),
		filepath.Join(p.RolesDir, "throwaway-two"),
		filepath.Join(p.LeadDir, "note-of-the-owner"),
	}
	for _, d := range others {
		mkdirs(t, w, d)
	}
	provisiontest.WriteFile(t, filepath.Join(others[0], "kept.txt"), []byte("other lead\n"), 0o600)
	// The lock of another role lies in the roles folder, beside the folders of
	// the projects.
	provisiontest.WriteFile(t, filepath.Join(p.RolesDir, "supervisor.lock"), nil, 0o600)
	// Folders of this lead that an earlier, failed run made and left empty.
	// None of them holds another: a kept folder that holds even an empty
	// folder is refused, as the test of the home below shows.
	empties := []string{p.Holder, p.Tree, p.LeadSkills, p.ConfigRoot, p.Mailbox, p.Published, p.AssignmentDir, p.RoleDir}
	for _, d := range empties {
		mkdirs(t, w, d)
	}
	lines, err := provisionWorld(w)
	if err != nil {
		t.Fatalf("Provision: %v\n%q", err, lines)
	}
	if !exists(p.Marker) {
		t.Fatal("no marker")
	}
	for _, d := range others {
		if !exists(d) {
			t.Errorf("%s is gone", d)
		}
	}
	if got := readString(t, filepath.Join(others[0], "kept.txt")); got != "other lead\n" {
		t.Errorf("the other lead's file changed: %q", got)
	}
	joined := strings.Join(lines, "\n")
	for _, d := range empties {
		if !strings.Contains(joined, "kept folder "+d+"\n") {
			t.Errorf("no line says that %s was kept", d)
		}
	}
}

func TestAHomeThatHoldsOnlyTheEmptyReplayFolderIsRefusedToo(t *testing.T) {
	// The rule is about what is in a kept folder, not about who made it: the
	// home of a failed run holds the replay folder, and is refused until the
	// lead's folder is removed.
	w := provisiontest.New(t)
	p := mustPaths(t, w)
	mkdirs(t, w, p.ReplayDir)
	before := snapshot(t, w.State)
	if _, err := provisionWorld(w); !errors.Is(err, ErrNotEmpty) || !strings.HasSuffix(err.Error(), ": "+p.Home) {
		t.Fatalf("Provision: %v, want ErrNotEmpty for the home", err)
	}
	if snapshot(t, w.State) != before {
		t.Fatal("the refused run made something")
	}
}

func TestAnOldGitMetadataFolderRefusesTheRunBeforeAnythingIsMade(t *testing.T) {
	// The lead's own folder may hold other things, so the git metadata folder
	// and the temporary name it is built under are looked at by name.
	for _, suffix := range []string{"", gitMetaTmpSuffix} {
		t.Run("gitmeta"+suffix, func(t *testing.T) {
			w := provisiontest.New(t)
			p := mustPaths(t, w)
			old := p.GitMeta + suffix
			mkdirs(t, w, old)
			before := snapshot(t, w.State)
			lines, err := provisionWorld(w)
			if err == nil || !strings.Contains(err.Error(), old+": exists already") {
				t.Fatalf("Provision: %v, want a refusal of %s\n%q", err, old, lines)
			}
			if len(lines) != 0 || snapshot(t, w.State) != before || exists(w.Runtime) {
				t.Fatalf("the refused run made something\n%q", lines)
			}
		})
	}
}
