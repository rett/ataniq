package eventlog

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"
)

func newDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "log")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func openLog(t *testing.T, dir string, o Options) *Log {
	t.Helper()
	l, err := Open(dir, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return l
}

func mustAppend(t *testing.T, l *Log, k Kind, n EventName, f Fields) {
	t.Helper()
	if err := l.Append(k, n, f); err != nil {
		t.Fatal(err)
	}
}

func readAll(t *testing.T, dir string) []Event {
	t.Helper()
	ev, err := Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	return ev
}

func fileMode(t *testing.T, p string) os.FileMode {
	t.Helper()
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return st.Mode()
}

func TestOpenCreatesPrivateFile(t *testing.T) {
	dir := newDir(t)
	old := syscall.Umask(0)
	defer syscall.Umask(old)
	l := openLog(t, dir, Options{})
	if m := fileMode(t, filepath.Join(dir, FileName)); m != 0o600 {
		t.Fatalf("file mode %v, want 0600", m)
	}
	mustAppend(t, l, KindHolder, EventStart, Fields{Role: "proj/lead"})
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	// A second Open appends to the same file.
	l2 := openLog(t, dir, Options{})
	mustAppend(t, l2, KindHolder, EventExit, Fields{})
	ev := readAll(t, dir)
	if len(ev) != 2 || ev[0].Event != EventStart || ev[1].Event != EventExit {
		t.Fatalf("events %+v", ev)
	}
	if ev[1].Seq != 1 {
		t.Fatalf("sequence restarts with a process: got %d", ev[1].Seq)
	}
}

func TestOpenCreatedFileFixesUmask(t *testing.T) {
	dir := newDir(t)
	old := syscall.Umask(0o277)
	defer syscall.Umask(old)
	openLog(t, dir, Options{})
	if m := fileMode(t, filepath.Join(dir, FileName)); m != 0o600 {
		t.Fatalf("file mode %v, want 0600", m)
	}
}

func TestOpenRefusals(t *testing.T) {
	t.Run("folder mode", func(t *testing.T) {
		for _, m := range []os.FileMode{0o755, 0o750, 0o770, 0o701, 0o500, 0o700 | os.ModeSticky, 0o700 | os.ModeSetgid} {
			dir := newDir(t)
			if err := os.Chmod(dir, m); err != nil {
				t.Fatal(err)
			}
			if _, err := Open(dir, Options{}); !errors.Is(err, ErrUnsafe) {
				t.Errorf("mode %v: got %v, want ErrUnsafe", m, err)
			}
			if _, err := Read(dir); !errors.Is(err, ErrUnsafe) {
				t.Errorf("Read, mode %v: got %v, want ErrUnsafe", m, err)
			}
			os.Chmod(dir, 0o700)
			if _, err := os.Stat(filepath.Join(dir, FileName)); err == nil {
				t.Errorf("mode %v: a file was created", m)
			}
		}
	})
	t.Run("folder is a symlink", func(t *testing.T) {
		real := newDir(t)
		link := filepath.Join(t.TempDir(), "link")
		if err := os.Symlink(real, link); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(link, Options{}); !errors.Is(err, ErrUnsafe) {
			t.Fatalf("got %v, want ErrUnsafe", err)
		}
	})
	t.Run("folder missing or a file", func(t *testing.T) {
		d := t.TempDir()
		if _, err := Open(filepath.Join(d, "none"), Options{}); err == nil {
			t.Fatal("missing folder accepted")
		}
		p := filepath.Join(d, "file")
		os.WriteFile(p, nil, 0o600)
		if _, err := Open(p, Options{}); err == nil {
			t.Fatal("a file as folder accepted")
		}
	})
	t.Run("file is a symlink", func(t *testing.T) {
		dir := newDir(t)
		target := filepath.Join(t.TempDir(), "target")
		if err := os.WriteFile(target, []byte("keep\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(dir, FileName)); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(dir, Options{}); !errors.Is(err, ErrUnsafe) {
			t.Fatalf("got %v, want ErrUnsafe", err)
		}
		if b, _ := os.ReadFile(target); string(b) != "keep\n" {
			t.Fatalf("symlink target was written: %q", b)
		}
		// A dangling symlink must not be followed to create the target.
		dir2 := newDir(t)
		dangling := filepath.Join(t.TempDir(), "absent")
		os.Symlink(dangling, filepath.Join(dir2, FileName))
		if _, err := Open(dir2, Options{}); !errors.Is(err, ErrUnsafe) {
			t.Fatalf("dangling: got %v, want ErrUnsafe", err)
		}
		if _, err := os.Lstat(dangling); err == nil {
			t.Fatal("dangling symlink target was created")
		}
		// Read refuses a symlinked rotated file too.
		dir3 := newDir(t)
		os.Symlink(target, filepath.Join(dir3, RotatedName))
		if _, err := Read(dir3); !errors.Is(err, ErrUnsafe) {
			t.Fatalf("Read: got %v, want ErrUnsafe", err)
		}
	})
	t.Run("file mode", func(t *testing.T) {
		dir := newDir(t)
		p := filepath.Join(dir, FileName)
		os.WriteFile(p, nil, 0o644)
		os.Chmod(p, 0o644)
		if _, err := Open(dir, Options{}); !errors.Is(err, ErrUnsafe) {
			t.Fatalf("got %v, want ErrUnsafe", err)
		}
	})
	t.Run("file is a fifo", func(t *testing.T) {
		dir := newDir(t)
		if err := syscall.Mkfifo(filepath.Join(dir, FileName), 0o600); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { _, err := Open(dir, Options{}); done <- err }()
		select {
		case err := <-done:
			if !errors.Is(err, ErrUnsafe) {
				t.Fatalf("got %v, want ErrUnsafe", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("Open blocked on a fifo")
		}
	})
	t.Run("max bytes", func(t *testing.T) {
		for _, n := range []int64{-1, 1, MinMaxBytes - 1, MaxMaxBytes + 1} {
			if _, err := Open(newDir(t), Options{MaxBytes: n}); err == nil {
				t.Errorf("MaxBytes %d accepted", n)
			}
		}
	})
}

func TestOpenFlags(t *testing.T) {
	dir := newDir(t)
	l := openLog(t, dir, Options{})
	f := l.f.(*os.File)
	b, err := os.ReadFile(fmt.Sprintf("/proc/self/fdinfo/%d", f.Fd()))
	if err != nil {
		t.Skip("no /proc fdinfo")
	}
	m := regexp.MustCompile(`flags:\s*([0-7]+)`).FindSubmatch(b)
	if m == nil {
		t.Fatalf("no flags in %q", b)
	}
	var flags int
	fmt.Sscanf(string(m[1]), "%o", &flags)
	if flags&syscall.O_APPEND == 0 {
		t.Errorf("O_APPEND not set: %o", flags)
	}
	if flags&0o2000000 == 0 { // O_CLOEXEC
		t.Errorf("O_CLOEXEC not set: %o", flags)
	}
	if flags&syscall.O_NONBLOCK != 0 {
		t.Errorf("O_NONBLOCK left set: %o", flags)
	}
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(1500 * time.Microsecond)
	return c.t
}

func TestClockAndLineShape(t *testing.T) {
	dir := newDir(t)
	zone := time.FixedZone("far", 5*3600+1800)
	c := &fakeClock{t: time.Date(2026, 3, 4, 5, 6, 7, 123456789, zone)}
	l := openLog(t, dir, Options{Clock: c.now})
	exit := 3
	yes, no := true, false
	mustAppend(t, l, KindHolder, EventExit, Fields{
		Role: "proj/lead", Epoch: "e7", ExitStatus: &exit,
		StreamEnded: &yes, TUIExited: &yes, TreeGone: &no,
	})
	mustAppend(t, l, KindOwnerDecision, EventResize, Fields{Cols: 120, Rows: 40})
	raw, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("lines: %q", lines)
	}
	if !regexp.MustCompile(`^\{"time":"2026-03-03T23:36:07\.12495678[0-9]Z","seq":1,"kind":"holder","event":"exit","role":"proj/lead","epoch":"e7","exit_status":3,"stream_ended":true,"tui_exited":true,"tree_gone":false\}$`).MatchString(lines[0]) {
		t.Errorf("line 1: %s", lines[0])
	}
	if !regexp.MustCompile(`^\{"time":"2026-03-03T23:36:07\.12645678[0-9]Z","seq":2,"kind":"owner-decision","event":"resize","cols":120,"rows":40\}$`).MatchString(lines[1]) {
		t.Errorf("line 2: %s", lines[1])
	}
	ev := readAll(t, dir)
	if got, want := ev[0].Time, time.Date(2026, 3, 3, 23, 36, 7, 124956789, time.UTC); !got.Equal(want) {
		// the fake clock steps before each reading
		t.Errorf("time %v, want %v", got, want)
	}
	if ev[0].ExitStatus == nil || *ev[0].ExitStatus != 3 || ev[0].TreeGone == nil || *ev[0].TreeGone {
		t.Errorf("fields did not round-trip: %+v", ev[0])
	}
}

func TestEventNamesAndKinds(t *testing.T) {
	dir := newDir(t)
	l := openLog(t, dir, Options{})
	kinds := []Kind{KindProviderReport, KindAtaniqObservation, KindOwnerDecision, KindHolder}
	names := []EventName{EventStart, EventExit, EventAttach, EventDetach, EventWriterChange, EventResize,
		EventState, EventInject, EventRefusal, EventCounters, EventCheckMismatch, EventResync, EventKeyHeld}
	wantNames := "start exit attach detach writer-change resize state inject refusal counters check-mismatch resync key-held"
	var got []string
	for _, n := range names {
		got = append(got, string(n))
	}
	if strings.Join(got, " ") != wantNames {
		t.Fatalf("event names %v", got)
	}
	for _, k := range kinds {
		for _, n := range names {
			mustAppend(t, l, k, n, Fields{})
		}
	}
	if n := len(readAll(t, dir)); n != len(kinds)*len(names) {
		t.Fatalf("read %d events", n)
	}
	for _, bad := range []struct {
		k Kind
		n EventName
	}{{"", EventStart}, {KindHolder, ""}, {"holder ", EventStart}, {KindHolder, "keystroke"}, {"other", EventExit}} {
		if err := l.Append(bad.k, bad.n, Fields{}); !errors.Is(err, ErrInvalid) {
			t.Errorf("%q %q: got %v", bad.k, bad.n, err)
		}
	}
	if s := l.Stats(); s.Invalid != 5 || s.Written != uint64(len(kinds)*len(names)) {
		t.Fatalf("stats %+v", s)
	}
}

func TestRotation(t *testing.T) {
	dir := newDir(t)
	const max = MinMaxBytes
	c := &fakeClock{t: time.Unix(0, 0)}
	l := openLog(t, dir, Options{MaxBytes: max, Clock: c.now})
	f := Fields{Role: "proj/lead", ClientName: "console", ClientCommand: strings.Repeat("x", 300), Reason: "normal"}
	const total = 400 // about 160 KiB: several rotations
	for i := 0; i < total; i++ {
		mustAppend(t, l, KindHolder, EventAttach, f)
		var sum int64
		for _, n := range []string{FileName, RotatedName} {
			if st, err := os.Stat(filepath.Join(dir, n)); err == nil {
				if st.Size() > max {
					t.Fatalf("%s is %d bytes, over the cap %d", n, st.Size(), max)
				}
				sum += st.Size()
			}
		}
		if sum > 2*max {
			t.Fatalf("total %d over twice the cap", sum)
		}
	}
	if s := l.Stats(); s.Rotations < 3 || s.Written != total {
		t.Fatalf("stats %+v", s)
	}
	for _, n := range []string{FileName, RotatedName} {
		if m := fileMode(t, filepath.Join(dir, n)); m != 0o600 {
			t.Errorf("%s mode %v", n, m)
		}
		// No split line: every line of both files is a whole event.
		b, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			t.Fatal(err)
		}
		if len(b) == 0 || b[len(b)-1] != '\n' {
			t.Errorf("%s does not end in a newline", n)
		}
		for _, line := range bytes.Split(bytes.TrimSuffix(b, []byte{'\n'}), []byte{'\n'}) {
			if !bytes.HasPrefix(line, []byte(`{"time":`)) || !bytes.HasSuffix(line, []byte(`}`)) {
				t.Errorf("%s: split line %q", n, line)
			}
		}
	}
	ev := readAll(t, dir)
	if len(ev) == 0 || ev[len(ev)-1].Seq != total {
		t.Fatalf("last event %+v", ev[len(ev)-1])
	}
	for i := 1; i < len(ev); i++ {
		if ev[i].Seq != ev[i-1].Seq+1 {
			t.Fatalf("sequence jumps %d -> %d at %d (events are lost or reordered)", ev[i-1].Seq, ev[i].Seq, i)
		}
	}
	if ev[0].Seq == 1 {
		t.Fatal("nothing was rotated out")
	}
}

func TestRotationReplacesOlderFile(t *testing.T) {
	dir := newDir(t)
	// A stale rotated file, even a read-only one, is replaced.
	old := filepath.Join(dir, RotatedName)
	os.WriteFile(old, []byte("stale\n"), 0o400)
	l := openLog(t, dir, Options{MaxBytes: MinMaxBytes})
	f := Fields{ClientCommand: strings.Repeat("y", 400)}
	for i := 0; i < 200; i++ {
		mustAppend(t, l, KindHolder, EventAttach, f)
	}
	b, _ := os.ReadFile(old)
	if bytes.Contains(b, []byte("stale")) {
		t.Fatal("the older rotated file was not replaced")
	}
	if _, err := Read(dir); err != nil {
		t.Fatal(err)
	}
}

func TestFileOverTheCapAtOpenIsRotatedFirst(t *testing.T) {
	dir := newDir(t)
	line := `{"time":"2026-01-01T00:00:00Z","seq":1,"kind":"holder","event":"start"}` + "\n"
	big := strings.Repeat(line, 3*MinMaxBytes/len(line))
	os.WriteFile(filepath.Join(dir, FileName), []byte(big), 0o600)
	l := openLog(t, dir, Options{MaxBytes: MinMaxBytes})
	mustAppend(t, l, KindHolder, EventExit, Fields{})
	if b, _ := os.ReadFile(filepath.Join(dir, RotatedName)); string(b) != big {
		t.Fatal("the old file was not rotated whole")
	}
	st, _ := os.Stat(filepath.Join(dir, FileName))
	if st.Size() > MinMaxBytes {
		t.Fatalf("file is %d bytes", st.Size())
	}
}

func TestConcurrentAppendsInSequenceOrder(t *testing.T) {
	for _, max := range []int64{DefaultMaxBytes, MinMaxBytes} {
		t.Run(fmt.Sprint(max), func(t *testing.T) {
			dir := newDir(t)
			l := openLog(t, dir, Options{MaxBytes: max})
			const workers, each = 8, 150
			var wg sync.WaitGroup
			for w := 0; w < workers; w++ {
				wg.Add(1)
				go func(w int) {
					defer wg.Done()
					for i := 0; i < each; i++ {
						if err := l.Append(KindHolder, EventResize, Fields{ClientPID: w + 1, Cols: i + 1, Rows: 24, ClientCommand: strings.Repeat("c", 200)}); err != nil {
							t.Error(err)
							return
						}
					}
				}(w)
			}
			wg.Wait()
			ev := readAll(t, dir)
			if len(ev) == 0 || ev[len(ev)-1].Seq != workers*each {
				t.Fatalf("got %d events", len(ev))
			}
			next := map[int]int{}
			for i, e := range ev {
				if i > 0 && (e.Seq != ev[i-1].Seq+1 || e.Time.Before(ev[i-1].Time)) {
					t.Fatalf("out of order at %d: seq %d after %d", i, e.Seq, ev[i-1].Seq)
				}
				// Each worker's own events keep their order.
				if prev := next[e.ClientPID]; e.Cols <= prev {
					t.Fatalf("worker %d: %d after %d", e.ClientPID, e.Cols, prev)
				}
				next[e.ClientPID] = e.Cols
			}
			if max == DefaultMaxBytes && len(ev) != workers*each {
				t.Fatalf("lost events: %d", len(ev))
			}
		})
	}
}

func TestOversizeEventRefused(t *testing.T) {
	dir := newDir(t)
	l := openLog(t, dir, Options{})
	l.maxEvent = 300
	mustAppend(t, l, KindHolder, EventStart, Fields{Role: "r"})
	big := Fields{Counters: map[string]uint64{}}
	for i := 0; i < 20; i++ {
		big.Counters[fmt.Sprintf("counter-with-a-long-name-%02d", i)] = 1
	}
	err := l.Append(KindHolder, EventCounters, big)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("got %v, want ErrTooLarge", err)
	}
	mustAppend(t, l, KindHolder, EventExit, Fields{})
	ev := readAll(t, dir)
	if len(ev) != 2 || ev[0].Event != EventStart || ev[1].Event != EventExit {
		t.Fatalf("events %+v", ev)
	}
	if ev[1].Seq != 3 {
		t.Fatalf("the refused event should leave a gap in the sequence, got %d", ev[1].Seq)
	}
	if s := l.Stats(); s.TooLarge != 1 || s.Written != 2 {
		t.Fatalf("stats %+v", s)
	}
}

// worst builds a Fields with every cap reached by the most expensive bytes.
func worst() Fields {
	ctl := func(n int) string { return strings.Repeat("\x00", n) }
	exit, yes := -2147483648, true
	f := Fields{
		Role: ctl(MaxRole), Epoch: ctl(MaxEpoch), ClientName: ctl(MaxClientName),
		ClientPID: 1<<62 - 1, ClientCommand: ctl(MaxCommandLine), Cols: 1<<62 - 1, Rows: 1<<62 - 1,
		Reason: ctl(MaxReason), State: ctl(MaxState), Digest: ctl(MaxDigest),
		ExitStatus: &exit, StreamEnded: &yes, TUIExited: &yes, TreeGone: &yes,
		Counters: map[string]uint64{},
	}
	for i := 0; i < 2*MaxCounters; i++ {
		f.Counters[fmt.Sprintf("%02d%s", i, strings.Repeat("n", MaxCounterName))] = 1<<64 - 1
	}
	return f
}

func TestWorstCaseEventFits(t *testing.T) {
	dir := newDir(t)
	l := openLog(t, dir, Options{})
	mustAppend(t, l, KindProviderReport, EventCheckMismatch, worst())
	st, err := os.Stat(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("worst-case event: %d bytes of %d", st.Size(), MaxEventBytes)
	// Leave room for growth: a field added later must not push the
	// worst case near the cap without someone noticing here.
	if st.Size() > MaxEventBytes*3/4 {
		t.Fatalf("worst case is %d bytes, over three quarters of the %d cap", st.Size(), MaxEventBytes)
	}
}

func TestStringCaps(t *testing.T) {
	dir := newDir(t)
	l := openLog(t, dir, Options{})
	long := func(n int) string { return strings.Repeat("a", n+100) }
	mustAppend(t, l, KindHolder, EventAttach, Fields{
		Role: long(MaxRole), Epoch: long(MaxEpoch), ClientName: long(MaxClientName),
		ClientCommand: long(MaxCommandLine), Reason: long(MaxReason), State: long(MaxState), Digest: long(MaxDigest),
	})
	e := readAll(t, dir)[0]
	for name, c := range map[string][2]any{
		"role": {e.Role, MaxRole}, "epoch": {e.Epoch, MaxEpoch}, "client": {e.ClientName, MaxClientName},
		"command": {e.ClientCommand, MaxCommandLine}, "reason": {e.Reason, MaxReason},
		"state": {e.State, MaxState}, "digest": {e.Digest, MaxDigest},
	} {
		if got := len(c[0].(string)); got != c[1].(int) {
			t.Errorf("%s: %d bytes, want %d", name, got, c[1])
		}
	}
	if s := l.Stats(); s.Truncated != 7 {
		t.Fatalf("stats %+v", s)
	}
	// The cut falls on a character boundary and the string stays valid.
	for _, r := range []string{"é", "€", "😀"} {
		d := newDir(t)
		l2 := openLog(t, d, Options{})
		for pad := 0; pad < 4; pad++ {
			s := strings.Repeat("a", MaxCommandLine-pad-1) + strings.Repeat(r, 4)
			mustAppend(t, l2, KindHolder, EventAttach, Fields{ClientCommand: s})
		}
		for _, e := range readAll(t, d) {
			if !utf8.ValidString(e.ClientCommand) || len(e.ClientCommand) > MaxCommandLine || strings.Contains(e.ClientCommand, "\uFFFD") {
				t.Errorf("%q: bad cut %q", r, e.ClientCommand)
			}
		}
	}
	// Invalid UTF-8 is replaced, not passed on or inflated.
	d := newDir(t)
	l3 := openLog(t, d, Options{})
	mustAppend(t, l3, KindHolder, EventAttach, Fields{ClientName: "a\xff\xfe\xfdb"})
	if got := readAll(t, d)[0].ClientName; got != "a\uFFFDb" {
		t.Fatalf("got %q", got)
	}
}

func TestControlCharactersAreEscaped(t *testing.T) {
	dir := newDir(t)
	l := openLog(t, dir, Options{})
	mustAppend(t, l, KindHolder, EventAttach, Fields{
		ClientName: "a\nb\rc\x1b[31m\x00\x7f\u2028<&>", ClientCommand: "cmd\n{\"seq\":99}\n",
		Reason: "x\ny z", Counters: map[string]uint64{"a\nb": 1},
	})
	raw, _ := os.ReadFile(filepath.Join(dir, FileName))
	if bytes.Count(raw, []byte{'\n'}) != 1 || raw[len(raw)-1] != '\n' {
		t.Fatalf("an embedded newline split the line: %q", raw)
	}
	for _, b := range raw[:len(raw)-1] {
		if b < 0x20 {
			t.Fatalf("raw control byte %#x in %q", b, raw)
		}
	}
	if bytes.Contains(raw, []byte{0x1b}) || bytes.Contains(raw, []byte("\u2028")) {
		t.Fatalf("unescaped control or separator in %q", raw)
	}
	e := readAll(t, dir)[0]
	if e.ClientName != "a\nb\rc\x1b[31m\x00\x7f\u2028<&>" {
		t.Fatalf("round trip: %q", e.ClientName)
	}
	if e.Seq != 1 {
		t.Fatalf("an injected record changed the sequence: %+v", e)
	}
	if e.Reason != "x_y_z" {
		t.Fatalf("code not restricted: %q", e.Reason)
	}
	if _, ok := e.Counters["a_b"]; !ok {
		t.Fatalf("counter name not restricted: %v", e.Counters)
	}
}

func TestCountersBounded(t *testing.T) {
	dir := newDir(t)
	l := openLog(t, dir, Options{})
	in := map[string]uint64{}
	for i := 0; i < MaxCounters+10; i++ {
		in[fmt.Sprintf("c%03d", i)] = uint64(i)
	}
	in[strings.Repeat("k", MaxCounterName+50)] = 7
	mustAppend(t, l, KindHolder, EventCounters, Fields{Counters: in})
	e := readAll(t, dir)[0]
	if len(e.Counters) != MaxCounters {
		t.Fatalf("%d counters kept, want %d", len(e.Counters), MaxCounters)
	}
	// The first names in sorted order are kept: the choice is deterministic.
	if _, ok := e.Counters["c000"]; !ok {
		t.Fatalf("counters %v", e.Counters)
	}
	for k := range e.Counters {
		if len(k) > MaxCounterName {
			t.Fatalf("name of %d bytes", len(k))
		}
	}
	if s := l.Stats(); s.CountersDropped == 0 {
		t.Fatalf("drops not counted: %+v", s)
	}
	if len(in) != MaxCounters+11 {
		t.Fatal("the caller's map was changed")
	}
	// A negative number is not written.
	mustAppend(t, l, KindHolder, EventResize, Fields{Cols: -5, Rows: -1, ClientPID: -9})
	e = readAll(t, dir)[1]
	if e.Cols != 0 || e.Rows != 0 || e.ClientPID != 0 {
		t.Fatalf("%+v", e)
	}
}

func TestCallerPointersAreCopied(t *testing.T) {
	dir := newDir(t)
	l := openLog(t, dir, Options{})
	st := 4
	mustAppend(t, l, KindHolder, EventExit, Fields{ExitStatus: &st})
	st = 9
	if e := readAll(t, dir)[0]; *e.ExitStatus != 4 {
		t.Fatalf("%d", *e.ExitStatus)
	}
}

// flaky is a sink that fails while fail is set, after writing partial bytes.
type flaky struct {
	*os.File
	fail    bool
	partial int
}

func (f *flaky) Write(p []byte) (int, error) {
	if !f.fail {
		return f.File.Write(p)
	}
	n := min(f.partial, len(p))
	if n > 0 {
		f.File.Write(p[:n])
	}
	return n, syscall.ENOSPC
}

func TestWriteErrorIsCountedAndLogRecovers(t *testing.T) {
	dir := newDir(t)
	l := openLog(t, dir, Options{})
	fl := &flaky{File: l.f.(*os.File), partial: 25}
	l.f = fl
	mustAppend(t, l, KindHolder, EventStart, Fields{})
	fl.fail = true
	err := l.Append(KindHolder, EventAttach, Fields{ClientName: "lost"})
	if !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("got %v", err)
	}
	fl.partial = 0
	if err := l.Append(KindHolder, EventAttach, Fields{ClientName: "lost2"}); err == nil {
		t.Fatal("no error while the disk is full")
	}
	if s := l.Stats(); s.WriteErrors != 2 || s.Written != 1 {
		t.Fatalf("stats %+v", s)
	}
	fl.fail = false // the disk recovers
	mustAppend(t, l, KindHolder, EventDetach, Fields{ClientName: "ok"})
	mustAppend(t, l, KindHolder, EventExit, Fields{})
	ev, err := Read(dir)
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("the torn fragment should be reported: %v", err)
	}
	var seqs []uint64
	for _, e := range ev {
		seqs = append(seqs, e.Seq)
	}
	if !reflect.DeepEqual(seqs, []uint64{1, 4, 5}) {
		t.Fatalf("sequences %v: the lost events must leave a gap", seqs)
	}
	if ev[1].ClientName != "ok" {
		t.Fatalf("%+v", ev[1])
	}
}

func TestOpenAfterTornTail(t *testing.T) {
	dir := newDir(t)
	p := filepath.Join(dir, FileName)
	os.WriteFile(p, []byte(`{"time":"2026-01-01T00:00:00Z","seq":1,"kind":"holder","event":"start"}`+"\n"+`{"time":"2026-01-01`), 0o600)
	l := openLog(t, dir, Options{})
	mustAppend(t, l, KindHolder, EventExit, Fields{})
	ev, err := Read(dir)
	if !errors.Is(err, ErrCorrupt) || len(ev) != 2 || ev[1].Event != EventExit {
		t.Fatalf("%v %+v", err, ev)
	}
}

func TestCloseAndSync(t *testing.T) {
	dir := newDir(t)
	l := openLog(t, dir, Options{})
	mustAppend(t, l, KindHolder, EventExit, Fields{})
	if err := l.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if err := l.Append(KindHolder, EventExit, Fields{}); !errors.Is(err, ErrClosed) {
		t.Fatalf("got %v", err)
	}
	if err := l.Sync(); !errors.Is(err, ErrClosed) {
		t.Fatalf("got %v", err)
	}
	if err := l.Close(); !errors.Is(err, ErrClosed) {
		t.Fatalf("got %v", err)
	}
	if n := len(readAll(t, dir)); n != 1 {
		t.Fatalf("%d events", n)
	}
}

func TestReadBounds(t *testing.T) {
	t.Run("empty folder", func(t *testing.T) {
		ev, err := Read(newDir(t))
		if err != nil || len(ev) != 0 {
			t.Fatalf("%v %v", ev, err)
		}
	})
	t.Run("bad lines", func(t *testing.T) {
		dir := newDir(t)
		good := `{"time":"2026-01-01T00:00:00Z","seq":%d,"kind":"holder","event":"start"}` + "\n"
		var b strings.Builder
		fmt.Fprintf(&b, good, 1)
		b.WriteString("not json\n")
		b.WriteString("{}\n")
		b.WriteString(`{"time":"2026-01-01T00:00:00Z","seq":0,"kind":"holder","event":"start"}` + "\n")
		b.WriteString(`{"seq":3,"kind":"holder","event":"start","role":"` + strings.Repeat("r", MaxEventBytes) + `"}` + "\n")
		b.WriteString(strings.Repeat("q", 3*MaxEventBytes) + "\n")
		fmt.Fprintf(&b, good, 2)
		os.WriteFile(filepath.Join(dir, FileName), []byte(b.String()), 0o600)
		ev, err := Read(dir)
		if !errors.Is(err, ErrCorrupt) {
			t.Fatalf("got %v", err)
		}
		if len(ev) != 2 || ev[0].Seq != 1 || ev[1].Seq != 2 {
			t.Fatalf("%+v", ev)
		}
	})
	t.Run("order of the two files", func(t *testing.T) {
		dir := newDir(t)
		line := `{"time":"2026-01-01T00:00:00Z","seq":%d,"kind":"holder","event":"start"}` + "\n"
		os.WriteFile(filepath.Join(dir, RotatedName), []byte(fmt.Sprintf(line, 1)+fmt.Sprintf(line, 2)), 0o600)
		os.WriteFile(filepath.Join(dir, FileName), []byte(fmt.Sprintf(line, 3)), 0o600)
		ev := readAll(t, dir)
		if len(ev) != 3 || ev[0].Seq != 1 || ev[2].Seq != 3 {
			t.Fatalf("%+v", ev)
		}
	})
	t.Run("a file over the read bound", func(t *testing.T) {
		dir := newDir(t)
		f, err := os.OpenFile(filepath.Join(dir, FileName), os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		line := `{"time":"2026-01-01T00:00:00Z","seq":1,"kind":"holder","event":"start"}` + "\n"
		f.WriteString(line)
		f.Truncate(MaxReadBytes + 5)
		f.Close()
		ev, err := Read(dir)
		if !errors.Is(err, ErrTooLarge) || len(ev) != 1 {
			t.Fatalf("%v %v", err, ev)
		}
	})
}

// --- Design case H14: no keystroke and no output text can be logged. ---

// bannedWords are fragments of a field name that would carry input or output.
var bannedWords = []string{"text", "input", "output", "key", "byte", "screen", "data", "payload", "content", "paste", "stdin", "stdout", "frame", "line", "raw", "body", "message", "msg"}

// checkType returns the problems with typ and every type reachable from it.
func checkType(typ reflect.Type, path string, seen map[reflect.Type]bool) []string {
	var bad []string
	switch typ.Kind() {
	case reflect.Interface:
		return append(bad, path+": interface type")
	case reflect.Slice, reflect.Array:
		if typ.Elem().Kind() == reflect.Uint8 {
			return append(bad, path+": byte slice or array")
		}
		return append(bad, path+": slice or array (not in the field set)")
	case reflect.Chan, reflect.Func, reflect.UnsafePointer, reflect.Complex64, reflect.Complex128, reflect.Float32, reflect.Float64:
		return append(bad, path+": "+typ.Kind().String())
	case reflect.Pointer:
		return checkType(typ.Elem(), path, seen)
	case reflect.Map:
		if typ.Key().Kind() != reflect.String {
			bad = append(bad, path+": map key is not a string")
		}
		return append(bad, checkType(typ.Elem(), path+"[]", seen)...)
	case reflect.Struct:
		if typ == reflect.TypeOf(time.Time{}) || seen[typ] {
			return nil
		}
		seen[typ] = true
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			if !f.IsExported() && !f.Anonymous {
				bad = append(bad, path+"."+f.Name+": unexported field")
				continue
			}
			names := []string{strings.ToLower(f.Name), strings.ToLower(f.Tag.Get("json"))}
			for _, n := range names {
				for _, w := range bannedWords {
					if strings.Contains(n, w) {
						bad = append(bad, fmt.Sprintf("%s.%s: name %q is like %q", path, f.Name, n, w))
					}
				}
			}
			bad = append(bad, checkType(f.Type, path+"."+f.Name, seen)...)
		}
	}
	return bad
}

func TestH14ExportedFieldSetCarriesNoInputOrOutput(t *testing.T) {
	for _, typ := range []reflect.Type{reflect.TypeOf(Event{}), reflect.TypeOf(Fields{})} {
		if bad := checkType(typ, typ.Name(), map[reflect.Type]bool{}); len(bad) > 0 {
			t.Errorf("%s:\n%s", typ.Name(), strings.Join(bad, "\n"))
		}
	}
	// Only string-kinded scalars and small numbers are allowed.
	typ := reflect.TypeOf(Fields{})
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		t.Logf("field %-14s %-18s %s", f.Name, f.Type, f.Tag.Get("json"))
	}
}

func TestH14TheCheckCatchesWhatItShouldCatch(t *testing.T) {
	cases := map[string]any{
		"bytes":      struct{ Role []byte }{},
		"byte array": struct{ Role [4]byte }{},
		"text name":  struct{ ScreenText string }{},
		"input":      struct{ UserInput string }{},
		"output":     struct{ Output string }{},
		"keys":       struct{ Keys string }{},
		"json tag": struct {
			Role string `json:"key_pressed"`
		}{},
		"any":        struct{ Extra any }{},
		"any map":    struct{ Extra map[string]any }{},
		"nested":     struct{ Inner struct{ Data []byte } }{},
		"pointer":    struct{ P *struct{ Bytes []byte } }{},
		"string map": struct{ M map[string][]byte }{},
		"slice":      struct{ L []string }{},
		"unexported": struct{ hidden string }{},
	}
	for name, v := range cases {
		typ := reflect.TypeOf(v)
		if bad := checkType(typ, name, map[reflect.Type]bool{}); len(bad) == 0 {
			t.Errorf("%s: not caught", name)
		}
	}
}

func TestH14AppendTakesNoFreeFormValue(t *testing.T) {
	m, ok := reflect.TypeOf(&Log{}).MethodByName("Append")
	if !ok {
		t.Fatal("no Append")
	}
	want := []reflect.Type{reflect.TypeOf(&Log{}), reflect.TypeOf(Kind("")), reflect.TypeOf(EventName("")), reflect.TypeOf(Fields{})}
	if m.Type.NumIn() != len(want) {
		t.Fatalf("Append takes %v", m.Type)
	}
	for i, w := range want {
		if m.Type.In(i) != w {
			t.Errorf("Append parameter %d is %v, want %v", i, m.Type.In(i), w)
		}
	}
	// No exported method of Log takes bytes, a reader or an empty interface.
	lt := reflect.TypeOf(&Log{})
	for i := 0; i < lt.NumMethod(); i++ {
		mt := lt.Method(i)
		for j := 1; j < mt.Type.NumIn(); j++ {
			switch k := mt.Type.In(j); {
			case k.Kind() == reflect.Interface, k.Kind() == reflect.Slice, k == reflect.TypeOf(""):
				t.Errorf("%s takes %v", mt.Name, k)
			}
		}
	}
}

// frozenField is one reviewed field: its name, its type and its JSON tag.
type frozenField struct{ name, typ, tag string }

// reviewedEvent and reviewedFields are the whole field set of the log line,
// reviewed for design case H14. A field added to Event or Fields fails
// TestH14FieldSetIsReviewed until it is added here on purpose, by a reviewer
// who has checked that it takes no input bytes and no screen text. The order
// is the order of the struct.
var reviewedEvent = []frozenField{
	{"Time", "time.Time", `json:"time"`},
	{"Seq", "uint64", `json:"seq"`},
	{"Kind", "eventlog.Kind", `json:"kind"`},
	{"Event", "eventlog.EventName", `json:"event"`},
	{"Fields", "eventlog.Fields", ``},
}

var reviewedFields = []frozenField{
	{"Role", "string", `json:"role,omitempty"`},
	{"Epoch", "string", `json:"epoch,omitempty"`},
	{"ClientName", "string", `json:"client,omitempty"`},
	{"ClientPID", "int", `json:"client_pid,omitempty"`},
	{"ClientCommand", "string", `json:"client_cmd,omitempty"`},
	{"Cols", "int", `json:"cols,omitempty"`},
	{"Rows", "int", `json:"rows,omitempty"`},
	{"Reason", "string", `json:"reason,omitempty"`},
	{"State", "string", `json:"state,omitempty"`},
	{"Counters", "map[string]uint64", `json:"counters,omitempty"`},
	{"Digest", "string", `json:"digest,omitempty"`},
	{"ExitStatus", "*int", `json:"exit_status,omitempty"`},
	{"StreamEnded", "*bool", `json:"stream_ended,omitempty"`},
	{"TUIExited", "*bool", `json:"tui_exited,omitempty"`},
	{"TreeGone", "*bool", `json:"tree_gone,omitempty"`},
}

// diffFields returns every difference between the exported fields of typ and
// the reviewed list: a field that is not listed, a listed field that is gone
// and a changed type or tag.
func diffFields(typ reflect.Type, want []frozenField) []string {
	var bad []string
	have := map[string]frozenField{}
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		have[f.Name] = frozenField{f.Name, f.Type.String(), string(f.Tag)}
	}
	listed := map[string]bool{}
	for _, w := range want {
		listed[w.name] = true
		h, ok := have[w.name]
		switch {
		case !ok:
			bad = append(bad, fmt.Sprintf("%s: reviewed field %s is gone", typ.Name(), w.name))
		case h != w:
			bad = append(bad, fmt.Sprintf("%s.%s is %s %s, reviewed as %s %s", typ.Name(), w.name, h.typ, h.tag, w.typ, w.tag))
		}
	}
	for i := 0; i < typ.NumField(); i++ {
		if n := typ.Field(i).Name; !listed[n] {
			bad = append(bad, fmt.Sprintf("%s.%s %s is not in the reviewed set", typ.Name(), n, typ.Field(i).Type))
		}
	}
	if typ.NumField() != len(want) && len(bad) == 0 {
		bad = append(bad, fmt.Sprintf("%s has %d fields, %d reviewed", typ.Name(), typ.NumField(), len(want)))
	}
	return bad
}

func TestH14FieldSetIsReviewed(t *testing.T) {
	for _, c := range []struct {
		typ  reflect.Type
		want []frozenField
	}{
		{reflect.TypeOf(Event{}), reviewedEvent},
		{reflect.TypeOf(Fields{}), reviewedFields},
	} {
		for _, p := range diffFields(c.typ, c.want) {
			t.Error(p)
		}
	}
}

func TestH14TheFrozenListCanFail(t *testing.T) {
	want := []frozenField{
		{"Role", "string", `json:"role"`},
		{"Pid", "int", `json:"pid"`},
	}
	cases := map[string]any{
		"same": struct {
			Role string `json:"role"`
			Pid  int    `json:"pid"`
		}{},
		"a new field with a harmless name": struct {
			Role   string `json:"role"`
			Pid    int    `json:"pid"`
			Detail string `json:"detail"`
		}{},
		"a changed type": struct {
			Role string `json:"role"`
			Pid  int64  `json:"pid"`
		}{},
		"a changed tag": struct {
			Role string `json:"role"`
			Pid  int    `json:"process"`
		}{},
		"a removed field": struct {
			Role string `json:"role"`
		}{},
		"a renamed field": struct {
			Role string `json:"role"`
			PID  int    `json:"pid"`
		}{},
	}
	for name, v := range cases {
		bad := diffFields(reflect.TypeOf(v), want)
		if (name == "same") != (len(bad) == 0) {
			t.Errorf("%s: problems %v", name, bad)
		}
	}
}
