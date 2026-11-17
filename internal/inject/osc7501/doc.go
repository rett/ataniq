// Package osc7501 reads the OSC 7501 status reports that terminal agents
// (codex 0.169, claude 2.1.325) write to their terminal, and builds the reply
// to the support query that claude sends before it will report at all.
//
// OSC 7501 is a private escape sequence, ESC ] 7501 ; payload, ended by BEL or
// ST. The payload is either a query (it starts with "?") or a report: fields
// separated by ":", each key=value, with a required state. The agent writes
// the report text, so every byte of it is untrusted; Parse bounds the input,
// refuses what it cannot read exactly and never guesses.
//
// The package is pure. It does no I/O, starts no goroutine, reads no clock
// (times are passed in) and holds no state beyond the Tracker's. The
// caller owns the emulator that parses the escape sequences, the agent's
// input and the decision of what a report means.
//
// The package is not a terminal parser: it receives the OSC data from an
// emulator's handler, it does not find sequences in a byte stream. It does
// not decide whether an agent is idle (a report is one signal among others),
// does not write to any agent and does not persist anything.
//
// The package is provisional and unqualified: it is built from recorded
// sessions of one version per runtime (claude 2.1.325, codex 0.169), with no
// live qualification yet.
package osc7501
