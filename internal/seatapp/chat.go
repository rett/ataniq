package seatapp

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	problemChatMissing    = problem{http.StatusNotFound, "No such lead", "The desk has no lead of this name."}
	problemChatOff        = problem{http.StatusForbidden, "Chat is off", "Chat is off for this lead. Turn it on at the desk."}
	problemChatNotOffered = problem{http.StatusServiceUnavailable, "Chat is not offered", "The desk does not offer lead chat."}
	problemChatChanged    = problem{http.StatusConflict, "Conversation changed", "The conversation changed while it was read. Refresh the page to try again."}
)

func chatProblem(err error) problem {
	switch {
	case errors.Is(err, ErrChatUnknownLead):
		return problemChatMissing
	case errors.Is(err, ErrChatOff):
		return problemChatOff
	case errors.Is(err, ErrNotOffered):
		return problemChatNotOffered
	case errors.Is(err, ErrBusy):
		return problemBusy
	case errors.Is(err, ErrDaemonUnavailable):
		return problemUnavailable
	}
	return problemFailed
}

func (a *app) chatOffered() bool {
	p, ok := a.cfg.Daemon.(Chat)
	return ok && p.ChatOffered()
}

type chatRow struct {
	Key, Chat, State string
	Here             bool
}
type chatMessage struct {
	ChatEntry
	Last bool
}

type timelineEvent struct {
	At   time.Time
	Text string
	Cut  bool
}

type chatSample struct {
	Project, Gate string
	Events        []timelineEvent
}

// Every validated trial directory shares these presentation samples.
var chatTrial = chatSample{
	Project: "Console trial",
	Gate:    "Waiting for review",
	Events: []timelineEvent{
		{At: time.Date(2026, 1, 12, 9, 0, 0, 0, time.UTC), Text: "Sample project opened."},
		{At: time.Date(2026, 1, 12, 9, 15, 0, 0, time.UTC), Text: "Sample gate queued for review."},
	},
}

type leadsData struct {
	pageData
	Sample *chatSample
	Rows   []chatRow
	// Watch is the chat hint taken before the page's reads, for the page's
	// hint stream (watchleads.go).
	Watch leadsWatch
}
type conversationData struct {
	leadsData
	Selected                                     chatRow
	Messages                                     []chatMessage
	Timeline                                     []timelineEvent
	Epoch, Head                                  int64
	Restarted, Reset, HavePrev, More, JumpRemote bool
	PrevAfter, LastSeq                           int64
}

// A login keeps only the last displayed epoch per listed lead, and the chat
// switch it was shown under, to say that a lead restarted between two reads;
// the page links are computed from the page itself. Serialize its chat
// requests independently of logout, Cards and other logins.
//
// What a login keeps of a lead is dropped whenever the lead it was kept for
// may be gone: the session ends (the history is the session's), the
// connection is replaced or the host turns from the trial's to a real one or
// back, a directory no longer lists the lead or lists it off, its switch
// changed (a downgrade from send to read may close the conversation the desk
// held), a read resets, or an epoch is below the one kept (another lead under
// the same key). So no restart marker of an earlier lead is carried to a key
// used again.
type chatHistory struct {
	mu         sync.Mutex
	connection time.Time
	trial      bool
	byLead     map[string]chatVisit
}
type chatVisit struct {
	epoch int64
	chat  string
}

func (h *chatHistory) directory(v ChatView) {
	if h.byLead == nil || h.connection != v.Connection || h.trial != v.StandIn {
		h.byLead = make(map[string]chatVisit)
		h.connection, h.trial = v.Connection, v.StandIn
	}
	for lead, visit := range h.byLead {
		shared := false
		for _, row := range v.Rows {
			if row.Lead == lead && row.Chat != "off" && row.Chat == visit.chat {
				shared = true
				break
			}
		}
		if !shared {
			delete(h.byLead, lead)
		}
	}
}

// chatAddress accepts the literal paths the app emits and at most one
// unsigned decimal after. Nothing from an invalid request reaches the port.
func chatAddress(r *http.Request) (lead string, after int64, explicit, ok bool) {
	path := r.URL.EscapedPath()
	if path == "/leads/" {
		return "", 0, false, r.URL.RawQuery == "" && !r.URL.ForceQuery
	}
	key := r.PathValue("key")
	if r.PathValue("kind") != "daemon" || !chatKey.MatchString(key) || path != "/leads/daemon/"+key {
		return
	}
	lead = "daemon:" + key
	if r.URL.RawQuery == "" {
		return lead, 0, false, !r.URL.ForceQuery
	}
	n, has := strings.CutPrefix(r.URL.RawQuery, "after=")
	if !has || n == "" || strings.Trim(n, "0123456789") != "" {
		return
	}
	after, err := strconv.ParseInt(n, 10, 64)
	return lead, after, true, err == nil && chatNumber(after)
}

func (a *app) chatPage(w http.ResponseWriter, r *http.Request, s Session) {
	lead, after, explicit, ok := chatAddress(r)
	if !ok {
		a.problem(w, r, problemNotFound)
		return
	}
	// As the project pages do, the page asks the port and answers by its
	// error, not by ChatOffered alone, which is false both while the link
	// is down and when the connected gate has no chat: the port tells the
	// two apart (ErrDaemonUnavailable, the page that offers a retry, and
	// ErrNotOffered) and sends nothing in either case.
	p, ok := a.cfg.Daemon.(Chat)
	if !ok {
		a.problem(w, r, problemChatNotOffered)
		return
	}
	if !a.chats.allow(s.Credential, a.cfg.Now()) {
		a.problem(w, r, problemTooMany)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), daemonTimeout)
	defer cancel()
	h := s.chat
	h.mu.Lock()
	defer h.mu.Unlock()
	_, gone, live := a.sess.look(cookieValue(r, SessionCookie+a.cookieSuffix))
	if !live || ctx.Err() != nil {
		a.problem(w, r, problemUnavailable)
		return
	}
	// The hint before the reads: a change between the two leaves the page
	// a cursor older than what it shows, and its stream says so at once.
	lw := a.leadsHint()
	c := ChatCall{Verb: ChatLeads, SessionDone: gone}
	list, err := p.Chat(ctx, c)
	if err != nil {
		a.problem(w, r, chatProblem(err))
		return
	}
	if !list.ValidFor(c) {
		a.problem(w, r, problemFailed)
		return
	}
	h.directory(list)
	// A cursor learned on another connection than the directory's (the
	// link was replaced between the two) is not this page's: the page is
	// not live. The read below must come from the directory's connection.
	lw = lw.boundTo(list.Connection)
	data := leadsData{pageData: pageData{Title: pageTitles["leads"], Nav: &navView{Current: navLeads, Leads: true, Projects: a.projectsOffered.Load()}}, Watch: lw}
	if list.StandIn {
		data.Sample = &chatTrial
	}
	var selected LeadRow
	var row chatRow
	for _, item := range list.Rows {
		entry := chatRow{Key: strings.TrimPrefix(item.Lead, "daemon:"), State: item.State, Chat: item.Chat, Here: item.Lead == lead}
		data.Rows = append(data.Rows, entry)
		if entry.Here {
			selected, row = item, entry
		}
	}
	if lead == "" {
		if a.chatLive(w, r, p, ctx, list.StandIn) {
			a.render(w, "leads", http.StatusOK, data)
		}
		return
	}
	if selected.Lead == "" {
		a.problemBanner(w, r, problemChatMissing, list.StandIn)
		return
	}
	if selected.Chat == "off" {
		a.problemBanner(w, r, problemChatOff, list.StandIn)
		return
	}
	// The chat hint is one cursor for every lead, so a page that follows
	// is read again after its last entry whenever any lead replied. A
	// cursor at the head the directory lists has nothing after it for this
	// lead (the head is the last entry's sequence, and it only grows
	// within an epoch): that page is answered as the conversation's own
	// address is, with its newest window and its follow mark, so the
	// reader keeps the conversation in view and the page goes on
	// following. The directory already told, so this adds no read.
	if explicit && after == *selected.Head {
		explicit = false
	}
	if !explicit {
		after = max(0, *selected.Head-MaxChatEntries)
	}
	// readAt reads the conversation after a cursor and checks the answer;
	// failed answers the page of an error.
	readAt := func(after int64) (ChatView, error) {
		c.Verb, c.Lead, c.After = ChatRead, lead, after
		v, err := p.Chat(ctx, c)
		if err == nil && !v.ValidFor(c) {
			err = errChatInvalid
		}
		return v, err
	}
	failed := func(err error) {
		if errors.Is(err, ErrChatUnknownLead) || errors.Is(err, ErrChatOff) {
			delete(h.byLead, lead)
		}
		a.problemBanner(w, r, chatProblem(err), list.StandIn)
	}
	read, err := readAt(after)
	if err != nil {
		failed(err)
		return
	}
	if read.Connection != list.Connection || read.StandIn != list.StandIn || read.Epoch != *selected.Epoch || read.Head < *selected.Head {
		a.problemBanner(w, r, problemChatChanged, list.StandIn)
		return
	}
	// The conversation's own address is its newest page. A recent window
	// that the gate's bound of a result cut short, or that replies arriving
	// since the directory pushed past the window, left entries out: the
	// page reads on toward the head, at most newestReads more times. A
	// busy room, or a cursor the chat no longer holds, keeps the page read
	// so far, which then offers Show later.
	for n := 0; !explicit && read.More && n < newestReads; n++ {
		next, err := readAt(newestAfter(read))
		if errors.Is(err, ErrBusy) {
			break
		}
		if err != nil {
			failed(err)
			return
		}
		if next.Connection != read.Connection || next.StandIn != read.StandIn || next.Epoch != read.Epoch || next.Head < read.Head {
			a.problemBanner(w, r, problemChatChanged, list.StandIn)
			return
		}
		if next.Reset {
			break
		}
		read, after = next, c.After
	}
	old, seen := h.byLead[lead]
	d, visit := conversation(data, row, read, after, old, seen)
	if a.chatLive(w, r, p, ctx, list.StandIn) && a.render(w, "conversation", http.StatusOK, d) {
		h.byLead[lead] = visit
	}
}

// newestReads bounds the reads a conversation's own address makes beyond
// its first to reach the newest entries: with the directory, a page is 4 of
// chat's 5 calls in 80 seconds at most.
const newestReads = 2

// errChatInvalid is an answer of the port that its call does not take.
var errChatInvalid = errors.New("seatapp: a chat answer of the wrong form")

// newestAfter is the cursor of the next read toward the newest entries, for
// a read v that left entries out (More, so v holds one at least): always a
// sequence v returned, never an inferred one, which might fall between kept
// entries. It is the earliest entry with no more sequences after it, to the
// head, than v held entries, so the next read ends at the head holding as
// many entries as v did, when they are no larger; when every entry has more
// after it, the last entry, so the next read goes on from there.
func newestAfter(v ChatView) int64 {
	n := int64(len(v.Entries))
	for _, e := range v.Entries {
		if v.Head-e.Seq <= n {
			return e.Seq
		}
	}
	return v.Entries[len(v.Entries)-1].Seq
}

func (a *app) chatLive(w http.ResponseWriter, r *http.Request, p Chat, ctx context.Context, standIn bool) bool {
	_, _, ok := a.sess.look(cookieValue(r, SessionCookie+a.cookieSuffix))
	if !ok || !p.ChatOffered() || ctx.Err() != nil {
		a.problemBanner(w, r, problemUnavailable, ok && standIn)
		return false
	}
	return true
}

// conversation makes a page of a read after the cursor after. Show earlier
// reads one window back (after MaxChatEntries entries less, from zero at
// least), Show later from the page's last entry, and Jump to newest, when
// the head lies past the page, goes to the conversation's own address, its
// newest page.
//
// The restart marker shows only when the epoch rose since the visit kept: a
// reset, or an epoch below the kept one (another lead under the key), drops
// the visit instead, and the page starts a new one.
func conversation(list leadsData, row chatRow, v ChatView, after int64, visit chatVisit, seen bool) (conversationData, chatVisit) {
	if v.Reset || visit.epoch > v.Epoch {
		seen = false
	}
	d := conversationData{leadsData: list, Selected: row, Epoch: v.Epoch, Head: v.Head, More: v.More, Reset: v.Reset, Restarted: seen && visit.epoch < v.Epoch}
	d.Title = pageTitles["conversation"]
	visit = chatVisit{epoch: v.Epoch, chat: row.Chat}
	d.HavePrev = after > 0 && !v.Reset
	if d.HavePrev {
		d.PrevAfter = max(0, after-MaxChatEntries)
	}
	for i, e := range v.Entries {
		d.Messages = append(d.Messages, chatMessage{ChatEntry: e, Last: i+1 == len(v.Entries)})
		if list.Sample == nil && e.Who == "system" {
			d.Timeline = append(d.Timeline, timelineEvent{At: e.At, Text: e.Text, Cut: e.Cut})
		}
		d.LastSeq = e.Seq
	}
	if list.Sample != nil {
		d.Timeline = list.Sample.Events
	} else {
		slices.SortStableFunc(d.Timeline, func(a, b timelineEvent) int { return a.At.Compare(b.At) })
	}
	d.JumpRemote = v.Head > d.LastSeq
	d.Watch.Conversation = true
	if !v.More {
		d.Watch.Follow, d.Watch.After = true, followAfter(v, after, d.LastSeq)
	}
	return d, visit
}

// followAfter is the cursor of the read that follows new replies on a page
// that shows the newest entries: the page's last rendered entry, a sequence
// the read returned, so the following read starts directly after it. Every
// new reply then comes first on the next page, whatever the size of the
// entries above it, and the cursor never falls between kept entries (a
// cursor the chat does not hold reads from the oldest kept entry again). A
// page with no entry rendered its read's cursor (the head at most), or,
// after a reset, the head.
func followAfter(v ChatView, after, lastSeq int64) int64 {
	if len(v.Entries) > 0 {
		return lastSeq
	}
	if v.Reset {
		return v.Head
	}
	return min(after, v.Head)
}
