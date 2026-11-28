package leadattach

import (
	"sync"

	"ataniq/internal/leadholder/wire"
)

// outFrame is a frame waiting to be sent to the holder.
type outFrame struct {
	typ     byte
	payload []byte
	// n is how many pushes (keys, mouse events, pastes) the frame holds: a
	// run of keys shares a frame, and a frame that is given up loses them
	// all.
	n int
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
	// fenced refuses pushes: the client lost the writer role on this
	// connection, and a sink that took the connection before that must not
	// queue anything behind the fence. A Role frame that says writer again
	// lifts it.
	fenced bool
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
	if q.closed || q.fenced {
		q.mu.Unlock()
		return queueClosed
	}
	res := queued
	// The push counts once, in the frame that takes its first piece.
	count := 1
	for len(b) > 0 {
		n := min(len(b), q.limit)
		if tail := len(q.frames) - 1; tail >= 0 && q.frames[tail].typ == wire.TypeInput && len(q.frames[tail].payload)+n <= q.limit {
			q.frames[tail].payload = append(q.frames[tail].payload, b[:n]...)
			q.frames[tail].n += count
		} else if len(q.frames) >= maxQueued {
			res = q.dropLocked()
			break
		} else {
			q.frames = append(q.frames, outFrame{typ: wire.TypeInput, payload: append([]byte(nil), b[:n]...), n: count})
		}
		count = 0
		b = b[n:]
	}
	q.mu.Unlock()
	q.signal()
	return res
}

// pushPaste queues the text of a paste, which is already a valid payload.
func (q *outQueue) pushPaste(p []byte) queueResult {
	q.mu.Lock()
	if q.closed || q.fenced {
		q.mu.Unlock()
		return queueClosed
	}
	if len(q.frames) >= maxQueued {
		res := q.dropLocked()
		q.mu.Unlock()
		return res
	}
	q.frames = append(q.frames, outFrame{typ: wire.TypePaste, payload: p, n: 1})
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
	if q.closed || q.fenced {
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

// drainLocked drops everything queued and returns how many things it was: the
// pushes (keys, mouse events, pastes) the dropped frames held, and a size
// waiting to be sent as one.
func (q *outQueue) drainLocked() (dropped int) {
	for _, f := range q.frames {
		dropped += f.n
	}
	if q.resize != nil {
		dropped++
	}
	q.frames, q.resize, q.full = nil, nil, false
	return dropped
}

// close drops everything queued and refuses what comes. It returns how many
// things were dropped (see drainLocked).
func (q *outQueue) close() (dropped int) {
	q.mu.Lock()
	q.closed = true
	dropped = q.drainLocked()
	q.mu.Unlock()
	return dropped
}

// fence drops everything queued, and refuses pushes until unfence: the client
// is no longer the writer here. It returns how many things were dropped. A
// frame the sender already took from the queue is not among them.
func (q *outQueue) fence() (dropped int) {
	q.mu.Lock()
	q.fenced = true
	dropped = q.drainLocked()
	q.mu.Unlock()
	return dropped
}

// unfence takes pushes again after a Role frame made this client the writer.
func (q *outQueue) unfence() {
	q.mu.Lock()
	q.fenced = false
	q.mu.Unlock()
}
