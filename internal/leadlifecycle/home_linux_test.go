package leadlifecycle

import (
	"testing"

	"ataniq/internal/leadregistry"
	"ataniq/internal/leadrole"
)

func TestProjectHomeFencesStartBeforePreflight(t *testing.T) {
	r := newRig(t)
	project := "sample"
	child, err := leadrole.Coordinator(project, "feature")
	noErr(t, err)
	roles := []leadrole.Role{mkRole(t, project), child}
	check := func(want error) {
		t.Helper()
		rev := r.revision()
		for _, role := range roles {
			_, err := r.start(role)
			isErr(t, err, want)
		}
		if r.revision() != rev || len(r.fake.Calls()) != 0 || r.pre.calls != 0 || r.holder.calls != 0 {
			t.Fatal("refused admission reached the journal, preflight, manager or holder")
		}
	}
	check(leadregistry.ErrHomeMissing)
	peer := newRig(t)
	noErr(t, r.reg.SetProjectAssignment(project, peer.authority, leadregistry.ProjectVersion{}, true))
	check(leadregistry.ErrOtherHome)
}

func TestStartRechecksAssignmentAfterInspect(t *testing.T) {
	r := newRig(t)
	role := r.define("alpha")
	v, err := r.reg.CheckProjectHome(role, nil)
	noErr(t, err)
	r.aud.afterInspect = func(string) {
		noErr(t, r.reg.SetProjectAssignment(role.Project(), r.authority, v, false))
	}
	_, err = r.start(role)
	isErr(t, err, leadregistry.ErrStaleRevision)
	e := r.get(role)
	if e.Intent != nil || e.Desired != leadregistry.DesiredStopped || len(r.fake.StartedSpecs()) != 0 {
		t.Fatalf("stale start: %+v", e)
	}
}
