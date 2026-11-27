package enrollment

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	cp "ataniq/internal/checkpoint"
)

func (f *fixture) prepare(fault Fault) (Prepared, error) {
	return Prepare(f.ctx, f.root, f.slot, 1, f.home, pub(f.witness), fault)
}

func noPrivateMaterial(t *testing.T, raw []byte, keys ...ed25519.PrivateKey) {
	t.Helper()
	for _, key := range keys {
		for _, secret := range [][]byte{key, key.Seed()} {
			for _, v := range []string{string(secret), hex.EncodeToString(secret), base64.StdEncoding.EncodeToString(secret), base64.RawURLEncoding.EncodeToString(secret)} {
				if bytes.Contains(raw, []byte(v)) {
					t.Fatal("private material in public record")
				}
			}
		}
	}
}

func TestSplitCeremony(t *testing.T) {
	f := setup(t)
	p, err := f.prepare(nil)
	check(t, err)
	_, err = f.prepare(nil)
	want(t, err, Occupied)
	encoded, err := EncodePrepared(p)
	check(t, err)
	noPrivateMaterial(t, encoded, f.home, f.witness)
	transported, err := DecodePrepared(encoded)
	check(t, err)
	f.w.LoseNextReply()
	_, err = Submit(f.ctx, f.w.Administration(), transported)
	if !errors.Is(err, cp.ErrUnknownOutcome) {
		t.Fatal(err)
	}
	raw, err := Submit(f.ctx, f.w.Administration(), transported)
	check(t, err)
	again, err := Submit(f.ctx, f.w.Administration(), transported)
	check(t, err)
	if !bytes.Equal(raw, again) || f.state(t).Revision != 1 {
		t.Fatal("enrollment replay changed receipt or state")
	}
	b, err := Finalize(f.ctx, f.root, p, raw, pub(f.witness))
	check(t, err)
	repeated, err := Finalize(f.ctx, f.root, p, raw, pub(f.witness))
	check(t, err)
	if !reflect.DeepEqual(b, repeated) {
		t.Fatal("finalize replay changed binding")
	}
	_, err = ResumePrepared(f.ctx, f.root)
	want(t, err, Unknown)
	f.b = b
	s := f.open(t, nil)
	check(t, s.Close())

	other := setup(t)
	other.home, other.witness = f.home, f.witness
	other.newWitness(t)
	check(t, other.enroll(nil))
	if b.Slot != other.b.Slot || b.Epoch != other.b.Epoch || b.Header.KeyID != other.b.Header.KeyID ||
		b.Header.Schema != other.b.Header.Schema || !bytes.Equal(b.HomePin, other.b.HomePin) || !bytes.Equal(b.WitnessPin, other.b.WitnessPin) ||
		b.EnrollmentID == other.b.EnrollmentID || b.Header.LedgerID == other.b.Header.LedgerID || b.Header.Incarnation == other.b.Header.Incarnation {
		t.Fatal("split and wrapped enrollment bindings disagree on stable fields or reused fresh IDs")
	}
}

func TestSplitRecovery(t *testing.T) {
	for _, point := range []string{"prepare.reserved", "prepare.pinned", "prepare.genesis-synced", "header.created", "header.written", "header.file-synced", "header.dir-synced", "enroll.registered", "enroll.receipt-synced", "enroll.stage-cleared"} {
		t.Run(point, func(t *testing.T) {
			f := setup(t)
			var original Prepared
			var raw []byte
			var err error
			switch point {
			case "enroll.registered", "enroll.receipt-synced", "enroll.stage-cleared":
				original, err = f.prepare(nil)
				check(t, err)
				raw, err = Submit(f.ctx, f.w.Administration(), original)
				check(t, err)
				if point != "enroll.registered" {
					_, err = finalize(f.ctx, f.root, original, raw, pub(f.witness), cut(point))
					if !errors.Is(err, errCut) {
						t.Fatal(err)
					}
				}
			default:
				_, err = f.prepare(cut(point))
				if !errors.Is(err, errCut) {
					t.Fatal(err)
				}
				_, err = f.prepare(nil)
				want(t, err, Occupied)
			}
			if point != "enroll.stage-cleared" {
				p, err := ResumePrepared(f.ctx, f.root)
				check(t, err)
				if original.Version != 0 && !reflect.DeepEqual(original, p) {
					t.Fatal("changed saved request")
				}
				original = p
				p, err = ResumePrepared(f.ctx, f.root)
				check(t, err)
				if !reflect.DeepEqual(original, p) {
					t.Fatal("unstable recovery")
				}
			}
			receipt, err := Submit(f.ctx, f.w.Administration(), original)
			check(t, err)
			if raw != nil && !bytes.Equal(raw, receipt) {
				t.Fatal("lost exact receipt")
			}
			f.b, err = Finalize(f.ctx, f.root, original, receipt, pub(f.witness))
			check(t, err)
			f.open(t, nil)
		})
	}
}

func TestSplitRefusals(t *testing.T) {
	f := setup(t)
	p, err := f.prepare(nil)
	check(t, err)
	a := f.w.Administration()
	raw, err := Submit(f.ctx, a, p)
	check(t, err)
	pin := pub(f.witness)
	other := setup(t)
	other.witness = f.witness
	other.newWitness(t)
	otherP, err := other.prepare(nil)
	check(t, err)
	otherRaw, err := Submit(f.ctx, other.w.Administration(), otherP)
	check(t, err)
	wrongEpoch := p
	wrongEpoch.Epoch++
	tampered := bytes.Clone(raw)
	tampered[len(raw)-1] ^= 1
	path := filepath.Join(f.root.Path, preparedName)
	stage := contents(t, path)
	for _, tc := range []struct {
		name    string
		p       Prepared
		r       []byte
		witness ed25519.PublicKey
	}{
		{"epoch", wrongEpoch, raw, pin}, {"pin", p, raw, pub(key(t))}, {"signature", p, tampered, pin}, {"framing", p, raw[1:], pin},
		{"other receipt", p, otherRaw, pin}, {"other valid request and receipt", otherP, otherRaw, pin},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Finalize(f.ctx, f.root, tc.p, tc.r, tc.witness); err == nil {
				t.Fatal("accepted")
			}
			if !bytes.Equal(contents(t, path), stage) {
				t.Fatal("changed pending evidence")
			}
		})
	}
	header := contents(t, f.ledgerPath())
	put(t, f.ledgerPath(), append(bytes.Clone(header), 'x'))
	_, err = Finalize(f.ctx, f.root, p, raw, pin)
	want(t, err, IdentityMismatch)
	put(t, f.ledgerPath(), header)
	check(t, os.Rename(f.ledgerPath(), f.ledgerPath()+".old"))
	put(t, f.ledgerPath(), header)
	_, err = Finalize(f.ctx, f.root, p, raw, pin)
	want(t, err, IdentityMismatch)
	_, err = ResumePrepared(f.ctx, f.root)
	want(t, err, IdentityMismatch)
	if !bytes.Equal(contents(t, path), stage) {
		t.Fatal("lost evidence on identity mismatch")
	}
}

func TestSplitPreparedStrict(t *testing.T) {
	f := setup(t)
	p, err := f.prepare(nil)
	check(t, err)
	b, err := EncodePrepared(p)
	check(t, err)
	for _, bad := range [][]byte{
		append(bytes.Clone(b), '\n'), append([]byte{' '}, b...), append([]byte(`{"extra":0,`), b[1:]...),
		append([]byte(`{"epoch":"1",`), b[1:]...), bytes.Replace(b, []byte(`"version":1`), []byte(`"Version":1`), 1),
		bytes.Replace(b, []byte(`"epoch":"1"`), []byte(`"epoch":"01"`), 1), append(b, b...), bytes.Repeat([]byte{' '}, ceremonyLimit+1),
	} {
		if _, err := DecodePrepared(bad); err == nil {
			t.Fatal("accepted noncanonical prepared")
		}
	}
	for _, mutate := range []func(*Prepared){
		func(p *Prepared) { p.Epoch++ }, func(p *Prepared) { p.Version++ }, func(p *Prepared) { p.WitnessKey = pub(key(t)) },
		func(p *Prepared) { p.Proof = p.Signature }, func(p *Prepared) { p.Intent = append(bytes.Clone(p.Intent), ' ') },
		func(p *Prepared) { p.Signature = string(bytes.Repeat([]byte{'A'}, 513)) },
	} {
		bad := p
		mutate(&bad)
		// A nil owner handle would panic if validation allowed delivery.
		if _, err := Submit(f.ctx, nil, bad); err == nil {
			t.Fatal("sent invalid request")
		}
		encoded, err := json.Marshal(bad)
		check(t, err)
		var got Prepared
		if json.Unmarshal(encoded, &got) == nil {
			t.Fatal("JSON accepted tampered request")
		}
	}
}

func TestSplitEnrollmentReceiptCannotReplaceAdvance(t *testing.T) {
	f := enrolled(t)
	path := filepath.Join(f.root.Path, receiptName)
	genesis := contents(t, path)
	s := f.open(t, nil)
	check(t, appendOnly(f.ctx, s, f.seed(t, s, "one")))
	check(t, s.Close())
	f.release(t)
	put(t, path, genesis)
	f.refused(t, Interrupted)
}

type epochAdmin struct {
	cp.RecoveryAdministration
	witness ed25519.PrivateKey
}

func (a epochAdmin) Status(_ context.Context, challenge cp.ID) (cp.Receipt, error) {
	s := cp.DirectoryStatus{Epoch: 1, Sequence: 1, Digest: strings.Repeat("0", 64), Inhibition: cp.Ready}
	body, err := s.Body(challenge)
	if err != nil {
		return cp.Receipt{}, err
	}
	sig, err := cp.Sign(cp.WitnessNamespace, a.witness, body)
	return cp.Receipt{Body: body, Signature: sig}, err
}

func TestSplitWrongSignedEpoch(t *testing.T) {
	f := setup(t)
	p, err := Prepare(f.ctx, f.root, f.slot, 2, f.home, pub(f.witness), nil)
	check(t, err)
	_, err = Submit(f.ctx, epochAdmin{witness: f.witness}, p)
	if !errors.Is(err, cp.ErrStale) {
		t.Fatal(err)
	}

	// The memory owner has no epoch accessor. Its valid epoch-1 receipt
	// must still fail verification against the home's durable epoch-2 request.
	q, err := p.request()
	check(t, err)
	r, err := f.w.Administration().Enroll(f.ctx, q)
	check(t, err)
	raw := binary.BigEndian.AppendUint32([]byte{1}, uint32(len(r.Body)))
	raw = append(raw, r.Body...)
	raw = append(raw, r.Signature...)
	_, err = Finalize(f.ctx, f.root, p, raw, pub(f.witness))
	if !errors.Is(err, cp.ErrUnknownOutcome) {
		t.Fatal(err)
	}
	got, err := ResumePrepared(f.ctx, f.root)
	check(t, err)
	if !reflect.DeepEqual(p, got) {
		t.Fatal("wrong epoch disturbed prepared record")
	}
}
