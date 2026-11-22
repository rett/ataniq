package credkind

import (
	"errors"
)

// The bounds of a credential id, in bytes. WebAuthn level 3, section 4,
// allows at most 1023; the wire of the pairing (ownerwire) and the key list
// (ownerjournal) hold the same bounds, so that a credential that is paired can
// be verified.
const (
	MinCredentialIDBytes = 16
	MaxCredentialIDBytes = 1023
)

// The names of the kinds.
const (
	NamePlatform = "platform-passkey"
	NameVault    = "vault-passkey"
	NameUnknown  = "passkey"
)

// The errors. Each is a fixed value that holds no part of the input.
var (
	ErrExpect           = errors.New("credkind: expectation incomplete")
	ErrClientData       = errors.New("credkind: client data malformed")
	ErrType             = errors.New("credkind: client data of another ceremony")
	ErrChallenge        = errors.New("credkind: challenge mismatch")
	ErrOrigin           = errors.New("credkind: origin mismatch")
	ErrCrossOrigin      = errors.New("credkind: made in another site's frame")
	ErrAuthData         = errors.New("credkind: authenticator data malformed")
	ErrRPID             = errors.New("credkind: relying party mismatch")
	ErrUserPresence     = errors.New("credkind: user presence missing")
	ErrUserVerification = errors.New("credkind: user verification missing")
	ErrBackup           = errors.New("credkind: backup flags inconsistent")
	ErrCredential       = errors.New("credkind: credential id mismatch")
	ErrKey              = errors.New("credkind: public key invalid")
	ErrSignature        = errors.New("credkind: signature invalid")
	ErrKind             = errors.New("credkind: unknown kind")
	ErrWrongKind        = errors.New("credkind: credential not of this kind")
	ErrNotAccepted      = errors.New("credkind: kind not accepted by the policy")
	ErrPolicy           = errors.New("credkind: policy names an unknown kind or a kind twice")
)

// Credential is a registered credential: what is stored at pairing.
type Credential struct {
	// Kind is the name of the credential's kind, a claim (see the package
	// documentation).
	Kind         string
	CredentialID []byte
	// SPKI is the credential's public key, canonical DER
	// SubjectPublicKeyInfo of a P-256 key.
	SPKI []byte
	// AAGUID is the authenticator's claimed model.
	AAGUID [16]byte
	// BackupEligible, BackupState and UserVerified are the BE, BS and UV
	// flags of the registration, as the authenticator claimed them.
	BackupEligible bool
	BackupState    bool
	UserVerified   bool
	// SignCount is the signature counter last seen.
	SignCount uint32
}

// Claim is the claim of the credential's AAGUID.
func (c Credential) Claim() Claim { return Lookup(c.AAGUID) }

// RegistrationInput is what a pairing hands over from the browser's
// registration response, with attestation "none". The client data of the
// registration is checked where its challenge was issued (the seat), not
// here.
type RegistrationInput struct {
	// RPID is the relying party id the credential is for.
	RPID string
	// CredentialID is the id the browser reported.
	CredentialID []byte
	// SPKI is the public key the browser reported (getPublicKey), DER.
	SPKI []byte
	// AuthenticatorData is the registration's authenticator data.
	AuthenticatorData []byte
}

// AssertionInput is the browser's assertion response, as raw bytes.
type AssertionInput struct {
	CredentialID      []byte
	ClientDataJSON    []byte
	AuthenticatorData []byte
	Signature         []byte
}

// Expect is what the relying party expects of an assertion: its own
// challenge, origin and relying party id, never values the client sent.
type Expect struct {
	// Challenge is the challenge as bytes, 16 to 64 of them.
	Challenge []byte
	// Origin is the exact origin, scheme, host and any port.
	Origin string
	// RPID is the relying party id.
	RPID string
}

// The bounds of an expected challenge.
const (
	minChallenge = 16
	maxChallenge = 64
)

func (e Expect) valid() bool {
	return len(e.Challenge) >= minChallenge && len(e.Challenge) <= maxChallenge &&
		e.Origin != "" && e.RPID != ""
}

// Result is what a verified assertion says.
type Result struct {
	// UserVerified is the UV flag; Verify refuses an assertion without it,
	// so it is always true on success.
	UserVerified bool
	// Counter is the assertion's signature counter.
	Counter uint32
	// CounterSuspect reports a counter that did not grow past the stored
	// SignCount while one of the two is not zero: a sign that the key may
	// have been copied, or of a synced passkey on another device. It is
	// reported, not refused; synced passkeys report 0 on every use.
	CounterSuspect bool
	// BackupState is the BS flag of the assertion, which may change over a
	// credential's life.
	BackupState bool
}

// Kind is a kind of credential.
type Kind interface {
	// Name is the kind's name, as a policy lists it.
	Name() string
	// Register checks a pairing's registration and returns the credential,
	// refused with ErrWrongKind when its AAGUID claims another kind.
	Register(RegistrationInput) (Credential, error)
	// Verify verifies an assertion of a credential of this kind.
	Verify(Credential, AssertionInput, Expect) (Result, error)
}

// kinds are the kinds, in the order Kinds lists them.
var kinds = []Kind{
	passkey{name: NamePlatform},
	passkey{name: NameVault},
	passkey{name: NameUnknown},
}

// Kinds returns every kind.
func Kinds() []Kind { return append([]Kind(nil), kinds...) }

// ByName returns the kind of a name.
func ByName(name string) (Kind, bool) {
	for _, k := range kinds {
		if k.Name() == name {
			return k, true
		}
	}
	return nil, false
}

// Classify returns the kind an AAGUID claims.
func Classify(aaguid [16]byte) Kind {
	k, _ := ByName(Lookup(aaguid).Kind)
	return k
}

// Register registers a pairing's credential with the kind its AAGUID claims.
func Register(in RegistrationInput) (Credential, error) {
	return registerCredential(in)
}

// Verify verifies an assertion with the credential's kind. It does not read
// a policy; Policy.Verify does.
func Verify(c Credential, in AssertionInput, e Expect) (Result, error) {
	k, ok := ByName(c.Kind)
	if !ok {
		return Result{}, ErrKind
	}
	return k.Verify(c, in, e)
}

// Policy is the kinds a project accepts. With no names it accepts every kind.
// A name that is not a kind matches nothing, so a policy that names only
// unknown kinds accepts nothing; Check finds such a name.
type Policy struct {
	Accepted []string
}

// Check reports ErrPolicy when the policy names a kind that does not exist,
// or a kind twice.
func (p Policy) Check() error {
	seen := map[string]bool{}
	for _, name := range p.Accepted {
		if _, ok := ByName(name); !ok || seen[name] {
			return ErrPolicy
		}
		seen[name] = true
	}
	return nil
}

// Accepts reports whether the policy accepts the kind of a name.
func (p Policy) Accepts(name string) bool {
	if _, ok := ByName(name); !ok {
		return false
	}
	if len(p.Accepted) == 0 {
		return true
	}
	for _, a := range p.Accepted {
		if a == name {
			return true
		}
	}
	return false
}

// Register registers a credential as Register does, then refuses it with
// ErrNotAccepted when the policy does not accept its kind.
func (p Policy) Register(in RegistrationInput) (Credential, error) {
	c, err := Register(in)
	if err != nil {
		return Credential{}, err
	}
	if !p.Accepts(c.Kind) {
		return Credential{}, ErrNotAccepted
	}
	return c, nil
}

// Verify refuses a credential whose kind the policy does not accept with
// ErrNotAccepted, and otherwise verifies the assertion as Verify does.
func (p Policy) Verify(c Credential, in AssertionInput, e Expect) (Result, error) {
	if _, ok := ByName(c.Kind); !ok {
		return Result{}, ErrKind
	}
	if !p.Accepts(c.Kind) {
		return Result{}, ErrNotAccepted
	}
	return Verify(c, in, e)
}
