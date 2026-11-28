// Package consoleclient reads the daemon's leads for the console, as plain
// values. [Read] changes nothing. The package also holds the console's
// approvals client, [Owner], which decides only what the owner hands it.
//
// [Read] given the folder of the daemon's socket
// dials the socket, asks the two read-only questions of the client protocol
// ("hello" and "status"), closes, and returns one [Result]. The result is
// either a [View] (the daemon, the supervisor, and the projects with their
// lead and their coordinators) or one of a few plain states, each with a
// one-line text and a next step that a person can follow, such as "start the
// daemon with: ataniqd run".
//
// [ReadManaged] reads one managed home's directory over a stream the caller
// opened, with the managed protocol alone: no fallback to version 1 or to the
// local scratch inventory, and no request but "hello" and the directory's
// list. [View.WithHome] puts the read of one home into a view of several,
// keeping the rows of a home it could not read, marked Stale and listed in
// OfflineHomes. A row's identity ([HomeID], project ID, lead ID) is made from
// the whole strings the home sent before they are cleaned for showing; a lead
// never launched has [Entry].NeverLaunched set and no epoch.
//
// This package is provisional and unqualified, like the client protocol it
// reads through.
//
// # What a caller may rely on
//
//   - Every string in a [View] came from the socket and has been cleaned and
//     bounded by this package: no control character, no format character (so
//     no escape sequence and no direction override), no invisible character,
//     no combining mark at the start, and at most [MaxText] bytes. The lists
//     are bounded too, and the view counts what it left out. The strings are
//     for showing. They are not identifiers: cleaning can make two different
//     names equal, so nothing may be decided by comparing them.
//   - The text and the next step of a state are fixed texts of this package.
//     Nothing the peer sent is in them, and no error text is.
//   - Read sends no operation other than "hello" and "status".
//
// # The owner channel's client
//
// The files named owner*.go hold [Owner], the console's approvals backend
// over the daemon's owner channel. They are in every build, behind no build
// tag: whether a daemon has the owner channel is found at run time, from the
// operations its hello lists ([View.OwnerChannel]), and a release daemon has
// none. [Owner] speaks the owner operations of package ownerwire
// through the client protocol's call for registered operations: list, show,
// the pages of a long path list and patch, the decider, a decision, a beat,
// the limit and the "looked" mark. It builds the console's values (the
// listing, the card, a page, an outcome, a limit's view) from what the
// daemon answers, and decodes every answer through the wire package's strict
// readers.
//
// What it keeps to:
//
//   - A daemon whose hello lists no owner operation is answered with "no owner
//     channel in this build" ([ErrNoOwnerChannel]), and one that lists only
//     some with [ErrOwnerVersion]; neither connection is kept.
//   - A decision is sent once, over the connection its card was read on, and
//     as the wire's frame says and nothing more. A lost connection drops
//     what was typed and decides nothing; a decision whose answer did not
//     come back is in doubt, and the card takes no second one until it is
//     opened again. The client shows the outcome the daemon tells it.
//   - No time of the console's is sent, and no error text of the daemon is
//     shown: a refusal with a code of the approval packages is a
//     [*record.Error] with that code, any other failure an [*Unavailable]
//     with the fixed text of one of the states above.
//
// The methods of owner_pair.go pair a phone and keep the list of deciding keys
// (PairStart, PairStatus, PairConfirm, PairReject, Keys, KeyRevoke, KeysOff).
// They return the types of the wire package, send each call once and never
// again after a failure, refuse locally what the wire package refuses, and
// decide nothing: the daemon's rules are the daemon's. A refusal with a code of
// the pairing and key operations is a [*PairError] with a fixed text of this
// package; PairConfirm and PairReject take the pending id that PairStatus
// showed with the eight words, so that the owner's word is about the credential
// he compared (the daemon refuses another with pairing-changed, changing
// nothing); the pairing code is in the result of PairStart, which prints
// without it, and in no error. The seat's call, seat.pair, is not the
// console's and has no method here.
//
// This part is provisional and unqualified. It was proved against a fake
// daemon on a private socket, not against the daemon's own owner channel.
//
// # The lead control client
//
// The files named leads*.go hold [Leads], the console's client of the
// daemon's lead control plane: the operations lead.list, lead.define,
// lead.start, lead.stop, lead.ticket and lead.inject of package leadwire,
// through the client protocol's call for registered operations. Like the
// owner channel's client it is in every build, behind no build tag, and
// the daemon's hello determines what the client can do on that connection.
// The directory needs lead.list. Each command needs its own operation and
// a ticket needs the requested attach role. The client is for any front end.
// Its knowledge of terminals, units and sandboxes is limited to the values
// package leadwire defines, and a test holds that.
//
// What it keeps to:
//
//   - A command is sent with a command id of 128 random bits and a client
//     string that names the run. The client keeps the id and the arguments
//     of every command that has no final receipt, and a call with the same
//     operation and arguments repeats the kept id, so the daemon runs the
//     command at most once. A final receipt, or a list that shows the id as
//     a lead's last command with a final status, forgets it.
//   - After a lost answer the client repeats the same id and arguments once
//     if the new connection offers the action. If recovery fails it returns
//     [ErrAnswerLost] and the id is kept. A new id is never made for a
//     command that has no final receipt; the one id the client lets go
//     without a final receipt is one the daemon answers "conflict" for,
//     which it will not run again.
//   - Every call returns a receipt, or an error only when no receipt came
//     back. A receipt holds no text the daemon wrote: its message is left
//     out, its words (a reason code, an inject outcome) must be of a closed
//     grammar or are "unrecognised", and a listing's strings are cleaned and
//     bounded. An answer that is not exactly the shape of package leadwire,
//     or that is for another command, role or epoch than the one asked for,
//     is a bad answer and the connection is dropped.
//   - A role, a message id and a body are checked before anything is sent.
//
// This part is provisional and unqualified. It was proved against a fake
// daemon on a private socket, not against the daemon's own lead control.
//
// # Boundary
//
// Of this repository the package imports ataniq/internal/clientapi, and it
// reads the wire only through that package's client. The files of the owner
// channel's client import also the wire package of the owner connection and
// the values of the console's backend (record, card and limits), and nothing
// that acts: not the door, the service, the journal or the sample backend.
// The files of the lead control client import also package leadwire, and
// nothing else.
// The package does not import the console: the command that owns the console
// maps these values onto the console's projection, and hands the client to
// the console as its backend. A test holds the imports.
package consoleclient
