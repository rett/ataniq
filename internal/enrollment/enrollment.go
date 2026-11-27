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
// Prepare creates the home ledger in place. Carry EncodePrepared's version-1
// JSON object to the owner. Its fields, in order, are version, epoch (a decimal
// string), witness_key (padded standard base64), intent (padded standard base64 of the checkpoint's
// canonical enrollment body), proof (the checkpoint proof of possession) and
// signature (the home signature over the preceding fields). Both signatures
// are unpadded base64url SSHSIG text. DecodePrepared requires the exact compact
// encoding, including field order, with no extra members or trailing newline.
// Submit returns a raw receipt: byte 1, a four-byte big-endian body length,
// the exact signed checkpoint body, then its unpadded base64url signature.
// Finalize authenticates this frame against the home stage and witness pin.
// ResumePrepared returns the same request after an interrupted Prepare.
// Retrying Finalize at genesis recovers the same binding after a lost return.
//
// Start acquires a local lock, replays read-only, compares a challenged witness
// read and inspects staged evidence before its first witness mutation, the
// session claim. It confirms the claim before local cleanup and checks the
// named ledger again before admission. A staged CAS binds an occupied fence.
// A new claim cannot preserve its predecessor, so an uncommitted suffix
// requires owner reconciliation. A successful start
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
	Capacity         Reason = "witness capacity exhausted"
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
	case errors.Is(err, checkpoint.ErrCapacity):
		return refuse(Capacity, err)
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

// Binding is the owner's installation record. Finalize issues it after
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

// LedgerStore is a sealed storage capability. FileRoot supplies it to Enroll
// and Start. Start keeps raw file operations and the witness client private.
// AppendAndAdvance on the admitted Store is the single managed append path.
type LedgerStore interface {
	lock(Fault) (lockedLedger, error)
}

// lockedLedger owns the file descriptors and exclusive cooperating-writer lock.
type lockedLedger interface {
	createLedger(Prepared) error
	resumePrepared() (preparedRecord, error)
	prepared() ([]byte, error)
	complete([]byte) error
	clearPrepared() error
	readLedger() (io.Reader, Identity, error)
	check() error           // recheck the locked root and the ledger observed by replay
	stage() ([]byte, error) // nil for absent
	receipt() ([]byte, error)
	saveReceipt([]byte) error
	saveStage([]byte) error // exclusive create
	append([]byte) error
	clearStage() error
	close() error
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
	p, err := Prepare(ctx, root, slot, epoch, home, witness, fault)
	if err != nil {
		return Binding{}, refuse(EnrollmentFailed, err)
	}
	raw, err := Submit(ctx, admin, p)
	if err == nil {
		err = after(fault, "enroll.registered")
	}
	if err != nil {
		return Binding{}, refuse(EnrollmentFailed, err)
	}
	b, err := finalize(ctx, root, p, raw, witness, fault)
	if err != nil {
		return Binding{}, refuse(EnrollmentFailed, err)
	}
	return b, nil
}
