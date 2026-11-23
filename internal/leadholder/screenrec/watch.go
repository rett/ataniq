//go:build linux && ataniq_canary

package screenrec

import (
	"time"

	"ataniq/internal/leadholder/injectbridge"
	"ataniq/internal/terminalconsole/sessionpane"
)

// Pane is what Watch reads of the holder's pane; *sessionpane.Pane has it.
// Quiescent and ModeSet are what the fence reads of the parser
// (session.TermState, from the session's frozenView.Term): whether it is at
// ground, and whether a synchronized-output frame (mode 2026) is open.
type Pane interface {
	Size() (cols, rows int)
	Title() string
	TextSnapshot() sessionpane.TextSnapshot
	Quiescent() bool
	ModeSet() sessionpane.ModeSet
}

// settled says the pane shows a whole frame: the checks of the fence, the
// parser at ground and no mode 2026 frame open.
func settled(p Pane) bool {
	return p.Quiescent() && !p.ModeSet().Has(2026, false)
}

// ScreenFunc runs fn with the pane and the count of output bytes applied to
// it, under the session's output lock, as session.Session.Screen does. fn
// returns promptly and never calls the session.
type ScreenFunc func(fn func(p Pane, pos uint64))

// Watch looks at the pane every PollInterval until done is closed or the
// recording stops, and closes the recorder then, which waits for the writer.
// When done is closed it takes one last look first, with neither Settle nor
// PairInterval to wait for: the pane keeps its last screen after the session
// ended, and that screen is recorded when it differs from the last one. Each
// look reads the position and the size; when either changed since the last
// look, it reads the title, which Title records when it changed, and marks the
// screen as changed at that time. A changed screen is snapshotted once it has
// been unchanged for Settle and PairInterval has passed since the last
// snapshot, while MaxPairs is not reached, through the conversion of the fence
// (injectbridge.SnapshotOf), and Screen records it when it differs from the
// last one. A screen that keeps changing and never stays unchanged for Settle
// (the working screen) is sampled instead: once it has changed for
// SampleEvery with no pause of Settle, and again each SampleEvery after the
// last sample while it goes on, a snapshot is due as well, recorded the same
// way and marked sampled; a pause of Settle ends the stretch, and the settle
// rule takes the screen. A snapshot that is due, a sample too, waits, the
// screen kept marked as changed, while the parser is in the middle of a
// sequence or a mode 2026 frame is open, as the fence does; one that has
// waited TornAfter, and one the last look takes before they settled, is taken
// anyway and marked torn. The snapshot is copied under the lock and written by
// the writer, off it.
func (r *Recorder) Watch(screenFn ScreenFunc, done <-chan struct{}) {
	defer r.Close()
	t := time.NewTicker(r.cfg.PollInterval)
	defer t.Stop()
	var w watcher
	for {
		select {
		case <-done:
			if !r.Stopped() {
				w.look(r, screenFn, true)
			}
			return
		case <-t.C:
			if r.Stopped() {
				return
			}
			w.look(r, screenFn, false)
		}
	}
}

// watcher is what Watch remembers between two looks.
type watcher struct {
	seen       bool
	pos        uint64
	cols, rows int
	dirty      bool      // the screen changed since the last snapshot
	changedAt  time.Time // when a look last saw it change
	snapAt     time.Time // when the last snapshot was taken
	waitingAt  time.Time // when a due snapshot began to wait for the parser
	// busySince is when the screen began to change with no pause of Settle,
	// or when the last sample of that stretch was taken; zero once a look
	// sees it unchanged for Settle.
	busySince time.Time
}

// look is one look at the pane. The last look (final) takes a changed screen
// whatever the time since the last change and the last snapshot.
func (w *watcher) look(r *Recorder, screenFn ScreenFunc, final bool) {
	now := r.cfg.Now()
	pairs := r.takesPairs()
	var (
		changed bool
		title   string
		snap    *sessionpane.TextSnapshot
		torn    bool
		sampled bool
	)
	screenFn(func(p Pane, pos uint64) {
		cols, rows := p.Size()
		if !w.seen || pos != w.pos || cols != w.cols || rows != w.rows {
			w.seen, w.pos, w.cols, w.rows = true, pos, cols, rows
			changed, w.dirty, w.changedAt = true, true, now
			title = p.Title()
			if w.busySince.IsZero() {
				w.busySince = now
			}
		}
		quiet := now.Sub(w.changedAt) >= r.cfg.Settle
		if quiet {
			w.busySince = time.Time{}
		}
		interval := w.snapAt.IsZero() || now.Sub(w.snapAt) >= r.cfg.PairInterval
		settleDue := quiet && interval
		sampleDue := !quiet && !w.busySince.IsZero() && now.Sub(w.busySince) >= r.cfg.SampleEvery && interval
		due := final || settleDue || sampleDue
		if !pairs || !w.dirty || !due {
			w.waitingAt = time.Time{}
			return
		}
		if !settled(p) {
			if w.waitingAt.IsZero() {
				w.waitingAt = now
			}
			if !final && now.Sub(w.waitingAt) < TornAfter {
				return
			}
			torn = true
		}
		w.waitingAt = time.Time{}
		ts := p.TextSnapshot()
		snap = &ts
		if !final && !settleDue {
			// A sample: the next one is due SampleEvery from now if the
			// screen goes on changing.
			sampled, w.busySince = true, now
		}
	})
	if changed {
		r.Title(title)
	}
	if snap != nil {
		w.dirty, w.snapAt = false, now
		r.screen(injectbridge.SnapshotOf(*snap), torn, sampled)
	}
}
