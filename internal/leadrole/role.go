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
)

// String returns "lead", "supervisor", or "invalid" for any other value.
func (k Kind) String() string {
	switch k {
	case KindLead:
		return "lead"
	case KindSupervisor:
		return "supervisor"
	}
	return "invalid"
}

// Role identifies a project lead or the supervisor. Roles are comparable and
// can be used as map keys. The zero Role is invalid: its String is empty and
// it cannot be marshaled. Obtain a Role from Lead, Supervisor or Parse.
type Role struct {
	kind    Kind
	project string
}

// ValidProject reports whether name is a valid project name: 1 to 63
// characters from a-z, 0-9 and hyphen, starting and ending with a letter or
// digit.
func ValidProject(name string) bool {
	return checkProject(name) == nil
}

func checkProject(name string) error {
	if name == "" {
		return fmt.Errorf("%w: empty project name", ErrInvalid)
	}
	if len(name) > MaxProjectLen {
		return fmt.Errorf("%w: project name longer than %d bytes", ErrInvalid, MaxProjectLen)
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case c == '-':
			if i == 0 || i == len(name)-1 {
				return fmt.Errorf("%w: project name starts or ends with a hyphen", ErrInvalid)
			}
		default:
			return fmt.Errorf("%w: project name has a character outside a-z, 0-9 and hyphen", ErrInvalid)
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

// Parse reads the canonical string of a role: "supervisor" or
// "<project>/lead". It accepts nothing else: no spaces, no upper case.
func Parse(s string) (Role, error) {
	if s == supervisorName {
		return Supervisor(), nil
	}
	project, ok := strings.CutSuffix(s, leadSuffix)
	if !ok {
		return Role{}, fmt.Errorf("%w: want \"supervisor\" or \"<project>/lead\"", ErrInvalid)
	}
	return Lead(project)
}

// IsZero reports whether r is the zero, invalid Role.
func (r Role) IsZero() bool { return r.kind == 0 }

// Kind returns the kind of the role, or 0 for the zero Role.
func (r Role) Kind() Kind { return r.kind }

// Project returns the project of a project lead, and "" for the supervisor
// and the zero Role.
func (r Role) Project() string { return r.project }

// String returns the canonical string: "<project>/lead" or "supervisor". The
// zero Role gives "".
func (r Role) String() string {
	switch r.kind {
	case KindLead:
		return r.project + leadSuffix
	case KindSupervisor:
		return supervisorName
	}
	return ""
}

// UnitName returns the systemd unit name of the role:
// "ataniq-lead-<project>.service" or "ataniq-supervisor.service". The zero
// Role gives "".
func (r Role) UnitName() string {
	switch r.kind {
	case KindLead:
		return "ataniq-lead-" + r.project + ".service"
	case KindSupervisor:
		return "ataniq-supervisor.service"
	}
	return ""
}

// LockPath returns the path of the role's lock file under stateDir:
// "<stateDir>/roles/<project>/lead.lock" or "<stateDir>/roles/supervisor.lock".
// It only builds the path. The zero Role gives "".
func (r Role) LockPath(stateDir string) string {
	switch r.kind {
	case KindLead:
		return filepath.Join(stateDir, "roles", r.project, "lead.lock")
	case KindSupervisor:
		return filepath.Join(stateDir, "roles", "supervisor.lock")
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
