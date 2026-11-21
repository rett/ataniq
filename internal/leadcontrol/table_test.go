//go:build linux && ataniq_canary

package leadcontrol

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"ataniq/internal/leadrole"
)

func TestReplayReturnsTheOriginalReceipt(t *testing.T) {
	tb := openAt(t, tablePath(t), Options{})
	b := bind(t, "cmd-1")
	first := admitFresh(t, tb, b)
	if first.Status != StatusInProgress || first.CommandID != "cmd-1" || first.Op != "lead.start" ||
		first.Role != "scratch-alpha/lead" || first.FinishedAt != nil || first.AdmittedAt.IsZero() {
		t.Fatalf("first receipt = %+v", first)
	}
	// While in progress, a replay returns the stored receipt and runs nothing.
	r, fresh, err := tb.Admit(b)
	if err != nil || fresh {
		t.Fatalf("replay in progress: fresh %v, err %v", fresh, err)
	}
	sameReceipt(t, r, first)

	done, err := tb.Finish("cmd-1", StatusDone, "", "", map[string]any{"epoch": 3, "unit": "ataniq-lead-scratch-alpha.service"})
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != StatusDone || done.FinishedAt == nil || string(done.Result) != `{"epoch":3,"unit":"ataniq-lead-scratch-alpha.service"}` {
		t.Fatalf("done receipt = %+v", done)
	}
	// After the finish, a replay returns exactly the final answer, again and again.
	for i := 0; i < 3; i++ {
		r, fresh, err = tb.Admit(b)
		if err != nil || fresh {
			t.Fatalf("replay after finish: fresh %v, err %v", fresh, err)
		}
		sameReceipt(t, r, done)
	}
}

func TestReplayOfEveryFinalStatusNeverRuns(t *testing.T) {
	tb := openAt(t, tablePath(t), Options{})
	for i, st := range []Status{StatusDone, StatusRefused, StatusUnavailable, StatusUncertain} {
		b := bind(t, fmt.Sprintf("id-%d", i))
		admitFresh(t, tb, b)
		want, err := tb.Finish(b.ID, st, "some-code", "A fixed text.", nil)
		if err != nil {
			t.Fatal(err)
		}
		r, fresh, err := tb.Admit(b)
		if err != nil || fresh {
			t.Fatalf("%s: fresh %v, err %v", st, fresh, err)
		}
		sameReceipt(t, r, want)
		if r.Code != "some-code" || r.Message != "A fixed text." {
			t.Fatalf("%s: code %q, message %q", st, r.Code, r.Message)
		}
	}
}

func TestChangedPayloadConflicts(t *testing.T) {
	other := mustRole(t, "scratch-beta/lead")
	for name, mod := range map[string]func(Binding) Binding{
		"op":     func(b Binding) Binding { b.Op = "lead.stop"; return b },
		"role":   func(b Binding) Binding { b.Role = other; return b },
		"digest": func(b Binding) Binding { b.Digest = digestOf("another payload"); return b },
	} {
		for _, final := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s final=%v", name, final), func(t *testing.T) {
				tb := openAt(t, tablePath(t), Options{})
				b := bind(t, "cmd-1")
				first := admitFresh(t, tb, b)
				if final {
					first = finish(t, tb, "cmd-1", StatusDone)
				}
				_, fresh, err := tb.Admit(mod(b))
				if !errors.Is(err, ErrConflict) || fresh {
					t.Fatalf("fresh %v, err %v; want ErrConflict", fresh, err)
				}
				// The refusal left the original binding as it was.
				r, fresh, err := tb.Admit(b)
				if err != nil || fresh {
					t.Fatalf("original after conflict: fresh %v, err %v", fresh, err)
				}
				sameReceipt(t, r, first)
			})
		}
	}
}

func TestSameIDOfAnotherClientIsAnotherKey(t *testing.T) {
	tb := openAt(t, tablePath(t), Options{})
	a := bind(t, "cmd-1")
	admitFresh(t, tb, a)
	finish(t, tb, "cmd-1", StatusDone)
	b := a
	b.Client = "c2"
	// The first command is final, so its id may be used again by another client.
	admitFresh(t, tb, b)
	// An open id may not: Finish names a command by its id alone.
	c := a
	c.Client = "c3"
	if _, fresh, err := tb.Admit(c); !errors.Is(err, ErrConflict) || fresh {
		t.Fatalf("open id of another client: fresh %v, err %v; want ErrConflict", fresh, err)
	}
	finish(t, tb, "cmd-1", StatusDone)
	admitFresh(t, tb, c)
}

func TestAdmitRefusesAnInvalidBinding(t *testing.T) {
	tb := openAt(t, tablePath(t), Options{})
	for name, mod := range map[string]func(*Binding){
		"empty client":        func(b *Binding) { b.Client = "" },
		"long client":         func(b *Binding) { b.Client = strings.Repeat("c", 65) },
		"client with space":   func(b *Binding) { b.Client = "a b" },
		"client with newline": func(b *Binding) { b.Client = "a\nb" },
		"client non-ASCII":    func(b *Binding) { b.Client = "café" },
		"empty id":            func(b *Binding) { b.ID = "" },
		"long id":             func(b *Binding) { b.ID = strings.Repeat("i", 65) },
		"id with slash":       func(b *Binding) { b.ID = "a/b" },
		"id with space":       func(b *Binding) { b.ID = "a b" },
		"empty op":            func(b *Binding) { b.Op = "" },
		"op upper case":       func(b *Binding) { b.Op = "Lead.Start" },
		"zero role":           func(b *Binding) { b.Role = leadrole.Role{} },
	} {
		b := bind(t, "cmd-1")
		mod(&b)
		if _, _, err := tb.Admit(b); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	// The longest legal values pass.
	b := bind(t, strings.Repeat("A", 64))
	b.Client = strings.Repeat("~", 64)
	admitFresh(t, tb, b)
	b = bind(t, "x.y_z:w-1")
	b.Op = "lead.inject"
	admitFresh(t, tb, b)
}

func TestFinish(t *testing.T) {
	tb := openAt(t, tablePath(t), Options{})
	if _, err := tb.Finish("nobody", StatusDone, "", "", nil); !errors.Is(err, ErrNotOpen) {
		t.Fatalf("unknown id: %v", err)
	}
	admitFresh(t, tb, bind(t, "cmd-1"))
	for name, fn := range map[string]func() error{
		"in-progress":  func() error { _, err := tb.Finish("cmd-1", StatusInProgress, "", "", nil); return err },
		"empty status": func() error { _, err := tb.Finish("cmd-1", "", "", "", nil); return err },
		"bad code":     func() error { _, err := tb.Finish("cmd-1", StatusRefused, "Bad Code", "", nil); return err },
		"long code": func() error {
			_, err := tb.Finish("cmd-1", StatusRefused, strings.Repeat("a", 65), "", nil)
			return err
		},
		"bad message": func() error { _, err := tb.Finish("cmd-1", StatusRefused, "", "line\nbreak", nil); return err },
		"long message": func() error {
			_, err := tb.Finish("cmd-1", StatusRefused, "", strings.Repeat("m", 257), nil)
			return err
		},
		"unmarshalable result": func() error { _, err := tb.Finish("cmd-1", StatusDone, "", "", make(chan int)); return err },
	} {
		if err := fn(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	// A line over the cap is refused and leaves the command open.
	big := strings.Repeat("x", MaxLineBytes)
	if _, err := tb.Finish("cmd-1", StatusDone, "", "", map[string]string{"big": big}); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("big result: %v, want ErrTooLarge", err)
	}
	// A result of 3000 bytes with the longest message still fits.
	if _, err := tb.Finish("cmd-1", StatusDone, "", strings.Repeat("m", MaxMessageBytes), map[string]string{"x": strings.Repeat("x", 3000)}); err != nil {
		t.Fatalf("a 3000 byte result: %v", err)
	}
	if _, err := tb.Finish("cmd-1", StatusDone, "", "", nil); !errors.Is(err, ErrNotOpen) {
		t.Fatalf("second finish: %v, want ErrNotOpen", err)
	}
}

func TestFinishResultForms(t *testing.T) {
	tb := openAt(t, tablePath(t), Options{})
	admitFresh(t, tb, bind(t, "a"))
	r, err := tb.Finish("a", StatusDone, "", "", json.RawMessage(" { \"k\" : \"<&>\" } "))
	if err != nil {
		t.Fatal(err)
	}
	if string(r.Result) != `{"k":"<&>"}` {
		t.Fatalf("result = %s", r.Result)
	}
	admitFresh(t, tb, bind(t, "b"))
	r, err = tb.Finish("b", StatusDone, "", "", nil)
	if err != nil || r.Result != nil {
		t.Fatalf("nil result: %v, %v", r.Result, err)
	}
	admitFresh(t, tb, bind(t, "c"))
	if r, err = tb.Finish("c", StatusDone, "", "", json.RawMessage("null")); err != nil || r.Result != nil {
		t.Fatalf("null result: %v, %v", r.Result, err)
	}
}

func TestReceiptsAreCopies(t *testing.T) {
	tb := openAt(t, tablePath(t), Options{})
	b := bind(t, "a")
	admitFresh(t, tb, b)
	done, err := tb.Finish("a", StatusDone, "", "", map[string]int{"n": 1})
	if err != nil {
		t.Fatal(err)
	}
	done.Result[2] = 'X'
	*done.FinishedAt = done.AdmittedAt
	r, _, _ := tb.Admit(b)
	if string(r.Result) != `{"n":1}` || r.FinishedAt.Equal(r.AdmittedAt) {
		t.Fatalf("a caller changed the stored receipt: %+v", r)
	}
}

func TestLast(t *testing.T) {
	tb := openAt(t, tablePath(t), Options{})
	alpha, beta := mustRole(t, "scratch-alpha/lead"), mustRole(t, "scratch-beta/lead")
	if _, ok := tb.Last(alpha); ok {
		t.Fatal("Last of an empty table")
	}
	admitFresh(t, tb, bind(t, "a1"))
	bb := bind(t, "b1")
	bb.Role = beta
	admitFresh(t, tb, bb)
	a2 := admitFresh(t, tb, bind(t, "a2"))
	if r, ok := tb.Last(alpha); !ok || r.CommandID != "a2" {
		t.Fatalf("Last(alpha) = %+v, %v", r, ok)
	}
	finish(t, tb, "a2", StatusDone)
	if r, ok := tb.Last(alpha); !ok || r.Status != StatusDone || !r.AdmittedAt.Equal(a2.AdmittedAt) {
		t.Fatalf("Last(alpha) after finish = %+v", r)
	}
	if r, ok := tb.Last(beta); !ok || r.CommandID != "b1" {
		t.Fatalf("Last(beta) = %+v", r)
	}
	if _, ok := tb.Last(mustRole(t, "scratch-gamma/lead")); ok {
		t.Fatal("Last of a role with no command")
	}
}

func TestConcurrentAdmitOfOneKey(t *testing.T) {
	path := tablePath(t)
	tb := openAt(t, path, Options{})
	b := bind(t, "same")
	const n = 48
	var wg sync.WaitGroup
	var mu sync.Mutex
	fresh := 0
	receipts := make([]Receipt, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, f, err := tb.Admit(b)
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			receipts[i] = r
			if f {
				fresh++
			}
			mu.Unlock()
		}()
	}
	wg.Wait()
	if fresh != 1 {
		t.Fatalf("%d admissions were fresh, want exactly 1", fresh)
	}
	for i := 1; i < n; i++ {
		sameReceipt(t, receipts[i], receipts[0])
	}
	if lines := fileLines(t, path); len(lines) != 1 {
		t.Fatalf("the file has %d lines, want one admit line", len(lines))
	}
}

func TestConcurrentAdmitAndFinishOfManyKeys(t *testing.T) {
	path := tablePath(t)
	tb := openAt(t, path, Options{})
	const n = 40
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b := bind(t, fmt.Sprintf("cmd-%d", i))
			if _, fresh, err := tb.Admit(b); err != nil || !fresh {
				t.Errorf("Admit %d: fresh %v, err %v", i, fresh, err)
				return
			}
			if _, err := tb.Finish(b.ID, StatusDone, "", "", map[string]int{"i": i}); err != nil {
				t.Errorf("Finish %d: %v", i, err)
			}
		}()
	}
	wg.Wait()
	if err := tb.Close(); err != nil {
		t.Fatal(err)
	}
	// Whatever the interleaving, every line is whole and the file replays.
	re := openAt(t, path, Options{})
	for i := 0; i < n; i++ {
		r, fresh, err := re.Admit(bind(t, fmt.Sprintf("cmd-%d", i)))
		if err != nil || fresh || r.Status != StatusDone || string(r.Result) != fmt.Sprintf(`{"i":%d}`, i) {
			t.Fatalf("cmd-%d after reopen: %+v, fresh %v, err %v", i, r, fresh, err)
		}
	}
}

func TestFullTable(t *testing.T) {
	path := tablePath(t)
	tb := openAt(t, path, Options{MaxReceipts: 3})
	for _, id := range []string{"a", "b", "c"} {
		admitFresh(t, tb, bind(t, id))
	}
	before := fileLines(t, path)
	_, fresh, err := tb.Admit(bind(t, "d"))
	if !errors.Is(err, ErrFull) || fresh {
		t.Fatalf("fourth open command: fresh %v, err %v; want ErrFull", fresh, err)
	}
	if after := fileLines(t, path); len(after) != len(before) {
		t.Fatalf("a full table wrote %d lines", len(after)-len(before))
	}
	// A replay of a held key is not an admission and still answers.
	if r, fresh, err := tb.Admit(bind(t, "b")); err != nil || fresh || r.Status != StatusInProgress {
		t.Fatalf("replay in a full table: %+v, %v, %v", r, fresh, err)
	}
	// One final receipt makes room: it is dropped, and its key is expired.
	finish(t, tb, "a", StatusDone)
	admitFresh(t, tb, bind(t, "d"))
	if _, _, err := tb.Admit(bind(t, "a")); !errors.Is(err, ErrExpired) {
		t.Fatalf("dropped key: %v, want ErrExpired", err)
	}
	if _, _, err := tb.Admit(bind(t, "e")); !errors.Is(err, ErrFull) {
		t.Fatalf("every slot open again: %v, want ErrFull", err)
	}
}

func TestEvictionKeepsReceiptsThatAreNotFinal(t *testing.T) {
	tb := openAt(t, tablePath(t), Options{MaxReceipts: 3})
	a, b := bind(t, "open-a"), bind(t, "open-b")
	ra := admitFresh(t, tb, a)
	admitFresh(t, tb, b)
	admitFresh(t, tb, bind(t, "c"))
	finish(t, tb, "c", StatusDone)
	// Each new command drops the only final receipt, never an open one.
	for i, id := range []string{"d", "e", "f"} {
		admitFresh(t, tb, bind(t, id))
		finish(t, tb, id, StatusDone)
		if i == 0 {
			if _, _, err := tb.Admit(bind(t, "c")); !errors.Is(err, ErrExpired) {
				t.Fatalf("c after d: %v, want ErrExpired", err)
			}
		}
	}
	r, fresh, err := tb.Admit(a)
	if err != nil || fresh || r.Status != StatusInProgress || !r.AdmittedAt.Equal(ra.AdmittedAt) {
		t.Fatalf("the open receipt after eviction: %+v, %v, %v", r, fresh, err)
	}
	if _, fresh, err := tb.Admit(b); err != nil || fresh {
		t.Fatalf("the second open receipt after eviction: %v, %v", fresh, err)
	}
	if _, ok := tb.Last(mustRole(t, "scratch-alpha/lead")); !ok {
		t.Fatal("Last lost the role")
	}
}

func TestAWriteFailureClosesTheTableForGood(t *testing.T) {
	path := tablePath(t)
	tb := openAt(t, path, Options{})
	admitFresh(t, tb, bind(t, "ok"))
	tb.write = func([]byte) (int, error) { return 0, errors.New("disk full") }
	if _, _, err := tb.Admit(bind(t, "lost")); !errors.Is(err, ErrBroken) {
		t.Fatalf("Admit: %v, want ErrBroken", err)
	}
	tb.write = tb.f.Write
	// Nothing was applied, and the table takes nothing more.
	if _, _, err := tb.Admit(bind(t, "later")); !errors.Is(err, ErrBroken) {
		t.Fatalf("Admit after the failure: %v, want ErrBroken", err)
	}
	if _, err := tb.Finish("ok", StatusDone, "", "", nil); !errors.Is(err, ErrBroken) {
		t.Fatalf("Finish after the failure: %v, want ErrBroken", err)
	}
	// A replay of a key the table holds is answered with its receipt.
	if r, fresh, err := tb.Admit(bind(t, "ok")); err != nil || fresh || r.Status != StatusInProgress {
		t.Fatalf("a replay after the failure: %+v, %v, %v; want the receipt in progress", r, fresh, err)
	}
	_ = tb.Close()

	re := openAt(t, path, Options{})
	re.sync = func() error { return errors.New("input/output error") }
	if _, _, err := re.Admit(bind(t, "lost-2")); !errors.Is(err, ErrBroken) {
		t.Fatalf("sync failure: %v, want ErrBroken", err)
	}
	_ = re.Close()

	// What the failed sync left is a whole admit line with no finish: the next
	// Open ends it as interrupted, so a client that asks again gets no second run.
	re = openAt(t, path, Options{})
	re.write = func(b []byte) (int, error) { return re.f.Write(b[:len(b)-1]) }
	if _, _, err := re.Admit(bind(t, "lost-3")); !errors.Is(err, ErrBroken) {
		t.Fatalf("short write: %v, want ErrBroken", err)
	}
	_ = re.Close()
	// The short write left a torn line at the end: it is cut, not corrupt.
	re = openAt(t, path, Options{})
	if r, fresh, err := re.Admit(bind(t, "lost-2")); err != nil || fresh || r.Status != StatusUncertain {
		t.Fatalf("lost-2 after the failed sync: %+v, %v, %v", r, fresh, err)
	}
	if _, fresh, err := re.Admit(bind(t, "lost-3")); err != nil || !fresh {
		t.Fatalf("lost-3 after the short write: %v, %v", fresh, err)
	}
}

func TestClosedTable(t *testing.T) {
	tb := openAt(t, tablePath(t), Options{})
	admitFresh(t, tb, bind(t, "a"))
	if err := tb.Close(); err != nil {
		t.Fatal(err)
	}
	if err := tb.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, _, err := tb.Admit(bind(t, "b")); !errors.Is(err, ErrClosed) {
		t.Fatalf("Admit: %v, want ErrClosed", err)
	}
	if _, err := tb.Finish("a", StatusDone, "", "", nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("Finish: %v, want ErrClosed", err)
	}
}

func TestNegativeBoundsAreRefused(t *testing.T) {
	for _, o := range []Options{{MaxReceipts: -1}, {MaxTombstones: -1}} {
		if _, err := Open(tablePath(t), o); !errors.Is(err, ErrInvalid) {
			t.Errorf("Open(%+v): %v, want ErrInvalid", o, err)
		}
	}
}

// A command that was admitted and whose finish could not be written keeps its
// receipt: a replay is answered with it, not with "nothing was admitted".
func TestReplayOfAnAdmittedCommandOnABrokenTable(t *testing.T) {
	path := tablePath(t)
	tb := openAt(t, path, Options{})
	b := bind(t, "k1")
	admitFresh(t, tb, b)
	tb.write = func([]byte) (int, error) { return 0, errors.New("disk full") }
	if _, err := tb.Finish("k1", StatusDone, "", "", nil); !errors.Is(err, ErrBroken) {
		t.Fatalf("Finish: %v, want ErrBroken", err)
	}
	r, fresh, err := tb.Admit(b)
	if err != nil || fresh || r.Status != StatusInProgress || r.CommandID != "k1" {
		t.Fatalf("replay: %+v, fresh %v, err %v; want the receipt in progress", r, fresh, err)
	}
	// The same key with another payload is still a conflict.
	other := b
	other.Digest = digestOf("another payload")
	if _, _, err := tb.Admit(other); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed payload: %v, want ErrConflict", err)
	}
	// Nothing new is admitted, and the command stays open for no one: a finish
	// fails too.
	if _, _, err := tb.Admit(bind(t, "k2")); !errors.Is(err, ErrBroken) {
		t.Fatalf("a new key: %v, want ErrBroken", err)
	}
	if _, err := tb.Finish("k1", StatusDone, "", "", nil); !errors.Is(err, ErrBroken) {
		t.Fatalf("Finish again: %v, want ErrBroken", err)
	}
	// A closed table answers nothing.
	_ = tb.Close()
	if _, _, err := tb.Admit(b); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed: %v, want ErrClosed", err)
	}
}
