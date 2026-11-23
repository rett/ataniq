// Package credkind holds the kinds of credential that may approve from the
// phone, and the WebAuthn assertion verifier they share.
//
// A credential is a WebAuthn public key credential, registered at pairing
// with attestation "none": the key, its id, and what the authenticator said
// about itself (its AAGUID and the backup flags). Nothing about the
// authenticator is proven. The AAGUID and the flags are claims, relayed by
// the seat, and a kind built on them is a claim too: every name and label
// this package gives says "claimed", and a policy on kinds is advisory. Only
// a later kind that verifies attestation can be enforced.
//
// # Kinds
//
// A Kind registers a credential from a pairing's authenticator data
// (Register) and verifies an assertion of a registered credential (Verify).
// The kinds are:
//
//   - platform-passkey: a passkey of the phone's or computer's own platform,
//     by the AAGUID it claims (the table in aaguid.go);
//   - vault-passkey: a passkey kept by a password vault, by its claimed
//     AAGUID;
//   - passkey: any other AAGUID, the all-zero one included, shown as
//     "passkey (claimed: unknown)".
//
// The table of AAGUIDs is data: it names vendors, and the package depends on
// none of them. Classify picks the kind for an AAGUID, Register registers
// with it, and Policy limits which kinds a project accepts (all by default).
//
// # The verifier
//
// Verify follows WebAuthn level 3, section 7.2, for an ES256 key on P-256:
//
//   - the assertion names the registered credential id;
//   - the client data is one JSON object, valid UTF-8, with no duplicate
//     member, whose type is "webauthn.get", whose challenge is the expected
//     one (base64url, no padding, compared in constant time), whose origin is
//     the expected one, with crossOrigin absent or false and no topOrigin;
//   - the authenticator data starts with SHA-256 of the relying party id
//     (constant time), has UP and UV set, BS only with BE, BE as registered,
//     no attested credential data, and after the counter only the extensions
//     map that ED announces, in strict canonical CBOR;
//   - the signature is a strict DER ECDSA signature, r and s in [1, n-1],
//     over authenticatorData || SHA-256(clientDataJSON), by the registered
//     SubjectPublicKeyInfo, which must be canonical DER of a P-256 key.
//
// A signature counter that does not grow is reported (Result.CounterSuspect),
// not refused: synced passkeys report 0 on every use.
//
// Errors are fixed values with no part of the input in them. The package
// uses the Go standard library only.
package credkind
