//go:build linux

package session

import (
	"bytes"
	"errors"
	"sync"
	"time"

	"ataniq/internal/leadholder/eventlog"
	"ataniq/internal/leadholder/ptyhost"
	"ataniq/internal/terminalconsole/sessionpane"
)

// The bytes the writer's input is held back for, whatever the terminal's
// settings say.
const ctrlZ = 0x1a

// Paste markers (bracketed paste, mode 2004).
const (
	pasteStart = "\x1b[200~"
	pasteEnd   = "\x1b[201~"
)

// Kinds of a held key, as the key-held event names them.
const (
	KindSuspend = "suspend"
	KindSignal  = "signal"
)

// Held says what a writer input left out and how it ended.
type Held struct {
	// Suspend counts the bytes held back as a suspend key: Ctrl+Z, and the
	// terminal's VSUSP while ISIG is set (design case H11).
	Suspend int
	// Signal counts the bytes held back as a signal key: the terminal's
	// VINTR and VQUIT while ISIG is set (case H12).
	Signal int
	// Written is how many bytes were written to the agent.
	Written int
	// Err is why the write failed, or ErrClosed; nil when it succeeded. A
	// failed write may have delivered a prefix.
	Err error
}

// Any reports whether a byte was held back, so the service can send a
// notice.
func (h Held) Any() bool { return h.Suspend > 0 || h.Signal > 0 }

// LastWriterInput returns when the writer last sent input (Input or Paste,
// whether or not anything was forwarded), or the zero time. A later injection
// uses it for the fence.
func (s *Session) LastWriterInput() time.Time {
	n := s.lastInput.Load()
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(0, n)
}

func (s *Session) noteInput() { s.lastInput.Store(time.Now().UnixNano()) }

// Input sends the writer's bytes to the agent after the held keys were taken
// out. It waits for the input lock. Ctrl+Z is never forwarded; with ISIG set
// the bytes for VINTR, VQUIT and VSUSP are not either. With ISIG clear (raw
// mode) 0x03 is forwarded. Each kind held is logged as a key-held event and
// counted.
func (s *Session) Input(b []byte) Held {
	s.noteInput()
	if len(b) == 0 {
		return Held{}
	}
	s.imu.Lock()
	h := s.writeFiltered(b, false)
	s.imu.Unlock()
	s.noteHeld(h)
	return h
}

// Paste sends the writer's pasted text. Held keys are taken out as for Input,
// every paste-end sequence (ESC [ 201 ~) is removed, and the text is wrapped in
// bracketed-paste markers when the agent set mode 2004, else it is written as it
// is.
func (s *Session) Paste(text string) Held {
	s.noteInput()
	if text == "" {
		return Held{}
	}
	s.imu.Lock()
	h := s.writeFiltered([]byte(text), true)
	s.imu.Unlock()
	s.noteHeld(h)
	return h
}

// writeFiltered filters b and writes it, with the input lock held.
func (s *Session) writeFiltered(b []byte, paste bool) Held {
	var h Held
	if s.closed.Load() {
		h.Err = ErrClosed
		return h
	}
	kinds := s.heldTable()
	body := make([]byte, 0, len(b))
	for _, c := range b {
		switch kinds[c] {
		case 0:
			body = append(body, c)
		case 1:
			h.Suspend++
		default:
			h.Signal++
		}
	}
	out := body
	if paste {
		// The held keys are out first: taking them out can join the parts
		// of a paste-end sequence, which the strip then removes.
		body = stripPasteEnd(body)
		out = body
		if len(body) > 0 && s.pane.Modes().BracketedPaste {
			out = append(append([]byte(pasteStart), body...), pasteEnd...)
		}
	}
	if len(out) == 0 {
		return h
	}
	n, err := s.host.Write(out)
	h.Written, h.Err = n, err
	if err != nil {
		s.cnt.inputWriteErrors.Add(1)
	}
	return h
}

// heldTable returns for each byte 0 (forward), 1 (held as a suspend key) or 2
// (held as a signal key), from the terminal's current settings. When they
// cannot be read the standard characters are assumed, so that a failure never
// lets a signal key through.
func (s *Session) heldTable() *[256]uint8 {
	var t [256]uint8
	tm, err := s.host.Termios()
	if err != nil {
		s.cnt.termiosErrors.Add(1)
		tm = ptyhost.Termios{ISIG: true, VINTR: 0x03, VQUIT: 0x1c, VSUSP: ctrlZ}
	}
	for _, c := range tm.HeldBack() {
		if c == tm.VSUSP {
			t[c] = 1
		} else {
			t[c] = 2
		}
	}
	t[ctrlZ] = 1
	return &t
}

// stripPasteEnd removes every paste-end sequence from b, including one that
// only appears once another was removed ("ESC [ 2 ESC [ 201 ~ 01 ~"). It is
// linear: a removal only looks at the end of what is kept.
func stripPasteEnd(b []byte) []byte {
	out := b[:0]
	for _, c := range b {
		out = append(out, c)
		if c == '~' && bytes.HasSuffix(out, []byte(pasteEnd)) {
			out = out[:len(out)-len(pasteEnd)]
		}
	}
	return out
}

// noteHeld counts and logs what a writer input held back: one event per kind
// per input, with the kind and the number of bytes, never the bytes or what
// was around them.
func (s *Session) noteHeld(h Held) {
	for _, k := range []struct {
		kind string
		n    int
	}{{KindSuspend, h.Suspend}, {KindSignal, h.Signal}} {
		if k.n == 0 {
			continue
		}
		if k.kind == KindSuspend {
			s.cnt.heldSuspend.Add(uint64(k.n))
		} else {
			s.cnt.heldSignal.Add(uint64(k.n))
		}
		s.event(eventlog.EventKeyHeld, eventlog.Fields{Reason: k.kind, Counters: map[string]uint64{"bytes": uint64(k.n)}})
	}
}

// replyQueue holds the emulator's replies on their way to the agent. push is
// the pane's Replies callback and returns at once; run writes the queue under
// the input lock. At most ReplyQueueBytes wait, and a reply that does not fit
// is dropped whole and counted.
type replyQueue struct {
	owner *Session
	mu    sync.Mutex
	buf   []byte
	wake  chan struct{}
	quit  chan struct{}
	exit  chan struct{}
	gone  bool
}

func (q *replyQueue) init() {
	q.wake = make(chan struct{}, 1)
	q.quit = make(chan struct{})
	q.exit = make(chan struct{})
}

func (q *replyQueue) push(p []byte) {
	q.mu.Lock()
	if q.gone {
		q.mu.Unlock()
		return
	}
	if len(q.buf)+len(p) > ReplyQueueBytes {
		q.mu.Unlock()
		q.owner.cnt.replyDroppedChunks.Add(1)
		q.owner.cnt.replyDroppedBytes.Add(uint64(len(p)))
		return
	}
	q.buf = append(q.buf, p...)
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

// pending returns how many bytes wait.
func (q *replyQueue) pending() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.buf)
}

func (q *replyQueue) take() []byte {
	q.mu.Lock()
	defer q.mu.Unlock()
	b := q.buf
	q.buf = nil
	return b
}

// run is the writer goroutine. It takes the input lock first and the queue
// after, so that what waits for the lock stays in the queue, under its bound.
func (q *replyQueue) run() {
	defer close(q.exit)
	s := q.owner
	for {
		select {
		case <-q.wake:
		case <-q.quit:
			return
		}
		s.imu.Lock()
		if batch := q.take(); len(batch) > 0 && !s.closed.Load() {
			if _, err := s.host.Write(batch); err != nil {
				s.cnt.replyWriteErrors.Add(1)
			}
		}
		s.imu.Unlock()
	}
}

func (q *replyQueue) stop() {
	q.mu.Lock()
	q.gone = true
	q.buf = nil
	q.mu.Unlock()
	close(q.quit)
	<-q.exit
}

// AgentWriter is what a critical section under the input lock can do: write
// bytes to the agent, as they are (no held-key filter: the caller of the
// critical section answers for them), and read the current screen. It is valid
// only inside the function WithInputLock calls.
type AgentWriter interface {
	// Write sends all of p to the agent or returns an error.
	Write(p []byte) (int, error)
	// Screen is Session.Screen: fn runs under the output lock.
	Screen(fn func(p *sessionpane.Pane, pos uint64))
}

type lockedWriter struct {
	s     *Session
	valid bool
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	if !w.valid {
		return 0, errors.New("session: the agent writer is used outside its critical section")
	}
	if w.s.closed.Load() {
		return 0, ErrClosed
	}
	n, err := w.s.host.Write(p)
	if err != nil {
		w.s.cnt.inputWriteErrors.Add(1)
	}
	return n, err
}

func (w *lockedWriter) Screen(fn func(p *sessionpane.Pane, pos uint64)) {
	if w.valid {
		w.s.Screen(fn)
	}
}

// WithInputLock takes the input lock for the whole of f. Writer input and the
// emulator's replies wait until f returns; nothing reaches the agent in
// between except what f writes. A later injection package uses it for its
// fence. f must not call Input, Paste or WithInputLock (the lock is not
// reentrant). It returns f's error, or ErrClosed.
func (s *Session) WithInputLock(f func(w AgentWriter) error) error {
	s.imu.Lock()
	defer s.imu.Unlock()
	if s.closed.Load() {
		return ErrClosed
	}
	w := &lockedWriter{s: s, valid: true}
	defer func() { w.valid = false }()
	return f(w)
}

// InjectRequest identifies an injection for the log. It carries no text: the
// session never sees the message, only the identity and the digest the caller
// made of it.
type InjectRequest struct {
	MessageID string
	Sender    string
	Digest    string
}

// Inject is the disabled stub of the injection (design section 4): it always
// returns ErrInjectionDisabled, after it logged an inject event with reason
// "disabled".
func (s *Session) Inject(req InjectRequest) error {
	s.event(eventlog.EventInject, eventlog.Fields{
		Reason: "disabled", Outcome: "refused",
		MessageID: req.MessageID, Sender: req.Sender, Digest: req.Digest,
	})
	return ErrInjectionDisabled
}
