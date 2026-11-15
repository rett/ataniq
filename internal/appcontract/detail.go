package appcontract

import (
	"encoding/hex"
	"math"

	"ataniq/internal/appmodel"
)

// EventDetail is a closed set of historical event payloads.
type EventDetail interface{ eventDetail() }
type EnqueueDetail struct {
	Client, Command string
	Position        uint64
	Revision        string
	State           appmodel.State
}
type CancelDetail struct{ Client, Command, Revision string }
type DispositionDetail struct {
	Client, Command, Revision string
	State                     appmodel.State
	Outcome                   Outcome
}
type EmptyDetail struct{}
type Outcome string

const (
	Applied   Outcome = "applied"
	Unchanged Outcome = "unchanged"
)

func (EnqueueDetail) eventDetail()     {}
func (CancelDetail) eventDetail()      {}
func (DispositionDetail) eventDetail() {}
func (EmptyDetail) eventDetail()       {}

func rawRevision(s string) ([]byte, error) {
	if len(s) != 64 {
		return nil, errInvalid
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return nil, errInvalid
		}
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil, errInvalid
	}
	return b, nil
}

func projectedState(s appmodel.State) bool {
	return s == appmodel.Queued || s == appmodel.Held || s == appmodel.Cancelled
}

// EncodeEventDetail returns bounded canonical bytes for a closed detail value.
// Association of a detail with an event kind belongs to its consuming boundary.
func EncodeEventDetail(d EventDetail) ([]byte, error) {
	var client, command, revision string
	var position uint64
	var state appmodel.State
	var outcome Outcome
	switch v := d.(type) {
	case EmptyDetail:
		return frame("EVENT-DETAIL"), nil
	case EnqueueDetail:
		client, command, revision, position, state = v.Client, v.Command, v.Revision, v.Position, v.State
		if position == 0 || position > math.MaxInt64 || !projectedState(state) {
			return nil, errInvalid
		}
	case CancelDetail:
		client, command, revision, state = v.Client, v.Command, v.Revision, appmodel.Cancelled
	case DispositionDetail:
		client, command, revision, state, outcome = v.Client, v.Command, v.Revision, v.State, v.Outcome
		if !projectedState(state) || (outcome != Applied && outcome != Unchanged) {
			return nil, errInvalid
		}
	default:
		return nil, errInvalid
	}
	if !validID(client) || !validID(command) {
		return nil, errInvalid
	}
	raw, err := rawRevision(revision)
	if err != nil {
		return nil, err
	}
	f := [][]byte{[]byte(client), []byte(command)}
	if position != 0 {
		f = append(f, number(position))
	}
	f = append(f, raw, []byte(state))
	if outcome != "" {
		f = append(f, []byte(outcome))
	}
	return frame("EVENT-DETAIL", f...), nil
}
