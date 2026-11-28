package leadregistry

import (
	"errors"

	"ataniq/internal/leadrole"
)

// LeadRef qualifies a role with the instance that owns it.
type LeadRef struct {
	Authority string        `json:"authority"`
	Role      leadrole.Role `json:"role"`
}

// ProjectVersion is the compare-and-set token of one project's assignment.
// Its zero value expects an absent row.
type ProjectVersion struct {
	HomeGeneration uint64 `json:"home_generation"`
	Revision       uint64 `json:"revision"`
}

// recorded omits the token for the supervisor and historical v1 records.
func (v ProjectVersion) recorded() *ProjectVersion {
	if v == (ProjectVersion{}) {
		return nil
	}
	return &v
}

// ProjectAssignment describes the sole lead assignment for a project.
type ProjectAssignment struct {
	ProjectVersion
	HomeAuthority     string   `json:"home_authority"`
	Holder            *LeadRef `json:"holder"`
	ExecutingInstance string   `json:"executing_instance"`
}

type replaceHomePayload struct {
	PreviousHome string `json:"previous_home_authority"`
	ProjectAssignment
}

// ErrInvalidHome identifies an invalid operator-supplied home designation.
var ErrInvalidHome = errors.New("--home must be self or an authority ID of 64 lowercase hex digits")

// HomeRefusal is the stable code of a refused project admission or assignment write.
type HomeRefusal string

const (
	ErrHomeMissing     HomeRefusal = "project-home-unset"
	ErrOtherHome       HomeRefusal = "project-home-foreign"
	ErrStaleGeneration HomeRefusal = "stale-home-generation"
	ErrStaleRevision   HomeRefusal = "stale-assignment-revision"
	ErrUnassigned      HomeRefusal = "project-unassigned"
	ErrAssignmentBusy  HomeRefusal = "project-assignment-busy"
)

func (e HomeRefusal) Error() string {
	switch e {
	case ErrHomeMissing:
		return "the project has no home; with the daemon stopped, use ataniqd project-home to enroll it"
	case ErrOtherHome:
		return "another instance is this project's home"
	}
	return string(e)
}

func (v ProjectVersion) compare(expected ProjectVersion) error {
	if v.HomeGeneration != expected.HomeGeneration {
		return ErrStaleGeneration
	}
	if v.Revision != expected.Revision {
		return ErrStaleRevision
	}
	return nil
}

func validAuthority(id string) bool { return len(id) == hashHexLen && isLowerHex([]byte(id)) }

func (a *ProjectAssignment) validate() error {
	if !validAuthority(a.HomeAuthority) {
		return invalid("home_authority must be 64 lowercase hex digits")
	}
	if a.HomeGeneration == 0 || a.Revision == 0 {
		return invalid("home_generation and revision must be positive")
	}
	if a.Holder == nil {
		if a.ExecutingInstance != "" {
			return invalid("vacant assignment has an executing instance")
		}
	} else if a.Holder.Authority != a.HomeAuthority || a.ExecutingInstance != a.HomeAuthority {
		return invalid("only the home instance may hold and execute its lead")
	}
	return nil
}

func (a *ProjectAssignment) checkRole(role leadrole.Role) error {
	if role.Kind() != leadrole.KindLead || (a.Holder != nil && a.Holder.Role != role) {
		return invalid("assignment must name the project's lead")
	}
	return nil
}
