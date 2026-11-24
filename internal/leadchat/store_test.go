//go:build linux && ataniq_canary

package leadchat

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestSwitchOffReadOffRead(t *testing.T) {
	e := newEnv(t)
	s := e.open()
	if st, _ := s.Status(); st.Chat != "off" || st.Gen != 0 || st.Head != 0 {
		t.Fatalf("new store %+v", st)
	}
	if _, err := s.Ingest(1); !errors.Is(err, ErrOff) {
		t.Fatalf("Ingest while off: %v", err)
	}
	if _, _, _, err := s.Read(0); !errors.Is(err, ErrOff) {
		t.Fatalf("Read while off: %v", err)
	}
	if _, err := s.SetSwitch("send", 1); !errors.Is(err, ErrInvalid) {
		t.Fatalf("send: %v", err)
	}
	// Written before chat was on: never taken.
	e.reply("old.txt", "From before.")
	st, err := s.SetSwitch("read", 1)
	if err != nil || st.Chat != "read" || st.Gen != 1 || st.Head != 1 {
		t.Fatalf("on: %+v, %v", st, err)
	}
	if n := e.names(); len(n) != 0 {
		t.Fatalf("the turn-on left %q", n)
	}
	if l := e.lines(testRole); !strings.Contains(l[1], `"discarded":1`) {
		t.Fatalf("switch line %s", l[1])
	}
	// Setting the same switch again changes nothing.
	if st, err := s.SetSwitch("read", 1); err != nil || st.Gen != 1 {
		t.Fatalf("again: %+v, %v", st, err)
	}
	e.reply("a.txt", "During.")
	ingest(t, s)
	wantTexts(t, s, TextTurnedOn, "During.")

	st, err = s.SetSwitch("off", 1)
	if err != nil || st.Chat != "off" || st.Gen != 2 || st.Head != 2 || st.Entries != 0 {
		t.Fatalf("off: %+v, %v", st, err)
	}
	if l := e.lines(testRole); len(l) != 2 || countKind(l, "entry") != 0 || !strings.Contains(l[0], `"next":3`) {
		t.Fatalf("file after off %q", l)
	}
	e.reply("b.txt", "While off.")
	if _, err := s.Ingest(1); !errors.Is(err, ErrOff) {
		t.Fatalf("Ingest while off: %v", err)
	}
	st, err = s.SetSwitch("read", 2)
	if err != nil || st.Gen != 3 || st.Head != 3 {
		t.Fatalf("on again: %+v, %v", st, err)
	}
	ingest(t, s)
	es := read(t, s)
	if !slices.Equal(texts(es), []string{TextTurnedOn}) || es[0].Seq != 3 || es[0].Epoch != 2 {
		t.Fatalf("entries %+v", es)
	}
}

func TestRestartReturnsToOff(t *testing.T) {
	e := newEnv(t)
	s := e.on()
	e.reply("a.txt", "Before the restart.")
	ingest(t, s)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = e.open()
	st, err := s.Status()
	if err != nil || st.Chat != "off" || st.Entries != 0 || st.Head != 2 || st.Gen != 2 {
		t.Fatalf("after restart %+v, %v", st, err)
	}
	if _, _, _, err := s.Read(0); !errors.Is(err, ErrOff) {
		t.Fatalf("Read: %v", err)
	}
	l := e.lines(testRole)
	if countKind(l, "entry") != 0 || strings.Contains(strings.Join(l, ""), "Before the restart") {
		t.Fatalf("file after restart %q", l)
	}
	// A store that was off stays as it is: no compaction, no new line.
	_ = s.Close()
	e.open()
	if l2 := e.lines(testRole); !slices.Equal(l, l2) {
		t.Fatalf("an off store changed at open:\n%q\n%q", l, l2)
	}
}

func TestSequenceGrowsAcrossRestarts(t *testing.T) {
	e := newEnv(t)
	s := e.on()
	e.reply("a.txt", "One.")
	e.reply("b.txt", "Two.")
	ingest(t, s)
	_ = s.Close()
	s = e.open()
	if _, err := s.SetSwitch("read", 2); err != nil {
		t.Fatal(err)
	}
	if es := read(t, s); es[0].Seq != 4 {
		t.Fatalf("seq after restart %d", es[0].Seq)
	}
	_ = s.Close()

	// The floor of the open line counts even with no entry.
	if err := os.WriteFile(e.chatFile("scratch-beta-lead"), []byte(`{"kind":"open","v":1,"next":100}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	e.makeOutbox("scratch-beta-lead")
	b, err := e.openRole("scratch-beta-lead")
	if err != nil {
		t.Fatal(err)
	}
	if st, err := b.SetSwitch("read", 1); err != nil || st.Head != 100 {
		t.Fatalf("floor: %+v, %v", st, err)
	}
}

func TestReadCursor(t *testing.T) {
	e := newEnv(t)
	s := e.on()
	for i := 0; i < 4; i++ {
		e.reply(fmt.Sprintf("%d.txt", i), fmt.Sprintf("Reply %d.", i))
	}
	ingest(t, s) // seqs 1 to 5
	cases := []struct {
		after int64
		first int64
		n     int
		reset bool
	}{
		{0, 1, 5, false}, {1, 2, 4, false}, {4, 5, 1, false}, {5, 0, 0, false}, {6, 1, 5, true},
	}
	for _, c := range cases {
		es, reset, head, err := s.Read(c.after)
		if err != nil || head != 5 || reset != c.reset || len(es) != c.n || (c.n > 0 && es[0].Seq != c.first) {
			t.Errorf("after %d: %v entries from %v, reset %v, head %d, %v", c.after, len(es), seqs(es), reset, head, err)
		}
	}
	if _, _, _, err := s.Read(-1); !errors.Is(err, ErrInvalid) {
		t.Fatalf("negative cursor: %v", err)
	}
	// After entries expired, a cursor before what is held resets.
	s.entries = s.entries[2:]
	if es, reset, _, _ := s.Read(1); !reset || es[0].Seq != 3 {
		t.Fatalf("before the start: reset %v, %v", reset, seqs(es))
	}
	if es, reset, _, _ := s.Read(2); reset || es[0].Seq != 3 {
		t.Fatalf("at the start: reset %v, %v", reset, seqs(es))
	}
}

func TestRetentionByAge(t *testing.T) {
	e := newEnv(t)
	s := e.on()
	e.reply("a.txt", "Old.")
	ingest(t, s)
	e.clock.add(RetainAge - time.Hour)
	e.reply("b.txt", "Newer.")
	ingest(t, s)
	e.clock.add(2 * time.Hour)
	// The old reply expired; the turned-on entry is older still and kept.
	wantTexts(t, s, TextTurnedOn, "Newer.")
	if st, _ := s.Status(); st.Head != 3 {
		t.Fatalf("head %d", st.Head)
	}
	// Reads touch no file; an ingest compacts once the hour has passed.
	before := e.lines(testRole)
	if countKind(before, "entry") != 3 {
		t.Fatalf("file before compaction %q", before)
	}
	ingest(t, s)
	after := e.lines(testRole)
	if countKind(after, "entry") != 2 || strings.Contains(strings.Join(after, ""), "Old.") {
		t.Fatalf("file after compaction %q", after)
	}
	if !strings.Contains(after[0], `"next":4`) || !strings.Contains(after[1], `"kind":"switch"`) {
		t.Fatalf("compacted head %q", after[:2])
	}
}

func TestRetentionByCount(t *testing.T) {
	e := newEnv(t)
	s := e.on()
	for i := 0; i < RetainEntries+100; i++ {
		if err := s.addEntries(e.clock.now(), 1, Entry{Who: "lead", Text: fmt.Sprintf("Reply %d.", i), src: fmt.Sprintf("%032x", i)}); err != nil {
			t.Fatal(err)
		}
	}
	es := read(t, s)
	if len(es) != RetainEntries || es[0].Text != TextTurnedOn || es[1].Text != "Reply 101." || es[len(es)-1].Text != fmt.Sprintf("Reply %d.", RetainEntries+99) {
		t.Fatalf("%d entries, first %q %q, last %q", len(es), es[0].Text, es[1].Text, es[len(es)-1].Text)
	}
	e.clock.add(compactEvery)
	ingest(t, s)
	if n := countKind(e.lines(testRole), "entry"); n != RetainEntries {
		t.Fatalf("%d entries in the file after compaction", n)
	}
}

func TestTornTailIsCut(t *testing.T) {
	e := newEnv(t)
	s := e.open()
	_ = s.Close()
	whole := e.lines(testRole)
	f, err := os.OpenFile(e.chatFile(testRole), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"kind":"switch","chat":"re`)
	f.Close()
	e.open()
	if got := e.lines(testRole); !slices.Equal(got, whole) {
		t.Fatalf("after the cut %q, want %q", got, whole)
	}
	data, _ := os.ReadFile(e.chatFile(testRole))
	if !strings.HasSuffix(string(data), "\n") {
		t.Fatalf("file %q", data)
	}
}

func TestTornFirstLine(t *testing.T) {
	e := newEnv(t)
	if err := os.Mkdir(filepath.Join(e.dir, ChatDirName), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.chatFile(testRole), []byte(`{"kind":"op`), 0o600); err != nil {
		t.Fatal(err)
	}
	e.open()
	if l := e.lines(testRole); len(l) != 1 || l[0] != `{"kind":"open","v":1,"next":1}` {
		t.Fatalf("file %q", l)
	}
}

func TestCorruptFileFailsThatLeadOnly(t *testing.T) {
	open := `{"kind":"open","v":1,"next":1}`
	sw := `{"kind":"switch","chat":"read","gen":1,"at":"2026-01-01T00:00:00Z","epoch":1,"discarded":0}`
	entry := func(seq int, extra string) string {
		return fmt.Sprintf(`{"kind":"entry","seq":%d,"gen":1,"epoch":1,"who":"system","at":"2026-01-01T00:00:00Z","text":"x"%s}`, seq, extra)
	}
	cases := map[string][]string{
		"garbage line":           {open, "not json", sw},
		"unknown member":         {open, strings.Replace(sw, `"discarded":0`, `"discarded":0,"extra":1`, 1)},
		"missing member":         {open, strings.Replace(sw, `,"discarded":0`, ``, 1)},
		"member of another kind": {open, sw, entry(1, `,"next":2`)},
		"unknown kind":           {open, `{"kind":"other"}`},
		"no open line":           {sw},
		"second open line":       {open, open},
		"wrong version":          {strings.Replace(open, `"v":1`, `"v":2`, 1)},
		"generation not grown":   {open, sw, sw},
		"entry while off":        {open, entry(1, "")},
		"sequence not grown":     {open, sw, entry(2, ""), entry(2, "")},
		"sequence under floor":   {strings.Replace(open, `"next":1`, `"next":5`, 1), sw, entry(3, "")},
		"bad time":               {open, strings.Replace(sw, "00Z", "00+00:00", 1)},
		"text not plain":         {open, sw, strings.Replace(entry(1, ""), `"text":"x"`, `"text":"a\tb"`, 1)},
		"lead entry without src": {open, sw, strings.Replace(entry(1, ""), `"system"`, `"lead"`, 1)},
		"bad src":                {open, sw, entry(1, `,"src":"XYZ"`)},
		"data after the record":  {open + ` {}`},
		"line too long":          {open, `{"kind":"other","pad":"` + strings.Repeat("a", MaxLineBytes) + `"}`},
	}
	for name, lines := range cases {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			e.makeOutbox("scratch-beta-lead")
			if err := os.Mkdir(filepath.Join(e.dir, ChatDirName), 0o700); err != nil {
				t.Fatal(err)
			}
			data := strings.Join(lines, "\n") + "\n"
			if err := os.WriteFile(e.chatFile(testRole), []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := e.openRole(testRole); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("OpenStore: %v", err)
			}
			if got, _ := os.ReadFile(e.chatFile(testRole)); string(got) != data {
				t.Fatalf("the corrupt file was changed")
			}
			other, err := e.openRole("scratch-beta-lead")
			if err != nil {
				t.Fatalf("the other lead: %v", err)
			}
			if _, err := other.SetSwitch("read", 1); err != nil {
				t.Fatalf("the other lead: %v", err)
			}
		})
	}
}

func TestStoreFileChecks(t *testing.T) {
	t.Run("mode", func(t *testing.T) {
		e := newEnv(t)
		_ = e.open().Close()
		if err := os.Chmod(e.chatFile(testRole), 0o640); err != nil {
			t.Fatal(err)
		}
		if _, err := e.openRole(testRole); !errors.Is(err, ErrInsecure) {
			t.Fatalf("OpenStore: %v", err)
		}
	})
	t.Run("hard link", func(t *testing.T) {
		e := newEnv(t)
		_ = e.open().Close()
		if err := os.Link(e.chatFile(testRole), filepath.Join(e.dir, "copy")); err != nil {
			t.Fatal(err)
		}
		if _, err := e.openRole(testRole); !errors.Is(err, ErrInsecure) {
			t.Fatalf("OpenStore: %v", err)
		}
	})
	t.Run("symbolic link", func(t *testing.T) {
		e := newEnv(t)
		_ = e.open().Close()
		real := filepath.Join(e.dir, "real.jsonl")
		if err := os.Rename(e.chatFile(testRole), real); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(real, e.chatFile(testRole)); err != nil {
			t.Fatal(err)
		}
		if _, err := e.openRole(testRole); !errors.Is(err, ErrInsecure) {
			t.Fatalf("OpenStore: %v", err)
		}
	})
	t.Run("lock", func(t *testing.T) {
		e := newEnv(t)
		e.open()
		if _, err := e.openRole(testRole); !errors.Is(err, ErrLocked) {
			t.Fatalf("second OpenStore: %v", err)
		}
	})
	t.Run("folder mode", func(t *testing.T) {
		e := newEnv(t)
		if err := os.Mkdir(filepath.Join(e.dir, ChatDirName), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(e.dir, ChatDirName), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := e.openRole(testRole); !errors.Is(err, ErrInsecure) {
			t.Fatalf("OpenStore: %v", err)
		}
	})
	t.Run("folder link", func(t *testing.T) {
		e := newEnv(t)
		other := filepath.Join(e.dir, "other")
		if err := os.Mkdir(other, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(other, filepath.Join(e.dir, ChatDirName)); err != nil {
			t.Fatal(err)
		}
		if _, err := e.openRole(testRole); !errors.Is(err, ErrInsecure) {
			t.Fatalf("OpenStore: %v", err)
		}
	})
	t.Run("new folder and file modes", func(t *testing.T) {
		e := newEnv(t)
		e.open()
		for p, want := range map[string]os.FileMode{filepath.Join(e.dir, ChatDirName): 0o700, e.chatFile(testRole): 0o600} {
			if fi, err := os.Lstat(p); err != nil || fi.Mode().Perm() != want {
				t.Fatalf("%s: %v, %v", filepath.Base(p), fi.Mode(), err)
			}
		}
	})
	t.Run("role", func(t *testing.T) {
		e := newEnv(t)
		if _, err := e.openRole("../escape"); !errors.Is(err, ErrInvalid) {
			t.Fatalf("OpenStore: %v", err)
		}
	})
}

func TestCompactionTempFile(t *testing.T) {
	e := newEnv(t)
	if err := os.Mkdir(filepath.Join(e.dir, ChatDirName), 0o700); err != nil {
		t.Fatal(err)
	}
	tmp := e.chatFile(testRole) + ".tmp"
	if err := os.WriteFile(tmp, []byte("left by a crash"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := e.on()
	if _, err := os.Lstat(tmp); !os.IsNotExist(err) {
		t.Fatalf("the old temporary file is still there: %v", err)
	}
	// A crash in a compaction before the rename leaves the temporary file and
	// the old chat file; the switch is off at once and the store is broken.
	e.reply("a.txt", "Kept until off.")
	ingest(t, s)
	s.beforeSwap = func() error { return errors.New("synthetic crash") }
	if _, err := s.SetSwitch("off", 1); !errors.Is(err, ErrBroken) {
		t.Fatalf("SetSwitch off: %v", err)
	}
	if _, _, _, err := s.Read(0); !errors.Is(err, ErrBroken) {
		t.Fatalf("Read after a failed off: %v", err)
	}
	if s.chat != "off" || len(s.entries) != 0 {
		t.Fatalf("memory after a failed off: %s, %d entries", s.chat, len(s.entries))
	}
	if _, err := os.Lstat(tmp); err != nil {
		t.Fatalf("no temporary file: %v", err)
	}
	_ = s.Close()
	s = e.open()
	if _, err := os.Lstat(tmp); !os.IsNotExist(err) {
		t.Fatalf("the temporary file is still there: %v", err)
	}
	if st, _ := s.Status(); st.Chat != "off" || st.Entries != 0 || st.Head != 2 {
		t.Fatalf("status %+v", st)
	}
}

func TestFailedWriteBreaksThatLeadOnly(t *testing.T) {
	for _, what := range []string{"write", "sync"} {
		t.Run(what, func(t *testing.T) {
			e := newEnv(t)
			e.makeOutbox("scratch-beta-lead")
			s := e.on()
			fail := errors.New("synthetic disk error")
			if what == "write" {
				s.write = func(*os.File, []byte) (int, error) { return 0, fail }
			} else {
				s.sync = func(*os.File) error { return fail }
			}
			e.reply("a.txt", "Lost.")
			if _, err := s.Ingest(1); !errors.Is(err, ErrBroken) {
				t.Fatalf("Ingest: %v", err)
			}
			if _, err := s.Status(); !errors.Is(err, ErrBroken) {
				t.Fatalf("Status: %v", err)
			}
			if _, _, _, err := s.Read(0); !errors.Is(err, ErrBroken) {
				t.Fatalf("Read: %v", err)
			}
			if _, err := s.SetSwitch("off", 1); !errors.Is(err, ErrBroken) {
				t.Fatalf("SetSwitch: %v", err)
			}
			// The reply was not removed: nothing acted on a write that failed.
			if n := e.names(); !slices.Equal(n, []string{"a.txt"}) {
				t.Fatalf("outbox %q", n)
			}
			other, err := e.openRole("scratch-beta-lead")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := other.SetSwitch("read", 1); err != nil {
				t.Fatalf("the other lead: %v", err)
			}
		})
	}
}

func TestClosedStore(t *testing.T) {
	e := newEnv(t)
	s := e.open()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); !errors.Is(err, ErrClosed) {
		t.Fatalf("second Close: %v", err)
	}
	if _, err := s.SetSwitch("read", 1); !errors.Is(err, ErrClosed) {
		t.Fatalf("SetSwitch: %v", err)
	}
}
