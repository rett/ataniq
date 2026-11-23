// Package webauthntest is a software WebAuthn authenticator for tests. It
// holds an ES256 key on the P-256 curve and a credential id, and it makes the
// responses a browser hands a web page: the registration of
// navigator.credentials.create (client data, authenticator data with the
// attested credential data, and the public key as a DER
// SubjectPublicKeyInfo, the value of the browser's getPublicKey) and the
// assertion of navigator.credentials.get (client data, authenticator data and
// the DER signature over authenticatorData || SHA-256(clientDataJSON)).
//
// The test chooses the flags, the AAGUID, the signature counter, the relying
// party id and the origin, and may build any part by hand (ClientData,
// RegistrationAuthData, AssertionAuthData) and sign it (Sign), so that a
// verifier's every check can be met or broken one at a time, without a
// browser and without a phone.
//
// It is for tests only. Its key is in memory, it asks no one before it
// signs, and it signs whatever it is given: no code that ships may import
// it. It uses the Go standard library only, and it shares no code with the
// verifier it serves (internal/approval/credkind), so that a mistake in one
// is not repeated in the other.
package webauthntest
