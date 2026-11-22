// Package gatehost is the daemon's side of the seat gate: the
// [seatgate.Handler] that gives the operations a seat sends over the gate
// their meaning, by handing them to the owner channel's service
// ([ownerchannel.Service], through the small [Port] it needs).
//
// The package carries no build tag, but only code behind the
// ataniq_ownerchannel tag imports it (the daemon's gate listener), which the
// tag guard of the repository allows: no command depends on it without the
// tag. It opens no socket and no file; package seatgate serves the
// connections and calls [Handler.Handle] once per request.
//
// # Who the seat is
//
// The gate's TLS handshake proved which pinned key the peer holds, and
// [seatgate.Peer] carries its fingerprint. The handler asks the pin check
// given to [New] again on every call, so a peer whose key is no longer pinned
// is refused not-pinned whatever its connection says. Every call is handed to
// the service with the source
//
//	Surface: ownerchannel.SurfaceSeat   ("seat")
//	Session: the first 16 hexadecimal digits of the peer's key fingerprint
//	Decider: false
//
// and nothing else: no process, terminal or client name, which belong to the
// desk's console. A seat is never the decider. Nothing a seat sends can make
// it one, because no field of any params is read into the source.
//
// # Operations and capabilities
//
// The handler serves five operations, in two capabilities ([Caps] is the list
// for the gate's hello, [seatgate.ServerConfig].Caps):
//
//	operation     capability  params                       result
//	seat.pair     pair        ownerwire.SeatPairParams     ownerwire.SeatPairResult
//	owner.list    read        none                         ownerwire.Listing
//	owner.show    read        ownerwire.ShowParams         ownerwire.ShowResult
//	owner.paths   read        ownerwire.PageParams         ownerwire.PathsPage
//	owner.patch   read        ownerwire.PageParams         ownerwire.PatchPage
//
// Any other operation is refused unknown-op before its params are read and
// without the service: every write of the owner connection (owner.decide,
// owner.pair-start, owner.pair-confirm, owner.key-revoke and the rest) and
// every read that is not listed (owner.keys, owner.pair-status, owner.limit)
// included. Deciding from a seat is a later capability.
//
// # Params
//
// A request's params (absent reads as {}) are read twice, and refused
// bad-params if either reading refuses them: by [seatgate.DecodeStrict], the
// gate's own rules, and by the decoder of package ownerwire for the operation,
// which holds the same strict form and checks the content (a pending id's
// grammar, a digest's length, a pairing code's alphabet). Params are never
// logged and never put in an error, whatever happened to them.
//
// # Codes
//
// A refusal is told to the seat as a code and nothing else (package seatgate
// sends no text). Each error of the service has a fixed code:
//
//	the service's error                         code
//	ErrPairRefused                              pairing-refused
//	ErrCredentialInvalid                        credential-invalid
//	ErrKeysUnavailable                          keys-unavailable
//	ErrKeysFull                                 keys-full
//	ErrKeyUnknown                               key-unknown
//	ErrKeyAmbiguous                             key-ambiguous
//	ErrPairingOpen                              pairing-open
//	ErrNoPairing                                pairing-none
//	ErrPairingExpired                           pairing-expired
//	ErrPairingChanged                           pairing-changed
//	ErrDoorUnavailable                          door-unavailable
//	ErrJournalReopened                          journal-reopened
//	ErrClosed, ErrStopped, a cancelled context  unavailable
//	an expired context                          deadline-exceeded
//	a refusal (*record.Error) of a code below   that code
//	anything else, ErrNotSeat included          failed
//
// The pairing codes are the ones the console knows for the same errors
// (package consoleclient). The refusals passed on with their own code are
// those the five operations can meet: approval-void, approval-expired,
// approval-consumed, decision-invalid, previous-epoch, review-changed,
// record-invalid and record-too-large. A refusal with any other code is
// answered failed and logged, so that only words from this list reach a seat.
//
// The handler adds codes of its own: unknown-op, bad-params, not-pinned and
// too-many-refusals (below). A result the service returned that its encoder
// refuses is answered failed.
//
// # The refusal bound
//
// The gate pins keys, but a pinned peer can still be hostile: a seat host that
// was taken over could send seat.pair with guess after guess, or anything else
// the handler refuses, to burn pairing codes or fill the log. The service
// burns a code after five wrong tries; the handler bounds each peer as well,
// by its key fingerprint, across all its connections, so that reconnecting
// does not start the count again.
//
// A refusal of what the peer sent counts: unknown-op, bad-params, not-pinned,
// the pairing codes (pairing-refused, credential-invalid and the rest) and the
// refusals passed on from the service (approval-void and the rest). A refusal
// for the daemon's own state does not: unavailable, deadline-exceeded, failed,
// door-unavailable, journal-reopened, keys-unavailable and keys-full, so a seat
// that polls while the door is down is not shut out for it. A call that is
// served never counts.
//
// After [RefusalBound] (16, the owner connection's bound on refused writes)
// counted refusals within [RefusalWindow] (10 minutes), every request of the
// peer, a read too, is refused too-many-refusals before the pin check, the
// params and the service. That lasts until the oldest of the refusals is
// RefusalWindow old; then one more request is served, and a refusal of it
// shuts the peer out again. Requests that were admitted before the bound was
// reached, which the gate may run together (16 per connection), are served
// and counted as they end, and the peer's shut-out runs from the latest. The
// count is in memory: a restart of the daemon starts it afresh.
//
// # The log
//
// Each refusal is one line of the log given to [New], naming the operation
// when it is one of the five, the code, and the peer by the first 16
// hexadecimal digits of its key fingerprint. A failed call and a
// keys-unavailable one add the error's own text, made one printable line of at
// most 300 bytes, since their cause (a path, say) is for the operator and not
// for the seat; the service's errors hold nothing a caller sent. A call that is
// served writes no line. The counted refusals of a peer are logged up to the
// bound; the request that finds the peer shut out writes one line saying so,
// and nothing more is written for the peer until a request of it is admitted
// again.
package gatehost
