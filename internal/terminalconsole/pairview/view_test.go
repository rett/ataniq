package pairview

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
)

func TestValidOrigin(t *testing.T) {
	for _, c := range []struct {
		in   string
		want string // "" refuses
	}{
		{"https://app.example", "https://app.example"},
		{"https://App.Example", "https://app.example"},
		{"HTTPS://app.example", "https://app.example"},
		{"https://app.example:443", "https://app.example"},
		{"https://a-b.c1.example", "https://a-b.c1.example"},
		{"https://xn--bcher-kva.example", "https://xn--bcher-kva.example"},
		{"", ""},
		{"https://", ""},
		{"app.example", ""},
		{"http://app.example", ""},
		{"ftp://app.example", ""},
		{"https://app.example:8443", ""},
		{"https://app.example:80", ""},
		{"https://app.example:", ""},
		{"https://app.example:0443", ""},
		{"https://app.example:443:443", ""},
		{"https://app.example/", ""},
		{"https://app.example/pair", ""},
		{"https://app.example/pair/", ""},
		{"https://app.example?x=1", ""},
		{"https://app.example#frag", ""},
		{"https://user@app.example", ""},
		{"https://user:pw@app.example", ""},
		{"https://app.example@evil.example", ""},
		{"https://192.0.2.1", ""},
		{"https://192.0.2.1:443", ""},
		{"https://0x7f.0x1", ""},
		{"https://0x7f.1", ""},
		{"https://app.0x", ""},
		{"https://app.0X", ""},
		{"https://app.0xff", ""},
		{"https://2130706433", ""},
		{"https://[::1]", ""},
		{"https://[2001:db8::1]:443", ""},
		{"https://localhost", ""},
		{"https://app.example.", ""},
		{"https://.example", ""},
		{"https://app..example", ""},
		{"https://-app.example", ""},
		{"https://app-.example", ""},
		{"https://app_x.example", ""},
		{"https://b\u00fccher.example", ""},
		{"https://app.example ", ""},
		{" https://app.example", ""},
		{"https://app .example", ""},
		{"https://app.example\n", ""},
		{"https://app.example\x00", ""},
		{"https://app.example\\evil", ""},
		{"https://" + strings.Repeat("a", 64) + ".example", ""},
		{"https://" + strings.Repeat("a.", 130) + "example", ""},
	} {
		got, ok := ValidOrigin(c.in)
		if ok != (c.want != "") || got != c.want {
			t.Errorf("ValidOrigin(%q) = %q, %v; want %q", c.in, got, ok, c.want)
		}
	}
}

func TestNoValidOriginDrawsNoQRAndStartsNothing(t *testing.T) {
	for _, o := range []string{"", "http://app.example", "https://app.example:8443", "https://app.example/x", "https://u@app.example", "https://192.0.2.1"} {
		b := &fakeBackend{}
		r := newRig(t, b, o, 80, 24)
		if r.m.Init() != nil {
			t.Fatalf("origin %q: Init asked the daemon", o)
		}
		r.see(80, 24, "pairing origin not configured")
		for _, k := range []string{"enter", "c", "x", "y"} {
			r.press(k)
		}
		if len(b.calls) != 0 {
			t.Errorf("origin %q: the backend was called: %v", o, b.calls)
		}
		buf := draw(r.m, 80, 24)
		for y := 0; y < 24; y++ {
			for x := 0; x < 80; x++ {
				if c := buf.CellAt(x, y); c != nil && strings.ContainsAny(c.Content, "█▀▄") {
					t.Fatalf("origin %q: a QR module at %d,%d", o, x, y)
				}
			}
		}
		r.press("esc")
		if r.last.Kind != Close {
			t.Errorf("origin %q: Esc did not close", o)
		}
	}
	// A nil backend is the same.
	m := New(Options{Origin: testOrigin})
	if m.Configured() || m.Init() != nil {
		t.Fatal("a view with no backend is configured")
	}
}

func TestGoldens(t *testing.T) {
	type tc struct {
		name string
		w, h int
		make func(t *testing.T) *rig
	}
	idle := func(t *testing.T) *rig { return newRig(t, &fakeBackend{}, testOrigin, 0, 0) }
	waiting := func(t *testing.T) *rig {
		r := newRig(t, &fakeBackend{}, testOrigin, 0, 0)
		r.start()
		return r
	}
	pending := func(t *testing.T) *rig {
		r := waiting(t)
		r.pending()
		return r
	}
	for _, c := range []tc{
		{"idle-80x24", 80, 24, idle},
		{"idle-60x20", 60, 20, idle},
		{"waiting-80x24", 80, 24, waiting},
		{"waiting-60x20", 60, 20, waiting},
		{"waiting-too-small-30x10", 30, 10, waiting},
		{"waiting-too-small-36x30", 36, 30, waiting},
		{"waiting-minquiet-40x22", 40, 22, waiting},
		{"pending-80x24", 80, 24, pending},
		{"pending-60x20", 60, 20, pending},
		{"pending-30x20", 30, 20, pending},
		{"confirm-80x24", 80, 24, func(t *testing.T) *rig {
			r := pending(t)
			r.m.SetSize(80, 24)
			r.press("c")
			return r
		}},
		{"paired-80x24", 80, 24, func(t *testing.T) *rig {
			r := pending(t)
			r.m.SetSize(80, 24)
			r.b.paired = Paired{ID: "cred-7f3a9b", Kind: "unverified"}
			r.press("c")
			r.now = r.now.Add(time.Second)
			r.press("y")
			return r
		}},
		{"expired-80x24", 80, 24, func(t *testing.T) *rig {
			r := waiting(t)
			r.b.state = State{Phase: Expired}
			r.fire()
			return r
		}},
		{"noorigin-80x24", 80, 24, func(t *testing.T) *rig {
			return newRig(t, &fakeBackend{}, "http://app.example", 0, 0)
		}},
		{"start-failed-80x24", 80, 24, func(t *testing.T) *rig {
			b := &fakeBackend{startErr: errBackend}
			r := newRig(t, b, testOrigin, 0, 0)
			r.init()
			r.press("enter")
			return r
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := c.make(t)
			golden(t, c.name, text(r.m, c.w, c.h))
		})
	}
}

func TestStartThenQRThenWordsThenConfirm(t *testing.T) {
	b := &fakeBackend{}
	r := newRig(t, b, testOrigin, 80, 24)
	r.init()
	if r.m.screen != scIdle || b.count("status") != 1 || b.count("start") != 0 {
		t.Fatalf("screen %v calls %v", r.m.screen, b.calls)
	}
	r.see(80, 24, "PAIR A PHONE", "Enter: start")
	r.press("enter")
	if len(b.replaces) != 1 || b.replaces[0] {
		t.Fatalf("start replaces %v, want [false]", b.replaces)
	}
	if r.m.screen != scWaiting {
		t.Fatalf("screen %v", r.m.screen)
	}
	r.see(80, 24, "Scan with your phone's camera. Expires in 600 s.", "▒")
	// The poll: nothing changes while the phone has not answered.
	b.state = State{Phase: Waiting, ExpiresAt: epoch.Add(10 * time.Minute)}
	r.fire()
	r.see(80, 24, "Expires in 598 s.")
	r.pending()
	r.see(80, 24, "1 lemon", "2 river", "3 tiger", "4 ocean", "5 maple", "6 quartz",
		"Compare with your phone. Confirm only if they match.", "c: confirm", "x: reject")
	if len(r.m.rows) != 0 {
		t.Fatal("the QR rows are kept after the words came")
	}
	r.press("c")
	r.see(80, 24, "press y to confirm, any other key cancels")
	if b.count("confirm") != 0 {
		t.Fatal("c confirmed")
	}
	r.now = r.now.Add(time.Second)
	b.paired = Paired{ID: "cred-7f3a9b", Kind: "unverified"}
	r.press("y")
	if b.count("confirm") != 1 || r.m.screen != scPaired {
		t.Fatalf("screen %v calls %v", r.m.screen, b.calls)
	}
	r.see(80, 24, "Paired. Kind: unverified (a syncing passkey).", "cred-7f3a9b")
	if b.count("reject") != 0 {
		t.Fatal("a reject was sent")
	}
}

func TestConfirmAsksOnceMoreAndAnyOtherKeyCancels(t *testing.T) {
	for _, k := range []tea.KeyPressMsg{
		{Code: 'n', Text: "n"}, {Code: 'c', Text: "c"}, {Code: tea.KeyEnter}, {Code: tea.KeySpace, Text: " "},
		{Code: 'Y', Text: "Y"}, {Code: 'y', Mod: tea.ModCtrl}, {Code: 'y', Mod: tea.ModAlt, Text: "y"}, {Code: 'y', Mod: tea.ModShift, Text: "Y"},
	} {
		b := &fakeBackend{}
		r := newRig(t, b, testOrigin, 80, 24)
		r.start()
		r.pending()
		r.press("c")
		r.now = r.now.Add(time.Second)
		r.send(k)
		if r.m.screen != scPending || b.count("confirm") != 0 {
			t.Errorf("%+v: screen %v calls %v", k, r.m.screen, b.calls)
		}
	}
}

func TestAYTypedAheadDoesNotConfirm(t *testing.T) {
	b := &fakeBackend{}
	r := newRig(t, b, testOrigin, 80, 24)
	r.start()
	r.pending()
	r.press("c")
	r.press("y") // the same instant
	if b.count("confirm") != 0 || r.m.screen != scConfirm {
		t.Fatalf("screen %v calls %v", r.m.screen, b.calls)
	}
	r.now = r.now.Add(confirmDelay)
	r.press("y")
	if b.count("confirm") != 1 {
		t.Fatalf("calls %v", b.calls)
	}
}

func TestXRejects(t *testing.T) {
	b := &fakeBackend{}
	r := newRig(t, b, testOrigin, 80, 24)
	r.start()
	r.pending()
	r.press("x")
	if b.count("reject") != 1 || b.count("confirm") != 0 || r.m.screen != scIdle {
		t.Fatalf("screen %v calls %v", r.m.screen, b.calls)
	}
	r.see(80, 24, "Rejected. Nothing was added.", "Enter: start")
	// Enter starts a new pairing without replace.
	r.press("enter")
	if len(b.replaces) != 2 || b.replaces[1] {
		t.Fatalf("replaces %v", b.replaces)
	}
}

func TestEscClosesEverywhereAndConfirmsNothing(t *testing.T) {
	setups := map[string]func(r *rig){
		"idle":     func(r *rig) {},
		"starting": func(r *rig) { r.init(); r.m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) },
		"waiting":  func(r *rig) { r.start() },
		"pending":  func(r *rig) { r.start(); r.pending() },
		"confirm":  func(r *rig) { r.start(); r.pending(); r.press("c") },
		"expired": func(r *rig) {
			r.start()
			r.b.state = State{Phase: Expired}
			r.fire()
		},
	}
	for name, setup := range setups {
		b := &fakeBackend{}
		r := newRig(t, b, testOrigin, 80, 24)
		setup(r)
		before := len(b.calls)
		r.press("esc")
		if r.last.Kind != Close {
			t.Errorf("%s: Esc asked for %v", name, r.last.Kind)
		}
		if len(b.calls) != before || b.count("confirm") != 0 || b.count("reject") != 0 {
			t.Errorf("%s: Esc called the backend: %v", name, b.calls)
		}
	}
	// ctrl+c quits from anywhere.
	r := newRig(t, &fakeBackend{}, testOrigin, 80, 24)
	r.start()
	r.press("ctrl+c")
	if r.last.Kind != Quit {
		t.Fatalf("ctrl+c asked for %v", r.last.Kind)
	}
}

func TestPollsEveryTwoSecondsOnAFakeClock(t *testing.T) {
	b := &fakeBackend{}
	r := newRig(t, b, testOrigin, 80, 24)
	r.start()
	if len(r.timers) != 1 || r.timers[0].d != 2*time.Second {
		t.Fatalf("timers %+v", r.timers)
	}
	b.state = State{Phase: Waiting, ExpiresAt: epoch.Add(10 * time.Minute)}
	for i := 1; i <= 5; i++ {
		r.fire()
		if b.count("status") != 1+i { // one at open, one per tick
			t.Fatalf("tick %d: status calls %d", i, b.count("status"))
		}
		if len(r.timers) != 1 {
			t.Fatalf("tick %d: %d timers", i, len(r.timers))
		}
	}
	if r.now.Sub(epoch) != 10*time.Second {
		t.Fatalf("clock %v", r.now.Sub(epoch))
	}
	// The polling stops with the expiry: no timer is left.
	b.state = State{Phase: Expired}
	r.fire()
	if len(r.timers) != 0 || r.m.screen != scExpired {
		t.Fatalf("timers %d screen %v", len(r.timers), r.m.screen)
	}
}

func TestPollingKeepsGoingWhileWordsWaitAndStopsWhenPaired(t *testing.T) {
	b := &fakeBackend{}
	r := newRig(t, b, testOrigin, 80, 24)
	r.start()
	r.pending()
	n := b.count("status")
	r.fire()
	if b.count("status") != n+1 || len(r.timers) != 1 {
		t.Fatalf("status %d timers %d", b.count("status"), len(r.timers))
	}
	r.press("c")
	r.now = r.now.Add(time.Second)
	b.paired = Paired{ID: "id1", Kind: "unverified"}
	r.press("y")
	// The tick that was waiting ends the cycle.
	r.fire()
	if len(r.timers) != 0 {
		t.Fatalf("%d timers after paired", len(r.timers))
	}
}

func TestExpiryOfTheCodeOffersANewOneWithReplace(t *testing.T) {
	b := &fakeBackend{}
	r := newRig(t, b, testOrigin, 80, 24)
	r.start()
	b.state = State{Phase: Expired}
	r.fire()
	r.see(80, 24, "The code expired. Nothing was added.", "Enter: new code")
	if strings.Contains(text(r.m, 80, 24), "▒") {
		t.Fatal("a QR is drawn after the expiry")
	}
	r.press("enter")
	if len(b.replaces) != 2 || !b.replaces[1] || r.m.screen != scWaiting {
		t.Fatalf("replaces %v screen %v", b.replaces, r.m.screen)
	}
	// A daemon that says nothing is open is told in words.
	b.state = State{Phase: None}
	r.fire()
	r.see(80, 24, "No pairing is open on the daemon.", "Enter: new code")
}

func TestTheLocalClockEndsAWaitingCodeThatTheDaemonStillCallsWaiting(t *testing.T) {
	b := &fakeBackend{}
	r := newRig(t, b, testOrigin, 80, 24)
	r.start()
	b.state = State{Phase: Waiting} // no time of its own: the offer's is kept
	r.now = epoch.Add(10*time.Minute - time.Second)
	r.send(r.timers[0].fn(r.now))
	r.timers = r.timers[1:]
	if r.m.screen != scWaiting {
		t.Fatalf("screen %v before the end", r.m.screen)
	}
	r.fire()
	if r.m.screen != scExpired {
		t.Fatalf("screen %v after the end", r.m.screen)
	}
}

func TestAnOfferThatIsAlreadyOverIsNotDrawn(t *testing.T) {
	b := &fakeBackend{}
	r := newRig(t, b, testOrigin, 80, 24)
	r.init()
	b.offer.ExpiresAt = epoch
	r.press("enter")
	if r.m.screen != scExpired {
		t.Fatalf("screen %v", r.m.screen)
	}
	b.offer = Offer{Code: synthCode}
	r.press("enter")
	if r.m.screen != scExpired {
		t.Fatalf("a code with no end: screen %v", r.m.screen)
	}
}

func TestBackendErrorsShowAFixedLineNeverTheirText(t *testing.T) {
	// A failed start.
	b := &fakeBackend{startErr: errBackend}
	r := newRig(t, b, testOrigin, 80, 24)
	r.init()
	r.press("enter")
	r.see(80, 24, errStart, "Enter: try again")
	r.notSee(80, 24, secretError)
	// A failed poll keeps the QR and says so, and goes on.
	b = &fakeBackend{}
	r = newRig(t, b, testOrigin, 80, 24)
	r.start()
	b.statErr = errBackend
	r.fire()
	r.see(80, 24, errPoll, "▒")
	r.notSee(80, 24, secretError)
	b.statErr = nil
	b.state = State{Phase: Waiting, ExpiresAt: epoch.Add(10 * time.Minute)}
	r.fire()
	r.notSee(80, 24, errPoll)
	// A failed confirm goes back to the words and can be tried again.
	b = &fakeBackend{}
	r = newRig(t, b, testOrigin, 80, 24)
	r.start()
	r.pending()
	b.confErr = errBackend
	r.press("c")
	r.now = r.now.Add(time.Second)
	r.press("y")
	if r.m.screen != scPending {
		t.Fatalf("screen %v", r.m.screen)
	}
	r.see(80, 24, errConfirm, "1 lemon")
	r.notSee(80, 24, secretError, "Paired.")
	if len(r.timers) == 0 {
		t.Fatal("the polling did not start again")
	}
	// A failed reject.
	b.rejErr = errBackend
	r.press("x")
	if r.m.screen != scPending {
		t.Fatalf("screen %v", r.m.screen)
	}
	r.see(80, 24, errReject)
	r.notSee(80, 24, secretError)
	// A failed first read.
	b = &fakeBackend{statErr: errBackend}
	r = newRig(t, b, testOrigin, 80, 24)
	r.init()
	r.see(80, 24, errRead, "Enter: start")
	r.notSee(80, 24, secretError)
}

func TestBackendTextIsCleanedBeforeItIsDrawn(t *testing.T) {
	b := &fakeBackend{}
	r := newRig(t, b, testOrigin, 80, 24)
	r.start()
	b.state = State{Phase: Pending, PendingID: testPID, ExpiresAt: epoch.Add(time.Minute), Words: [WordCount]string{
		"lemon\x1b]52;c;aGk=\x07", "ri\x00ver", "tig\u202eer", "ocean", "maple", "quartz", "cedar", "amber"}}
	r.fire()
	if r.m.wordsOK {
		t.Fatal("words with control bytes can be confirmed")
	}
	r.see(80, 24, errWords, "x: reject")
	r.notSee(80, 24, "c: confirm", "\x1b", "\x00", "\u202e")
	r.press("c")
	if r.m.screen != scPending {
		t.Fatal("c left the unusable words")
	}
	// Words that are too long, empty or with a space are unusable as well.
	for _, w := range []string{strings.Repeat("a", 40), "", "two words", "  "} {
		in := testWords
		in[2] = w
		b.state.Words = in
		r.fire()
		if r.m.wordsOK {
			t.Errorf("the word %q can be confirmed", w)
		}
	}
	// The id and the kind of a paired key are cleaned and bounded.
	b.state.Words = testWords
	r.fire()
	b.paired = Paired{ID: "id\x1b[31m-" + strings.Repeat("z", 100), Kind: "hard\x07ware"}
	r.press("c")
	r.now = r.now.Add(time.Second)
	r.press("y")
	s := text(r.m, 80, 24)
	if strings.ContainsAny(s, "\x1b\x07") || !strings.Contains(s, "Kind: hardware.") || !strings.Contains(s, "id[31m-zz") {
		t.Fatalf("paired screen:\n%s", s)
	}
	for _, l := range strings.Split(s, "\n") {
		if strings.Contains(l, "Device id:") && utf8.RuneCountInString(l) > 40 {
			t.Fatalf("the id is not bounded: %q", l)
		}
	}
}

func TestChangedWordsCancelTheSecondQuestion(t *testing.T) {
	b := &fakeBackend{}
	r := newRig(t, b, testOrigin, 80, 24)
	r.start()
	r.pending()
	r.press("c")
	if r.m.screen != scConfirm {
		t.Fatal("no question")
	}
	// The same words keep it.
	r.fire()
	if r.m.screen != scConfirm {
		t.Fatalf("the same words cancelled the question: %v", r.m.screen)
	}
	// Other words (another phone) drop it.
	other := testWords
	other[0] = "zebra"
	b.state.Words = other
	r.fire()
	if r.m.screen != scPending {
		t.Fatalf("screen %v", r.m.screen)
	}
	r.see(80, 24, "1 zebra")
	// So does a daemon that has gone back to waiting.
	r.press("c")
	b.state = State{Phase: Waiting, ExpiresAt: epoch.Add(time.Hour)}
	r.fire()
	if r.m.screen != scOpen {
		t.Fatalf("screen %v", r.m.screen)
	}
}

func TestReopeningReadsTheDaemonsState(t *testing.T) {
	// Words waiting for the owner are shown at once.
	b := &fakeBackend{state: State{Phase: Pending, PendingID: testPID, Words: testWords, ExpiresAt: epoch.Add(time.Minute)}}
	r := newRig(t, b, testOrigin, 80, 24)
	r.init()
	if r.m.screen != scPending || len(r.timers) != 1 {
		t.Fatalf("screen %v timers %d", r.m.screen, len(r.timers))
	}
	r.see(80, 24, "1 lemon", "c: confirm")
	// A pairing that still waits for its phone cannot show its code again.
	b = &fakeBackend{state: State{Phase: Waiting, ExpiresAt: epoch.Add(90 * time.Second)}}
	r = newRig(t, b, testOrigin, 80, 24)
	r.init()
	r.see(80, 24, "A pairing is already open. Its code cannot be shown again.", "It ends in 90 s.", "Enter: new code (ends the open one)")
	r.notSee(80, 24, "▒")
	r.press("enter")
	if len(b.replaces) != 1 || !b.replaces[0] || r.m.screen != scWaiting {
		t.Fatalf("replaces %v screen %v", b.replaces, r.m.screen)
	}
	// A pairing that is over is the idle screen.
	b = &fakeBackend{state: State{Phase: Expired}}
	r = newRig(t, b, testOrigin, 80, 24)
	r.init()
	if r.m.screen != scIdle || len(r.timers) != 0 {
		t.Fatalf("screen %v timers %d", r.m.screen, len(r.timers))
	}
}

func TestAnAnswerOfAnotherGenerationOrViewIsLeftAlone(t *testing.T) {
	b := &fakeBackend{}
	r := newRig(t, b, testOrigin, 80, 24)
	r.start()
	old := r.timers[0]
	r.timers = nil
	// A new code: the old tick, which fires late, does nothing.
	b.state = State{Phase: Expired}
	r.press("esc")
	r.m.screen = scExpired
	r.press("enter")
	if r.m.screen != scWaiting {
		t.Fatalf("screen %v", r.m.screen)
	}
	n := b.count("status")
	r.send(old.fn(r.now))
	if b.count("status") != n {
		t.Fatal("a tick of an older pairing read the status")
	}
	// A message of another view is not this view's.
	other := newRig(t, &fakeBackend{}, testOrigin, 80, 24)
	other.start()
	m := other.timers[0].fn(r.now)
	c, _ := r.m.Update(m)
	if c != nil {
		t.Fatal("another view's tick was taken")
	}
	// A key release, a paste and a mouse event do nothing.
	for _, msg := range []tea.Msg{tea.PasteMsg{Content: "c"}, tea.KeyReleaseMsg{Code: 'c'}, tea.MouseClickMsg{}} {
		if c, act := r.m.Update(msg); c != nil || act.Kind != Nothing {
			t.Fatalf("%T did something", msg)
		}
	}
}

func TestACodeThatIsNotAFragmentIsNotDrawn(t *testing.T) {
	for _, code := range []string{"", "a b", "a#b", "a/b", "a?b", "a\nb", "\x00", "caf\u00e9", strings.Repeat("a", 129), strings.Repeat("a", 400)} {
		b := &fakeBackend{}
		r := newRig(t, b, testOrigin, 80, 24)
		r.init()
		b.offer.Code = code
		r.press("enter")
		if r.m.screen != scFailed || len(r.m.rows) != 0 {
			t.Errorf("code %q: screen %v", code, r.m.screen)
		}
		r.see(80, 24, errCode)
	}
}

func TestNoCodeAsTextAnywhere(t *testing.T) {
	b := &fakeBackend{}
	r := newRig(t, b, testOrigin, 100, 40)
	check := func(stage string) {
		t.Helper()
		for _, sz := range [][2]int{{100, 40}, {80, 24}, {60, 20}, {45, 25}, {30, 10}, {10, 3}, {1, 1}} {
			buf := draw(r.m, sz[0], sz[1])
			// Every cell, the QR's included: no cell holds the code or a
			// piece of it, since a QR module is one block character.
			var all strings.Builder
			for y := 0; y < sz[1]; y++ {
				for x := 0; x < sz[0]; x++ {
					if c := buf.CellAt(x, y); c != nil {
						all.WriteString(c.Content)
					}
				}
				all.WriteByte('\n')
			}
			for _, bad := range []string{synthCode, "SYNTH", "TEST-CODE", "/pair/#", "pair/", "https://"} {
				if strings.Contains(all.String(), bad) {
					t.Fatalf("%s at %v: %q is drawn as text", stage, sz, bad)
				}
			}
		}
		for _, s := range []string{fmt.Sprintf("%+v", r.m), fmt.Sprintf("%#v", r.m)} {
			if strings.Contains(s, synthCode) || strings.Contains(s, "/pair/#") {
				t.Fatalf("%s: the model holds the code", stage)
			}
		}
	}
	r.init()
	check("idle")
	r.press("enter")
	check("waiting")
	b.statErr = errBackend
	r.fire()
	check("poll error")
	b.statErr = nil
	r.pending()
	check("pending")
	r.press("c")
	check("confirm")
	r.now = r.now.Add(time.Second)
	r.press("y")
	check("paired")
	// Errors, an expiry and a failed start.
	r2 := newRig(t, &fakeBackend{startErr: errBackend}, testOrigin, 100, 40)
	r2.init()
	r2.press("enter")
	r = r2
	check("failed")
}

func TestTheQRIsBlackOnWhiteInEveryTheme(t *testing.T) {
	for name, colors := range map[string]Colors{"dark": darkColors(), "light": lightColors(), "none": {}} {
		b := &fakeBackend{}
		r := newRig(t, b, testOrigin, 80, 24)
		r.m.o.Colors = colors
		r.start()
		buf := draw(r.m, 80, 24)
		cols, need := qrSize(r.m.rows)
		x0, y0 := (80-cols)/2, 1+(24-2-len(r.m.rows))/2
		if need > 24 || cols > 80 {
			t.Fatalf("%s: the code does not fit 80x24 (%dx%d)", name, cols, need)
		}
		for i, row := range r.m.rows {
			x := 0
			for _, ch := range row {
				c := buf.CellAt(x0+x, y0+i)
				if c == nil || c.Content != string(ch) {
					t.Fatalf("%s: cell %d,%d is %+v, want %q", name, x, i, c, string(ch))
				}
				if c.Style.Fg != qrDark || c.Style.Bg != qrLight {
					t.Fatalf("%s: cell %d,%d is %v on %v, want black on white", name, x, i, c.Style.Fg, c.Style.Bg)
				}
				x++
			}
		}
		// The quiet zone: the first and last text rows are blank cells on white.
		for _, i := range []int{0, len(r.m.rows) - 1} {
			if strings.TrimLeft(r.m.rows[i], " ") != "" && i == 0 {
				t.Fatalf("%s: the first row has dark modules in the quiet zone", name)
			}
		}
		// The text around it follows the theme.
		if c := buf.CellAt(0, 5); c == nil || c.Style.Bg != colors.Bg {
			t.Fatalf("%s: the ground is %+v", name, c)
		}
	}
}

func TestTooSmallSaysSoAndDrawsNoPartOfTheCode(t *testing.T) {
	b := &fakeBackend{}
	r := newRig(t, b, testOrigin, 80, 24)
	r.start()
	// Even with the smaller quiet zone the code does not fit these sizes.
	cols, need := qrSize(r.m.rowsMin)
	if cols != 37 || need != 21 {
		t.Fatalf("the synthetic code is %dx%d at the least quiet zone; the golden expects 37x21", cols, need)
	}
	for _, sz := range [][2]int{{cols - 1, 40}, {200, need - 1}, {60, 20}, {10, 3}, {1, 1}, {30, 22}} {
		buf := draw(r.m, sz[0], sz[1])
		for y := 0; y < sz[1]; y++ {
			for x := 0; x < sz[0]; x++ {
				if c := buf.CellAt(x, y); c != nil && (c.Style.Bg == qrLight || strings.ContainsAny(c.Content, "█▀▄")) {
					t.Fatalf("size %v: a part of the code at %d,%d", sz, x, y)
				}
			}
		}
		if sz[0] >= 36 && sz[1] >= 1 {
			want := fmt.Sprintf("terminal too small: need %d x %d", cols, need)
			if s := text(r.m, sz[0], sz[1]); !strings.Contains(s, want) {
				t.Errorf("size %v: no %q:\n%s", sz, want, s)
			}
		}
	}
	// Exactly the size fits.
	if s := text(r.m, cols, need); strings.Contains(s, "too small") || !strings.Contains(s, "▒") {
		t.Fatalf("the exact size did not draw the code:\n%s", s)
	}
}

func TestEveryDrawStaysInsideItsRegion(t *testing.T) {
	b := &fakeBackend{}
	r := newRig(t, b, testOrigin, 80, 24)
	stages := []func(){
		func() {},
		func() { r.start() },
		func() { r.pending() },
		func() { r.press("c") },
	}
	for _, st := range stages {
		st()
		for _, sz := range [][2]int{{0, 0}, {1, 1}, {3, 2}, {20, 5}, {41, 23}, {80, 24}, {300, 100}} {
			// A canvas larger than the region: nothing is drawn outside the region.
			m := r.m
			m.SetSize(sz[0], sz[1])
			buf := uv.NewBuffer(sz[0]+5, sz[1]+5)
			m.Draw(buf, uv.Rect(2, 2, sz[0], sz[1]))
			for y := 0; y < sz[1]+5; y++ {
				for x := 0; x < sz[0]+5; x++ {
					inside := x >= 2 && x < 2+sz[0] && y >= 2 && y < 2+sz[1]
					if c := buf.CellAt(x, y); !inside && c != nil && c.Content != "" && c.Content != " " {
						t.Fatalf("size %v: a cell outside the region at %d,%d: %q", sz, x, y, c.Content)
					}
				}
			}
		}
	}
}

func TestKeysThatAreNotTheViewsDoNothing(t *testing.T) {
	b := &fakeBackend{}
	r := newRig(t, b, testOrigin, 80, 24)
	r.start()
	for _, k := range []string{"c", "x", "y", "q", "g"} {
		r.press(k)
	}
	r.send(tea.KeyPressMsg{Code: tea.KeyEnter})
	r.send(tea.KeyPressMsg{Code: 'c', Text: "cyx"})
	if r.m.screen != scWaiting || b.count("confirm") != 0 || b.count("reject") != 0 || b.count("start") != 1 {
		t.Fatalf("screen %v calls %v", r.m.screen, b.calls)
	}
	// In the words, c needs the plain key: a chord or a string is not it.
	r.pending()
	for _, k := range []tea.KeyPressMsg{{Code: 'c', Mod: tea.ModAlt, Text: "c"}, {Code: 'c', Text: "cyx"}, {Code: 'x', Mod: tea.ModCtrl}} {
		r.send(k)
		if r.m.screen != scPending || b.count("reject") != 0 {
			t.Fatalf("%+v: screen %v calls %v", k, r.m.screen, b.calls)
		}
	}
	// Caps lock does not change the key.
	r.send(tea.KeyPressMsg{Code: 'c', Text: "c", Mod: tea.ModCapsLock})
	if r.m.screen != scConfirm {
		t.Fatalf("screen %v", r.m.screen)
	}
}

func TestTheBackendIsCalledOnlyFromCommands(t *testing.T) {
	b := &fakeBackend{}
	m := New(Options{Backend: b, Origin: testOrigin})
	m.SetSize(80, 24)
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m.Update(tea.KeyPressMsg{Code: 'c', Text: "c"})
	m.Draw(uv.NewBuffer(80, 24), uv.Rect(0, 0, 80, 24))
	if len(b.calls) != 0 {
		t.Fatalf("the backend was called outside a command: %v", b.calls)
	}
}

// The owner must never confirm with a word off screen or cut: at a size
// where the whole words screen does not fit, c is refused, y re-checks, and
// the view says so (only x and Esc are offered).
func TestWordsThatDoNotFitCannotBeConfirmed(t *testing.T) {
	const msg = "terminal too small to show the eight words"
	for _, sz := range []struct {
		name string
		w, h int
	}{{"80x6", 80, 6}, {"12x20", 12, 20}, {"30x18", 30, 18}} {
		t.Run(sz.name, func(t *testing.T) {
			b := &fakeBackend{}
			r := newRig(t, b, testOrigin, sz.w, sz.h)
			r.start()
			r.pending()
			if r.m.wordsFit() {
				t.Fatal("wordsFit is true")
			}
			r.see(sz.w, sz.h, "too small")
			golden(t, "pending-too-small-"+sz.name, text(r.m, sz.w, sz.h))
			r.press("c")
			if r.m.screen != scPending {
				t.Fatalf("c opened the second question (screen %v)", r.m.screen)
			}
			r.now = r.now.Add(time.Second)
			r.press("y")
			if b.count("confirm") != 0 {
				t.Fatal("confirmed with the words off screen")
			}
			r.notSee(sz.w, sz.h, "c: confirm")
			// Reject stays possible.
			r.press("x")
			if b.count("reject") != 1 {
				t.Fatalf("calls %v, want a reject", b.calls)
			}
		})
	}
	// The full message at a size that holds it.
	b := &fakeBackend{}
	r := newRig(t, b, testOrigin, 80, 6)
	r.start()
	r.pending()
	r.see(80, 6, msg, "x: reject   Esc: close")
}

func TestWordsThatFitCanBeConfirmed(t *testing.T) {
	for _, sz := range [][2]int{{80, 24}, {60, 20}, {30, 20}, {30, 19}} {
		b := &fakeBackend{}
		r := newRig(t, b, testOrigin, sz[0], sz[1])
		r.start()
		r.pending()
		if !r.m.wordsFit() {
			t.Fatalf("%dx%d: wordsFit is false", sz[0], sz[1])
		}
		r.press("c")
		if r.m.screen != scConfirm {
			t.Fatalf("%dx%d: screen %v, want the second question", sz[0], sz[1], r.m.screen)
		}
	}
}

func TestYRechecksThatTheWordsFit(t *testing.T) {
	b := &fakeBackend{}
	r := newRig(t, b, testOrigin, 80, 24)
	r.start()
	r.pending()
	r.press("c")
	r.now = r.now.Add(time.Second)
	// The size shrinks behind the view's back.
	r.m.w, r.m.h = 80, 6
	r.press("y")
	if b.count("confirm") != 0 || r.m.screen == scConfirming {
		t.Fatalf("y confirmed with the words off screen (screen %v, calls %v)", r.m.screen, b.calls)
	}
}

func TestAResizeDropsTheSecondQuestion(t *testing.T) {
	b := &fakeBackend{}
	r := newRig(t, b, testOrigin, 80, 24)
	r.start()
	r.pending()
	r.press("c")
	r.m.SetSize(80, 24) // the same size: nothing changes
	if r.m.screen != scConfirm {
		t.Fatalf("an unchanged size dropped the question (screen %v)", r.m.screen)
	}
	r.m.SetSize(100, 30)
	if r.m.screen != scPending {
		t.Fatalf("a resize left the question open (screen %v)", r.m.screen)
	}
	r.now = r.now.Add(time.Second)
	r.press("y")
	if b.count("confirm") != 0 {
		t.Fatal("y confirmed after a resize")
	}
}

func TestConfirmAndRejectNameThePendingCredential(t *testing.T) {
	b := &fakeBackend{}
	r := newRig(t, b, testOrigin, 80, 24)
	r.start()
	r.pending()
	r.press("c")
	r.now = r.now.Add(time.Second)
	r.press("y")
	if fmt.Sprint(b.ids) != "[confirm:"+testPID+"]" {
		t.Fatalf("ids %v", b.ids)
	}
	b2 := &fakeBackend{}
	r = newRig(t, b2, testOrigin, 80, 24)
	r.start()
	r.pending()
	r.press("x")
	if fmt.Sprint(b2.ids) != "[reject:"+testPID+"]" {
		t.Fatalf("ids %v", b2.ids)
	}
}

func TestAChangedPendingIDDropsTheSecondQuestion(t *testing.T) {
	b := &fakeBackend{}
	r := newRig(t, b, testOrigin, 80, 24)
	r.start()
	r.pending()
	r.press("c")
	// The same words under another credential: not the one the owner saw.
	b.state.PendingID = "fedcba9876543210"
	r.fire()
	if r.m.screen != scPending {
		t.Fatalf("screen %v, want the question dropped", r.m.screen)
	}
	r.now = r.now.Add(time.Second)
	r.press("y")
	if b.count("confirm") != 0 {
		t.Fatalf("calls %v", b.calls)
	}
	// A new question names the new id.
	r.press("c")
	r.now = r.now.Add(time.Second)
	r.press("y")
	if fmt.Sprint(b.ids) != "[confirm:fedcba9876543210]" {
		t.Fatalf("ids %v", b.ids)
	}
}

func TestAPendingIDThatIsNotSixteenHexCannotBeConfirmed(t *testing.T) {
	for _, id := range []string{"", "0123456789ABCDEF", "0123456789abcde", "0123456789abcdef0", "0123456789abcdeg", "../../etc/passwd"} {
		b := &fakeBackend{}
		r := newRig(t, b, testOrigin, 80, 24)
		r.start()
		r.b.state = State{Phase: Pending, PendingID: id, Words: testWords, ExpiresAt: r.now.Add(5 * time.Minute)}
		r.fire()
		r.press("c")
		if r.m.screen == scConfirm {
			t.Errorf("id %q: the second question is open", id)
		}
		r.notSee(80, 24, "c: confirm")
		r.see(80, 24, "x: reject")
	}
}

// A code that does not fit with the quiet zone of 4 modules is drawn with
// the least one (2) when that fits; only a screen too small even for that
// says so.
func TestAQRThatNeedsTheSmallerQuietZoneIsDrawnWithIt(t *testing.T) {
	// A host name of 36 characters makes a version 5 code: 45 x 25 with the
	// zone of 4, 41 x 23 with the zone of 2.
	const origin = "https://a-fairly-long-host-name-here.example"
	r := newRig(t, &fakeBackend{}, origin, 80, 24)
	r.start()
	if cols, need := qrSize(r.m.rows); cols != 45 || need != 25 {
		t.Fatalf("the code with the full zone is %dx%d, want 45x25", cols, need)
	}
	if cols, need := qrSize(r.m.rowsMin); cols != 41 || need != 23 {
		t.Fatalf("the code with the least zone is %dx%d, want 41x23", cols, need)
	}
	s := text(r.m, 80, 24)
	if strings.Contains(s, "too small") || !strings.Contains(s, "▒") {
		t.Fatalf("the code was not drawn at 80x24:\n%s", s)
	}
	golden(t, "waiting-v5-80x24", s)
	// At 41x23 it still fits; one less does not, and says what it needs.
	if s := text(r.m, 41, 23); strings.Contains(s, "too small") || !strings.Contains(s, "▒") {
		t.Fatalf("41x23:\n%s", s)
	}
	if s := text(r.m, 40, 23); !strings.Contains(s, "terminal too small: need 41 x 23") || strings.Contains(s, "▒") {
		t.Fatalf("40x23:\n%s", s)
	}
	// With room for the full zone it is the full zone.
	big := newRig(t, &fakeBackend{}, origin, 80, 40)
	big.start()
	buf := draw(big.m, 80, 40)
	light := 0
	for y := 0; y < 40; y++ {
		if c := buf.CellAt(40, y); c != nil && c.Style.Bg == qrLight {
			light++
		}
	}
	if light != len(big.m.rows) {
		t.Fatalf("the QR is %d rows tall, want %d (the full zone)", light, len(big.m.rows))
	}
}

func TestAnExpiredCodeIsNotLeftDrawnWhenEveryStatusReadFails(t *testing.T) {
	b := &fakeBackend{}
	r := newRig(t, b, testOrigin, 80, 24)
	r.start()
	b.statErr = errBackend
	r.now = r.now.Add(11 * time.Minute)
	r.fire()
	if r.m.screen != scExpired {
		t.Fatalf("screen %v, want expired", r.m.screen)
	}
	r.notSee(80, 24, "▒", "Expires in")
}

func TestATooLongOriginShowsTheOriginLineNotTheDaemons(t *testing.T) {
	// A valid origin of 200 characters: no code of version 10 holds its address.
	origin := "https://" + strings.Repeat("a", 60) + "." + strings.Repeat("b", 60) + "." + strings.Repeat("c", 60) + ".example"
	if _, ok := ValidOrigin(origin); !ok {
		t.Fatal("the test origin is not valid")
	}
	r := newRig(t, &fakeBackend{}, origin, 80, 24)
	r.init()
	r.press("enter")
	if r.m.screen != scFailed {
		t.Fatalf("screen %v", r.m.screen)
	}
	r.see(80, 24, noOrigin)
	r.notSee(80, 24, errCode, "daemon")
}

// The eight words are four to a row where the width holds four columns of the
// widest cell (the numbered word and a gap of four), two to a row below that
// and one to a row below two columns. At 30 columns they are two to a row, the
// whole pending screen is 19 rows (so 30x19 is the smallest size at which the
// owner may confirm), and from 44 columns they are four to a row.
func TestEightWordsLayInColumnsByWidth(t *testing.T) {
	layout := func(w int) (rows, perRow int) {
		r := newRig(t, &fakeBackend{}, testOrigin, w+2, 40)
		r.start()
		r.pending()
		for _, l := range r.m.wordLines(w) {
			if l.k != lWords {
				continue
			}
			rows++
			perRow = len(strings.Fields(l.s)) / 2
		}
		return rows, perRow
	}
	cases := []struct{ w, rows, perRow int }{
		{80, 2, 4}, {44, 2, 4}, {43, 4, 2}, {20, 4, 2}, {19, 8, 1}, {8, 8, 1},
	}
	for _, c := range cases {
		if rows, per := layout(c.w); rows != c.rows || per != c.perRow {
			t.Errorf("width %d: %d rows of %d words, want %d of %d", c.w, rows, per, c.rows, c.perRow)
		}
	}
	if WordCount%4 != 0 {
		t.Errorf("the layouts of 4, 2 and 1 columns need a count they divide: %d", WordCount)
	}
}
