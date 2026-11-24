package ownerchannel

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/approval/record"
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
	// onRead, if set, runs in each read, under the lock, after the cursor
	// was taken: it may change the leads and close the channel, as a change
	// that comes just after a read does.
	onRead func(f *fakeCursor, n int)
	// failFrom makes every read from the failFrom-th on fail, when not 0.
	failFrom int
	// subPanics makes ChatCursorChanged panic.
	subPanics bool
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
	if f.err != nil || (f.failFrom > 0 && f.reads >= f.failFrom) {
		return 0, errors.New("the desk's chat is down")
	}
	v := f.cursorLocked()
	if f.onRead != nil {
		f.onRead(f, f.reads)
	}
	return v, nil
}

func (f *fakeCursor) readCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reads
}

// changeLocked is change with the lock held.
func (f *fakeCursor) changeLocked(edit func(map[string]ownerwire.ChatCursorLead)) {
	if edit != nil {
		edit(f.leads)
	}
	close(f.ch)
	f.ch = make(chan struct{})
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
	if f.subPanics {
		panic("the cursor's change channel broke")
	}
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

// chatScene is the Card and chat setting of an off-only run: a card that
// expires expiry after the wait begins (0 for none within the hold), and a
// seat whose cursor is stale, so that the chat's gap runs.
type chatScene struct {
	expiry time.Duration
	stale  bool
}

// chatRun is what a run shows: the answer, when it came, the timers made and
// stopped and the looks at the door, each at its time from the wait's start.
type chatRun struct {
	res   ownerwire.SeatWaitResult
	at    time.Duration
	ops   []string
	looks []time.Duration
	// wakes counts the off-only wakes the wait took.
	wakes int
}

// offRun runs an extended wait in scene, the clock moved in steps of 100
// milliseconds. With busy, a lead whose chat is off is active: before every
// fifth step its port wakes the wait twice, each wake taken by the wait
// before the next (it sampled the cursor again and is waiting once more)
// while the wait is waiting. Each step that fires a timer waits for the wait
// to wait again, or answer, before the next.
func offRun(t *testing.T, scene chatScene, busy bool) chatRun {
	t.Helper()
	w := newWaitEnv(t)
	a := w.card("feature-a")
	if scene.expiry > 0 {
		expires, err := record.ParseTime(a.Binding.PendingExpiresAt)
		if err != nil {
			t.Fatal(err)
		}
		// w.now moves the clock a second more.
		w.wc.Advance(expires.Sub(w.clock.Now()) - scene.expiry - waitFloor)
	}
	src := newFakeCursor(leadA)
	first := w.now(t)
	chat := src.cursor()
	if scene.stale {
		chat = ownerwire.ChatCursor(testKey, nil)
	}
	var parks atomic.Int64
	parked := make(chan struct{}, 1)
	w.svc.wait.parked = func() {
		parks.Add(1)
		select {
		case parked <- struct{}{}:
		default:
		}
	}
	w.wc.record()
	t0 := w.clock.Now()
	w.door.mu.Lock()
	looked := len(w.door.listAt)
	w.door.mu.Unlock()
	out := w.startChatWait(context.Background(), first.Version, chat, src)
	var got *waitOut
	// settle waits for the wait to wait again after a wake, or to answer.
	// A wake that leaves it in the floor's wait for a look parks it later,
	// after the step that ends the floor; settle then gives up after a
	// second of real time and says so.
	settle := func(since int64) bool {
		deadline := time.After(time.Second)
		for got == nil && parks.Load() <= since {
			select {
			case r := <-out:
				got = &r
			case <-parked:
			case <-deadline:
				return false
			}
		}
		return true
	}
	if !settle(0) {
		t.Fatal("the wait never waited")
	}
	run := chatRun{}
	waiting := true
	for step := 0; got == nil && step < 400; step++ {
		if busy && waiting && step%5 == 0 {
			for i := 0; i < 2; i++ {
				before, reads := parks.Load(), src.readCount()
				src.change(nil)
				if !settle(before) || got != nil {
					t.Fatalf("an off-only wake at %s was not taken", run.at)
				}
				if src.readCount() == reads {
					t.Fatalf("an off-only wake at %s did not sample the cursor", run.at)
				}
				run.wakes++
			}
		}
		before := parks.Load()
		fired := w.wc.Advance(100 * time.Millisecond)
		run.at += 100 * time.Millisecond
		if fired > 0 {
			waiting = settle(before)
		}
	}
	if got == nil {
		r := answered(t, out)
		got = &r
	}
	if got.err != nil {
		t.Fatal(got.err)
	}
	run.res, run.ops = got.res, w.wc.opLog()
	w.door.mu.Lock()
	for _, at := range w.door.listAt[looked:] {
		run.looks = append(run.looks, at.Sub(t0))
	}
	w.door.mu.Unlock()
	return run
}

// A lead whose chat is off is not in the cursor, so its activity, even when
// the port wakes the wait for it, changes nothing: the answer, the time it
// comes, every timer the wait makes or stops, with its time, and every look
// at the door, with its time, are those of an idle desk. That holds with a
// Card that expires within the hold, with the chat's gap running, with both,
// and with neither.
func TestWaitChatOffOnlyActivityIsInvisible(t *testing.T) {
	for name, scene := range map[string]chatScene{
		"an idle hold":                      {},
		"a Card that expires":               {expiry: 10 * time.Second},
		"the chat's gap":                    {stale: true},
		"a Card that expires and the gap":   {expiry: 10 * time.Second, stale: true},
		"the gap, then a Card that expires": {expiry: 21 * time.Second, stale: true},
	} {
		t.Run(name, func(t *testing.T) {
			idle := offRun(t, scene, false)
			busy := offRun(t, scene, true)
			t.Logf("idle: answered %+v after %s, timers %q, looks %v; busy took %d off-only wakes", idle.res, idle.at, idle.ops, idle.looks, busy.wakes)
			if busy.wakes == 0 {
				t.Fatal("the busy run took no off-only wake")
			}
			if !reflect.DeepEqual(idle.res, busy.res) {
				t.Errorf("an off lead's activity changed the answer:\n idle %+v\n busy %+v", idle.res, busy.res)
			}
			if idle.at != busy.at {
				t.Errorf("an off lead's activity changed the time of the answer: %s, idle %s", busy.at, idle.at)
			}
			if !reflect.DeepEqual(idle.ops, busy.ops) {
				t.Errorf("an off lead's activity changed the timers:\n busy %q\n idle %q", busy.ops, idle.ops)
			}
			if !reflect.DeepEqual(idle.looks, busy.looks) {
				t.Errorf("an off lead's activity changed the looks at the door: %v, idle %v", busy.looks, idle.looks)
			}
			switch {
			case scene.expiry > 0 && scene.expiry < WaitChatGap:
				if idle.res.Waiting != 0 || idle.at > scene.expiry+2*time.Second {
					t.Errorf("the Card's expiry answered %+v after %s", idle.res, idle.at)
				}
			case scene.stale:
				if idle.at < WaitChatGap || idle.at > WaitChatGap+time.Second {
					t.Errorf("the gap answered after %s", idle.at)
				}
			default:
				if idle.at < defaultWaitHold {
					t.Errorf("answered after %s, before the hold", idle.at)
				}
			}
		})
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

// The cursor that answers a seat with none at once is the one sample that
// decided to answer: a source whose second read fails still gives it, and
// is read once.
func TestWaitChatBootstrapIsOneSample(t *testing.T) {
	w := newWaitEnv(t)
	w.card("feature-a")
	src := newFakeCursor(leadA)
	first := w.now(t)
	want := src.cursor()
	src.set(func(f *fakeCursor) { f.failFrom = 2 })
	got := answered(t, w.startChatWait(context.Background(), first.Version, 0, src))
	if got.err != nil || chatOf(t, got.res) != want {
		t.Fatalf("answered %+v, want the cursor %d", got, want)
	}
	if n := src.readCount(); n != 1 {
		t.Fatalf("the source was read %d times, want once", n)
	}
}

// A sample after which the port's channel closed (a change just after the
// read) is taken again before it is answered: at once to a seat with none,
// and when the chat's gap ends. The answer is the cursor after the change.
func TestWaitChatAnswersAValidatedSample(t *testing.T) {
	moveA := func(f *fakeCursor, n int) {
		if n == 1 {
			f.changeLocked(func(m map[string]ownerwire.ChatCursorLead) { l := m[leadA.Lead]; l.Head += 5; m[leadA.Lead] = l })
		}
	}
	t.Run("at once", func(t *testing.T) {
		w := newWaitEnv(t)
		src := newFakeCursor(leadA)
		first := w.now(t)
		before := src.cursor()
		src.set(func(f *fakeCursor) { f.onRead = moveA })
		got := answered(t, w.startChatWait(context.Background(), first.Version, 0, src))
		if c := chatOf(t, got.res); got.err != nil || c == before || c != src.cursor() {
			t.Fatalf("answered %d (%v), want the cursor after the change %d, not %d", c, got.err, src.cursor(), before)
		}
	})
	t.Run("at the end of the gap", func(t *testing.T) {
		w := newWaitEnv(t)
		src := newFakeCursor(leadA)
		first := w.now(t)
		stale := ownerwire.ChatCursor(testKey, nil)
		out := w.startChatWait(context.Background(), first.Version, stale, src)
		w.held(t, 2)
		before := src.cursor()
		base := src.readCount()
		src.set(func(f *fakeCursor) {
			f.onRead = func(f *fakeCursor, n int) { moveA(f, n-base) }
		})
		got, moved := w.drive(t, out, time.Second, 40)
		if got.err != nil || moved < WaitChatGap-time.Second {
			t.Fatalf("answered %+v after %s", got, moved)
		}
		if c := chatOf(t, got.res); c == before || c != src.cursor() {
			t.Fatalf("the gap answered %d, want the cursor after the change %d, not %d", c, src.cursor(), before)
		}
	})
}

// A port whose change channel cannot be taken (ChatCursorChanged panics),
// though its cursor can be read, is the plain wait's behaviour: no answer
// carries chat, a seat with no cursor is held as any other, a Card change
// answers at once, and the cursor is not read.
func TestWaitChatAPanickingSubscriptionIsTheCardOnlyWait(t *testing.T) {
	w := newWaitEnv(t)
	w.card("feature-a")
	src := newFakeCursor(leadA)
	src.set(func(f *fakeCursor) { f.subPanics = true })
	first := w.now(t)
	got := answered(t, w.startChatWait(context.Background(), "", 0, src))
	if got.err != nil || got.res.Chat != nil || got.res.Version != first.Version {
		t.Fatalf("with no version: %+v", got)
	}
	out := w.startChatWait(context.Background(), first.Version, 0, src)
	w.held(t, 1)
	got, moved := w.drive(t, out, time.Second, 40)
	if got.err != nil || got.res.Chat != nil || moved < defaultWaitHold {
		t.Fatalf("a seat with no cursor: %+v after %s", got, moved)
	}
	out = w.startChatWait(context.Background(), first.Version, src.cursor(), src)
	w.held(t, 1)
	w.card("feature-b")
	got, moved = w.drive(t, out, time.Second, 40)
	if got.err != nil || got.res.Chat != nil || got.res.Waiting != 2 || moved >= defaultWaitHold {
		t.Fatalf("a Card change: %+v after %s", got, moved)
	}
	if n := src.readCount(); n != 0 {
		t.Fatalf("the cursor was read %d times", n)
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
