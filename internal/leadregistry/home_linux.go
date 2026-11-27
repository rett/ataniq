package leadregistry

import (
	"math"

	"ataniq/internal/instanceid"
	"ataniq/internal/leadrole"
)

// validateAssignment checks the same predecessor and local occupancy for
// replay and for live writes. V1 enrolls at generation 1. A transfer of home
// authority requires a separate re-homing protocol.
func (r *Registry) validateAssignment(role leadrole.Role, next *ProjectAssignment) error {
	if r.binding.Authority == "" {
		return instanceid.ErrMissing
	}
	cur, exists := r.homes[role.Project()]
	if exists {
		if cur.HomeAuthority != r.binding.Authority {
			return ErrOtherHome
		}
		if next.HomeAuthority != cur.HomeAuthority {
			return invalid("cannot change an enrolled project's home")
		}
		if cur.HomeGeneration != next.HomeGeneration {
			return ErrStaleGeneration
		}
	} else {
		if next.HomeGeneration != 1 {
			return ErrStaleGeneration
		}
		if len(r.homes) >= MaxRoles {
			return ErrTooManyRoles
		}
	}
	if cur.Revision == math.MaxUint64 || next.Revision != cur.Revision+1 {
		return ErrStaleRevision
	}
	if next.HomeAuthority != r.binding.Authority || next.Holder == nil {
		for _, e := range r.roles {
			if e.Role.Project() == role.Project() && (e.pending() || (e.Shown.State != StateStopped && e.Shown.State != StateEnded)) {
				return ErrAssignmentBusy
			}
		}
	}
	return nil
}

// SetProjectAssignment enrolls a project or changes whether its home lead is
// assigned. The first writer compares against the zero version. A foreign
// home's row is a local record of the designation and cannot be updated here.
func (r *Registry) SetProjectAssignment(project, home string, expected ProjectVersion, assigned bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.usable(); err != nil {
		return err
	}
	role, err := leadrole.Lead(project)
	if err != nil {
		return err
	}
	cur := r.homes[project]
	if err := cur.compare(expected); err != nil {
		return err
	}
	next := &ProjectAssignment{HomeAuthority: home, ProjectVersion: ProjectVersion{HomeGeneration: 1, Revision: cur.Revision + 1}}
	if assigned {
		next.Holder = &LeadRef{Authority: home, Role: role}
		next.ExecutingInstance = home
	}
	if err := next.validate(); err != nil {
		return err
	}
	if err := r.validateAssignment(role, next); err != nil {
		return err
	}
	rec, line, sum, err := r.prepare(kindProjectHome, role, next)
	if err != nil {
		return err
	}
	if err := r.commit(rec, line, sum); err != nil {
		return err
	}
	r.homes[project] = *next
	return nil
}

// ProjectHome returns a snapshot independent of future assignment writes.
func (r *Registry) ProjectHome(project string) (ProjectAssignment, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.usable(); err != nil {
		return ProjectAssignment{}, err
	}
	a, ok := r.homes[project]
	if !ok {
		return ProjectAssignment{}, ErrHomeMissing
	}
	if a.Holder != nil {
		h := *a.Holder
		a.Holder = &h
	}
	return a, nil
}

// CheckProjectHome admits a local operation and returns the version it saw.
// Project admissions require explicit enrollment. Standalone v1 fixture
// writers are outside the instance registry and cannot enroll a project.
func (r *Registry) CheckProjectHome(role leadrole.Role, expected *ProjectVersion) (ProjectVersion, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.checkProjectHome(role, expected)
}

func (r *Registry) checkProjectHome(role leadrole.Role, expected *ProjectVersion) (ProjectVersion, error) {
	var zero ProjectVersion
	if err := r.usable(); err != nil {
		return zero, err
	}
	if role.Project() == "" || r.standaloneV1 {
		if expected != nil {
			return zero, zero.compare(*expected)
		}
		return zero, nil
	}
	a, ok := r.homes[role.Project()]
	if !ok {
		return zero, ErrHomeMissing
	}
	if expected != nil {
		if err := a.compare(*expected); err != nil {
			return zero, err
		}
	}
	if a.HomeAuthority != r.binding.Authority {
		return zero, ErrOtherHome
	}
	if a.Holder == nil {
		return zero, ErrUnassigned
	}
	return a.ProjectVersion, nil
}

// Bound journals written before enrollment existed can contain admissions
// ahead of the project's first home row. After it, each admission names the
// precise assignment that was checked under the writer lock.
func (r *Registry) checkRecordedAdmission(rec record) error {
	var expected *ProjectVersion
	switch p := rec.payload.(type) {
	case *definePayload:
		expected = p.Assignment
	case *beginPayload:
		expected = p.Assignment
	default:
		return nil
	}
	_, enrolled := r.homes[rec.role.Project()]
	if !enrolled && expected == nil {
		return nil
	}
	if !enrolled || expected == nil {
		return invalid("admission does not match project enrollment")
	}
	_, err := r.checkProjectHome(rec.role, expected)
	return err
}
