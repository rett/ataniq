package credkind

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/binary"
	"encoding/hex"
	"strings"
	"testing"
)

// These software fixtures implement the OpenSSH PROTOCOL.sshsig and
// PROTOCOL.u2f layouts independently of the verifier. No hardware is used.
func vectorFields(fields ...[]byte) []byte {
	var out bytes.Buffer
	for _, field := range fields {
		_ = binary.Write(&out, binary.BigEndian, uint32(len(field)))
		out.Write(field)
	}
	return out.Bytes()
}

func vectorDecision(d ScratchSKDecision, kind, route string) []byte {
	number := func(n uint64) []byte { return binary.BigEndian.AppendUint64(nil, n) }
	return vectorFields([]byte(kind), []byte(route), []byte(d.HomeID), d.GateFingerprint[:],
		[]byte(d.PrincipalID), []byte(d.DeviceID), []byte(d.CredentialID), []byte(d.SessionID),
		d.ExporterSHA256[:], []byte(d.PendingID), []byte(d.Decision), number(d.ExpectedRevision),
		number(d.PolicyRevision), number(uint64(d.ExpiresUnix)), d.FrameSHA256[:], d.Nonce[:])
}

type skVector struct {
	algorithm, application, namespace, reserved, hash string
	flags                                             byte
	counter                                           uint32
}

func (v skVector) sign(key ed25519.PrivateKey, msg []byte) []byte {
	alg := []byte(v.algorithm)
	pub := key.Public().(ed25519.PublicKey)
	publicBlob := vectorFields(alg, pub, []byte(v.application))
	header := vectorFields([]byte(v.namespace), []byte(v.reserved), []byte(v.hash))
	messageHash := sha512.Sum512(msg)
	digest := messageHash[:]
	if v.hash == "sha256" {
		sum := sha256.Sum256(msg)
		digest = sum[:]
	}
	signedData := bytes.Join([][]byte{[]byte("SSHSIG"), header, vectorFields(digest)}, nil)

	appHash := sha256.Sum256([]byte(v.application))
	dataHash := sha256.Sum256(signedData)
	trailer := binary.BigEndian.AppendUint32([]byte{v.flags}, v.counter)
	sig := ed25519.Sign(key, bytes.Join([][]byte{appHash[:], trailer, dataHash[:]}, nil))
	signatureBlob := append(vectorFields(alg, sig), trailer...)
	return bytes.Join([][]byte{[]byte("SSHSIG\x00\x00\x00\x01"), vectorFields(publicBlob), header, vectorFields(signatureBlob)}, nil)
}

func TestCF01(t *testing.T) {
	seed := sha256.Sum256([]byte("CF01 scratch vector fixture"))
	key := ed25519.NewKeyFromSeed(seed[:])
	pub := key.Public().(ed25519.PublicKey)
	d := ScratchSKDecision{
		HomeID: "home", GateFingerprint: sha256.Sum256([]byte("gate")),
		PrincipalID: "owner", DeviceID: "laptop", CredentialID: "scratch-key", SessionID: "session",
		ExporterSHA256: sha256.Sum256([]byte("exporter")), PendingID: "scratch-card", Decision: "approve",
		ExpectedRevision: 7, PolicyRevision: 3, ExpiresUnix: 1800000000,
		FrameSHA256: sha256.Sum256([]byte("displayed frame")), Nonce: [32]byte{1},
	}
	// Model a separately witnessed enrollment for the positive format cases.
	cred := ScratchSKCredential{ID: d.CredentialID, PublicKey: pub, Provenance: SKWitnessed, Counter: 0x1020303}
	scope := ScratchSKScope{
		AllowFeatureFinish: true, DeskClass: "laptop", DisposableLocal: true, CardClass: "ref-move", Verb: "feature finish",
	}
	v := skVector{"sk-ssh-ed25519@openssh.com", "ssh:ataniq-console", "ataniq-console-decision-v1", "", "sha512", 0x05, 0x1020304}
	msg := vectorDecision(d, "security-key", "console-decision")
	sig := v.sign(key, msg)
	refuse := func(t *testing.T, c ScratchSKCredential, expected ScratchSKDecision, blob []byte) error {
		t.Helper()
		counter, err := VerifyScratchSK(c, expected, scope, blob)
		if err == nil || counter != 0 {
			t.Fatalf("accepted invalid vector or returned counter on failure: %v, %d", err, counter)
		}
		return err
	}

	t.Run("SKVector", func(t *testing.T) {
		// Digest of the same fixture independently encoded and signed with libsodium.
		sum := sha256.Sum256(sig)
		if hex.EncodeToString(sum[:]) != "ec117165d792718bd67f898e6e599a8c84871065ea2dda328fa667c4a8416206" {
			t.Fatalf("SSHSIG vector digest %x", sum)
		}
		got, err := d.SigningBytes()
		if err != nil || !bytes.Equal(got, msg) {
			t.Fatal("desk signing bytes differ from vector", err)
		}
		if count, err := VerifyScratchSK(cred, d, scope, sig); err != nil || count != v.counter {
			t.Fatalf("got counter %d, err %v", count, err)
		}
		c := cred
		c.PublicKey = nil
		refuse(t, c, d, sig)
		other, _, err := ed25519.GenerateKey(nil)
		if err != nil {
			t.Fatal(err)
		}
		c.PublicKey = other
		refuse(t, c, d, sig)
		c = cred
		c.ID = "another-key"
		refuse(t, c, d, sig)
		for i := range len(sig) {
			refuse(t, cred, d, sig[:i])
			damaged := bytes.Clone(sig)
			damaged[i] ^= 0x80
			refuse(t, cred, d, damaged)
		}
		refuse(t, cred, d, append(bytes.Clone(sig), 0))
	})

	t.Run("InvalidKey", func(t *testing.T) {
		zeros, ones := strings.Repeat("00", 30), strings.Repeat("ff", 30)
		for name, encoded := range map[string]string{
			"identity":       "01" + zeros + "00",
			"order2":         "ec" + ones + "7f",
			"order4positive": "00" + zeros + "00",
			"order4negative": "00" + zeros + "80",
			"order8a":        "26e8958fc2b227b045c3f489f2ef98f0d5dfac05d3c63339b13802886d53fc05",
			"order8b":        "26e8958fc2b227b045c3f489f2ef98f0d5dfac05d3c63339b13802886d53fc85",
			"order8c":        "c7176a703d4dd84fba3c0b760d10670f2a2053fa2c39ccc64ec7fd7792ac037a",
			"order8d":        "c7176a703d4dd84fba3c0b760d10670f2a2053fa2c39ccc64ec7fd7792ac03fa",
			"negativeZero":   "01" + zeros + "80",
			"unreduced":      "f6" + ones + "7f",
			"offCurve":       "02" + zeros + "00",
			"short":          hex.EncodeToString(pub[:31]),
		} {
			t.Run(name, func(t *testing.T) {
				c := cred
				var err error
				c.PublicKey, err = hex.DecodeString(encoded)
				if err != nil {
					t.Fatal(err)
				}
				if len(c.PublicKey) != ed25519.PublicKeySize {
					// Key validation precedes even a missing signature tail.
					if _, err := VerifyScratchSK(c, d, scope, skPrefix(c.PublicKey)); err != ErrKey {
						t.Fatalf("malformed key length: got %v, want %v", err, ErrKey)
					}
				}
				// Keep the signed fixture's suffix. The malformed enrolled key
				// must be rejected even when the SSHSIG header matches it.
				blob := append(skPrefix(c.PublicKey), sig[len(skPrefix(pub)):]...)
				wantErr(t, "stored key", refuse(t, c, d, blob), ErrKey)
				wantErr(t, "enrollment key", ValidateScratchSKKey(c.PublicKey), ErrKey)
			})
		}
		if err := ValidateScratchSKKey(pub); err != nil {
			t.Fatalf("valid enrollment key: %v", err)
		}
	})

	t.Run("BadCounter", func(t *testing.T) {
		for _, pair := range [][2]uint32{{0, 0}, {8, 7}, {8, 8}, {0xffffffff, 1}} {
			c, vector := cred, v
			c.Counter, vector.counter = pair[0], pair[1]
			wantErr(t, "non-advancing counter", refuse(t, c, d, vector.sign(key, msg)), ErrSKCounter)
		}
		forged := bytes.Clone(sig)
		binary.BigEndian.PutUint32(forged[len(forged)-4:], v.counter+1)
		wantErr(t, "altered counter", refuse(t, cred, d, forged), ErrSignature)
	})

	t.Run("Flags", func(t *testing.T) {
		for flags, want := range map[byte]error{0: ErrUserPresence, 1: ErrUserVerification, 4: ErrUserPresence} {
			vector := v
			vector.flags = flags
			b := vector.sign(key, msg)
			wantErr(t, "signed flags", refuse(t, cred, d, b), want)
			b[len(b)-5] = 0x05
			wantErr(t, "altered flags", refuse(t, cred, d, b), ErrSignature)
		}
	})

	t.Run("Namespace", func(t *testing.T) {
		for _, change := range []func(*skVector){
			func(v *skVector) { v.namespace = "ataniq-console-login-v1" },
			func(v *skVector) { v.application = "ssh:other" },
			func(v *skVector) { v.algorithm = "ssh-ed25519" },
			func(v *skVector) { v.reserved = "extension" },
			func(v *skVector) { v.hash = "sha256" },
		} {
			vector := v
			change(&vector)
			refuse(t, cred, d, vector.sign(key, msg))
		}
	})

	t.Run("WrongExporter", func(t *testing.T) {
		other := d
		other.ExporterSHA256 = sha256.Sum256([]byte("second connection"))
		refuse(t, cred, other, sig)
	})

	t.Run("CrossRoute", func(t *testing.T) {
		for _, pair := range [][2]string{{"security-key", "seat-decision"}, {"security-key", "console-login"}, {"passkey", "console-decision"}} {
			refuse(t, cred, d, v.sign(key, vectorDecision(d, pair[0], pair[1])))
		}
		_, err := Verify(Credential{Kind: NameSecurityKey}, AssertionInput{Signature: sig}, Expect{})
		wantErr(t, "SK in WebAuthn verifier", err, ErrKind)
	})

	t.Run("SoftwareProvenance", func(t *testing.T) {
		for _, provenance := range []SKProvenance{"", "software", "attested", "unrecognized"} {
			c := cred
			c.Provenance = provenance
			wantErr(t, "unqualified key", refuse(t, c, d, sig), ErrSKProvenance)
		}
	})

	t.Run("BoundDecision", func(t *testing.T) {
		for _, change := range []func(*ScratchSKDecision){
			func(d *ScratchSKDecision) { d.HomeID += "2" },
			func(d *ScratchSKDecision) { d.GateFingerprint[5]++ },
			func(d *ScratchSKDecision) { d.PrincipalID += "2" },
			func(d *ScratchSKDecision) { d.DeviceID += "2" },
			func(d *ScratchSKDecision) { d.CredentialID += "2" },
			func(d *ScratchSKDecision) { d.SessionID += "2" },
			func(d *ScratchSKDecision) { d.PendingID += "2" },
			func(d *ScratchSKDecision) { d.Decision = "deny" },
			func(d *ScratchSKDecision) { d.ExpectedRevision++ },
			func(d *ScratchSKDecision) { d.PolicyRevision++ },
			func(d *ScratchSKDecision) { d.ExpiresUnix++ },
			func(d *ScratchSKDecision) { d.FrameSHA256[5]++ },
			func(d *ScratchSKDecision) { d.Nonce[5]++ },
		} {
			other, c := d, cred
			change(&other)
			c.ID = other.CredentialID
			refuse(t, c, other, sig)
		}
	})

	t.Run("IncompleteDecision", func(t *testing.T) {
		for _, change := range []func(*ScratchSKDecision){
			func(d *ScratchSKDecision) { d.HomeID = "" },
			func(d *ScratchSKDecision) { d.SessionID = strings.Repeat("s", 1025) },
			func(d *ScratchSKDecision) { d.Nonce = [32]byte{} },
			func(d *ScratchSKDecision) { d.ExporterSHA256 = [32]byte{} },
			func(d *ScratchSKDecision) { d.Decision = "login" },
			func(d *ScratchSKDecision) { d.ExpiresUnix = 0 },
		} {
			other := d
			change(&other)
			_, err := other.SigningBytes()
			wantErr(t, "incomplete or invalid expectation", err, ErrExpect)
			b := v.sign(key, vectorDecision(other, "security-key", "console-decision"))
			wantErr(t, "signed malformed decision", refuse(t, cred, other, b), ErrExpect)
		}
	})

	t.Run("ScratchScope", func(t *testing.T) {
		for name, change := range map[string]func(*ScratchSKScope){
			"empty":        func(s *ScratchSKScope) { *s = ScratchSKScope{} },
			"noOptIn":      func(s *ScratchSKScope) { s.AllowFeatureFinish = false },
			"aiHost":       func(s *ScratchSKScope) { s.DeskClass = "ai-host" },
			"unknownDesk":  func(s *ScratchSKScope) { s.DeskClass = "" },
			"outsideTrial": func(s *ScratchSKScope) { s.DisposableLocal = false },
			"otherClass":   func(s *ScratchSKScope) { s.CardClass = "publish" },
			"otherVerb":    func(s *ScratchSKScope) { s.Verb = "feature start" },
		} {
			t.Run(name, func(t *testing.T) {
				other := scope
				change(&other)
				if counter, err := VerifyScratchSK(cred, d, other, sig); counter != 0 || err != ErrNotAccepted {
					t.Fatalf("ineligible desk or card: counter %d, err %v", counter, err)
				}
			})
		}
	})

	t.Run("KindPolicy", func(t *testing.T) {
		sk := Policy{Accepted: []string{NameSecurityKey}}
		for _, policy := range []Policy{{}, sk, {Accepted: []string{NameVault, NameSecurityKey}}} {
			if policy.Accepts(NameSecurityKey) {
				t.Errorf("phone policy %v accepts security-key", policy.Accepted)
			}
			if len(policy.Accepted) > 0 {
				wantErr(t, "security-key in phone settings", policy.Check(), ErrPolicy)
			}
		}
		for _, k := range Kinds() {
			if sk.Accepts(k.Name()) || (Policy{Accepted: []string{k.Name()}}).Accepts(NameSecurityKey) {
				t.Fatalf("policy confused %s with security-key", k.Name())
			}
		}
		if _, ok := ByName(NameSecurityKey); ok {
			t.Fatal("SK in WebAuthn registry")
		}
		wantErr(t, "duplicate SK", (Policy{Accepted: []string{NameSecurityKey, NameSecurityKey}}).Check(), ErrPolicy)
		_, err := sk.Register(regInput(newAuth(t).Create(chal(1))))
		wantErr(t, "passkey on SK-only policy", err, ErrNotAccepted)
	})
}
