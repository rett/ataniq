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
// hint of cursor 7, all of one connection.
func leadsWrap(wd *watchDaemon) *leadsDaemon {
	f := sampleChat()
	f.list.Connection, f.read.Connection = time.Unix(1, 0), time.Unix(1, 0)
	return &leadsDaemon{watchDaemon: wd, leadsChat: &leadsChat{fakeChat: f}, fakeChatWatcher: newFakeChatWatcher(7)}
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
	if b, ok := c.next(t, waitBound); !ok || b != ":" {
		t.Fatalf("first block %q %v, want the heartbeat", b, ok)
	}
	if b, ok := cards.next(t, waitBound); !ok || b != ":" {
		t.Fatalf("the card list's first block %q %v", b, ok)
	}
	// The Cards move: nothing on the Leads stream; the card list hears it.
	ld.watchDaemon.set(func(st *WatchState) { st.Version, st.Waiting = "bb", 4 })
	if b, ok := cards.nextEvent(t, waitBound); !ok || !hintRe.MatchString(b) {
		t.Fatalf("the card list's event %q %v", b, ok)
	}
	ld.fakeChatWatcher.set(ChatHint{Live: true, Cursor: 9, Connection: time.Unix(1, 0)})
	if b, ok := c.nextEvent(t, waitBound); !ok || b != "event: chat-changed\ndata: 9" {
		t.Fatalf("event %q %v", b, ok)
	}
	// A wake that leaves the cursor says nothing; the next move does.
	ld.fakeChatWatcher.set(ChatHint{Live: true, Cursor: 9, Connection: time.Unix(1, 0)})
	ld.fakeChatWatcher.set(ChatHint{Live: true, Cursor: MaxChatSeq, Connection: time.Unix(1, 0)})
	if b, ok := c.nextEvent(t, waitBound); !ok || b != fmt.Sprintf("event: chat-changed\ndata: %d", int64(MaxChatSeq)) {
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
		c.next(t, waitBound)
		if b, ok := c.next(t, waitBound); !ok || b != want {
			t.Errorf("%s: %q %v", body, b, ok)
		}
		c.cancel()
		r.waitStreams(t, 0)
	}
	r.app.streamBeat = 20 * time.Millisecond
	c := openStream(t, srv, leadsOK, s)
	for i := 0; i < 3; i++ {
		if b, ok := c.next(t, waitBound); !ok || b != ":" {
			t.Fatalf("a stream of the current cursor: %q %v", b, ok)
		}
	}
}

// waitChatHintCalls waits until ChatHint has been called n times.
func waitChatHintCalls(t *testing.T, w *fakeChatWatcher, n int) {
	t.Helper()
	deadline := time.Now().Add(waitBound)
	for w.calls() < n {
		if time.Now().After(deadline) {
			t.Fatalf("ChatHint called %d times, want %d", w.calls(), n)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestSeatLeadsStreamEndsWhenTheHintDies: the stream ends, cleanly and with
// no event, when the hint stops being live (a lost link, a downgrade) or is
// of a new connection, when its gate stops offering chat, also when the hint died
// with no wake (its connection gone before its watcher said so: the view is
// looked at again before each heartbeat), and at a logout. A change that is
// pending in the gap is not said once the hint is dead.
func TestSeatLeadsStreamEndsWhenTheHintDies(t *testing.T) {
	for name, kill := range map[string]func(*leadsDaemon){
		"not live": func(ld *leadsDaemon) { ld.fakeChatWatcher.set(ChatHint{}) },
		"no offer": func(ld *leadsDaemon) { ld.gone.Store(true); ld.fakeChatWatcher.set(ChatHint{Live: true, Cursor: 7}) },
		"in the gap": func(ld *leadsDaemon) {
			ld.fakeChatWatcher.set(ChatHint{Live: true, Cursor: 8, Connection: time.Unix(1, 0)})
			ld.fakeChatWatcher.set(ChatHint{Live: true, Cursor: 9, Connection: time.Unix(1, 0)})
			ld.fakeChatWatcher.setQuiet(ChatHint{})
		},
		// A live hint of a new connection: the stream was admitted on the
		// first, and says nothing of the second's cursor.
		"new connection": func(ld *leadsDaemon) {
			ld.fakeChatWatcher.set(ChatHint{Live: true, Cursor: 8, Connection: time.Unix(2, 0)})
		},
	} {
		t.Run(name, func(t *testing.T) {
			r, ld := newLeadsRig(t)
			r.app.streamBeat = 50 * time.Millisecond
			r.app.streamGap = 200 * time.Millisecond
			s := r.login(t)
			srv := streamServer(t, r)
			c := openStream(t, srv, leadsOK, s)
			c.next(t, waitBound)
			kill(ld)
			c.ended(t, waitBound)
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
	// The hint dies with no wake: only the look at the view before a
	// heartbeat can find it. The stream's timers stand still, so neither a
	// heartbeat the host happens to run nor the rotation can end it. The
	// test waits until the stream has read the view after its opening
	// heartbeat, so that read cannot be the one that finds the dead hint,
	// then kills the hint and fires exactly one heartbeat: the stream ends
	// at it, cleanly, and writes nothing more, the heartbeat included.
	for name, kill := range map[string]func(*leadsDaemon){
		"no wake":      func(ld *leadsDaemon) { ld.fakeChatWatcher.setQuiet(ChatHint{}) },
		"offer, quiet": func(ld *leadsDaemon) { ld.gone.Store(true) },
		"new connection, quiet": func(ld *leadsDaemon) {
			ld.fakeChatWatcher.setQuiet(ChatHint{Live: true, Cursor: 8, Connection: time.Unix(2, 0)})
		},
	} {
		t.Run(name, func(t *testing.T) {
			r, ld := newLeadsRig(t)
			ft := stillTimers(r)
			s := r.login(t)
			srv := streamServer(t, r)
			read := ld.fakeChatWatcher.calls()
			c := openStream(t, srv, leadsOK, s)
			if b, ok := c.next(t, waitBound); !ok || b != ":" {
				t.Fatalf("the first block %q %v, want the heartbeat", b, ok)
			}
			// The admission's read and the one after the opening heartbeat.
			waitChatHintCalls(t, ld.fakeChatWatcher, read+2)
			kill(ld)
			waitDue(t, ft, r.app.streamBeat)
			ft.advance(r.app.streamBeat)
			if b, ok := c.next(t, waitBound); ok {
				t.Fatalf("a block after the hint died, at the heartbeat: %q", b)
			}
			if c.readErr != nil {
				t.Errorf("the stream ended with %v, not a clean end", c.readErr)
			}
			r.waitStreams(t, 0)
		})
	}
	// The logout: the stream's timers stand still, so that the logout, not
	// a heartbeat that finds the session gone, ends it.
	r, _ := newLeadsRig(t)
	ft := stillTimers(r)
	s := r.login(t)
	got := r.heldStream(t, leadsOK, s)
	r.waitStreams(t, 1)
	waitTimers(t, ft, 3) // the life, the heartbeat and the session's end
	r.postWith("/api/logout", "{}", s)
	within(t, got, waitBound)
	r.waitStreams(t, 0)
}

// flushHook is a recorder that runs a hook at its first flush: the opening
// heartbeat's.
type flushHook struct {
	*httptest.ResponseRecorder
	once sync.Once
	hook func()
}

func (f *flushHook) Flush() {
	f.ResponseRecorder.Flush()
	f.once.Do(f.hook)
}

// TestSeatLeadsStreamOpeningFlush: the hint dies, or its connection is
// replaced, while the opening heartbeat is written: the stream looks at the
// view again after it, so no block that follows carries a cursor, even for
// a page that opened with an older cursor (whose first event would say the
// current one).
func TestSeatLeadsStreamOpeningFlush(t *testing.T) {
	for name, kill := range map[string]func(*leadsDaemon){
		"not live": func(ld *leadsDaemon) { ld.fakeChatWatcher.setQuiet(ChatHint{}) },
		"no offer": func(ld *leadsDaemon) { ld.gone.Store(true) },
		"new connection": func(ld *leadsDaemon) {
			ld.fakeChatWatcher.setQuiet(ChatHint{Live: true, Cursor: 8, Connection: time.Unix(2, 0)})
		},
		"new connection, woken": func(ld *leadsDaemon) {
			ld.fakeChatWatcher.set(ChatHint{Live: true, Cursor: 9, Connection: time.Unix(2, 0)})
		},
	} {
		for _, body := range []string{`{"leads":"0"}`, `{"leads":"3"}`, leadsOK} {
			t.Run(name+" "+body, func(t *testing.T) {
				r, ld := newLeadsRig(t)
				r.app.streamBeat = 20 * time.Millisecond
				s := r.login(t)
				ctx, cancel := context.WithTimeout(context.Background(), waitBound)
				defer cancel()
				w := &flushHook{ResponseRecorder: httptest.NewRecorder(), hook: func() { kill(ld) }}
				r.h.ServeHTTP(w, streamRequest(ctx, streamPath, body, s))
				if ctx.Err() != nil {
					t.Fatal("the stream did not end")
				}
				if w.Code != 200 || w.Body.String() != ":\n\n" {
					t.Errorf("after the opening heartbeat: %d %q", w.Code, w.Body)
				}
				r.waitStreams(t, 0)
			})
		}
	}
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
		within(t, o, waitBound)
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
		c.next(t, waitBound)
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
	if b, ok := c.next(t, waitBound); !ok || b != ":" {
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

// TestSeatLeadsPagesCarryTheStream: the Leads pages, and they alone, carry
// the stream's mark (data-view="leads"), with the cursor taken before their
// reads, which page they are, and, for a conversation whose read reached
// the head, the cursor of its following read; the Cards pages carry none.
func TestSeatLeadsPagesCarryTheStream(t *testing.T) {
	r, ld := newLeadsRig(t)
	s := r.login(t)
	// The hint moves while the directory is read: the page has the older
	// cursor, so that its stream says the change at once.
	ld.leadsChat.onRead = func() { ld.fakeChatWatcher.setQuiet(ChatHint{Live: true, Cursor: 8, Connection: time.Unix(1, 0)}) }
	const status = `<p id="watch-status" role="status" aria-live="polite"></p>`
	for path, mark := range map[string]string{
		"/leads/":                     `<div id="watch" data-view="leads" data-live="yes" data-chat="7" data-page="directory"></div>`,
		"/leads/daemon/alpha?after=4": `<div id="watch" data-view="leads" data-live="yes" data-chat="7" data-page="conversation"></div>`,
	} {
		ld.fakeChatWatcher.setQuiet(ChatHint{Live: true, Cursor: 7, Connection: time.Unix(1, 0)})
		body := r.getWith(path, s).Body.String()
		if strings.Count(body, mark) != 1 || strings.Count(body, `data-view=`) != 1 || strings.Count(body, status) != 1 ||
			strings.Index(body, status) > strings.Index(body, mark) || strings.Index(body, mark) > strings.Index(body, `<script src="/static/watch.js"></script>`) {
			t.Errorf("%s: the stream's mark: %s", path, body)
		}
	}
	// The sample read ends before the head (More): no follow mark. A read
	// that reached the head follows after its last entry.
	ld.leadsChat.onRead = nil
	ld.leadsChat.read.More = false
	body := r.getWith("/leads/daemon/alpha?after=4", s).Body.String()
	if !strings.Contains(body, `data-page="conversation" data-follow="7"></div>`) {
		t.Errorf("a conversation at the head: %s", body)
	}
	ld.leadsChat.read.Entries[0].Seq = 30
	ld.leadsChat.read.Head = 30
	*ld.leadsChat.list.Rows[0].Head = 30
	if body := r.getWith("/leads/daemon/alpha", s).Body.String(); !strings.Contains(body, `data-follow="30"`) {
		t.Errorf("the follow cursor: %s", body)
	}
	// A hint that is not live, or chat not offered when the page asks:
	// the page is not live, with no cursor.
	ld.fakeChatWatcher.setQuiet(ChatHint{})
	if body := r.getWith("/leads/", s).Body.String(); !strings.Contains(body, `data-view="leads" data-live="no" data-chat="0" data-page="directory"`) {
		t.Errorf("a Leads page with no hint: %s", body)
	}
	// The Cards pages carry no mark of the Leads.
	for _, path := range []string{"/cards/", "/cards/" + testCardID, "/cards/" + testCardID + "/paths/1"} {
		if body := r.getWith(path, s).Body.String(); strings.Contains(body, "data-view") || strings.Contains(body, "data-chat") || strings.Contains(body, "data-follow") {
			t.Errorf("%s carries a Leads mark", path)
		}
	}
}

// TestSeatLeadsPagesCursorOfTheirConnection: the link is replaced between
// the hint the page takes and its directory read. The reads come from the
// new connection and agree with each other, but the cursor is the old
// one's: the page carries no cursor and is not live, so it shows its
// Refresh link; a page whose hint and reads share their connection is live.
func TestSeatLeadsPagesCursorOfTheirConnection(t *testing.T) {
	r, ld := newLeadsRig(t)
	s := r.login(t)
	const notLive = `data-view="leads" data-live="no" data-chat="0"`
	ld.leadsChat.list.Connection, ld.leadsChat.read.Connection = time.Unix(2, 0), time.Unix(2, 0)
	for _, path := range []string{"/leads/", "/leads/daemon/alpha?after=4"} {
		w := r.getWith(path, s)
		body := w.Body.String()
		if w.Code != 200 || !strings.Contains(body, notLive) || strings.Contains(body, `data-chat="7"`) || strings.Contains(body, "data-follow") {
			t.Errorf("%s: a cursor of another connection: %d %s", path, w.Code, body)
		}
	}
	// The hint moves to the new connection too: the page is live with its
	// cursor.
	ld.fakeChatWatcher.setQuiet(ChatHint{Live: true, Cursor: 12, Connection: time.Unix(2, 0)})
	for _, path := range []string{"/leads/", "/leads/daemon/alpha?after=4"} {
		if body := r.getWith(path, s).Body.String(); !strings.Contains(body, `data-view="leads" data-live="yes" data-chat="12"`) {
			t.Errorf("%s: the cursor of the reads' connection: %s", path, body)
		}
	}
}

// TestSeatFollowAfter: the following read starts directly after the last
// rendered entry, a sequence the read returned, so it skips nothing and
// never starts at a cursor the chat may not hold.
func TestSeatFollowAfter(t *testing.T) {
	entries := func(seqs ...int64) []ChatEntry {
		var out []ChatEntry
		for _, s := range seqs {
			out = append(out, ChatEntry{Seq: s})
		}
		return out
	}
	for _, c := range []struct {
		v     ChatView
		after int64
		last  int64
		want  int64
	}{
		{ChatView{Head: 3, Entries: entries(1, 2, 3)}, 0, 3, 3},
		{ChatView{Head: 40, Entries: entries(25, 40)}, 24, 40, 40},
		{ChatView{Head: 40, Entries: entries(25, 40), Reset: true}, 3, 40, 40},
		{ChatView{Head: 8}, 8, 0, 8},
		{ChatView{Head: 20}, 20, 0, 20},
		{ChatView{Head: 20}, 50, 0, 20},
		{ChatView{Head: 20, Reset: true}, 5, 0, 20},
	} {
		got := followAfter(c.v, c.after, c.last)
		if got != c.want {
			t.Errorf("%+v after %d: %d, want %d", c.v, c.after, got, c.want)
		}
		if n := len(c.v.Entries); n > 0 && got != c.v.Entries[n-1].Seq {
			t.Errorf("%+v: the following read does not start after the last entry", c.v)
		}
	}
}

// TestSeatFollowAtTheByteCap: the entries the reader saw are as large as an
// entry can be, so that a page holds fewer than a window of them (the
// gate's bound of a result). The page shows the newest entry and follows
// after it; a reply as large arrives, and the following read shows it, at
// the newest, still following. Half a window back, the bound cuts the
// page before the new reply: a look-back would lose it.
func TestSeatFollowAtTheByteCap(t *testing.T) {
	r, ld := newLeadsRig(t)
	s := r.login(t)
	l := &chatLog{lead: "daemon:alpha", epoch: 1, conn: time.Unix(1, 0)}
	l.add(8, 10)
	l.add(8, MaxChatTextBytes)
	ld.leadsChat.fakeChat = logChat(l)
	const entry = `<p dir="auto" class="lead chat-body">`
	body := r.getWith("/leads/daemon/alpha?after=15", s).Body.String()
	if strings.Count(body, entry) != 1 || !strings.Contains(body, `data-follow="16"`) {
		t.Fatalf("the newest page: %s", body)
	}
	l.add(1, MaxChatTextBytes)
	body = r.getWith("/leads/daemon/alpha?after=16", s).Body.String()
	if strings.Count(body, entry) != 1 || !strings.Contains(body, `data-follow="17"`) || !strings.Contains(body, `<li id="newest" tabindex="-1">`) ||
		strings.Contains(body, `id="chat-next"`) {
		t.Errorf("the following read: %s", body)
	}
	body = r.getWith("/leads/daemon/alpha?after=8", s).Body.String()
	if n := strings.Count(body, entry); n >= 9 || !strings.Contains(body, `id="chat-next"`) || strings.Contains(body, "data-follow") {
		t.Errorf("half a window back: %d entries: %s", n, body)
	}
}
