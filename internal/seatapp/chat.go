package seatapp

import (
	"context"
	"errors"
	"net/http"
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
	Stamp string
	Last  bool
}
type leadsData struct {
	pageData
	StandIn bool
	Rows    []chatRow
}
type conversationData struct {
	leadsData
	Selected                                     chatRow
	Messages                                     []chatMessage
	Epoch, Head                                  int64
	Restarted, Reset, HavePrev, More, JumpRemote bool
	PrevAfter, LastSeq, TailAfter                int64
}

// A login keeps only the last displayed epoch per listed lead, to say that a
// lead restarted between two reads; the page links are computed from the
// page itself. Serialize its chat requests independently of logout, Cards
// and other logins.
type chatHistory struct {
	mu         sync.Mutex
	connection time.Time
	trial      bool
	byLead     map[string]chatVisit
}
type chatVisit struct {
	epoch int64
}

func (h *chatHistory) directory(v ChatView) {
	if h.byLead == nil || h.connection != v.Connection || h.trial != v.StandIn {
		h.byLead = make(map[string]chatVisit)
		h.connection, h.trial = v.Connection, v.StandIn
	}
	for lead := range h.byLead {
		shared := false
		for _, row := range v.Rows {
			if row.Lead == lead && row.Chat != "off" {
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
	_, gone, live := a.sess.look(cookieValue(r, SessionCookie))
	if !live || ctx.Err() != nil {
		a.problem(w, r, problemUnavailable)
		return
	}
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
	data := leadsData{pageData: pageData{Title: pageTitles["leads"], Nav: &navView{Current: navLeads, Leads: true, Projects: a.projectsOffered.Load()}}, StandIn: list.StandIn}
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
		if a.chatLive(w, r, p, ctx) {
			a.render(w, "leads", http.StatusOK, data)
		}
		return
	}
	if selected.Lead == "" {
		a.problemBanner(w, r, problemChatMissing, data.StandIn)
		return
	}
	if selected.Chat == "off" {
		a.problemBanner(w, r, problemChatOff, data.StandIn)
		return
	}
	if !explicit {
		after = max(0, *selected.Head-MaxChatEntries)
	}
	c.Verb, c.Lead, c.After = ChatRead, lead, after
	read, err := p.Chat(ctx, c)
	if err != nil {
		if errors.Is(err, ErrChatUnknownLead) || errors.Is(err, ErrChatOff) {
			delete(h.byLead, lead)
		}
		a.problemBanner(w, r, chatProblem(err), data.StandIn)
		return
	}
	if !read.ValidFor(c) {
		a.problemBanner(w, r, problemFailed, data.StandIn)
		return
	}
	if read.Connection != list.Connection || read.StandIn != list.StandIn || read.Epoch != *selected.Epoch || read.Head < *selected.Head {
		a.problemBanner(w, r, problemChatChanged, data.StandIn)
		return
	}
	old, seen := h.byLead[lead]
	d, visit := conversation(data, row, read, after, old, seen)
	if a.chatLive(w, r, p, ctx) && a.render(w, "conversation", http.StatusOK, d) {
		h.byLead[lead] = visit
	}
}

func (a *app) chatLive(w http.ResponseWriter, r *http.Request, p Chat, ctx context.Context) bool {
	_, _, ok := a.sess.look(cookieValue(r, SessionCookie))
	if !ok || !p.ChatOffered() || ctx.Err() != nil {
		a.problem(w, r, problemUnavailable)
		return false
	}
	return true
}

// conversation makes a page of a read after the cursor after. Show earlier
// reads one window back (after MaxChatEntries entries less, from zero at
// least), Show later from the page's last entry, and Jump to newest, when
// the head lies past the page, the last window (after head less
// MaxChatEntries, from zero at least).
func conversation(list leadsData, row chatRow, v ChatView, after int64, visit chatVisit, seen bool) (conversationData, chatVisit) {
	d := conversationData{leadsData: list, Selected: row, Epoch: v.Epoch, Head: v.Head, More: v.More, Reset: v.Reset, Restarted: seen && visit.epoch != v.Epoch}
	d.Title = pageTitles["conversation"]
	visit.epoch = v.Epoch
	d.HavePrev = after > 0 && !v.Reset
	if d.HavePrev {
		d.PrevAfter = max(0, after-MaxChatEntries)
	}
	for i, e := range v.Entries {
		d.Messages = append(d.Messages, chatMessage{ChatEntry: e, Stamp: e.At.UTC().Format(time.RFC3339), Last: i+1 == len(v.Entries)})
		d.LastSeq = e.Seq
	}
	d.JumpRemote = v.Head > d.LastSeq
	if d.JumpRemote {
		d.TailAfter = max(0, v.Head-MaxChatEntries)
	}
	return d, visit
}
