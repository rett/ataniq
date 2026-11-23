//go:build linux && ataniq_ownerchannel

package hostadapter

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"ataniq/internal/approval/door/doortest"
	"ataniq/internal/approval/gatehost"
	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/approval/record"
	"ataniq/internal/approval/webauthntest"
	"ataniq/internal/seatapp"
	"ataniq/internal/seatgate"
	"ataniq/internal/seatlink"
)

// These tests run the live refresh through the whole chain on the loopback
// address, over the real pieces of each end, as the decision tests do
// (seat_decide_e2e_test.go, whose world, relay and helpers they use):
//
//	the owner channel's service (WaitPending) <- the stand-in door
//	  -> gatehost (seat.wait) -> seatgate.Serve, TLS 1.3 with pins
//	  -> a TCP relay on 127.0.0.1 -> seatgate.Client
//	  -> seatlink.Daemon's watcher (StartWatch) -> seatapp (POST /api/watch)
//	  -> the browser's long poll
//
// Only the planner's stand-in door and the phone's authenticator are fakes.
// The watcher's timings and the app's hold are the real ones (the packages
// give no way to set them from outside): a test waits for what it expects
// with a deadline, never for a fixed time, and what must not happen within a
// short interval is shown within that interval only. The app's session clock
// is the rig's, which a test advances.

const (
	// watchHeldFor is how long a poll must stay unanswered for a test to
	// call it held.
	watchHeldFor = 500 * time.Millisecond
	// watchWithin bounds a wait for a change to reach a poll or the watcher's
	// state: the daemon's look floor (1 s), the watcher's pause after a
	// change (2 s) and its idle look (1 s), and room to spare.
	watchWithin = 12 * time.Second
)

// pollAnswer is the answer of POST /api/watch.
type pollAnswer struct {
	State   string `json:"state"`
	Version string `json:"version"`
	Waiting int    `json:"waiting"`
	Card    string `json:"card"`
}

// polled is the outcome of a poll started with startPoll.
type polled struct {
	ans pollAnswer
	rep reply
	err error
}

// startPoll sends POST /api/watch as the page's script does, in the
// background, and returns the channel its outcome comes on. The test
// goroutine reads the channel and reports a failure.
func (s *decSeat) startPoll(sess *http.Cookie, version, card, binding string) <-chan polled {
	out := make(chan polled, 1)
	go func() {
		var p polled
		defer func() { out <- p }()
		b, err := json.Marshal(map[string]string{"version": version, "card": card, "binding": binding})
		if err != nil {
			p.err = err
			return
		}
		req, err := http.NewRequest(http.MethodPost, s.web.URL+"/api/watch", bytes.NewReader(b))
		if err != nil {
			p.err = err
			return
		}
		req.Host = pairRPID
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", pairOrigin)
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		req.AddCookie(&http.Cookie{Name: sess.Name, Value: sess.Value})
		client := &http.Client{Timeout: pairCallTime, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		resp, err := client.Do(req)
		if err != nil {
			p.err = err
			return
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			p.err = err
			return
		}
		p.rep = reply{status: resp.StatusCode, body: body, cookies: resp.Cookies()}
		if resp.StatusCode == http.StatusOK {
			p.err = json.Unmarshal(body, &p.ans)
		}
	}()
	return out
}

// answer waits up to within for the outcome of a poll and requires a 200.
func answer(t *testing.T, c <-chan polled, within time.Duration, why string) pollAnswer {
	t.Helper()
	select {
	case p := <-c:
		if p.err != nil || p.rep.status != http.StatusOK {
			t.Fatalf("%s: the poll ended with %d %q (%v)", why, p.rep.status, p.rep.body, p.err)
		}
		return p.ans
	case <-time.After(within):
		t.Fatalf("%s: the poll was not answered within %v", why, within)
	}
	return pollAnswer{}
}

// held requires that a poll is not answered for watchHeldFor.
func held(t *testing.T, c <-chan polled, why string) {
	t.Helper()
	select {
	case p := <-c:
		t.Fatalf("%s: the poll was answered at once: %d %q (%v)", why, p.rep.status, p.rep.body, p.err)
	case <-time.After(watchHeldFor):
	}
}

// poll is a poll that must be answered at once.
func (s *decSeat) poll(sess *http.Cookie, version, card, binding, why string) pollAnswer {
	s.t.Helper()
	return answer(s.t, s.startPoll(sess, version, card, binding), 2*time.Second, why)
}

var watchDivRE = regexp.MustCompile(`<div id="watch"[^>]*>`)

// watchAttr reads an attribute of the page's watch element, as the page's
// script does, and whether the page has the element.
func watchAttr(page, name string) (string, bool) {
	div := watchDivRE.FindString(page)
	if div == "" {
		return "", false
	}
	_, after, ok := strings.Cut(div, " "+name+`="`)
	if !ok {
		return "", true
	}
	v, _, _ := strings.Cut(after, `"`)
	return v, true
}

// pageWatch reads a page of the seat and returns it, its data-version and
// whether it says it is live.
func (s *decSeat) pageWatch(path string, sess *http.Cookie) (page, version string, live bool) {
	s.t.Helper()
	r := s.get(path, sess)
	if r.status != http.StatusOK {
		s.t.Fatalf("GET %s: %d %q", path, r.status, r.body)
	}
	page = string(r.body)
	version, ok := watchAttr(page, "data-version")
	if !ok {
		s.t.Fatalf("GET %s: the page has no watch element", path)
	}
	l, _ := watchAttr(page, "data-live")
	return page, version, l == "yes"
}

// cardWatch reads the Card page and returns what its watch element names:
// the version, the card and the binding.
func (s *decSeat) cardWatch(sess *http.Cookie, card string) (version, id, binding string) {
	s.t.Helper()
	page, version, live := s.pageWatch("/cards/"+card, sess)
	id, _ = watchAttr(page, "data-card")
	binding, _ = watchAttr(page, "data-binding")
	if !live || version == "" || id != card || len(binding) != 64 {
		s.t.Fatalf("the Card page's watch element names live %v, version %q, card %q, binding %q", live, version, id, binding)
	}
	return version, id, binding
}

// waitWatch waits until the link's watch state satisfies ok.
func (s *decSeat) waitWatch(why string, ok func(seatlink.WatchState) bool) seatlink.WatchState {
	s.t.Helper()
	deadline := time.Now().Add(watchWithin)
	for time.Now().Before(deadline) {
		if st := s.link.Watch(); ok(st) {
			return st
		}
		time.Sleep(10 * time.Millisecond)
	}
	st := s.link.Watch()
	s.t.Fatalf("%s: the watch state did not come within %v: %+v (client error %v)", why, watchWithin, st, s.c.LastError())
	return st
}

// listed is a watch state that is live, reachable and lists the cards given.
func listed(cards ...string) func(seatlink.WatchState) bool {
	return func(st seatlink.WatchState) bool {
		if !st.Watching || st.Offline || st.Version == "" {
			return false
		}
		for _, id := range cards {
			if !slices.ContainsFunc(st.Cards, func(c seatlink.WatchCard) bool { return c.ID == id }) {
				return false
			}
		}
		return true
	}
}

// tick ticks the daemon, as its loop does every so often: the way a change
// at the door that the daemon did not write reaches a held wait.
func (w *decideWorld) tick() {
	w.t.Helper()
	if err := w.svc.Tick(pairCtx(w.t)); err != nil {
		w.t.Fatalf("Tick: %v", err)
	}
}

// watchSetup is the start of a live refresh test: a world on the scene
// change makes, one seat whose watcher runs, the phone paired, a card asked
// for, the daemon ticked, the phone logged in, and the watcher showing the
// card.
func watchSetup(t *testing.T, change func(l *lab)) (w *decideWorld, s *decSeat, phone *webauthntest.Authenticator, card string, sess *http.Cookie) {
	t.Helper()
	w = newDecideWorld(t, change)
	s = w.newSeat(w.seatID)
	s.link.StartWatch()
	t.Cleanup(func() { _ = s.link.Close() })
	phone = newPhone(t)
	w.pair(s, phone)
	card = w.card()
	w.tick()
	sess = s.logIn(phone)
	s.waitWatch("the first card", listed(card))
	return w, s, phone, card, sess
}

// A new request at the door reaches the open list without a request of the
// page's: the list names its version, the page's poll with that version is
// held, a request submitted at the door and a tick of the daemon wake the
// watcher through the gate, the poll answers changed with the new count, and
// the list read again shows the new card under a new version.
func TestSeatWatchE2EListGainsACard(t *testing.T) {
	w, s, _, card, sess := watchSetup(t, nil)

	list, v0, live := s.pageWatch("/cards/", sess)
	if !live || v0 == "" || !strings.Contains(list, card) {
		t.Fatalf("the list names live %v, version %q, and shows the card %v", live, v0, strings.Contains(list, card))
	}
	if st := s.link.Watch(); st.Version != v0 {
		t.Errorf("the list's version is %q, the watcher's %q", v0, st.Version)
	}
	if w.calls.count(ownerwire.OpSeatWait) == 0 {
		t.Errorf("the gate served no seat.wait: %q", w.calls.seen())
	}

	c := s.startPoll(sess, v0, "", "")
	held(t, c, "a poll with the list's version")

	second := w.ask("y", w.feature("y", "y.txt", "y\n"))
	w.tick()
	got := answer(t, c, watchWithin, "after a request at the door")
	if got.State != "changed" || got.Version == v0 || got.Version == "" || got.Waiting != 2 || got.Card != "unknown" {
		t.Errorf("the poll answers %+v, want changed with a new version and 2 waiting", got)
	}

	list, v1, _ := s.pageWatch("/cards/", sess)
	if !strings.Contains(list, card) || !strings.Contains(list, second) || v1 != got.Version {
		t.Errorf("the list read again shows the first card %v, the new one %v, version %q (the poll's %q)",
			strings.Contains(list, card), strings.Contains(list, second), v1, got.Version)
	}
	// The page's next poll is held again.
	held(t, s.startPoll(sess, v1, "", ""), "a poll with the new version")
}

// A Card page's poll names its card and binding. While nothing changes the
// card is live: a session holds up to three polls, one per tab, and a fourth
// replaces the oldest, which answers replaced with the card unknown. When
// the owner decides the card at the desk, the held poll answers changed with
// the card gone, and the Card page read again offers no decide form.
func TestSeatWatchE2ECardDecidedAtTheDesk(t *testing.T) {
	w, s, _, card, sess := watchSetup(t, nil)
	s.show(sess, card)
	v, id, binding := s.cardWatch(sess, card)
	p := w.pendingOf(card)
	if binding != p.BindingSHA256 {
		t.Fatalf("the page's binding is %q, the card's %q", binding, p.BindingSHA256)
	}

	first := s.startPoll(sess, v, id, binding)
	held(t, first, "a poll on the card")
	other := s.startPoll(sess, v, id, binding)
	held(t, other, "a second tab's poll")
	third := s.startPoll(sess, v, id, binding)
	held(t, third, "a third tab's poll")
	second := s.startPoll(sess, v, id, binding)
	got := answer(t, first, 2*time.Second, "the oldest poll, replaced by a fourth")
	if got.State != "replaced" || got.Card != "unknown" || got.Version != v {
		t.Errorf("the oldest poll answers %+v, want replaced", got)
	}
	held(t, second, "the newer poll")

	if res, err := w.rig.decide(p, false); err != nil || res.State != record.StateDenied {
		t.Fatalf("the desk's decision: %+v, %v", res, err)
	}
	got = answer(t, second, watchWithin, "after the desk's decision")
	if got.State != "changed" || got.Card != "gone" || got.Waiting != 0 || got.Version == v {
		t.Errorf("the poll answers %+v, want changed, the card gone and nothing waiting", got)
	}

	again := s.get("/cards/"+card, sess)
	if again.status != http.StatusOK || strings.Contains(string(again.body), decideFormMark) ||
		!strings.Contains(string(again.body), string(record.StateDenied)) {
		t.Errorf("the card read again: %d, denied shown %v, form shown %v", again.status,
			strings.Contains(string(again.body), string(record.StateDenied)), strings.Contains(string(again.body), decideFormMark))
	}
}

// A card whose binding changed under the page is not "live": the stand-in
// door forgets its cards, as a restart would, and the id comes back as a card
// with another binding. The poll that names the old binding answers changed
// with the card changed; the page read again names the new binding, and a
// poll on it is held.
func TestSeatWatchE2ECardBindingChanged(t *testing.T) {
	w, s, _, card, sess := watchSetup(t, execFeature)
	s.show(sess, card)
	v, id, binding := s.cardWatch(sess, card)

	c := s.startPoll(sess, v, id, binding)
	held(t, c, "a poll on the card")

	cfg := doortest.Sample(w.rig.clock.Now)
	cfg.Command = "qargi"
	w.door.swap(doortest.New(cfg))
	again := w.ask("y", w.feature("y", "y.txt", "y\n", "Makefile", "y:\n\ttrue\n"))
	if now := w.pendingOf(card).BindingSHA256; again != card || now == binding {
		t.Fatalf("the new card is %s with the binding %s, want %s with another binding than %s", again, now, card, binding)
	}
	w.tick()
	got := answer(t, c, watchWithin, "after the card's binding changed")
	if got.State != "changed" || got.Card != "changed" || got.Version == v || got.Waiting != 1 {
		t.Errorf("the poll answers %+v, want changed with the card changed", got)
	}

	s.show(sess, card)
	v2, id2, binding2 := s.cardWatch(sess, card)
	if binding2 != w.pendingOf(card).BindingSHA256 || binding2 == binding {
		t.Errorf("the page read again names the binding %q, want the new one", binding2)
	}
	held(t, s.startPoll(sess, v2, id2, binding2), "a poll on the card with the new binding")
	// A poll with the old binding is told at once, whatever its version.
	old := s.poll(sess, "0", id2, binding, "a poll with the old binding")
	if old.Card != "changed" {
		t.Errorf("a poll with the old binding says the card is %q, want changed", old.Card)
	}
}

// The gate is cut: the watcher's wait fails, the held poll answers offline
// within the watcher's backoff, and a new poll answers offline at once. With
// the gate back, the connection is made again, the watcher recovers under
// its old version, and a change at the door is answered changed to the poll
// after it.
func TestSeatWatchE2EGateDropAndRecovery(t *testing.T) {
	w, s, _, card, sess := watchSetup(t, nil)
	_, v, _ := s.pageWatch("/cards/", sess)

	c := s.startPoll(sess, v, "", "")
	held(t, c, "a poll with the list's version")

	// The gate goes: the relay has nothing to reach, and what it carries is
	// closed.
	w.relay.mu.Lock()
	gate := w.relay.target
	w.relay.mu.Unlock()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dead := ln.Addr().String()
	_ = ln.Close()
	before := s.since()
	w.relay.retarget(dead)
	w.relay.cut()

	got := answer(t, c, watchWithin, "after the gate was cut")
	if got.State != "offline" {
		t.Errorf("the held poll answers %+v, want offline", got)
	}
	if now := s.poll(sess, v, "", "", "a poll while the gate is out of reach"); now.State != "offline" {
		t.Errorf("a poll while the gate is out of reach answers %+v, want offline", now)
	}
	if st := s.link.Watch(); !st.Offline || !st.Watching {
		t.Errorf("the watcher's state is %+v, want watching and offline", st)
	}

	// The gate is back.
	w.relay.retarget(gate)
	s.waitReconnected(before, true)
	st := s.waitWatch("the watcher's recovery", listed(card))
	if st.Version != v {
		t.Fatalf("the watcher's version is %q after the recovery, want %q, nothing having changed", st.Version, v)
	}
	again := s.startPoll(sess, st.Version, "", "")
	held(t, again, "a poll after the recovery")

	second := w.ask("y", w.feature("y", "y.txt", "y\n"))
	w.tick()
	got = answer(t, again, watchWithin, "after a change at the door, the gate being back")
	if got.State != "changed" || got.Waiting != 2 || got.Version == v {
		t.Errorf("the poll after the recovery answers %+v, want changed with 2 waiting", got)
	}
	if list, _, _ := s.pageWatch("/cards/", sess); !strings.Contains(list, second) {
		t.Errorf("the list after the recovery does not show the new card")
	}
}

// A poll does not keep a session alive: after more than SessionIdle with
// polls alone the session has ended, and the next poll says so. A session
// that read in between lives on. The clock is the rig's, which the app reads.
func TestSeatWatchE2EPollsDoNotKeepASessionAlive(t *testing.T) {
	w, s, phone, _, sess := watchSetup(t, nil)
	reader := s.logIn(phone)

	clock := w.rig.clock
	clock.Advance(seatapp.SessionIdle * 2 / 3)
	// A version other than the watcher's is answered at once, which keeps
	// the session as it is.
	if got := s.poll(sess, "0", "", "", "a poll inside the idle time"); got.State != "changed" {
		t.Fatalf("a poll inside the idle time answers %+v, want changed", got)
	}
	if r := s.get("/cards/", reader); r.status != http.StatusOK {
		t.Fatalf("the reading session's read inside the idle time: %d", r.status)
	}

	// More than the idle time since the first session's last use, which was
	// its login; less since the reader's read.
	clock.Advance(seatapp.SessionIdle / 2)
	if got := s.poll(sess, "0", "", "", "a poll past the idle time"); got.State != "ended" {
		t.Errorf("a poll past the idle time answers %+v, want ended", got)
	}
	if r := s.get("/cards/", sess); r.status == http.StatusOK {
		t.Errorf("the session that only polled still reads past its idle time")
	}
	if r := s.get("/cards/", reader); r.status != http.StatusOK {
		t.Errorf("the session that read in between does not read: %d", r.status)
	}
}

// A gate whose service cannot wait does not list "watch": the watcher says
// the link does not watch, a poll is answered offline at once and the pages
// say they are not live, with the Card page still offering its decision.
func TestSeatWatchE2EGateWithoutTheWaitCapability(t *testing.T) {
	w, s, _, card, sess := watchSetup(t, nil)
	if _, _, live := s.pageWatch("/cards/", sess); !live {
		t.Fatal("the list is not live on a gate that can wait")
	}
	if w.calls.count(ownerwire.OpSeatWait) == 0 {
		t.Errorf("the first gate served no seat.wait: %q", w.calls.seen())
	}

	addr, calls := w.startGateWith(noWait{w.svc})
	before := s.since()
	w.relay.retarget(addr)
	w.relay.cut()
	s.waitReconnected(before, true)
	if slices.Contains(s.c.Caps(), seatlink.CapWatch) {
		t.Fatalf("the gate lists %q, which has no watch", s.c.Caps())
	}
	s.waitWatch("the watcher's state on a gate without watch", func(st seatlink.WatchState) bool { return !st.Watching && !st.Offline })

	if got := s.poll(sess, "0", "", "", "a poll on a gate without watch"); got.State != "offline" {
		t.Errorf("the poll answers %+v, want offline", got)
	}
	if got := s.poll(sess, "", card, strings.Repeat("a", 64), "a Card poll on a gate without watch"); got.State != "offline" || got.Card != "unknown" {
		t.Errorf("the Card poll answers %+v, want offline with the card unknown", got)
	}
	list, version, live := s.pageWatch("/cards/", sess)
	if live || version != "" || !strings.Contains(list, card) {
		t.Errorf("the list on a gate without watch: live %v, version %q, card shown %v", live, version, strings.Contains(list, card))
	}
	if div := watchDivRE.FindString(list); !strings.Contains(div, `data-live="no"`) {
		t.Errorf("the list's watch element is %q, want data-live no", div)
	}
	page, _, live := s.pageWatch("/cards/"+card, sess)
	if live || !strings.Contains(page, decideFormMark) {
		t.Errorf("the Card page on a gate without watch: live %v, decide form shown %v", live, strings.Contains(page, decideFormMark))
	}
	if n := calls.count(ownerwire.OpSeatWait); n != 0 {
		t.Errorf("the seat asked a gate without watch for seat.wait %d times", n)
	}
}

// noWait is the owner channel's service as the gate's port without its
// WaitPending: a service that cannot wait.
type noWait struct{ gatehost.Port }

// startGateWith serves a deciding gate over the port given, as startGate does
// with the service, pinning both seat keys, and returns its address and the
// calls it served.
func (w *decideWorld) startGateWith(port gatehost.Port) (string, *seenHandler) {
	w.t.Helper()
	pins := []string{w.seatID.Fingerprint(), w.otherID.Fingerprint()}
	pinned := func(fp string) bool { return slices.Contains(pins, fp) }
	h, err := gatehost.NewDeciding(port, w.svc, w.gate, pinned, nil)
	if err != nil {
		w.t.Fatal(err)
	}
	if slices.Contains(h.Caps(), gatehost.CapWatch) {
		w.t.Fatalf("the gate lists %q for a service that cannot wait", h.Caps())
	}
	calls := &seenHandler{h: h}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		w.t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() {
		served <- seatgate.Serve(ctx, ln, seatgate.ServerConfig{
			TLS: seatgate.ServerTLS(w.gateID, pins), Caps: h.Caps(),
		}, calls)
	}()
	w.t.Cleanup(func() {
		cancel()
		select {
		case err := <-served:
			if err != nil {
				w.t.Errorf("Serve: %v", err)
			}
		case <-time.After(10 * time.Second):
			w.t.Error("Serve did not end")
		}
	})
	return ln.Addr().String(), calls
}
