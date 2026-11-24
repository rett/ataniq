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
}

// readCase is one read of a cursor test: the cursor, the sequence numbers
// answered and the reset.
type readCase struct {
	after int64
	want  []int64
	reset bool
}

func checkReads(t *testing.T, s *Store, cases []readCase) {
	t.Helper()
	for _, c := range cases {
		es, reset, _, err := s.Read(c.after)
		if err != nil || reset != c.reset || !slices.Equal(seqs(es), c.want) {
			t.Errorf("after %d: %v, reset %v, %v; want %v, reset %v", c.after, seqs(es), reset, err, c.want, c.reset)
		}
	}
}

// After entries expired, the store holds the pinned turned-on entry and the
// newest: a cursor in the gap, the pinned entry's gap included, resets, and
// a reader that pages on from an entry it was given goes on.
func TestReadCursorAfterExpiry(t *testing.T) {
	e := newEnv(t)
	s := e.on() // the turned-on entry is 1
	for i := 0; i < 98; i++ {
		if err := s.addEntries(e.clock.now(), 1, Entry{Who: "lead", Text: "Old.", src: fmt.Sprintf("%032x", i)}); err != nil {
			t.Fatal(err)
		}
	}
	e.clock.add(RetainAge + time.Hour)
	for i := 0; i < 2; i++ {
		if err := s.addEntries(e.clock.now(), 1, Entry{Who: "lead", Text: "New.", src: fmt.Sprintf("%032x", 100+i)}); err != nil {
			t.Fatal(err)
		}
	}
	checkReads(t, s, []readCase{
		{0, []int64{1, 100, 101}, false},
		{1, []int64{100, 101}, false}, // paging on from the turned-on entry
		{50, []int64{1, 100, 101}, true},
		{2, []int64{1, 100, 101}, true},
		{99, []int64{1, 100, 101}, true},
		{100, []int64{101}, false},
		{101, []int64{}, false},
		{102, []int64{1, 100, 101}, true},
	})
}

// A cursor of an earlier segment resets, even the one just before the new
// turned-on entry.
func TestReadCursorOfAnEarlierSegment(t *testing.T) {
	e := newEnv(t)
	s := e.on()
	e.reply("a.txt", "First segment.")
	ingest(t, s) // 1 and 2
	if _, err := s.SetSwitch("off", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetSwitch("read", 1); err != nil {
		t.Fatal(err)
	}
	checkReads(t, s, []readCase{
		{2, []int64{3}, true},
		{1, []int64{3}, true},
		{3, []int64{}, false},
		{0, []int64{3}, false},
	})
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
	// The newest entry expires too: the high-water stays.
	e.clock.add(RetainAge)
	ingest(t, s)
	wantTexts(t, s, TextTurnedOn)
	if l := e.lines(testRole); countKind(l, "entry") != 1 || !strings.Contains(l[0], `"next":4`) {
		t.Fatalf("file after the newest expired %q", l)
	}
	// The compacted file replays.
	_ = s.Close()
	s = e.open()
	if st, err := s.Status(); err != nil || st.Head != 3 || st.Chat != "off" {
		t.Fatalf("after reopen %+v, %v", st, err)
	}
	if st, err := s.SetSwitch("read", 1); err != nil || st.Head != 4 {
		t.Fatalf("on after reopen %+v, %v", st, err)
	}
}

// A compaction that kept the turned-on entry and newer entries above it, with
// a gap between, replays: the entries are checked in their own order.
func TestCompactedFileReplaysWithAppendsAfterIt(t *testing.T) {
	e := newEnv(t)
	s := e.on()
	e.reply("a.txt", "Old.")
	ingest(t, s)
	e.clock.add(RetainAge + compactEvery)
	e.reply("b.txt", "Newer.")
	ingest(t, s) // compacts (1 kept, 2 gone), then appends 3
	l := e.lines(testRole)
	if !strings.Contains(l[0], `"next":3`) || countKind(l, "entry") != 2 {
		t.Fatalf("file %q", l)
	}
	_ = s.Close()
	s = e.open()
	if st, err := s.Status(); err != nil || st.Head != 3 {
		t.Fatalf("after reopen %+v, %v", st, err)
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
	head := int64(RetainEntries + 101)
	if st, _ := s.Status(); st.Head != head {
		t.Fatalf("head %d", st.Head)
	}
	// The compacted file replays.
	_ = s.Close()
	s = e.open()
	if st, err := s.Status(); err != nil || st.Head != head || st.Chat != "off" {
		t.Fatalf("after reopen %+v, %v", st, err)
	}
}

func TestJournalBound(t *testing.T) {
	t.Run("an append that would pass it compacts first", func(t *testing.T) {
		e := newEnv(t)
		s := e.on()
		before := len(e.lines(testRole))
		s.size = MaxJournalBytes - 10
		s.stale = true
		if err := s.addEntries(e.clock.now(), 1, Entry{Who: "lead", Text: "Fits.", src: fmt.Sprintf("%032x", 1)}); err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(e.chatFile(testRole))
		if s.size != int64(len(data)) || len(e.lines(testRole)) != before+1 {
			t.Fatalf("size %d, file %d bytes", s.size, len(data))
		}
	})
	t.Run("an append that would still pass it is refused", func(t *testing.T) {
		e := newEnv(t)
		s := e.on()
		s.size = MaxJournalBytes
		if err := s.appendData([]byte("{}\n")); !errors.Is(err, ErrFull) {
			t.Fatalf("appendData: %v", err)
		}
		if _, err := s.Status(); err != nil {
			t.Fatalf("a refused append broke the store: %v", err)
		}
	})
	t.Run("a replay refuses a larger file", func(t *testing.T) {
		e := newEnv(t)
		if err := os.Mkdir(filepath.Join(e.dir, ChatDirName), 0o700); err != nil {
			t.Fatal(err)
		}
		var b strings.Builder
		b.WriteString(`{"kind":"open","v":1,"next":1}` + "\n")
		b.WriteString(`{"kind":"switch","chat":"read","gen":1,"at":"2026-01-01T00:00:00Z","epoch":1,"discarded":0}` + "\n")
		b.WriteString(`{"kind":"entry","seq":1,"gen":1,"epoch":1,"who":"system","at":"2026-01-01T00:00:00Z","text":"` + TextTurnedOn + `"}` + "\n")
		text := strings.Repeat("x", MaxReplyBytes)
		for seq := 2; b.Len() <= MaxJournalBytes; seq++ {
			fmt.Fprintf(&b, `{"kind":"entry","seq":%d,"gen":1,"epoch":1,"who":"lead","at":"2026-01-01T00:00:00Z","text":"%s","src":"%032x"}`+"\n", seq, text, seq)
		}
		if err := os.WriteFile(e.chatFile(testRole), []byte(b.String()), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := e.openRole(testRole); !errors.Is(err, ErrCorrupt) || !strings.Contains(err.Error(), "larger than the bound") {
			t.Fatalf("OpenStore: %v", err)
		}
	})
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
	marker := `{"kind":"entry","seq":1,"gen":1,"epoch":1,"who":"system","at":"2026-01-01T00:00:00Z","text":"` + TextTurnedOn + `"}`
	lead := func(seq int, extra string) string {
		return fmt.Sprintf(`{"kind":"entry","seq":%d,"gen":1,"epoch":1,"who":"lead","at":"2026-01-01T00:00:00Z","text":"x"%s}`, seq, extra)
	}
	src := `,"src":"0123456789abcdef0123456789abcdef"`
	// padded is the open line padded with spaces to n bytes, its newline
	// included.
	padded := func(n int) string { return open[:len(open)-1] + strings.Repeat(" ", n-1-len(open)) + "}" }
	cases := []struct {
		name   string
		lines  []string
		reason string
	}{
		{"garbage line", []string{open, "not json", sw}, "not one JSON object"},
		{"unknown member", []string{open, strings.Replace(sw, `"discarded":0`, `"discarded":0,"extra":1`, 1)}, "does not have"},
		{"missing member", []string{open, strings.Replace(sw, `,"discarded":0`, ``, 1)}, "is missing"},
		{"member of another kind", []string{open, sw, marker, lead(2, src+`,"next":2`)}, "does not have"},
		{"member twice", []string{open, strings.Replace(sw, `"gen":1`, `"gen":1,"gen":1`, 1)}, "appears twice"},
		{"kind twice", []string{open, strings.Replace(sw, `"kind":"switch"`, `"kind":"switch","kind":"switch"`, 1)}, "appears twice"},
		{"null scalar", []string{open, strings.Replace(sw, `"epoch":1`, `"epoch":null`, 1)}, "is null"},
		{"null text", []string{open, sw, marker, strings.Replace(lead(2, src), `"text":"x"`, `"text":null`, 1)}, "is null"},
		{"invalid UTF-8", []string{open, sw, marker, strings.Replace(lead(2, src), `"text":"x"`, "\"text\":\"a\xffb\"", 1)}, "UTF-8"},
		{"wrong type", []string{open, strings.Replace(sw, `"epoch":1`, `"epoch":"1"`, 1)}, "wrong type"},
		{"unknown kind", []string{open, `{"kind":"other"}`}, "kind"},
		{"no open line", []string{sw}, "open line"},
		{"second open line", []string{open, open}, "open line"},
		{"wrong version", []string{strings.Replace(open, `"v":1`, `"v":2`, 1)}, "version"},
		{"generation not grown", []string{open, sw, sw}, "generation"},
		{"entry while off", []string{open, lead(1, src)}, "while chat is off"},
		{"segment without its turned-on entry", []string{open, sw, lead(1, src)}, "turned-on"},
		{"second turned-on entry", []string{open, sw, marker, strings.Replace(marker, `"seq":1`, `"seq":2`, 1)}, "turned-on"},
		{"sequence not grown", []string{open, sw, marker, lead(2, src), lead(2, src)}, "does not grow"},
		{"bad time", []string{open, strings.Replace(sw, "00Z", "00+00:00", 1)}, "UTC"},
		{"text not plain", []string{open, sw, marker, strings.Replace(lead(2, src), `"text":"x"`, `"text":"a\tb"`, 1)}, "plain"},
		{"system text not fixed", []string{open, sw, marker, strings.Replace(lead(2, ""), `"lead"`, `"system"`, 1)}, "fixed texts"},
		{"lead entry without src", []string{open, sw, marker, lead(2, "")}, "source"},
		{"bad src", []string{open, sw, marker, lead(2, `,"src":"XYZ"`)}, "source"},
		{"data after the record", []string{open + ` {}`}, "after the record"},
		{"line too long", []string{open, `{"kind":"other","pad":"` + strings.Repeat("a", MaxLineBytes) + `"}`}, "longer than the bound"},
		{"line one byte too long with its newline", []string{padded(MaxLineBytes + 1)}, "longer than the bound"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			e.makeOutbox("scratch-beta-lead")
			if err := os.Mkdir(filepath.Join(e.dir, ChatDirName), 0o700); err != nil {
				t.Fatal(err)
			}
			data := strings.Join(c.lines, "\n") + "\n"
			if err := os.WriteFile(e.chatFile(testRole), []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := e.openRole(testRole)
			if !errors.Is(err, ErrCorrupt) || !strings.Contains(err.Error(), c.reason) {
				t.Fatalf("OpenStore: %v, want %q", err, c.reason)
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
	// A line of exactly the bound, its newline included, is read.
	t.Run("line of the bound", func(t *testing.T) {
		e := newEnv(t)
		if err := os.Mkdir(filepath.Join(e.dir, ChatDirName), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(e.chatFile(testRole), []byte(padded(MaxLineBytes)+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := e.openRole(testRole); err != nil {
			t.Fatalf("OpenStore: %v", err)
		}
	})
}

// The error of a corrupt file holds a fixed reason and a line number, never
// a byte of the file.
func TestCorruptErrorHoldsNoFileBytes(t *testing.T) {
	e := newEnv(t)
	if err := os.Mkdir(filepath.Join(e.dir, ChatDirName), 0o700); err != nil {
		t.Fatal(err)
	}
	data := `{"kind":"open","v":1,"next":1}` + "\n" + `{"kind":"open","v":1,"next":1,"\u001b[31mplanted":1}` + "\n"
	if err := os.WriteFile(e.chatFile(testRole), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := e.openRole(testRole)
	if !errors.Is(err, ErrCorrupt) || strings.Contains(err.Error(), "planted") || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("OpenStore: %v", err)
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
