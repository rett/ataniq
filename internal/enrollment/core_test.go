package enrollment

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"io"
	"testing"

	"ataniq/internal/checkpoint"
)

// This store has no filesystem, clock or writer. Calling a mutator other than
// owner creation panics, so these tests also guard the startup write boundary.
type readOnlyLedger struct {
	lockedLedger
	header []byte
	locked bool
}

func (m *readOnlyLedger) lock(Fault) (lockedLedger, error) {
	if m.locked {
		return nil, refuse(Occupied, nil)
	}
	m.locked = true
	return m, nil
}

func (m *readOnlyLedger) createLedger(b []byte) (Identity, error) {
	if m.header != nil {
		return Identity{}, errors.New("nonempty")
	}
	m.header = bytes.Clone(b)
	return Identity{RootInode: 1, LedgerInode: 2}, nil
}

func (m *readOnlyLedger) readLedger() (io.Reader, Identity, error) {
	return bytes.NewReader(m.header), Identity{RootInode: 1, LedgerInode: 2}, nil
}

func (m *readOnlyLedger) check() error { return nil }

func (m *readOnlyLedger) receipt() ([]byte, error) { return nil, nil }

func (m *readOnlyLedger) stage() ([]byte, error) { return nil, nil }

func (m *readOnlyLedger) close() error { m.locked = false; return nil }

type claimHook struct {
	checkpoint.Client
	after func()
}

func (c claimHook) Claim(ctx context.Context, q checkpoint.Claim) (checkpoint.Receipt, error) {
	r, err := c.Client.Claim(ctx, q)
	if err == nil {
		c.after()
	}
	return r, err
}

func TestGateUsesCurrentSessionAfterClaim(t *testing.T) {
	for _, lost := range []bool{true, false} {
		root := new(readOnlyLedger)
		gen := func() ed25519.PrivateKey {
			_, k, err := ed25519.GenerateKey(nil)
			if err != nil {
				t.Fatal(err)
			}
			return k
		}
		home, witness, session := gen(), gen(), gen()
		homePin, witnessPin := home.Public().(ed25519.PublicKey), witness.Public().(ed25519.PublicKey)
		w, err := checkpoint.NewMemory("w", 1, witness)
		if err != nil {
			t.Fatal(err)
		}
		slot := checkpoint.Slot{Witness: "w", Authority: "home", Store: "store"}
		if err := w.Grant(slot, homePin, true); err != nil {
			t.Fatal(err)
		}
		b, err := Enroll(t.Context(), root, w.Administration(), slot, 1, home, witnessPin, nil)
		if err != nil {
			t.Fatal(err)
		}
		c, err := w.Connect(slot, home, session, witnessPin)
		if err != nil {
			t.Fatal(err)
		}
		reason := Occupied
		if lost {
			w.LoseNextReply()
			reason = ClaimUncertain
		} else {
			c = claimHook{c, func() {
				admin := w.Administration()
				v, err := admin.Read(t.Context(), checkpoint.ReadRequest{Slot: slot, Challenge: id()})
				if err != nil {
					t.Fatal(err)
				}
				_, err = admin.Release(t.Context(), checkpoint.Mutation{ID: id(), Expected: v.Current})
				if err != nil {
					t.Fatal(err)
				}
			}}
		}
		s, err := Start(t.Context(), root, b, c, session, nil)
		var e *Error
		if s != nil || !errors.As(err, &e) || e.Reason != reason || root.locked {
			t.Fatalf("lost reply %v: store %v, lock %v, err %v", lost, s, root.locked, err)
		}
	}
}
