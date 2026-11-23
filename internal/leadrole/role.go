package leadrole

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// MaxProjectLen is the longest accepted project name, in bytes.
const MaxProjectLen = 63

// supervisorName is the canonical string of the supervisor role.
const supervisorName = "supervisor"

// leadSuffix follows the project in the canonical string of a project lead.
const leadSuffix = "/lead"

// coordSegment is the middle segment of the canonical string of a
// coordinator: "<project>/coord/<feature>".
const coordSegment = "coord"

// MaxFeatureLen is the longest accepted feature name, in bytes. A feature
// follows the grammar of a project name.
const MaxFeatureLen = MaxProjectLen

// ErrInvalid is wrapped by every error that rejects a project name or a role
// string.
var ErrInvalid = errors.New("invalid lead role")

// Kind says which kind of lead a Role is. The zero Kind is not a role.
type Kind uint8

const (
	// KindLead is the lead of one project.
	KindLead Kind = 1
	// KindSupervisor is the single supervisor.
	KindSupervisor Kind = 2
	// KindCoordinator is the coordinator of one feature of one project.
	KindCoordinator Kind = 3
)

// String returns "lead", "supervisor" or "coordinator", or "invalid" for any
// other value.
func (k Kind) String() string {
	switch k {
	case KindLead:
		return "lead"
	case KindSupervisor:
		return "supervisor"
	case KindCoordinator:
		return "coordinator"
	}
	return "invalid"
}

// Role identifies a project lead, the supervisor or the coordinator of one
// feature. Roles are comparable and can be used as map keys. The zero Role is
// invalid: its String is empty and it cannot be marshaled. Obtain a Role from
// Lead, Supervisor, Coordinator or Parse.
type Role struct {
	kind    Kind
	project string
	feature string // coordinators only
}

// ValidProject reports whether name is a valid project name: 1 to 63
// characters from a-z, 0-9 and hyphen, starting and ending with a letter or
// digit.
func ValidProject(name string) bool {
	return checkProject(name) == nil
}

// ValidFeature reports whether name is a valid feature name. The grammar is
// the project's: 1 to 63 characters from a-z, 0-9 and hyphen, starting and
// ending with a letter or digit.
func ValidFeature(name string) bool {
	return checkName("feature", name) == nil
}

func checkProject(name string) error { return checkName("project", name) }

// checkName checks a project or feature name; what names it in the errors.
func checkName(what, name string) error {
	if name == "" {
		return fmt.Errorf("%w: empty %s name", ErrInvalid, what)
	}
	if len(name) > MaxProjectLen {
		return fmt.Errorf("%w: %s name longer than %d bytes", ErrInvalid, what, MaxProjectLen)
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case c == '-':
			if i == 0 || i == len(name)-1 {
				return fmt.Errorf("%w: %s name starts or ends with a hyphen", ErrInvalid, what)
			}
		default:
			return fmt.Errorf("%w: %s name has a character outside a-z, 0-9 and hyphen", ErrInvalid, what)
		}
	}
	return nil
}

// Lead returns the lead role of a project. It fails when the project name is
// not valid.
func Lead(project string) (Role, error) {
	if err := checkProject(project); err != nil {
		return Role{}, err
	}
	return Role{kind: KindLead, project: project}, nil
}

// Supervisor returns the supervisor role.
func Supervisor() Role {
	return Role{kind: KindSupervisor}
}

// Coordinator returns the coordinator role of one feature of a project. It
// fails when the project or the feature name is not valid.
func Coordinator(project, feature string) (Role, error) {
	if err := checkProject(project); err != nil {
		return Role{}, err
	}
	if err := checkName("feature", feature); err != nil {
		return Role{}, err
	}
	return Role{kind: KindCoordinator, project: project, feature: feature}, nil
}

// Parse reads the canonical string of a role: "supervisor",
// "<project>/lead" or "<project>/coord/<feature>". It accepts nothing else:
// no spaces, no upper case.
func Parse(s string) (Role, error) {
	if s == supervisorName {
		return Supervisor(), nil
	}
	if parts := strings.Split(s, "/"); len(parts) == 3 && parts[1] == coordSegment {
		return Coordinator(parts[0], parts[2])
	}
	project, ok := strings.CutSuffix(s, leadSuffix)
	if !ok {
		return Role{}, fmt.Errorf("%w: want \"supervisor\", \"<project>/lead\" or \"<project>/coord/<feature>\"", ErrInvalid)
	}
	return Lead(project)
}

// IsZero reports whether r is the zero, invalid Role.
func (r Role) IsZero() bool { return r.kind == 0 }

// Kind returns the kind of the role, or 0 for the zero Role.
func (r Role) Kind() Kind { return r.kind }

// Project returns the project of a project lead or a coordinator, and "" for
// the supervisor and the zero Role.
func (r Role) Project() string { return r.project }

// Feature returns the feature of a coordinator, and "" for every other role.
func (r Role) Feature() string { return r.feature }

// Parent returns the role a coordinator lives under: the lead of its project.
// Every other role, the zero Role included, has no parent and gives the zero
// Role.
func (r Role) Parent() Role {
	if r.kind != KindCoordinator {
		return Role{}
	}
	return Role{kind: KindLead, project: r.project}
}

// String returns the canonical string: "<project>/lead", "supervisor" or
// "<project>/coord/<feature>". The zero Role gives "".
func (r Role) String() string {
	switch r.kind {
	case KindLead:
		return r.project + leadSuffix
	case KindSupervisor:
		return supervisorName
	case KindCoordinator:
		return r.project + "/" + coordSegment + "/" + r.feature
	}
	return ""
}

// UnitName returns the systemd unit name of the role:
// "ataniq-lead-<project>.service", "ataniq-supervisor.service" or
// "ataniq-coord-<project>_<feature>.service". An underscore cannot occur in a
// project or a feature, so no two roles share a unit name. The zero Role gives
// "".
func (r Role) UnitName() string {
	switch r.kind {
	case KindLead:
		return "ataniq-lead-" + r.project + ".service"
	case KindSupervisor:
		return "ataniq-supervisor.service"
	case KindCoordinator:
		return "ataniq-coord-" + r.project + "_" + r.feature + ".service"
	}
	return ""
}

// LockPath returns the path of the role's lock file under stateDir:
// "<stateDir>/roles/<project>/lead.lock", "<stateDir>/roles/supervisor.lock" or
// "<stateDir>/roles/<project>/coord-<feature>.lock". It only builds the path.
// The zero Role gives "".
func (r Role) LockPath(stateDir string) string {
	switch r.kind {
	case KindLead:
		return filepath.Join(stateDir, "roles", r.project, "lead.lock")
	case KindSupervisor:
		return filepath.Join(stateDir, "roles", "supervisor.lock")
	case KindCoordinator:
		return filepath.Join(stateDir, "roles", r.project, "coord-"+r.feature+".lock")
	}
	return ""
}

// MarshalJSON writes the canonical string as a JSON string. The zero Role
// cannot be marshaled.
func (r Role) MarshalJSON() ([]byte, error) {
	if r.IsZero() {
		return nil, fmt.Errorf("%w: zero role", ErrInvalid)
	}
	return json.Marshal(r.String())
}

// UnmarshalJSON reads a JSON string holding the canonical string. A JSON null
// and any other type are rejected.
func (r *Role) UnmarshalJSON(data []byte) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return fmt.Errorf("%w: null", ErrInvalid)
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return fmt.Errorf("%w: not a JSON string", ErrInvalid)
	}
	parsed, err := Parse(s)
	if err != nil {
		return err
	}
	*r = parsed
	return nil
}

// ThrowawayPrefix begins the name of a project that exists only for a canary
// slice run: its state, its journal and its lead are meant to be deleted.
//
// ValidProject still accepts such a name, because the grammar of a project
// name is not changed. The prefix is reserved by the callers, not by the
// grammar: a daemon that defines real leads must refuse a project with this
// prefix, and only canary mode (a build with the ataniq_canary tag) accepts
// it, and accepts nothing else. ScratchPrefix is reserved in the same way.
const ThrowawayPrefix = "throwaway-"

// IsThrowaway reports whether project is a valid project name that starts with
// ThrowawayPrefix and has at least one character after it. The prefix alone,
// a name that is not valid and a name with the prefix in the middle are not
// throwaway.
func IsThrowaway(project string) bool {
	rest, ok := strings.CutPrefix(project, ThrowawayPrefix)
	return ok && rest != "" && ValidProject(project)
}

// ScratchPrefix begins the name of a project that is a scratch lead: a lead
// that runs in a scratch place (one state root and one runtime root that only
// the owner's tool makes) next to others of its kind. Like ThrowawayPrefix, it
// is reserved by the callers, not by the grammar: ValidProject still accepts
// such a name, a daemon that defines real leads must refuse a project with
// this prefix, and only a build with the ataniq_canary tag accepts it.
const ScratchPrefix = "scratch-"

// IsScratch reports whether project is a valid project name that starts with
// ScratchPrefix and has at least one character after it. The prefix alone, a
// name that is not valid and a name with the prefix in the middle are not
// scratch.
func IsScratch(project string) bool {
	rest, ok := strings.CutPrefix(project, ScratchPrefix)
	return ok && rest != "" && ValidProject(project)
}

// Reserved is a kind of reserved project prefix. The zero value is
// ReservedThrowaway, the first kind, so a value that was never set means the
// canary's.
type Reserved int

// The kinds of reserved prefix.
const (
	// ReservedThrowaway is the kind of ThrowawayPrefix.
	ReservedThrowaway Reserved = iota
	// ReservedScratch is the kind of ScratchPrefix.
	ReservedScratch
)

// String returns "throwaway" or "scratch", and "reserved(N)" for a value that
// is not a kind.
func (k Reserved) String() string {
	switch k {
	case ReservedThrowaway:
		return "throwaway"
	case ReservedScratch:
		return "scratch"
	}
	return fmt.Sprintf("reserved(%d)", int(k))
}

// IsReserved reports whether project carries the reserved prefix of kind k
// (IsThrowaway or IsScratch). A value that is not a kind never matches.
func IsReserved(project string, k Reserved) bool {
	switch k {
	case ReservedThrowaway:
		return IsThrowaway(project)
	case ReservedScratch:
		return IsScratch(project)
	}
	return false
}

// ReservedOf returns the kind of reserved prefix project carries, and false
// when it carries neither. The two prefixes do not overlap, so there is at most
// one kind.
func ReservedOf(project string) (Reserved, bool) {
	switch {
	case IsThrowaway(project):
		return ReservedThrowaway, true
	case IsScratch(project):
		return ReservedScratch, true
	}
	return 0, false
}
