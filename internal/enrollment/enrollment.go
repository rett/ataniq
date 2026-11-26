// Package enrollment gates access to a managed assignment ledger. The owner
// provisions Binding outside the store and its restore domain. Keep the old
// root as evidence after a failure. Retire a doubtful identity through the
// owner administration capability and enroll a new key in a fresh empty root.
// A valid shorter prefix and a backup made
// at that prefix produce the same HomeBehind observation. Restoring both home
// and witness to the same point is undetectable without a newer outside record.
// An ordinary directory copy changes the pinned device/inode identities.
// An offline clone preserving those identities, the credentials and all
// witness evidence cannot be distinguished locally. Likewise an equal ledger
// head alone cannot expose a witness-only rollback of session history.
// A restored witness exactly matching an outstanding signed intent's original
// predecessor is indistinguishable from one that never received that intent.
//
// Start acquires a local lock, replays read-only, compares a challenged witness
// read, claims a process session and checks the head again. A successful start
// is a startup decision. Actual dispatch still requires a live route fence.
// Close releases the local lock. Replacing a claimed session requires the
// owner's independent fencing and checkpoint.Administration.Release.
package enrollment

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"slices"

	"ataniq/internal/assignment"
	"ataniq/internal/checkpoint"
)

// Reason classifies observable failures. A clean record-boundary cut is
// indistinguishable from a rollback to the same head.
type Reason string

const (
	HomeBehind       Reason = "home behind"
	HomeAhead        Reason = "home ahead"
	Fork             Reason = "fork"
	Retired          Reason = "retired identity. Enroll afresh in a new empty root"
	Unknown          Reason = "unknown enrollment"
	Unreachable      Reason = "witness unavailable"
	MissingHeader    Reason = "missing ledger header"
	IdentityMismatch Reason = "store identity mismatch"
	Insecure         Reason = "unsafe store"
	Truncated        Reason = "incomplete ledger frame"
	Legacy           Reason = "legacy format"
	Corrupt          Reason = "invalid ledger"
	Interrupted      Reason = "interrupted checkpoint advance"
	Occupied         Reason = "session or store occupied"
	ClaimUncertain   Reason = "session claim outcome unknown"
	EnrollmentFailed Reason = "enrollment failed. This root cannot be used for this enrollment"
	Closed           Reason = "store closed"
)

type Error struct {
	Reason Reason
	Err    error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("enrollment: %s: %v", e.Reason, e.Err)
	}
	return "enrollment: " + string(e.Reason)
}

func (e *Error) Unwrap() error { return e.Err }

func refuse(r Reason, err error) error { return &Error{r, err} }

func witnessError(err error) error {
	var r *checkpoint.Reason
	switch {
	case errors.Is(err, checkpoint.ErrRetired):
		return refuse(Retired, err)
	case errors.As(err, &r):
		if r.Kind == checkpoint.SessionConflict {
			return refuse(Occupied, err)
		}
		return refuse(Unknown, err)
	case errors.Is(err, checkpoint.ErrUnknownOutcome):
		return refuse(ClaimUncertain, err)
	case errors.Is(err, checkpoint.ErrRefused), errors.Is(err, checkpoint.ErrStale):
		return refuse(Unknown, err)
	default:
		return refuse(Unreachable, err)
	}
}

// Identity pins the locked root and regular ledger file at enrollment.
type Identity struct{ RootDevice, RootInode, LedgerDevice, LedgerInode uint64 }

// Binding is the owner's installation record. Only Enroll issues it, after
// durable header creation and authenticated create-only registration.
type Binding struct {
	Identity            Identity
	Header              assignment.Header
	Slot                checkpoint.Slot
	Epoch               uint64
	HomePin, WitnessPin ed25519.PublicKey
	EnrollmentID        checkpoint.ID
	Receipt             checkpoint.Receipt
}

type LedgerStore interface {
	Lock(Fault) (LockedLedger, error)
}

// LockedLedger holds an exclusive lock until Close. Ledger and Stage only
// read. Create requires an empty root and a new file. All mutators sync the
// affected file and parent before returning success; Sync covers the ledger
// and parent even when the previous process died during an append.
type LockedLedger interface {
	Create(header []byte) (Identity, error)
	Ledger() (io.Reader, Identity, error)
	Check() error           // recheck the locked root and the ledger observed by replay
	Stage() ([]byte, error) // nil for absent
	SaveStage([]byte) error // exclusive create
	Append([]byte) error
	Sync() error
	ClearStage() error
	Close() error
}

// Fault stops an operation immediately after a named boundary. A nil Fault
// runs normally. Hooks model loss of the caller, including a lost reply.
type Fault interface{ After(boundary string) error }

func after(f Fault, point string) error {
	if f != nil {
		return f.After(point)
	}
	return nil
}

func id() checkpoint.ID {
	var v checkpoint.ID
	rand.Read(v[:])
	return v
}

func newName() string {
	v := id()
	return hex.EncodeToString(v[:16])
}

func (b Binding) request() (checkpoint.Enrollment, error) {
	raw, err := b.Header.Bytes()
	return checkpoint.Enrollment{ID: b.EnrollmentID, Slot: b.Slot, HeaderBytes: raw, HomePublicKey: b.HomePin}, err
}

// Enroll uses a fresh incarnation and ledger ID for the independently granted
// owner-selected slot and key. No binding is issued on failure, including a
// lost enrollment reply. Retire any uncertain registration before replacing it.
func Enroll(ctx context.Context, root LedgerStore, admin checkpoint.Administration, slot checkpoint.Slot, epoch uint64, home ed25519.PrivateKey, witness ed25519.PublicKey, fault Fault) (Binding, error) {
	f, err := root.Lock(fault)
	if err != nil {
		return Binding{}, refuse(EnrollmentFailed, err)
	}
	b, err := enroll(ctx, f, admin, slot, epoch, home, witness, fault)
	err = errors.Join(err, f.Close())
	if err != nil {
		return Binding{}, refuse(EnrollmentFailed, err)
	}
	return b, nil
}

func enroll(ctx context.Context, f LockedLedger, admin checkpoint.Administration, slot checkpoint.Slot, epoch uint64, home ed25519.PrivateKey, witness ed25519.PublicKey, fault Fault) (Binding, error) {
	if len(home) != ed25519.PrivateKeySize || epoch == 0 {
		return Binding{}, checkpoint.ErrRefused
	}
	if _, err := checkpoint.KeyID(witness); err != nil {
		return Binding{}, err
	}
	pub := home.Public().(ed25519.PublicKey)
	keyID, err := checkpoint.KeyID(pub)
	if err != nil {
		return Binding{}, err
	}
	b := Binding{
		Header: assignment.Header{
			Authority: slot.Authority, Store: slot.Store, Schema: 1,
			LedgerID: newName(), Incarnation: newName(), KeyID: keyID,
		},
		Slot: slot, Epoch: epoch, HomePin: pub, WitnessPin: slices.Clone(witness), EnrollmentID: id(),
	}
	q, err := b.request()
	if err != nil {
		return Binding{}, err
	}
	q.ProofOfPossession, err = checkpoint.ProveEnrollment(home, q)
	if err != nil {
		return Binding{}, err
	}
	b.Identity, err = f.Create(q.HeaderBytes)
	if err != nil {
		return Binding{}, err
	}
	b.Receipt, err = admin.Enroll(ctx, q)
	if err != nil {
		return Binding{}, err
	}
	if err = after(fault, "enroll.registered"); err != nil {
		return Binding{}, err
	}
	body, err := q.Body("")
	if err != nil {
		return Binding{}, err
	}
	if err = checkpoint.MatchReceipt(witness, epoch, "enroll", body, b.Receipt); err != nil {
		return Binding{}, err
	}
	return b, nil
}
