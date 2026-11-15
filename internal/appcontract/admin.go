package appcontract

import (
	"crypto/sha256"
	"math"
	"unicode/utf8"

	"ataniq/internal/appmodel"
)

// Precondition is either Absent or Match. A zero value is not a valid variant.
type Precondition interface{ precondition() }
type Absent struct{}
type Match struct{ Revision string }

func (Absent) precondition() {}
func (Match) precondition()  {}

func preconditionBytes(p Precondition, absent, match bool) ([]byte, error) {
	switch v := p.(type) {
	case Absent:
		if absent {
			return []byte{0}, nil
		}
	case Match:
		if match {
			r, err := rawRevision(v.Revision)
			if err != nil {
				return nil, err
			}
			return append([]byte{1}, r...), nil
		}
	}
	return nil, errInvalid
}

type AdminOperation interface{ adminOperation() }
type ProjectCreate struct {
	Project, Name string
	Expected      Precondition
}
type LeadCreate struct {
	Project, Role, Name string
	Expected            Precondition
}
type ConversationCreate struct {
	Project, Conversation, Role string
	Expected                    Precondition
}
type AdministrationScope uint8

const (
	None AdministrationScope = iota + 1
	OwnerAdministration
)

type ArtifactRef struct {
	ID             string
	ManifestDigest [32]byte
}
type CredentialBinding struct {
	Client, Credential                 string
	Generation, NotBeforeMS, ExpiresMS uint64
	Artifact                           ArtifactRef
}
type ClientEnroll struct {
	CredentialBinding
	Scope    AdministrationScope
	Expected Precondition
}
type CredentialRotate struct {
	CredentialBinding
	Expected Precondition
}
type ClientRevoke struct {
	Client   string
	Expected Precondition
}
type PermissionsReplace struct {
	Project, Client string
	Mask            uint8
	Expected        Precondition
}
type DispositionMode string

const (
	Release          DispositionMode = "release"
	CancelAndResolve DispositionMode = "cancel_and_resolve"
)

type DispositionCommand struct {
	Project                         string
	Target                          appmodel.CommandKey
	Expected                        Precondition
	Mode                            DispositionMode
	Reason                          string
	EnrollmentPrefix, EnqueuePrefix *uint64
}

func (ProjectCreate) adminOperation()      {}
func (LeadCreate) adminOperation()         {}
func (ConversationCreate) adminOperation() {}
func (ClientEnroll) adminOperation()       {}
func (CredentialRotate) adminOperation()   {}
func (ClientRevoke) adminOperation()       {}
func (PermissionsReplace) adminOperation() {}
func (DispositionCommand) adminOperation() {}

func textBound(s string, limit int) bool { return len(s) <= limit && utf8.ValidString(s) }
func credentialFields(c CredentialBinding) ([][]byte, error) {
	if !validID(c.Client) || !validID(c.Credential) || !validID(c.Artifact.ID) || c.Generation == 0 || c.Generation > math.MaxInt64 || c.NotBeforeMS > math.MaxInt64 || c.ExpiresMS > math.MaxInt64 || c.NotBeforeMS >= c.ExpiresMS {
		return nil, errInvalid
	}
	return [][]byte{[]byte(c.Credential), number(c.Generation), number(c.NotBeforeMS), number(c.ExpiresMS), []byte(c.Artifact.ID), c.Artifact.ManifestDigest[:]}, nil
}

// AdminBinding owns the complete canonical representation of an operation.
type AdminBinding struct {
	canonical string
	digest    [32]byte
}

func (b AdminBinding) Bytes() []byte    { return []byte(b.canonical) }
func (b AdminBinding) Digest() [32]byte { return b.digest }
func (b AdminBinding) Equal(other AdminBinding) bool {
	return b.canonical != "" && other.canonical != "" && b.canonical == other.canonical
}

// BindAdmin binds a validated administration operation.
func BindAdmin(i Instance, actor, command string, op AdminOperation) (AdminBinding, error) {
	var empty AdminBinding
	if !validID(i.Authority) || !validID(actor) || !validID(command) {
		return empty, errInvalid
	}
	var name string
	var fields [][]byte
	var p Precondition
	var allowAbsent, allowMatch bool
	switch v := op.(type) {
	case ProjectCreate:
		if !validID(v.Project) || !textBound(v.Name, 256) {
			return empty, errInvalid
		}
		name = "project.create"
		fields = [][]byte{[]byte(v.Project), []byte(v.Name)}
		p = v.Expected
		allowAbsent = true
	case LeadCreate:
		if !validID(v.Project) || !validID(v.Role) || !textBound(v.Name, 256) {
			return empty, errInvalid
		}
		name = "lead.create"
		fields = [][]byte{[]byte(v.Project), []byte(v.Role), []byte(v.Name)}
		p = v.Expected
		allowAbsent = true
	case ConversationCreate:
		if !validID(v.Project) || !validID(v.Conversation) || !validID(v.Role) {
			return empty, errInvalid
		}
		name = "conversation.create"
		fields = [][]byte{[]byte(v.Project), []byte(v.Conversation), []byte(v.Role)}
		p = v.Expected
		allowAbsent = true
	case ClientEnroll:
		c, err := credentialFields(v.CredentialBinding)
		if err != nil || (v.Scope != None && v.Scope != OwnerAdministration) || v.Generation != 1 {
			return empty, errInvalid
		}
		name = "client.enroll"
		fields = append([][]byte{[]byte(v.Client), byteField(byte(v.Scope - 1))}, c...)
		p = v.Expected
		allowAbsent = true
	case CredentialRotate:
		c, err := credentialFields(v.CredentialBinding)
		if err != nil {
			return empty, errInvalid
		}
		name = "credential.rotate"
		fields = append([][]byte{[]byte(v.Client)}, c...)
		p = v.Expected
		allowMatch = true
	case ClientRevoke:
		if !validID(v.Client) {
			return empty, errInvalid
		}
		name = "client.revoke"
		fields = [][]byte{[]byte(v.Client)}
		p = v.Expected
		allowMatch = true
	case PermissionsReplace:
		if !validID(v.Project) || !validID(v.Client) || v.Mask > 15 {
			return empty, errInvalid
		}
		name = "project.permissions.replace"
		fields = [][]byte{[]byte(v.Project), []byte(v.Client), byteField(v.Mask)}
		p = v.Expected
		allowAbsent = true
		allowMatch = true
	case DispositionCommand:
		// Callers must synchronize concurrent mutation of inputs.
		if v.EnrollmentPrefix != nil {
			n := *v.EnrollmentPrefix
			v.EnrollmentPrefix = &n
		}
		if v.EnqueuePrefix != nil {
			n := *v.EnqueuePrefix
			v.EnqueuePrefix = &n
		}
		if !validID(v.Project) || v.Target.Authority != i.Authority || !validID(v.Target.EnrolledClientID) || !validID(string(v.Target.CommandID)) || (v.Mode != Release && v.Mode != CancelAndResolve) || !textBound(v.Reason, 1024) || (v.EnrollmentPrefix != nil && *v.EnrollmentPrefix > math.MaxInt64) || (v.EnqueuePrefix != nil && *v.EnqueuePrefix > math.MaxInt64) {
			return empty, errInvalid
		}
		name = "item.disposition"
		p = v.Expected
		allowMatch = true
		pre, err := preconditionBytes(p, false, true)
		if err != nil {
			return empty, err
		}
		fields = [][]byte{[]byte(v.Project), []byte(v.Target.Authority), []byte(v.Target.EnrolledClientID), []byte(v.Target.CommandID), pre, []byte(v.Mode), []byte(v.Reason), optionalNumber(v.EnrollmentPrefix), optionalNumber(v.EnqueuePrefix)}
		p = nil
	default:
		return empty, errInvalid
	}
	if p != nil {
		pre, err := preconditionBytes(p, allowAbsent, allowMatch)
		if err != nil {
			return empty, err
		}
		fields = append(fields, pre)
	} else if name != "item.disposition" {
		return empty, errInvalid
	}
	f := append(i.fields(), []byte(actor), []byte(command), []byte(name))
	f = append(f, fields...)
	b := frame("ADMIN", f...)
	if len(b) > 16<<10 {
		return empty, errInvalid
	}
	return AdminBinding{string(b), sha256.Sum256(b)}, nil
}
