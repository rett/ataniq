package sessionpane

import (
	"errors"
	"fmt"
)

// ErrOSCReserved is returned by HandleOSC for a command the pane or its
// emulator handles itself.
var ErrOSCReserved = errors.New("sessionpane: OSC command handled by the pane")

// reservedOSC are the OSC commands the pane and its emulator answer, which
// a caller's handler would pre-empt, and so change the state a snapshot
// carries: the titles and the icon name (0, 1, 2), the working directory (7),
// hyperlinks (8), the default colours and their resets (10 to 12, 110 to
// 112) and the clipboard (52).
var reservedOSC = map[int]bool{
	0: true, 1: true, 2: true, 7: true, 8: true,
	10: true, 11: true, 12: true, 52: true,
	110: true, 111: true, 112: true,
}

// HandleOSC lets the caller answer an OSC string the pane does not know, for
// example a status query of a provider. fn is called, on the goroutine that
// calls Write and with the pane locked, for every OSC string whose command
// number is cmd; data is the whole string as the parser collected it (the
// command number, a semicolon and the rest, at most 64 KiB) and is valid only
// during the call. fn must return promptly and must not call the pane. When
// handled is true the string is consumed and reply, if it is not empty, is
// written to the reply pipe in order with the emulator's other answers, so
// it reaches Options.Replies, or is discarded, as they do. When handled is
// false the next handler for cmd, if any, is tried, and then the string is
// ignored.
//
// More than one handler may be set for a command; the last set is tried
// first. A handler cannot be removed. A pane restored from a snapshot keeps
// its handlers: they are the caller's, not state. A pane that was restored
// still answers a session's queries, and a client discards those replies.
//
// The commands the pane handles itself (0, 1, 2, 7, 8, 10, 11, 12, 52, 110,
// 111 and 112) are refused with ErrOSCReserved, as are a nil fn and a
// negative cmd.
func (p *Pane) HandleOSC(cmd int, fn func(data []byte) (reply []byte, handled bool)) error {
	if cmd < 0 || fn == nil {
		return fmt.Errorf("sessionpane: HandleOSC(%d): no command or no function", cmd)
	}
	if reservedOSC[cmd] {
		return fmt.Errorf("%w: %d", ErrOSCReserved, cmd)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.emu.RegisterOscHandler(cmd, func(data []byte) bool {
		reply, handled := fn(data)
		if !handled {
			return false
		}
		if len(reply) > 0 {
			_, _ = p.pipe.Write(reply)
		}
		return true
	})
	return nil
}
