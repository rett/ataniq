package ownerchannel

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"ataniq/internal/approval/door"
	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/approval/presence"
	"ataniq/internal/approval/record"
)

// This file holds the owner connection's operations other than the
// decision: each is a function that returns the result type of package
// ownerwire, so that the daemon only has to register them.

// Looked moves the "since he last looked" mark to now (owner.looked).
func (s *Service) Looked(ctx context.Context) error {
	ctx, done, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer done()
	if err := s.journalIn(ctx).AppendLooked(s.now()); err != nil {
		return s.jfix(ctx, err)
	}
	return nil
}

// stamp is a time as a record writes it.
func stamp(t time.Time) string { return record.FormatTime(t) }

// maxRecent is how many recent decisions the listing carries.
const maxRecent = ownerwire.MaxRecent

// List returns the listing (owner.list): the cards that wait, the recent
// decisions and outcomes, the count from the first card and the "since he
// last looked" mark. A listing too large for one page drops recent rows,
// oldest first, and then the newest pending rows, so that the oldest, which
// end first, are the ones shown; the listing's NotListed says how many
// were left out.
func (s *Service) List(ctx context.Context) (ownerwire.Listing, error) {
	ctx, done, err := s.begin(ctx)
	if err != nil {
		return ownerwire.Listing{}, err
	}
	defer done()
	now := s.now()
	if _, err := s.journalIn(ctx).ExpireHeld(now); err != nil {
		return ownerwire.Listing{}, s.jfix(ctx, err)
	}
	if err := s.syncIndex(ctx); err != nil {
		return ownerwire.Listing{}, s.jfix(ctx, err)
	}
	entries, err := s.cfg.Door.List(ctx)
	if err != nil {
		return ownerwire.Listing{}, wrapDoor(err)
	}
	held := make(map[string]bool)
	seen := make(map[string]bool)

	type row struct {
		p     record.Pending
		entry door.Entry
	}
	var rows []row
	for _, e := range entries {
		p, err := s.pendingFor(ctx, e)
		if err != nil {
			return ownerwire.Listing{}, wrapDoor(err)
		}
		rows = append(rows, row{p, e})
		if project := p.Binding.Project; !seen[project] {
			seen[project] = true
			cards, err := s.journalIn(ctx).HeldCards(project)
			if err != nil {
				return ownerwire.Listing{}, s.jfix(ctx, err)
			}
			for _, c := range cards {
				held[c.ID] = true
			}
		}
	}

	out := ownerwire.Listing{Pending: []ownerwire.PendingItem{}, Recent: []ownerwire.RecentItem{}, Rules: []ownerwire.Rule{}}
	var recent []timedRow
	for _, r := range rows {
		feature := featureOf(r.p.Binding)
		if r.entry.State == record.StatePending {
			out.Pending = append(out.Pending, ownerwire.PendingItem{
				ID: r.p.ID, SHA256: r.entry.PendingSHA256, Project: r.p.Binding.Project,
				Verb: r.p.Binding.Verb, Feature: feature,
				RequestedAt: r.p.Binding.RequestedAt, PendingExpiresAt: r.p.Binding.PendingExpiresAt,
				Held: held[r.p.ID],
			})
			continue
		}
		at, text := s.recentOf(r.p, r.entry)
		recent = append(recent, timedRow{at: at, item: ownerwire.RecentItem{
			ID: r.p.ID, Project: r.p.Binding.Project, Verb: r.p.Binding.Verb, Feature: feature,
			State: r.entry.State, Line: text, At: stamp(at),
		}})
	}
	sort.SliceStable(out.Pending, func(i, j int) bool {
		a, b := out.Pending[i], out.Pending[j]
		if a.RequestedAt != b.RequestedAt {
			return a.RequestedAt < b.RequestedAt
		}
		return a.ID < b.ID
	})
	sort.SliceStable(recent, func(i, j int) bool { return recent[i].at.After(recent[j].at) })
	if len(recent) > maxRecent {
		recent = recent[:maxRecent]
	}
	for _, r := range recent {
		out.Recent = append(out.Recent, r.item)
	}
	s.mu.Lock()
	out.Count.Requests, out.Count.WouldMeet = s.idx.totals()
	s.mu.Unlock()
	if at, err := s.journalIn(ctx).LookedAt(); err != nil {
		return ownerwire.Listing{}, s.jfix(ctx, err)
	} else if !at.IsZero() {
		out.LookedAt = stamp(at)
	}
	return fitListing(out)
}

type timedRow struct {
	at   time.Time
	item ownerwire.RecentItem
}

// featureOf returns the feature name of a binding, or empty for a prune.
func featureOf(b record.Binding) string {
	if b.Verb == record.VerbFeatureFinish && len(b.Argv) == 1 {
		return b.Argv[0]
	}
	return ""
}

// recentOf returns the time and the host-written line of a card that is not
// pending.
func (s *Service) recentOf(p record.Pending, e door.Entry) (time.Time, string) {
	r, journaled := s.indexed(p.ID)
	requested, _ := record.ParseTime(p.Binding.RequestedAt)
	at := requested
	text := string(e.State)
	switch e.State {
	case record.StateDenied:
		text = "denied by the owner"
		if journaled && !r.decidedAt.IsZero() {
			at = r.decidedAt
		}
	case record.StateApproved, record.StateApprovedByRule, record.StateApprovedByGrant:
		text = lineWaiting
		if journaled && !r.decidedAt.IsZero() {
			at = r.decidedAt
		}
	case record.StateConsumed:
		text = "used, and waiting for the outcome"
		if journaled && !r.decidedAt.IsZero() {
			at = r.decidedAt
		}
	case record.StateApplied, record.StateNotApplied, record.StateUnknown:
		text = outcomeLine(door.Outcome(e.State))
		if journaled && r.outcomeState == e.State {
			text, at = r.outcomeLine, r.outcomeAt
		}
	case record.StateExpired:
		text = endLine(e.State)
		if t, err := record.ParseTime(e.ExpiresAt); err == nil {
			at = t
		}
	default:
		text = endLine(e.State)
		s.mu.Lock()
		if t, ok := s.ended[p.ID]; ok {
			at = t
		}
		s.mu.Unlock()
	}
	if s.isBlocked(p.ID) {
		text = "refused: the door holds a decision that the journal does not"
	}
	return at, line(text)
}

// fitListing makes a listing fit one page. It drops recent rows from the
// oldest, and then the newest pending rows, with the number left out in the
// listing's NotListed.
func fitListing(l ownerwire.Listing) (ownerwire.Listing, error) {
	for {
		_, err := ownerwire.EncodeListing(l)
		if err == nil {
			return l, nil
		}
		if c, _ := code(err); c != record.CodeRecordTooLarge {
			return ownerwire.Listing{}, err
		}
		if len(l.Recent) == 0 {
			break
		}
		l.Recent = l.Recent[:len(l.Recent)/2]
	}
	all := l.Pending
	lo, hi := 0, len(all)
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if _, err := ownerwire.EncodeListing(cut(l, all, mid)); err == nil {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	out := cut(l, all, lo)
	if _, err := ownerwire.EncodeListing(out); err != nil {
		return ownerwire.Listing{}, err
	}
	return out, nil
}

// cut keeps the oldest n pending rows and says how many were left out.
func cut(l ownerwire.Listing, all []ownerwire.PendingItem, n int) ownerwire.Listing {
	l.Pending = all[:n]
	l.NotListed = int64(len(all) - n)
	return l
}

// openedCard is a card the owner opened.
type openedCard struct {
	id          string
	requestedAt string
	readAt      time.Time
}

// earlierWindow is how long an opened card stays the earlier card of a
// later one for the same verb and target.
const earlierWindow = 24 * time.Hour

// maxOpened is how many opened cards are kept for each verb and target.
const maxOpened = 8

// Show returns one pending record as its bytes, with the held mark, the mark
// of a decision on its way to the door (journaled, not yet handed over), the
// outcome when it is decided, and the earlier card for the same verb and
// target that the owner opened in the last 24 hours (owner.show). Opening a
// card is remembered for that purpose, in memory: after a restart there is
// no earlier card until he opens one again.
func (s *Service) Show(ctx context.Context, id string) (ownerwire.ShowResult, error) {
	ctx, done, err := s.begin(ctx)
	if err != nil {
		return ownerwire.ShowResult{}, err
	}
	defer done()
	shown, p, err := s.lookup(ctx, id)
	if err != nil {
		if isRefusal(err) {
			return ownerwire.ShowResult{}, err
		}
		return ownerwire.ShowResult{}, wrapDoor(err)
	}
	if err := s.syncIndex(ctx); err != nil {
		return ownerwire.ShowResult{}, s.jfix(ctx, err)
	}
	out := ownerwire.ShowResult{Record: ownerwire.NewRecord(append([]byte(nil), shown.Pending.Bytes...))}
	if shown.Entry.State == record.StatePending {
		cards, err := s.journalIn(ctx).HeldCards(p.Binding.Project)
		if err != nil {
			return ownerwire.ShowResult{}, s.jfix(ctx, err)
		}
		for _, c := range cards {
			if c.ID == id {
				out.Held = true
			}
		}
		// The door refused the last decision and the card is pending again:
		// say so, with the door's code.
		r, ok := s.indexed(id)
		if ok && r.refusedByDoor != "" && !out.Held {
			out.Outcome = &ownerwire.Outcome{State: record.StatePending, Line: lineRefusedByDoor(r.refusedByDoor)}
		}
		// A decision is journaled and has no delivered entry: it is on its
		// way to the door, which still shows the record pending.
		if ok && r.decided && !r.delivered {
			out.OnItsWay = true
		}
	} else {
		_, text := s.recentOf(p, shown.Entry)
		out.Outcome = &ownerwire.Outcome{State: shown.Entry.State, Line: text}
	}

	now := s.now()
	key := p.Binding.Verb + "\x00" + strings.Join(p.Binding.Argv, "\x00")
	var earlier *openedCard
	s.mu.Lock()
	list := s.opened[key]
	for i := range list {
		c := &list[i]
		before := c.requestedAt < p.Binding.RequestedAt || (c.requestedAt == p.Binding.RequestedAt && c.id < p.ID)
		if c.id != p.ID && before && now.Sub(c.readAt) <= earlierWindow && (earlier == nil || c.readAt.After(earlier.readAt)) {
			e := *c
			earlier = &e
		}
	}
	found := false
	for i := range list {
		if list[i].id == p.ID {
			list[i].readAt, found = now, true
		}
	}
	if !found {
		list = append(list, openedCard{id: p.ID, requestedAt: p.Binding.RequestedAt, readAt: now})
		if len(list) > maxOpened {
			list = list[1:]
		}
	}
	s.opened[key] = list
	s.mu.Unlock()
	if earlier != nil {
		if prev, err := s.cfg.Door.Show(ctx, earlier.id); err == nil {
			out.Earlier = &ownerwire.Earlier{
				Record: ownerwire.NewRecord(append([]byte(nil), prev.Pending.Bytes...)),
				ReadAt: stamp(earlier.readAt),
			}
		}
	}
	return out, nil
}

// cachedPages holds the pages of one review.
type cachedPages struct {
	key   string
	paths []ownerwire.PathsPage
	patch []ownerwire.PatchPage
}

const maxCachedPages = 4

// reviewOf finds the review a page request names: the pending record's
// summary, and a refusal when the digest is not the one the record has.
func (s *Service) reviewOf(ctx context.Context, id, digest string) (record.Pending, *record.Summary, error) {
	_, p, err := s.lookup(ctx, id)
	if err != nil {
		if isRefusal(err) {
			return record.Pending{}, nil, err
		}
		return record.Pending{}, nil, wrapDoor(err)
	}
	sum := p.Binding.Summary
	if sum == nil {
		return record.Pending{}, nil, refuse(record.CodeReviewChanged, "this card has no review")
	}
	if sum.ReviewDigest != digest {
		return record.Pending{}, nil, refuse(record.CodeReviewChanged, "the review behind this card is not the one named")
	}
	return p, sum, nil
}

func (s *Service) cached(key string) *cachedPages {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.pages {
		if s.pages[i].key == key {
			c := s.pages[i]
			return &c
		}
	}
	return nil
}

func (s *Service) keep(c cachedPages) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pages = append(s.pages, c)
	if len(s.pages) > maxCachedPages {
		s.pages = s.pages[1:]
	}
}

// Paths returns one page of the full path list of a review (owner.paths).
// A page request that names another review digest than the card's is
// refused review-changed, and so is one whose review the intake now
// computes with another digest.
func (s *Service) Paths(ctx context.Context, params ownerwire.PageParams) (ownerwire.PathsPage, error) {
	ctx, done, err := s.begin(ctx)
	if err != nil {
		return ownerwire.PathsPage{}, err
	}
	defer done()
	p, sum, err := s.reviewOf(ctx, params.ID, params.ReviewDigest)
	if err != nil {
		return ownerwire.PathsPage{}, err
	}
	key := "paths/" + p.ID + "/" + sum.ReviewDigest
	c := s.cached(key)
	if c == nil {
		paths := sum.Paths
		if sum.PathsTruncated {
			rp, err := s.cfg.Intake.Paths(ctx, ReviewRef{Project: p.Binding.Project, Base: sum.Base, Merge: sum.Tip})
			if err != nil {
				return ownerwire.PathsPage{}, fmt.Errorf("ownerchannel: the path list could not be read: %w", err)
			}
			if rp.ReviewDigest != sum.ReviewDigest {
				return ownerwire.PathsPage{}, refuse(record.CodeReviewChanged, "the review now has another digest")
			}
			paths = rp.Paths
		}
		pages, err := ownerwire.SplitPaths(p.ID, sum.ReviewDigest, paths)
		if err != nil {
			return ownerwire.PathsPage{}, err
		}
		c = &cachedPages{key: key, paths: pages}
		s.keep(*c)
	}
	if params.Page < 0 || params.Page >= len(c.paths) {
		return ownerwire.PathsPage{}, refuse(record.CodeRecordInvalid, "the review has no such page")
	}
	return c.paths[params.Page], nil
}

// Patch returns one page of the patch of a review as raw lines
// (owner.patch), under the same rules as [Service.Paths].
func (s *Service) Patch(ctx context.Context, params ownerwire.PageParams) (ownerwire.PatchPage, error) {
	ctx, done, err := s.begin(ctx)
	if err != nil {
		return ownerwire.PatchPage{}, err
	}
	defer done()
	p, sum, err := s.reviewOf(ctx, params.ID, params.ReviewDigest)
	if err != nil {
		return ownerwire.PatchPage{}, err
	}
	key := "patch/" + p.ID + "/" + sum.ReviewDigest
	c := s.cached(key)
	if c == nil {
		rp, err := s.cfg.Intake.Patch(ctx, ReviewRef{Project: p.Binding.Project, Base: sum.Base, Merge: sum.Tip})
		if err != nil {
			return ownerwire.PatchPage{}, fmt.Errorf("ownerchannel: the patch could not be read: %w", err)
		}
		if rp.ReviewDigest != sum.ReviewDigest {
			return ownerwire.PatchPage{}, refuse(record.CodeReviewChanged, "the review now has another digest")
		}
		pages, err := ownerwire.SplitPatch(p.ID, sum.ReviewDigest, rp.Lines)
		if err != nil {
			return ownerwire.PatchPage{}, err
		}
		c = &cachedPages{key: key, patch: pages}
		s.keep(*c)
	}
	if params.Page < 0 || params.Page >= len(c.patch) {
		return ownerwire.PatchPage{}, refuse(record.CodeRecordInvalid, "the review has no such page")
	}
	return c.patch[params.Page], nil
}

// Dispose records what the owner found for an approval whose outcome is
// unknown: applied or not-applied. The service never settles an unknown
// outcome by itself; this is the owner's word. It is refused to a
// connection that is not the decider and to an owner who is away, as a
// decision is.
//
// The door's record is settled first and the journal's outcome follows, so
// a stop between the two leaves the door with his word and the journal
// still unknown. Dispose accepts that state, and so does a restart, which
// copies what the door holds into the journal. Dispose also takes a record
// that the door holds as consumed: the door refused the settle that came
// with the outcome, and the owner's word is settled again now.
func (s *Service) Dispose(ctx context.Context, src Source, id string, state record.State) (ownerwire.DecideResult, error) {
	ctx, done, err := s.begin(ctx)
	if err != nil {
		return ownerwire.DecideResult{}, err
	}
	defer done()
	if !presence.ValidSurface(src.Surface) || !presence.ValidSession(src.Session) {
		return ownerwire.DecideResult{}, refuse(record.CodeDecisionInvalid, "the surface or the session is not well formed")
	}
	s.dmu.Lock()
	defer s.dmu.Unlock()
	now := s.now()
	if !src.Decider {
		return ownerwire.DecideResult{}, refuse(record.CodeNotDecider, "this connection is not the one that may decide")
	}
	if _, err := s.mayDecide(src, now, true); err != nil {
		return ownerwire.DecideResult{}, refuse(record.CodeOwnerAway, "no activity of the owner inside the presence window")
	}
	if state != record.StateApplied && state != record.StateNotApplied {
		return ownerwire.DecideResult{}, refuse(record.CodeDecisionInvalid, "an unknown outcome is disposed of as applied or not-applied")
	}
	shown, p, err := s.lookup(ctx, id)
	if err != nil {
		if isRefusal(err) {
			return ownerwire.DecideResult{}, err
		}
		return ownerwire.DecideResult{}, wrapDoor(err)
	}
	settle := true
	switch shown.Entry.State {
	case record.StateUnknown, record.StateConsumed:
	case state:
		// The door has his word already: a stop came between the door's
		// settle and the journal's entry. Only the journal is written.
		settle = false
	default:
		return ownerwire.DecideResult{}, refuse(record.CodeDecisionInvalid, "the approval's outcome is not unknown")
	}
	unknown, err := s.journal().UnknownOutcomes()
	if err != nil {
		return ownerwire.DecideResult{}, s.jerr(ctx, err)
	}
	for _, u := range unknown {
		if u.Decision().ID() != id {
			continue
		}
		return s.journalOutcome(ctx, u, p, door.Outcome(state), settle)
	}
	return ownerwire.DecideResult{}, refuse(record.CodeDecisionInvalid, "the journal has no unknown outcome for this approval")
}
