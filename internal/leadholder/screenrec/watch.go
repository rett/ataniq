//go:build linux && ataniq_canary

package screenrec

import (
	"time"

	"ataniq/internal/leadholder/injectbridge"
	"ataniq/internal/terminalconsole/sessionpane"
)

// Pane is what Watch reads of the holder's pane; *sessionpane.Pane has it.
type Pane interface {
	Size() (cols, rows int)
	Title() string
	TextSnapshot() sessionpane.TextSnapshot
}

// ScreenFunc runs fn with the pane and the count of output bytes applied to
// it, under the session's output lock, as session.Session.Screen does. fn
// returns promptly and never calls the session.
type ScreenFunc func(fn func(p Pane, pos uint64))

// Watch looks at the pane every PollInterval until done is closed or the
// recording stops, and closes the recorder then. Each look reads the position
// and the size; when either changed since the last look, it reads the title,
// which Title records when it changed, and marks the screen as changed. A
// changed screen is snapshotted when PairInterval has passed since the last
// snapshot, through the conversion of the fence (injectbridge.SnapshotOf), and
// Screen records it when it differs from the last one. The snapshot is copied
// under the lock and written by the writer, off it.
func (r *Recorder) Watch(screenFn ScreenFunc, done <-chan struct{}) {
	defer r.Close()
	t := time.NewTicker(r.cfg.PollInterval)
	defer t.Stop()
	var w watcher
	for {
		select {
		case <-done:
			return
		case <-t.C:
			if r.Stopped() {
				return
			}
			w.look(r, screenFn)
		}
	}
}

// watcher is what Watch remembers between two looks.
type watcher struct {
	seen       bool
	pos        uint64
	cols, rows int
	dirty      bool      // the screen changed since the last snapshot
	snapAt     time.Time // when the last snapshot was taken
}

func (w *watcher) look(r *Recorder, screenFn ScreenFunc) {
	now := r.cfg.Now()
	var (
		changed bool
		title   string
		snap    *sessionpane.TextSnapshot
	)
	screenFn(func(p Pane, pos uint64) {
		cols, rows := p.Size()
		if !w.seen || pos != w.pos || cols != w.cols || rows != w.rows {
			w.seen, w.pos, w.cols, w.rows = true, pos, cols, rows
			changed, w.dirty = true, true
			title = p.Title()
		}
		if w.dirty && (w.snapAt.IsZero() || now.Sub(w.snapAt) >= r.cfg.PairInterval) {
			ts := p.TextSnapshot()
			snap = &ts
		}
	})
	if changed {
		r.Title(title)
	}
	if snap != nil {
		w.dirty, w.snapAt = false, now
		r.Screen(injectbridge.SnapshotOf(*snap))
	}
}
