package seatapp

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// leadsOK is a Leads stream's body with the cursor the Leads rigs start with.
const leadsOK = `{"leads":"7"}`

// fakeChatWatcher is a ChatWatcher whose hint a test sets.
type fakeChatWatcher struct {
	mu    sync.Mutex
	h     ChatHint
	ch    chan struct{}
	asked int
}

func newFakeChatWatcher(cursor int64) *fakeChatWatcher {
	return &fakeChatWatcher{h: ChatHint{Live: true, Cursor: cursor, Connection: time.Unix(1, 0)}, ch: make(chan struct{})}
}

func (w *fakeChatWatcher) ChatHint() ChatHint {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.asked++
	return w.h
}

func (w *fakeChatWatcher) ChatHintChanged() <-chan struct{} {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.ch
}

// set changes the hint and wakes whoever holds the channel.
func (w *fakeChatWatcher) set(h ChatHint) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.h = h
	close(w.ch)
	w.ch = make(chan struct{})
}

// setQuiet changes the hint and wakes nobody: a hint whose connection went
// before its watcher said so.
func (w *fakeChatWatcher) setQuiet(h ChatHint) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.h = h
}

func (w *fakeChatWatcher) calls() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.asked
}

// leadsChat is the sample chat port, whose offer a test can take back while
// streams read it.
type leadsChat struct {
	*fakeChat
	gone atomic.Bool
}

func (c *leadsChat) ChatOffered() bool { return !c.gone.Load() && c.fakeChat.ChatOffered() }

// leadsDaemon is the fake daemon that watches the Cards, has the chat port
// and learns the chat hint.
type leadsDaemon struct {
	*watchDaemon
	*leadsChat
	*fakeChatWatcher
}

var (
	_ ChatWatcher = (*leadsDaemon)(nil)
	_ Chat        = (*leadsDaemon)(nil)
	_ Watcher     = (*leadsDaemon)(nil)
)

// leadsWrap makes a leadsDaemon of a watching port, the sample chat and a
// hint of cursor 7.
func leadsWrap(wd *watchDaemon) *leadsDaemon {
	return &leadsDaemon{watchDaemon: wd, leadsChat: &leadsChat{fakeChat: sampleChat()}, fakeChatWatcher: newFakeChatWatcher(7)}
}

// newLeadsRig is a watch rig whose port also has the sample chat and a live
// chat hint of cursor 7.
func newLeadsRig(t *testing.T) (*rig, *leadsDaemon) {
	t.Helper()
	wd := &watchDaemon{fakeWatcher: newFakeWatcher("aa")}
	var ld *leadsDaemon
	r := newRig(t, func(c *Config) {
		wd.fakeDaemon = c.Daemon.(*fakeDaemon)
		ld = leadsWrap(wd)
		c.Daemon = ld
	})
	r.app.watchHold = time.Minute
	return r, ld
}

// chatRe is the one form an event of a Leads stream has: the kind and the
// cursor, an integer, and nothing else.
var chatRe = regexp.MustCompile(`^event: chat-changed\ndata: [1-9][0-9]{0,15}$`)

// TestSeatLeadsStreamStrictInput: the body names the view by its one
// member; a Leads cursor is a decimal integer of the browser's range, with
// no sign, no leading zero and nothing else; two members of either view, or
// of both, and any other form are refused before the watcher or the hint is
// looked at.
func TestSeatLeadsStreamStrictInput(t *testing.T) {
	r, ld := newLeadsRig(t)
	s := r.login(t)
	for name, body := range map[string]string{
		"a number":            `{"leads":7}`,
		"null":                `{"leads":null}`,
		"empty":               `{"leads":""}`,
		"a leading zero":      `{"leads":"07"}`,
		"a sign":              `{"leads":"+7"}`,
		"negative":            `{"leads":"-7"}`,
		"a fraction":          `{"leads":"7.0"}`,
		"an exponent":         `{"leads":"7e1"}`,
		"hex":                 `{"leads":"ff"}`,
		"a space":             `{"leads":" 7"}`,
		"past 2^53-1":         `{"leads":"9007199254740992"}`,
		"seventeen digits":    `{"leads":"10000000000000000"}`,
		"another case":        `{"Leads":"7"}`,
		"the key twice":       `{"leads":"7","leads":"7"}`,
		"both views":          `{"leads":"7","version":"aa"}`,
		"both views, cards 1": `{"version":"aa","leads":"7"}`,
		"a view member":       `{"view":"leads"}`,
		"the view's name":     `{"cards":"aa"}`,
		"chat":                `{"chat":"7"}`,
		"an array":            `["leads","7"]`,
		"after the object":    `{"leads":"7"} {}`,
	} {
		w := r.streamDo(context.Background(), body, nil, s)
		if w.Code != 400 || w.Body.String() != `{"error":"bad_request"}`+"\n" {
			t.Errorf("%s: %d %q", name, w.Code, w.Body)
		}
	}
	if n := ld.fakeChatWatcher.calls() + ld.watchCalls(); n != 0 {
		t.Errorf("the refusals reached the watchers %d times", n)
	}
	// The cursors a page may carry: none yet, and the largest.
	done, cancel := context.WithCancel(context.Background())
	cancel()
	for _, body := range []string{`{"leads":"0"}`, `{"leads":"9007199254740991"}`, leadsOK} {
		if w := r.streamDo(done, body, nil, s); w.Code != 200 || strings.HasPrefix(w.Header().Get("Content-Type"), "text/event-stream") {
			t.Errorf("%s: %d %q", body, w.Code, w.Body)
		}
	}
	if r.streams() != 0 {
		t.Errorf("%d streams open", r.streams())
	}
}

// TestSeatLeadsStreamNotLive: a hint that is not live, a gate that does not
// offer chat, a port without a hint and a port without chat refuse the
// Leads stream with the fixed 503 and its Retry-After (the page keeps to
// its Refresh link), while the card list's stream of the same port, where
// it watches, still opens.
func TestSeatLeadsStreamNotLive(t *testing.T) {
	r, ld := newLeadsRig(t)
	s := r.login(t)
	refused := func(name string, r *rig, s *http.Cookie) {
		t.Helper()
		w := r.streamDo(context.Background(), leadsOK, nil, s)
		if w.Code != 503 || w.Body.String() != streamTextNotLive || w.Header().Get("Retry-After") != "30" ||
			strings.HasPrefix(w.Header().Get("Content-Type"), "text/event-stream") || w.Header().Get("Location") != "" {
			t.Errorf("%s: %d %q Retry-After %q", name, w.Code, w.Body, w.Header().Get("Retry-After"))
		}
		checkHeaders(t, name, w)
	}
	for name, h := range map[string]ChatHint{
		"not live":              {},
		"live with no cursor":   {Live: true},
		"a cursor out of range": {Live: true, Cursor: MaxChatSeq + 1},
	} {
		ld.fakeChatWatcher.set(h)
		refused(name, r, s)
	}
	ld.fakeChatWatcher.set(ChatHint{Live: true, Cursor: 7})
	ld.gone.Store(true)
	refused("chat not offered", r, s)
	done, cancel := context.WithCancel(context.Background())
	cancel()
	if w := r.streamDo(done, streamOK, nil, s); w.Code != 200 {
		t.Errorf("the card list's stream beside a Leads view that is not live: %d %q", w.Code, w.Body)
	}
	// A port with chat and no hint, and one that watches and has no chat.
	noHint := newRig(t, withChat(sampleChat()))
	refused("no hint", noHint, noHint.login(t))
	cardsOnly, _ := newWatchRig(t, "aa", time.Minute)
	refused("no chat", cardsOnly, cardsOnly.login(t))
	if r.streams()+noHint.streams()+cardsOnly.streams() != 0 {
		t.Error("a refused stream stayed open")
	}
}

// TestSeatLeadsStreamHints: a Leads stream starts with a heartbeat, says
// each move of the chat cursor as one chat-changed event that holds the
// cursor and nothing else, says nothing of the Cards, and asks the daemon
// nothing; a card list's stream beside it says nothing of the chat.
func TestSeatLeadsStreamHints(t *testing.T) {
	r, ld := newLeadsRig(t)
	r.app.streamGap = 10 * time.Millisecond
	s := r.login(t)
	srv := streamServer(t, r)
	asked, chats := r.daemon.count(), len(ld.fakeChat.calls)
	c := openStream(t, srv, leadsOK, s)
	cards := openStream(t, srv, streamOK, s)
	if c.resp.StatusCode != 200 || c.resp.Header.Get("Content-Type") != "text/event-stream" || c.resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("stream: %d %v", c.resp.StatusCode, c.resp.Header)
	}
	if b, ok := c.next(t, 3*time.Second); !ok || b != ":" {
		t.Fatalf("first block %q %v, want the heartbeat", b, ok)
	}
	if b, ok := cards.next(t, 3*time.Second); !ok || b != ":" {
		t.Fatalf("the card list's first block %q %v", b, ok)
	}
	// The Cards move: nothing on the Leads stream; the card list hears it.
	ld.watchDaemon.set(func(st *WatchState) { st.Version, st.Waiting = "bb", 4 })
	if b, ok := cards.nextEvent(t, 3*time.Second); !ok || !hintRe.MatchString(b) {
		t.Fatalf("the card list's event %q %v", b, ok)
	}
	ld.fakeChatWatcher.set(ChatHint{Live: true, Cursor: 9, Connection: time.Unix(1, 0)})
	if b, ok := c.nextEvent(t, 3*time.Second); !ok || b != "event: chat-changed\ndata: 9" {
		t.Fatalf("event %q %v", b, ok)
	}
	// A wake that leaves the cursor says nothing; the next move does.
	ld.fakeChatWatcher.set(ChatHint{Live: true, Cursor: 9, Connection: time.Unix(2, 0)})
	ld.fakeChatWatcher.set(ChatHint{Live: true, Cursor: MaxChatSeq, Connection: time.Unix(2, 0)})
	if b, ok := c.nextEvent(t, 3*time.Second); !ok || b != fmt.Sprintf("event: chat-changed\ndata: %d", int64(MaxChatSeq)) {
		t.Fatalf("event %q %v", b, ok)
	}
	if n := r.daemon.count(); n != asked {
		t.Errorf("the streams asked the daemon %d times", n-asked)
	}
	if len(ld.fakeChat.calls) != chats {
		t.Errorf("the stream read the chat %d times", len(ld.fakeChat.calls)-chats)
	}
	c.cancel()
	cards.cancel()
	for _, block := range strings.Split(strings.TrimSuffix(c.text(), "\n\n"), "\n\n") {
		if block != ":" && !chatRe.MatchString(block) {
			t.Errorf("a block of another form: %q", block)
		}
		if len(block)+2 > streamEventMax {
			t.Errorf("a block of %d bytes", len(block)+2)
		}
	}
	for _, bad := range []string{"cards-changed", "alpha", "approve", "later", "running", "daemon", "bb", "version"} {
		if strings.Contains(c.text(), bad) {
			t.Errorf("the Leads stream holds %q: %q", bad, c.text())
		}
	}
	if strings.Contains(cards.text(), "chat-changed") {
		t.Errorf("the card list's stream holds a chat event: %q", cards.text())
	}
}

// TestSeatLeadsStreamSaysAMissedChangeAtOnce: a stream opened with an older
// cursor, or with none, says the current one as its first event; one opened
// with the current one says nothing.
func TestSeatLeadsStreamSaysAMissedChangeAtOnce(t *testing.T) {
	r, _ := newLeadsRig(t)
	s := r.login(t)
	srv := streamServer(t, r)
	for body, want := range map[string]string{`{"leads":"3"}`: "event: chat-changed\ndata: 7", `{"leads":"0"}`: "event: chat-changed\ndata: 7"} {
		c := openStream(t, srv, body, s)
		c.next(t, 3*time.Second)
		if b, ok := c.next(t, 3*time.Second); !ok || b != want {
			t.Errorf("%s: %q %v", body, b, ok)
		}
		c.cancel()
		r.waitStreams(t, 0)
	}
	r.app.streamBeat = 20 * time.Millisecond
	c := openStream(t, srv, leadsOK, s)
	for i := 0; i < 3; i++ {
		if b, ok := c.next(t, 3*time.Second); !ok || b != ":" {
			t.Fatalf("a stream of the current cursor: %q %v", b, ok)
		}
	}
}

// TestSeatLeadsStreamEndsWhenTheHintDies: the stream ends, cleanly and with
// no event, when the hint stops being live (a lost link, a new connection, a
// downgrade), when its gate stops offering chat, also when the hint died
// with no wake (its connection gone before its watcher said so: the view is
// looked at again before each heartbeat), and at a logout. A change that is
// pending in the gap is not said once the hint is dead.
func TestSeatLeadsStreamEndsWhenTheHintDies(t *testing.T) {
	for name, kill := range map[string]func(*leadsDaemon){
		"not live":     func(ld *leadsDaemon) { ld.fakeChatWatcher.set(ChatHint{}) },
		"no offer":     func(ld *leadsDaemon) { ld.gone.Store(true); ld.fakeChatWatcher.set(ChatHint{Live: true, Cursor: 7}) },
		"no wake":      func(ld *leadsDaemon) { ld.fakeChatWatcher.setQuiet(ChatHint{}) },
		"offer, quiet": func(ld *leadsDaemon) { ld.gone.Store(true) },
		"in the gap": func(ld *leadsDaemon) {
			ld.fakeChatWatcher.set(ChatHint{Live: true, Cursor: 8})
			ld.fakeChatWatcher.set(ChatHint{Live: true, Cursor: 9})
			ld.fakeChatWatcher.setQuiet(ChatHint{})
		},
	} {
		t.Run(name, func(t *testing.T) {
			r, ld := newLeadsRig(t)
			r.app.streamBeat = 50 * time.Millisecond
			r.app.streamGap = 200 * time.Millisecond
			s := r.login(t)
			srv := streamServer(t, r)
			c := openStream(t, srv, leadsOK, s)
			c.next(t, 3*time.Second)
			kill(ld)
			c.ended(t, 3*time.Second)
			// In the gap, the one change said before the hint died is 8,
			// or 9 when the stream woke once for both; the change pending
			// when it died is never said.
			events := 0
			for _, block := range strings.Split(strings.TrimSuffix(c.text(), "\n\n"), "\n\n") {
				if block == ":" {
					continue
				}
				events++
				if name != "in the gap" || (block != "event: chat-changed\ndata: 8" && block != "event: chat-changed\ndata: 9") {
					t.Errorf("a block after the hint died: %q", block)
				}
			}
			if events > 1 {
				t.Errorf("%d events: a change pending in the gap was said after the hint died", events)
			}
			r.waitStreams(t, 0)
		})
	}
	r, _ := newLeadsRig(t)
	s := r.login(t)
	got := r.streamAsync(context.Background(), leadsOK, s)
	r.waitStreams(t, 1)
	r.postWith("/api/logout", "{}", s)
	within(t, got, 3*time.Second)
	r.waitStreams(t, 0)
}

// TestSeatLeadsStreamSharesTheLimits: the streams of both views are counted
// together, three of one credential and 32 in all, and the opens of one
// credential in a minute: a fourth stream of either view is refused, and
// the three keep their streams.
func TestSeatLeadsStreamSharesTheLimits(t *testing.T) {
	r, _ := newLeadsRig(t)
	s := r.login(t)
	s2 := r.login(t) // another session of the same passkey
	ctx, cancel := context.WithCancel(context.Background())
	outs := []<-chan *httptest.ResponseRecorder{
		r.streamAsync(ctx, streamOK, s), r.streamAsync(ctx, leadsOK, s), r.streamAsync(ctx, leadsOK, s2),
	}
	r.waitStreams(t, 3)
	r.daemon.mu.Lock()
	credential := r.daemon.loginRes.Credential
	r.daemon.mu.Unlock()
	if total, mine := r.app.streams.open(credential); total != 3 || mine != 3 {
		t.Errorf("open: %d in all, %d of the credential", total, mine)
	}
	for _, body := range []string{streamOK, leadsOK} {
		w := r.streamDo(context.Background(), body, nil, s2)
		if w.Code != 429 || w.Body.String() != streamTextLimited || w.Header().Get("Retry-After") != "25" {
			t.Errorf("a fourth stream (%s): %d %q %q", body, w.Code, w.Body, w.Header().Get("Retry-After"))
		}
	}
	if r.streams() != 3 {
		t.Errorf("%d streams open after the refusals", r.streams())
	}
	cancel()
	for _, o := range outs {
		within(t, o, 3*time.Second)
	}
	r.waitStreams(t, 0)
	// The opens of a minute: 3 above and 2 refused are 5 of the 12; Leads
	// opens take the rest, and the 13th open, of either view, is refused.
	done, stop := context.WithCancel(context.Background())
	stop()
	for i := 5; i < streamOpens; i++ {
		if w := r.streamDo(done, leadsOK, nil, s); w.Code != 200 {
			t.Fatalf("open %d: %d %q", i, w.Code, w.Body)
		}
	}
	for _, body := range []string{leadsOK, streamOK} {
		if w := r.streamDo(done, body, nil, s); w.Code != 429 || w.Header().Get("Retry-After") != "60" {
			t.Errorf("an open past the minute's (%s): %d %q", body, w.Code, w.Body)
		}
	}
}

// TestSeatLeadsStreamIsNoCheckIn: a Leads stream takes no foreground lease
// and counts no card as seen, whatever is waiting.
func TestSeatLeadsStreamIsNoCheckIn(t *testing.T) {
	r, ld := newLeadsRig(t)
	ld.watchDaemon.set(func(st *WatchState) { st.Version, st.Waiting = "bb", 3 })
	s := r.login(t)
	srv := streamServer(t, r)
	for _, body := range []string{leadsOK, `{"leads":"0"}`} {
		c := openStream(t, srv, body, s)
		c.next(t, 3*time.Second)
		if got := r.app.foregroundUntil(r.clock.Now()); !got.IsZero() {
			t.Errorf("%s: a lease to %v", body, got)
		}
		if seq, _ := r.app.pollSeen(); seq != 0 {
			t.Errorf("%s: the cards counted as seen", body)
		}
		if !r.app.lastPollTime().IsZero() {
			t.Errorf("%s: the stream counted as a poll", body)
		}
		c.cancel()
		r.waitStreams(t, 0)
	}
}

// TestSeatLeadsStreamNoticeNotHeldBack: with only a Leads page open, a card
// that comes is told at once, as with no page open: the Leads stream holds
// no notice back. The card list's stream, opened then, holds the next one
// back, its positive control.
func TestSeatLeadsStreamNoticeNotHeldBack(t *testing.T) {
	sr := newStreamNoticeRigWith(t, func(wd *watchDaemon) Daemon { return leadsWrap(wd) })
	c := openStream(t, sr.srv, leadsOK, sr.s)
	if b, ok := c.next(t, 3*time.Second); !ok || b != ":" {
		t.Fatalf("stream: %q %v", b, ok)
	}
	sr.change(t, 1)
	sr.expect(t, 1)
	if seq, _ := sr.app.pollSeen(); seq != 0 {
		t.Error("the Leads stream counted the card as seen")
	}
	cards := sr.open(t, sr.version())
	sr.change(t, 2)
	sr.expect(t, 1)
	cards.cancel()
	c.cancel()
	sr.waitStreams(t, 0)
}
