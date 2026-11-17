//go:build linux

package serve

import (
	"time"

	"ataniq/internal/leadholder/wire"
	"ataniq/internal/terminalconsole/sessionpane"
)

// timekeeper does what no chunk of output triggers: it takes the snapshots of
// clients that wait for a point when the agent is quiet, sends the checks that
// are due, and follows the lifetime facts while clients are attached.
func (s *Server) timekeeper() {
	defer s.wg.Done()
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	for {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(s.nextWake())
		select {
		case <-s.stop:
			return
		case <-s.kick:
		case <-timer.C:
		}
		s.tick()
	}
}

// nextWake is the time to the next thing the timekeeper must look at.
func (s *Server) nextWake() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.clients) == 0 || s.finishing {
		return time.Hour
	}
	now := s.now()
	d := s.cfg.PollInterval
	for c := range s.clients {
		if c.wantSnap {
			d = min(d, snapshotPoll)
		} else if c.streaming {
			d = min(d, max(c.nextCheck.Sub(now), time.Millisecond))
		}
	}
	return d
}

// tick follows the facts and takes the points that are due.
func (s *Server) tick() {
	now := s.now()
	s.mu.Lock()
	if s.closed || s.finishing || len(s.clients) == 0 {
		s.mu.Unlock()
		return
	}
	pollFacts := !now.Before(s.nextFacts)
	if pollFacts {
		s.nextFacts = now.Add(s.cfg.PollInterval)
	}
	s.mu.Unlock()
	if pollFacts {
		facts, status := wireFacts(s.sess.Facts()) // outside mu: it may read /proc
		s.mu.Lock()
		s.facts, s.exitStatus = facts, status
		s.mu.Unlock()
	}
	s.mu.Lock()
	due := false
	for c := range s.clients {
		if c.wantSnap || (c.streaming && !now.Before(c.nextCheck)) {
			due = true
		}
	}
	s.mu.Unlock()
	// A point between two chunks is as good as the end of the last one: the
	// output lock is held, so the emulator is at rest.
	s.sess.Screen(func(p *sessionpane.Pane, pos uint64) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.closed || s.finishing || len(s.clients) == 0 {
			return
		}
		if due {
			s.pointLocked(p, pos, s.now())
		} else {
			s.syncStateLocked(p)
		}
	})
}

// finishSession ends the session for the clients: each gets a State and then
// the Exit record, and is closed after it took them (or after FlushWait). The
// listeners are closed first, so that nobody new attaches to a finished
// session.
func (s *Server) finishSession() {
	s.attachLn.Close()
	s.controlLn.Close()
	s.unsub() // outside mu
	rec, _ := s.sess.Final()
	facts, status := wireFacts(rec.Facts)
	counters := s.allCounters()

	title := ""
	if s.pane != nil {
		title = wire.Sanitize(s.pane.Title(), wire.MaxTitle, false)
	}
	statePayload, _ := wire.EncodeState(wire.State{Facts: facts, ExitStatus: status, Idle: wire.IdleUnknown, Title: title})
	exit := wire.Exit{Facts: facts, ExitStatus: status, Counters: counters, LastScreen: wire.Sanitize(rec.Screen.Text, wire.MaxLastScreen, true)}
	exitPayload, err := wire.EncodeExit(exit)
	if err != nil {
		exit.LastScreen = ""
		exitPayload, _ = wire.EncodeExit(exit)
	}

	s.mu.Lock()
	s.finishing = true
	for c := range s.clients {
		c.wantSnap, c.streaming = false, false
		c.draining = true
		if c.reason == "" {
			c.reason = "session-over"
		}
		if statePayload != nil {
			c.q = append(c.q, item{typ: wire.TypeState, data: statePayload})
		}
		if exitPayload != nil {
			c.q = append(c.q, item{typ: wire.TypeExit, data: exitPayload})
		}
		c.poke()
		cc := c
		time.AfterFunc(s.cfg.FlushWait, func() { cc.conn.Close() })
	}
	empty := len(s.clients) == 0
	s.mu.Unlock()
	if empty {
		return
	}
	wait := time.NewTimer(s.cfg.FlushWait + time.Second)
	defer wait.Stop()
	for {
		select {
		case <-s.gone:
			s.mu.Lock()
			n := len(s.clients)
			s.mu.Unlock()
			if n == 0 {
				return
			}
		case <-wait.C:
			return
		case <-s.stop:
			return
		}
	}
}
