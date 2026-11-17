package leadattach

import (
	"sync"

	"ataniq/internal/leadholder/wire"
)

// outFrame is a frame waiting to be sent to the holder.
type outFrame struct {
	typ     byte
	payload []byte
}

// queueResult says what a push did.
type queueResult int

const (
	queued queueResult = iota
	// queueFullFirst: the frame was dropped, and it is the first drop since
	// the queue was last empty (the caller says so once).
	queueFullFirst
	// queueFull: the frame was dropped after an earlier drop.
	queueFull
	// queueClosed: the connection is going away.
	queueClosed
)

// outQueue is the bounded queue of frames to the holder. Pushes never block.
// At most maxQueued frames wait; a byte string of input joins the Input frame
// at the tail when it fits under the payload cap, so that a burst of keys is
// a few frames. Only the newest Resize is kept, and it goes first.
type outQueue struct {
	mu     sync.Mutex
	frames []outFrame
	resize *wire.Size
	limit  int
	full   bool
	closed bool
	// sig tells the sender there is something to send.
	sig chan struct{}
}

func newOutQueue() *outQueue {
	return &outQueue{limit: wire.MaxClientPayload, sig: make(chan struct{}, 1)}
}

// setLimit sets the largest payload of a frame to the holder.
func (q *outQueue) setLimit(n int) {
	q.mu.Lock()
	q.limit = min(max(n, 1), wire.MaxClientPayload)
	q.mu.Unlock()
}

func (q *outQueue) payloadLimit() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.limit
}

func (q *outQueue) signal() {
	select {
	case q.sig <- struct{}{}:
	default:
	}
}

// pushInput queues bytes already encoded for the session.
func (q *outQueue) pushInput(b []byte) queueResult {
	if len(b) == 0 {
		return queued
	}
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return queueClosed
	}
	res := queued
	for len(b) > 0 {
		n := min(len(b), q.limit)
		if tail := len(q.frames) - 1; tail >= 0 && q.frames[tail].typ == wire.TypeInput && len(q.frames[tail].payload)+n <= q.limit {
			q.frames[tail].payload = append(q.frames[tail].payload, b[:n]...)
		} else if len(q.frames) >= maxQueued {
			res = q.dropLocked()
			break
		} else {
			q.frames = append(q.frames, outFrame{typ: wire.TypeInput, payload: append([]byte(nil), b[:n]...)})
		}
		b = b[n:]
	}
	q.mu.Unlock()
	q.signal()
	return res
}

// pushPaste queues the text of a paste, which is already a valid payload.
func (q *outQueue) pushPaste(p []byte) queueResult {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return queueClosed
	}
	if len(q.frames) >= maxQueued {
		res := q.dropLocked()
		q.mu.Unlock()
		return res
	}
	q.frames = append(q.frames, outFrame{typ: wire.TypePaste, payload: p})
	q.mu.Unlock()
	q.signal()
	return queued
}

// dropLocked records a drop and says whether it opens an overflow.
func (q *outQueue) dropLocked() queueResult {
	if q.full {
		return queueFull
	}
	q.full = true
	return queueFullFirst
}

// setResize replaces the size waiting to be sent. It reports whether the
// queue takes it.
func (q *outQueue) setResize(s wire.Size) bool {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return false
	}
	q.resize = &s
	q.mu.Unlock()
	q.signal()
	return true
}

// resizePending reports whether a size waits to be sent.
func (q *outQueue) resizePending() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.resize != nil
}

// pop returns the next frame to send: the size first, then the others in the
// order they were queued.
func (q *outQueue) pop() (outFrame, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.resize != nil {
		s := *q.resize
		q.resize = nil
		if p, err := wire.EncodeResize(s); err == nil {
			return outFrame{typ: wire.TypeResize, payload: p}, true
		}
	}
	if len(q.frames) == 0 {
		q.full = false
		return outFrame{}, false
	}
	f := q.frames[0]
	q.frames[0] = outFrame{}
	q.frames = q.frames[1:]
	if len(q.frames) == 0 {
		q.frames = nil
	}
	return f, true
}

// close drops everything queued and refuses what comes.
func (q *outQueue) close() {
	q.mu.Lock()
	q.closed = true
	q.frames, q.resize = nil, nil
	q.mu.Unlock()
}
