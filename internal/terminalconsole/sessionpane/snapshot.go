package sessionpane

import (
	"fmt"

	"github.com/charmbracelet/x/ansi"
)

// A snapshot lets a second pane, the attach client's, take over the screen of
// the first, the holder's, and then follow it by being fed the same output
// bytes (the lead-hosting design, section 2.3). The snapshot is the real
// state of the emulator, which is everything later output can depend on: the
// size, both screens with their cells, styles and links, the cursors, the
// scroll region, the tab stops, the modes, the character sets, the pen, the
// open hyperlink, the open grapheme cluster with whether it is capped, the
// last character, the colours the session set and the title. It is never the
// frame held for synchronized output (mode 2026), which is a presentation
// copy: the mode is carried as set, and the restored pane shows its real
// screen until the session ends the update.
//
// The pane the snapshot is restored into is equal to the first for ever:
// after Restore, writing the bytes the first pane received after the
// snapshot gives a pane with the same Digest, and the same state in every
// field the snapshot carries. The property test of this package checks that
// on many streams, cut at many points.
//
// What a snapshot does not carry is what the pane is given and not what the
// session did: the options (reply policy, input sink, clock, colours the
// pane reports by default, grapheme widths), the handlers set with
// HandleOSC, the counters, the end of the session, and the frame held for
// synchronized output. The replies of a restored pane are made as any pane's
// are (it answers a session's queries), and a client discards them: only the
// holder answers the session.

// AppendSnapshot appends a snapshot of the pane's real current state to dst
// and returns the extended slice. It fails with ErrNotQuiescent when the
// parser is not at ground, with ErrSnapshotTooLarge when the snapshot would
// be over MaxSnapshotBytes, and with ErrUnsupportedState for a state that
// only the API, not output, can make. On an error dst is returned as it was.
// The pane is not changed. It works after End and Close too.
func (p *Pane) AppendSnapshot(dst []byte) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.emu.Quiescent() {
		return dst, ErrNotQuiescent
	}
	st := p.emu.State()
	e := &encoder{b: dst, start: len(dst)}
	if err := e.encodeState(&st, p.title); err != nil {
		return dst, err
	}
	return e.b, nil
}

// Restore replaces the pane's whole emulator state, and its size, with the
// snapshot's. The snapshot is untrusted: it is decoded strictly and within
// bounds (a size inside the pane's limits, content of at most 64 bytes in a
// cell, at most 256 links per screen of at most 2 KiB and 256 bytes, modes of
// the emulator's table, no byte left over, at most MaxSnapshotBytes) and any
// violation is an error wrapping ErrBadSnapshot, never a panic, and leaves
// the pane as it was. The links go through the emulator's admission, so the
// cells share one copy of each link.
//
// Restore is refused with ErrEnded after End or Close. It may be called any
// number of times, in the middle of a sequence too: the parser starts again at
// ground. It is not a Write: it makes no reply and moves no callback, and the
// frame held for synchronized output is dropped.
func (p *Pane) Restore(snapshot []byte) error {
	p.mu.Lock()
	gone := p.ended || p.closed
	p.mu.Unlock()
	if gone {
		return ErrEnded
	}
	// Decoding takes no pane state, so it runs outside the lock: a large
	// snapshot does not keep a Write waiting.
	st, title, err := decodeSnapshot(snapshot)
	if err != nil {
		return err
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ended || p.closed {
		return ErrEnded
	}
	if err := p.emu.SetState(st); err != nil {
		return fmt.Errorf("%w: %v", ErrBadSnapshot, err)
	}
	p.cols, p.rows = st.Width, st.Height
	p.title = title
	p.dec = decModes{}
	p.syncOut.on = false
	for _, m := range st.Modes {
		d, ok := m.Mode.(ansi.DECMode)
		switch {
		case !ok:
		case d == ansi.ModeSynchronizedOutput:
			// Mode 2026 as it is, without the copy of the screen that
			// setting it would take.
			p.syncOut.on = m.Setting.IsSet()
		default:
			p.setMode(d, m.Setting.IsSet())
		}
	}
	// A frame held for synchronized output is a copy of a screen that is no
	// longer there.
	p.syncOut.held, p.syncOut.kept, p.syncOut.closesLater = false, false, false
	return nil
}
