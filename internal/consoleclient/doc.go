// Package consoleclient reads the daemon's leads for the console, as plain
// values, and changes nothing. In the build with the owner channel it also
// holds the console's approvals client, [Owner].
//
// [Read] is the whole package in a release build: given the folder of the daemon's socket it
// dials the socket, asks the two read-only questions of the client protocol
// ("hello" and "status"), closes, and returns one [Result]. The result is
// either a [View] (the daemon, the supervisor, and the projects with their
// lead and their coordinators) or one of a few plain states, each with a
// one-line text and a next step that a person can follow, such as "start the
// daemon with: ataniqd run".
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
// The files that start with the build line "//go:build linux &&
// ataniq_ownerchannel" hold [Owner], the console's approvals backend over the
// daemon's owner channel. A release build has none of them, and there [Read]
// is all there is. [Owner] speaks the owner operations of package ownerwire
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
// This part is provisional and unqualified. It was proved against a fake
// daemon on a private socket, not against the daemon's own owner channel.
//
// # Boundary
//
// Of this repository the package imports ataniq/internal/clientapi, and it
// reads the wire only through that package's client. The files of the owner
// channel's client import also the wire package of the owner connection and
// the values of the console's backend (record, card and limits), and nothing
// that acts: not the door, the service, the journal or the sample backend.
// The package does not import the console: the command that owns the console
// maps these values onto the console's projection, and hands the client to
// the console as its backend. A test holds the imports.
package consoleclient
