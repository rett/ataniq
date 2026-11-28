package leadregistry

import (
	"errors"
	"fmt"
	"maps"
	"math"
	"path/filepath"
	"slices"
	"strings"

	"ataniq/internal/instanceid"
	"ataniq/internal/leadrole"
)

// EnrollmentOptions holds the owner acknowledgements for offline enrollment.
type EnrollmentOptions struct {
	ConfirmSelf bool
	Replace     bool
}

// Enrollment identifies the requested authority and any foreign home in the
// project history that the operator must acknowledge before claiming it.
type Enrollment struct {
	Authority        string
	PriorForeignHome string
}

// EnrollProjectHome records or confirms the initial assignment under the same
// exclusive registry lock as the daemon.
func EnrollProjectHome(state, home, project string, opts EnrollmentOptions) (result Enrollment, retErr error) {
	if home != "self" && !validAuthority(home) {
		return result, ErrInvalidHome
	}
	id, err := instanceid.Load(state)
	if err != nil {
		return result, err
	}
	authority := home
	if authority == "self" {
		authority = id.Authority
	}
	r, err := Open(filepath.Join(state, DirName), Options{})
	if err != nil {
		return result, err
	}
	defer func() { retErr = errors.Join(retErr, r.Close()) }()
	if id.Binding != r.binding {
		return result, ErrForeignInstance
	}
	result.Authority = authority
	if authority == id.Authority {
		result.PriorForeignHome = r.foreignHomes[project]
		if result.PriorForeignHome != "" && !opts.ConfirmSelf {
			return result, errors.New("verify the home on both instances and supply --confirm-self")
		}
	}
	row, err := r.ProjectHome(project)
	switch {
	case errors.Is(err, ErrHomeMissing):
		if opts.Replace {
			return result, err
		}
		if err := r.SetProjectAssignment(project, authority, ProjectVersion{}, true); err != nil {
			return result, err
		}
	case err != nil:
		return result, err
	case row.HomeAuthority != authority && opts.Replace:
		role, err := leadrole.Lead(project)
		if err != nil {
			return result, err
		}
		return result, r.replaceHome(role, row, authority)
	case row.HomeAuthority != authority || row.Holder == nil:
		return result, fmt.Errorf("project already enrolled with a different assignment: home_authority=%s home_generation=%d revision=%d", row.HomeAuthority, row.HomeGeneration, row.Revision)
	}
	return result, nil
}

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
	next := &ProjectAssignment{HomeAuthority: home, ProjectVersion: ProjectVersion{HomeGeneration: max(1, cur.HomeGeneration), Revision: cur.Revision + 1}}
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
	return r.writeAssignment(kindProjectHome, role, next, next)
}

// replaceHome runs only on the private, exclusively locked offline registry.
func (r *Registry) replaceHome(role leadrole.Role, old ProjectAssignment, authority string) error {
	p := &replaceHomePayload{PreviousHome: old.HomeAuthority, ProjectAssignment: ProjectAssignment{
		ProjectVersion: ProjectVersion{HomeGeneration: old.HomeGeneration + 1, Revision: old.Revision + 1},
		HomeAuthority:  authority, Holder: &LeadRef{Authority: authority, Role: role}, ExecutingInstance: authority,
	}}
	if err := r.validateReplacement(role, p); err != nil {
		return err
	}
	return r.writeAssignment(kindReplaceHome, role, p, &p.ProjectAssignment)
}

func (r *Registry) validateReplacement(role leadrole.Role, p *replaceHomePayload) error {
	old, ok := r.homes[role.Project()]
	if !ok || p.PreviousHome != old.HomeAuthority || p.HomeAuthority == old.HomeAuthority || p.Holder == nil {
		return invalid("home correction must replace the recorded home with a different assigned home")
	}
	if old.HomeGeneration == math.MaxUint64 || p.HomeGeneration != old.HomeGeneration+1 {
		return ErrStaleGeneration
	}
	if old.Revision == math.MaxUint64 || p.Revision != old.Revision+1 {
		return ErrStaleRevision
	}
	for defined := range r.roles {
		if defined.Project() == role.Project() {
			return fmt.Errorf("%w: %s is defined", ErrAssignmentBusy, defined)
		}
	}
	return nil
}

func (r *Registry) writeAssignment(kind string, role leadrole.Role, body payload, next *ProjectAssignment) error {
	rec, line, sum, err := r.prepare(kind, role, body)
	if err != nil {
		return err
	}
	if err := r.commit(rec, line, sum); err != nil {
		return err
	}
	r.recordAssignment(role.Project(), next)
	return nil
}

// A foreign home is the v1 registry designation for an executor-only project.
func (r *Registry) recordAssignment(project string, next *ProjectAssignment) {
	r.homes[project] = *next
	if next.HomeAuthority != r.binding.Authority {
		if r.foreignHomes == nil {
			r.foreignHomes = make(map[string]string)
		}
		r.foreignHomes[project] = next.HomeAuthority
	}
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

// MissingHomeWarnings names each unenrolled project and its blocked roles.
func (r *Registry) MissingHomeWarnings(configured []leadrole.Role) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	byProject := make(map[string][]string)
	for _, role := range append(slices.Collect(maps.Keys(r.roles)), configured...) {
		project := role.Project()
		if _, enrolled := r.homes[project]; project != "" && !enrolled {
			byProject[project] = append(byProject[project], role.String())
		}
	}
	var warnings []string
	for _, project := range slices.Sorted(maps.Keys(byProject)) {
		roles := byProject[project]
		slices.Sort(roles)
		warnings = append(warnings, fmt.Sprintf("project %s has no home, blocking define and start for %s", project, strings.Join(slices.Compact(roles), ", ")))
	}
	return warnings
}
