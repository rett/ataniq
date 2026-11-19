package ownerchannel

import (
	"context"
	"errors"
	"time"

	"ataniq/internal/approval/limits"
	"ataniq/internal/approval/ownerjournal"
	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/approval/presence"
	"ataniq/internal/approval/record"
)

// limitView builds the view of a project's limit: the limit in force, the
// 14 days before today with their uses and marks, the suggestion and
// whether the limit was raised today. Each day is marked as having reached
// its limit when the journal has the alert for it, or when its uses are at
// the highest limit that was in force that day.
func (s *Service) limitView(project string, now time.Time) (limits.View, error) {
	st := s.journal()
	limit, err := s.limitOf(project)
	if err != nil {
		return limits.View{}, err
	}
	l, err := st.Limit(project)
	if err != nil {
		return limits.View{}, err
	}
	days, err := st.Days(project, now)
	if err != nil {
		return limits.View{}, err
	}
	changes, err := st.LimitChanges(project)
	if err != nil {
		return limits.View{}, err
	}
	loc := now.Location()
	// limitOn is the highest limit that was in force at any moment of a
	// day: the limit at its start (the last change made at or before it, else
	// the limit the first change started from, else the default) and every
	// limit a change set during the day. A day whose uses are under that did
	// not reach its limit, however the limit moved; a day that did reach it
	// has its alert besides.
	limitOn := func(date string) int {
		start, err := time.ParseInLocation("2006-01-02", date, loc)
		if err != nil {
			return limits.Default
		}
		end := start.AddDate(0, 0, 1)
		in := limits.Default
		if len(changes) > 0 {
			in = changes[0].From
		}
		high := 0
		for _, c := range changes {
			switch {
			case !c.At.After(start):
				in = c.To
			case c.At.Before(end) && c.To > high:
				high = c.To
			}
		}
		if in > high {
			high = in
		}
		return high
	}
	out := make([]limits.Day, 0, len(days))
	for _, d := range days {
		out = append(out, limits.Day{
			Date: d.Date, Uses: d.Uses, Alert: d.Alert,
			ReachedLimit: d.ReachedLimit || (d.Uses > 0 && limits.Reached(d.Uses, limitOn(d.Date))),
		})
	}
	return limits.NewView(project, limit, out, l.LastRaise, now)
}

// Limit returns the daily card limit of a project and the days it is judged
// from (owner.limit).
func (s *Service) Limit(ctx context.Context, project string) (ownerwire.LimitView, error) {
	ctx, done, err := s.begin(ctx)
	if err != nil {
		return ownerwire.LimitView{}, err
	}
	defer done()
	v, err := s.limitView(project, s.now())
	if err != nil {
		return ownerwire.LimitView{}, s.jfix(ctx, err)
	}
	return wireView(v), nil
}

// wireView turns the limit's view into the connection's.
func wireView(v limits.View) ownerwire.LimitView {
	out := ownerwire.LimitView{
		Project: v.Project, Limit: v.Limit, Suggestion: v.Suggestion, RaisedToday: v.RaisedToday,
		Days: make([]ownerwire.LimitDay, 0, len(v.Days)),
	}
	for _, d := range v.Days {
		out.Days = append(out.Days, ownerwire.LimitDay{Date: d.Date, Uses: d.Uses, ReachedLimit: d.ReachedLimit, Alert: d.Alert})
	}
	return out
}

// LimitSet changes the daily card limit of a project (owner.limit-set). The
// change is checked with package limits before it is journaled: one key
// lowers, a raise is typed and by half at most, there is one raise a day.
// A refused change is a *record.Error with the code limit-refused and the
// reason of the limit's rule. Like a decision it is refused to a connection
// that is not the decider and to an owner who is away.
func (s *Service) LimitSet(ctx context.Context, src Source, p ownerwire.LimitSetParams) (ownerwire.LimitView, error) {
	ctx, done, err := s.begin(ctx)
	if err != nil {
		return ownerwire.LimitView{}, err
	}
	defer done()
	if !presence.ValidSurface(src.Surface) || !presence.ValidSession(src.Session) {
		return ownerwire.LimitView{}, refuse(record.CodeRecordInvalid, "the surface or the session is not well formed")
	}
	s.dmu.Lock()
	defer s.dmu.Unlock()
	now := s.now()
	if !src.Decider {
		return ownerwire.LimitView{}, refuse(record.CodeNotDecider, "this connection is not the one that may decide")
	}
	if _, err := s.mayDecide(src, now, true); err != nil {
		return ownerwire.LimitView{}, refuse(record.CodeOwnerAway, "no activity of the owner inside the presence window")
	}
	v, err := s.limitView(p.Project, now)
	if err != nil {
		return ownerwire.LimitView{}, s.jerr(ctx, err)
	}
	c := limits.Change{To: p.To, Typed: p.Typed}
	if err := limits.Check(v.Limit, v.RaisedToday, c); err != nil {
		var r *limits.Refusal
		if errors.As(err, &r) {
			return ownerwire.LimitView{}, refuse(record.CodeLimitRefused, r.Reason)
		}
		return ownerwire.LimitView{}, err
	}
	if c.To != v.Limit {
		err := s.journal().AppendLimit(ownerjournal.LimitChange{Project: p.Project, At: now, From: v.Limit, To: c.To, Typed: c.Typed})
		switch {
		case errors.Is(err, ownerjournal.ErrLimitMoved):
			return ownerwire.LimitView{}, refuse(record.CodeLimitRefused, "the limit changed meanwhile: read it again")
		case err != nil:
			return ownerwire.LimitView{}, s.jerr(ctx, err)
		}
	}
	v, err = s.limitView(p.Project, now)
	if err != nil {
		return ownerwire.LimitView{}, s.jerr(ctx, err)
	}
	return wireView(v), nil
}
