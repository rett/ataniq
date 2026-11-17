package sessionpane_test

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"reflect"
	"strings"
	"sync"
	"testing"

	"ataniq/internal/terminalconsole/sessionpane"
	"ataniq/internal/terminalconsole/sessionreplay"
)

// The property of this file (the lead-hosting design, cases H5 and A5): a pane
// that was restored from a snapshot of another, at any point between two
// writes where the other is quiescent, stays equal to it for every write that
// follows, in the digest and in every field of its state, whichever way the
// stream was cut.

// propOp is a write or, when data is nil, a change of size.
type propOp struct {
	data       []byte
	cols, rows int
}

func (o propOp) apply(p *sessionpane.Pane) {
	if o.data != nil {
		p.Write(o.data)
	} else {
		p.Resize(o.cols, o.rows)
	}
}

// propTokens are the pieces of the generated streams: text of every width,
// combining marks and joiners, styles of every kind of colour, cursor moves,
// scroll regions (some beyond the screen), insert and delete, the alternate
// screen, modes, hyperlinks, titles, colours, tab stops, character sets and
// shifts, synchronized output frames, saves and restores, resets, and the
// queries a session makes.
var propTokens = []string{
	"a", "b", "bcd", "hello world ", "0123456789", "\n", "\r\n", "\r", "\t", "\b", "\x0b", "\x0c", "\x07", " ",
	"\u4e16", "\u754c", "\u3042", "\u0301", "e\u0301", "\u200d", "\U0001F468\u200d\U0001F469\u200d\U0001F467", "\U0001F1F3\U0001F1FF",
	"x\ufe0f", "\u1100\u1161\u11a8", "\U0001F44B\U0001F3FF", "#\ufe0f\u20e3", "\u00e9", "\u0080", "\u009b",
	"\xff", "\xc3", "\xe4\xb8", "\xf0\x9f\x91",
	"\x1b[1m", "\x1b[0m", "\x1b[m", "\x1b[3;4m", "\x1b[7m", "\x1b[31m", "\x1b[38;5;200m", "\x1b[48;2;1;2;3m",
	"\x1b[58;2;9;8;7m", "\x1b[4:3m", "\x1b[38;6;1;2;3;4m", "\x1b[48;3;1;2;3m", "\x1b[38;4;1;2;3;4m", "\x1b[38:2::10:20:30m",
	"\x1b[38:3::1:2:3m", "\x1b[48:4::1:2:3:4m", "\x1b[38:6::1:2:3:4m", "\x1b[58:3::5:6:7m",
	"\x1b[91m", "\x1b[102m", "\x1b[39m", "\x1b[49m", "\x1b[59m", "\x1b[22m", "\x1b[24m", "\x1b[38;1m",
	"\x1b[H", "\x1b[3;4H", "\x1b[999;999H", "\x1b[2J", "\x1b[0J", "\x1b[1J", "\x1b[3J", "\x1b[K", "\x1b[1K", "\x1b[2K",
	"\x1b[A", "\x1b[3B", "\x1b[2C", "\x1b[4D", "\x1b[2E", "\x1b[F", "\x1b[5G", "\x1b[3d", "\x1b[2;3f", "\x1b[`", "\x1b[2a", "\x1b[2e",
	"\x1b[2;5r", "\x1b[r", "\x1b[3;99r", "\x1b[40;90r", "\x1b[2;7s", "\x1b[s", "\x1b[u", "\x1b[?69h", "\x1b[?69l",
	"\x1b[3L", "\x1b[2M", "\x1b[4@", "\x1b[3P", "\x1b[2S", "\x1b[2T", "\x1b[5X", "\x1b[3b", "\x1b[b",
	"\x1b7", "\x1b8", "\x1bD", "\x1bM", "\x1bE", "\x1bH", "\x1b[g", "\x1b[3g", "\x1b[3I", "\x1b[2Z", "\x1b[?5W",
	"\x1b[?1049h", "\x1b[?1049l", "\x1b[?1047h", "\x1b[?1047l", "\x1b[?1048h", "\x1b[?1048l",
	"\x1b[?25l", "\x1b[?25h", "\x1b[?7l", "\x1b[?7h", "\x1b[?6h", "\x1b[?6l", "\x1b[?1h", "\x1b[?1l",
	"\x1b[?1000h", "\x1b[?1002h", "\x1b[?1003h", "\x1b[?1006h", "\x1b[?1000l", "\x1b[?1004h", "\x1b[?2004h", "\x1b[?2004l",
	"\x1b[?2026h", "\x1b[?2026l", "\x1b[?2027h", "\x1b[?2027l", "\x1b[?2048h", "\x1b[?66h", "\x1b[?9999h", "\x1b[?9999l",
	"\x1b[?2026;1000h", "\x1b[?1000;2026l", "\x1b[4h", "\x1b[4l", "\x1b[20h", "\x1b[20l", "\x1b[12h", "\x1b[9999h", "\x1b=", "\x1b>",
	"\x1b(0", "\x1b(B", "\x1b(A", "\x1b)0", "\x1b)A", "\x1b)B", "\x1b*0", "\x1b+A", "\x0e", "\x0f", "\x1bn", "\x1bo",
	"\x1b|", "\x1b}", "\x1b~", "\x1bN", "\x1bO", "\x8e", "\x8f", "lqkxj{|}~`$", "\x1b%G", "\x1b(%5",
	"\x1b]8;;http://a.example/1\x1b\\", "\x1b]8;id=1;http://a.example/2\x1b\\", "\x1b]8;;http://a.example/3\x07",
	"\x1b]8;;\x1b\\", "\x1b]8;p=1;\x1b\\", "\x1b]8;bad\x1b\\", "\x1b]8;;http://a.example/with;semi\x1b\\",
	"\x1b]0;a title\x07", "\x1b]2;another;title\x1b\\", "\x1b]1;icon\x07", "\x1b]0;\x07", "\x1b]7;file:///tmp\x07",
	"\x1b]10;#ff0000\x1b\\", "\x1b]11;rgb:00/ff/00\x07", "\x1b]12;#123\x07", "\x1b]110\x07", "\x1b]111\x07", "\x1b]112\x07",
	"\x1b]10;rgba:ff/00/00/80\x07", "\x1b]10;bogus\x07", "\x1b]4;1;rgb:ff/00/00\x07", "\x1b]52;c;aGk=\x07", "\x1b]52;c;?\x07",
	"\x1b]10;?\x07", "\x1b]11;?\x07", "\x1b]12;?\x07", "\x1b]777;notify\x07", "\x1b]133;A\x07",
	"\x1b[2 q", "\x1b[6 q", "\x1b[ q", "\x1b[0 q", "\x1bc", "\x1bP1$rq\x1b\\", "\x1bP+q\x1b\\", "\x1b_apc\x1b\\", "\x1b^pm\x1b\\", "\x1bXsos\x1b\\",
	"\x1b[c", "\x1b[>c", "\x1b[6n", "\x1b[5n", "\x1b[?6n", "\x1b[?25$p", "\x1b[?2026$p", "\x1b[4$p", "\x1b[?9999$p",
	"\x18", "\x1a", "\x1b\x1b", "\x1b[1;2;3;4;5;6;7;8;9;10;11;12;13;14;15;16;17;18;19;20;21;22;23;24;25;26;27;28;29;30;31;32;33;34;m",
	"\x1b[99999999999999999999H", "\x9b1;1H", "\x90q\x9c", "\x9d0;c8\x9c",
}

// genStream returns the starting size and the operations of one seeded
// stream, cut into writes of one to a few dozen bytes, with changes of size.
func genStream(rng *rand.Rand, tokens int) (cols, rows int, ops []propOp) {
	cols, rows = 2+rng.IntN(30), 1+rng.IntN(12)
	// Each stream leans on its own mix, so that some are mostly text and some
	// mostly sequences.
	textShare := 1 + rng.IntN(6)
	// One stream in five prints under many distinct links, so that the link
	// table fills, refuses and recounts.
	storm := rng.IntN(5) == 0
	var sb strings.Builder
	flush := func() {
		b := []byte(sb.String())
		sb.Reset()
		for len(b) > 0 {
			n := 1 + rng.IntN(9)
			switch rng.IntN(8) {
			case 0:
				n = 1
			case 1:
				n = 1 + rng.IntN(70)
			}
			n = min(n, len(b))
			ops = append(ops, propOp{data: b[:n:n]})
			b = b[n:]
		}
	}
	for i := 0; i < tokens; i++ {
		if storm && rng.IntN(3) == 0 {
			fmt.Fprintf(&sb, "\x1b]8;;http://a.example/%d\x1b\\%c", rng.IntN(600), 'a'+rng.IntN(26))
			continue
		}
		switch r := rng.IntN(70); {
		case r == 0:
			flush()
			ops = append(ops, propOp{cols: 2 + rng.IntN(30), rows: 1 + rng.IntN(12)})
		case r == 1:
			fmt.Fprintf(&sb, "\x1b]8;;http://a.example/%d\x1b\\", rng.IntN(400))
		case r == 2:
			sb.WriteString(strings.Repeat("\u0301", 1+rng.IntN(40)))
		case r == 3:
			fmt.Fprintf(&sb, "\x1b[%d;%dH", rng.IntN(15), rng.IntN(40))
		case r == 4:
			fmt.Fprintf(&sb, "\x1b[%d;%dr", rng.IntN(15), rng.IntN(30))
		case r < 4+textShare:
			sb.WriteString(propTokens[rng.IntN(20)])
		default:
			sb.WriteString(propTokens[rng.IntN(len(propTokens))])
		}
	}
	flush()
	return cols, rows, ops
}

// fixtureOps returns the operations of a recording of the replay package:
// its output chunks cut a second time at random places, and its resizes.
func fixtureOps(t testing.TB, name string, rng *rand.Rand) (cols, rows int, ops []propOp) {
	rec := fixture(t, name)
	for _, e := range rec.Events {
		switch e.Kind {
		case sessionreplay.KindOutput:
			for b := e.Data; len(b) > 0; {
				n := len(b)
				if rng.IntN(3) != 0 {
					n = 1 + rng.IntN(min(len(b), 12))
				}
				ops = append(ops, propOp{data: append([]byte(nil), b[:n]...)})
				b = b[n:]
			}
		case sessionreplay.KindResize:
			ops = append(ops, propOp{cols: e.Cols, rows: e.Rows})
		}
	}
	return rec.Cols, rec.Rows, ops
}

// junk is what a pane held before it is restored: a stream cut anywhere, so
// that the pane may be in the middle of a sequence, of another size.
func junk(rng *rand.Rand) (cols, rows int, data []byte) {
	cols, rows, ops := genStream(rng, 60)
	for _, op := range ops {
		data = append(data, op.data...)
	}
	return cols, rows, data[:rng.IntN(len(data)+1)]
}

// propDiff returns the path of the first difference of two values, or "".
func propDiff(path string, a, b reflect.Value) string {
	if a.Kind() != b.Kind() || a.Type() != b.Type() {
		return path + ": types differ"
	}
	switch a.Kind() {
	case reflect.Interface, reflect.Pointer:
		if a.IsNil() || b.IsNil() {
			if a.IsNil() != b.IsNil() {
				return path + ": nil differs"
			}
			return ""
		}
		return propDiff(path, a.Elem(), b.Elem())
	case reflect.Struct:
		for i := 0; i < a.NumField(); i++ {
			if d := propDiff(path+"."+a.Type().Field(i).Name, a.Field(i), b.Field(i)); d != "" {
				return d
			}
		}
	case reflect.Slice:
		if a.Len() != b.Len() {
			return fmt.Sprintf("%s: length %d != %d", path, a.Len(), b.Len())
		}
		for i := 0; i < a.Len(); i++ {
			if d := propDiff(fmt.Sprintf("%s[%d]", path, i), a.Index(i), b.Index(i)); d != "" {
				return d
			}
		}
	case reflect.Array:
		for i := 0; i < a.Len(); i++ {
			if d := propDiff(fmt.Sprintf("%s[%d]", path, i), a.Index(i), b.Index(i)); d != "" {
				return d
			}
		}
	default:
		if fmt.Sprintf("%#v", a) != fmt.Sprintf("%#v", b) {
			return fmt.Sprintf("%s: %#v != %#v", path, a, b)
		}
	}
	return ""
}

// sameState fails the test when the two panes differ in the digest or in any
// field of their state, and returns what differs.
func sameState(a, b *sessionpane.Pane) string {
	ds, es := sessionpane.DeepState(a), sessionpane.DeepState(b)
	if !reflect.DeepEqual(ds, es) {
		return propDiff("state", reflect.ValueOf(ds), reflect.ValueOf(es))
	}
	if a.Digest() != b.Digest() {
		return "the digests differ, the states do not"
	}
	if x, y := a.ModeSet(), b.ModeSet(); !reflect.DeepEqual(x, y) {
		return fmt.Sprintf("mode sets %+v and %+v", x, y)
	}
	return ""
}

// propStats counts what a run of the property did.
type propStats struct{ copies, compared, snapshots, bytes int }

func (s *propStats) add(o propStats) {
	s.copies += o.copies
	s.compared += o.compared
	s.snapshots += o.snapshots
	s.bytes += o.bytes
}

// follow runs a pane through ops. At every boundary between two operations
// where pick says so and the pane is quiescent, a second pane, which was in
// some other state, is restored from a snapshot of it; then every operation
// goes to both and their states are compared after each. At most max panes
// follow.
func follow(t testing.TB, cols, rows int, ops []propOp, rng *rand.Rand, max int, pick func(i int) bool) propStats {
	t.Helper()
	var st propStats
	newT := func(c, r int) *sessionpane.Pane {
		p := sessionpane.New(c, r, sessionpane.Options{})
		return p
	}
	a := newT(cols, rows)
	defer a.Close()
	type follower struct {
		p    *sessionpane.Pane
		from int
	}
	var followers []follower
	defer func() {
		for _, f := range followers {
			f.p.Close()
		}
	}()

	for i, op := range ops {
		op.apply(a)
		// Comparing every field copies the screens, which costs the more the
		// larger the screen is: a large one is compared every few operations
		// and at the last, a screen of the largest sizes only at the last.
		c, r := a.Size()
		cells := c * r
		deep := i == len(ops)-1 || cells <= 400 || i%4 == 0 && cells <= 4000
		digest := deep || cells <= 20000 || i%8 == 0
		for _, f := range followers {
			op.apply(f.p)
			if !digest {
				continue
			}
			if !deep {
				if a.Digest() != f.p.Digest() {
					t.Fatalf("the pane restored after operation %d has another digest after operation %d", f.from, i)
				}
				continue
			}
			if d := sameState(a, f.p); d != "" {
				t.Fatalf("the pane restored after operation %d differs after operation %d (%d bytes written): %s", f.from, i, len(op.data), d)
			}
			st.compared++
			// The snapshot of a follower is the snapshot of the source.
			if i%5 == 0 && a.Quiescent() && f.p.Quiescent() {
				x, err1 := a.AppendSnapshot(nil)
				y, err2 := f.p.AppendSnapshot(nil)
				if err1 != nil || err2 != nil {
					t.Fatalf("snapshots after operation %d: %v, %v", i, err1, err2)
				}
				if !bytes.Equal(x, y) {
					t.Fatalf("the snapshots of the source and of the pane restored after operation %d differ after operation %d", f.from, i)
				}
				st.snapshots++
			}
		}
		if len(followers) < max && a.Quiescent() && pick(i) {
			snap, err := a.AppendSnapshot(nil)
			if err != nil {
				t.Fatalf("snapshot after operation %d: %v", i, err)
			}
			jc, jr, jd := junk(rng)
			b := newT(jc, jr)
			b.Write(jd)
			if err := b.Restore(snap); err != nil {
				b.Close()
				t.Fatalf("restore of the snapshot after operation %d: %v", i, err)
			}
			followers = append(followers, follower{b, i})
			if d := sameState(a, b); d != "" {
				t.Fatalf("the pane restored after operation %d differs at once: %s", i, d)
			}
			again, err := b.AppendSnapshot(nil)
			if err != nil || !bytes.Equal(again, snap) {
				t.Fatalf("the snapshot of the pane restored after operation %d is not the snapshot it was restored from (%v)", i, err)
			}
			st.copies++
			st.bytes += len(snap)
		}
	}
	return st
}

// The streams: the fixture recordings of the replay package, and seeded
// generated ones. Each is cut at a few places, at random.
func TestSnapshotFollowsTheSource(t *testing.T) {
	seeds, tokens, repeats := 28, 450, 2
	if raceEnabled {
		// The race detector makes the work ten times slower.
		seeds, tokens, repeats = 6, 300, 1
	}
	var mu sync.Mutex
	var total propStats
	record := func(s propStats) {
		mu.Lock()
		total.add(s)
		mu.Unlock()
	}
	t.Run("streams", func(t *testing.T) {
		for _, name := range sessionreplay.FixtureNames() {
			for k := uint64(1); k <= uint64(repeats); k++ {
				t.Run(fmt.Sprintf("%s/%d", name, k), func(t *testing.T) {
					t.Parallel()
					rng := rand.New(rand.NewPCG(k, 11))
					cols, rows, ops := fixtureOps(t, name, rng)
					record(follow(t, cols, rows, ops, rng, 6, func(int) bool { return rng.IntN(3) == 0 }))
				})
			}
		}
		for seed := uint64(1); seed <= uint64(seeds); seed++ {
			t.Run(fmt.Sprintf("seed%d", seed), func(t *testing.T) {
				t.Parallel()
				rng := rand.New(rand.NewPCG(seed, 5))
				cols, rows, ops := genStream(rng, tokens)
				record(follow(t, cols, rows, ops, rng, 6, func(int) bool { return rng.IntN(60) == 0 }))
			})
		}
	})
	t.Logf("%d panes restored, %d comparisons, %d snapshots compared, %d bytes of snapshots", total.copies, total.compared, total.snapshots, total.bytes)
	want := 100
	if raceEnabled {
		want = 25
	}
	if total.copies < want {
		t.Errorf("only %d panes were restored, want %d", total.copies, want)
	}
}

// A fuzz input is a program as in FuzzWriteResize: the starting size, then
// writes and changes of size (an end is skipped). The pane is restored from a
// snapshot at the quiescent boundaries the first byte selects.
func opsFromProgram(data []byte) (cols, rows int, ops []propOp) {
	if len(data) < 2 {
		return 0, 0, nil
	}
	cols, rows = int(data[0]), int(data[1]%64)
	rest := data[2:]
	for len(rest) > 0 {
		op := rest[0]
		rest = rest[1:]
		switch op & 3 {
		case opResize:
			if len(rest) < 2 {
				return cols, rows, ops
			}
			ops = append(ops, propOp{cols: int(rest[0]) * 2, rows: int(rest[1])})
			rest = rest[2:]
		case opEnd:
		default:
			n := min(int(op>>2)+1, len(rest))
			ops = append(ops, propOp{data: rest[:n:n]})
			rest = rest[n:]
		}
	}
	return cols, rows, ops
}

// programFromOps is the inverse of opsFromProgram.
func programFromOps(cols, rows int, ops []propOp) []byte {
	b := []byte{byte(cols), byte(rows)}
	for _, op := range ops {
		if op.data == nil {
			b = append(b, opResize, byte(op.cols/2), byte(op.rows))
			continue
		}
		for d := op.data; len(d) > 0; {
			n := min(len(d), maxWrite)
			b = append(b, byte((n-1)<<2|opWrite))
			b = append(b, d[:n]...)
			d = d[n:]
		}
	}
	return b
}

func FuzzSnapshotFollows(f *testing.F) {
	for _, s := range seeds(f) {
		f.Add(s)
	}
	for seed := uint64(1); seed <= 4; seed++ {
		cols, rows, ops := genStream(rand.New(rand.NewPCG(seed, 3)), 60)
		f.Add(programFromOps(cols, rows, ops))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		cols, rows, ops := opsFromProgram(data)
		if ops == nil || raceEnabled && len(data) > 300 {
			return // a long seed takes seconds under the race detector
		}
		// A mutated input grows without bound; the comparisons copy the
		// screens, so only the first operations are followed.
		ops = ops[:min(len(ops), 200)]
		rng := rand.New(rand.NewPCG(uint64(len(data)), uint64(data[0])))
		start := int(data[0])
		follow(t, cols, rows, ops, rng, 2, func(i int) bool { return (start+i)%9 == 0 })
	})
}

// The cases a random cut reaches only by luck: the state that is carried
// between two writes and that a few bytes of the next one use.
func TestSnapshotCarriesWhatTheNextBytesUse(t *testing.T) {
	// Printing under 300 distinct links fills a table that holds 256.
	linkRun := func(from, to int) string {
		var b strings.Builder
		for i := from; i < to; i++ {
			fmt.Fprintf(&b, "\x1b]8;;http://a.example/%d\x1b\\x", i)
		}
		return b.String()
	}
	for _, tc := range []struct {
		name         string
		cols, rows   int
		before, then string
	}{
		{"a single shift waiting", 10, 3, "\x1b*0\x1b+0\x8e", "q\x8fqq"},
		{"a single shift used by the open cluster", 10, 3, "\x1b*0\x8eq", "\u0301q\u0301"},
		{"a single shift used by a capped cluster", 10, 3, "\x1b*0\x8eq" + strings.Repeat("\u0301", 31), "\u0301\u0301qq"},
		{"a pending wrap", 5, 3, "abcde", "f\u0301g"},
		{"a pending wrap with the open cluster", 5, 3, "abcd\U0001F468", "\u200d\U0001F469x"},
		{"an open cluster on the alternate screen", 6, 3, "\x1b[?1049hab\U0001F468", "\u200d\U0001F469\x1b[?1049lx"},
		{"a capped cluster", 6, 2, "a" + strings.Repeat("\u0301", 30) + "\U0001F3FB", "\u0301\u0301b"},
		{"the repeat of the last character", 12, 2, "x\x1b[2b", "\x1b[3b\x1b[2Kz\x1b[2b"},
		{"the link table after a recount", 40, 10, linkRun(0, 300), linkRun(100, 400)},
		// A recount has happened and 100 links are stale; the next refusals
		// wait for the next recount.
		{"the wait before the next recount", 100, 20, linkRun(0, 262) + "\x1b[H\x1b]8;;\x1b\\" + strings.Repeat("z", 150), linkRun(300, 330)},
		{"a full link table with a link no cell has", 20, 2, linkRun(0, 256), "\x1b[1;1H\x1b[0m" + linkRun(256, 300) + linkRun(0, 100)},
		{"the alternate screen behind the main", 10, 3, "main\x1b7\x1b[?1049h\x1b[1;31mALT\x1b8", "\x1b[?1049lback\x1b8x"},
		{"colours the session set", 10, 3, "\x1b]10;#ff8000\x07\x1b]11;rgb:00/80/ff\x07\x1b]12;rgba:01/02/03/04\x07", "\x1b]10;?\x07\x1b]112\x07\x1b]10;?\x07\x1b]11;?\x07"},
		{"the held frame", 10, 3, "one\x1b[?2026htwo", "\x1b[?2026lthree\x1b[?2026hfour"},
		{"a restored 2026 reset by a full reset", 10, 3, "\x1b[?2026hx", "\x1bc\x1b[?2026$p"},
		{"left and right margins", 10, 4, "\x1b[?69h\x1b[3;8s\x1b[2;2H\x1b[?6h", "ab\n\nc\x1b[2L\x1b[3M\x1b[2@\x1b[2P"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rng := rand.New(rand.NewPCG(1, 2))
			ops := []propOp{{data: []byte(tc.before)}}
			for rest := []byte(tc.then); len(rest) > 0; {
				n := 1 + rng.IntN(3)
				if len(tc.then) > 500 {
					n = 1 + rng.IntN(60)
				}
				n = min(n, len(rest))
				ops = append(ops, propOp{data: rest[:n:n]})
				rest = rest[n:]
			}
			st := follow(t, tc.cols, tc.rows, ops, rng, 1, func(i int) bool { return i == 0 })
			if st.copies != 1 {
				t.Fatalf("%d panes were restored: the first write left a sequence open", st.copies)
			}
		})
	}
}
