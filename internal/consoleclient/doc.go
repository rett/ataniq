// Package consoleclient reads the daemon's leads for the console, as plain
// values, and changes nothing.
//
// [Read] is the whole package: given the folder of the daemon's socket it
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
//     are bounded too, and the view counts what it left out. The strings are for showing. They are not
//     identifiers: cleaning can make two different names equal, so nothing
//     may be decided by comparing them.
//   - The text and the next step of a state are fixed texts of this package.
//     Nothing the peer sent is in them, and no error text is.
//   - Read sends no operation other than "hello" and "status".
//
// # Boundary
//
// Of this repository the package imports only ataniq/internal/clientapi, and
// it reads the wire only through that package's client. It does not import
// the console: the command that owns the console maps these values onto the
// console's projection. A test holds the imports.
package consoleclient
