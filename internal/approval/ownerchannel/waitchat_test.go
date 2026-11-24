package ownerchannel

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"ataniq/internal/approval/ownerwire"
)

// fakeCursor is a chat port's cursor: the cursor of the enabled leads, which
// a test sets, and a broadcast that it closes when it says the cursor may
// have changed. err and panics make it fail.
type fakeCursor struct {
	mu     sync.Mutex
	leads  map[string]ownerwire.ChatCursorLead
	ch     chan struct{}
	err    error
	panics bool
	reads  int
}

func newFakeCursor(leads ...ownerwire.ChatCursorLead) *fakeCursor {
	f := &fakeCursor{leads: map[string]ownerwire.ChatCursorLead{}, ch: make(chan struct{})}
	for _, l := range leads {
		f.leads[l.Lead] = l
	}
	return f
}

func (f *fakeCursor) ChatCursor(ctx context.Context) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if f.panics {
		panic("the cursor's source broke")
	}
	if f.err != nil {
		return 0, f.err
	}
	return f.cursorLocked(), nil
}

func (f *fakeCursor) cursorLocked() int64 {
	var all []ownerwire.ChatCursorLead
	for _, l := range f.leads {
		all = append(all, l)
	}
	return ownerwire.ChatCursor(testKey, all)
}

// testKey is the fake's cursor key; the daemon's is random.
var testKey = ownerwire.NewChatCursorKey()

func (f *fakeCursor) cursor() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cursorLocked()
}

func (f *fakeCursor) ChatCursorChanged() <-chan struct{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ch
}

// change applies edit and wakes the waits, whether or not the cursor moved.
func (f *fakeCursor) change(edit func(map[string]ownerwire.ChatCursorLead)) {
	f.mu.Lock()
	if edit != nil {
		edit(f.leads)
	}
	close(f.ch)
	f.ch = make(chan struct{})
	f.mu.Unlock()
}

func (f *fakeCursor) set(fn func(*fakeCursor)) {
	f.mu.Lock()
	fn(f)
	f.mu.Unlock()
}

var (
	leadA = ownerwire.ChatCursorLead{Lead: "daemon:alpha", Gen: 1, Head: 4}
	leadB = ownerwire.ChatCursorLead{Lead: "daemon:bravo", Gen: 2, Head: 0}
)

// startChatWait runs an extended wait in the background.
func (w *waitEnv) startChatWait(ctx context.Context, since string, chat int64, src ChatCursor) <-chan waitOut {
	out := make(chan waitOut, 1)
	go func() {
		r, err := w.svc.WaitPendingChat(ctx, since, chat, src)
		out <- waitOut{r, err}
	}()
	return out
}

func chatOf(t *testing.T, r ownerwire.SeatWaitResult) int64 {
	t.Helper()
	if r.Chat == nil {
		t.Fatalf("the result carries no chat: %+v", r)
	}
	if _, err := ownerwire.EncodeSeatWaitChatResult(r); err != nil {
		t.Fatalf("the result is not one the wire takes: %v", err)
	}
	return *r.Chat
}

func (w *waitEnv) lists() int {
	w.door.mu.Lock()
	defer w.door.mu.Unlock()
	return w.door.lists
}

// A seat with no cursor gets one at once, with the Card result; one with no
// version gets both at once too.
func TestWaitChatAnswersACursorAtOnceToASeatWithNone(t *testing.T) {
	w := newWaitEnv(t)
	w.card("feature-a")
	src := newFakeCursor(leadA, leadB)
	first := w.now(t)
	got := answered(t, w.startChatWait(context.Background(), first.Version, 0, src))
	if got.err != nil || got.res.Version != first.Version || chatOf(t, got.res) != src.cursor() {
		t.Fatalf("answered %+v", got)
	}
	got = answered(t, w.startChatWait(context.Background(), "", src.cursor(), src))
	if got.err != nil || got.res.Version != first.Version || chatOf(t, got.res) != src.cursor() {
		t.Fatalf("with no version: %+v", got)
	}
	if !reflect.DeepEqual(got.res.Cards, first.Cards) {
		t.Fatalf("the Card result is %+v, want %+v", got.res.Cards, first.Cards)
	}
}

// A held extended wait with nothing changed answers at the end of the hold,
// with the same version and the same cursor.
func TestWaitChatHoldsAndAnswersTheSameCursor(t *testing.T) {
	w := newWaitEnv(t)
	src := newFakeCursor(leadA)
	first := w.now(t)
	out := w.startChatWait(context.Background(), first.Version, src.cursor(), src)
	w.held(t, 1)
	got, moved := w.drive(t, out, time.Second, 40)
	if got.err != nil || got.res.Version != first.Version || chatOf(t, got.res) != src.cursor() {
		t.Fatalf("answered %+v", got)
	}
	if moved < defaultWaitHold {
		t.Fatalf("answered after %s, before the hold", moved)
	}
	if w.wc.active() != 0 {
		t.Fatalf("%d timers are left", w.wc.active())
	}
}

// offRun is one run of an extended wait held over an idle desk, with during,
// if not nil, run while it is held, and what it answered and when.
func offRun(t *testing.T, during func(src *fakeCursor)) (ownerwire.SeatWaitResult, time.Duration, []time.Duration, int) {
	w := newWaitEnv(t)
	w.card("feature-a")
	src := newFakeCursor(leadA)
	first := w.now(t)
	base := w.lists()
	out := w.startChatWait(context.Background(), first.Version, src.cursor(), src)
	w.held(t, 1)
	asked := len(w.wc.asks())
	var moved time.Duration
	for i := 0; i < 40; i++ {
		if during != nil && i < 20 {
			during(src)
		}
		select {
		case r := <-out:
			if r.err != nil {
				t.Fatal(r.err)
			}
			return r.res, moved, w.wc.asks()[asked:], w.lists() - base
		case <-time.After(15 * time.Millisecond):
		}
		w.wc.Advance(time.Second)
		moved += time.Second
	}
	r := answered(t, out)
	if r.err != nil {
		t.Fatal(r.err)
	}
	return r.res, moved, w.wc.asks()[asked:], w.lists() - base
}

// A lead whose chat is off is not in the cursor, so its activity, even when
// the source wakes the wait for it, changes nothing: the answer, the time it
// comes, the timers the wait set and the looks at the door are those of an
// idle desk.
func TestWaitChatOffOnlyActivityIsInvisible(t *testing.T) {
	idle, idleAt, idleTimers, idleLooks := offRun(t, nil)
	busy, busyAt, busyTimers, busyLooks := offRun(t, func(src *fakeCursor) {
		// An off lead wrote: nothing the cursor covers moved, but the
		// source wakes the waits all the same.
		src.change(nil)
		src.change(nil)
	})
	if !reflect.DeepEqual(idle, busy) {
		t.Errorf("an off lead's activity changed the answer:\n idle %+v\n busy %+v", idle, busy)
	}
	if idleAt != busyAt {
		t.Errorf("an off lead's activity changed the time of the answer: %s, idle %s", busyAt, idleAt)
	}
	if !reflect.DeepEqual(idleTimers, busyTimers) {
		t.Errorf("an off lead's activity changed the timers: %v, idle %v", busyTimers, idleTimers)
	}
	if idleLooks != busyLooks {
		t.Errorf("an off lead's activity made %d looks at the door, idle %d", busyLooks, idleLooks)
	}
	if busyAt < defaultWaitHold {
		t.Errorf("answered after %s, before the hold", busyAt)
	}
}

// A change of the chat answers once WaitChatGap has passed since the wait
// began, with the new cursor and the Card result as it was, and makes no
// look at the door.
func TestWaitChatChangeAnswersAfterTheGapWithoutALook(t *testing.T) {
	w := newWaitEnv(t)
	w.card("feature-a")
	src := newFakeCursor(leadA)
	first := w.now(t)
	before := src.cursor()
	out := w.startChatWait(context.Background(), first.Version, before, src)
	w.held(t, 1)
	base := w.lists()
	src.change(func(m map[string]ownerwire.ChatCursorLead) { l := m[leadA.Lead]; l.Head++; m[leadA.Lead] = l })
	w.held(t, 2) // the hold and the gap
	got, moved := w.drive(t, out, time.Second, 40)
	if got.err != nil {
		t.Fatal(got.err)
	}
	if c := chatOf(t, got.res); c == before || c != src.cursor() {
		t.Fatalf("the cursor is %d, want the new %d (was %d)", c, src.cursor(), before)
	}
	if got.res.Version != first.Version || !reflect.DeepEqual(got.res.Cards, first.Cards) {
		t.Fatalf("the Card result changed: %+v", got.res)
	}
	if moved < WaitChatGap-time.Second || moved > WaitChatGap+time.Second {
		t.Fatalf("answered after %s, want about %s", moved, WaitChatGap)
	}
	if n := w.lists() - base; n != 0 {
		t.Fatalf("a chat change made %d looks at the door", n)
	}
	if w.wc.active() != 0 {
		t.Fatalf("%d timers are left", w.wc.active())
	}
}

// A cursor that is already not the seat's when the wait is armed is
// answered after the gap too, not at once: a busy lead cannot make the seat
// spin.
func TestWaitChatStaleCursorWaitsTheGap(t *testing.T) {
	w := newWaitEnv(t)
	src := newFakeCursor(leadA)
	first := w.now(t)
	out := w.startChatWait(context.Background(), first.Version, ownerwire.ChatCursor(testKey, nil), src)
	w.held(t, 2)
	got, moved := w.drive(t, out, time.Second, 40)
	if got.err != nil || chatOf(t, got.res) != src.cursor() {
		t.Fatalf("answered %+v", got)
	}
	if moved < WaitChatGap-time.Second {
		t.Fatalf("answered after %s, before the gap", moved)
	}
}

// A Card change answers at once while a chat change waits out its gap, and
// the answer carries both: the new version and the new cursor.
func TestWaitChatCardAndChatChangeTogether(t *testing.T) {
	w := newWaitEnv(t)
	w.card("feature-a")
	src := newFakeCursor(leadA)
	first := w.now(t)
	before := src.cursor()
	out := w.startChatWait(context.Background(), first.Version, before, src)
	w.held(t, 1)
	src.change(func(m map[string]ownerwire.ChatCursorLead) { m[leadB.Lead] = leadB })
	w.held(t, 2)
	w.card("feature-b")
	got, moved := w.drive(t, out, time.Second, 40)
	if got.err != nil {
		t.Fatal(got.err)
	}
	if moved >= WaitChatGap {
		t.Fatalf("a Card change waited %s, the chat's gap", moved)
	}
	if got.res.Version == first.Version || got.res.Waiting != 2 {
		t.Fatalf("the Card change is not in the answer: %+v", got.res)
	}
	if c := chatOf(t, got.res); c != src.cursor() || c == before {
		t.Fatalf("the cursor is %d, want %d", c, src.cursor())
	}
	// The Card version is the plain wait's: the chat is not in it.
	if got.res.Version != ownerwire.PendingVersion(got.res.Cards) {
		t.Fatal("the version is not the Card set's alone")
	}
}

// A lead whose head moved and that was then switched off before the answer
// leaves no trace: the answer's cursor is that of the leads enabled when it
// is given, the same as if the head had never moved. One that comes back to
// the seat's cursor before the gap is not answered for the chat at all.
func TestWaitChatSwitchOffBeforeTheAnswerPurgesTheHint(t *testing.T) {
	w := newWaitEnv(t)
	src := newFakeCursor(leadA, leadB)
	first := w.now(t)
	before := src.cursor()
	out := w.startChatWait(context.Background(), first.Version, before, src)
	w.held(t, 1)
	src.change(func(m map[string]ownerwire.ChatCursorLead) { l := m[leadA.Lead]; l.Head += 3; m[leadA.Lead] = l })
	w.held(t, 2)
	w.wc.Advance(5 * time.Second)
	src.change(func(m map[string]ownerwire.ChatCursorLead) { delete(m, leadA.Lead) })
	got, _ := w.drive(t, out, time.Second, 40)
	if got.err != nil {
		t.Fatal(got.err)
	}
	if c, want := chatOf(t, got.res), ownerwire.ChatCursor(testKey, []ownerwire.ChatCursorLead{leadB}); c != want {
		t.Fatalf("the cursor is %d, want %d, that of the lead still enabled", c, want)
	}

	// Moved and moved back within the gap: the cursor is the seat's again,
	// and the wait holds to its end.
	src = newFakeCursor(leadA, leadB)
	out = w.startChatWait(context.Background(), first.Version, before, src)
	w.held(t, 1)
	src.change(func(m map[string]ownerwire.ChatCursorLead) { l := m[leadA.Lead]; l.Head++; m[leadA.Lead] = l })
	w.held(t, 2)
	src.change(func(m map[string]ownerwire.ChatCursorLead) { l := m[leadA.Lead]; l.Head--; m[leadA.Lead] = l })
	until(t, "the gap to be let go", func() bool { return w.wc.active() == 1 })
	got, moved := w.drive(t, out, time.Second, 40)
	if got.err != nil || chatOf(t, got.res) != before {
		t.Fatalf("answered %+v", got)
	}
	if moved < defaultWaitHold {
		t.Fatalf("answered after %s, before the hold", moved)
	}
}

// A source that fails or panics gives no cursor: the answer is the plain
// Card result, without chat, and the wait holds on the Cards as the plain
// one does; it does not answer at once to a seat with no cursor, so it
// cannot spin.
func TestWaitChatFailingSourceFailsClosed(t *testing.T) {
	for name, broken := range map[string]func(*fakeCursor){
		"an error": func(f *fakeCursor) { f.err = errors.New("the desk's chat is down") },
		"a panic":  func(f *fakeCursor) { f.panics = true },
	} {
		t.Run(name, func(t *testing.T) {
			w := newWaitEnv(t)
			w.card("feature-a")
			src := newFakeCursor(leadA)
			src.set(broken)
			first := w.now(t)
			got := answered(t, w.startChatWait(context.Background(), "", 0, src))
			if got.err != nil || got.res.Chat != nil || got.res.Version != first.Version {
				t.Fatalf("answered %+v", got)
			}
			if _, err := ownerwire.EncodeSeatWaitChatResult(got.res); err != nil {
				t.Fatal(err)
			}
			out := w.startChatWait(context.Background(), first.Version, 0, src)
			w.held(t, 1)
			src.change(nil)
			got, moved := w.drive(t, out, time.Second, 40)
			if got.err != nil || got.res.Chat != nil {
				t.Fatalf("held: %+v", got)
			}
			if moved < defaultWaitHold {
				t.Fatalf("answered after %s, before the hold", moved)
			}
			// A Card change still answers at once.
			out = w.startChatWait(context.Background(), first.Version, 0, src)
			w.held(t, 1)
			w.card("feature-b")
			got, moved = w.drive(t, out, time.Second, 40)
			if got.err != nil || got.res.Chat != nil || got.res.Waiting != 2 || moved >= defaultWaitHold {
				t.Fatalf("a Card change: %+v after %s", got, moved)
			}
		})
	}
	// A source that breaks after the change was seen answers without chat.
	w := newWaitEnv(t)
	src := newFakeCursor(leadA)
	first := w.now(t)
	before := src.cursor()
	out := w.startChatWait(context.Background(), first.Version, before, src)
	w.held(t, 1)
	w.card("feature-c")
	src.set(func(f *fakeCursor) { f.panics = true })
	got, _ := w.drive(t, out, time.Second, 40)
	if got.err != nil || got.res.Chat != nil || got.res.Waiting != 1 {
		t.Fatalf("answered %+v", got)
	}
}

// The extended and the plain waits count together against the service's
// limit, and a cancelled extended wait leaves no timer.
func TestWaitChatSharesTheLimit(t *testing.T) {
	w := newWaitEnv(t)
	w.svc.cfg.WaitMax = 2
	src := newFakeCursor(leadA)
	first := w.now(t)
	ctx, cancel := context.WithCancel(context.Background())
	outA := w.startChatWait(ctx, first.Version, src.cursor(), src)
	outB := w.startWait(ctx, first.Version)
	w.held(t, 2)
	if _, err := w.svc.WaitPendingChat(context.Background(), "", 0, src); !errors.Is(err, ErrWaitFull) {
		t.Fatalf("a third wait answered %v, want ErrWaitFull", err)
	}
	cancel()
	for _, o := range []<-chan waitOut{outA, outB} {
		if got := answered(t, o); !errors.Is(got.err, context.Canceled) {
			t.Fatalf("answered %+v", got)
		}
	}
	until(t, "the timers to stop", func() bool { return w.wc.active() == 0 })
}

// The plain wait is untouched by a chat source: it never carries chat.
func TestWaitPlainCarriesNoChat(t *testing.T) {
	w := newWaitEnv(t)
	r := w.now(t)
	if r.Chat != nil {
		t.Fatalf("the plain wait answered chat: %+v", r)
	}
	if _, err := ownerwire.EncodeSeatWaitResult(r); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(reflect.TypeOf(w.svc).String()), "chat") {
		t.Fatal("the service's type names chat")
	}
}
