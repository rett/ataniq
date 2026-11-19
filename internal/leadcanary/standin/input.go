//go:build linux && ataniq_canary

package standin

import "bytes"

// What the scanner makes of the bytes the terminal sends.
type eventKind int

const (
	// evKey is bytes typed, or a sequence the scanner does not know. Data
	// holds them.
	evKey eventKind = iota
	// evCR is a carriage return outside a paste.
	evCR
	// evPaste is a whole bracketed paste. Data holds its text, without the
	// markers.
	evPaste
	// evReply7501 is the reply to the OSC 7501 support query.
	evReply7501
	// evReplyDA1 is a device attributes reply.
	evReplyDA1
)

type event struct {
	kind eventKind
	data []byte
}

// The caps of the scanner. What is over a cap is dropped, never kept.
const (
	// maxSequence is the longest control sequence the scanner reads. A longer
	// one is handed on as typed bytes.
	maxSequence = 256
	// maxPaste is the most bytes of one paste that are kept. The rest of the
	// paste is read and dropped.
	maxPaste = 1 << 20
)

var (
	pasteStart = []byte("\x1b[200~")
	pasteEnd   = []byte("\x1b[201~")
)

type scanState int

const (
	stGround scanState = iota
	stEsc
	stCSI
	stOSC
	stOSCEsc
	stPaste
)

// scanner splits the terminal's input into events. It reads the bytes of the
// terminal only for their shape: inside a paste everything is text until the
// closing marker; outside one it knows the paste markers, the reply to the
// OSC 7501 query and a device attributes reply, and treats everything else
// as keys. It holds no more than maxSequence bytes of a sequence and maxPaste
// of a paste.
type scanner struct {
	state scanState
	seq   []byte // the sequence being read, from its ESC
	paste []byte
	tail  []byte // the last bytes of the paste, to find its closing marker
	over  bool   // the paste is over maxPaste: the rest of it is not kept
}

// feed reads b and returns the events it completes. The scanner keeps what it
// has not completed for the next call.
func (s *scanner) feed(b []byte) []event {
	var out []event
	var keys []byte
	flush := func() {
		if len(keys) > 0 {
			out = append(out, event{evKey, keys})
			keys = nil
		}
	}
	for _, c := range b {
		switch s.state {
		case stGround:
			switch c {
			case 0x1b:
				s.state, s.seq = stEsc, append(s.seq[:0], c)
			case '\r':
				flush()
				out = append(out, event{evCR, nil})
			default:
				keys = append(keys, c)
			}
		case stEsc:
			switch c {
			case '[':
				s.state, s.seq = stCSI, append(s.seq, c)
			case ']':
				s.state, s.seq = stOSC, append(s.seq, c)
			case 0x1b:
				// A lone ESC is a key; the second one starts a sequence.
				keys = append(keys, 0x1b)
				s.seq = append(s.seq[:0], c)
			default:
				keys = append(keys, 0x1b, c)
				s.state = stGround
			}
		case stCSI:
			s.seq = append(s.seq, c)
			switch {
			case c >= 0x40 && c <= 0x7e:
				s.state = stGround
				switch {
				case bytes.Equal(s.seq, pasteStart):
					flush()
					s.state, s.paste, s.tail, s.over = stPaste, s.paste[:0], s.tail[:0], false
				case c == 'c' && len(s.seq) > 2 && s.seq[2] == '?':
					flush()
					out = append(out, event{evReplyDA1, nil})
				default:
					keys = append(keys, s.seq...)
				}
			case c >= 0x20 && c <= 0x3f:
				if len(s.seq) > maxSequence {
					keys = append(keys, s.seq...)
					s.state = stGround
				}
			default:
				keys = append(keys, s.seq...)
				s.state = stGround
			}
		case stOSC:
			switch {
			case c == 0x07:
				flush()
				out = s.endOSC(out)
			case c == 0x1b:
				s.state = stOSCEsc
			default:
				s.seq = append(s.seq, c)
				if len(s.seq) > maxSequence {
					s.state, s.seq = stGround, s.seq[:0]
				}
			}
		case stOSCEsc:
			if c == '\\' {
				flush()
				out = s.endOSC(out)
			} else {
				// Not a string terminator: the string is abandoned.
				s.state, s.seq = stGround, s.seq[:0]
			}
		case stPaste:
			if len(s.paste) >= maxPaste {
				s.over = true
			} else {
				s.paste = append(s.paste, c)
			}
			if len(s.tail) == len(pasteEnd) {
				copy(s.tail, s.tail[1:])
				s.tail[len(s.tail)-1] = c
			} else {
				s.tail = append(s.tail, c)
			}
			if c == '~' && bytes.Equal(s.tail, pasteEnd) {
				flush()
				text := s.paste
				if !s.over {
					text = text[:len(text)-len(pasteEnd)]
				}
				out = append(out, event{evPaste, append([]byte(nil), text...)})
				s.state, s.paste, s.tail, s.over = stGround, s.paste[:0], s.tail[:0], false
			}
		}
	}
	flush()
	return out
}

// endOSC ends the OSC string in seq (which starts "ESC ]") and returns out
// with its event, when it is the reply to the support query.
func (s *scanner) endOSC(out []event) []event {
	payload := s.seq[2:]
	s.state, s.seq = stGround, s.seq[:0]
	if bytes.Equal(payload, []byte("7501;?")) {
		out = append(out, event{evReply7501, nil})
	}
	return out
}
