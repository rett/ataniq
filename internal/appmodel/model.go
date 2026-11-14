// Package appmodel defines pure application command contracts and codecs.
// Values in this package are not proof of authentication, authorization,
// durable admission, or provider execution. Use Decode, Encode and Bind at
// composition boundaries; encoding/json alone is not this wire contract.
package appmodel

import "unicode/utf8"

// Limits are engineering defaults, in bytes except for depth and token count.
const (
	SchemaVersion   = 1
	MaxWireBytes    = 1 << 20
	MaxTextBytes    = 64 << 10
	MaxBindingBytes = 128 << 10
	MaxIDBytes      = 128
	MaxDepth        = 16    // simultaneously open JSON objects/arrays, including the root
	MaxTokens       = 16384 // keys, scalar values, and opening/closing containers
)

type CommandID string
type Revision string

type ProjectRef struct{ Authority, ID string }
type ConversationRef struct{ Authority, ID string }

// CommandKey includes the original enrolled client, also when used as a target.
// It is distinct from an internal effect ID and any adapter RPC request ID.
type CommandKey struct {
	Authority        string
	EnrolledClientID string
	CommandID        CommandID
}

type EnqueuePayload struct{ Text string }

// Operation is closed to the two value variants below. Pointers and nil are
// rejected by validation, even though Go's method sets admit pointer values.
type Operation interface{ applicationOperation() }
type Enqueue struct {
	Target  ConversationRef
	Payload EnqueuePayload
}
type Cancel struct{ Target CommandKey }

func (Enqueue) applicationOperation() {}
func (Cancel) applicationOperation()  {}

type Envelope struct {
	SchemaVersion    int
	CommandID        CommandID
	Project          ProjectRef
	ExpectedRevision Revision
	Operation        Operation
}

// CompositionIdentity supplies server-derived facts separately from client
// JSON. Constructing it performs no authentication or enrollment decision.
type CompositionIdentity struct{ Authority, EnrolledClientID string }

// Code values are stable application-domain outcomes, independent of transport.
type Code string

const (
	InvalidInput          Code = "invalid_input"
	UnsupportedVersion    Code = "unsupported_version"
	UnsupportedCapability Code = "unsupported_capability"
	Unauthenticated       Code = "unauthenticated"
	PermissionDenied      Code = "permission_denied"
	HiddenOrAbsent        Code = "hidden_or_absent"
	BindingConflict       Code = "binding_conflict"
	RevisionConflict      Code = "revision_conflict"
	QueueHeld             Code = "queue_held"
	CancelTooLate         Code = "cancel_too_late"
	AlreadyCancelled      Code = "already_cancelled"
	StorageUnknown        Code = "storage_unknown"
	ResetRequired         Code = "reset_required"
	CapacityExceeded      Code = "capacity_exceeded"
)

// DomainError carries a code, never input text, credentials or hidden records.
// Current revision/status disclosure is a separate service authorization step.
type DomainError struct{ Code Code }

func (e DomainError) Error() string { return string(e.Code) }
func invalid() error                { return DomainError{Code: InvalidInput} }

func validID(s string) bool {
	if len(s) == 0 || len(s) > MaxIDBytes {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '.' || c == '_' || c == ':' || c == '-') {
			return false
		}
	}
	return true
}

// Validate rejects invalid typed values as well as unsupported versions.
func (e Envelope) Validate() error {
	if e.SchemaVersion != SchemaVersion {
		return DomainError{Code: UnsupportedVersion}
	}
	for _, s := range []string{string(e.CommandID), e.Project.Authority, e.Project.ID, string(e.ExpectedRevision)} {
		if !validID(s) {
			return invalid()
		}
	}
	switch op := e.Operation.(type) {
	case Enqueue:
		if !validID(op.Target.Authority) || !validID(op.Target.ID) || len(op.Payload.Text) > MaxTextBytes || !utf8.ValidString(op.Payload.Text) {
			return invalid()
		}
	case Cancel:
		if !validID(op.Target.Authority) || !validID(op.Target.EnrolledClientID) || !validID(string(op.Target.CommandID)) {
			return invalid()
		}
	default:
		return invalid()
	}
	return nil
}

// AdmissionReceipt is immutable recorded data, separate from CurrentStatus.
// Only a future service's confirmed transaction can establish its provenance.
// AdmittedRevision identifies the addressed mutation resource immediately after
// confirmed admission: the conversation for enqueue, the target command for cancel.
// Cursor is a historical admission position and may expire or require reset.
// This value type itself asserts no commit or authorization.
type AdmissionReceipt struct {
	Command          CommandKey
	Digest           [32]byte
	AdmittedRevision Revision
	Cursor           string
}

type State string

const (
	Queued      State = "queued"
	Held        State = "held"
	Cancelled   State = "cancelled"
	Applied     State = "applied"
)

// CurrentStatus is a revisioned projection, never part of immutable binding.
type CurrentStatus struct {
	Command  CommandKey
	Revision Revision
	State    State
}
