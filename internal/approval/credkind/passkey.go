package credkind

import (
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/subtle"
)

// typeGet is the client data type of an assertion.
const typeGet = "webauthn.get"

// passkey is a kind of WebAuthn passkey with an ES256 key on P-256, told
// apart from the other passkey kinds only by the AAGUID it claims. The three
// kinds verify the same way.
type passkey struct {
	name string
}

func (k passkey) Name() string { return k.name }

// Register registers a pairing's credential (see registerCredential) and
// refuses it with ErrWrongKind when its AAGUID claims another kind.
func (k passkey) Register(in RegistrationInput) (Credential, error) {
	c, err := registerCredential(in)
	if err != nil {
		return Credential{}, err
	}
	if c.Kind != k.name {
		return Credential{}, ErrWrongKind
	}
	return c, nil
}

// registerCredential checks a registration's authenticator data against the
// relying party and what the browser reported (WebAuthn level 3, section
// 7.1, the steps that hold with attestation "none"): the rpIdHash, UP and UV
// set, BS only with BE, attested credential data whose id is the reported id
// and whose COSE_Key is the reported key, byte for byte. The credential's
// kind is the one its AAGUID claims.
func registerCredential(in RegistrationInput) (Credential, error) {
	if in.RPID == "" {
		return Credential{}, ErrExpect
	}
	if len(in.CredentialID) < minCredentialID || len(in.CredentialID) > maxCredentialID {
		return Credential{}, ErrCredential
	}
	ad, err := parseRegistrationAuthData(in.AuthenticatorData)
	if err != nil {
		return Credential{}, err
	}
	if err := checkFlags(ad, in.RPID); err != nil {
		return Credential{}, err
	}
	if subtle.ConstantTimeCompare(ad.credID, in.CredentialID) != 1 {
		return Credential{}, ErrCredential
	}
	// With attestation "none" nothing is signed at registration: what binds
	// the id to the key is that the browser's key is the authenticator's.
	if _, ok := parseSPKI(in.SPKI); !ok || subtle.ConstantTimeCompare(ad.spki, in.SPKI) != 1 {
		return Credential{}, ErrKey
	}
	return Credential{
		Kind:           Lookup(ad.aaguid).Kind,
		CredentialID:   append([]byte(nil), in.CredentialID...),
		SPKI:           append([]byte(nil), in.SPKI...),
		AAGUID:         ad.aaguid,
		BackupEligible: ad.flags&flagBE != 0,
		BackupState:    ad.flags&flagBS != 0,
		UserVerified:   ad.flags&flagUV != 0,
		SignCount:      ad.count,
	}, nil
}

// checkFlags checks the rpIdHash and the flags both ceremonies require.
func checkFlags(ad authData, rpID string) error {
	sum := sha256.Sum256([]byte(rpID))
	if subtle.ConstantTimeCompare(ad.rpIDHash, sum[:]) != 1 {
		return ErrRPID
	}
	if ad.flags&flagUP == 0 {
		return ErrUserPresence
	}
	if ad.flags&flagUV == 0 {
		return ErrUserVerification
	}
	// Section 6.1.3: a credential that is backed up is backup eligible.
	if ad.flags&flagBS != 0 && ad.flags&flagBE == 0 {
		return ErrBackup
	}
	return nil
}

// Verify verifies an assertion of a credential of this kind (WebAuthn level
// 3, section 7.2).
func (k passkey) Verify(c Credential, in AssertionInput, e Expect) (Result, error) {
	if c.Kind != k.name {
		return Result{}, ErrKind
	}
	if !e.valid() {
		return Result{}, ErrExpect
	}
	if len(c.CredentialID) < minCredentialID || len(c.CredentialID) > maxCredentialID ||
		subtle.ConstantTimeCompare(in.CredentialID, c.CredentialID) != 1 {
		return Result{}, ErrCredential
	}
	pub, ok := parseSPKI(c.SPKI)
	if !ok {
		return Result{}, ErrKey
	}
	if err := checkClientData(in.ClientDataJSON, typeGet, e); err != nil {
		return Result{}, err
	}
	ad, err := parseAssertionAuthData(in.AuthenticatorData)
	if err != nil {
		return Result{}, err
	}
	if err := checkFlags(ad, e.RPID); err != nil {
		return Result{}, err
	}
	// Section 7.2, step 18: the BE flag does not change over a credential's
	// life.
	if (ad.flags&flagBE != 0) != c.BackupEligible {
		return Result{}, ErrBackup
	}
	r, s, ok := parseSignature(in.Signature)
	if !ok {
		return Result{}, ErrSignature
	}
	cdHash := sha256.Sum256(in.ClientDataJSON)
	h := sha256.New()
	h.Write(in.AuthenticatorData)
	h.Write(cdHash[:])
	if !ecdsa.Verify(pub, h.Sum(nil), r, s) {
		return Result{}, ErrSignature
	}
	return Result{
		UserVerified:   true,
		Counter:        ad.count,
		CounterSuspect: (ad.count != 0 || c.SignCount != 0) && ad.count <= c.SignCount,
		BackupState:    ad.flags&flagBS != 0,
	}, nil
}
