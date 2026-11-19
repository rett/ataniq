//go:build linux && ataniq_canary

package injectbridge

import (
	"context"
	"errors"
	"time"

	"ataniq/internal/inject"
	"ataniq/internal/inject/osc7501"
	"ataniq/internal/inject/screen"
	"ataniq/internal/leadholder/session"
	"ataniq/internal/terminalconsole/sessionpane"
)

// ErrNoSession is returned by a Bridge that was made without a session. The
// library reads it as a session error before anything was written.
var ErrNoSession = errors.New("injectbridge: no session")

// Config is what a Bridge knows that the session does not.
type Config struct {
	// Runtime and Version are the agent's runtime and its version as the
	// holder was told them. The library refuses a pair that is not in its
	// tested table, before it touches the session.
	Runtime, Version string
	// Tracker is the status tracker that the hook made by Hook feeds. It must
	// be the tracker given to Hook, and that hook must be in the session's
	// configuration, or no report ever arrives. With a nil Tracker the view
	// reports no status.
	Tracker *osc7501.Tracker
	// Now is the clock a Deliverer made from the Bridge reads its deadline
	// on. Nil means time.Now. The session measures its own waits and write
	// bounds on the system clock, so anything but a clock that follows it is
	// for tests of what happens before the session is touched.
	Now func() time.Time
}

// Bridge is one session as the injection library sees it. It satisfies
// inject.Session. Its methods are safe for concurrent use, as the session's
// are; the lock is the session's input lock, which is not reentrant.
type Bridge struct {
	s   *session.Session
	cfg Config
}

// The three interfaces of the library that this package implements.
var (
	_ inject.Session = (*Bridge)(nil)
	_ inject.Locked  = locked{}
	_ inject.View    = view{}
)

// New returns the bridge of s. The hook made by Hook, with cfg.Tracker, must
// have been in the configuration s was started with: the bridge cannot install
// it afterwards.
func New(s *session.Session, cfg Config) *Bridge {
	return &Bridge{s: s, cfg: cfg}
}

// Runtime returns the configured runtime and version.
func (b *Bridge) Runtime() (runtime, version string) {
	if b == nil {
		return "", ""
	}
	return b.cfg.Runtime, b.cfg.Version
}

// status is the tracker's status, or no status without a tracker.
func (b *Bridge) status() osc7501.Status {
	if b.cfg.Tracker == nil {
		return osc7501.Status{}
	}
	return b.cfg.Tracker.Status()
}

// WithInputLock takes the session's input lock for the whole of fn, through
// session.WithFence: writer input is held and delivered after fn, in order.
// When ctx ends before the lock is taken it returns an error that wraps
// ctx.Err() and does not run fn; when the session is over it returns
// session.ErrClosed and does not run fn. Otherwise it returns fn's error. fn
// runs at most once, and the Locked it is given is valid only inside it.
func (b *Bridge) WithInputLock(ctx context.Context, fn func(inject.Locked) error) error {
	if b == nil || b.s == nil {
		return ErrNoSession
	}
	if fn == nil {
		return errors.New("injectbridge: WithInputLock: no function")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return b.s.WithFence(ctx, func(f session.Fenced) error {
		return fn(locked{b: b, f: f})
	})
}

// locked is the library's Locked over the session's Fenced.
type locked struct {
	b *Bridge
	f session.Fenced
}

// Settle is session.Fenced.Settle: it applies output on the caller's side and
// writes the replies that output caused, the hook's among them, in the order
// the emulator made them, before it returns. The error is the session's: it
// wraps ctx.Err() when ctx ended, and session.ErrInputBroken, session.ErrClosed
// or session.ErrStreamEnded when the session cannot go on.
func (l locked) Settle(ctx context.Context, min, quiet, max time.Duration) (bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	return l.f.Settle(ctx, min, quiet, max)
}

// Frozen is session.Fenced.Frozen: fn runs at most once, on the calling
// goroutine, with nothing applied to the emulator while it runs, and write is
// the session's one bounded write, never retried or completed. It returns fn's
// error, or the session's when fn could not be run.
func (l locked) Frozen(deadline time.Time, fn func(v inject.View, write func([]byte) error) error) error {
	if fn == nil {
		return errors.New("injectbridge: Frozen: no function")
	}
	return l.f.Frozen(deadline, func(v session.FenceView, write func([]byte) error) error {
		// The hook runs only where output is applied, and nothing is applied
		// while this function runs, so the status read here is the status for
		// the whole call: every Report of this view answers the same.
		return fn(view{v: v, report: l.b.status()}, write)
	})
}

// view is the library's View over the session's FenceView of one Frozen call.
type view struct {
	v      session.FenceView
	report osc7501.Status
}

func (v view) Snapshot() screen.Snapshot { return snapshotOf(v.v.Snapshot()) }

// Report is the tracker's status as it was when the Frozen call began: stale
// when the tracker says so, and without a report (Has false) when the bridge
// has no tracker or no hook feeds it.
func (v view) Report() osc7501.Status { return v.report }

func (v view) Term() inject.TermState {
	t := v.v.Term()
	return inject.TermState{
		ParserGround:   t.ParserGround,
		FrameOpen:      t.FrameOpen,
		BracketedPaste: t.BracketedPaste,
	}
}

func (v view) OwnerQuiet() time.Duration  { return v.v.OwnerQuiet() }
func (v view) ScreenQuiet() time.Duration { return v.v.ScreenQuiet() }
func (v view) Ended() bool                { return v.v.Ended() }

// Pending asks the session at each call; it is never remembered.
func (v view) Pending() bool { return v.v.Pending() }

// snapshotOf converts the pane's text snapshot to the library's, field by
// field. The faint spans are offsets in the text of their row in both, so
// they are copied as they are; no span gives a nil list.
func snapshotOf(ts sessionpane.TextSnapshot) screen.Snapshot {
	out := screen.Snapshot{
		Text:         ts.Text,
		Cols:         ts.Cols,
		Rows:         ts.Rows,
		CursorX:      ts.CursorX,
		CursorY:      ts.CursorY,
		CursorHidden: ts.CursorHidden,
		AltScreen:    ts.AltScreen,
		Title:        ts.Title,
	}
	if len(ts.Faint) > 0 {
		out.Faint = make([]screen.Span, len(ts.Faint))
		for i, f := range ts.Faint {
			out.Faint[i] = screen.Span{Row: f.Row, From: f.From, To: f.To}
		}
	}
	return out
}
