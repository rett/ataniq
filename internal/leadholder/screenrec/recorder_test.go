//go:build linux && ataniq_canary

package screenrec

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"ataniq/internal/inject/screen"
	"ataniq/internal/terminalconsole/sessionpane"
)

// clock is a fake clock that tests move by hand.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock { return &clock{t: time.Unix(1_700_000_000, 0)} }

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// logs collects the recorder's log lines.
type logs struct {
	mu    sync.Mutex
	lines []string
}

func (l *logs) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *logs) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.lines...)
}

// rec is a recorder over a folder that does not exist yet, in a temporary
// parent.
type rec struct {
	t     *testing.T
	r     *Recorder
	dir   string
	clock *clock
	log   *logs
}

func newRec(t *testing.T, tweak func(*Config)) *rec {
	t.Helper()
	x := &rec{t: t, dir: filepath.Join(t.TempDir(), DirName), clock: newClock(), log: &logs{}}
	cfg := Config{Now: x.clock.now, Logf: x.log.logf}
	if tweak != nil {
		tweak(&cfg)
	}
	x.r = New(cfg)
	return x
}

func (x *rec) open() {
	x.t.Helper()
	if err := x.r.Open(x.dir); err != nil {
		x.t.Fatal(err)
	}
}

func (x *rec) read(name string) string {
	x.t.Helper()
	b, err := os.ReadFile(filepath.Join(x.dir, name))
	if err != nil {
		x.t.Fatal(err)
	}
	return string(b)
}

func (x *rec) names() []string {
	x.t.Helper()
	ents, err := os.ReadDir(x.dir)
	if err != nil {
		x.t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

func (x *rec) lines(name string) []map[string]any {
	x.t.Helper()
	var out []map[string]any
	sc := bufio.NewScanner(strings.NewReader(x.read(name)))
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			x.t.Fatalf("%s: a line is not JSON: %v: %q", name, err, sc.Text())
		}
		out = append(out, m)
	}
	return out
}

func sample(text string) screen.Snapshot {
	return screen.Snapshot{
		Text: text, Cols: 40, Rows: 3, CursorX: 2, CursorY: 1, CursorHidden: false, AltScreen: true,
		Title: "✳ sample <title> & more",
		Faint: []screen.Span{{Row: 1, From: 2, To: 9}},
	}
}

// fixture is the fixture format of internal/inject/screen/testdata, read the
// way its replay test reads it.
type fixture struct {
	Source       string `json:"source"`
	Description  string `json:"description"`
	Cols         int    `json:"cols"`
	Rows         int    `json:"rows"`
	CursorX      int    `json:"cursor_x"`
	CursorY      int    `json:"cursor_y"`
	CursorHidden bool   `json:"cursor_hidden"`
	AltScreen    bool   `json:"alt_screen"`
	Title        string `json:"title"`
	Faint        []struct {
		Row  int `json:"row"`
		From int `json:"from"`
		To   int `json:"to"`
	} `json:"faint"`
}

func (f fixture) snapshot(text string) screen.Snapshot {
	s := screen.Snapshot{
		Text: text, Cols: f.Cols, Rows: f.Rows, CursorX: f.CursorX, CursorY: f.CursorY,
		CursorHidden: f.CursorHidden, AltScreen: f.AltScreen, Title: f.Title,
	}
	for _, sp := range f.Faint {
		s.Faint = append(s.Faint, screen.Span{Row: sp.Row, From: sp.From, To: sp.To})
	}
	return s
}

// The folder is made 0700 and every file 0600; README.txt says the files are
// raw; nothing but the fixed names is in the folder.
func TestOpenMakesAPrivateFolderWithTheReadme(t *testing.T) {
	x := newRec(t, nil)
	x.open()
	x.r.Screen(sample("one"))
	x.r.Title("a title")
	x.r.OSC7501([]byte("state=idle"))
	x.r.Close()
	fi, err := os.Lstat(x.dir)
	if err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
		t.Fatalf("the folder: %v %v", fi, err)
	}
	for _, n := range x.names() {
		fi, err := os.Lstat(filepath.Join(x.dir, n))
		if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 {
			t.Errorf("%s: %v %v", n, fi, err)
		}
	}
	want := []string{"0001.json", "0001.txt", ReadmeName, OSCName, TitlesName}
	if got := x.names(); !reflect.DeepEqual(got, want) {
		t.Fatalf("names %v, want %v", got, want)
	}
	if !strings.Contains(x.read(ReadmeName), "raw, scrub before use; do not commit") {
		t.Fatalf("README.txt: %s", x.read(ReadmeName))
	}
	if len(x.log.all()) != 0 {
		t.Errorf("log lines: %v", x.log.all())
	}
}

// Open makes a new folder: one that exists, or a link at the name, is refused
// and nothing is written through it.
func TestOpenRefusesAnExistingFolderOrALink(t *testing.T) {
	x := newRec(t, nil)
	if err := os.Mkdir(x.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := x.r.Open(x.dir); err == nil {
		t.Fatal("an existing folder was opened")
	}
	if ents, _ := os.ReadDir(x.dir); len(ents) != 0 {
		t.Fatalf("written into the existing folder: %v", ents)
	}
	if !x.r.Stopped() {
		t.Error("a failed Open left the recorder taking items")
	}

	y := newRec(t, nil)
	target := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, y.dir); err != nil {
		t.Fatal(err)
	}
	err := y.r.Open(y.dir)
	if err == nil {
		t.Fatal("a link was opened")
	}
	if strings.Contains(err.Error(), y.dir) || strings.Contains(err.Error(), target) {
		t.Errorf("the error names a path: %v", err)
	}
	if ents, _ := os.ReadDir(target); len(ents) != 0 {
		t.Fatalf("written through the link: %v", ents)
	}
	for _, bad := range []string{"", "relative/record", "/a/../record", "/"} {
		if err := New(Config{}).Open(bad); err == nil {
			t.Errorf("Open(%q) passed", bad)
		}
	}
}

// One pair per distinct screen, in order, named by a counter, in the fixture
// format: the text with a final newline, the fields of the snapshot, "source":
// "recorded", an empty description and no "want".
func TestPairsForDistinctScreensOnly(t *testing.T) {
	x := newRec(t, nil)
	x.open()
	a, b := sample("first row\n  ask anything\nlast"), sample("other\n\nrows")
	b.CursorHidden, b.AltScreen, b.Faint, b.Title = true, false, nil, ""
	for _, s := range []screen.Snapshot{a, a, b, b, a} {
		x.r.Screen(s)
	}
	x.r.Close()
	want := []string{"0001.json", "0001.txt", "0002.json", "0002.txt", "0003.json", "0003.txt", ReadmeName, OSCName, TitlesName}
	if got := x.names(); !reflect.DeepEqual(got, want) {
		t.Fatalf("names %v, want %v", got, want)
	}
	for i, s := range []screen.Snapshot{a, b, a} {
		name := fmt.Sprintf("%04d", i+1)
		text := x.read(name + ".txt")
		if text != s.Text+"\n" {
			t.Errorf("%s.txt %q, want the text and a newline", name, text)
		}
		raw := x.read(name + ".json")
		var keys map[string]any
		if err := json.Unmarshal([]byte(raw), &keys); err != nil {
			t.Fatal(err)
		}
		var got []string
		for k := range keys {
			got = append(got, k)
		}
		sort.Strings(got)
		wantKeys := []string{"alt_screen", "cols", "cursor_hidden", "cursor_x", "cursor_y", "description", "faint", "rows", "source", "title"}
		if !reflect.DeepEqual(got, wantKeys) {
			t.Errorf("%s.json keys %v, want %v", name, got, wantKeys)
		}
		if keys["source"] != SourceRecorded || keys["description"] != "" {
			t.Errorf("%s.json source %v description %v", name, keys["source"], keys["description"])
		}
		if f, ok := keys["faint"].([]any); !ok || f == nil {
			t.Errorf("%s.json faint is not a list: %v", name, keys["faint"])
		}
		var f fixture
		dec := json.NewDecoder(strings.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&f); err != nil {
			t.Fatal(err)
		}
		snap := f.snapshot(strings.TrimSuffix(text, "\n"))
		if len(s.Faint) == 0 {
			s.Faint = nil
		}
		if !reflect.DeepEqual(snap, s) {
			t.Errorf("%s read back\n got %+v\nwant %+v", name, snap, s)
		}
		if !strings.HasPrefix(raw, "{\n \"source\": \"recorded\",\n \"description\": \"\",\n") {
			t.Errorf("%s.json is not laid out as the fixtures are: %q", name, raw)
		}
		if s.Title != "" && !strings.Contains(raw, s.Title) {
			t.Errorf("%s.json does not hold the title as it is (< and & unescaped): %q", name, raw)
		}
	}
}

// The pairs stop at MaxPairs: the pair that would pass it is not written, one
// line goes to LIMIT.txt and one to the log, and the titles and payloads go on
// being recorded. Screen takes no more pairs and a look takes no snapshot.
func TestPairLimitStopsPairsOnly(t *testing.T) {
	x := newRec(t, func(c *Config) { c.MaxPairs = 3 })
	x.open()
	for i := 0; i < 6; i++ {
		x.r.Screen(sample(fmt.Sprintf("screen %d", i)))
	}
	deadline := time.Now().Add(10 * time.Second)
	for x.r.takesPairs() {
		if time.Now().After(deadline) {
			t.Fatal("the pairs did not stop")
		}
		time.Sleep(time.Millisecond)
	}
	p := &fakePane{cols: 40, rows: 5, title: "after the limit", text: "after"}
	var w watcher
	w.look(x.r, p.screen, true)
	if p.snaps != 0 {
		t.Errorf("%d snapshots after the pairs stopped", p.snaps)
	}
	x.r.OSC7501([]byte("state=idle"))
	x.r.Close()
	if _, err := os.Lstat(filepath.Join(x.dir, "0004.txt")); err == nil {
		t.Fatal("a fourth pair was written")
	}
	if _, err := os.Lstat(filepath.Join(x.dir, "0003.json")); err != nil {
		t.Fatal("the third pair is missing")
	}
	lim := x.read(LimitName)
	if strings.Count(lim, "\n") != 1 || !strings.HasPrefix(lim, "screens stopped: 3 screens") {
		t.Fatalf("LIMIT.txt %q", lim)
	}
	if x.r.Stopped() {
		t.Error("the recording stopped")
	}
	if l := x.log.all(); len(l) != 1 || !strings.Contains(l[0], "no more screens") || strings.Contains(l[0], x.dir) {
		t.Errorf("log %q", l)
	}
	if titles := x.lines(TitlesName); len(titles) != 1 || titles[0]["title"] != "after the limit" {
		t.Errorf("titles after the limit: %v", titles)
	}
	if osc := x.lines(OSCName); len(osc) != 1 || osc[0]["payload"] != "state=idle" {
		t.Errorf("payloads after the limit: %v", osc)
	}
}

// The bytes stop at MaxBytes, README.txt included and LIMIT.txt not.
func TestByteLimit(t *testing.T) {
	const max = 4096
	x := newRec(t, func(c *Config) { c.MaxBytes = max })
	x.open()
	for i := 0; i < 50; i++ {
		x.r.Screen(sample(fmt.Sprintf("screen %d %s", i, strings.Repeat("x", 200))))
	}
	x.r.Close()
	total := int64(0)
	for _, n := range x.names() {
		if n == LimitName {
			continue
		}
		fi, err := os.Lstat(filepath.Join(x.dir, n))
		if err != nil {
			t.Fatal(err)
		}
		total += fi.Size()
	}
	if total > max {
		t.Fatalf("%d bytes written, the bound is %d", total, max)
	}
	if lim := x.read(LimitName); !strings.Contains(lim, "would pass 4096 bytes") {
		t.Fatalf("LIMIT.txt %q", lim)
	}
	if _, err := os.Lstat(filepath.Join(x.dir, "0001.txt")); err != nil {
		t.Fatal("no pair was written before the bound")
	}
}

// The constants are the task's: 400 pairs, 8 MiB, 4 KiB, 500 ms, 300 ms.
func TestTheBounds(t *testing.T) {
	if MaxPairs != 400 || MaxBytes != 8<<20 || MaxPayload != 4096 || PairInterval != 500*time.Millisecond ||
		Settle != 300*time.Millisecond || DirName != "record" {
		t.Fatal("a bound changed")
	}
	r := New(Config{})
	if r.cfg.MaxPairs != MaxPairs || r.cfg.MaxBytes != MaxBytes || r.cfg.PairInterval != PairInterval || r.cfg.QueueLen != QueueLen ||
		r.cfg.Settle != Settle {
		t.Fatalf("defaults %+v", r.cfg)
	}
}

// Titles and payloads are agent-controlled: each is cut at MaxPayload bytes
// and stored escaped as a JSON string, with the elapsed milliseconds.
func TestTitlesAndPayloadsAreCutAndEscaped(t *testing.T) {
	x := newRec(t, nil)
	x.open()
	x.clock.add(1500 * time.Millisecond)
	evil := "\x1b]0;x\x07\"quote\" \\ back\nnew\u2028line\x00nul"
	x.r.Title(evil)
	x.r.Title(evil)                   // the same title is not recorded twice
	long := strings.Repeat("é", 3000) // 6000 bytes
	x.r.Title(long)
	x.clock.add(250 * time.Millisecond)
	x.r.OSC7501([]byte(evil))
	big := bytes.Repeat([]byte{'a'}, 5000)
	x.r.OSC7501(big)
	x.r.OSC7501([]byte{'s', 0xff, 0xfe, 'x'})
	x.r.Close()

	for _, name := range []string{TitlesName, OSCName} {
		raw := x.read(name)
		for i := 0; i < len(raw); i++ {
			if c := raw[i]; (c < 0x20 && c != '\n') || c == 0x7f {
				t.Fatalf("%s holds the raw byte %#x: %q", name, c, raw)
			}
		}
		if strings.Contains(raw, "\u2028") {
			t.Errorf("%s holds a raw line separator", name)
		}
	}
	titles := x.lines(TitlesName)
	if len(titles) != 2 {
		t.Fatalf("%d title lines", len(titles))
	}
	if titles[0]["title"] != evil || titles[0]["ms"] != float64(1500) || titles[0]["cut"] != false || titles[0]["bytes"] != float64(len(evil)) {
		t.Errorf("title line %v", titles[0])
	}
	cut, _ := titles[1]["title"].(string)
	if len(cut) > MaxPayload || !strings.HasPrefix(long, cut) || titles[1]["cut"] != true || titles[1]["bytes"] != float64(len(long)) {
		t.Errorf("long title: %d bytes, cut %v, bytes %v", len(cut), titles[1]["cut"], titles[1]["bytes"])
	}

	osc := x.lines(OSCName)
	if len(osc) != 3 {
		t.Fatalf("%d payload lines", len(osc))
	}
	if osc[0]["payload"] != evil || osc[0]["ms"] != float64(1750) || osc[0]["utf8"] != true {
		t.Errorf("payload line %v", osc[0])
	}
	if p, _ := osc[1]["payload"].(string); len(p) != MaxPayload || osc[1]["cut"] != true || osc[1]["bytes"] != float64(5000) {
		t.Errorf("big payload: %d bytes, %v", len(p), osc[1]["cut"])
	}
	if osc[2]["utf8"] != false || !strings.HasPrefix(osc[2]["payload"].(string), "s") {
		t.Errorf("invalid payload line %v", osc[2])
	}
}

// The queue drops the oldest item when it is full and counts the drops into
// LIMIT.txt as "dropped N".
func TestQueueDropsTheOldest(t *testing.T) {
	x := newRec(t, func(c *Config) { c.QueueLen = 2 })
	// Before Open the writer does not run, so the queue fills.
	for i := 1; i <= 5; i++ {
		x.r.OSC7501([]byte(fmt.Sprintf("payload-%d", i)))
	}
	x.open()
	x.r.Close()
	osc := x.lines(OSCName)
	if len(osc) != 2 || osc[0]["payload"] != "payload-4" || osc[1]["payload"] != "payload-5" {
		t.Fatalf("payload lines %v", osc)
	}
	if lim := x.read(LimitName); lim != "dropped 3\n" {
		t.Fatalf("LIMIT.txt %q", lim)
	}
	if x.r.Stopped() {
		t.Error("drops stopped the recording")
	}
}

// A write that fails stops the recording quietly: one log line with no path,
// nothing more is taken, and Close returns.
func TestWriteFailureStopsQuietly(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes into a read-only folder")
	}
	x := newRec(t, nil)
	x.open()
	if err := os.Chmod(x.dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(x.dir, 0o700) })
	x.r.Screen(sample("cannot be written"))
	deadline := time.Now().Add(10 * time.Second)
	for !x.r.Stopped() {
		if time.Now().After(deadline) {
			t.Fatal("the failed write did not stop the recording")
		}
		time.Sleep(2 * time.Millisecond)
	}
	x.r.Screen(sample("after"))
	x.r.OSC7501([]byte("after"))
	x.r.Close()
	l := x.log.all()
	if len(l) != 1 || !strings.Contains(l[0], "recording screens stopped") || !strings.Contains(l[0], "permission denied") {
		t.Fatalf("log %q", l)
	}
	if strings.Contains(l[0], x.dir) || strings.Contains(l[0], "/") {
		t.Errorf("the log line names a path: %q", l[0])
	}
	if got := x.read(OSCName); got != "" {
		t.Errorf("written after the failure: %q", got)
	}
}

// Close before Open writes nothing, and a later Open is refused.
func TestCloseBeforeOpenWritesNothing(t *testing.T) {
	x := newRec(t, nil)
	x.r.OSC7501([]byte("early"))
	x.r.Close()
	x.r.Close()
	if err := x.r.Open(x.dir); err == nil {
		t.Fatal("Open after Close passed")
	}
	if _, err := os.Lstat(x.dir); err == nil {
		t.Fatal("a folder was made")
	}
}

// fakePane is a Pane whose position, size, title and screen a test sets.
type fakePane struct {
	pos        uint64
	cols, rows int
	title      string
	text       string
	snaps      int
}

func (p *fakePane) Size() (int, int) { return p.cols, p.rows }
func (p *fakePane) Title() string    { return p.title }
func (p *fakePane) TextSnapshot() sessionpane.TextSnapshot {
	p.snaps++
	return sessionpane.TextSnapshot{Text: p.text, Cols: p.cols, Rows: p.rows, Title: p.title}
}

func (p *fakePane) screen(fn func(Pane, uint64)) { fn(p, p.pos) }

// A look snapshots the pane only after output or a resize, once the screen
// has been unchanged for Settle, at most once per PairInterval, and records a
// title when it changed.
func TestLookSnapshotsOnlyChangesAndAtMostOncePerInterval(t *testing.T) {
	x := newRec(t, nil)
	x.open()
	p := &fakePane{cols: 40, rows: 5, title: "t1", text: "one"}
	var w watcher
	look := func() { w.look(x.r, p.screen, false) }
	want := func(n int, what string) {
		t.Helper()
		if p.snaps != n {
			t.Fatalf("%d snapshots %s, want %d", p.snaps, what, n)
		}
	}

	look()
	want(0, "at the first look: the screen has not settled")
	x.clock.add(300 * time.Millisecond)
	look()
	want(1, "once the first screen settled")
	x.clock.add(time.Second)
	look()
	want(1, "with nothing new")
	p.pos, p.text = 10, "two"
	look()
	want(1, "at the look that saw output")
	x.clock.add(300 * time.Millisecond)
	look()
	want(2, "once the output settled")
	p.pos, p.text, p.title = 20, "three", "t2"
	x.clock.add(100 * time.Millisecond)
	look()
	x.clock.add(300 * time.Millisecond)
	look()
	want(2, "settled, within the interval")
	x.clock.add(100 * time.Millisecond)
	look()
	want(3, "settled, after the interval")
	x.clock.add(time.Second)
	p.cols = 50
	look()
	x.clock.add(300 * time.Millisecond)
	look()
	want(4, "after a resize")
	x.clock.add(time.Second)
	p.pos = 30 // output that changed nothing visible: a snapshot, no pair
	look()
	x.clock.add(300 * time.Millisecond)
	look()
	want(5, "after output that changed nothing")
	x.r.Close()
	wantPairs := []string{"0001.json", "0001.txt", "0002.json", "0002.txt", "0003.json", "0003.txt", "0004.json", "0004.txt"}
	var pairs []string
	for _, n := range x.names() {
		if strings.HasPrefix(n, "000") {
			pairs = append(pairs, n)
		}
	}
	if !reflect.DeepEqual(pairs, wantPairs) {
		t.Fatalf("pairs %v, want %v", pairs, wantPairs)
	}
	titles := x.lines(TitlesName)
	if len(titles) != 2 || titles[0]["title"] != "t1" || titles[1]["title"] != "t2" {
		t.Fatalf("titles %v", titles)
	}
}

// A screen that keeps changing (an animated spinner) is not snapshotted while
// it changes, so it cannot fill the pairs; it is taken once it has been
// unchanged for Settle, and the last look takes it at once.
func TestLookWaitsForAnAnimationToSettle(t *testing.T) {
	x := newRec(t, nil)
	x.open()
	p := &fakePane{cols: 40, rows: 5, title: "t", text: "spin"}
	var w watcher
	for i := 0; i < 100; i++ {
		p.pos++
		p.text = fmt.Sprintf("working %c", "|/-\\"[i%4])
		w.look(x.r, p.screen, false)
		x.clock.add(100 * time.Millisecond)
	}
	if p.snaps != 0 {
		t.Fatalf("%d snapshots of a screen that never settled", p.snaps)
	}
	x.clock.add(200 * time.Millisecond)
	w.look(x.r, p.screen, false)
	if p.snaps != 1 {
		t.Fatalf("%d snapshots once it settled", p.snaps)
	}
	x.clock.add(time.Second)
	p.pos, p.text = p.pos+1, "the end"
	w.look(x.r, p.screen, true)
	if p.snaps != 2 {
		t.Fatalf("%d snapshots after the last look", p.snaps)
	}
	x.r.Close()
	if x.read("0001.txt") != "working \\\n" || x.read("0002.txt") != "the end\n" {
		t.Fatalf("pairs %q %q", x.read("0001.txt"), x.read("0002.txt"))
	}
}

// Watch stops at done and closes the recorder.
func TestWatchStopsAtDoneAndCloses(t *testing.T) {
	x := newRec(t, func(c *Config) { c.PollInterval = time.Millisecond; c.Now = time.Now })
	x.open()
	var mu sync.Mutex
	p := &fakePane{cols: 40, rows: 5, title: "t", text: "screen"}
	screenFn := func(fn func(Pane, uint64)) {
		mu.Lock()
		defer mu.Unlock()
		p.screen(fn)
	}
	done := make(chan struct{})
	ended := make(chan struct{})
	go func() { x.r.Watch(screenFn, done); close(ended) }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		mu.Lock()
		n := p.snaps
		mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no snapshot")
		}
		time.Sleep(time.Millisecond)
	}
	close(done)
	select {
	case <-ended:
	case <-time.After(10 * time.Second):
		t.Fatal("Watch did not end")
	}
	if x.read("0001.txt") != "screen\n" {
		t.Fatalf("0001.txt %q", x.read("0001.txt"))
	}
	x.r.OSC7501([]byte("after"))
	if got := x.read(OSCName); got != "" {
		t.Errorf("taken after Watch ended: %q", got)
	}
}

// When the session ends, Watch takes one last look with no interval to wait
// for: the screen shown at the end is recorded even when the last pair was
// taken just before, and what was queued is written before Watch returns. The
// test does not call Close itself.
func TestWatchRecordsTheLastScreenWhenTheSessionEnds(t *testing.T) {
	x := newRec(t, func(c *Config) {
		c.PollInterval, c.PairInterval, c.Now = time.Millisecond, time.Hour, time.Now
	})
	x.open()
	var mu sync.Mutex
	p := &fakePane{cols: 40, rows: 5, title: "t", text: "first"}
	screenFn := func(fn func(Pane, uint64)) {
		mu.Lock()
		defer mu.Unlock()
		p.screen(fn)
	}
	done := make(chan struct{})
	ended := make(chan struct{})
	go func() { x.r.Watch(screenFn, done); close(ended) }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if b, err := os.ReadFile(filepath.Join(x.dir, "0001.txt")); err == nil && string(b) == "first\n" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no first pair")
		}
		time.Sleep(time.Millisecond)
	}
	// The last screen comes within the pair interval of the first: only the
	// look at the end can take it.
	mu.Lock()
	p.pos, p.text, p.title = 10, "last", "last title"
	mu.Unlock()
	x.r.OSC7501([]byte("state=last"))
	close(done)
	select {
	case <-ended:
	case <-time.After(10 * time.Second):
		t.Fatal("Watch did not end")
	}
	if got := x.read("0002.txt"); got != "last\n" {
		t.Fatalf("0002.txt %q, want the last screen", got)
	}
	if !strings.Contains(x.read("0002.json"), `"title": "last title"`) {
		t.Errorf("0002.json %s", x.read("0002.json"))
	}
	if osc := x.lines(OSCName); len(osc) != 1 || osc[0]["payload"] != "state=last" {
		t.Errorf("payload lines %v", osc)
	}
}
