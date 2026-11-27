package leadregistry

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ataniq/internal/leadrole"
)

func closeHomeRegistry(t *testing.T, r *Registry) {
	t.Helper()
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
}

func instanceRegistry(t *testing.T) (string, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "state")
	id, err := InitInstance(root, "registry", "")
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, "registry"), id.Authority
}

func TestProjectHomeAdmission(t *testing.T) {
	dir, self := instanceRegistry(t)
	_, peer := instanceRegistry(t)
	r := openReg(t, dir)
	l := lead(t, "sample")
	c := coord(t, "sample", "feature")
	check := func(want error) {
		t.Helper()
		size := journalSize(t, dir)
		for _, role := range []leadrole.Role{l, c} {
			if _, err := r.Define(role, ManagerAtaniq, goodProfile()); !errors.Is(err, want) {
				t.Fatalf("define %v: %v", role, err)
			}
			if _, err := r.BeginLaunch(role); !errors.Is(err, want) {
				t.Fatalf("launch %v: %v", role, err)
			}
		}
		if journalSize(t, dir) != size {
			t.Fatal("refusal appended to the registry")
		}
	}
	check(ErrHomeMissing)
	if err := r.SetProjectAssignment(l.Project(), peer, ProjectVersion{}, true); err != nil {
		t.Fatal(err)
	}
	check(ErrOtherHome)
	row, err := r.ProjectHome(l.Project())
	if err != nil || row.Holder == nil || *row.Holder != (LeadRef{peer, l}) || row.ExecutingInstance != peer {
		t.Fatalf("foreign assignment %+v: %v", row, err)
	}
	if err := r.SetProjectAssignment(l.Project(), self, row.ProjectVersion, true); !errors.Is(err, ErrOtherHome) {
		t.Fatalf("foreign home claimed locally: %v", err)
	}
	live := lead(t, "local")
	if err := r.SetProjectAssignment(live.Project(), self, ProjectVersion{}, true); err != nil {
		t.Fatal(err)
	}
	liveLead(t, r, live)
	child := coord(t, live.Project(), "feature")
	okf(t)(r.Define(child, ManagerAtaniq, Requested{}))
	if _, err := r.BeginLaunch(child); err != nil {
		t.Fatal(err)
	}
	if err := r.SetProjectAssignment(live.Project(), self, ProjectVersion{1, 1}, false); !errors.Is(err, ErrAssignmentBusy) {
		t.Fatalf("vacated a live assignment: %v", err)
	}
}

func TestAssignmentCASAndRecordedVersion(t *testing.T) {
	dir, self := instanceRegistry(t)
	r := openReg(t, dir)
	role := lead(t, "sample")
	set := func(v ProjectVersion, held bool) error {
		return r.SetProjectAssignment(role.Project(), self, v, held)
	}
	if err := set(ProjectVersion{}, true); err != nil {
		t.Fatal(err)
	}
	okf(t)(r.Define(role, ManagerAtaniq, goodProfile()))
	for _, tc := range []struct {
		token ProjectVersion
		want  error
	}{{ProjectVersion{0, 1}, ErrStaleGeneration}, {ProjectVersion{1, 0}, ErrStaleRevision}} {
		size := journalSize(t, dir)
		_, define := r.DefineAt(role, ManagerAtaniq, goodProfile(), tc.token)
		_, launch := r.BeginLaunchWith(role, LaunchOptions{Project: &tc.token})
		for _, err := range []error{set(tc.token, false), define, launch} {
			if !errors.Is(err, tc.want) {
				t.Fatalf("token %+v: %v", tc.token, err)
			}
		}
		if journalSize(t, dir) != size {
			t.Fatal("stale admission appended a line")
		}
	}
	old := ProjectVersion{1, 1}
	if err := set(old, false); err != nil {
		t.Fatal(err)
	}
	if _, err := r.BeginLaunch(role); !errors.Is(err, ErrUnassigned) {
		t.Fatal(err)
	}
	if err := set(ProjectVersion{1, 2}, true); err != nil {
		t.Fatal(err)
	}
	// A well-chained stale admission after release and reassignment is
	// corrupt, even though the project is again assigned to the same lead.
	_, line, _, err := r.prepare(kindDefine, role, &definePayload{Manager: ManagerAtaniq, Requested: goodProfile(), Assignment: &old})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.f.Write(line); err != nil {
		t.Fatal(err)
	}
	closeHomeRegistry(t, r)
	if _, err := Open(dir, Options{}); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("replayed stale admission: %v", err)
	}
}

func TestCompetingProjectHomesAfterCrash(t *testing.T) {
	dir, self := instanceRegistry(t)
	_, peer := instanceRegistry(t)
	project := "sample"
	r := openReg(t, dir)
	path := filepath.Join(dir, FileName)
	header, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Both callers compare against an empty row. The losing call cannot
	// replace the winner even if it observed the same initial version.
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, home := range []string{self, peer} {
		go func() {
			<-start
			results <- r.SetProjectAssignment(project, home, ProjectVersion{}, true)
		}()
	}
	close(start)
	first, second := <-results, <-results
	if (first == nil) == (second == nil) || !errors.Is(errors.Join(first, second), ErrStaleGeneration) {
		t.Fatalf("competing enrollments: %v, %v", first, second)
	}
	row, err := r.ProjectHome(project)
	if err != nil {
		t.Fatal(err)
	}
	closeHomeRegistry(t, r)
	full, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Count(full, []byte("\n")) != 2 {
		t.Fatal("multiple enrollment records")
	}
	other := self
	if row.HomeAuthority == self {
		other = peer
	}

	for _, size := range []int{len(header), len(header) + 1, len(header) + (len(full)-len(header))/2, len(full) - 1, len(full)} {
		if err := os.WriteFile(path, full[:size], 0600); err != nil {
			t.Fatal(err)
		}
		r := openReg(t, dir)
		err := r.SetProjectAssignment(project, other, ProjectVersion{}, true)
		want := other
		if size == len(full) {
			want = row.HomeAuthority
			if !errors.Is(err, ErrStaleGeneration) {
				t.Fatalf("lost reply: %v", err)
			}
		} else if err != nil {
			t.Fatalf("cut at %d: %v", size, err)
		}
		closeHomeRegistry(t, r)
		r = openReg(t, dir)
		got, err := r.ProjectHome(project)
		if err != nil || got.ProjectVersion != (ProjectVersion{1, 1}) || got.HomeAuthority != want ||
			got.Holder == nil || *got.Holder != (LeadRef{want, lead(t, project)}) || got.ExecutingInstance != want {
			t.Fatalf("cut at %d: inconsistent row %+v, %v", size, got, err)
		}
		closeHomeRegistry(t, r)
	}
}

func TestReplaceProjectHome(t *testing.T) {
	dir, self := instanceRegistry(t)
	_, peer := instanceRegistry(t)
	project := "sample"
	enroll := func(home string, replace bool) error {
		_, err := EnrollProjectHome(filepath.Dir(dir), home, project, EnrollmentOptions{Replace: replace, ConfirmSelf: true})
		return err
	}
	if err := enroll(self, true); !errors.Is(err, ErrHomeMissing) {
		t.Fatal(err)
	}
	if err := enroll(peer, false); err != nil {
		t.Fatal(err)
	}
	if err := enroll(self, true); err != nil {
		t.Fatal(err)
	}
	size := journalSize(t, dir)
	if err := enroll(self, true); err != nil || journalSize(t, dir) != size {
		t.Fatalf("retry changed the journal: %v", err)
	}
	r := openReg(t, dir)
	row, err := r.ProjectHome(project)
	if err != nil || row.ProjectVersion != (ProjectVersion{2, 2}) || row.HomeAuthority != self {
		t.Fatalf("reopened correction: %+v, %v", row, err)
	}
	role := lead(t, project)
	if _, err := r.DefineAt(role, ManagerAtaniq, goodProfile(), ProjectVersion{1, 1}); !errors.Is(err, ErrStaleGeneration) {
		t.Fatal(err)
	}
	if err := r.SetProjectAssignment(project, self, row.ProjectVersion, false); err != nil {
		t.Fatal(err)
	}
	if err := r.SetProjectAssignment(project, self, ProjectVersion{2, 3}, true); err != nil {
		t.Fatal(err)
	}
	okf(t)(r.Define(role, ManagerAtaniq, goodProfile()))
	closeHomeRegistry(t, r)
	size = journalSize(t, dir)
	if err := enroll(peer, true); !errors.Is(err, ErrAssignmentBusy) || journalSize(t, dir) != size {
		t.Fatalf("replaced a defined lead: %v", err)
	}
	r = openReg(t, dir)
	if _, err := r.BeginLaunch(role); err != nil {
		t.Fatal(err)
	}
	closeHomeRegistry(t, r)
	if err := enroll(peer, true); !errors.Is(err, ErrAssignmentBusy) {
		t.Fatalf("replaced a pending launch: %v", err)
	}
}

func TestReplacementReplayChecksOldHome(t *testing.T) {
	dir, self := instanceRegistry(t)
	_, peer := instanceRegistry(t)
	r := openReg(t, dir)
	role := lead(t, "sample")
	if err := r.SetProjectAssignment(role.Project(), self, ProjectVersion{}, true); err != nil {
		t.Fatal(err)
	}
	body := &replaceHomePayload{PreviousHome: peer, ProjectAssignment: ProjectAssignment{
		HomeAuthority: peer, ProjectVersion: ProjectVersion{2, 2}, Holder: &LeadRef{peer, role}, ExecutingInstance: peer,
	}}
	_, line, _, err := r.prepare(kindReplaceHome, role, body)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.f.Write(line); err != nil {
		t.Fatal(err)
	}
	closeHomeRegistry(t, r)
	if _, err := Open(dir, Options{}); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("replayed correction against the wrong predecessor: %v", err)
	}
}

func TestHomeRequiredAfterMigration(t *testing.T) {
	data, _ := readV1Base(t)
	dir := writeJournal(t, data)
	root := filepath.Dir(dir)
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	id, err := InitInstance(root, filepath.Base(dir), "")
	if err != nil {
		t.Fatal(err)
	}
	r := openReg(t, dir)
	role := lead(t, "fixture-a")
	if _, err := r.BeginLaunch(role); !errors.Is(err, ErrHomeMissing) || !strings.Contains(err.Error(), "project-home") {
		t.Fatalf("migrated project: %v", err)
	}
	if err := r.SetProjectAssignment(role.Project(), id.Authority, ProjectVersion{}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := r.CheckProjectHome(role, nil); err != nil {
		t.Fatal(err)
	}
}

func TestUnboundRegistryCannotAdmitProject(t *testing.T) {
	r, err := Open(regDir(t), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer closeHomeRegistry(t, r)
	role := lead(t, "sample")
	if _, err := r.Define(role, ManagerAtaniq, goodProfile()); !errors.Is(err, ErrHomeMissing) {
		t.Fatal(err)
	}
	if _, err := r.BeginLaunch(role); !errors.Is(err, ErrHomeMissing) {
		t.Fatal(err)
	}
}

func TestReplayedCompetingAssignmentIsCorrupt(t *testing.T) {
	dir, self := instanceRegistry(t)
	_, peer := instanceRegistry(t)
	r := openReg(t, dir)
	role := lead(t, "sample")
	if err := r.SetProjectAssignment(role.Project(), self, ProjectVersion{}, true); err != nil {
		t.Fatal(err)
	}
	row := &ProjectAssignment{ProjectVersion: ProjectVersion{1, 1}, HomeAuthority: peer, Holder: &LeadRef{peer, role}, ExecutingInstance: peer}
	_, line, _, err := r.prepare(kindProjectHome, role, row)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.f.Write(line); err != nil {
		t.Fatal(err)
	}
	closeHomeRegistry(t, r)
	if _, err := Open(dir, Options{}); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("accepted a second first assignment: %v", err)
	}
}
