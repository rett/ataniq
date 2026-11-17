// Package wire is the vocabulary of the lead holder's two sockets: the attach
// protocol that the holder's service and the console's attach client share,
// and the control protocol the daemon speaks to the holder (the lead-hosting
// design, section 2.2).
//
// # Boundary
//
// The package is pure. It works on [io.Reader] and [io.Writer], opens no
// file and no socket, starts nothing and imports only the standard library
// (an import guard test keeps it that way). Who is connected, which ticket
// was issued and when a Hello may come are decisions of the holder, not of
// this package.
//
// # Frames
//
// A frame is one type byte, a four-byte big-endian payload length and the
// payload. A [Reader] is made for one [Direction] and checks the header
// before it reads or allocates any payload: an unknown type for that
// direction, or a length over the cap of the type, is an error and the reader
// reads nothing more. A payload that is declared large but never arrives is
// not allocated up front: the reader grows its buffer as bytes come in.
// [Write] and [Writer] write one frame.
//
// # Payloads
//
// Structured payloads are JSON objects, decoded strictly: a field that the
// struct does not have (also one that differs only in case), a repeated
// key, trailing data, a value over its length cap, text with a control
// character, a Unicode format character (category Cf: the bidirectional
// controls, zero-width characters, the byte order mark) or a line or
// paragraph separator (U+2028, U+2029), and text that is not valid UTF-8 are
// all refused. [Sanitize] replaces the same characters. Invalid UTF-8 is
// refused as it comes: a raw invalid byte, and a \uD800 to \uDFFF escape that
// is not half of a pair, are errors and are never replaced by U+FFFD (an
// escaped U+FFFD itself is fine). The Encode functions apply the same checks,
// so a well-behaved sender cannot produce what the decoder refuses. Input, Paste,
// Output and Snapshot are binary and have helpers to build and split them.
//
// A field whose zero value could be read as a real answer (an epoch, an
// output position, a presence flag, the writer's name and pid, the op of a
// request, the version of a Hello and of a Welcome, the ticket of a Hello,
// the digest of a Check) is required: a payload that lacks it, or has it
// null, is refused, not decoded as zero.
//
// # Epochs
//
// Epochs are numbered from 1. Epoch 0 is invalid wherever an epoch is
// carried (a control request, a Welcome, a Status), on encode and on decode,
// so a payload that lacks an epoch can never match a holder's.
package wire
