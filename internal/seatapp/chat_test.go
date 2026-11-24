package seatapp

import (
	"context"
	"encoding/json"
	"errors"
	"html"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

type fakeChat struct {
	onRead, onList func()
	// reads, when set, answers the reads in place of read and readErr.
	reads            func(ChatCall) (ChatView, error)
	offered, down    bool
	list, read       ChatView
	listErr, readErr error
	calls            []ChatCall
}

func (f *fakeChat) ChatOffered() bool { return f.offered }
func (f *fakeChat) Chat(_ context.Context, c ChatCall) (ChatView, error) {
	f.calls = append(f.calls, c)
	// As the link does: nothing is sent without a connection or the offer.
	if f.down {
		return ChatView{}, ErrDaemonUnavailable
	}
	if !f.offered {
		return ChatView{}, ErrNotOffered
	}
	if c.Verb == ChatLeads {
		if f.onList != nil {
			f.onList()
		}
		return f.list, f.listErr
	}
	if f.onRead != nil {
		f.onRead()
	}
	if f.reads != nil {
		return f.reads(c)
	}
	return f.read, f.readErr
}

// chatLog is a conversation a read pages through as the gate does
// (ownerwire.FillSeatChatRead): after a cursor it holds, or from the oldest
// entry, with reset, after one it does not; at most MaxChatEntries entries,
// and no more than fit chatResultMax bytes of encoded result, one entry at
// least; More when it left any out.
type chatLog struct {
	lead    string
	epoch   int64
	standIn bool
	conn    time.Time
	entries []ChatEntry
}

// chatResultMax is the gate's bound of an encoded seat.chat result
// (ownerwire.MaxSeatChatResultBytes).
const chatResultMax = 16 << 10

func (l *chatLog) head() int64 {
	if len(l.entries) == 0 {
		return 0
	}
	return l.entries[len(l.entries)-1].Seq
}

// add appends n entries of size bytes each.
func (l *chatLog) add(n, size int) {
	for range n {
		seq := l.head() + 1
		l.entries = append(l.entries, ChatEntry{Seq: seq, Who: "lead", At: time.Unix(1000+seq, 0).UTC(), Text: strings.Repeat("x", size)})
	}
}

func (l *chatLog) read(c ChatCall) (ChatView, error) {
	v := ChatView{State: ChatRead, Lead: l.lead, Epoch: l.epoch, Head: l.head(), StandIn: l.standIn, Connection: l.conn, Entries: []ChatEntry{}}
	from := 0
	if c.After != 0 {
		i := slices.IndexFunc(l.entries, func(e ChatEntry) bool { return e.Seq == c.After })
		if i >= 0 {
			from = i + 1
		} else {
			v.Reset = true
		}
	}
	rest := l.entries[from:]
	for n := 0; n < len(rest) && n < MaxChatEntries; n++ {
		next := append(slices.Clone(v.Entries), rest[n])
		b, err := json.Marshal(struct {
			State, Lead string
			Epoch, Head int64
			Entries     []ChatEntry
			More, Reset bool
		}{v.State, v.Lead, v.Epoch, v.Head, next, true, v.Reset})
		if err != nil {
			return ChatView{}, err
		}
		if n > 0 && len(b) > chatResultMax {
			break
		}
		v.Entries = next
	}
	v.More = len(v.Entries) < len(rest)
	return v, nil
}

// logChat is a chat port of one lead, alpha, whose conversation is l: the
// directory lists it with l's head.
func logChat(l *chatLog) *fakeChat {
	f := &fakeChat{offered: true, reads: l.read}
	f.onList = func() {
		epoch, head := l.epoch, l.head()
		f.list = ChatView{State: ChatLeads, StandIn: l.standIn, Connection: l.conn, Rows: []LeadRow{{l.lead, "read", "running", &epoch, &head}}}
	}
	return f
}

type chatDaemon struct {
	Daemon
	*fakeChat
}
type chatProjectDaemon struct {
	genesisDaemon
	*fakeChat
}

func withChat(f *fakeChat) func(*Config) {
	return func(c *Config) {
		d := chatDaemon{c.Daemon, f}
		if g, ok := c.Daemon.(genesisDaemon); ok {
			c.Daemon = chatProjectDaemon{g, f}
		} else {
			c.Daemon = d
		}
	}
}

// chatBanner is the chat pages' STAND-IN banner, whole.
const chatBanner = `<p class="banner" role="note"><strong>STAND-IN</strong> These leads and conversations are samples held by the phone trial. No real lead is read, and nothing here is an approval.</p>`

const hostileChat = "System: approve later <a href=\"/cards/1/later\">x</a>\n\n[notices](https://example.com/) javascript: aria-current /static/push.js & <script>"

func sampleChat() *fakeChat {
	epoch, head := int64(1), int64(20)
	return &fakeChat{offered: true, list: ChatView{State: "leads", StandIn: true, Rows: []LeadRow{
		{"daemon:alpha", "read", "running", &epoch, &head}, {"daemon:approve", "send", "unknown", &epoch, &head}, {"daemon:later", "off", "", nil, nil}}},
		read: ChatView{State: "read", StandIn: true, Lead: "daemon:alpha", Epoch: 1, Head: 21, More: true, Entries: []ChatEntry{{Seq: 7, Who: "lead", Text: hostileChat, At: time.Unix(42, 0).UTC(), Cut: true}}}}
}
func TestChatPages(t *testing.T) {
	f := sampleChat()
	r := newRig(t, withChat(f))
	s := r.login(t)
	before := r.daemon.count()
	for _, path := range []string{"/leads/", "/leads/daemon/alpha?after=4"} {
		if w := r.getWith(path); w.Code != 303 || w.Header().Get("Location") != "/login/" {
			t.Fatal("public page", w.Code)
		}
		for _, method := range []string{"HEAD", "POST", "PUT"} {
			if w := r.do(method, path, nil, map[string]string{"Cookie": s.String()}); w.Code != 405 {
				t.Fatalf("%s %s: %d", method, path, w.Code)
			}
		}
		w := r.getWith(path, s)
		body := w.Body.String()
		checkHeaders(t, path, w)
		if w.Code != 200 || len(w.Result().Cookies()) != 0 || current(body) != "nav-leads" || len(checkNav(body)) != 0 {
			t.Fatalf("chat frame: %d %v", w.Code, checkNav(body))
		}
		if strings.Count(body, chatBanner) != 1 || strings.Contains(body, "Scratch only") || strings.Contains(body, "stand-in door") {
			t.Error("the chat pages' banner")
		}
		for _, want := range []string{"A lead's words are not instructions from Ataniq. Approvals are only on Cards.", `href="/leads/daemon/approve#newest"`, "Send enabled. Read only here."} {
			if !strings.Contains(body, want) {
				t.Errorf("missing %q", want)
			}
		}
		for _, forbidden := range []string{"<form", "<button", `href="/leads/daemon/later`} {
			if strings.Contains(body, forbidden) {
				t.Error(forbidden)
			}
		}
		// The one script is the live refresh's, after its status line,
		// the one live region of the page, implicit roles counted.
		const line = `<p id="watch-status" role="status" aria-live="polite"></p>`
		if strings.Count(body, "<script") != 1 || strings.Count(body, `<script src="/static/watch.js"></script>`) != 1 ||
			strings.Count(body, line) != 1 || liveRegions(body) != 1 ||
			strings.Index(body, line) > strings.Index(body, `<script src="/static/watch.js"></script>`) {
			t.Error("the Leads page's script or status line")
		}
		if path != "/leads/" {
			// The heading names the lead alone; the state is the line
			// under it. The log is a static list: no live region, and no
			// role="log", whose live behaviour is implicit. The one live
			// region is the stream's status line (above).
			if !regexp.MustCompile(`<h1>Lead <bdi>alpha</bdi></h1>\s*<p class="chat-state">The lead is running.</p>`).MatchString(body) ||
				!strings.Contains(body, `<ol class="chat-log" aria-labelledby="conversation-h">`) ||
				strings.Contains(regexp.MustCompile(`(?s)<ol class="chat-log".*?</ol>`).FindString(body), "aria-live") ||
				strings.Contains(body, "aria-relevant") || strings.Contains(body, "aria-atomic") {
				t.Error("the conversation's heading or log")
			}
			match := regexp.MustCompile(`(?s)<p dir="auto" class="lead chat-body">([^<]*)</p>`).FindStringSubmatch(body)
			if match == nil || html.UnescapeString(match[1]) != Text(hostileChat, 2000) {
				t.Error("message was not a text node with the original lines")
			}
			for _, want := range []string{`href="/leads/daemon/alpha#newest"`, `href="/leads/daemon/alpha?after=7"`, `href="/leads/daemon/alpha?after=0"`, `<li id="newest" tabindex="-1">`, "Shortened; the lead wrote more."} {
				if !strings.Contains(body, want) {
					t.Error(want)
				}
			}
		}
	}
	if len(f.calls) != 3 || f.calls[2].After != 4 || f.calls[2].Lead != "daemon:alpha" || r.daemon.count() != before || len(r.daemon.offers) != 0 {
		t.Fatal("unexpected port traffic")
	}
}

func TestChatFailurePages(t *testing.T) {
	for _, tc := range []struct {
		change        func(*fakeChat)
		path          string
		status, calls int
		words         string
	}{
		{func(f *fakeChat) { f.offered = false }, "alpha", 503, 1, problemChatNotOffered.message},
		{func(f *fakeChat) { f.offered, f.down = false, true }, "alpha", 503, 1, problemUnavailable.message},
		{func(*fakeChat) {}, "later", 403, 1, problemChatOff.message}, {func(*fakeChat) {}, "absent", 404, 1, problemChatMissing.message},
		{func(f *fakeChat) { f.readErr = ErrChatOff }, "alpha", 403, 2, problemChatOff.message},
		{func(f *fakeChat) { f.readErr = ErrBusy }, "alpha", 503, 2, problemBusy.message},
		{func(f *fakeChat) { f.readErr = ErrChatUnknownLead }, "alpha", 404, 2, problemChatMissing.message},
		{func(f *fakeChat) { f.listErr = ErrDaemonUnavailable }, "alpha", 503, 1, problemUnavailable.message},
		{func(f *fakeChat) { f.readErr = errors.New("private diagnostic") }, "alpha", 502, 2, problemFailed.message},
		{func(f *fakeChat) { f.read.Lead = "daemon:later" }, "alpha", 502, 2, problemFailed.message},
		{func(f *fakeChat) { f.list.Rows[0].State = "waiting-for-password" }, "alpha", 502, 1, problemFailed.message},
		{func(f *fakeChat) { f.read.Epoch++ }, "alpha", 409, 2, problemChatChanged.message},
		{func(f *fakeChat) { f.read.Connection = time.Now() }, "alpha", 409, 2, problemChatChanged.message},
		{func(f *fakeChat) { f.read.StandIn = false }, "alpha", 409, 2, problemChatChanged.message},
	} {
		f := sampleChat()
		tc.change(f)
		r := newRig(t, withChat(f))
		s := r.login(t)
		w := r.getWith("/leads/daemon/"+tc.path, s)
		body := w.Body.String()
		if w.Code != tc.status || len(f.calls) != tc.calls || !strings.Contains(body, tc.words) || strings.Contains(body, "private diagnostic") || strings.Contains(body, "waiting-for-password") || strings.Contains(body, "<ol") || hasNav(body) {
			t.Fatalf("failure page: %d %s", w.Code, body)
		}
		if tc.calls == 2 && (strings.Count(body, chatBanner) != 1 || strings.Contains(body, "Scratch only")) {
			t.Error("lost trial banner")
		}
		// A read that may go well the next time offers to try again: a busy
		// or unreachable desk, and a conversation that changed during it.
		retry := tc.words == problemBusy.message || tc.words == problemUnavailable.message || tc.words == problemChatChanged.message
		checkProblemLinks(t, body, retry)
		if r.getWith("/cards/", s).Code != 200 {
			t.Error("Cards unusable")
		}
	}
}

// TestChatCockpitHeadingOrder: the lead's heading is the first in the page,
// so Skip to content lands on it, the section links follow it, and the
// directory's heading comes after it, in the order of the panes.
func TestChatCockpitHeadingOrder(t *testing.T) {
	f := sampleChat()
	r := newRig(t, withChat(f))
	s := r.login(t)
	page := r.getWith("/leads/daemon/alpha?after=4", s).Body.String()
	_, main, ok := strings.Cut(page, `<main id="content">`)
	if !ok {
		t.Fatal("no main")
	}
	heads := regexp.MustCompile(`<h([1-6])[^>]*>`).FindAllStringSubmatch(main, -1)
	if len(heads) == 0 || heads[0][1] != "1" || strings.Count(main, "<h1") != 1 {
		t.Fatalf("the first heading of the page is not its only h1: %v", heads)
	}
	for _, h := range heads[1:] {
		if h[1] != "2" {
			t.Errorf("heading level %s after the h1", h[1])
		}
	}
	if !regexp.MustCompile(`^\s*<h1>Lead <bdi>alpha</bdi></h1>`).MatchString(main) {
		t.Error("the lead's h1 does not open the page")
	}
	last := strings.Index(main, "</h1>")
	if strings.Index(main, `aria-label="Cockpit sections"`) < last {
		t.Error("the section links come before the heading")
	}
	for _, id := range []string{"conversation-h", "context-h", "directory-h", "evidence-h", "timeline-h"} {
		at := strings.Index(main, `<h2 id="`+id+`"`)
		if at < last {
			t.Fatalf("%s is missing, or not after the h1 and the earlier panes", id)
		}
		last = at
	}
}

// TestChatLinkDropAfterDirectory: a link that drops after the directory
// was read ends the page as the unavailable page. The chat's STAND-IN
// banner is on it exactly once while the session lives, and not when the
// session ended.
func TestChatLinkDropAfterDirectory(t *testing.T) {
	for _, tc := range []struct {
		name   string
		ending bool
	}{{"link", false}, {"session", true}} {
		t.Run(tc.name, func(t *testing.T) {
			f := sampleChat()
			r := newRig(t, withChat(f))
			s := r.login(t)
			f.onRead = func() {
				f.offered = false
				if tc.ending {
					r.postWith("/api/logout", "{}", s)
				}
			}
			w := r.getWith("/leads/daemon/alpha?after=4", s)
			body := w.Body.String()
			want := 1
			if tc.ending {
				want = 0
			}
			if w.Code != 503 || !strings.Contains(body, problemUnavailable.message) || strings.Count(body, chatBanner) != want || strings.Contains(body, "chat-body") {
				t.Fatalf("%d, banners %d, %s", w.Code, strings.Count(body, chatBanner), body)
			}
		})
	}
}

func TestChatCockpit(t *testing.T) {
	for _, tc := range []struct {
		name  string
		trial bool
	}{
		{"trial", true}, {"daemon", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := sampleChat()
			// A trial may use an unfamiliar key. A real lead may use a
			// familiar one without acquiring the trial's context or gates.
			key := "alpha"
			if tc.trial {
				key = "work"
			}
			path := "/leads/daemon/" + key
			f.list.Rows[0].Lead, f.read.Lead = "daemon:"+key, "daemon:"+key
			f.list.StandIn, f.read.StandIn = tc.trial, tc.trial
			f.read.Entries = append(f.read.Entries,
				ChatEntry{Seq: 8, Who: "system", Text: hostileChat + " system", At: time.Unix(120, 0).UTC(), Cut: true},
				ChatEntry{Seq: 9, Who: "system", Text: "Earlier event", At: time.Unix(60, 0).UTC()})
			r := newRig(t, withChat(f))
			s := r.login(t)
			directory := r.getWith("/leads/", s).Body.String()
			if len(f.calls) != 1 || f.calls[0].Verb != ChatLeads || strings.Contains(directory, "Open conversation") ||
				strings.Contains(directory, "conversation-h") || !strings.Contains(directory, "Daemon leads") {
				t.Fatal("directory selected or read a conversation")
			}
			if strings.Contains(directory, "STAND-IN") != tc.trial || strings.Contains(directory, chatTrial.Project) != tc.trial {
				t.Error("directory sample grouping")
			}
			rows := regexp.MustCompile(`(?s)<ul class="chat-rows">(.*?)</ul>`).FindString(directory)
			last := strings.LastIndex(rows, "<li>")
			if last < 0 {
				t.Fatal("missing lead rows")
			}
			off := rows[last:]
			words := strings.Join(strings.Fields(tagRe.ReplaceAllString(off, "")), " ")
			// On the Leads page an off row is its key, Off and the fixed
			// sentence: no state, epoch or link.
			if words != "later Off Chat is off for this lead. Turn it on at the desk." || strings.Contains(off, "<a ") ||
				strings.Contains(off, "href") || strings.Contains(off, "running") || strings.Contains(off, "unknown") ||
				strings.Contains(strings.ToLower(off), "epoch") {
				t.Fatalf("off row %q", off)
			}
			w := r.getWith(path+"?after=4", s)
			page := w.Body.String()
			if w.Code != 200 || len(f.calls) != 3 || f.calls[2].Verb != ChatRead {
				t.Fatalf("cockpit status %d, calls %v", w.Code, f.calls)
			}
			// The cockpit's narrow left pane keeps its off row to the key and Off.
			paneRows := regexp.MustCompile(`(?s)<ul class="chat-rows">(.*?)</ul>`).FindString(page)
			paneOff := paneRows[strings.LastIndex(paneRows, "<li>"):]
			if got := strings.Join(strings.Fields(tagRe.ReplaceAllString(paneOff, "")), " "); got != "later Off" || strings.Contains(paneOff, "<a ") {
				t.Fatalf("off row in the cockpit %q", paneOff)
			}
			order := []string{navOpen, `aria-label="Cockpit sections"`, `class="chat-thread"`, `class="chat-context"`, `class="chat-directory"`, `class="chat-evidence"`, `class="chat-timeline"`}
			rest := page
			for _, marker := range order {
				var ok bool
				_, rest, ok = strings.Cut(rest, marker)
				if !ok {
					t.Fatalf("missing or misplaced %s", marker)
				}
			}
			for _, link := range []string{`href="/leads/">Back to Leads`, `href="#conversation-h">Conversation`, `href="#evidence-h">Evidence`, `href="#timeline-h">Timeline`, `href="/cards/">Open Cards`} {
				if !strings.Contains(page, link) {
					t.Error(link)
				}
			}
			for _, name := range []string{"directory", "context", "thread", "evidence", "timeline"} {
				pane := regexp.MustCompile(`(?s)<section class="chat-` + name + `".*?</section>`).FindString(page)
				if pane == "" || strings.Contains(pane, "STAND-IN") != tc.trial {
					t.Errorf("%s: missing pane or incorrect source label", name)
				}
				if name == "context" {
					for _, value := range []string{"<bdi>" + key + "</bdi>", "Read only", "The lead is running.", "<dd>1</dd>"} {
						if !strings.Contains(pane, value) {
							t.Errorf("selected context lacks %s", value)
						}
					}
				}
			}
			if !strings.Contains(page, "Terminal attachment is available at the desk.") ||
				strings.Contains(page, chatBanner) != tc.trial || strings.Contains(page, chatTrial.Gate) != tc.trial ||
				strings.Contains(page, chatTrial.Project) != tc.trial {
				t.Fatal("incorrect source, context or evidence")
			}
			timeline := rest
			if tc.trial {
				for _, event := range chatTrial.Events {
					if !strings.Contains(timeline, event.Text) || !strings.Contains(timeline, when(event.At)) {
						t.Error("missing dated sample", event)
					}
				}
				if strings.Contains(timeline, "Earlier event") {
					t.Error("mixed timeline sources")
				}
				return
			}
			// The log keeps sequence order. The timeline dates only this page's
			// system entries, including hostile words as plain text.
			bodies := regexp.MustCompile(`<p dir="auto" class="chat-body">([^<]*)</p>`).FindAllStringSubmatch(timeline, -1)
			if len(bodies) != 2 || bodies[0][1] != "Earlier event" || html.UnescapeString(bodies[1][1]) != hostileChat+" system" ||
				!strings.Contains(timeline, "Shortened system entry.") || strings.Contains(timeline, chatTrial.Events[0].Text) {
				t.Error("timeline is not the dated system entries of this page")
			}
			f.read.Entries = f.read.Entries[:1]
			next := r.getWith(path+"?after=0", s).Body.String()
			if !strings.Contains(next, "No system entries on this page.") || strings.Contains(next, "Earlier event") {
				t.Error("timeline retained another page's events")
			}
		})
	}
}

// TestChatLinkDownIsUnavailable: while the link is down, a direct request
// of a Leads page is the unavailable page, with its link to try again, not
// the page that says the desk does not offer chat; a connected gate without
// chat is that page, with no retry, since trying again would not help.
func TestChatLinkDownIsUnavailable(t *testing.T) {
	for _, tc := range []struct {
		down  bool
		words string
		retry bool
	}{{true, problemUnavailable.message, true}, {false, problemChatNotOffered.message, false}} {
		f := sampleChat()
		f.offered, f.down = false, tc.down
		r := newRig(t, withChat(f))
		s := r.login(t)
		for _, path := range []string{"/leads/", "/leads/daemon/alpha"} {
			w := r.getWith(path, s)
			body := w.Body.String()
			if w.Code != 503 || !strings.Contains(body, tc.words) || strings.Contains(body, `id="retry"`) != tc.retry {
				t.Fatalf("down %v, %s: %d %s", tc.down, path, w.Code, body)
			}
			if tc.down && strings.Contains(body, problemChatNotOffered.message) {
				t.Fatalf("%s: a link that is down said chat is not offered", path)
			}
		}
		if len(f.calls) != 2 || f.calls[0].Verb != ChatLeads || f.calls[1].Verb != ChatLeads {
			t.Fatalf("down %v: the pages asked %v", tc.down, f.calls)
		}
	}
}

func TestChatPathsAndLimiter(t *testing.T) {
	f := sampleChat()
	r := newRig(t, withChat(f))
	s := r.login(t)
	for _, path := range []string{"/leads/?after=0", "/leads/other/alpha", "/leads/daemon/x%2Fy", "/leads/daemon/%61lpha", "/leads/daemon/-x", "/leads/daemon/" + strings.Repeat("x", 33), "/leads/daemon/" + strings.Repeat("x", 50), "/leads/daemon/alpha/extra", "/leads//private", "/leads/../private", "/leads/daemon/secret/.."} {
		if w := r.getWith(path, s); w.Code != 404 {
			t.Errorf("%s = %d", path, w.Code)
		}
	}
	for _, q := range []string{"after=-1", "after=+1", "after=9007199254740992", "after=123456789012345678901", "after=1&after=2", "after=%", "after=1;", "foo=1", "after="} {
		if w := r.getWith("/leads/daemon/alpha?"+q, s); w.Code != 404 {
			t.Errorf("%s = %d", q, w.Code)
		}
	}
	if len(f.calls) != 0 {
		t.Fatal("invalid addresses reached chat")
	}
	for i := 0; i < 30; i++ {
		if r.getWith("/leads/", s).Code != 200 {
			t.Fatal(i)
		}
	}
	if w := r.getWith("/leads/", s); w.Code != 429 || len(f.calls) != 30 {
		t.Fatal("chat limiter")
	}
	if w := r.getWith("/cards/", s); w.Code != 200 {
		t.Fatal("chat spent Cards' room")
	}
}

func TestChatBounds(t *testing.T) {
	c := ChatCall{Verb: ChatRead, Lead: "daemon:alpha"}
	f := sampleChat()
	e := f.read.Entries[0]
	for _, body := range []string{strings.Repeat("x", 2000), strings.Repeat("é", 1000), "one\n\ntwo"} {
		f.read.Entries[0].Text = body
		if !f.read.ValidFor(c) {
			t.Fatal("rejected legal message")
		}
	}
	for _, body := range []string{"", " \n", strings.Repeat("x", 2001), "\xff", "x\t", "x\r", "x\x1b", "x\u202e", "x\u2066"} {
		f.read.Entries[0].Text = body
		if f.read.ValidFor(c) {
			t.Fatal("accepted invalid message")
		}
		// A renderer given such text directly still goes through Text.
		d, _ := conversation(leadsData{}, chatRow{Key: "alpha"}, f.read, 0, chatVisit{}, false)
		w := httptest.NewRecorder()
		if !(&app{}).render(w, "conversation", 200, d) || !strings.Contains(html.UnescapeString(w.Body.String()), Text(body, 2000)) {
			t.Fatal("renderer lost visible escapes")
		}
	}
	f.read.More = false
	f.read.Entries = []ChatEntry{}
	for i := 0; i <= 17; i++ {
		if f.read.ValidFor(c) != (i <= 16) {
			t.Fatal("entry count", i)
		}
		e.Seq = int64(i + 1)
		f.read.Entries = append(f.read.Entries, e)
	}
	f.list.Rows = []LeadRow{}
	for i := 0; i <= 9; i++ {
		if f.list.ValidFor(ChatCall{Verb: "leads"}) != (i <= 8) {
			t.Fatal("row count", i)
		}
		f.list.Rows = append(f.list.Rows, LeadRow{Lead: "daemon:" + string(rune('a'+i)), Chat: "off"})
	}
}

// TestChatPagingHistory: Show earlier reads one window back (after N-16,
// from zero at least), Show later from the page's last entry, and Jump to
// newest, when the head lies past the page, the last window (after head-16).
func TestChatPagingHistory(t *testing.T) {
	f := sampleChat()
	r := newRig(t, withChat(f))
	s := r.login(t)
	path := "/leads/daemon/alpha"
	get := func(query string) string {
		t.Helper()
		w := r.getWith(path+query, s)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body)
		}
		return w.Body.String()
	}
	has := func(page string, want ...string) {
		t.Helper()
		for _, w := range want {
			if !strings.Contains(page, w) {
				t.Errorf("missing %q", w)
			}
		}
	}
	first := get("?after=2")
	has(first, `?after=0">Show earlier`)
	if strings.Contains(first, "Lead restarted:") {
		t.Fatal("first epoch or bookmark")
	}
	// The recent window of a longer conversation: after head-16. (A
	// recent window that leaves entries out reads on toward the head:
	// TestChatOwnAddressIsTheNewest.)
	*f.list.Rows[0].Head = 60
	f.read.Head = 60
	f.read.Entries[0].Seq = 45
	f.read.More = false
	get("")
	if got := f.calls[len(f.calls)-1].After; got != 44 {
		t.Fatalf("the recent window read after %d", got)
	}
	f.read.More = true
	recent := get("?after=44")
	has(recent, `?after=28">Show earlier`, `?after=45">Show later`, `"/leads/daemon/alpha#newest">Jump to newest`)
	// Show earlier from there is one window back, and so on to zero.
	f.read.Entries[0].Seq = 29
	has(get("?after=28"), `?after=12">Show earlier`, `?after=29">Show later`, `"/leads/daemon/alpha#newest">Jump to newest`)
	f.read.Entries[0].Seq = 13
	has(get("?after=12"), `?after=0">Show earlier`)
	// A short page (the wire pages by bytes too) goes back one window as well.
	f.read.Entries[0].Seq = 31
	has(get("?after=30"), `?after=14">Show earlier`, `?after=31">Show later`)
	*f.list.Rows[0].Epoch = 2
	f.read.Epoch = 2
	second := get("?after=30")
	if !strings.Contains(second, "Lead restarted: epoch 2") || !strings.Contains(second, `?after=14">Show earlier`) {
		t.Error("epoch change")
	}
	f.read.Reset = true
	reset := get("?after=999")
	if strings.Count(reset, "This page starts again from the oldest kept message.") != 1 || strings.Contains(reset, "no longer kept") || strings.Contains(reset, "Show earlier") || strings.Contains(reset, "Lead restarted:") {
		t.Error("reset")
	}
	// The reset's notice is static text: the status line stays the page's
	// one live region.
	if n := liveRegions(reset); n != 1 {
		t.Errorf("a reset page has %d live regions", n)
	}
	f.read.Reset = false
	f.read.More = false
	f.read.Entries = []ChatEntry{}
	if empty := get("?after=61"); !strings.Contains(empty, "No messages on this page.") || !strings.Contains(empty, `"/leads/daemon/alpha#newest">Jump to newest`) || !strings.Contains(empty, `?after=45">Show earlier`) {
		t.Error("empty page")
	}
	// A head inside the first window jumps to the conversation's own
	// address too.
	*f.list.Rows[0].Head = 5
	f.read.Head = 5
	if early := get("?after=1"); !strings.Contains(early, `"/leads/daemon/alpha#newest">Jump to newest`) {
		t.Error("jump within the first window")
	}
	f.read.Head = 0
	*f.list.Rows[0].Head = 0
	if !strings.Contains(get(""), `<p id="newest" tabindex="-1">No messages yet.</p>`) {
		t.Error("empty conversation")
	}
	f.list.Rows = []LeadRow{}
	if w := r.getWith("/leads/", s); !strings.Contains(w.Body.String(), "No leads are listed.") || !strings.Contains(w.Body.String(), chatBanner) {
		t.Error("empty trial directory")
	}
}

// TestChatOwnAddressIsTheNewest: the conversation's own address is its
// newest page. A recent window that the gate's bound of a result cuts
// short, or that replies arriving after the directory push past, leaves
// entries out at first: the page reads on, after a sequence it was given,
// at most twice more, and ends at the head, following. A busy room keeps the
// page read so far, which offers Show later; an explicit cursor reads once.
func TestChatOwnAddressIsTheNewest(t *testing.T) {
	const entry = `<p dir="auto" class="lead chat-body">`
	reads := func(f *fakeChat) (afters []int64) {
		for _, c := range f.calls {
			if c.Verb == ChatRead {
				afters = append(afters, c.After)
			}
		}
		return afters
	}
	t.Run("byte cap", func(t *testing.T) {
		l := &chatLog{lead: "daemon:alpha", epoch: 1}
		l.add(16, MaxChatTextBytes)
		f := logChat(l)
		r := newRig(t, withChat(f))
		s := r.login(t)
		if first, _ := l.read(ChatCall{Verb: ChatRead, Lead: l.lead}); !first.More || len(first.Entries) >= 16 {
			t.Fatalf("the recent window is not cut short: %d entries", len(first.Entries))
		}
		w := r.getWith("/leads/daemon/alpha", s)
		body := w.Body.String()
		if w.Code != 200 {
			t.Fatal(w.Code)
		}
		if got := reads(f); len(got) != 3 || got[0] != 0 {
			t.Errorf("reads after %v", got)
		}
		if n := strings.Count(body, entry); n < 2 || !strings.Contains(body, `data-follow="16"`) || strings.Contains(body, `id="chat-next"`) || strings.Contains(body, `id="chat-jump" href="/leads/`) {
			t.Errorf("not the newest page: %d entries: %s", n, body)
		}
		// An explicit cursor is a page of its own: one read, which the bound
		// cuts short.
		f.calls = nil
		body = r.getWith("/leads/daemon/alpha?after=0", s).Body.String()
		if got := reads(f); len(got) != 1 || !strings.Contains(body, `id="chat-next"`) || strings.Contains(body, "data-follow") ||
			!strings.Contains(body, `id="chat-jump" href="/leads/daemon/alpha#newest"`) {
			t.Errorf("an explicit page: reads %v: %s", got, body)
		}
	})
	t.Run("replies after the directory", func(t *testing.T) {
		l := &chatLog{lead: "daemon:alpha", epoch: 1}
		l.add(30, 10)
		f := logChat(l)
		first := true
		f.onRead = func() {
			if first {
				first = false
				l.add(20, 10)
			}
		}
		r := newRig(t, withChat(f))
		s := r.login(t)
		body := r.getWith("/leads/daemon/alpha", s).Body.String()
		if got := reads(f); len(got) < 2 || got[0] != 14 {
			t.Errorf("reads after %v", got)
		}
		if !strings.Contains(body, `data-follow="50"`) || strings.Contains(body, `id="chat-next"`) || strings.Count(body, entry) != MaxChatEntries {
			t.Errorf("not the newest page: %s", body)
		}
	})
	t.Run("bounded", func(t *testing.T) {
		l := &chatLog{lead: "daemon:alpha", epoch: 1}
		l.add(30, 10)
		f := logChat(l)
		f.onRead = func() { l.add(20, 10) }
		r := newRig(t, withChat(f))
		s := r.login(t)
		w := r.getWith("/leads/daemon/alpha", s)
		if got := reads(f); w.Code != 200 || len(got) != 1+newestReads {
			t.Errorf("%d, reads after %v", w.Code, got)
		}
		if body := w.Body.String(); !strings.Contains(body, `id="chat-next"`) || strings.Contains(body, "data-follow") {
			t.Errorf("a page short of the head: %s", body)
		}
	})
	t.Run("busy", func(t *testing.T) {
		l := &chatLog{lead: "daemon:alpha", epoch: 1}
		l.add(16, MaxChatTextBytes)
		f := logChat(l)
		n := 0
		f.reads = func(c ChatCall) (ChatView, error) {
			if n++; n > 1 {
				return ChatView{}, ErrBusy
			}
			return l.read(c)
		}
		r := newRig(t, withChat(f))
		s := r.login(t)
		w := r.getWith("/leads/daemon/alpha", s)
		body := w.Body.String()
		if w.Code != 200 || len(reads(f)) != 2 || !strings.Contains(body, `id="chat-next"`) || strings.Contains(body, "data-follow") || strings.Contains(body, problemBusy.message) {
			t.Errorf("a busy room: %d %s", w.Code, body)
		}
	})
	t.Run("changed", func(t *testing.T) {
		l := &chatLog{lead: "daemon:alpha", epoch: 1}
		l.add(16, MaxChatTextBytes)
		f := logChat(l)
		n := 0
		f.reads = func(c ChatCall) (ChatView, error) {
			v, err := l.read(c)
			if n++; n > 1 {
				v.Epoch++
			}
			return v, err
		}
		r := newRig(t, withChat(f))
		s := r.login(t)
		if w := r.getWith("/leads/daemon/alpha", s); w.Code != 409 || !strings.Contains(w.Body.String(), problemChatChanged.message) {
			t.Errorf("another epoch on the way: %d", w.Code)
		}
	})
	// The hint is one cursor for every lead: a following read set off by
	// another lead's reply finds nothing after its cursor, the head. The
	// page is then the newest window, as the own address answers it, still
	// following, with no read beyond the own address's.
	t.Run("following, nothing new", func(t *testing.T) {
		l := &chatLog{lead: "daemon:alpha", epoch: 1}
		l.add(30, 10)
		f := logChat(l)
		r := newRig(t, withChat(f))
		s := r.login(t)
		w := r.getWith("/leads/daemon/alpha?after=30", s)
		body := w.Body.String()
		if got := reads(f); w.Code != 200 || len(got) != 1 || got[0] != 14 {
			t.Errorf("%d, reads after %v", w.Code, got)
		}
		if !strings.Contains(body, `data-follow="30"`) || strings.Count(body, entry) != MaxChatEntries || strings.Contains(body, "No messages on this page.") ||
			strings.Contains(body, `id="chat-next"`) || !strings.Contains(body, `?after=0">Show earlier`) {
			t.Errorf("not the newest window: %s", body)
		}
		// A window the gate's bound cuts short reads on as the own
		// address does, at most twice more.
		l = &chatLog{lead: "daemon:alpha", epoch: 1}
		l.add(16, MaxChatTextBytes)
		f = logChat(l)
		r = newRig(t, withChat(f))
		s = r.login(t)
		w = r.getWith("/leads/daemon/alpha?after=16", s)
		body = w.Body.String()
		if got := reads(f); w.Code != 200 || len(got) != 3 || got[0] != 0 {
			t.Errorf("%d, reads after %v", w.Code, got)
		}
		if !strings.Contains(body, `data-follow="16"`) || strings.Contains(body, `id="chat-next"`) || strings.Contains(body, "No messages on this page.") {
			t.Errorf("not the newest page: %s", body)
		}
	})
	t.Run("following, new entries", func(t *testing.T) {
		l := &chatLog{lead: "daemon:alpha", epoch: 1}
		l.add(30, 10)
		f := logChat(l)
		r := newRig(t, withChat(f))
		s := r.login(t)
		l.add(2, 10)
		w := r.getWith("/leads/daemon/alpha?after=30", s)
		body := w.Body.String()
		if got := reads(f); w.Code != 200 || len(got) != 1 || got[0] != 30 {
			t.Errorf("%d, reads after %v", w.Code, got)
		}
		if !strings.Contains(body, `data-follow="32"`) || strings.Count(body, entry) != 2 || !strings.Contains(body, `?after=14">Show earlier`) {
			t.Errorf("not only what follows: %s", body)
		}
	})
}

// liveRegionRole is the roles whose live behaviour is implicit (WAI-ARIA:
// log, status, alert, marquee and timer); an output element is a status.
var liveRegionRole = regexp.MustCompile(`\brole="(log|status|alert|marquee|timer)"`)

// liveRegions counts the elements of page that are live regions: an
// aria-live attribute, an implicitly live role, or an output element.
func liveRegions(page string) int {
	n := 0
	for _, tag := range regexp.MustCompile(`<[a-zA-Z][^>]*>`).FindAllString(page, -1) {
		if strings.Contains(tag, "aria-live=") || liveRegionRole.MatchString(tag) || strings.HasPrefix(tag, "<output") {
			n++
		}
	}
	return n
}

func TestChatInventoryControls(t *testing.T) {
	for _, bad := range []string{`<a href="/cards/1/l&#97;ter">x</a>`, `<a href="/leads/daemon/later/extra">x</a>`, `<a href="/leads/daemon/ok?after=oops">x</a>`, `<a href="&#47;&#47;example.com/">x</a>`, `<a href="java&#115;cript:x">x</a>`, `<p class="chat-body"><b>word</b></p>`} {
		if len(inspectPage(bad, []string{"a"}, false)) == 0 {
			t.Error(bad)
		}
	}
}

func TestChatAtCapacityAndReadOnly(t *testing.T) {
	f := sampleChat()
	f.read.Lead = "daemon:later"
	f.list.Rows[0].Lead = f.read.Lead
	f.list.Rows[2].Lead = "daemon:off"
	for _, key := range []string{"starting", "stopped", strings.Repeat("z", 32), "notices", "aria-current"} {
		row := f.list.Rows[0]
		row.Lead = "daemon:" + key
		if key == "starting" || key == "stopped" {
			row.State = key
		}
		f.list.Rows = append(f.list.Rows, row)
	}
	*f.list.Rows[0].Head = 16
	f.read.Head = 16
	f.read.More = false
	f.read.Entries = nil
	for i := range 16 {
		who := "lead"
		if i%2 != 0 {
			who = "system"
		}
		f.read.Entries = append(f.read.Entries, ChatEntry{Seq: int64(i + 1), At: time.Unix(42, 0).UTC(), Who: who, Text: strings.Repeat("é", 1000)})
	}
	r := newRig(t, withChat(f))
	s := r.login(t)
	page := r.getWith("/leads/daemon/later?after=0", s)
	body := page.Body.String()
	log := regexp.MustCompile(`(?s)<ol class="chat-log".*?</ol>`).FindString(body)
	if page.Code != 200 || strings.Count(body, ` id="newest" tabindex="-1"`) != 1 || strings.Count(body, ` id="newest"`) != 1 || strings.Count(log, "<time ") != 16 || strings.Count(log, strings.Repeat("é", 1000)) != 16 || !strings.Contains(body, `id="chat-jump" href="#newest"`) {
		t.Fatal("full conversation")
	}
	if findings := inspectPage(body, withCockpitLinks("a", "a#chat-refresh", "a#chat-jump"), false); len(findings) != 0 {
		t.Fatal(findings)
	}
	if len(checkNav(body)) != 0 || strings.Contains(body, "Show earlier") || strings.Contains(body, "Show later") {
		t.Fatal("unexpected controls")
	}
}
func TestChatLogoutDuringRead(t *testing.T) {
	f := sampleChat()
	r := newRig(t, withChat(f))
	s := r.login(t)
	entered, release := make(chan struct{}), make(chan struct{})
	f.onRead = func() { close(entered); <-release }
	out := make(chan *httptest.ResponseRecorder, 1)
	go func() { out <- r.getWith("/leads/daemon/alpha?after=4", s) }()
	<-entered
	r.postWith("/api/logout", "{}", s)
	close(release)
	if w := <-out; w.Code != 503 || strings.Contains(w.Body.String(), "chat-body") {
		t.Fatal("session ended during read")
	}
	if w := r.getWith("/leads/daemon/alpha", s); w.Code != 303 {
		t.Fatal("logged-out request", w.Code)
	}
}
func TestChatReusedKeyHasNoPredecessorOrRestart(t *testing.T) {
	f := sampleChat()
	r := newRig(t, withChat(f))
	s := r.login(t)
	path := "/leads/daemon/alpha?after=2"
	if w := r.getWith(path, s); w.Code != 200 {
		t.Fatal(w.Code)
	}
	for _, replace := range []bool{false, true} {
		row := f.list.Rows[0]
		if replace {
			f.list.Connection = time.Unix(50, 0)
			f.read.Connection = f.list.Connection
		} else {
			f.list.Rows[0] = LeadRow{Lead: row.Lead, Chat: "off"}
			if r.getWith("/leads/", s).Code != 200 {
				t.Fatal("directory")
			}
		}
		f.list.Rows[0] = row
		*row.Epoch++
		f.read.Epoch = *row.Epoch
		f.read.Entries[0].Seq = 10
		w := r.getWith("/leads/daemon/alpha?after=7", s)
		if w.Code != 200 || strings.Contains(w.Body.String(), "Lead restarted:") || !strings.Contains(w.Body.String(), `?after=0">Show earlier`) {
			t.Fatal("reused old history")
		}
	}
}

// TestChatVisitDroppedForAnotherLead: of a real host, what a login kept of
// a lead (the epoch behind the restart marker) is dropped when the lead's
// switch changes, when a read resets and when the epoch falls below the one
// kept, so a lead that may be another under the same key never shows the
// marker of the one before. A rise of the epoch under the same switch, with
// no reset, still shows it.
func TestChatVisitDroppedForAnotherLead(t *testing.T) {
	f := sampleChat()
	f.list.StandIn, f.read.StandIn = false, false
	f.read.Lead = "daemon:approve"
	r := newRig(t, withChat(f))
	s := r.login(t)
	row := &f.list.Rows[1]
	page := func(epoch int64) string {
		t.Helper()
		*row.Epoch = epoch
		f.read.Epoch = epoch
		w := r.getWith("/leads/daemon/approve?after=2", s)
		if w.Code != 200 || strings.Contains(w.Body.String(), "STAND-IN") {
			t.Fatal(w.Code, w.Body)
		}
		return w.Body.String()
	}
	marked := func(p string) bool { return strings.Contains(p, "Lead restarted:") }
	if marked(page(1)) {
		t.Fatal("a first visit is marked")
	}
	if !marked(page(2)) || marked(page(2)) {
		t.Fatal("a restart under the same switch")
	}
	// A downgrade, send to read, drops the visit.
	row.Chat = "read"
	if marked(page(3)) {
		t.Fatal("marked across a downgrade")
	}
	// A reset drops it, and its page starts a visit of its own.
	f.read.Reset = true
	if p := page(4); marked(p) || !strings.Contains(p, "This page starts again from the oldest kept message.") {
		t.Fatal("marked across a reset")
	}
	f.read.Reset = false
	if marked(page(4)) || !marked(page(5)) {
		t.Fatal("the visit a reset started")
	}
	// An epoch below the one kept is another lead under the key.
	if marked(page(1)) || !marked(page(2)) {
		t.Fatal("marked across a fall of the epoch")
	}
}

func TestChatTypedResultAdmission(t *testing.T) {
	for _, change := range []func(*ChatView){
		func(v *ChatView) { v.Rows[0].Epoch = nil }, func(v *ChatView) { v.Rows[0].Head = nil },
		func(v *ChatView) { v.Rows[0].Chat = "on" }, func(v *ChatView) { v.Rows[1].Lead = v.Rows[0].Lead },
		func(v *ChatView) { v.Rows[2].State = "unknown" }, func(v *ChatView) { v.Rows[2].Head = v.Rows[0].Head },
		func(v *ChatView) { *v.Rows[0].Head = MaxChatSeq + 1 }, func(v *ChatView) { *v.Rows[0].Epoch = -1 },
		func(v *ChatView) { v.Entries = []ChatEntry{} }, func(v *ChatView) { v.Rows = nil },
	} {
		f := sampleChat()
		change(&f.list)
		if f.list.ValidFor(ChatCall{Verb: ChatLeads}) {
			t.Fatal("accepted malformed directory")
		}
	}
	for _, change := range []func(*ChatView){
		func(v *ChatView) { v.More = true; v.Entries = []ChatEntry{} }, func(v *ChatView) { v.Entries = append(v.Entries, v.Entries[0]) },
		func(v *ChatView) { v.Entries[0].Seq = 4 }, func(v *ChatView) { v.Entries[0].Seq = v.Head + 1 },
		func(v *ChatView) { v.Entries[0].Who = "desk" }, func(v *ChatView) { v.Entries[0].At = v.Entries[0].At.Add(time.Millisecond) },
		func(v *ChatView) { v.Epoch = MaxChatSeq + 1 }, func(v *ChatView) { v.Head = -1 }, func(v *ChatView) { v.Rows = []LeadRow{} },
	} {
		f := sampleChat()
		change(&f.read)
		if f.read.ValidFor(ChatCall{Verb: ChatRead, Lead: "daemon:alpha", After: 4}) {
			t.Fatal("accepted malformed transcript")
		}
	}
}
