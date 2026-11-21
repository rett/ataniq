package consoleclient

import (
	"errors"
	"time"

	"ataniq/internal/approval/card"
	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/approval/record"
)

// This file holds the reading operations of [Owner]: the list, one card, and
// the pages behind it.

// Decider says whether this connection may decide, and why not. The daemon
// answers it; this method asks it on every call, so that a console that lost
// the place to another one sees the reason change. When the daemon cannot be
// asked, or has no owner channel, the answer is false and the reason is the
// fixed text of the error.
func (o *Owner) Decider() (ok bool, reason string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	c, err := o.connect()
	if err != nil {
		return false, reasonOf(err)
	}
	if c.fresh {
		// connect asked a moment ago.
		c.fresh = false
		return c.decider, c.reason
	}
	params, err := ownerwire.EncodeDeciderParams(ownerwire.DeciderParams{Client: o.name, Session: o.session})
	if err != nil {
		return false, stateTexts[StateBadAnswer][0]
	}
	raw, err := o.call(ownerwire.OpDecider, jsonRaw(params))
	if err != nil {
		return false, reasonOf(err)
	}
	res, err := ownerwire.DecodeDeciderResult(raw)
	if err != nil {
		return false, reasonOf(o.badAnswer())
	}
	return res.Decider, clean(res.Reason)
}

// reasonOf is the line that says why a connection is not the decider when the
// daemon could not say. It is fixed text of this package or of a state.
func reasonOf(err error) string {
	var rec *record.Error
	if errors.As(err, &rec) {
		return "the daemon refused the question: " + string(rec.Code)
	}
	return err.Error()
}

// Activity tells the daemon that the owner is active at this surface. The
// time is not sent: the daemon's clock decides. A failure is not an error of
// the console's: the next call finds the connection as it is.
func (o *Owner) Activity(time.Time) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if raw, err := o.ask(ownerwire.OpBeat, nil); err == nil && ownerwire.DecodeNothing(raw) != nil {
		o.badAnswer()
	}
}

// Looked tells the daemon that the owner looked at the list now: the daemon
// moves its mark, and List returns it. The time is not sent.
func (o *Owner) Looked(time.Time) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if raw, err := o.ask(ownerwire.OpLooked, nil); err == nil && ownerwire.DecodeNothing(raw) != nil {
		o.badAnswer()
	}
}

// List returns the daemon's listing as the console's. The rows come as the
// daemon sent them in order, the pending ones with their asked and expiry
// times and the held mark, the recent ones with their state and the host's
// line. A row has no binding digest here: the listing carries the digest of
// the record, and the binding's is on the card. A listing with a project that
// is not plain text is a bad answer.
func (o *Owner) List() (card.Listing, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	raw, err := o.ask(ownerwire.OpList, nil)
	if err != nil {
		return card.Listing{}, err
	}
	l, err := ownerwire.DecodeListing(raw)
	if err != nil {
		return card.Listing{}, o.badAnswer()
	}
	out, ok := listingOf(l)
	if !ok {
		return card.Listing{}, o.badAnswer()
	}
	o.notListed = l.NotListed
	return out, nil
}

// listingOf maps the wire listing. It returns false for one that the
// console cannot show faithfully.
func listingOf(l ownerwire.Listing) (card.Listing, bool) {
	out := card.Listing{Count: card.Count{Requests: int(l.Count.Requests), Met: int(l.Count.WouldMeet)}}
	if l.LookedAt != "" {
		at, err := record.ParseTime(l.LookedAt)
		if err != nil {
			return card.Listing{}, false
		}
		out.LookedAt = at
	}
	for _, p := range l.Pending {
		if !plain(p.Project) {
			return card.Listing{}, false
		}
		asked, err1 := record.ParseTime(p.RequestedAt)
		expires, err2 := record.ParseTime(p.PendingExpiresAt)
		if err1 != nil || err2 != nil {
			return card.Listing{}, false
		}
		out.Pending = append(out.Pending, card.Item{
			ID: p.ID, Project: p.Project, Verb: p.Verb, Feature: p.Feature,
			Asked: asked, Expires: expires, Held: p.Held,
		})
	}
	for _, r := range l.Recent {
		if !plain(r.Project) {
			return card.Listing{}, false
		}
		at, err := record.ParseTime(r.At)
		if err != nil {
			return card.Listing{}, false
		}
		// The row's Asked is the time of the event: a recent row has no other
		// time on the wire.
		out.Recent = append(out.Recent, card.Item{
			ID: r.ID, Project: r.Project, Verb: r.Verb, Feature: r.Feature,
			Asked: at, State: r.State, Line: clean(r.Line),
		})
	}
	for _, r := range l.Rules {
		if !plain(r.Project) {
			return card.Listing{}, false
		}
		out.Rules = append(out.Rules, card.Rule{Project: r.Project, Name: clean(r.ID), Text: clean(r.Line), On: r.On, Uses: int(r.Uses)})
	}
	return out, true
}

// plain reports whether s is shown as it is by clean, so that the project's
// name the console shows is the one it asks the limit about.
func plain(s string) bool { return s != "" && clean(s) == s }

// Open reads one card: the pending record as bytes with its digest, what a
// card needs beside it, and the earlier card he read, and builds the card from
// them. It notes the card's review digest, which the pages are asked for, and
// the connection the card was read over, which its decision must go out on.
// A successful Open also ends a doubt about an earlier decision on the card.
func (o *Owner) Open(id string) (card.Card, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	params, err := ownerwire.EncodeShowParams(ownerwire.ShowParams{ID: id})
	if err != nil {
		return card.Card{}, err
	}
	raw, err := o.ask(ownerwire.OpShow, jsonRaw(params))
	if err != nil {
		return card.Card{}, err
	}
	shown, err := ownerwire.DecodeShowResult(raw)
	if err != nil {
		return card.Card{}, o.badAnswer()
	}
	pending, err := record.DecodePending(shown.Record.Bytes)
	if err != nil || pending.ID != id {
		// A record that does not decode, or is another card's, is not what
		// was asked for.
		return card.Card{}, o.badAnswer()
	}
	in := card.Inputs{Held: shown.Held}
	if shown.Outcome != nil {
		in.State, in.Line = shown.Outcome.State, clean(shown.Outcome.Line)
	}
	if shown.Earlier != nil {
		prev, err := record.DecodePending(shown.Earlier.Record.Bytes)
		at, err2 := record.ParseTime(shown.Earlier.ReadAt)
		if err != nil || err2 != nil {
			return card.Card{}, o.badAnswer()
		}
		in.Earlier = &card.Earlier{Record: prev, ReadAt: at}
	}
	c, err := card.New(pending, in)
	if err != nil {
		return card.Card{}, err
	}
	o.remember(id, c.Diff.ReviewDigest)
	delete(o.doubt, id)
	return c, nil
}

// Page reads page n of a card's whole path list or of its full patch. The
// card must have been opened through this client: the page is asked for the
// review digest the card was made from, and a page of another review is
// refused review-changed, whether the daemon says so or names another review
// on the page. A page is taken in only when it is the page asked for. The
// lines of a patch are raw bytes of a lead: only the pager shows them, and
// only escaped.
func (o *Owner) Page(id string, kind card.PageKind, n int) (card.Page, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	ref, ok := o.cards[id]
	if !ok || ref.review == "" {
		return card.Page{}, ErrCardNotOpen
	}
	q := ownerwire.PageParams{ID: id, ReviewDigest: ref.review, Page: n}
	params, err := ownerwire.EncodePageParams(q)
	if err != nil {
		return card.Page{}, err
	}
	switch kind {
	case card.PagePaths:
		raw, err := o.ask(ownerwire.OpPaths, jsonRaw(params))
		if err != nil {
			return card.Page{}, err
		}
		p, err := ownerwire.DecodePathsPage(raw)
		if err != nil {
			return card.Page{}, o.badAnswer()
		}
		if err := p.Answers(q); err != nil {
			return card.Page{}, o.answersError(err)
		}
		return card.Page{Kind: kind, N: p.Page, Pages: p.Pages, ReviewDigest: p.ReviewDigest, Paths: p.Paths}, nil
	case card.PagePatch:
		raw, err := o.ask(ownerwire.OpPatch, jsonRaw(params))
		if err != nil {
			return card.Page{}, err
		}
		p, err := ownerwire.DecodePatchPage(raw)
		if err != nil {
			return card.Page{}, o.badAnswer()
		}
		if err := p.Answers(q); err != nil {
			return card.Page{}, o.answersError(err)
		}
		out := card.Page{Kind: kind, N: p.Page, Pages: p.Pages, ReviewDigest: p.ReviewDigest}
		for _, l := range p.Lines {
			out.Patch = append(out.Patch, string(l.Text))
			out.Cont = append(out.Cont, l.Cont)
		}
		return out, nil
	}
	return card.Page{}, &record.Error{Code: record.CodeRecordInvalid, Detail: "page.kind is not a kind of page"}
}

// answersError is the error for a page that is not the page asked for: a
// page of another review is refused review-changed, which the console shows
// as the end of the reading, and anything else is a bad answer. The caller
// holds o.mu.
func (o *Owner) answersError(err error) error {
	if code, ok := record.CodeOf(err); ok && code == record.CodeReviewChanged {
		return &record.Error{Code: record.CodeReviewChanged, Detail: "the page belongs to another review than the card's"}
	}
	return o.badAnswer()
}
