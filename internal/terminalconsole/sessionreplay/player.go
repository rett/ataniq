package sessionreplay

import "time"

// Player replays a recording against a clock the caller owns. It reads no
// clock and starts no goroutine: Advance takes the time elapsed since the
// start of the replay and returns the events that have become due. It is not
// safe for concurrent use.
type Player struct {
	rec  *Recording
	next int
}

// NewPlayer returns a player at the start of a copy of rec. It fails when the
// recording is not valid.
func NewPlayer(rec *Recording) (*Player, error) {
	if err := rec.Validate(); err != nil {
		return nil, err
	}
	return &Player{rec: rec.clone()}, nil
}

// Size returns the terminal size at the start of the recording.
func (p *Player) Size() (cols, rows int) { return p.rec.Cols, p.rec.Rows }

// Advance returns, in order, the events not yet returned whose time is at or
// before elapsed, which is the time since the start of the replay. Every event
// is returned once. An elapsed time that is not past the last one returns
// nothing, so a caller may repeat or lower it. A jump past several events
// returns all of them. The data of the returned events is the caller's to keep
// or change.
func (p *Player) Advance(elapsed time.Duration) []Event {
	var due []Event
	for p.next < len(p.rec.Events) && p.rec.Events[p.next].At <= elapsed {
		e := p.rec.Events[p.next]
		e.Data = append([]byte(nil), e.Data...)
		due = append(due, e)
		p.next++
	}
	return due
}

// Next returns the time of the next event, so a caller can schedule its next
// tick. It reports false when every event has been returned.
func (p *Player) Next() (at time.Duration, ok bool) {
	if p.next >= len(p.rec.Events) {
		return 0, false
	}
	return p.rec.Events[p.next].At, true
}

// Done reports whether every event, the end included, has been returned.
func (p *Player) Done() bool { return p.next >= len(p.rec.Events) }

// Reset returns the player to the start of the recording.
func (p *Player) Reset() { p.next = 0 }
