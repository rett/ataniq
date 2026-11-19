package demo

import (
	"errors"
	"sort"
	"sync"
	"time"
	"unicode/utf8"

	"ataniq/internal/approval/card"
	"ataniq/internal/approval/limits"
	"ataniq/internal/approval/presence"
	"ataniq/internal/approval/record"
)

// sampleSession is the session id of the sample backend's presence tracker:
// 16 lower-case hexadecimal digits, the same in every run.
const sampleSession = "5a4d504c45000001"

// Lines of the outcomes. Each says "sample" and that nothing was changed.
const (
	lineApproved = "sample: approved; nothing was changed"
	lineDenied   = "sample: denied; nothing was changed"
	lineHeld     = "sample: held past the daily limit; nothing was changed"
	lineExpired  = "sample: the request ended while it was open; nothing was changed"
)

// entry is one sample card as the backend holds it.
type entry struct {
	sample
	state     record.State
	line      string
	held      bool
	opened    time.Time // the first opening, zero until then
	decidedAt time.Time
	pathPages [][]record.PathChange
	patch     []patchPage
}

// project is the limit state of one sample project.
type project struct {
	limit int
	// history is the limit by day, oldest first: each entry holds the limit
	// from its date on, and the first has the date "" (from the start). A
	// day is judged against the limit that was in force on it.
	history   []limitFrom
	lastRaise time.Time
	// uses are the times of the approves that counted.
	uses []time.Time
}

// limitFrom is a limit in force from a day on, as a date from limits.Today.
type limitFrom struct {
	date  string
	limit int
}

// limitOn returns the limit in force on a day.
func (p *project) limitOn(date string) int {
	limit := p.limit
	for _, h := range p.history {
		if h.date > date {
			break
		}
		limit = h.limit
	}
	return limit
}

// Backend is the sample backend. Make one with [New]. It is safe for use
// from several goroutines.
type Backend struct {
	now func() time.Time

	mu       sync.Mutex
	tracker  *presence.Tracker
	cards    []*entry
	projects map[string]*project
	lookedAt time.Time
	rules    []card.Rule
	count    card.Count
}

// New returns a backend holding the sample cards, made with the times of the
// clock now. The clock is read on every call that needs the time.
func New(now func() time.Time) *Backend {
	t0 := now()
	tracker, err := presence.New(presence.DefaultWindow, presence.SurfaceConsole, sampleSession)
	if err != nil {
		panic("demo: " + err.Error())
	}
	b := &Backend{now: now, tracker: tracker, projects: map[string]*project{}}
	for _, s := range samples(t0) {
		b.cards = append(b.cards, &entry{
			sample:    s,
			state:     s.state,
			line:      s.line,
			held:      s.held,
			pathPages: splitPaths(s.paths),
			patch:     splitPatch(s.patch),
		})
		if in, ok := card.BindingCountInput(s.pending.Binding); ok {
			b.count.Add(limits.Count(in))
		}
	}
	b.seedProjects(t0)
	b.rules = []card.Rule{
		{Project: projectA, Name: "sample-first", Text: "sample rule: a change that meets the First template would need no card; this one is off", On: false},
		{Project: projectB, Name: "sample-quiet", Text: "sample rule: shown only to fill the list; it approves nothing", On: false},
	}
	return b
}

// seedProjects gives each sample project its limit and its past uses. Project
// c has used its whole limit today, which is what holds sample 8.
func (b *Backend) seedProjects(now time.Time) {
	day := func(back int) time.Time { return now.AddDate(0, 0, -back) }
	a := &project{limit: limits.Default, history: []limitFrom{{"", limits.Default}}}
	a.uses = append(a.uses, day(1), day(1), day(1), day(2), day(2), day(2), day(2), day(2), day(3), day(3))
	bp := &project{limit: 6, history: []limitFrom{{"", 6}}}
	bp.uses = append(bp.uses, day(1), day(4), day(4))
	c := &project{limit: 2, history: []limitFrom{{"", 2}}}
	c.uses = append(c.uses, day(1), day(1), now, now)
	b.projects[projectA], b.projects[projectB], b.projects[projectC] = a, bp, c
}

// settle ends the samples that end while open, when their time is up. The
// caller holds the lock.
func (b *Backend) settle(now time.Time) {
	for _, e := range b.cards {
		if e.endsAfter > 0 && !e.opened.IsZero() && e.isPending() && !now.Before(e.opened.Add(e.endsAfter)) {
			e.state, e.line, e.decidedAt = record.StateExpired, lineExpired, now
		}
	}
}

// isPending reports whether the sample still waits for a decision.
func (e *entry) isPending() bool { return e.state == "" || e.state == record.StatePending }

// find returns the sample of an id. The caller holds the lock.
func (b *Backend) find(id string) (*entry, error) {
	for _, e := range b.cards {
		if e.sample.pending.ID == id {
			return e, nil
		}
	}
	return nil, errors.New("demo: no such sample card")
}

// cardOf builds the card of an entry as a real one is built.
func (b *Backend) cardOf(e *entry) (card.Card, error) {
	in := card.Inputs{Held: e.held, State: e.state, Line: e.line, Verdicts: e.verdicts}
	if e.earlier != "" {
		if prev, err := b.find(e.earlier); err == nil && !prev.opened.IsZero() {
			in.Earlier = &card.Earlier{Record: prev.sample.pending, ReadAt: prev.opened}
		}
	}
	return card.New(e.sample.pending, in)
}

// List returns the pending cards, the recent decisions, the sample rules and
// the count from the first card. The listing says it holds samples.
func (b *Backend) List() (card.Listing, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.settle(b.now())
	l := card.Listing{Sample: true, LookedAt: b.lookedAt, Rules: append([]card.Rule(nil), b.rules...), Count: b.count}
	var recent []*entry
	for _, e := range b.cards {
		if e.isPending() {
			c, err := b.cardOf(e)
			if err != nil {
				return card.Listing{}, err
			}
			l.Pending = append(l.Pending, c.Item())
		} else {
			recent = append(recent, e)
		}
	}
	sort.SliceStable(recent, func(i, j int) bool { return recent[i].decidedAt.After(recent[j].decidedAt) })
	for _, e := range recent {
		c, err := b.cardOf(e)
		if err != nil {
			return card.Listing{}, err
		}
		l.Recent = append(l.Recent, c.Item())
	}
	return l, nil
}

// Open returns the card of an id and notes when it was first opened: the
// "differs from" block of a later card and the end of a card that ends while
// open both start from that time.
func (b *Backend) Open(id string) (card.Card, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	e, err := b.find(id)
	if err != nil {
		return card.Card{}, err
	}
	if e.opened.IsZero() {
		e.opened = now
	}
	b.settle(now)
	return b.cardOf(e)
}

// Page returns page n of a card's whole path list or of its full patch.
func (b *Backend) Page(id string, kind card.PageKind, n int) (card.Page, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	e, err := b.find(id)
	if err != nil {
		return card.Page{}, err
	}
	p := card.Page{Kind: kind, N: n, ReviewDigest: e.reviewDigest}
	switch kind {
	case card.PagePaths:
		p.Pages = len(e.pathPages)
		if n < 0 || n >= p.Pages {
			return card.Page{}, errors.New("demo: no such page")
		}
		p.Paths = append([]record.PathChange(nil), e.pathPages[n]...)
	case card.PagePatch:
		p.Pages = len(e.patch)
		if n < 0 || n >= p.Pages {
			return card.Page{}, errors.New("demo: no such page")
		}
		p.Patch = append([]string(nil), e.patch[n].lines...)
		p.Cont = append([]bool(nil), e.patch[n].cont...)
	default:
		return card.Page{}, errors.New("demo: no such kind of page")
	}
	return p, nil
}

// refuse makes a refusal with a code and a line of the host's words.
func refuse(code record.Code, detail string) error {
	return &record.Error{Code: code, Detail: detail}
}

// Decide takes a decision on a sample in memory. It refuses, in this order:
// a decision with no recent activity (owner-away), an unknown card, a card
// that ended, a binding digest that is not the card's, a reason a decision
// cannot carry, and sign-offs that are not the card's flagged list. An
// approve past the project's limit is held: the card stays pending and comes
// back marked. A deny is never held. Nothing outside the process changes.
func (b *Backend) Decide(d card.Decision) (card.Outcome, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	if _, err := b.tracker.MayDecide(now); err != nil {
		var away *presence.Away
		if errors.As(err, &away) {
			return card.Outcome{}, refuse(record.CodeOwnerAway, "no activity within the window")
		}
		return card.Outcome{}, err
	}
	b.settle(now)
	e, err := b.find(d.ID)
	if err != nil {
		return card.Outcome{}, err
	}
	pending := e.sample.pending
	switch e.state {
	case "", record.StatePending:
	case record.StateExpired:
		return card.Outcome{}, refuse(record.CodeApprovalExpired, "the request ended")
	case record.StateWithdrawn, record.StateVoid:
		return card.Outcome{}, refuse(record.CodeApprovalVoid, "the request was withdrawn")
	default:
		return card.Outcome{}, refuse(record.CodeApprovalConsumed, "the request was decided already")
	}
	if d.BindingSHA256 != pending.BindingSHA256 {
		return card.Outcome{}, refuse(record.CodeBindingMismatch, "the digest is not the card's")
	}
	if len(d.Reason) > record.MaxReasonBytes || !utf8.ValidString(d.Reason) {
		return card.Outcome{}, refuse(record.CodeDecisionInvalid, "the reason cannot be carried")
	}
	for _, r := range d.Reason {
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
			return card.Outcome{}, refuse(record.CodeDecisionInvalid, "the reason holds a control character")
		}
	}
	if d.Approve && d.Reason != "" {
		return card.Outcome{}, refuse(record.CodeDecisionInvalid, "an approve has no reason")
	}
	if d.Approve && !record.SignOffsEqual(pending.Binding.Summary.Flagged, d.SignedOff) {
		return card.Outcome{}, refuse(record.CodeDecisionInvalid, "the sign-offs are not the flagged list")
	}
	if !d.Approve {
		e.state, e.line, e.decidedAt, e.held = record.StateDenied, lineDenied, now, false
		return card.Outcome{State: e.state, Line: e.line}, nil
	}
	pr := b.projects[pending.Binding.Project]
	switch limits.Judge(true, limits.UsesToday(pr.uses, now), pr.limit) {
	case limits.Held:
		e.held = true
		e.line = ""
		return card.Outcome{State: record.StatePending, Line: lineHeld, Held: true}, nil
	}
	pr.uses = append(pr.uses, now)
	e.state, e.line, e.decidedAt, e.held = record.StateApproved, lineApproved, now, false
	return card.Outcome{State: e.state, Line: e.line}, nil
}

// Decider says that the sample backend may decide: it takes decisions, in
// memory only.
func (b *Backend) Decider() (ok bool, reason string) { return true, "" }

// view makes the limit view of a project. The caller holds the lock.
func (b *Backend) view(name string, now time.Time) (limits.View, error) {
	pr, ok := b.projects[name]
	if !ok {
		return limits.View{}, errors.New("demo: no such sample project")
	}
	days := limits.DaysOf(pr.uses, pr.limitOn, now)
	return limits.NewView(name, pr.limit, days, pr.lastRaise, now)
}

// Limit returns the limit's view of a project, by the rules of the limits
// package.
func (b *Backend) Limit(name string) (limits.View, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.view(name, b.now())
}

// SetLimit changes the limit of a project by the rules of the limits
// package. A change it refuses is answered with limit-refused and the
// refusal's reason, and leaves the view as it was.
func (b *Backend) SetLimit(name string, c limits.Change) (limits.View, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	v, err := b.view(name, now)
	if err != nil {
		return limits.View{}, err
	}
	if _, err := limits.Apply(v, c); err != nil {
		return v, refuse(record.CodeLimitRefused, err.Error())
	}
	pr := b.projects[name]
	today := limits.Today(now)
	if n := len(pr.history); pr.history[n-1].date == today {
		pr.history[n-1].limit = c.To
	} else {
		pr.history = append(pr.history, limitFrom{today, c.To})
	}
	if c.To > pr.limit {
		pr.lastRaise = now
	}
	pr.limit = c.To
	return b.view(name, now)
}

// Activity notes that the owner did something at the given time, which is
// what lets a decision through.
func (b *Backend) Activity(at time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.tracker.Activity(at)
}

// Looked moves the "since he last looked" mark that List returns. It never
// moves back.
func (b *Backend) Looked(at time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lookedAt = presence.Advance(b.lookedAt, at)
}
