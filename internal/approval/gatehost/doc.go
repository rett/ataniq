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
//	seat.wait             watch       ownerwire.SeatWaitParams        ownerwire.SeatWaitResult
//	seat.genesis          genesis     ownerwire.SeatGenesisParams     ownerwire.SeatGenesisResult
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
// # The wait
//
// seat.wait asks the service ([WaitPort]) whether the set of pending cards
// differs from the version the seat last saw, and the call is held until it
// does or the service's hold ends. The handler gives the service the
// request's own context, which carries the gate's deadline for the request
// (30 seconds): the service ends its hold a margin before it and returns when
// the context is done, so a wait whose peer's connection was lost or closed
// ends with it. The handler holds at most [MaxWaitsPerPeer] (2) waits at once
// for each peer, across its connections: the seat holds one, and the second
// is room for a reconnect while its predecessor unwinds. A third, or a call
// the service itself refuses with [ownerchannel.ErrWaitFull], is answered
// wait-full before anything is looked at. A slot is held for exactly the life
// of the call, and the handler starts no goroutine of its own. A wait takes
// one of the gate's handler places while it is held, as any request does.
//
// # The interview
//
// seat.genesis is the guided new-project interview: one operation of nine
// verbs (start, list, round, answer, change, summary, finish, discard, plan),
// each with the members its verb takes, which the wire's decoder holds. The handler gives
// the decoded params to the daemon's thread store ([GenesisPort], given with
// [WithGenesis]) and encodes its result with the wire's encoder. The handler
// serves it, and lists genesis last in its hello, only when it was given the
// port: it needs no service that can wait and no seat origin, as an answer
// decides nothing. Without the port the operation is unknown-op before its
// params are read. One thread's calls are serialised by the store, not by the
// handler, which holds no state for it.
//
// The port refuses with the gate's word, which the handler passes on when it is
// one of genesis-busy, genesis-unknown-thread, genesis-bad-answer,
// genesis-over-cap, genesis-secret-shaped, genesis-done, genesis-stale-round,
// genesis-not-ready, genesis-no-template and bad-params; any
// other word or error is answered failed, and the cause, which the store words
// without a byte of an answer, is logged.
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
//	ErrWaitFull                                 wait-full
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
// The thread store's refusals are told as the nine words of seat.genesis
// (genesis-busy and the rest, above): a third open thread, a thread the store
// does not hold, an answer the question does not allow, an answer past a cap,
// text that looks like a secret, an answer, change or discard of a finished
// thread, an answer to a round the thread has already taken, a plan of a
// brief that is not complete, and a plan of a kind no template serves.
//
// The handler adds codes of its own: unknown-op, bad-params, not-pinned,
// wait-full and too-many-refusals (below). A result the service returned that its encoder
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
// the desk. Nor does wait-full: it is a load answer, not a misuse, and a seat
// that reconnects while its old wait unwinds meets it with no fault of its
// own; counting it would let a reconnect loop shut a good seat out for ten
// minutes. Its log line is limited instead, to one a minute for each peer.
//
// Of seat.genesis, only genesis-bad-answer counts, with bad-params: an answer
// the question cannot take, which the seat's own pages never send. The others
// do not: genesis-stale-round, genesis-busy, genesis-done,
// genesis-unknown-thread, genesis-not-ready and genesis-no-template answer the
// state of the thread store, which a seat in good order meets (a double tap, a
// second tab, a page left open past a discard, a plan asked of a brief the
// owner finished early or of a kind no template serves), and
// genesis-secret-shaped and genesis-over-cap answer what the owner typed,
// which the owner may get wrong a few times; a few taps must not shut the
// owner out of approvals. plan, like round and list, is a read the seat may
// send as often as its own room allows. They are bounded by the seat's 30 project
// requests a minute, the seat link's room of 12 interview calls, the gate's
// 60 requests a minute and the store's own limits.
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
