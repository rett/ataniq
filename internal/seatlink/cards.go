package seatlink

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"ataniq/internal/approval/card"
	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/approval/record"
	"ataniq/internal/seatapp"
	"ataniq/internal/seatgate"
)

// The codes of the gate the reads tell apart.
const (
	codeApprovalVoid  = "approval-void"
	codeReviewChanged = "review-changed"
)

// readCodes are the codes of the gate a read maps to the app's errors.
var readCodes = map[string]error{
	codeApprovalVoid:         seatapp.ErrNoCard,
	codeReviewChanged:        seatapp.ErrCardChanged,
	seatgate.CodeTooLarge:    seatapp.ErrTooLarge,
	codeUnknownOp:            seatapp.ErrNotOffered,
	seatgate.CodeRateLimited: seatapp.ErrBusy,
	seatgate.CodeBusy:        seatapp.ErrBusy,
	codeTooManyRefusals:      seatapp.ErrBusy,
}

// errReadAnswer is the error of an answer to a read that is not of its form,
// or not the answer to what was asked: a record whose bytes are not its
// digest's, a record of another card, a page of another card. It holds
// nothing of the answer.
var errReadAnswer = errors.New("seatlink: the gate's answer to the read is not of its form")

// errCard is the error of a record the card model refuses: there is no card,
// so the phone shows none.
var errCard = errors.New("seatlink: the card model refuses the record")

// maxReviews bounds the review digests the link remembers.
const maxReviews = 256

// reviews remembers the review digest of each card the link showed, which a
// page of its path list is asked for. A pending record does not change, so
// neither does its review digest.
type reviews struct {
	mu sync.Mutex
	m  map[string]string
}

func (r *reviews) get(id string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.m[id]
}

func (r *reviews) put(id, digest string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.m == nil || len(r.m) >= maxReviews {
		r.m = map[string]string{}
	}
	r.m[id] = digest
}

// reading checks what every read needs before it is sent: a context that has
// not ended and a gate that lists "read".
func (d *Daemon) reading(ctx context.Context) error {
	if ctx.Err() != nil {
		return seatapp.ErrDaemonUnavailable
	}
	if !d.offers(CapRead) {
		if _, ok := d.c.Connected(); !ok {
			return seatapp.ErrDaemonUnavailable
		}
		return seatapp.ErrNotOffered
	}
	return nil
}

// List reads the card list (owner.list). A row whose id is not a card's or
// whose times are not a record's is a bad answer.
func (d *Daemon) List(ctx context.Context) (seatapp.Listing, error) {
	if err := d.reading(ctx); err != nil {
		return seatapp.Listing{}, err
	}
	if !d.budget.take(kindRead) {
		return seatapp.Listing{}, seatapp.ErrBusy
	}
	var raw json.RawMessage
	if err := d.c.Call(ctx, ownerwire.OpList, nil, &raw); err != nil {
		return seatapp.Listing{}, mapCall(ctx, ownerwire.OpList, err, readCodes)
	}
	l, err := ownerwire.DecodeListing(raw)
	if err != nil {
		return seatapp.Listing{}, errReadAnswer
	}
	out := seatapp.Listing{NotListed: l.NotListed}
	for _, p := range l.Pending {
		asked, err1 := record.ParseTime(p.RequestedAt)
		expires, err2 := record.ParseTime(p.PendingExpiresAt)
		if !record.ValidPendingID(p.ID) || err1 != nil || err2 != nil {
			return seatapp.Listing{}, errReadAnswer
		}
		out.Pending = append(out.Pending, seatapp.Item{
			ID: p.ID, Project: p.Project, Verb: p.Verb, Feature: p.Feature,
			Asked: asked, Expires: expires, Held: p.Held,
		})
	}
	for _, r := range l.Recent {
		at, err := record.ParseTime(r.At)
		if !record.ValidPendingID(r.ID) || err != nil {
			return seatapp.Listing{}, errReadAnswer
		}
		out.Recent = append(out.Recent, seatapp.Recent{
			ID: r.ID, Project: r.Project, Verb: r.Verb, Feature: r.Feature,
			State: string(r.State), Line: r.Line, At: at,
		})
	}
	return out, nil
}

// Show reads one card (owner.show) and builds what the phone shows from the
// record's bytes with the card model, never from text the gate rendered. The
// bytes must be their digest's, decode as a pending record of the card asked
// for, and pass the card model's checks, which include the binding's digest;
// an earlier record must decode as well. An id that is not a card's is not
// sent.
func (d *Daemon) Show(ctx context.Context, id string) (seatapp.Card, error) {
	if !record.ValidPendingID(id) {
		return seatapp.Card{}, seatapp.ErrNoCard
	}
	if err := d.reading(ctx); err != nil {
		return seatapp.Card{}, err
	}
	params, err := ownerwire.EncodeShowParams(ownerwire.ShowParams{ID: id})
	if err != nil {
		return seatapp.Card{}, seatapp.ErrNoCard
	}
	if !d.budget.take(kindRead) {
		return seatapp.Card{}, seatapp.ErrBusy
	}
	var raw json.RawMessage
	if err := d.c.Call(ctx, ownerwire.OpShow, json.RawMessage(params), &raw); err != nil {
		return seatapp.Card{}, mapCall(ctx, ownerwire.OpShow, err, readCodes)
	}
	shown, err := ownerwire.DecodeShowResult(raw)
	if err != nil || record.Digest(shown.Record.Bytes) != shown.Record.SHA256 {
		return seatapp.Card{}, errReadAnswer
	}
	pending, err := record.DecodePending(shown.Record.Bytes)
	if err != nil || pending.ID != id {
		return seatapp.Card{}, errReadAnswer
	}
	in := card.Inputs{Held: shown.Held}
	line := ""
	if shown.Outcome != nil {
		// The line is the host's and is shown through the app's Text; it is
		// not handed to the card model, which would refuse the whole card
		// for a line it would have to escape.
		in.State, line = shown.Outcome.State, shown.Outcome.Line
	}
	if shown.Earlier != nil {
		prev, err := record.DecodePending(shown.Earlier.Record.Bytes)
		at, err2 := record.ParseTime(shown.Earlier.ReadAt)
		if err != nil || err2 != nil || record.Digest(shown.Earlier.Record.Bytes) != shown.Earlier.Record.SHA256 {
			return seatapp.Card{}, errReadAnswer
		}
		in.Earlier = &card.Earlier{Record: prev, ReadAt: at}
	}
	c, err := card.New(pending, in)
	if err != nil {
		return seatapp.Card{}, errCard
	}
	if c.Diff != nil {
		d.reviews.put(id, c.Diff.ReviewDigest)
	}
	d.shown.put(id, c)
	v := viewOf(c)
	v.Line = line
	v.OnItsWay = shown.OnItsWay
	return v, nil
}

// Pages reads page n of a card's whole path list (owner.paths), for the
// review the card was shown with. A card the link has not shown is shown
// first, to learn its review; a card without a diff has no path list. The
// page must be the one asked for.
func (d *Daemon) Pages(ctx context.Context, id string, n int) (seatapp.PathsPage, error) {
	if !record.ValidPendingID(id) || n < 0 || n >= ownerwire.MaxPages {
		return seatapp.PathsPage{}, seatapp.ErrNoCard
	}
	if err := d.reading(ctx); err != nil {
		return seatapp.PathsPage{}, err
	}
	review := d.reviews.get(id)
	if review == "" {
		c, err := d.Show(ctx, id)
		if err != nil {
			return seatapp.PathsPage{}, err
		}
		if c.Diff == nil {
			return seatapp.PathsPage{}, seatapp.ErrNoCard
		}
		review = d.reviews.get(id)
	}
	q := ownerwire.PageParams{ID: id, ReviewDigest: review, Page: n}
	params, err := ownerwire.EncodePageParams(q)
	if err != nil {
		return seatapp.PathsPage{}, seatapp.ErrNoCard
	}
	if !d.budget.take(kindRead) {
		return seatapp.PathsPage{}, seatapp.ErrBusy
	}
	var raw json.RawMessage
	if err := d.c.Call(ctx, ownerwire.OpPaths, json.RawMessage(params), &raw); err != nil {
		return seatapp.PathsPage{}, mapCall(ctx, ownerwire.OpPaths, err, readCodes)
	}
	p, err := ownerwire.DecodePathsPage(raw)
	if err != nil {
		return seatapp.PathsPage{}, errReadAnswer
	}
	if err := p.Answers(q); err != nil {
		if code, ok := record.CodeOf(err); ok && code == record.CodeReviewChanged {
			return seatapp.PathsPage{}, seatapp.ErrCardChanged
		}
		return seatapp.PathsPage{}, errReadAnswer
	}
	out := seatapp.PathsPage{N: p.Page, Pages: p.Pages, Review: p.ReviewDigest}
	for _, pc := range p.Paths {
		out.Rows = append(out.Rows, pathRow(card.RowOf(pc)))
	}
	return out, nil
}

// lead is the text a card's lead text gives out: escaped by the lead-text
// escaping, the form the desk console shows, so that the phone shows the same.
// The app shows it through its Text.
func lead(t card.LeadText) seatapp.Lead {
	b, _ := t.MarshalText()
	return seatapp.NewLead(string(b))
}

func leads(ts []card.LeadText) []seatapp.Lead {
	var out []seatapp.Lead
	for _, t := range ts {
		out = append(out, lead(t))
	}
	return out
}

func pathRow(r card.Row) seatapp.PathRow {
	return seatapp.PathRow{Path: lead(r.Path), Status: r.Status, Added: r.Added, Removed: r.Removed}
}

// viewOf is the card as the phone shows it.
func viewOf(c card.Card) seatapp.Card {
	v := seatapp.Card{
		ID: c.Title.ID, Project: c.Title.Project, Role: c.Title.Subject.Role,
		Verb: c.Verb.Name, Feature: c.Verb.Feature, Target: c.Verb.Target, Class: c.Verb.Class,
		Asked: c.Title.Asked, Expires: c.Title.Expires,
		BindingSHA256: c.BindingSHA256,
		State:         string(c.After.State),
		Ended:         c.Ended(),
		Held:          c.After.Held,
		Refused:       c.Refused,
		Plan:          leads(c.Plan.Lines),
		Note:          leads(c.Note.Lines()),
	}
	for _, r := range c.Refs {
		ref := seatapp.Ref{Kind: r.Kind, Ref: lead(r.Ref), Old: r.Old, New: r.New, Deletes: r.Deletes}
		if m := r.MergedInto; m != nil {
			ref.MergedInto = &seatapp.MergedInto{Ref: lead(m.Ref), Tip: m.Tip}
		}
		v.Refs = append(v.Refs, ref)
	}
	if c.Diff != nil {
		diff := &seatapp.Diff{Files: c.Diff.Files, Added: c.Diff.Added, Removed: c.Diff.Removed, NewBytes: c.Diff.NewBytes, Truncated: c.Diff.Truncated}
		for _, r := range c.Diff.Rows {
			diff.Rows = append(diff.Rows, pathRow(r))
		}
		v.Diff = diff
	}
	for _, l := range c.Lists {
		v.Lists = append(v.Lists, seatapp.List{Label: l.Kind.Label(), Paths: leads(l.Paths)})
	}
	for _, f := range c.Flagged {
		v.Flagged = append(v.Flagged, seatapp.Flagged{Kind: f.Kind, Reason: f.Reason, Path: lead(f.Path), Pattern: lead(f.Pattern)})
	}
	for _, e := range c.Evidence {
		v.Evidence = append(v.Evidence, seatapp.Evidence{Kind: e.Kind, RecordID: e.RecordID})
	}
	if df := c.Differs; df != nil {
		out := &seatapp.Differs{
			EarlierID: df.EarlierID, ReadAt: df.ReadAt,
			PathsAdded: leads(df.PathsAdded), PathsRemoved: leads(df.PathsRemoved), Partial: df.PathsPartial,
			RefsAdded: leads(df.RefsAdded), RefsRemoved: leads(df.RefsRemoved),
		}
		for _, ch := range []struct {
			what string
			c    card.Change
		}{{"The feature's tip", df.Feature}, {"The target's tip", df.Target}, {"The merge commit", df.Merge}} {
			if ch.c.Changed() {
				out.Lines = append(out.Lines, ch.what+" moved: "+short(ch.c.Old)+" to "+short(ch.c.New)+".")
			}
		}
		if len(out.Lines) == 0 && len(out.PathsAdded) == 0 && len(out.PathsRemoved) == 0 && len(out.RefsAdded) == 0 && len(out.RefsRemoved) == 0 {
			out.Lines = append(out.Lines, "Nothing it shows moved since then.")
		}
		v.Differs = out
	}
	v.DiffersRefused = c.DiffersRefused
	return v
}

// short is the first 12 digits of an object id, or "none" for an empty one.
func short(s string) string {
	if s == "" {
		return "none"
	}
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
