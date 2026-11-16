package sessionreplay_test

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"ataniq/internal/terminalconsole/sessionreplay"
	"ataniq/internal/terminalconsole/sessionreplay/fixturegen"
)

func generated(t *testing.T) []fixturegen.File {
	t.Helper()
	files, err := fixturegen.Files()
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestFixturesRegenerateByteIdentically(t *testing.T) {
	first := generated(t)
	second := generated(t)
	if !reflect.DeepEqual(first, second) {
		t.Fatal("two runs of the generator differ")
	}
	var names []string
	for _, f := range first {
		names = append(names, f.Name)
		got, err := sessionreplay.FixtureBytes(f.Name)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, f.Data) {
			t.Errorf("%s differs from the generator output; run go generate ./... in the package directory", f.Name)
		}
		// The files on disk are the embedded ones.
		disk, err := os.ReadFile(filepath.Join("testdata", f.Name))
		if err != nil || !bytes.Equal(disk, f.Data) {
			t.Errorf("testdata/%s differs from the generator output (%v)", f.Name, err)
		}
	}
	if got := sessionreplay.FixtureNames(); !reflect.DeepEqual(got, names) {
		t.Fatalf("embedded %v, generated %v", got, names)
	}
	disk, _ := filepath.Glob(filepath.Join("testdata", "*.jsonl"))
	if len(disk) != len(names) {
		t.Fatalf("testdata has %d recordings, the generator makes %d", len(disk), len(names))
	}
}

func TestFixturesAreValidAndPlayToTheEnd(t *testing.T) {
	for _, name := range sessionreplay.FixtureNames() {
		rec, err := sessionreplay.LoadFixture(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		enc, err := sessionreplay.Encode(rec)
		data, _ := sessionreplay.FixtureBytes(name)
		if err != nil || !bytes.Equal(enc, data) {
			t.Errorf("%s: does not re-encode to itself (%v)", name, err)
		}
		p, err := sessionreplay.NewPlayer(rec)
		if err != nil {
			t.Fatal(err)
		}
		var got []sessionreplay.Event
		for at := time.Duration(0); !p.Done(); at += 16 * time.Millisecond {
			got = append(got, p.Advance(at)...)
			if at > time.Minute {
				t.Fatalf("%s: never finishes", name)
			}
		}
		if !reflect.DeepEqual(got, rec.Events) {
			t.Errorf("%s: player events differ from the recording", name)
		}
	}
	if _, err := sessionreplay.LoadFixture("missing.jsonl"); err == nil {
		t.Error("loaded a missing fixture")
	}
}

// stream joins the output chunks of a recording.
func stream(rec *sessionreplay.Recording) []byte {
	var b []byte
	for _, e := range rec.Events {
		if e.Kind == sessionreplay.KindOutput {
			b = append(b, e.Data...)
		}
	}
	return b
}

func fixtures(t *testing.T) map[string]*sessionreplay.Recording {
	t.Helper()
	m := map[string]*sessionreplay.Recording{}
	for _, name := range sessionreplay.FixtureNames() {
		rec, err := sessionreplay.LoadFixture(name)
		if err != nil {
			t.Fatal(err)
		}
		m[name] = rec
	}
	return m
}

func chunks(rec *sessionreplay.Recording) []string {
	var c []string
	for _, e := range rec.Events {
		if e.Kind == sessionreplay.KindOutput {
			c = append(c, string(e.Data))
		}
	}
	return c
}

func count(rec *sessionreplay.Recording, sub string) int {
	return strings.Count(string(stream(rec)), sub)
}

var csiComplete = regexp.MustCompile("^\x1b\\[[0-9;?<=>]*[@-~]")

// endsMidSequence reports whether a chunk before the last ends inside an
// escape sequence: an ESC with no final byte or terminator yet.
func endsMidSequence(cs []string) bool {
	for _, c := range cs[:len(cs)-1] {
		i := strings.LastIndex(c, "\x1b")
		if i < 0 {
			continue
		}
		tail := c[i:]
		switch {
		case tail == "\x1b":
			return true
		case strings.HasPrefix(tail, "\x1b[") && !csiComplete.MatchString(tail):
			return true
		case strings.HasPrefix(tail, "\x1b]") && !strings.Contains(tail, "\x07") && !strings.Contains(tail, "\x1b\\"):
			return true
		}
	}
	return false
}

// cutRune reports whether a chunk ends inside the UTF-8 bytes of a code point
// that the next chunk finishes, and returns that code point.
func cutRune(a, b string) (rune, bool) {
	if a == "" || b == "" || b[0]&0xc0 != 0x80 {
		return 0, false
	}
	for n := 1; n <= 3 && n <= len(a); n++ {
		c := a[len(a)-n]
		if c&0xc0 == 0x80 {
			continue
		}
		need := 1
		switch {
		case c&0xf8 == 0xf0:
			need = 4
		case c&0xf0 == 0xe0:
			need = 3
		case c&0xe0 == 0xc0:
			need = 2
		}
		if n >= need || len(b) < need-n {
			return 0, false
		}
		r, size := utf8.DecodeRuneInString(a[len(a)-n:] + b[:need-n])
		return r, size == need && r != utf8.RuneError
	}
	return 0, false
}

// cutRunes returns the code points that a chunk boundary of the recording
// cuts, in order.
func cutRunes(rec *sessionreplay.Recording) []rune {
	var rs []rune
	cs := chunks(rec)
	for i := 0; i+1 < len(cs); i++ {
		if r, ok := cutRune(cs[i], cs[i+1]); ok {
			rs = append(rs, r)
		}
	}
	return rs
}

func hasRune(rs []rune, want func(rune) bool) bool {
	for _, r := range rs {
		if want(r) {
			return true
		}
	}
	return false
}

// continuesClose reports whether chunk c finishes a mode-set sequence that
// the output before it left cut, as "26l" finishes "\x1b[?20".
func continuesClose(before, c string) bool {
	const off = "\x1b[?2026l"
	for n := 1; n < len(off); n++ {
		if strings.HasSuffix(before, off[:n]) && strings.HasPrefix(c, off[n:]) {
			return true
		}
	}
	return false
}

// heldLimit is how long the console waits for the end of a synchronized frame.
// The fixture holds one open for longer.
const heldLimit = 150 * time.Millisecond

// The fixtures exist to exercise these things; a change to the generator that
// loses one fails here.
func TestFixturesCoverWhatTheyAreFor(t *testing.T) {
	fx := fixtures(t)
	const (
		zwj = "\u200d"
		bel = "\x07"
		st  = "\x1b\\"
	)

	// Emoji ZWJ sequences split across chunks: after a ZWJ, before one, and
	// inside the bytes of a code point.
	rec := fx["zwj-split.jsonl"]
	if count(rec, "\U0001F468"+zwj+"\U0001F469"+zwj+"\U0001F467"+zwj+"\U0001F466") != 1 {
		t.Error("zwj: no family sequence")
	}
	var afterZWJ, beforeZWJ bool
	cs := chunks(rec)
	for i := 0; i+1 < len(cs); i++ {
		afterZWJ = afterZWJ || strings.HasSuffix(cs[i], zwj)
		beforeZWJ = beforeZWJ || strings.HasPrefix(cs[i+1], zwj)
	}
	cut := cutRunes(rec)
	insideZWJ := hasRune(cut, func(r rune) bool { return r == 0x200d })
	insideEmoji := hasRune(cut, func(r rune) bool { return r >= 0x1f000 })
	if !afterZWJ || !beforeZWJ || !insideEmoji || !insideZWJ {
		t.Errorf("zwj: split after %v, before %v, inside an emoji %v, inside a ZWJ %v",
			afterZWJ, beforeZWJ, insideEmoji, insideZWJ)
	}

	// OSC 0 and 2 titles with the two marks, each mark with both terminators,
	// and titles split across chunks, between the bytes of the marks as well.
	rec = fx["title.jsonl"]
	s := string(stream(rec))
	for _, w := range []string{
		"\x1b]0;\u2733 synthetic task" + bel,
		"\x1b]2;\u2733 synthetic star" + st,
		"\x1b]2;\u2705 synthetic task done" + st,
		"\x1b]0;\u2705 synthetic check" + bel,
		"\x1b]2;\u2733 \u2705 both marks" + st,
	} {
		if !strings.Contains(s, w) {
			t.Errorf("title: missing %q", w)
		}
	}
	if !endsMidSequence(chunks(rec)) {
		t.Error("title: no title split across chunks")
	}
	cut = cutRunes(rec)
	if !hasRune(cut, func(r rune) bool { return r == 0x2733 }) || !hasRune(cut, func(r rune) bool { return r == 0x2705 }) {
		t.Errorf("title: code points cut inside a mark: %U, want U+2733 and U+2705", cut)
	}

	// Mode sets: each is set and reset, and some are cut at a chunk boundary.
	rec = fx["modes.jsonl"]
	for _, m := range []string{"1000", "1002", "1003", "1006", "2004", "1004", "1049"} {
		if count(rec, "\x1b[?"+m+"h") != 1 || count(rec, "\x1b[?"+m+"l") != 1 {
			t.Errorf("modes: ?%s is not set and reset once", m)
		}
	}
	if !endsMidSequence(chunks(rec)) {
		t.Error("modes: no chunk ends inside a sequence")
	}

	// Synchronized frames: balanced, chunk boundaries between pieces of frame
	// content in several frames (the cut inside the closing sequence does not
	// count), the mode sequence itself cut, whole several-frame chunks, and a
	// frame held open longer than heldLimit.
	rec = fx["sync-frames.jsonl"]
	const (
		syncOn  = "\x1b[?2026h"
		syncOff = "\x1b[?2026l"
	)
	if on, off := count(rec, syncOn), count(rec, syncOff); on != off || on < 8 {
		t.Errorf("sync: %d begins, %d ends", on, off)
	}
	var multi bool
	var held, openedAt time.Duration
	cutFrames := map[int]bool{}
	var seen string
	for _, e := range rec.Events {
		if e.Kind != sessionreplay.KindOutput {
			continue
		}
		c := string(e.Data)
		was := strings.LastIndex(seen, syncOn) > strings.LastIndex(seen, syncOff)
		if was && !continuesClose(seen, c) {
			cutFrames[strings.Count(seen, syncOn)] = true
		}
		if strings.Count(c, syncOn) >= 2 && strings.Count(c, syncOff) >= 2 {
			multi = true
		}
		seen += c
		now := strings.LastIndex(seen, syncOn) > strings.LastIndex(seen, syncOff)
		if now && strings.Contains(c, syncOn) {
			openedAt = e.At
		}
		if was && !now && e.At-openedAt > held {
			held = e.At - openedAt
		}
	}
	if len(cutFrames) < 3 || !multi || !endsMidSequence(chunks(rec)) || held <= heldLimit {
		t.Errorf("sync: frames with a boundary in their content %d (want 3), multi-frame chunk %v, longest hold %v (want over %v)",
			len(cutFrames), multi, held, heldLimit)
	}

	// Colour queries with both terminators, device attribute queries, and a
	// split query.
	rec = fx["queries.jsonl"]
	for _, q := range []string{"\x1b]10;?" + bel, "\x1b]11;?" + st, "\x1b]12;?" + bel, "\x1b[c", "\x1b[0c", "\x1b[>c"} {
		if count(rec, q) == 0 {
			t.Errorf("queries: missing %q", q)
		}
	}
	if !endsMidSequence(chunks(rec)) {
		t.Error("queries: no query split across chunks")
	}

	// An OSC 52 clipboard write, with a made-up body.
	rec = fx["clipboard.jsonl"]
	if count(rec, "\x1b]"+osc52Body+bel) != 1 {
		t.Errorf("clipboard: no OSC 52 write of %q", osc52Body)
	}
	if b, err := base64.StdEncoding.Strict().DecodeString(strings.TrimPrefix(osc52Body, "52;c;")); err != nil || string(b) != "synthetic clipboard text" {
		t.Errorf("clipboard: the body decodes to %q (%v)", b, err)
	}

	// A resize to another size, with output after it.
	rec = fx["resize.jsonl"]
	var sizes [][2]int
	for _, e := range rec.Events {
		if e.Kind == sessionreplay.KindResize {
			sizes = append(sizes, [2]int{e.Cols, e.Rows})
		}
	}
	if len(sizes) < 1 || sizes[0] == [2]int{rec.Cols, rec.Rows} {
		t.Errorf("resize: %v", sizes)
	}
	if rec.Events[len(rec.Events)-2].Kind != sessionreplay.KindOutput {
		t.Error("resize: no output after the last resize")
	}

	// Colour: basic, 256 and true colour.
	rec = fx["exit-zero.jsonl"]
	for _, c := range []string{"\x1b[31m", "\x1b[38;5;", "\x1b[38;2;"} {
		if count(rec, c) == 0 {
			t.Errorf("colour: missing %q", c)
		}
	}

	// A normal end, a non-zero end and an end with no known status; every
	// recording ends once.
	status := map[string]int{}
	known := map[string]bool{}
	for name, r := range fx {
		last := r.Events[len(r.Events)-1]
		if last.Kind != sessionreplay.KindEnd {
			t.Errorf("%s: last record is %v", name, last.Kind)
		}
		status[name], known[name] = last.Status, last.StatusKnown
	}
	if status["exit-zero.jsonl"] != 0 || !known["exit-zero.jsonl"] ||
		status["exit-nonzero.jsonl"] == 0 || !known["exit-nonzero.jsonl"] {
		t.Errorf("end statuses %v, known %v", status, known)
	}
	if known["exit-unknown.jsonl"] || status["exit-unknown.jsonl"] != 0 {
		t.Errorf("exit-unknown: status %d, known %v", status["exit-unknown.jsonl"], known["exit-unknown.jsonl"])
	}
	unknown, _ := sessionreplay.FixtureBytes("exit-unknown.jsonl")
	if !bytes.HasSuffix(unknown, []byte("\"k\":\"end\"}\n")) {
		t.Errorf("exit-unknown: the end record is not a bare end: %q", unknown)
	}
}

// pieces splits a stream into its visible text runs and its OSC bodies. It
// reads only what the generator writes: CSI, OSC and C0 controls.
func pieces(t *testing.T, b []byte) (text, osc []string) {
	t.Helper()
	var run []byte
	flush := func() {
		if s := strings.TrimSpace(string(run)); s != "" {
			text = append(text, s)
		}
		run = run[:0]
	}
	for i := 0; i < len(b); {
		c := b[i]
		switch {
		case c == 0x1b && i+1 < len(b) && b[i+1] == '[':
			flush()
			j := i + 2
			for j < len(b) && (b[j] < 0x40 || b[j] > 0x7e) {
				j++
			}
			if j >= len(b) {
				t.Fatal("unterminated CSI")
			}
			i = j + 1
		case c == 0x1b && i+1 < len(b) && b[i+1] == ']':
			flush()
			j := i + 2
			for j < len(b) && b[j] != 0x07 && !(b[j] == 0x1b && j+1 < len(b) && b[j+1] == '\\') {
				j++
			}
			if j >= len(b) {
				t.Fatal("unterminated OSC")
			}
			osc = append(osc, string(b[i+2:j]))
			if b[j] == 0x07 {
				i = j + 1
			} else {
				i = j + 2
			}
		case c == 0x1b:
			t.Fatalf("escape sequence the checker does not know at %d", i)
		case c < 0x20 || c == 0x7f:
			flush()
			i++
		default:
			run = append(run, c)
			i++
		}
	}
	flush()
	return text, osc
}

func TestFixtureTextIsOnlyWhatTheGeneratorKnows(t *testing.T) {
	known := map[string]bool{}
	for _, k := range fixturegen.KnownText() {
		known[k] = true
	}
	seen := map[string]bool{}
	for name, rec := range fixtures(t) {
		text, osc := pieces(t, stream(rec))
		for _, s := range append(text, osc...) {
			seen[s] = true
			if !known[s] {
				t.Errorf("%s: %q is not known to the generator", name, s)
			}
		}
	}
	for k := range known {
		if !seen[k] {
			t.Errorf("known text %q is in no fixture", k)
		}
	}
}

func TestKnownTextIsSortedAndUnique(t *testing.T) {
	k := fixturegen.KnownText()
	if !sort.StringsAreSorted(k) {
		t.Error("not sorted")
	}
	for i := 1; i < len(k); i++ {
		if k[i] == k[i-1] {
			t.Errorf("%q twice", k[i])
		}
	}
}

var (
	dotted     = regexp.MustCompile(`[A-Za-z0-9-]\.[A-Za-z0-9-]`)
	windowsAbs = regexp.MustCompile(`[A-Za-z]:[\\/]`)
	oscBody    = regexp.MustCompile(`^(0|2|10|11|12);`)
	recordLine = regexp.MustCompile(`^\{"t":\d+,"k":"(out","d":"[A-Za-z0-9+/]+=*"|resize","cols":\d+,"rows":\d+|end"(,"status":\d+)?)\}$`)
	// osc52Body is the one OSC 52 write the fixtures hold: the base64 of a
	// made-up text. The privacy check lets this body and no other past its
	// shape rules.
	osc52Body  = "52;c;c3ludGhldGljIGNsaXBib2FyZCB0ZXh0"
	privatePfx = []string{"/home/", "/users/", "/root", "/tmp", "/var", "/etc", "/mnt", "/opt", "/usr"}
)

// privacyProblem names what makes visible text look like a path, a user or a
// host. Generated text has no slash, tilde, at sign, colon or dotted name.
func privacyProblem(s string) string {
	switch {
	case strings.ContainsAny(s, `/\`):
		return "a slash, so a possible path"
	case strings.Contains(s, "~"):
		return "a tilde, so a possible home path"
	case strings.Contains(s, "@"):
		return "an at sign, so a possible user or address"
	case strings.Contains(s, ":"):
		return "a colon, so a possible URL, address or drive"
	case dotted.MatchString(s), windowsAbs.MatchString(s):
		return "a dotted name, so a possible host or address"
	}
	return ""
}

func TestPrivacyCheckCatchesWhatItShould(t *testing.T) {
	for _, bad := range []string{
		"/home/someone", "/etc/passwd", `C:\Users`, "~/work", "user@example", "host.example.com",
		"192.168.1.20", "https://example.net", "name.local",
	} {
		if privacyProblem(bad) == "" {
			t.Errorf("the check misses %q", bad)
		}
	}
	for _, good := range []string{"synthetic task", "frame one, line a", "\u2733 synthetic task"} {
		if p := privacyProblem(good); p != "" {
			t.Errorf("the check flags %q: %s", good, p)
		}
	}
}

func TestFixturesHoldNoPathsOrHosts(t *testing.T) {
	for _, name := range sessionreplay.FixtureNames() {
		data, _ := sessionreplay.FixtureBytes(name)
		// The file is ASCII: JSON keys and base64 only.
		for _, b := range data {
			if b >= 0x80 || (b < 0x20 && b != '\n') {
				t.Errorf("%s: file is not plain ASCII text", name)
				break
			}
		}
		lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
		for i, line := range lines[1:] {
			if !recordLine.MatchString(line) {
				t.Errorf("%s line %d: not a plain record: %s", name, i+2, line)
			}
			if j := strings.Index(line, `"d":"`); j >= 0 {
				if _, err := base64.StdEncoding.Strict().DecodeString(line[j+5 : len(line)-2]); err != nil {
					t.Errorf("%s line %d: %v", name, i+2, err)
				}
			}
		}
	}
	for name, rec := range fixtures(t) {
		out := stream(rec)
		low := strings.ToLower(string(out))
		for _, w := range privatePfx {
			if strings.Contains(low, w) {
				t.Errorf("%s: output contains %q", name, w)
			}
		}
		text, osc := pieces(t, out)
		for _, s := range text {
			if p := privacyProblem(s); p != "" {
				t.Errorf("%s: text %q has %s", name, s, p)
			}
		}
		for _, s := range osc {
			if s == osc52Body {
				continue
			}
			// An OSC body is a number, a semicolon and a title or "?".
			if !oscBody.MatchString(s) {
				t.Errorf("%s: OSC %q is not a title or colour query", name, s)
				continue
			}
			if p := privacyProblem(strings.SplitN(s, ";", 2)[1]); p != "" && !strings.HasSuffix(s, ";?") {
				t.Errorf("%s: OSC %q has %s", name, s, p)
			}
		}
	}
}
