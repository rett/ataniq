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
//
// # Hello and heartbeat
//
// The client speaks first. Its first request is seat.hello with the params
// {"proto": 1, "caps": [...], "role": "seat"}; the server answers
// {"proto": 1, "caps": [...]}, its capability list. A hello for another major
// version is refused with the code proto-unsupported, a hello that is not
// as specified with bad-hello, a second hello with hello-repeated; and any
// other operation before the hello is refused with hello-required. After a
// refused hello, or a request before it, the server closes the connection. The
// capability list is how a later minor addition is found without a version bump:
// operation names are versioned by their own names, and a capability names a
// group of them.
//
// seat.ping needs no params and is answered with an empty result. The client
// pings every 15 s and treats 45 s without any frame from the server as a
// dead connection; the server closes a connection from which it has received
// no frame for 60 s. The server sends no request of its own.
//
// # The server
//
// [Serve] takes a listener and a [Handler]. A request other than the two
// above goes to the handler, in its own goroutine, with the [Peer] (the pinned
// fingerprint of the client) and the params; the answer is the handler's result
// object, or a code: the code of a [CodedError], else failed. A panic in a
// handler is answered failed and the connection stays. The limits, each with
// a field in [ServerConfig]:
//
//	connections open at once                8   (the ninth is closed on arrival)
//	handlers running at once, per connection 16  (more is answered busy)
//	handlers running at once, in all        128 (8 × 16; more is answered busy)
//	deadline of one request                 30 s (answered deadline-exceeded)
//	requests per connection                 60 per minute, sliding (rate-limited)
//	TLS handshake                           5 s
//	hello after the handshake               10 s
//	a frame, from its first byte to its last 10 s (a half-sent frame ends the connection)
//	silence                                 60 s
//
// A request that reuses the id of one still in flight (until its answer is
// written) closes the connection, with no answer to it, since its answer could
// not be told from the other's. A reuse that the server reads while that
// answer is being written waits for the write, since a client may have the
// answer before the server's write returns: if the write succeeds the id is
// free and the request is served; if it fails the connection is closed and the
// request is not served. So one id never has two answers pending. That is
// about answers, not about handlers: a request that is answered
// deadline-exceeded has its answer written, which frees its id, though its
// handler may still be running, so a handler that ignores its context can
// still be running when a request with the same id is served.
// Responses may come in any order; the id says which request each answers. On
// shutdown (the context of Serve is done) the server stops accepting, lets
// requests in flight be answered for up to the grace period and closes
// everything.
//
// A handler that ignores its context keeps its places, on its connection and
// among the 128, until it returns, even when its connection has gone; so a
// peer that reconnects cannot start more handlers than that.
//
// A connection counts against the 8 from the moment it is accepted, before
// its handshake. So a peer that cannot be pinned, but can reach the port, can
// hold places for up to the handshake time by not finishing; the places are not
// kept for pinned keys. The port is for the owner's firewall to restrict.
// # The client
//
// [Dial] connects to the gate (TLS, then hello) and returns a [Client] that
// keeps the connection up; [NewClient] and [Client.Run] do the same for a
// caller that wants the supervisor in a goroutine of its own. [Client.Call]
// makes one request over the connection and waits for its answer, many at
// once if the caller likes, each by its own id.
//
// Call never waits longer than its context. With no connection it returns
// [ErrDisconnected] at once, and so it does when the connection is lost while
// it waits (the request may or may not have run: the caller decides, nothing is
// retried or queued here). A coded refusal is a [*RemoteError]. If the gate
// does not take a request's bytes, a write ends at the context, and then the
// connection is given up and redialled, since a frame may be half sent.
//
// The client sends seat.ping every 15 s and gives up a connection from which
// no frame has come for 45 s. It redials with exponential backoff: 1 s doubled
// to 30 s, jittered into the upper half of each step, starting over only after
// a connection that stood for 45 s (a link that drops sooner, hello or no
// hello, goes on up the steps). [ClientConfig] can replace the clock (Sleep), the
// random source (Rand) and the dialler, so a test runs the schedule with no
// waiting. [Client.Connected] says since when the connection has stood.
// [Client.LastError] says why there is none.
//
// A frame from the gate that breaks the rules of the wire (an answer to a
// request never made, a repeated key, a request) ends the connection.
package seatgate
