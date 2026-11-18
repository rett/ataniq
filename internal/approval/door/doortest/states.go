package doortest

import "ataniq/internal/approval/record"

// moves is every move between states the stand-in allows: from a state, the
// states that may follow it. A state with no move is final. The keys are
// the whole closed list of [record.State]. Pending follows nothing, and
// approved follows only pending: no state returns to either.
var moves = map[record.State][]record.State{
	record.StatePending: {
		record.StateApproved, record.StateApprovedByRule, record.StateApprovedByGrant,
		record.StateDenied, record.StateWithdrawn, record.StateExpired, record.StateVoid,
	},
	record.StateApproved:        {record.StateConsumed, record.StateWithdrawn, record.StateExpired, record.StateVoid},
	record.StateApprovedByRule:  {record.StateConsumed, record.StateWithdrawn, record.StateExpired, record.StateVoid},
	record.StateApprovedByGrant: {record.StateConsumed, record.StateWithdrawn, record.StateExpired, record.StateVoid},
	record.StateConsumed:        {record.StateApplied, record.StateNotApplied, record.StateUnknown},
	record.StateUnknown:         {record.StateApplied, record.StateNotApplied},
	record.StateDenied:          nil,
	record.StateWithdrawn:       nil,
	record.StateExpired:         nil,
	record.StateVoid:            nil,
	record.StateApplied:         nil,
	record.StateNotApplied:      nil,
}

// Moves returns a copy of the table of moves: for each state of the closed
// list, the states that may follow it. A state with none is final.
func Moves() map[record.State][]record.State {
	out := make(map[record.State][]record.State, len(moves))
	for from, to := range moves {
		out[from] = append([]record.State(nil), to...)
	}
	return out
}

// Final reports whether s is a state nothing follows. A request whose
// record is final has ended: it no longer counts against the cap, and the
// lead may ask for the same thing again.
func Final(s record.State) bool {
	return len(moves[s]) == 0
}

// mayMove reports whether the table allows from to be followed by to.
func mayMove(from, to record.State) bool {
	for _, s := range moves[from] {
		if s == to {
			return true
		}
	}
	return false
}

// usable reports whether s is a state an approval can be used from.
func usable(s record.State) bool {
	return s == record.StateApproved || s == record.StateApprovedByRule || s == record.StateApprovedByGrant
}
