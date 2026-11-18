//go:build linux

package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"ataniq/internal/terminalconsole/sessionpane"
)

// The fence. WithFence is the session's side of a critical section that reads
// the screen and writes to the agent with nothing else writing and, while a
// write is made, nothing applied to the screen: the input lock held for the
// whole of a function, the two calls the function reads and writes with
// (Settle and Frozen) and the view of the screen it reads from. The session
// imports neither the injection library that uses it nor its parser: the types
// below are its own, and the library's adapter converts them field by field.
//
// # How the output is held
//
// With no fence the reader applies each slice itself, as it always did. While
// a fence holds the input lock the reader keeps reading the PTY master but does
// not apply: it appends what it read to a buffer (admit), up to
// HeldOutputBytes, and the fence's goroutine applies the buffer (Settle, and
// the end of the fence). So between two calls of the fence the emulator changes
// only when the fence says so, Frozen has nothing to freeze but the call it is
// in, and an agent that blocks on its own output is read all the same.
//
// When the buffer is full the reader waits for room, and drops nothing: a
// dropped byte would leave the emulator's screen and parser out of step with
// the agent's and nothing could say so later. The wait ends when a Settle takes
// the buffer, when the fence ends, or when the session closes, and it is
// counted ("held_output_full"). The agent then fills the PTY's own buffer and
// blocks on its output; a Frozen write that waits for the agent to read its
// input stays blocked until the write's bound passes, and the write returns the
// bound's error, never completed or retried. That is the one case where a
// frozen buffer stops the reader, and it stops it until the fence ends, not for
// good: output of 4 MiB inside one fence is a flood the fence's own screen
// check would have refused, and the outcome is the write's error, which the
// fence reports as died with Written unknown.
//
// The end of a fence applies what the reader held for at most endFenceBudget.
// If the agent writes faster than the emulator applies, what is left goes to a
// goroutine (drainHeld) that applies it in order while the reader goes on
// holding, and the reader applies again when that backlog is gone: the fence
// returns, no byte is dropped and none is applied out of order. One drainHeld
// lives at a time; a fence that begins while it runs finds it applying what the
// last fence left, between the fence's own calls (never inside Frozen, which
// holds the output lock), and its Settle waits for it like for any output. The
// output of an agent that ends while a fence holds some is applied by the
// lifetime path before the pane ends, so the last screen has it: the path
// applies the buffer and then waits for the slice another applier may have
// taken from it (applyLast).
//
// # Lock order
//
// The order in which locks may be taken is, outermost first:
//
//	imu        the input lock: writer input, the reply writer, WithInputLock
//	           and WithFence, whole critical sections
//	apply      one applier of held output at a time, for one slice (Settle,
//	           the end of a fence, drainHeld, the lifetime path), so that held
//	           bytes are applied in the order they were read
//	omu        the output lock: pane writes in slices, the subscriber,
//	           Resize, Screen, and the whole of a Frozen function
//	pane       the pane's own lock (sessionpane), taken inside its methods
//	           and, for the OSC hooks and the emulator's callbacks, while it
//	           applies a slice
//
// and the leaves, which are held for a few statements, with no other lock of
// this list taken inside them except where written here:
//
//	hold.mu    the fence's buffer and flags (waited on only through its
//	           condition, which releases it); the reader takes it alone
//	scr.mu     the screen change tracker; it takes the pane's lock inside
//	           (scr.mu before the pane, never the reverse)
//	replies.mu the reply queue; the pane's drain goroutine takes it through
//	           the Replies callback and nothing else
//	cnt.mu, fmu
//
// The reader never holds hold.mu while it takes omu, and takes omu only for a
// slice it applies with no fence active; the fence, which begins by waiting
// until that slice is done, never waits for omu with the reader inside it. The
// goroutine that carries replies (the pane's drain, which calls the Replies
// callback) takes replies.mu and nothing else, so the emulator never waits on
// it, and the reply writer (replyQueue.run) takes imu before it takes the
// queue, so a fence that holds imu keeps the replies in the bounded queue.
// Settle writes the queued replies itself (FlushReplies, which does not take
// imu: the fence owns it). SyncReplies is called with none of omu or the
// pane's lock held by the caller.

// inputLock is the input lock. It is a mutex whose waiters are served in the
// order they came (a channel's blocked senders are queued, and each release
// hands the lock to the first), and which a waiter can leave when a context
// ends (lockCtx). Writer input that arrives while a fence holds the lock
// therefore reaches the agent in the order it came, which a sync.Mutex does not
// promise. The zero value is not usable; init makes it.
type inputLock struct{ ch chan struct{} }

func (l *inputLock) init() { l.ch = make(chan struct{}, 1) }

// Lock takes the lock, waiting for it.
func (l *inputLock) Lock() { l.ch <- struct{}{} }

// Unlock releases the lock. It panics when the lock is not held.
func (l *inputLock) Unlock() {
	select {
	case <-l.ch:
	default:
		panic("session: unlock of an unlocked input lock")
	}
}

// lockCtx takes the lock, or returns ctx's error if ctx ends first. When ctx
// has ended already it returns the error and does not take the lock, even if it
// is free.
func (l *inputLock) lockCtx(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case l.ch <- struct{}{}:
		if err := ctx.Err(); err != nil {
			l.Unlock()
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// OSCHook answers an OSC string the pane does not know, for a command number.
// It is the signature of Pane.HandleOSC: Fn is called with the whole string as
// the parser collected it (the command number, a semicolon and the rest) and,
// when it reports handled, its reply joins the queue of the emulator's replies
// at that point. It is called on the goroutine that applies the output, in the
// order the emulator meets the strings, with the pane locked: it must return
// promptly and must not call the session or the pane. While a fence holds the
// input lock output is applied only by Settle and at the end of the fence (and
// by the drain a previous fence left, if one is still running), so the hook is
// not called between them otherwise.
type OSCHook struct {
	Cmd int
	Fn  func(data []byte) (reply []byte, handled bool)
}

// TermState is what the terminal parser says about the output applied so far.
type TermState struct {
	// ParserGround: the parser is between sequences (no escape sequence,
	// string or partly received character is open).
	ParserGround bool
	// FrameOpen: a synchronized-output frame (DEC private mode 2026) is open.
	FrameOpen bool
	// BracketedPaste: the agent turned bracketed paste (mode 2004) on.
	BracketedPaste bool
}

// OwnerNeverTyped is what FenceView.OwnerQuiet returns when the writer has
// never sent input: quiet for as long as can be said.
const OwnerNeverTyped = time.Duration(1 << 62)

// FenceView is the session's state at one instant, for a function that runs
// inside Fenced.Frozen. Every method but Pending answers from what the
// emulator has applied, and nothing is applied while the function runs, so two
// calls see the same state (ScreenQuiet and OwnerQuiet are times and grow).
type FenceView interface {
	// Snapshot is the screen as text, with its size, the cursor, the
	// alternate screen flag, the title and the faint spans as offsets in the
	// text of their row (not cell columns).
	Snapshot() sessionpane.TextSnapshot
	// Term is the parser, frame and bracketed-paste state.
	Term() TermState
	// OwnerQuiet is the time since the writer last sent input (Input or
	// Paste), counted from when it was sent, not from when it reached the
	// agent: a key typed while a fence held the lock counts from then. It is
	// OwnerNeverTyped when there has been none.
	OwnerQuiet() time.Duration
	// ScreenQuiet is the time since applied output last changed the screen
	// (its cells, cursor, modes, size or title). Output that changes
	// nothing, an answered query or a status report, does not restart it.
	// It is found by comparing the screen's digest, on a timer at most once
	// in ScreenCheckInterval while output is applied, and again each time it
	// is asked. Each slice applied is noted before the output lock is
	// released, so a fence that holds that lock (Frozen) is told of every
	// slice applied before it. The answer is exact to the last slice
	// applied: it can be shorter than the time since the screen changed (by
	// the output that changed nothing after the change, within one
	// interval), and it is longer only when the screen was changed and
	// changed back between two comparisons, which is seen as no change at
	// all.
	ScreenQuiet() time.Duration
	// Ended is true when the agent's process has ended.
	Ended() bool
	// Pending is true when output of the agent has not been applied: bytes
	// queued on the PTY master (asked of the kernel, not read), or bytes the
	// reader has read since its read returned and the emulator has not taken
	// (the buffer a fence holds, or a slice on its way into it). It is asked
	// at each call and never cached. Output the agent has written that the
	// kernel has not yet handed to the master, or that the reader has taken
	// and not yet counted, is not seen.
	Pending() bool
}

// Fenced is what a function that WithFence runs can do. Its methods are valid
// only inside that function, on its goroutine.
type Fenced interface {
	// Settle applies everything the reader has held and anything that
	// arrives, on the caller's goroutine, and returns when quiet has passed
	// with no new output and at least min has passed since the call, or max
	// has. The quiet is counted from the last output read or from the call,
	// whichever is later. It returns drained false when max ended the wait
	// with output still arriving (or still unapplied). The replies the
	// applied output caused, the emulator's and the OSC hooks', are written
	// to the agent in the order they were made before it returns (it writes
	// them after each batch it applies, so that an agent that waits for an
	// answer to a query goes on, and once more at the end). An error means
	// the session cannot be read any more (the output stream ended), that
	// the replies cannot be written (ErrClosed, ErrInputBroken or the
	// write's error), or that ctx ended, and then it wraps ctx.Err().
	Settle(ctx context.Context, min, quiet, max time.Duration) (drained bool, err error)
	// Frozen runs fn once, on the calling goroutine, with no output applied
	// while it runs. The reader goes on reading into the fence's buffer and
	// does not take the output lock; Resize, Screen, Subscribe and the
	// subscriber's calls wait until fn returns, and the buffer is applied by
	// the next Settle or when the fence ends. write is one write to the
	// agent with the bound deadline (the earlier of it and the host's write
	// timeout): it returns an error when the bytes were not all accepted by
	// then, and then they may or may not have reached the agent. A write
	// that fails is never retried or completed and the session writes no
	// paste end of its own; the input is broken (InputBroken) like after any
	// failed write, so nothing more is written from any source. write is
	// valid only inside fn. Frozen returns fn's error. fn must return by
	// deadline, and must not call the session's Screen, Resize, Subscribe or
	// WithInputLock, which would wait for it.
	Frozen(deadline time.Time, fn func(v FenceView, write func([]byte) error) error) error
}

// ErrStreamEnded: the agent's output stream ended, so Settle has nothing more
// to read.
var ErrStreamEnded = errors.New("session: the agent's output stream ended")

// heldOutput is the buffer the reader fills while a fence holds the input
// lock, with the flags that hand the applying of output between the reader and
// the fence.
type heldOutput struct {
	mu   sync.Mutex
	cond *sync.Cond // on mu: applying fell to false

	fenced   bool // a fence holds the input lock: the reader holds, it does not apply
	draining bool // the end of a fence left a backlog for drainHeld: the reader holds
	applying bool // the reader is applying a slice with no fence
	buf      []byte
	limit    int

	room   chan struct{} // cap 1: a take made room in buf
	arrive chan struct{} // cap 1: bytes were added to buf

	// drainDone is closed when the drainHeld goroutine of the last hand-off
	// returned; nil if there was none. draining is true exactly while that
	// goroutine lives, so there is at most one.
	drainDone chan struct{}
	drains    int // drainHeld goroutines started (tests)

	// apply serialises the appliers of held bytes (see the lock order).
	apply sync.Mutex
}

func (h *heldOutput) init(limit int) {
	h.cond = sync.NewCond(&h.mu)
	h.limit = limit
	h.room = make(chan struct{}, 1)
	h.arrive = make(chan struct{}, 1)
}

func poke(c chan struct{}) {
	select {
	case c <- struct{}{}:
	default:
	}
}

// applied is called by the reader when a slice it applied is done.
func (h *heldOutput) applied() {
	h.mu.Lock()
	h.applying = false
	h.mu.Unlock()
	h.cond.Broadcast()
}

// admit decides what the reader does with the rest of a chunk. With no fence,
// and no backlog a fence left, it returns the chunk and true: the reader
// applies a slice itself and calls hold.applied. While a fence holds the lock,
// or until its backlog is applied, it keeps the chunk in the buffer, waiting
// for room when the buffer is full, and returns false with nothing left to
// apply; if the hold ends while it waits, the rest is returned to apply. It
// returns false and drops the rest only when the session is closing.
func (s *Session) admit(chunk []byte) (rest []byte, apply bool) {
	h := &s.hold
	h.mu.Lock()
	counted := false
	for (h.fenced || h.draining) && len(chunk) > 0 {
		if room := h.limit - len(h.buf); room > 0 {
			n := min(room, len(chunk))
			h.buf = append(h.buf, chunk[:n]...)
			chunk = chunk[n:]
			poke(h.arrive)
			continue
		}
		if !counted {
			counted = true
			s.cnt.heldOutputFull.Add(1)
		}
		h.mu.Unlock()
		select {
		case <-h.room:
		case <-s.closing:
			s.unapplied.Add(-int64(len(chunk)))
			return nil, false
		}
		h.mu.Lock()
	}
	if len(chunk) == 0 {
		h.mu.Unlock()
		return nil, false
	}
	h.applying = true
	h.mu.Unlock()
	return chunk, true
}

// takeSlice removes and returns the first slice of the held bytes: at most
// OutputSlice of them, or nil if there are none. The caller holds hold.apply, so
// the slices leave the buffer, and are applied, in the order they were read.
func (s *Session) takeSlice() []byte {
	h := &s.hold
	h.mu.Lock()
	n := min(len(h.buf), OutputSlice)
	b := h.buf[:n:n]
	if n == len(h.buf) {
		h.buf = nil
	} else {
		h.buf = h.buf[n:]
	}
	h.mu.Unlock()
	if n > 0 {
		poke(h.room)
	}
	return b
}

func (s *Session) heldEmpty() bool {
	h := &s.hold
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.buf) == 0
}

// applyHeld applies what the reader held when it was called, in slices of at
// most OutputSlice bytes, each under its own hold of the output lock, as the
// reader does. The applier lock is held for one slice at a time, so that
// another applier (a fence's Settle, the end of a fence, drainHeld) waits for at
// most one slice and not for a whole batch; the slices still leave the buffer
// in the order they were read. stop, when given, is asked before each slice
// with the number of slices applied so far in this call, and the call ends when
// it says so. It reports whether it applied anything and whether it stopped.
func (s *Session) applyHeld(stop func(applied int) bool) (applied, stopped bool) {
	h := &s.hold
	h.mu.Lock()
	want := len(h.buf)
	h.mu.Unlock()
	for i := 0; want > 0; i++ {
		h.apply.Lock()
		if stop != nil && stop(i) {
			h.apply.Unlock()
			return applied, true
		}
		slice := s.takeSlice()
		if len(slice) == 0 {
			h.apply.Unlock()
			break
		}
		s.deliverSlice(slice)
		h.apply.Unlock()
		applied = true
		want -= len(slice)
	}
	return applied, false
}

// applyLast is the lifetime path's applying of held output, called when the
// reader is gone, so that nothing more enters the buffer: when it returns,
// every byte the reader held has been applied. applyHeld returns with the
// buffer empty, but another applier (drainHeld, a fence's Settle or the end of
// a fence) may have taken the last slice from it and not have applied it yet.
// It holds the applier lock until it has, so taking and releasing that lock
// waits for the slice: for one slice, or for the Frozen function that the
// other applier waits behind.
func (s *Session) applyLast() {
	s.applyHeld(nil)
	s.hold.apply.Lock()
	s.hold.apply.Unlock()
}

// release lets the reader apply again, if nothing is held: it clears the flag
// of a fence (drain false) or of drainHeld (drain true). It takes the applier
// lock first, so that a slice another applier took from the buffer is applied
// before the reader can apply a later byte, and tests the buffer under the lock
// the reader adds to it with. It reports whether the flag fell.
func (s *Session) release(drain bool) bool {
	h := &s.hold
	h.apply.Lock()
	defer h.apply.Unlock()
	h.mu.Lock()
	if len(h.buf) > 0 {
		h.mu.Unlock()
		return false
	}
	if drain {
		h.draining = false
	} else {
		h.fenced = false
	}
	h.mu.Unlock()
	poke(h.room)
	return true
}

// beginFence makes the reader hold. It returns when the slice the reader may be
// applying is done, so that from then on the reader neither applies nor waits
// for the output lock.
func (s *Session) beginFence() {
	h := &s.hold
	h.mu.Lock()
	h.fenced = true
	for h.applying {
		h.cond.Wait()
	}
	h.mu.Unlock()
}

// endFenceBudget is how long the end of a fence applies what the reader held
// before it hands the rest to a goroutine (see endFence).
const endFenceBudget = 250 * time.Millisecond

// endFence applies what the reader held and lets it apply again. The bytes are
// applied before the flag falls, and the flag falls only with the buffer empty,
// under the lock the reader adds to it with, so the order of the output is
// kept. It applies for at most endFenceBudget, checked before each slice, so it
// returns within the budget and one slice (plus a wait for the slice another
// applier has in hand): an agent that writes faster than the emulator applies
// would keep the fence from ending, which no unfenced reader would suffer. What
// is left then goes to drainHeld, a goroutine that applies it in order while
// the reader goes on holding, and the flag for it falls the same way. Only one
// drainHeld lives at a time: when one is running already it applies what is
// left, and the end of this fence leaves it to it.
func (s *Session) endFence() {
	h := &s.hold
	end := time.Now().Add(endFenceBudget)
	over := func(int) bool { return !time.Now().Before(end) }
	for {
		s.applyHeld(over)
		if s.release(false) {
			return
		}
		if !time.Now().Before(end) {
			break
		}
	}
	h.mu.Lock()
	h.fenced = false
	if !h.draining {
		h.draining = true
		h.drainDone = make(chan struct{})
		h.drains++
		go s.drainHeld(h.drainDone)
	}
	h.mu.Unlock()
}

// drainHeld applies the backlog a fence left, and what the reader adds to it
// meanwhile, then lets the reader apply again.
func (s *Session) drainHeld(done chan<- struct{}) {
	defer close(done)
	for {
		s.applyHeld(nil)
		if s.release(true) {
			return
		}
	}
}

// waitDrained waits for the goroutine endFence may have started.
func (s *Session) waitDrained() {
	h := &s.hold
	h.mu.Lock()
	done := h.drainDone
	h.mu.Unlock()
	if done != nil {
		<-done
	}
}

// readerGone reports whether the output loop has returned.
func (s *Session) readerGone() bool {
	select {
	case <-s.readDone:
		return true
	default:
		return false
	}
}

// fence is the Fenced of one WithFence call.
type fence struct {
	s      *Session
	w      *lockedWriter
	valid  atomic.Bool
	frozen atomic.Bool
}

// WithFence takes the input lock for the whole of fn, as WithInputLock does,
// and gives fn the fence's two calls (see Fenced). Writer input (Input, Paste)
// that arrives meanwhile waits for the lock, in the order it arrived, and is
// delivered after fn returns; the keys count for LastWriterInput from when they
// were sent. The emulator's replies wait in their queue, bounded as ever, and
// only Settle writes them while fn runs. While fn runs the reader holds what it
// reads (see the fence's comment), and the end of the fence applies it.
//
// If ctx ends before the lock is taken, or the session is over, it returns an
// error without running fn: for ctx's end the error wraps ctx.Err(); for the
// session's end it is ErrClosed. fn runs at most once. fn must not call Input,
// Paste, WithInputLock or WithFence (the lock is not reentrant), nor, while it
// is inside Frozen, anything that takes the output lock.
func (s *Session) WithFence(ctx context.Context, fn func(Fenced) error) error {
	if fn == nil {
		return errors.New("session: WithFence: no function")
	}
	if err := s.imu.lockCtx(ctx); err != nil {
		return fmt.Errorf("session: the input lock was not taken: %w", err)
	}
	defer s.imu.Unlock()
	if s.isOver() {
		return ErrClosed
	}
	f := &fence{s: s, w: &lockedWriter{s: s, valid: true}}
	f.valid.Store(true)
	s.beginFence()
	defer func() {
		f.valid.Store(false)
		f.w.valid = false
		s.endFence()
	}()
	return fn(f)
}

func (f *fence) check(what string) error {
	if !f.valid.Load() {
		return fmt.Errorf("session: %s is used outside its fence", what)
	}
	return nil
}

// flush writes the replies the applied output made: first it waits for the
// pane's drain goroutine to hand every one of them to the queue (a reply can
// reach the queue just after Write returned), then it writes the queue.
func (f *fence) flush() error {
	f.s.pane.SyncReplies()
	_, err := f.w.FlushReplies()
	return err
}

func (f *fence) Settle(ctx context.Context, minWait, quiet, maxWait time.Duration) (bool, error) {
	if err := f.check("Settle"); err != nil {
		return false, err
	}
	if f.frozen.Load() {
		return false, errors.New("session: Settle is called inside Frozen")
	}
	s := f.s
	minWait, quiet, maxWait = max(minWait, 0), max(quiet, 0), max(maxWait, 0)
	minWait = min(minWait, maxWait)
	start := time.Now()
	startN := start.UnixNano()
	minEnd, maxEnd := start.Add(minWait), start.Add(maxWait)
	// The maximum is checked from the second slice of a call on, so that a
	// call applies at least one slice of what is held, however short it is.
	stop := func(slices int) bool {
		return ctx.Err() != nil || (slices > 0 && !time.Now().Before(maxEnd))
	}
	for {
		if err := ctx.Err(); err != nil {
			return false, fmt.Errorf("session: settle: %w", err)
		}
		applied, _ := s.applyHeld(stop)
		if applied {
			if err := f.flush(); err != nil {
				return false, fmt.Errorf("session: settle: %w", err)
			}
		}
		if err := ctx.Err(); err != nil {
			return false, fmt.Errorf("session: settle: %w", err)
		}
		now := time.Now()
		if s.readerGone() {
			if err := f.flush(); err != nil {
				return false, fmt.Errorf("session: settle: %w", err)
			}
			return false, fmt.Errorf("session: settle: %w", ErrStreamEnded)
		}
		recv := s.lastRecv.Load()
		since := max(recv, startN)
		quietEnd := time.Unix(0, since).Add(quiet)
		queued, qerr := s.host.Queued()
		calm := s.heldEmpty() && s.unapplied.Load() == 0 && (qerr != nil || queued == 0)
		switch {
		case !now.Before(maxEnd):
			drained := calm && (recv <= startN || !now.Before(quietEnd))
			if err := f.flush(); err != nil {
				return false, fmt.Errorf("session: settle: %w", err)
			}
			return drained, nil
		case calm && !now.Before(quietEnd) && !now.Before(minEnd):
			// Once more at the end of the quiet: the replies of anything
			// applied since, and the state of the input.
			if err := f.flush(); err != nil {
				return false, fmt.Errorf("session: settle: %w", err)
			}
			return true, nil
		}
		wake := maxEnd
		if calm {
			wake = minTime(maxEnd, maxTime(quietEnd, minEnd))
		} else {
			// Bytes are on their way to the buffer: look again soon.
			wake = minTime(wake, now.Add(5*time.Millisecond))
		}
		t := time.NewTimer(max(time.Until(wake), 0))
		select {
		case <-s.hold.arrive:
		case <-t.C:
		case <-ctx.Done():
		case <-s.readDone:
		}
		t.Stop()
	}
}

func minTime(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

func maxTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

func (f *fence) Frozen(deadline time.Time, fn func(v FenceView, write func([]byte) error) error) error {
	if err := f.check("Frozen"); err != nil {
		return err
	}
	if fn == nil {
		return errors.New("session: Frozen: no function")
	}
	if !f.frozen.CompareAndSwap(false, true) {
		return errors.New("session: Frozen is called inside Frozen")
	}
	defer f.frozen.Store(false)
	s := f.s
	// The output lock keeps Resize, Screen and the subscriber out for the
	// whole of fn. The reader does not take it: the fence is active, so the
	// reader only appends to the buffer.
	s.omu.Lock()
	defer s.omu.Unlock()
	fw := &frozenWrite{s: s, deadline: deadline}
	fw.valid.Store(true)
	defer fw.valid.Store(false)
	return fn(frozenView{s}, fw.write)
}

// frozenWrite is the write function of one Frozen call.
type frozenWrite struct {
	s        *Session
	deadline time.Time
	valid    atomic.Bool
}

// write is one write to the agent, bounded by the deadline of the Frozen call.
func (w *frozenWrite) write(p []byte) error {
	if !w.valid.Load() {
		return errors.New("session: the agent writer is used outside its Frozen function")
	}
	if len(p) == 0 {
		return nil
	}
	s := w.s
	if s.isOver() {
		return ErrClosed
	}
	if s.inputBroken.Load() {
		s.cnt.inputRefused.Add(1)
		return ErrInputBroken
	}
	if !time.Now().Before(w.deadline) {
		// The bound passed before the write began: nothing was written, so
		// the input is not broken by it.
		return fmt.Errorf("session: write: the bound passed before any byte was written: %w", os.ErrDeadlineExceeded)
	}
	n, err := s.host.WriteBy(p, w.deadline)
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	if err != nil {
		s.cnt.inputWriteErrors.Add(1)
		s.breakInput("section", err)
	}
	return err
}

// frozenView is the FenceView of a Frozen call.
type frozenView struct{ s *Session }

func (v frozenView) Snapshot() sessionpane.TextSnapshot { return v.s.pane.TextSnapshot() }

func (v frozenView) Term() TermState {
	modes := v.s.pane.ModeSet()
	return TermState{
		ParserGround:   v.s.pane.Quiescent(),
		FrameOpen:      modes.Has(2026, false),
		BracketedPaste: modes.Has(2004, false),
	}
}

func (v frozenView) OwnerQuiet() time.Duration {
	last := v.s.LastWriterInput()
	if last.IsZero() {
		return OwnerNeverTyped
	}
	return max(time.Since(last), 0)
}

func (v frozenView) ScreenQuiet() time.Duration { return v.s.screenQuiet() }

func (v frozenView) Ended() bool {
	s := v.s
	s.fmu.Lock()
	defer s.fmu.Unlock()
	return s.facts.Status != nil || s.concl
}

// Pending asks the kernel first and the reader's count second: a byte that the
// reader takes between the two questions is then seen by the second.
func (v frozenView) Pending() bool {
	s := v.s
	if n, err := s.host.Queued(); err != nil || n > 0 {
		return true
	}
	return s.unapplied.Load() > 0
}

// screenTrack finds when applied output last changed the screen. Comparing the
// whole screen after each slice would cost the reader a hash of the screen (14
// ms on the largest screen) for every read, so the reader never compares: for
// each slice it applies it notes the time, under the output lock, and arms a
// timer if none is armed. The timer compares the digest at the end of the
// interval, on its own goroutine, so the change is found within
// ScreenCheckInterval and not at the next apply, which may be long after, and
// the digest is compared again whenever the answer is asked for.
type screenTrack struct {
	mu        sync.Mutex
	sig       [32]byte    // the digest at sigAt
	sigAt     time.Time   // when sig was taken
	lastApply time.Time   // when output was last applied
	changed   time.Time   // when the screen last changed, within the interval
	dirty     bool        // output was applied since sig
	timer     *time.Timer // armed: a comparison is due
}

func (t *screenTrack) init(now time.Time, sig [32]byte) {
	t.sig, t.sigAt, t.lastApply, t.changed = sig, now, now, now
}

// refreshScreen compares the digest, with scr.mu held. A screen that differs
// changed at the last output applied or earlier, and at the earliest at the
// comparison before, so taking the last output's time is never too old and is
// at most the interval too new (the timer sees to that).
func (s *Session) refreshScreen(now time.Time) {
	t := &s.scr
	if t.timer != nil {
		t.timer.Stop()
		t.timer = nil
	}
	t.dirty = false
	t.sigAt = now
	if d := s.pane.Digest(); d != t.sig {
		t.sig = d
		if t.lastApply.After(t.changed) {
			t.changed = t.lastApply
		}
	}
}

// noteApplied records that a slice was applied, with the output lock held (so
// that whoever takes it next sees the slice noted). It never compares the
// digest: it notes the time, marks the screen as maybe changed and arms the
// timer that will compare.
func (s *Session) noteApplied() {
	now := time.Now()
	t := &s.scr
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lastApply, t.dirty = now, true
	if t.timer == nil {
		t.timer = time.AfterFunc(max(ScreenCheckInterval-now.Sub(t.sigAt), 0), s.screenTick)
	}
}

// screenTick is the timer's comparison.
func (s *Session) screenTick() {
	t := &s.scr
	t.mu.Lock()
	defer t.mu.Unlock()
	t.timer = nil
	if t.dirty {
		s.refreshScreen(time.Now())
	}
}

// noteResized records that a resize changed the screen.
func (s *Session) noteResized() {
	now := time.Now()
	t := &s.scr
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lastApply, t.changed, t.dirty = now, now, true
}

func (s *Session) screenQuiet() time.Duration {
	t := &s.scr
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.dirty {
		s.refreshScreen(time.Now())
	}
	return max(time.Since(t.changed), 0)
}
