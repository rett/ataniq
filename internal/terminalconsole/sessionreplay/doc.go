// Package sessionreplay defines a recording format for terminal sessions and
// a clock-driven player for it.
//
// It is the replay half of the provisional fixture console: a recording holds
// the bytes a session wrote to its terminal, with the time of each write, an
// optional resize and the end of the session with its exit status. The package
// has no terminal emulator and no clock of its own. The caller owns both: it
// feeds the chunks the player returns to its emulator, and it advances the
// player with a time value, as the cockpit does with tea.Tick.
//
// # Recording format
//
// A recording is UTF-8 text in JSON Lines form: one JSON object per line,
// every line ended by "\n", no blank lines, no unknown fields. The first line
// is the header, the rest are records in time order.
//
//	{"format":"ataniq-session-replay","version":1,"cols":100,"rows":30}
//	{"t":0,"k":"out","d":"G1tIG1sySg=="}
//	{"t":250,"k":"resize","cols":80,"rows":24}
//	{"t":900,"k":"end","status":0}
//
// The header gives the terminal size at the start. A record has "t", the time
// in whole milliseconds since the start of the session, and "k", its kind:
//
//   - "out": "d" is one chunk of terminal output, in standard base64 with
//     padding. A chunk is not text: it may end inside an escape sequence or
//     inside a UTF-8 sequence, and the next chunk continues it.
//   - "resize": "cols" and "rows" are the new terminal size.
//   - "end": "status" is the exit status of the session, 0 to 255. It is the
//     last record and there is exactly one.
//
// A recording is output only. It has no record for input, notes or anything
// else, and a parser rejects what it does not know. Times never decrease;
// records with the same time keep their order.
//
// Recordings are untrusted input. The limits are MaxCols and MaxRows for a
// terminal size, MaxChunk for one chunk, MaxEvents for the records, MaxAt for
// a time and MaxBytes for the whole file.
package sessionreplay
