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
//
// # Frames
//
// After the TLS handshake the two ends exchange frames: a 4-byte big-endian
// length, then that many bytes of one JSON object. A frame is 1 to 65536
// (64 KiB) bytes; a length of zero or above that is refused before any payload
// is read. A frame is a request or a response:
//
//	{"id": 7, "op": "owner.list", "params": {...}}     params may be absent
//	{"id": 7, "result": {...}}
//	{"id": 7, "error": {"code": "no-such-card"}}
//
// The id is a number from 1 to 2^64-1, chosen by the client. An operation
// name is of the grammar ^[a-z]+\.[a-z0-9-]{1,40}$. An error code is of the
// grammar [a-z0-9-]{1,40} and is never a text for a person: nothing of a
// handler's error, a panic or a peer's bytes is put on the wire.
//
// Decoding is strict. A frame is refused, and the connection closed, if it
// is not valid UTF-8 and valid JSON; if it is not exactly one object with
// no byte before or after it; if an object at any depth has a key twice (a
// key is compared after its escapes are read); if objects and arrays nest more
// than 8 levels; if it has a field the message does not name, or names it in
// another case; if the id is not a plain whole number of 1 or more; or if the
// operation name is outside its grammar. [DecodeStrict] holds the params
// a handler reads to the same rules.
package seatgate
