package seatapp

import "time"

// WatchCard is one pending card in the watcher's state: what tells one card
// from another, never card text.
type WatchCard struct {
	ID            string
	Kind          string
	BindingSHA256 string
	// Held is the daemon's mark "decided past the limit".
	Held bool
}

// WatchState is where the pending cards stand, as the daemon port's watcher
// last learned from the gate.
type WatchState struct {
	// Watching is true when the gate offers the long wait and the watcher
	// runs. When it is false nothing is watched, whatever else the state
	// says (the zero state is a port that does not watch).
	Watching bool
	// Version is the pending set's version the gate last answered, empty
	// before the first answer.
	Version string
	// Waiting is how many cards are pending, the whole set.
	Waiting int
	// Cards are the pending cards the gate listed.
	Cards []WatchCard
	// More is true when Waiting is more than the cards listed.
	More bool
	// Offline is true when the gate is out of reach or failed its last
	// wait; Version, Waiting and Cards are then the last known, which may
	// be old.
	Offline bool
	// At is when the state was last published.
	At time.Time
}

// Watcher is the part of a [Daemon] that watches the pending cards for the
// live refresh (package seatlink keeps one seat.wait in flight at the gate
// for the whole seat, so a browser's poll never reaches the gate). The app
// reads its state to answer POST /api/watch and to put the version in the
// card pages. A port without it, or one whose state says it does not watch,
// is "not live": a poll is answered offline at once.
//
// WatchChanged returns a channel that is closed when the state next
// changes. To miss no change, take the channel first and the state after.
// A port that does not watch may return a channel that is never closed
// (nil).
type Watcher interface {
	Watch() WatchState
	WatchChanged() <-chan struct{}
}
