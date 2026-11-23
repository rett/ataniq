package seatapp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// Lead is text a lead wrote, or that may hold bytes a lead wrote: a path, a
// ref's name, a line of the plan, the lead's note. It has no exported field;
// a page shows it only through [Text] (the template function "lead"), and
// printing it with the fmt package, as a template does with a value that is
// not a string, gives Text's output too. Make one with [NewLead].
//
// The port fills it with the text as the card model lets it out: escaped by
// the lead-text escaping the desk console uses, so that a path shows the same
// on the phone as at the desk. Text then shows that as plain text.
type Lead struct {
	s *string
}

// NewLead wraps text a lead wrote. Empty text is the zero Lead.
func NewLead(s string) Lead {
	if s == "" {
		return Lead{}
	}
	return Lead{s: &s}
}

func (l Lead) str() string {
	if l.s == nil {
		return ""
	}
	return *l.s
}

// Empty reports whether the text is empty.
func (l Lead) Empty() bool { return l.str() == "" }

// maxLeadShown is how much of a Lead fmt shows, in characters.
const maxLeadShown = 1024

// Format writes the text through [Text], whatever the verb.
func (l Lead) Format(f fmt.State, _ rune) {
	io.WriteString(f, Text(l.str(), maxLeadShown))
}

// Listing is what the card list shows: the daemon's owner.list.
type Listing struct {
	// Pending are the cards that wait for a decision, oldest first.
	Pending []Item
	// Recent are the latest decisions and outcomes, newest first.
	Recent []Recent
	// NotListed is how many pending cards were left out because the list
	// did not fit.
	NotListed int64
}

// Item is one pending card in the list. Every string is the daemon's, and
// the app shows it with [Text].
type Item struct {
	ID, Project, Verb, Feature string
	Asked, Expires             time.Time
	// Held is the mark "decided past the limit".
	Held bool
}

// Recent is one decided or ended card in the list.
type Recent struct {
	ID, Project, Verb, Feature string
	// State is the record's state, and Line what happened, in the host's
	// words.
	State, Line string
	At          time.Time
}

// Card is one card as the phone shows it, built by the port from the
// record's bytes with the card model (package seatlink): the app never takes
// a card rendered elsewhere. Strings other than [Lead] are the host's, and
// the app shows them with [Text] all the same.
type Card struct {
	ID, Project, Role string
	// Verb is the verb, Feature the feature's name of a feature finish,
	// Target the branch it merges into, and Class the verb's class.
	Verb, Feature, Target, Class string
	Asked, Expires               time.Time
	// BindingSHA256 is the digest of the binding the card shows.
	BindingSHA256 string
	// State is the record's state, empty while it is pending; Line is the
	// host's line about what came after; Ended says it can no longer be
	// decided; Held is the mark "decided past the limit"; Refused says the
	// card exists because a run refused.
	State, Line          string
	Ended, Held, Refused bool
	// Plan is the plan text, line by line.
	Plan []Lead
	// Refs are the refs the approval would move or delete.
	Refs []Ref
	// Diff is the diff summary, nil for a prune.
	Diff *Diff
	// Lists are the four lists of the summary.
	Lists []List
	// Flagged are the paths that each need their own sign-off.
	Flagged []Flagged
	// Evidence are the host's records the approval rests on.
	Evidence []Evidence
	// Note is the lead's note, line by line: lead-stated, not checked.
	Note []Lead
	// Differs is the "differs from an earlier card" block, or nil.
	Differs *Differs
	// DiffersRefused is the host's line when the earlier card could not be
	// compared.
	DiffersRefused string
}

// Ref is one ref line of a card.
type Ref struct {
	Kind     string
	Ref      Lead
	Old, New string
	Deletes  bool
}

// Diff is a card's diff summary and its rows.
type Diff struct {
	Files, Added, Removed, NewBytes int64
	// Truncated says the rows are only the first part of the path list;
	// the whole list is read in pages.
	Truncated bool
	Rows      []PathRow
}

// PathRow is one changed path.
type PathRow struct {
	Path           Lead
	Status         string
	Added, Removed int64
}

// List is one of the summary's four lists.
type List struct {
	Label string
	Paths []Lead
}

// Flagged is one flagged path.
type Flagged struct {
	Kind, Reason  string
	Path, Pattern Lead
}

// Evidence names one record the approval rests on.
type Evidence struct {
	Kind, RecordID string
}

// Differs is how a card differs from an earlier card the owner read.
type Differs struct {
	EarlierID string
	ReadAt    time.Time
	// Lines are the host's lines on what moved.
	Lines []string
	// PathsAdded and PathsRemoved are the paths this card has and the
	// earlier had not, and the other way round; Partial says that either
	// list may miss paths.
	PathsAdded, PathsRemoved []Lead
	Partial                  bool
	// RefsAdded and RefsRemoved are the same for the refs.
	RefsAdded, RefsRemoved []Lead
}

// PathsPage is one page of a card's whole path list.
type PathsPage struct {
	// N is the page's number, from 0, and Pages how many there are.
	N, Pages int
	Rows     []PathRow
}

// The errors of the reads of a Daemon that the app tells apart.
var (
	// ErrNoCard says that the daemon has no card of this id.
	ErrNoCard = errors.New("seatapp: no card of this id")
	// ErrCardChanged says that the card's review changed while it was read.
	ErrCardChanged = errors.New("seatapp: the card changed")
	// ErrTooLarge says that the answer does not fit the gate's frame.
	ErrTooLarge = errors.New("seatapp: too large for the phone")
)

// The limits of the reads.
const (
	// readRequests and readWindow bound the reads of one credential's
	// sessions, so that with logins (at most 16 a minute) the reads of a
	// phone stay within the gate's 60 a minute.
	readRequests = 20
	readWindow   = time.Minute
	// maxPage is the highest page number asked for.
	maxPage = 1<<16 - 1
	// maxCardID is the longest card id: "ap-" and 48 characters.
	maxCardID = 3 + 48
)

// validCardID reports whether s is a card's id: "ap-" and 1 to 48 lower-case
// letters, digits and dashes, starting with a letter or digit, as the record
// package says. An id that is not one is not asked of the daemon.
func validCardID(s string) bool {
	if len(s) < 4 || len(s) > maxCardID || s[:3] != "ap-" || s[3] == '-' {
		return false
	}
	for i := 3; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

// cardHref is the address of a card's page.
func cardHref(id string) string {
	if !validCardID(id) {
		return cardsPath
	}
	return cardsPath + id
}

// pathsHref is the address of page n of a card's path list.
func pathsHref(id string, n int) string {
	if !validCardID(id) || n < 0 || n > maxPage {
		return cardsPath
	}
	return cardsPath + id + "/paths/" + strconv.Itoa(n)
}

// cardsPath is the card list.
const cardsPath = "/cards/"

// problem is a page that says why the page asked for cannot be shown, with
// its status.
type problem struct {
	status  int
	title   string
	message string
}

// The problems of the card pages, each with fixed words.
var (
	problemNoCard      = problem{http.StatusNotFound, "No such card", "The desk has no card of this name. It may have ended. Look at the list again."}
	problemChanged     = problem{http.StatusConflict, "The card changed", "The card changed while it was read. Open it again."}
	problemTooLarge    = problem{http.StatusRequestEntityTooLarge, "Too large for the phone", "This is too large for this phone. Read it at the desk."}
	problemUnavailable = problem{http.StatusServiceUnavailable, "The desk cannot be reached", "The desk cannot be reached. Try again in a moment."}
	problemNotOffered  = problem{http.StatusServiceUnavailable, "Cards are not offered", "The desk does not show its cards on the phone now."}
	problemBusy        = problem{http.StatusServiceUnavailable, "The desk is busy", "The desk is busy. Try again in a minute."}
	problemTooMany     = problem{http.StatusTooManyRequests, "Too many reads", "This phone read too many pages in a minute. Wait a minute, then try again."}
	problemFailed      = problem{http.StatusBadGateway, "Something went wrong", "This could not be read. Look at the list again, or read it at the desk."}
	problemNotFound    = problem{http.StatusNotFound, "Not found", "There is no such page."}
)

// readProblem maps an error of the reads to its problem.
func readProblem(err error) problem {
	switch {
	case errors.Is(err, ErrNoCard):
		return problemNoCard
	case errors.Is(err, ErrCardChanged):
		return problemChanged
	case errors.Is(err, ErrTooLarge):
		return problemTooLarge
	case errors.Is(err, ErrNotOffered):
		return problemNotOffered
	case errors.Is(err, ErrBusy):
		return problemBusy
	case errors.Is(err, ErrDaemonUnavailable):
		return problemUnavailable
	}
	return problemFailed
}

// The data of the card pages. Each has the Title the layout shows.
type (
	cardsData struct {
		Title   string
		Now     time.Time
		Listing Listing
	}
	cardData struct {
		Title string
		Now   time.Time
		Card  Card
	}
	pathsData struct {
		Title string
		ID    string
		Page  PathsPage
		// Number is the page's number from 1; Prev, Next and Back are the
		// app's own addresses, empty when there is none.
		Number           int
		Prev, Next, Back string
	}
	problemData struct {
		Title   string
		Message string
	}
)

// read checks a session's bound of reads, and gives the context of a call.
// It answers the problem and returns false when the bound is met.
func (a *app) read(w http.ResponseWriter, r *http.Request, s Session) (context.Context, context.CancelFunc, bool) {
	if !a.reads.allow(s.Credential, a.cfg.Now()) {
		a.problem(w, problemTooMany)
		return nil, nil, false
	}
	ctx, cancel := context.WithTimeout(r.Context(), daemonTimeout)
	return ctx, cancel, true
}

// cardList answers GET /cards/: the pending cards and the recent ones.
func (a *app) cardList(w http.ResponseWriter, r *http.Request, s Session) {
	ctx, cancel, ok := a.read(w, r, s)
	if !ok {
		return
	}
	defer cancel()
	l, err := a.cfg.Daemon.List(ctx)
	if err != nil {
		a.problem(w, readProblem(err))
		return
	}
	a.render(w, "cards", http.StatusOK, cardsData{Title: pageTitles["cards"], Now: a.cfg.Now(), Listing: l})
}

// cardPage answers GET /cards/{id}: one card, read-only.
func (a *app) cardPage(w http.ResponseWriter, r *http.Request, s Session) {
	id := r.PathValue("id")
	if !validCardID(id) {
		a.problem(w, problemNotFound)
		return
	}
	ctx, cancel, ok := a.read(w, r, s)
	if !ok {
		return
	}
	defer cancel()
	c, err := a.cfg.Daemon.Show(ctx, id)
	if err != nil {
		a.problem(w, readProblem(err))
		return
	}
	if c.ID != id {
		a.problem(w, problemFailed)
		return
	}
	a.render(w, "card", http.StatusOK, cardData{Title: pageTitles["card"], Now: a.cfg.Now(), Card: c})
}

// pathsPage answers GET /cards/{id}/paths/{n}: a page of a card's whole path
// list.
func (a *app) pathsPage(w http.ResponseWriter, r *http.Request, s Session) {
	id := r.PathValue("id")
	n, err := strconv.Atoi(r.PathValue("n"))
	if !validCardID(id) || err != nil || n < 0 || n > maxPage || strconv.Itoa(n) != r.PathValue("n") {
		a.problem(w, problemNotFound)
		return
	}
	ctx, cancel, ok := a.read(w, r, s)
	if !ok {
		return
	}
	defer cancel()
	p, err := a.cfg.Daemon.Pages(ctx, id, n)
	if err != nil {
		a.problem(w, readProblem(err))
		return
	}
	if p.N != n || p.Pages < 1 || p.N >= p.Pages {
		a.problem(w, problemFailed)
		return
	}
	d := pathsData{Title: pageTitles["paths"], ID: id, Page: p, Number: n + 1, Back: cardHref(id)}
	if n > 0 {
		d.Prev = pathsHref(id, n-1)
	}
	if n+1 < p.Pages {
		d.Next = pathsHref(id, n+1)
	}
	a.render(w, "paths", http.StatusOK, d)
}

// problem answers a problem page.
func (a *app) problem(w http.ResponseWriter, p problem) {
	a.render(w, "problem", p.status, problemData{Title: p.title, Message: p.message})
}
