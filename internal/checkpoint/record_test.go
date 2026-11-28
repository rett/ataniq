package checkpoint

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"ataniq/internal/assignment"
	"ataniq/internal/assignment/wire"
)

type vector struct {
	Name       string
	Body       json.RawMessage
	Signatures map[string]string
}

func vectors(t *testing.T) map[string]vector {
	t.Helper()
	file, err := os.Open("testdata/transcript.jsonl")
	requireOK(t, err)
	t.Cleanup(func() { requireOK(t, file.Close()) })
	out := make(map[string]vector)
	rows := bufio.NewScanner(file)
	for rows.Scan() {
		var v vector
		requireOK(t, json.Unmarshal(rows.Bytes(), &v))
		if _, ok := out[v.Name]; ok {
			t.Fatal("duplicate", v.Name)
		}
		out[v.Name] = v
	}
	requireOK(t, rows.Err())
	if len(out) != 11 {
		t.Fatal("incomplete transcript")
	}
	return out
}
func fixtureID(s string) ID { return sha256.Sum256([]byte(s)) }
func fixtureKey(role string) ed25519.PrivateKey {
	s := fixtureID("checkpoint fixture " + role)
	return ed25519.NewKeyFromSeed(s[:])
}
func public(k ed25519.PrivateKey) ed25519.PublicKey { return k.Public().(ed25519.PublicKey) }
func requireOK(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

type setup struct {
	w                      *Memory
	c                      Client
	e                      Enrollment
	home, session, witness ed25519.PrivateKey
}

func newSetup(t *testing.T) setup {
	t.Helper()
	f := setup{home: fixtureKey("home"), session: fixtureKey("session"), witness: fixtureKey("witness")}
	var err error
	f.w, err = NewMemory("witness", 1, f.witness)
	requireOK(t, err)
	slot := Slot{"witness", "home", "store"}
	requireOK(t, f.w.Grant(slot, public(f.home), true))
	f.c, err = f.w.Connect(slot, f.home, f.session, public(f.witness))
	requireOK(t, err)
	id, err := KeyID(public(f.home))
	requireOK(t, err)
	h := assignment.Header{Schema: 1, Authority: slot.Authority, Store: slot.Store, LedgerID: "ledger", Incarnation: "first", KeyID: id}
	raw, err := h.Bytes()
	requireOK(t, err)
	f.e = Enrollment{ID: fixtureID("enroll"), Slot: slot, HeaderBytes: raw, HomePublicKey: public(f.home)}
	f.e.ProofOfPossession, err = ProveEnrollment(f.home, f.e)
	requireOK(t, err)
	return f
}

func TestCanonicalTranscript(t *testing.T) {
	f := newSetup(t)
	vs := vectors(t)
	ctx := t.Context()
	wantBody := func(name string, b []byte) {
		t.Helper()
		if !bytes.Equal(b, vs[name].Body) {
			t.Fatalf("%s body:\n got %s\nwant %s", name, b, vs[name].Body)
		}
	}
	verify := func(name string, r Receipt) {
		t.Helper()
		wantBody(name, r.Body)
		if string(r.Signature) != vs[name].Signatures["witness"] {
			t.Fatal(name, "signature")
		}
	}
	var s State
	ack := func(name string, r Receipt) {
		t.Helper()
		verify(name+"-ack", r)
		a, err := VerifyReceipt(public(f.witness), r)
		requireOK(t, err)
		s = a.State
	}
	b, err := enrollmentBody(f.e, f.w.grants[f.e.Slot].keyID)
	requireOK(t, err)
	wantBody("enroll", b)
	if string(f.e.ProofOfPossession) != vs["enroll"].Signatures["home"] {
		t.Fatal("enrollment proof")
	}
	r, err := f.w.Administration().Enroll(ctx, f.e)
	requireOK(t, err)
	ack("enroll", r)
	for _, op := range []string{"claim", "advance", "release", "retire"} {
		m := Mutation{fixtureID(op), s}
		switch op {
		case "claim":
			b, err = mutationBody(op, s.HomeKeyID, m, public(f.session), nil, "")
			requireOK(t, err)
			r, err = f.c.Claim(ctx, Claim{m, public(f.session)})
		case "advance":
			next := s.Head
			next.Sequence++
			next.Digest = idString(fixtureID("next frame"))
			q := Advance{m, next}
			b, err = AdvanceBody(q)
			requireOK(t, err)
			r, err = f.c.Advance(ctx, q)
		case "release":
			b, err = mutationBody(op, f.w.keyID, m, nil, nil, "")
			requireOK(t, err)
			r, err = f.w.Administration().Release(ctx, m)
		case "retire":
			disposition := idString(fixtureID("disposition"))
			b, err = mutationBody(op, f.w.keyID, m, nil, nil, disposition)
			requireOK(t, err)
			r, err = f.w.Administration().Retire(ctx, Retirement{m, disposition})
		}
		requireOK(t, err)
		wantBody(op, b)
		ack(op, r)
		for role, sig := range vs[op].Signatures {
			p := homeProfile
			key := f.home
			if role == "session" {
				p = sessionProfile
				key = f.session
			}
			signed, err := p.sign(key, b)
			requireOK(t, err)
			if string(signed) != sig || !p.verify(public(key), b, []byte(sig)) {
				t.Fatal(op, role, "signature")
			}
		}
		if op == "advance" {
			q := ReadRequest{f.e.Slot, fixtureID("challenge"), m.ID}
			v, err := f.c.Read(ctx, q)
			requireOK(t, err)
			requireOK(t, VerifyView(public(f.witness), q, v))
			verify("read", v.Fresh)
		}
	}
}

func TestOpenSSHInterop(t *testing.T) {
	exe, err := exec.LookPath("ssh-keygen")
	if errors.Is(err, exec.ErrNotFound) {
		t.Skip("ssh-keygen is absent; OpenSSH interoperability requires it")
	}
	requireOK(t, err)
	dir := t.TempDir()
	signers := filepath.Join(dir, "allowed_signers")
	var lines strings.Builder
	for _, role := range []string{"home", "session", "witness"} {
		fmt.Fprintf(&lines, "%s ssh-ed25519 %s\n", role, base64.StdEncoding.EncodeToString(keyBlob(public(fixtureKey(role)))))
	}
	requireOK(t, os.WriteFile(signers, []byte(lines.String()), 0600))
	for _, v := range vectors(t) {
		for role, signature := range v.Signatures {
			t.Run(v.Name+"/"+role, func(t *testing.T) {
				raw, err := base64.RawURLEncoding.Strict().DecodeString(signature)
				requireOK(t, err)
				armor := "-----BEGIN SSH SIGNATURE-----\n" + base64.StdEncoding.EncodeToString(raw) + "\n-----END SSH SIGNATURE-----\n"
				sig := filepath.Join(dir, "signature")
				requireOK(t, os.WriteFile(sig, []byte(armor), 0600))
				ns := "ataniq.checkpoint." + role + ".v1"
				msg := append([]byte(ns+"\x00"), v.Body...)
				cmd := exec.Command(exe, "-Y", "verify", "-I", role, "-f", signers, "-n", ns, "-s", sig)
				cmd.Stdin = bytes.NewReader(msg)
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("%v: %s", err, out)
				}
				if wire.Verify(public(fixtureKey(role)), v.Body, armor) == nil {
					t.Fatal("accepted checkpoint signature for an assignment event")
				}
			})
		}
	}
}
func TestRejectMalformedSignedRecords(t *testing.T) {
	vs := vectors(t)
	b := vs["advance"].Body
	for name, edit := range map[string]func([]byte) []byte{
		"duplicate": func(b []byte) []byte {
			return bytes.Replace(b, []byte(`"schema":1`), []byte(`"schema":1,"schema":1`), 1)
		},
		"unknown": func(b []byte) []byte {
			return bytes.Replace(b, []byte(`"schema":1`), []byte(`"invented":1,"schema":1`), 1)
		},
		"wrong case":   func(b []byte) []byte { return bytes.Replace(b, []byte(`"audience"`), []byte(`"Audience"`), 1) },
		"missing":      func(b []byte) []byte { return bytes.Replace(b, []byte(`"epoch":"1",`), nil, 1) },
		"leading zero": func(b []byte) []byte { return bytes.Replace(b, []byte(`"epoch":"1"`), []byte(`"epoch":"01"`), 1) },
		"overflow": func(b []byte) []byte {
			return bytes.Replace(b, []byte(`"epoch":"1"`), []byte(`"epoch":"18446744073709551616"`), 1)
		},
		"number": func(b []byte) []byte { return bytes.Replace(b, []byte(`"epoch":"1"`), []byte(`"epoch":1`), 1) },
		"unicode": func(b []byte) []byte {
			return bytes.Replace(b, []byte(`"audience":"witness"`), []byte(`"audience":"\uD800"`), 1)
		},
		"utf8":            func(b []byte) []byte { return bytes.Replace(b, []byte("witness"), []byte{255}, 1) },
		"depth":           func([]byte) []byte { return []byte(strings.Repeat("[", 9) + "0" + strings.Repeat("]", 9)) },
		"size":            func([]byte) []byte { return bytes.Repeat([]byte(" "), maxFrame+1) },
		"wrong operation": func([]byte) []byte { return vs["claim"].Body },
		"wrong audience": func(b []byte) []byte {
			return bytes.Replace(b, []byte(`"audience":"witness"`), []byte(`"audience":"elsewhere"`), 1)
		},
		"extra member for operation": func(b []byte) []byte {
			return bytes.Replace(b, []byte(`"schema":1}`), []byte(`"schema":1,"session_public_key":"extraneous"}`), 1)
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseIntent("advance", "", edit(b)); !errors.Is(err, ErrRefused) {
				t.Fatal(err)
			}
		})
	}
	for _, name := range []string{"enroll-ack", "advance-ack", "retire-ack"} {
		v := vs[name]
		r := Receipt{v.Body, []byte(v.Signatures["witness"])}
		for _, bad := range [][]byte{append(bytes.Clone(r.Signature), '='), append(bytes.Clone(r.Signature), '\n'), []byte("wrong")} {
			if _, err := VerifyReceipt(public(fixtureKey("witness")), Receipt{r.Body, bad}); !errors.Is(err, ErrRefused) {
				t.Fatal(name, "signature encoding")
			}
		}
		if _, err := VerifyReceipt(public(fixtureKey("home")), r); !errors.Is(err, ErrRefused) {
			t.Fatal("wrong witness pin")
		}
	}
}

func TestCounterExhaustion(t *testing.T) {
	for _, field := range []string{"sequence", "revision", "fence"} {
		t.Run(field, func(t *testing.T) {
			f := newSetup(t)
			ctx := t.Context()
			_, err := f.w.Administration().Enroll(ctx, f.e)
			requireOK(t, err)
			e := f.w.slots[f.e.Slot]
			if field == "fence" {
				e.state.Fence = math.MaxUint64
				_, err = f.c.Claim(ctx, Claim{Mutation{fixtureID("claim"), e.state}, public(f.session)})
			} else {
				_, err = f.c.Claim(ctx, Claim{Mutation{fixtureID("claim"), e.state}, public(f.session)})
				requireOK(t, err)
				if field == "sequence" {
					e.state.Head.Sequence = math.MaxUint64
				} else {
					e.state.Revision = math.MaxUint64
				}
				next := e.state.Head
				next.Sequence++
				next.Digest = idString(fixtureID("next"))
				_, err = f.c.Advance(ctx, Advance{Mutation{fixtureID("advance"), e.state}, next})
			}
			if !errors.Is(err, ErrStale) {
				t.Fatal("wrapped", field, err)
			}
		})
	}
}

func TestWitnessChecksBothSignaturesAndOperation(t *testing.T) {
	f := newSetup(t)
	accept := func(ctx context.Context, op string, b, hs, ss []byte) (Receipt, error) {
		return f.w.accept(ctx, f.e.Slot, public(f.home), op, b, hs, ss)
	}
	v := vectors(t)
	ctx := t.Context()
	_, err := f.w.Administration().Enroll(ctx, f.e)
	requireOK(t, err)
	claim := v["claim"]
	sessionInHomeProfile, err := homeProfile.sign(f.session, claim.Body)
	requireOK(t, err)
	for _, sessionSig := range [][]byte{nil, sessionInHomeProfile} {
		_, err := accept(ctx, "claim", claim.Body, []byte(claim.Signatures["home"]), sessionSig)
		if !errors.Is(err, ErrRefused) {
			t.Fatalf("claim session proof: got %v, want ErrRefused", err)
		}
	}
	_, err = accept(ctx, "claim", claim.Body, []byte(claim.Signatures["home"]), []byte(claim.Signatures["session"]))
	requireOK(t, err)
	advance := v["advance"]
	hs, ss := []byte(advance.Signatures["home"]), []byte(advance.Signatures["session"])
	homeInSessionProfile, err := sessionProfile.sign(f.home, advance.Body)
	requireOK(t, err)
	for _, tc := range []struct {
		op                  string
		body, home, session []byte
	}{
		{"claim", advance.Body, hs, ss},
		{"advance", advance.Body, homeInSessionProfile, ss},
		{"advance", advance.Body, hs, homeInSessionProfile},
		{"advance", advance.Body, nil, ss},
		{"advance", advance.Body, hs, nil},
		{"advance", claim.Body, []byte(claim.Signatures["home"]), []byte(claim.Signatures["session"])},
	} {
		if _, err := accept(ctx, tc.op, tc.body, tc.home, tc.session); !errors.Is(err, ErrRefused) {
			t.Fatal("invalid proof accepted", tc.op, err)
		}
	}
	r, err := accept(ctx, "advance", advance.Body, hs, ss)
	requireOK(t, err)
	if !bytes.Equal(r.Body, v["advance-ack"].Body) {
		t.Fatal("refusal consumed the mutation")
	}
}

func TestReturnedEvidenceIsIndependent(t *testing.T) {
	f := newSetup(t)
	ctx := t.Context()
	r, err := f.w.Administration().Enroll(ctx, f.e)
	requireOK(t, err)
	want := Receipt{bytes.Clone(r.Body), bytes.Clone(r.Signature)}
	r.Body[0] ^= 1
	r.Signature[0] ^= 1
	q := ReadRequest{f.e.Slot, fixtureID("first read"), f.e.ID}
	v, err := f.c.Read(ctx, q)
	requireOK(t, err)
	v.Prior.Body[0] ^= 1
	v.Prior.Signature[0] ^= 1
	q.Challenge = fixtureID("second read")
	v, err = f.c.Read(ctx, q)
	requireOK(t, err)
	if !bytes.Equal(v.Prior.Body, want.Body) || !bytes.Equal(v.Prior.Signature, want.Signature) {
		t.Fatal("caller corrupted retained receipt")
	}
	requireOK(t, VerifyView(public(f.witness), q, v))
}

// The enrollment acknowledgement is the signed golden row from 4580bd22.
func TestLegacyReceiptFormat(t *testing.T) {
	data, err := os.ReadFile("testdata/legacy-enroll-ack.json")
	requireOK(t, err)
	var old vector
	requireOK(t, json.Unmarshal(data, &old))
	r := Receipt{old.Body, []byte(old.Signatures["witness"])}
	pin := public(fixtureKey("witness"))
	requireOK(t, VerifySignature(WitnessNamespace, pin, r.Body, r.Signature))
	_, verify := VerifyReceipt(pin, r)
	match := MatchReceipt(pin, 1, "enroll", vectors(t)["enroll"].Body, r)
	for _, err := range []error{verify, match} {
		if !errors.Is(err, ErrLegacyFormat) {
			t.Errorf("missing legacy receipt diagnosis: %v", err)
		}
	}
	if !errors.Is(match, ErrUnknownOutcome) {
		t.Fatal("legacy evidence resolved delivery", match)
	}
	r.Signature[0] ^= 1
	if _, err := VerifyReceipt(pin, r); !errors.Is(err, ErrRefused) || errors.Is(err, ErrLegacyFormat) {
		t.Fatal("classified unauthenticated evidence as legacy", err)
	}
}
