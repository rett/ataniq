package ownerchannel

import (
	"context"
	"time"

	"ataniq/internal/approval/presence"
	"ataniq/internal/approval/record"
)

// tracker returns the presence tracker of a surface and session, making it
// when there is none. At most maxTrackers are kept; the oldest goes first.
func (s *Service) tracker(src Source) *presence.Tracker {
	key := src.Surface + "/" + src.Session
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.trackers[key]; ok {
		return t
	}
	t, err := presence.New(s.cfg.PresenceWindow, src.Surface, src.Session)
	if err != nil {
		// The surface and the session were checked by every caller, and
		// the window by New.
		panic("ownerchannel: " + err.Error())
	}
	if len(s.order) >= maxTrackers {
		delete(s.trackers, s.order[0])
		s.order = s.order[1:]
	}
	s.trackers[key] = t
	s.order = append(s.order, key)
	return t
}

// Beat says the owner was active at a surface (owner.beat). Only the
// connection that may decide is heard: another console's beat makes no one
// present.
func (s *Service) Beat(src Source) error {
	_, done, err := s.begin(context.Background())
	if err != nil {
		return err
	}
	defer done()
	if !presence.ValidSurface(src.Surface) || !presence.ValidSession(src.Session) {
		return refuse(record.CodeRecordInvalid, "the surface or the session of the beat is not well formed")
	}
	if !src.Decider {
		return nil
	}
	now := s.now()
	t := s.tracker(src)
	s.tmu.Lock()
	defer s.tmu.Unlock()
	t.Activity(now)
	return nil
}

// mayDecide asks the tracker of a surface and session whether the owner is
// present at now. With count set, the action that carries the request is
// first taken as activity when he was present, so that the journal reads
// Arrived and then the decision, never the decision and then Arrived. It
// cannot make an absent owner present. The stamp it returns holds the last
// activity before that action.
func (s *Service) mayDecide(src Source, now time.Time, count bool) (presence.Stamp, error) {
	t := s.tracker(src)
	s.tmu.Lock()
	defer s.tmu.Unlock()
	before, _ := t.LastActivity()
	if count && t.Present(now) {
		t.Activity(now)
	}
	st, err := t.MayDecide(now)
	if err == nil {
		// The journal keeps the last activity before the decision, not the
		// action that carried it.
		st.LastActivity = before
	}
	return st, err
}
