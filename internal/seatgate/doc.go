// Package seatgate is the transport between the seat app on the authority host
// and the gate on an AI host: an authenticated, framed request and response
// channel, and nothing more. It knows no card, no decision and no owner
// operation; a [Handler] gives the operations their meaning.
//
// The package is the standard library only (imports_test.go holds it to
// that). It uses package os in files.go alone, for the key files, and package
// net in the server and the client alone.
//
// # Identity and trust
//
// Each end has an [Identity]: an ECDSA P-256 key and a self-signed
// certificate. Nobody verifies the certificate by a public authority. An end
// pins the other's key by its fingerprint, the lower-case hex SHA-256 of the
// certificate's DER SubjectPublicKeyInfo (64 characters), which the owner
// carries from one end to the other at pairing time. [ServerTLS] and
// [ClientTLS] build the configurations: TLS 1.3 only, a client certificate
// required, the peer's certificate required to be exactly one certificate
// with the right extended usage whose key is pinned (compared in constant
// time), no session tickets, no renegotiation, ALPN "ataniq-gate/1". Validity
// dates and names are not looked at.
package seatgate
