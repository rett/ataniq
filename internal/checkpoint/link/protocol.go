package link

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"unicode/utf8"

	"ataniq/internal/assignment"
	"ataniq/internal/assignment/wire"
	cp "ataniq/internal/checkpoint"
	"ataniq/internal/checkpoint/link/internal/frame"
)

func validSlot(s cp.Slot) bool {
	for _, v := range []string{s.Witness, s.Authority, s.Store} {
		if v == "" || len(v) > assignment.MaxReferenceBytes || !utf8.ValidString(v) {
			return false
		}
	}
	return true
}

// Checkpoint already defines the signed read *reply*. The home signs this
// distinct request, binding both IDs, the exact slot and the witness key.
type readRecord struct {
	ExpectedEpoch uint64  `json:"expected_epoch,string,omitempty"`
	Schema        int     `json:"schema"`
	Operation     string  `json:"operation"`
	WitnessKey    string  `json:"witness_key_id"`
	Slot          cp.Slot `json:"slot"`
	Challenge     string  `json:"challenge"`
	ReceiptID     string  `json:"receipt_id"`
}

func readBody(q cp.ReadRequest, wid, op string, epoch uint64) ([]byte, error) {
	if (op == "settle") != (epoch != 0) || !validSlot(q.Slot) || q.Challenge == (cp.ID{}) {
		return nil, &cp.Reason{Kind: cp.InvalidRequest}
	}
	b, err := json.Marshal(readRecord{epoch, 1, op, wid, q.Slot, hex.EncodeToString(q.Challenge[:]), hex.EncodeToString(q.ReceiptID[:])})
	if err != nil {
		return nil, err
	}
	return wire.Canonicalize(b)
}

// Bound nesting before encoding/json scans a field. Leave syntax and
// canonical representation checks to the decoders.
func decodeJSON(b []byte, dst any) bool {
	depth := 0
	quoted, escaped := false, false
	for _, c := range b {
		if quoted {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				quoted = false
			}
			continue
		}
		switch c {
		case '"':
			quoted = true
		case '[', '{':
			depth++
			if depth > 8 {
				return false
			}
		case ']', '}':
			depth--
			if depth < 0 {
				return false
			}
		}
	}
	return json.Unmarshal(b, dst) == nil
}

func parseRead(b []byte, wid, op string) (cp.ReadRequest, uint64, error) {
	var r readRecord
	var q cp.ReadRequest
	if !decodeJSON(b, &r) || len(r.Challenge) != 64 || len(r.ReceiptID) != 64 {
		return q, 0, frame.ErrProtocol
	}
	q.Slot = r.Slot
	if _, err := hex.Decode(q.Challenge[:], []byte(r.Challenge)); err != nil {
		return q, 0, frame.ErrProtocol
	}
	if _, err := hex.Decode(q.ReceiptID[:], []byte(r.ReceiptID)); err != nil {
		return q, 0, frame.ErrProtocol
	}
	exact, err := readBody(q, r.WitnessKey, op, r.ExpectedEpoch)
	if err != nil || !bytes.Equal(exact, b) {
		return q, 0, frame.ErrProtocol
	}
	if r.WitnessKey != wid {
		return q, 0, &cp.Reason{Kind: cp.Unauthorized}
	}
	return q, r.ExpectedEpoch, nil
}

// Send only contract error codes. An unclassified backend error may have
// happened after commit. Its diagnostic never goes on the wire.
var categories = []error{cp.ErrNotApplied, cp.ErrRefused, cp.ErrStale, cp.ErrRetired, cp.ErrCapacity, cp.ErrUnavailable, cp.ErrUnknownOutcome}
var reasons = []cp.ReasonKind{cp.InvalidRequest, cp.Unauthorized, cp.HomeMismatch, cp.KeyRoleConflict, cp.NotEnrolled, cp.MutationConflict,
	cp.Superseded, cp.PredecessorMismatch, cp.SessionConflict, cp.EnrollmentConflict, cp.CounterExhausted, cp.InvalidSuccessor}

func errorCode(err error) string {
	if errors.Is(err, cp.ErrUnknownOutcome) {
		return string(cp.ErrUnknownOutcome.Error())
	}
	var reason *cp.Reason
	if errors.As(err, &reason) {
		for _, k := range reasons {
			if k == reason.Kind {
				return string(k)
			}
		}
	}
	for _, e := range categories {
		if errors.Is(err, e) {
			return string(e.Error())
		}
	}
	return string(cp.ErrUnknownOutcome.Error())
}
func decodeError(b []byte) error {
	for _, k := range reasons {
		if string(b) == string(k) {
			return &cp.Reason{Kind: k}
		}
	}
	for _, e := range categories {
		if string(b) == e.Error() {
			return e
		}
	}
	return frame.ErrProtocol
}

var operation = [...]string{
	frame.Settle: "settle", frame.OwnerSettle: "settle", frame.Connect: "connect", frame.Read: "read", frame.PendingRead: "pending_read", frame.OwnerRead: "read",
	frame.Claim: "claim", frame.Advance: "advance", frame.Enroll: "enroll",
	frame.Release: "release", frame.Retire: "retire",
}
