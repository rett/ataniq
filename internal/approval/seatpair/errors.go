package seatpair

// Error is the type of every error the package returns for a refusal. The
// values are constants, so they compare with == and with errors.Is. None of
// their texts holds a code, a key or a credential id.
type Error string

// Error returns the text of the refusal.
func (e Error) Error() string { return string(e) }

// The refusals of a pairing code.
const (
	// ErrWrongCode: the code presented is not the code of the pairing. The
	// try is counted.
	ErrWrongCode Error = "seatpair: wrong pairing code"
	// ErrExpired: the code is older than its 10 minutes.
	ErrExpired Error = "seatpair: pairing code expired"
	// ErrUsed: the code has been used already.
	ErrUsed Error = "seatpair: pairing code already used"
	// ErrBurnt: the code had its fifth wrong try, and is dead.
	ErrBurnt Error = "seatpair: pairing code burnt by wrong tries"
)

// The refusals of the state machine.
const (
	// ErrNoPairing: no pairing has been started.
	ErrNoPairing Error = "seatpair: no pairing started"
	// ErrPairingOpen: a pairing is open, and Start was not asked to replace it.
	ErrPairingOpen Error = "seatpair: a pairing is already open"
	// ErrPending: a credential is already presented and waits at the desk.
	ErrPending Error = "seatpair: a credential is already pending"
	// ErrNotPending: no credential is pending, so there is none to confirm
	// or reject.
	ErrNotPending Error = "seatpair: no credential is pending"
	// ErrPendingExpired: the credential waited at the desk for its 10 minutes
	// and no longer; the pairing is over.
	ErrPendingExpired Error = "seatpair: the pending credential expired"
	// ErrClock: the time handed in is zero or earlier than a time the pairing
	// has already seen.
	ErrClock Error = "seatpair: clock is zero or went backwards"
	// ErrRandom: the random source did not give a code.
	ErrRandom Error = "seatpair: random source failed"
)
