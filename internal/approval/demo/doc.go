// Package demo is a small in-memory approvals backend with sample cards, so
// that the owner can try the card and the list with no daemon.
//
// [Backend] has exactly the methods of the console's backend interface and
// nothing more, so the console can take it through that interface without
// this package importing the console. It is a backend of its own and not a
// stand-in for the real one: it imports the card, record, limits and
// presence packages and nothing the service could use (no door, no journal),
// and it has no method the service could call.
//
// The cards are samples. Each is built as a real one would be: a pending
// record written as bytes, read back by [record.DecodePending], and made into
// a card by [card.New]. A decision on a sample changes the backend's memory
// and nothing else: no file is written, no socket opened and no environment
// variable read. Every outcome line and every rule line says "sample", and
// the listing says it holds samples.
//
// The backend follows the real rules in memory: a decision needs recent
// activity (owner-away otherwise), names the binding digest the card showed,
// signs off exactly the flagged list, and an approve past the project's daily
// limit is held. The time comes from the clock given to [New].
package demo
