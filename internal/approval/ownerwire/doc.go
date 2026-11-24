// Package ownerwire holds the words of the owner connection: the names of
// its operations, and what each one sends and answers, as types with strict
// decoding.
//
// The owner connection is how the owner's console talks to the daemon about
// approvals: it lists the cards, reads one, reads the long lists behind a
// card in pages, and sends the owner's answer. This package is only the
// words. It opens no socket and has no server and no client; the daemon and
// the console each use it for their side, so that the two cannot disagree
// on a field. Each payload is one JSON object, which the client protocol
// carries as the params of a request or the result of a response.
//
// # The operations
//
//	operation           params             result
//	owner.list          none               Listing
//	owner.show          ShowParams         ShowResult
//	owner.paths         PageParams         PathsPage
//	owner.patch         PageParams         PatchPage
//	owner.decider       DeciderParams      DeciderResult
//	owner.decide        DecisionFrame      DecideResult
//	owner.beat          none               nothing
//	owner.limit         LimitParams        LimitView
//	owner.limit-set     LimitSetParams     LimitView
//	owner.looked        none               nothing
//	owner.pair-start    PairStartParams    PairStartResult
//	owner.pair-status   none               PairStatus
//	owner.pair-confirm  PairPendingParams  KeyItem
//	owner.pair-reject   PairPendingParams  nothing
//	owner.keys          none               KeysView
//	owner.key-revoke    KeyRevokeParams    KeysView
//	owner.keys-off      none               KeysView
//	seat.pair           SeatPairParams     SeatPairResult
//
// [Operations] lists the names. Every type has a Decode and an Encode
// function named after it; "none" and "nothing" are an empty object, read
// by [DecodeNothing].
//
// Six more operations are the seat gate's alone, a login and a decision
// from the owner's phone, the long wait that tells the seat when the
// pending cards changed, and the guided new-project interview; the daemon's
// Unix socket neither serves nor announces them, and [GateOperations] lists
// them apart:
//
//	operation             params               result
//	seat.login-challenge  none                 SeatLoginChallengeResult
//	seat.login            SeatLoginParams      SeatLoginResult
//	seat.challenge        SeatChallengeParams  SeatChallengeResult
//	seat.decide           SeatDecideParams     DecideResult
//	seat.wait             SeatWaitParams       SeatWaitResult
//	seat.genesis          SeatGenesisParams    SeatGenesisResult
//
// # Strict decoding
//
// A payload is read under the rules of the approval records (package
// record): valid UTF-8, no float, no null, no control character in a
// string, no key twice in one object, nothing after the object. A key must
// match the form's name byte for byte, and a key the form does not name is
// refused. Every payload has a cap in bytes. A decoder accepts any JSON
// spelling of a valid payload; an encoder writes one spelling, with keys
// sorted and no whitespace.
//
// The decoders check content as well as form (an id's grammar, a digest's
// length, a closed word), and the encoders make the same checks, so what
// one side can write the other can read.
//
// # The decision frame
//
// A [DecisionFrame] carries what only the owner can say: which card, the
// digest of what it showed, approve or deny, the sign-offs, and the reason
// of a deny. The daemon fills the rest of the decision record from what it
// knows itself: who planned, the bound fields, the times, the console block
// with the process, the terminal and the session. A frame has no place for
// any of that, so a console cannot state it, and a frame that tries is
// refused decision-invalid.
//
// # The challenge of a phone decision
//
// A decision from the phone is a WebAuthn assertion over a challenge the
// daemon draws and binds to its own record of the card: [DecisionChallenge]
// hashes a domain string, the gate's key fingerprint, the frame's id, binding
// digest and decision, the digests of its sign-offs and its reason
// ([SignedOffDigest], [ReasonDigest]), and the daemon's nonce. A login is an
// assertion over [LoginChallenge], whose domain string differs. Both ends call
// these functions, so they cannot disagree on a byte. The params of
// seat.decide are read in two parts: [DecodeSeatDecideParams] reads the
// envelope, and the daemon checks the frame's content with
// [CheckDecisionFrame] only after it took the challenge, so that a malformed
// frame still burns it. [Assertion.Digest] is the digest a passkey decision
// record keeps of the assertion.
//
// # The wait for a change
//
// seat.wait (the capability "watch") lets the seat learn that the pending set
// changed without asking again and again. Its params carry the version the
// seat last saw, or none; its result carries the version of the pending set
// now, the count, and at most [MaxSeatWaitCards] cards, each only an id, a
// kind, the binding digest and the held mark, never card text. [PendingVersion]
// is the one function that makes a version: a digest of a canonical encoding of
// the whole set, independent of its order, so the same set has the same version
// after a restart. The gate answers [CodeWaitFull] when it already holds as many
// waits for a seat as it allows.
//
// # The guided new project
//
// seat.genesis (the capability "genesis") is the owner's interview for a new
// project, asked on the phone: a thread of at most [MaxGenesisRounds] rounds of
// one to [MaxGenesisNodes] questions, which fill a project brief. It is one
// operation whose params are a union tagged by "verb" ([SeatGenesisParams]):
// start opens a thread (the host holds at most [MaxGenesisThreads] open ones
// and answers [CodeGenesisBusy] to a third), round reads where a thread
// stands, answer gives the answers of a round, change replaces an earlier
// answer (the result's reset says how many later answers it cleared),
// summary reads the brief grouped by who chose each field, finish freezes
// the brief and gives its digest, discard drops the thread, list reads the
// open threads, and plan reads the project plan a complete brief gives: a
// preview compiled for the read on an open thread, the plan stored at finish
// on a finished one. The result is a union tagged by "state"
// ([SeatGenesisResult]): question, summary, finished, discarded, threads or
// plan. The decoders hold exactly the members each verb and each state takes,
// the grammars of ids and the caps (three questions, eight options, the
// catalog's text caps, 64 summary rows, a plan of 12 KiB, 16 KiB the result).
// A plan ([GenesisPlan]) is the plan record of the host, member by member:
// the wire writes it to the record's own canonical bytes, and the result
// carries their digest, which the decoder holds to them.
//
// Every text in a result is plain text the host owns: the catalog's, or the
// owner's own "other" text in a row or a plan's note marked untrusted, which
// a surface escapes and an agent never obeys. An answer writes a brief field
// and nothing else, and a plan is a proposal: no member of these forms is, or
// leads to, a decision, a contract or an approval. The thread ids, the
// questions, the brief and the plan are the host's; this package holds only
// the words, and the gate codes [CodeGenesisBusy], [CodeGenesisUnknownThread],
// [CodeGenesisBadAnswer], [CodeGenesisOverCap], [CodeGenesisSecretShaped],
// [CodeGenesisDone], [CodeGenesisStaleRound], [CodeGenesisNotReady] and
// [CodeGenesisNoTemplate].
//
// # Records travel as bytes
//
// A pending record is under digests, so it is never decoded and written
// again on its way. A [Record] carries the bytes (base64 in the JSON) and
// their digest, and the decoder checks that the digest is the bytes' own.
// The listing carries no record at all, only what a row shows.
//
// # Pages
//
// The full path list and the patch of a review can be larger than one
// response, so they are read in pages. A page names the pending id and the
// review digest it belongs to, its number and the number of pages, and is
// at most [MaxPageBytes] as encoded. A line of a patch is raw bytes in the
// record package's Text form. A line that does not fit in the room left on
// a page travels in pieces, each marked as continuing but the last, and the
// reader joins them byte for byte. [SplitPaths] and [SplitPatch] make the
// pages, and [JoinPaths] and [JoinPatch] put them together again.
//
// The lines of a patch are lead-written. Nothing here escapes them: a
// surface shows them only through the lead-text escaping.
//
// # Pairing a phone and the list of deciding keys
//
// The owner at the desk pairs a phone in steps: owner.pair-start makes a
// one-time pairing code and answers it, once, in [PairStartResult]; the
// phone's side presents the code with the credential it made (seat.pair, the
// one name that does not begin with "owner.", which is the seat app's call and
// not the console's); the desk reads the eight words of the credential's
// fingerprint with owner.pair-status and confirms (owner.pair-confirm) or
// rejects (owner.pair-reject). owner.pair-status also gives the pending
// credential a pending id, 8 random bytes in 16 lower-case hexadecimal digits
// the daemon draws when the credential becomes pending, and confirm and reject
// must give that id back ([PairPendingParams], required): the daemon compares
// it to the credential pending at that moment and, when it is not that one
// (the pairing was replaced or ran out and another took its place), changes
// nothing and answers pairing-changed. A confirmed credential is on the list of
// deciding keys, which owner.keys reads. owner.key-revoke and owner.keys-off
// ("phone off") revoke one credential or all of them; a revoked credential
// stays on the list, marked, and is never made good again.
//
// The pairing code is a secret. It is in the result of owner.pair-start and in
// the params of seat.pair and nowhere else: no other result carries it, a
// value of either type prints without it, and a refusal never holds it.
//
// A credential is shown by its display id, 16 lower-case hexadecimal digits:
// the first 8 bytes of the SHA-256 of the credential id. It names the
// credential and holds nothing of the key.
//
// The seat relays the registration's authenticator data and its relying party
// id with the credential (both optional, for a seat that relays none), and the
// daemon reads from them what the authenticator claimed: the kind its AAGUID
// claims, the AAGUID and the backup flags. A pending [PairStatus] and a
// [KeyItem] carry them as optional members. With attestation "none" they are
// claims and not facts, and the desk says so where it shows them.
//
// # Refusals
//
// Every refusal is a [*record.Error] with one of the record package's
// codes; this package declares no refusal of its own. It names the words the
// seat gate answers its operations with when the daemon refuses one
// ([CodeChallengeStale], [CodeWaitFull], [CodeGenesisBusy] and the others),
// which no decoder here answers with. A decision frame that is wrong is
// decision-invalid, any other payload record-invalid, a payload over its
// cap record-too-large, and a record whose digest is not its bytes' own
// binding-mismatch. The detail of an error is written here and never holds
// a byte the payload brought, so it can be shown as it is.
//
// The package imports the standard library and package record only.
// Everything here is provisional until the joint contract with the
// planner's project is agreed.
package ownerwire
