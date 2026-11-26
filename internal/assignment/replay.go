package assignment

import "fmt"

// Replay rebuilds all projections from decoded events. It returns no partial
// ledger on failure. The adapter verifies framing and pins the resulting head
// against its independently obtained checkpoint before serving the row.
func Replay(h Header, events []Event, v Verifier) (Ledger, error) {
	l, err := NewLedger(h)
	if err != nil {
		return Ledger{}, err
	}
	if v == nil {
		return Ledger{}, ErrInvalid
	}
	for i, e := range events {
		if e.Signature == "" {
			return Ledger{}, fmt.Errorf("record %d: missing signature: %w", i+1, ErrChain)
		}
		if err := v.Verify(h, e); err != nil {
			return Ledger{}, fmt.Errorf("record %d: %w", i+1, err)
		}
		l, err = apply(l, e, true)
		if err != nil {
			return Ledger{}, fmt.Errorf("record %d: %w", i+1, err)
		}
	}
	return l, nil
}

func VerifyCheckpoint(observed, pinned Checkpoint) error {
	if observed.Authority == "" || observed.Store == "" || observed.Incarnation == "" || observed.LedgerID == "" || !validDigest(observed.Digest) || observed != pinned {
		return ErrCheckpoint
	}
	return nil
}
