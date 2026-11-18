// Package clientapi is the daemon's local client protocol, version
// "ataniq.client/1": the server side that the daemon serves on a Unix socket
// and the client side that the console and the command line dial.
//
// This package is provisional and unqualified. It has not been tested against
// a hostile peer beyond its own tests, and nothing here is a claim of
// isolation.
//
// # What version 1 is
//
// Version 1 is read-only. It has two operations, "hello" and "status". It has
// no command, no command ID, no approval and no event stream, and it says so
// on the wire: without an extension, the hello result carries "mutations":
// false and lists the two operations. Every other operation is answered with
// the code "unsupported-operation". A server may register extension
// operations; hello then lists them and says whether one mutates. No release
// build registers one. A version that adds commands to every build is a
// different protocol name.
//
// # Extensions
//
// A caller of [Serve] may pass [Extension] values in [Options].Extra. Each
// names its operations, lower-case letters, digits and dashes around exactly
// one dot, such as "canary.start", and serves them in Handle. Serve refuses an
// extension that names no operation, a name that is not of that form, or one
// that repeats another extension's, before it accepts. With none the server
// answers as without the field. With one or more, hello lists the base
// operations and then each extension's, in order, and says "mutations": true
// only when a registered extension's Mutating is true. A request names an
// extension operation in "op" and may carry "params", one JSON object inside
// the request line cap that hello and status ignore; for an extension operation
// a params member that is not an object, or that repeats a top-level key, is
// "invalid-request". The request passes the same peer check, caps, timeouts and
// counters as the base operations. A panic in Handle, a result that cannot be
// marshalled or panics while it is, one over the response cap, and an error body that is not
// well-formed are answered "internal"; a well-formed error body is sent with
// its own code, which an extension may choose (lower-case letters, digits and
// dashes, starting with a letter) except the codes that end a connection.
// [Client.Call] is the client side.
//
// # Wire
//
// One JSON object per line, UTF-8, each line ended by a single LF. A request
// line is at most [MaxRequestBytes] bytes (without the LF) and a response line
// at most [MaxResponseBytes] bytes (with the LF); both are advertised in the
// hello result. A request is
//
//	{"protocol":"ataniq.client/1","request_id":"<1..64 printable ASCII>","op":"hello"}
//
// and a response is either
//
//	{"protocol":"ataniq.client/1","request_id":"...","ok":true,"result":{...}}
//
// or
//
//	{"protocol":"ataniq.client/1","request_id":"...","ok":false,"error":{"code":"...","message":"..."}}
//
// Unknown fields in a request are ignored, "params" among them for the two base
// operations. A line that is not valid UTF-8 and JSON, is not an object, or
// lacks a field, is answered "invalid-request" (the connection stays open); a
// line over the cap is answered "too-large" and the connection is closed; a
// protocol name the server does not speak is answered "unsupported-protocol"
// with the list of supported ones. The codes are the [ErrorCode] constants.
// Messages are fixed texts: nothing the peer sent is echoed in them. The one
// peer-supplied value in a response is the request_id, echoed only when it is a
// valid one (otherwise it is empty). A connection may carry several requests in
// turn; responses come in request order.
//
// A response that would exceed the response cap is replaced by an "internal"
// error: the server never sends a line over the cap, and the client refuses to
// read one.
//
// # Status entries
//
// The status result lists entries of three kinds, told apart by "kind": a
// lead ("<project>/lead"), the supervisor ("supervisor") and a coordinator
// ("<project>/coord/<feature>"). The shown state is one of "desired",
// "starting", "ready", "degraded", "stalled", "stopping", "stopped", "ended"
// and "unknown"; "stalled" is shown for a coordinator only. A coordinator
// entry also carries "feature", "parent" (its parent lead's role string),
// "parent_epoch" (that lead's epoch at the coordinator's launch), "instance"
// (the coordinator's own epoch) and "identity". Those five members are omitted
// from the entry of a lead and of the supervisor; a client reads an absent
// one as unset. The identity is an identifier: compare it whole, never use it
// as a path or ref component, never match a prefix of it. A coordinator entry
// is a record: it grants no seat, approval or handoff. Adding these members
// and the kind is additive, so the protocol name stays "ataniq.client/1".
//
// # Peer check and its limit
//
// The server reads the peer credentials of every connection with SO_PEERCRED
// before it reads any byte, and answers "unauthorized" and closes when the UID
// differs from its own effective UID. That check does not tell the owner's
// console from any other unsandboxed process of the same OS user: any such
// process can dial the socket and is served. This is the limit that
// docs/design/security.md states, and version 1 accepts it by owner decision.
// Reading the two operations over this socket therefore discloses the registry
// summary to every process of the user that can reach the socket. A lead that
// runs in a sandbox must never see this socket; keeping it away is the
// sandbox's job and is not asserted or tested here.
//
// # Socket
//
// [Listen] creates the folder with mode 0700 and binds daemon.sock in it with
// mode 0600; see its documentation for why no wider window exists. A stale
// socket file is removed only by [RemoveStale], which the caller invokes after
// it holds the daemon lock.
//
// # Boundary
//
// The package defines its own wire structs and imports no other package of
// this repository. The caller supplies values through [Handler]. The server
// bounds connections, request size, idle time and write time; it does not
// bound what a handler does. It does not authenticate the daemon to the
// client: a client trusts the path it dials.
package clientapi
