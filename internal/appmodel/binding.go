package appmodel

import (
	"crypto/sha256"
	"encoding/binary"
)

const bindingHeader = "ATANIQ-COMMAND\x00\x01"

// Binding owns exact canonical bytes. The zero value is not a valid binding.
// Credentials, current policy, effect IDs and RPC IDs have no place here.
type Binding struct {
	canonical string
	digest    [32]byte
	key       CommandKey
}

// Bytes returns an independent copy; Digest and Key return value copies.
func (b Binding) Bytes() []byte    { return []byte(b.canonical) }
func (b Binding) Digest() [32]byte { return b.digest }
func (b Binding) Key() CommandKey  { return b.key }

// Equal compares complete canonical bytes, not just a hash. Invalid zero values
// are never equal bindings.
func (b Binding) Equal(other Binding) bool {
	return b.canonical != "" && b.canonical == other.canonical
}

// Bind validates both the command and trusted composition identity. Identity
// syntax validation supplies no authentication, current policy or commit proof.
func Bind(identity CompositionIdentity, e Envelope) (Binding, error) {
	if err := e.Validate(); err != nil {
		return Binding{}, err
	}
	if !validID(identity.Authority) || !validID(identity.EnrolledClientID) {
		return Binding{}, invalid()
	}
	var targetAuthority, targetKind, targetClient, targetID, operation string
	var payload []string
	switch op := e.Operation.(type) {
	case Enqueue:
		targetAuthority, targetKind, targetID = op.Target.Authority, "conversation", op.Target.ID
		operation, payload = "input.enqueue", []string{op.Payload.Text}
	case Cancel:
		targetAuthority, targetKind, targetClient, targetID = op.Target.Authority, "command", op.Target.EnrolledClientID, string(op.Target.CommandID)
		operation = "queue.cancel"
	}
	fields := []string{identity.Authority, identity.EnrolledClientID, string(e.CommandID), e.Project.Authority, e.Project.ID, targetAuthority, targetKind, targetClient, targetID, operation, string(e.ExpectedRevision)}
	fields = append(fields, payload...)
	canonical, err := canonicalFields(fields)
	if err != nil {
		return Binding{}, err
	}
	return Binding{canonical: string(canonical), digest: sha256.Sum256(canonical), key: CommandKey{identity.Authority, identity.EnrolledClientID, e.CommandID}}, nil
}

func canonicalFields(fields []string) ([]byte, error) {
	size := len(bindingHeader)
	for _, field := range fields {
		// Subtraction before addition keeps arithmetic and the u32 conversion
		// safe even for invalid, extremely large typed inputs.
		if size > MaxBindingBytes-4 || len(field) > MaxBindingBytes-size-4 {
			return nil, invalid()
		}
		size += 4 + len(field)
	}
	out := make([]byte, 0, size)
	out = append(out, bindingHeader...)
	for _, field := range fields {
		out = binary.BigEndian.AppendUint32(out, uint32(len(field)))
		out = append(out, field...)
	}
	return out, nil
}
