//go:build linux && ataniq_canary

package injectbridge

import (
	"time"

	"ataniq/internal/inject/osc7501"
	"ataniq/internal/leadholder/session"
)

// OSCCommand is the command number of the status protocol the hook handles.
const OSCCommand = 7501

// Hook returns the session's handler for OSC 7501. Put it in
// session.Config.OSC before the session starts, and give the same tracker and
// clock to New.
//
// The handler gives every payload, the support query included, to
// t.Observe(payload, now()), and answers the support query with the library's
// reply (osc7501.Reply). The session calls it where it applies the output, in
// the order the emulator meets the strings, and puts the reply into its reply
// queue at that point, so the reply is written before the reply to any request
// the agent sent after the query. It consumes every string of the command,
// readable or not: an unreadable one is counted and marks the status stale.
//
// The handler runs on the goroutine that applies output while the view's
// Report and Counts are read from others. The guard between them is the
// tracker's own lock (osc7501.Tracker is safe for concurrent use), and the
// handler touches nothing else; now must be safe to call from any goroutine.
// A nil now means time.Now. With a nil t the query is still answered and
// nothing is recorded.
func Hook(t *osc7501.Tracker, now func() time.Time) session.OSCHook {
	if now == nil {
		now = time.Now
	}
	return session.OSCHook{Cmd: OSCCommand, Fn: func(data []byte) ([]byte, bool) {
		if t != nil {
			t.Observe(data, now())
		}
		reply, _ := osc7501.Reply(data)
		return reply, true
	}}
}

// Totals are the totals of a tracker: the support queries, the readable
// reports and the unreadable payloads it was shown. They only grow.
type Totals struct {
	Queries, Reports, Invalid uint64
}

// Counts returns the totals of t so far, read at once under the tracker's
// lock, the guard the hook writes them under. The holder copies the increase
// of each since its last call into the session's counters. A nil t has none.
func Counts(t *osc7501.Tracker) Totals {
	if t == nil {
		return Totals{}
	}
	c := t.Counts()
	return Totals{Queries: count(c.Queries), Reports: count(c.Reports), Invalid: count(c.Invalid)}
}

func count(n int) uint64 {
	if n < 0 {
		return 0
	}
	return uint64(n)
}
