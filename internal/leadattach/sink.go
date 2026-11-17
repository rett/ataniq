package leadattach

import (
	"strings"
	"unicode/utf8"

	uv "github.com/charmbracelet/ultraviolet"

	"ataniq/internal/leadholder/wire"
	"ataniq/internal/terminalconsole/sessionpane"
)

// inputSink is the sessionpane.InputSink the client returns from Start. It
// sends input only while the client is attached as the writer; otherwise it
// drops the input and counts it in LiveStatus.RefusedInput. Nothing it does
// blocks: a frame is queued, and a full queue drops it.
type inputSink struct{ c *Client }

var _ sessionpane.InputSink = (*inputSink)(nil)

// writerConn returns the connection input goes to and the session's modes,
// or counts the input as refused and returns nil.
func (c *Client) writerConn() (*conn, ModeSet) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cn := c.inputConnLocked()
	if cn == nil {
		c.st.RefusedInput++
		return nil, ModeSet{}
	}
	return cn, c.modes
}

// pushed handles what the queue said of a frame it was given: a drop is
// counted, and the first of a run of drops says so in a notice.
func (c *Client) pushed(res queueResult) {
	switch res {
	case queued:
		return
	case queueClosed:
		c.mu.Lock()
		c.st.RefusedInput++
		c.mu.Unlock()
	case queueFullFirst:
		c.mutate(func() {
			c.cnt.inputDropped++
			c.addNoticeLocked(noticeInputDropped)
		})
	default:
		c.mu.Lock()
		c.cnt.inputDropped++
		c.mu.Unlock()
	}
}

var noticeInputDropped = liveNotice("input-dropped", "The holder is not taking input fast enough: input was dropped.")

// Key encodes the key for the session's modes as the holder reports them and
// queues it as input.
func (s *inputSink) Key(k uv.KeyPressEvent) {
	c := s.c
	cn, ms := c.writerConn()
	if cn == nil {
		return
	}
	b := EncodeKey(k, ms, KeyOptions{ShiftEnter: c.cfg.ShiftEnter})
	if len(b) == 0 {
		c.mu.Lock()
		c.cnt.keysUnencodable++
		c.mu.Unlock()
		return
	}
	c.pushed(cn.q.pushInput(b))
}

// Mouse encodes the mouse event (in the session's cells) for the session's
// mouse modes as the holder reports them and queues it as input.
func (s *inputSink) Mouse(ev uv.MouseEvent) {
	c := s.c
	cn, ms := c.writerConn()
	if cn == nil {
		return
	}
	if b := EncodeMouse(ev, ms); len(b) > 0 {
		c.pushed(cn.q.pushInput(b))
	}
}

// Paste queues the text as a Paste frame, which the holder wraps for the
// session's paste mode. A paste over the payload cap is refused whole (and
// said in a notice): sent in pieces, the session would see several pastes.
// Invalid UTF-8 is replaced.
func (s *inputSink) Paste(text string) {
	c := s.c
	cn, _ := c.writerConn()
	if cn == nil {
		return
	}
	if !utf8.ValidString(text) {
		text = strings.ToValidUTF8(text, "�")
	}
	if text == "" {
		return
	}
	p, err := wire.EncodePaste(text)
	if err != nil || len(p) > cn.q.payloadLimit() {
		c.mutate(func() {
			c.cnt.pasteRefused++
			c.addNoticeLocked(noticePasteTooLarge)
		})
		return
	}
	c.pushed(cn.q.pushPaste(p))
}

var noticePasteTooLarge = liveNotice("paste-too-large", "Paste not sent: it is longer than the holder takes in one piece (64 KiB).")
