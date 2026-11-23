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
// A handler made with [New] serves five operations, in two capabilities
// ([Caps]); one made with [NewDeciding] serves four more, in two more
// capabilities ([DecidingCaps]). [Handler.Caps] is the list for the gate's
// hello, [seatgate.ServerConfig].Caps:
//
//	operation             capability  params                          result
//	seat.pair             pair        ownerwire.SeatPairParams        ownerwire.SeatPairResult
//	owner.list            read        none                            ownerwire.Listing
//	owner.show            read        ownerwire.ShowParams            ownerwire.ShowResult
//	owner.paths           read        ownerwire.PageParams            ownerwire.PathsPage
//	owner.patch           read        ownerwire.PageParams            ownerwire.PatchPage
//	seat.login-challenge  login       none                            ownerwire.SeatLoginChallengeResult
//	seat.login            login       ownerwire.SeatLoginParams       ownerwire.SeatLoginResult
//	seat.challenge        decide      ownerwire.SeatChallengeParams   ownerwire.SeatChallengeResult
//	seat.decide           decide      ownerwire.SeatDecideParams      ownerwire.DecideResult
//
// An operation whose capability the handler does not serve is refused
// unknown-op before its params are read and without the service, and so is
// every write of the owner connection (owner.decide, owner.pair-start,
// owner.pair-confirm, owner.key-revoke and the rest) and every read that is
// not listed (owner.keys, owner.pair-status, owner.limit). The daemon makes a
// deciding handler only when the seat origin is configured; the four are
// handed to the service ([SeatPort]) with the gate's own values
// ([ownerchannel.Gate]: its key fingerprint, the seat origin and its relying
// party id), never with anything of the params.
//
// # Params
//
// A request's params (absent reads as {}) are read twice, and refused
// bad-params if either reading refuses them: by [seatgate.DecodeStrict], the
// gate's own rules, and by the decoder of package ownerwire for the operation,
// which holds the same strict form and checks the content (a pending id's
// grammar, a digest's length, a pairing code's alphabet). The params of
// seat.login and seat.decide are read as their envelope only (the members,
// their types, the challenge id's grammar, each byte string's length): the
// service takes and burns the challenge before it judges the frame or the
// assertion, and answers a frame whose content is wrong bad-params too.
// Params are never logged and never put in an error, whatever happened to
// them.
//
// A deciding handler checks one more thing of seat.pair: a registration the
// seat relays (its rp_id given) must be for the gate's own relying party id,
// the one every login and decision is checked against, or it is refused
// credential-invalid without the service, so the pairing code is not spent
// on it. A key registered for another relying party could never log in. A
// pairing with no registration is passed on (its key stays unverified), and
// a handler made with [New], which has no seat origin, passes every one on.
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
//	ErrSeatParams                               bad-params
//	ErrChallengeStale                           challenge-stale
//	ErrChallengesFull                           challenges-full
//	ErrAssertionInvalid                         assertion-invalid
//	ErrKeyRevoked                               key-revoked
//	ErrKindRefused                              kind-refused
//	ErrClosed, ErrStopped, a cancelled context  unavailable
//	an expired context                          deadline-exceeded
//	a refusal (*record.Error) of a code below   that code
//	anything else, ErrNotSeat included          failed
//
// The pairing codes are the ones the console knows for the same errors
// (package consoleclient), and the seat's are the words of package ownerwire
// (ownerwire.CodeChallengeStale and the rest). The refusals passed on with
// their own code are those the operations can meet: approval-void,
// approval-expired, approval-consumed, decision-invalid, binding-mismatch,
// previous-epoch, review-changed, record-invalid and record-too-large. A refusal with any other code is
// answered failed and logged, so that only words from this list reach a seat.
//
// The handler adds codes of its own: unknown-op, bad-params, not-pinned and
// too-many-refusals (below). A result the service returned that its encoder
// refuses is answered failed.
//
// # The refusal bound
//
// The gate pins keys, but a pinned peer can still be hostile: a seat host that
// was taken over could send anything the handler refuses, to fill the log or
// to probe. The service burns a pairing code after five wrong tries; the
// handler bounds each peer as well, by its key fingerprint, across all its
// connections, so that reconnecting does not start the count again.
//
// A refusal of what the peer sent counts: unknown-op, bad-params, not-pinned,
// key-unknown, assertion-invalid and the refusals passed on from the service
// (approval-void and the rest). A refusal for the daemon's own state does not:
// unavailable, deadline-exceeded, failed, door-unavailable, journal-reopened,
// keys-unavailable and keys-full, so a seat that polls while the door is down
// is not shut out for it. A call that is served never counts. Nor does a
// refusal of seat.pair, seat.login-challenge or seat.login, but unknown-op and
// not-pinned: the seat relays a pairing for anyone who reaches its pairing
// page and a login for anyone who reaches its login page, and a stranger must
// not shut the owner out by them; wrong pairing codes stay bounded by the
// service's burn after five, and the seat limits both. Nor do challenge-stale,
// challenges-full, key-revoked and kind-refused, which come of normal use of
// a decision (a double tap, a slow owner, a card that changed, "phone off" at
// the desk).
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
// when it is one the handler serves, the code, and the peer by the first 16
// hexadecimal digits of its key fingerprint (its gate session). A failed call
// and a keys-unavailable one add the error's own text, made one printable line
// of at most 300 bytes, since their cause (a path, say) is for the operator
// and not for the seat; an assertion-invalid one adds the check that failed,
// from the verifier's fixed list; the service's errors hold nothing a caller
// sent. A call that is served writes no line, but that a challenge was
// issued (seat.login-challenge, seat.challenge), which is one line without
// the challenge. [Handler.Counts] gives how often each code was answered,
// kept in memory. The counted refusals of a peer are logged up to the
// bound; the request that finds the peer shut out writes one line saying so,
// and nothing more is written for the peer until a request of it is admitted
// again.
package gatehost
