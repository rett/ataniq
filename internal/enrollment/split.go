package enrollment

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"

	"ataniq/internal/assignment"
	"ataniq/internal/checkpoint"
)

// Prepared carries the public material for one create-only enrollment.
type Prepared struct {
	Version    int               `json:"version"`
	Epoch      uint64            `json:"epoch,string"`
	WitnessKey ed25519.PublicKey `json:"witness_key"`
	Intent     []byte            `json:"intent"`
	Proof      string            `json:"proof"`
	Signature  string            `json:"signature,omitempty"`
}

type preparedRecord struct {
	Request  Prepared `json:"request"`
	Identity Identity `json:"identity"`
}

type completedRecord struct {
	Identity Identity `json:"identity"`
	Receipt  []byte   `json:"receipt"`
}

func exactJSON(raw []byte, dst any) error {
	if len(raw) > ceremonyLimit || json.Unmarshal(raw, dst) != nil {
		return checkpoint.ErrRefused
	}
	b, err := json.Marshal(dst)
	if err != nil || !bytes.Equal(raw, b) {
		return checkpoint.ErrRefused
	}
	return nil
}

const ceremonyLimit = 192 << 10

func (p Prepared) signingBytes() ([]byte, error) {
	p.Signature = ""
	return json.Marshal(p)
}

func generatedName(s string) bool {
	raw, err := hex.DecodeString(s)
	return err == nil && len(raw) == 16 && hex.EncodeToString(raw) == s
}

func (p Prepared) request() (checkpoint.Enrollment, error) {
	var zero checkpoint.Enrollment
	if p.Version != 1 || p.Epoch == 0 || len(p.Proof) > 512 || len(p.Signature) > 512 {
		return zero, checkpoint.ErrRefused
	}
	if _, err := checkpoint.KeyID(p.WitnessKey); err != nil {
		return zero, err
	}
	i, err := checkpoint.ParseIntent("enroll", "", p.Intent)
	if err != nil {
		return zero, err
	}
	q := i.(checkpoint.Enrollment)
	if _, err := assignment.NewLedgerFromHeader(q.HeaderBytes); err != nil {
		return zero, err
	}
	var frame struct{ Header assignment.Header }
	if err := json.Unmarshal(q.HeaderBytes, &frame); err != nil {
		return zero, err
	}
	keyID, err := checkpoint.KeyID(q.HomePublicKey)
	if !generatedName(frame.Header.LedgerID) || !generatedName(frame.Header.Incarnation) {
		return zero, checkpoint.ErrRefused
	}
	if err != nil || frame.Header.KeyID != keyID || frame.Header.Authority != q.Slot.Authority || frame.Header.Store != q.Slot.Store || bytes.Equal(q.HomePublicKey, p.WitnessKey) {
		return zero, checkpoint.ErrRefused
	}
	q.ProofOfPossession = []byte(p.Proof)
	if err := checkpoint.VerifySignature(checkpoint.HomeNamespace, q.HomePublicKey, p.Intent, q.ProofOfPossession); err != nil {
		return zero, err
	}
	body, err := p.signingBytes()
	if err != nil {
		return zero, err
	}
	return q, checkpoint.VerifySignature(checkpoint.HomeNamespace, q.HomePublicKey, body, []byte(p.Signature))
}

// EncodePrepared produces the transferable canonical JSON.
func EncodePrepared(p Prepared) ([]byte, error) {
	if _, err := p.request(); err != nil {
		return nil, err
	}
	return json.Marshal(p)
}

// UnmarshalJSON also enforces schema, canonical fields and signatures when
// Prepared is embedded in another JSON document.
func (p *Prepared) UnmarshalJSON(raw []byte) error {
	decoded, err := DecodePrepared(raw)
	if err == nil {
		*p = decoded
	}
	return err
}

// DecodePrepared validates the entire transport document before returning it.
func DecodePrepared(raw []byte) (Prepared, error) {
	type wirePrepared Prepared
	var w wirePrepared
	if err := exactJSON(raw, &w); err != nil {
		return Prepared{}, err
	}
	p := Prepared(w)
	if _, err := p.request(); err != nil {
		return Prepared{}, err
	}
	return p, nil
}

// Prepare creates the final ledger and durably pins its identity before export.
func Prepare(ctx context.Context, root LedgerStore, slot checkpoint.Slot, epoch uint64, home ed25519.PrivateKey, witness ed25519.PublicKey, fault Fault) (Prepared, error) {
	if err := ctx.Err(); err != nil {
		return Prepared{}, err
	}
	if len(home) != ed25519.PrivateKeySize {
		return Prepared{}, checkpoint.ErrRefused
	}
	pub := home.Public().(ed25519.PublicKey)
	kid, err := checkpoint.KeyID(pub)
	if err != nil {
		return Prepared{}, err
	}
	h := assignment.Header{Authority: slot.Authority, Store: slot.Store, Schema: 1, LedgerID: newName(), Incarnation: newName(), KeyID: kid}
	header, err := h.Bytes()
	if err != nil {
		return Prepared{}, err
	}
	q := checkpoint.Enrollment{ID: id(), Slot: slot, HeaderBytes: header, HomePublicKey: pub}
	proof, err := checkpoint.ProveEnrollment(home, q)
	if err != nil {
		return Prepared{}, err
	}
	body, err := q.Body("")
	if err != nil {
		return Prepared{}, err
	}
	p := Prepared{Version: 1, Epoch: epoch, WitnessKey: bytes.Clone(witness), Intent: body, Proof: string(proof)}
	signed, err := p.signingBytes()
	if err != nil {
		return Prepared{}, err
	}
	sig, err := checkpoint.Sign(checkpoint.HomeNamespace, home, signed)
	if err != nil {
		return Prepared{}, err
	}
	p.Signature = string(sig)
	if _, err = p.request(); err != nil {
		return Prepared{}, err
	}
	f, err := root.lock(fault)
	if err != nil {
		return Prepared{}, err
	}
	err = errors.Join(f.createLedger(p), f.close())
	if err != nil {
		return Prepared{}, err
	}
	return p, nil
}

// ResumePrepared finishes any interrupted header publication under the home lock.
func ResumePrepared(ctx context.Context, root LedgerStore) (Prepared, error) {
	if err := ctx.Err(); err != nil {
		return Prepared{}, err
	}
	f, err := root.lock(nil)
	if err != nil {
		return Prepared{}, err
	}
	r, err := f.resumePrepared()
	if err = errors.Join(err, f.close()); err != nil {
		return Prepared{}, err
	}
	return r.Request, nil
}

// Submit authenticates the request before exercising the owner capability.
func Submit(ctx context.Context, admin checkpoint.Administration, p Prepared) ([]byte, error) {
	q, err := p.request()
	if err != nil {
		return nil, err
	}
	// The owner link and durable witness expose the configured epoch through
	// challenged status. The basic Administration capability has no scope getter.
	if scoped, ok := admin.(checkpoint.RecoveryAdministration); ok {
		challenge := id()
		status, err := scoped.Status(ctx, challenge)
		if err != nil {
			return nil, err
		}
		if _, err = checkpoint.VerifyDirectoryStatus(p.WitnessKey, p.Epoch, challenge, status); err != nil {
			return nil, err
		}
	}
	r, err := admin.Enroll(ctx, q)
	var reason *checkpoint.Reason
	if errors.As(err, &reason) && reason.Kind == checkpoint.Superseded {
		// Later slot activity moved the enrollment receipt into history.
		// Recover that exact acknowledgement without changing current state.
		read := checkpoint.ReadRequest{Slot: q.Slot, Challenge: id(), ReceiptID: q.ID}
		v, readErr := admin.Read(ctx, read)
		if readErr != nil {
			return nil, readErr
		}
		if readErr = checkpoint.VerifyView(p.WitnessKey, read, v); readErr != nil {
			return nil, readErr
		}
		if v.Current.Epoch != p.Epoch || v.Prior == nil {
			return nil, err
		}
		r, err = *v.Prior, nil
	}
	if err != nil {
		return nil, err
	}
	if err = checkpoint.MatchReceipt(p.WitnessKey, p.Epoch, "enroll", p.Intent, r); err != nil {
		return nil, err
	}
	raw := binary.BigEndian.AppendUint32([]byte{1}, uint32(len(r.Body)))
	raw = append(raw, r.Body...)
	return append(raw, r.Signature...), nil
}

func unpackReceipt(raw []byte) (checkpoint.Receipt, error) {
	if len(raw) < 5 || raw[0] != 1 {
		return checkpoint.Receipt{}, checkpoint.ErrRefused
	}
	n := uint64(binary.BigEndian.Uint32(raw[1:5]))
	if n == 0 || n > checkpoint.MaxFrame || n >= uint64(len(raw)-5) || uint64(len(raw)-5)-n > 512 {
		return checkpoint.Receipt{}, checkpoint.ErrRefused
	}
	return checkpoint.Receipt{Body: bytes.Clone(raw[5 : 5+n]), Signature: bytes.Clone(raw[5+n:])}, nil
}

// Finalize checks both pins and the exact genesis before saving the receipt.
func Finalize(ctx context.Context, root LedgerStore, p Prepared, raw []byte, witness ed25519.PublicKey) (Binding, error) {
	return finalize(ctx, root, p, raw, witness, nil)
}

func finalize(ctx context.Context, root LedgerStore, p Prepared, raw []byte, witness ed25519.PublicKey, fault Fault) (Binding, error) {
	if err := ctx.Err(); err != nil {
		return Binding{}, err
	}
	q, err := p.request()
	if err != nil {
		return Binding{}, err
	}
	if !bytes.Equal(witness, p.WitnessKey) {
		return Binding{}, checkpoint.ErrRefused
	}
	r, err := unpackReceipt(raw)
	if err != nil {
		return Binding{}, err
	}
	if err = checkpoint.MatchReceipt(witness, p.Epoch, "enroll", p.Intent, r); err != nil {
		return Binding{}, err
	}
	f, err := root.lock(fault)
	if err != nil {
		return Binding{}, err
	}
	b, err := finish(f, p, q, raw, r)
	if err = errors.Join(err, f.close()); err != nil {
		return Binding{}, err
	}
	return b, nil
}

func finish(f lockedLedger, p Prepared, q checkpoint.Enrollment, raw []byte, r checkpoint.Receipt) (Binding, error) {
	stage, err := f.prepared()
	if err != nil {
		return Binding{}, err
	}
	done, err := f.receipt()
	if err != nil {
		return Binding{}, err
	}
	var rec preparedRecord
	if stage != nil {
		if err = exactJSON(stage, &rec); err != nil {
			return Binding{}, err
		}
		expected, _ := json.Marshal(preparedRecord{p, rec.Identity})
		if !bytes.Equal(stage, expected) {
			return Binding{}, refuse(IdentityMismatch, nil)
		}
	} else if done == nil {
		return Binding{}, refuse(Unknown, errors.New("no prepared enrollment"))
	}
	var c completedRecord
	if done != nil {
		if err = exactJSON(done, &c); err != nil {
			return Binding{}, err
		}
		if !bytes.Equal(raw, c.Receipt) || stage != nil && c.Identity != rec.Identity {
			return Binding{}, refuse(IdentityMismatch, nil)
		}
		rec.Identity = c.Identity
	}
	if err = checkGenesis(f, rec.Identity, q.HeaderBytes); err != nil {
		return Binding{}, err
	}
	if done == nil {
		data, err := json.Marshal(completedRecord{rec.Identity, raw})
		if err != nil {
			return Binding{}, err
		}
		if err = f.complete(data); err != nil {
			return Binding{}, err
		}
	}
	if stage != nil {
		if err = f.check(); err != nil {
			return Binding{}, err
		}
		if err = f.clearPrepared(); err != nil {
			return Binding{}, err
		}
	}
	var frame struct{ Header assignment.Header }
	if err = json.Unmarshal(q.HeaderBytes, &frame); err != nil {
		return Binding{}, err
	}
	b := Binding{RootPath: f.rootPath(), Identity: rec.Identity, Header: frame.Header, Slot: q.Slot, Epoch: p.Epoch, HomePin: q.HomePublicKey, WitnessPin: bytes.Clone(p.WitnessKey), EnrollmentID: q.ID, Receipt: r}
	return b, f.check()
}

func checkGenesis(f lockedLedger, pin Identity, header []byte) error {
	r, identity, err := f.readLedger()
	if err != nil {
		return err
	}
	if identity != pin {
		return refuse(IdentityMismatch, nil)
	}
	got, err := io.ReadAll(io.LimitReader(r, int64(len(header))+1))
	if err != nil {
		return err
	}
	if !bytes.Equal(header, got) {
		return refuse(IdentityMismatch, nil)
	}
	return f.check()
}
