// Package frame is the v2 checkpoint link envelope, shared with the fault proxy.
package frame

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"

	"ataniq/internal/checkpoint"
)

// Each message has a 4-byte big-endian payload length, version 2, kind,
// and exactly the fields for that kind, each with a 4-byte big-endian length.
type Kind byte

const (
	Read        Kind = 1 + iota // canonical read request, home public key, home signature
	OwnerRead                   // canonical read request
	Claim                       // scoped slot, home public key, canonical intent, home and session signatures
	Advance                     // same layout as Claim
	Enroll                      // canonical intent, home proof of possession
	Release                     // canonical intent
	Retire                      // canonical intent
	Receipt                     // canonical acknowledgement, witness signature
	View                        // current state; followed by Fresh and Prior under the same deadline
	Fresh                       // challenged body, witness signature
	Prior                       // historical body and signature, both empty when absent
	Failure                     // closed-set code, disposition signature
	Connect                     // signed home grant check, same fields as Read
	Granted                     // disposition signature
	Poll                        // witness requests one home request
	Idle                        // no home request waiting
	Settle                      // signed reconciliation request from home
	OwnerSettle                 // reconciliation request from owner
)

var arity = [...]int{
	Read: 3, OwnerRead: 1, Claim: 5, Advance: 5, Enroll: 2, Release: 1, Retire: 1,
	Receipt: 2, View: 1, Fresh: 2, Prior: 2, Failure: 2, Connect: 3, Granted: 1, Poll: 0, Idle: 0, Settle: 3, OwnerSettle: 1,
}
var ErrProtocol = errors.New("checkpoint link: invalid frame")

type Message struct {
	Kind   Kind
	Fields [][]byte
	Nonce  [32]byte // Fresh delivery challenge on every mutation attempt.
}

func New(kind Kind, fields ...[]byte) Message { return Message{Kind: kind, Fields: fields} }

func (k Kind) Mutation() bool {
	switch k {
	case Claim, Advance, Enroll, Release, Retire, Settle, OwnerSettle:
		return true
	}
	return false
}

func (m Message) Marshal() ([]byte, error) {
	if m.Kind == 0 || int(m.Kind) >= len(arity) || len(m.Fields) != arity[m.Kind] {
		return nil, ErrProtocol
	}
	size := 2
	if m.Kind.Mutation() {
		size += len(m.Nonce)
	}
	for _, p := range m.Fields {
		if len(p) > checkpoint.MaxFrame-size-4 {
			return nil, ErrProtocol
		}
		size += 4 + len(p)
	}
	b := make([]byte, 4, 4+size)
	binary.BigEndian.PutUint32(b, uint32(size))
	b = append(b, 2, byte(m.Kind))
	if m.Kind.Mutation() {
		b = append(b, m.Nonce[:]...)
	}
	for _, p := range m.Fields {
		b = binary.BigEndian.AppendUint32(b, uint32(len(p)))
		b = append(b, p...)
	}
	return b, nil
}

func ReadMessage(r io.Reader) (Message, error) {
	return ReadAfterHeader(r, nil)
}

// ReadAfterHeader invokes ready once after validating the length prefix.
func ReadAfterHeader(r io.Reader, ready func() error) (Message, error) {
	var prefix [4]byte
	if _, err := io.ReadFull(r, prefix[:]); err != nil {
		return Message{}, err
	}
	size := binary.BigEndian.Uint32(prefix[:])
	if size < 2 || size > checkpoint.MaxFrame {
		return Message{}, ErrProtocol
	}
	if ready != nil {
		if err := ready(); err != nil {
			return Message{}, err
		}
	}
	b := make([]byte, size)
	if _, err := io.ReadFull(r, b); err != nil {
		return Message{}, err
	}
	k := Kind(b[1])
	if b[0] != 2 || k == 0 || int(k) >= len(arity) {
		return Message{}, ErrProtocol
	}
	m := Message{Kind: k, Fields: make([][]byte, arity[k])}
	b = b[2:]
	if k.Mutation() {
		if len(b) < len(m.Nonce) {
			return Message{}, ErrProtocol
		}
		copy(m.Nonce[:], b)
		b = b[len(m.Nonce):]
	}
	for i := range m.Fields {
		if len(b) < 4 {
			return Message{}, ErrProtocol
		}
		n := binary.BigEndian.Uint32(b)
		b = b[4:]
		if uint64(n) > uint64(len(b)) {
			return Message{}, ErrProtocol
		}
		m.Fields[i], b = bytes.Clone(b[:n]), b[n:]
	}
	if len(b) != 0 {
		return Message{}, ErrProtocol
	}
	return m, nil
}

func (m Message) WriteToStream(w io.Writer) error {
	b, err := m.Marshal()
	if err != nil {
		return err
	}
	n, err := w.Write(b)
	if err == nil && n != len(b) {
		return io.ErrShortWrite
	}
	return err
}
