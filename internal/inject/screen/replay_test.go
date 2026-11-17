package screen

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// update rewrites the "want" of every fixture from what the recogniser reads
// now. Read the diff before committing it: it blesses whatever the code does.
var update = flag.Bool("update", false, "rewrite the want of every fixture in testdata")

// spanJSON is a Span as a fixture stores it.
type spanJSON struct {
	Row  int `json:"row"`
	From int `json:"from"`
	To   int `json:"to"`
}

// wantJSON is a Reading as a fixture stores it.
type wantJSON struct {
	Known            bool     `json:"known,omitempty"`
	Composer         bool     `json:"composer,omitempty"`
	ComposerEmpty    bool     `json:"composer_empty,omitempty"`
	ComposerText     string   `json:"composer_text,omitempty"`
	CursorInComposer bool     `json:"cursor_in_composer,omitempty"`
	Dialog           bool     `json:"dialog,omitempty"`
	DialogMarks      []string `json:"dialog_marks,omitempty"`
	BackgroundTask   bool     `json:"background_task,omitempty"`
	Mode             string   `json:"mode,omitempty"`
	PastedMarker     bool     `json:"pasted_marker,omitempty"`
	Notes            []string `json:"notes,omitempty"`
	Working          bool     `json:"working,omitempty"`
	Placeholder      bool     `json:"placeholder,omitempty"`
}

func toWant(r Reading) wantJSON {
	return wantJSON{
		Known: r.Known, Composer: r.Composer, ComposerEmpty: r.ComposerEmpty,
		ComposerText: r.ComposerText, CursorInComposer: r.CursorInComposer,
		Dialog: r.Dialog, DialogMarks: r.DialogMarks, BackgroundTask: r.BackgroundTask,
		Mode: r.Mode, PastedMarker: r.PastedMarker, Notes: r.Notes,
		Working: r.Working, Placeholder: r.Placeholder,
	}
}

// fixture is one <name>.json beside its <name>.txt.
type fixture struct {
	Source       string     `json:"source"`
	Description  string     `json:"description"`
	Cols         int        `json:"cols"`
	Rows         int        `json:"rows"`
	CursorX      int        `json:"cursor_x"`
	CursorY      int        `json:"cursor_y"`
	CursorHidden bool       `json:"cursor_hidden"`
	AltScreen    bool       `json:"alt_screen"`
	Title        string     `json:"title"`
	Faint        []spanJSON `json:"faint"`
	Want         wantJSON   `json:"want"`
}

func (f fixture) snapshot(text string) Snapshot {
	s := Snapshot{
		Text: text, Cols: f.Cols, Rows: f.Rows, CursorX: f.CursorX, CursorY: f.CursorY,
		CursorHidden: f.CursorHidden, AltScreen: f.AltScreen, Title: f.Title,
	}
	for _, sp := range f.Faint {
		s.Faint = append(s.Faint, Span{Row: sp.Row, From: sp.From, To: sp.To})
	}
	return s
}

type loaded struct {
	runtime, version, name string
	jsonPath               string
	fix                    fixture
	text                   string
}

func (l loaded) snap() Snapshot { return l.fix.snapshot(l.text) }

// loadAll reads every fixture under testdata. A folder is <runtime>-<version>.
func loadAll(t testing.TB) []loaded {
	t.Helper()
	dirs, err := filepath.Glob(filepath.Join("testdata", "*-*"))
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(dirs)
	var all []loaded
	for _, d := range dirs {
		rt, ver, ok := strings.Cut(filepath.Base(d), "-")
		if !ok {
			t.Fatalf("testdata folder %q is not <runtime>-<version>", d)
		}
		files, _ := filepath.Glob(filepath.Join(d, "*.json"))
		sort.Strings(files)
		for _, jp := range files {
			raw, err := os.ReadFile(jp)
			if err != nil {
				t.Fatal(err)
			}
			var f fixture
			if err := json.Unmarshal(raw, &f); err != nil {
				t.Fatalf("%s: %v", jp, err)
			}
			text, err := os.ReadFile(strings.TrimSuffix(jp, ".json") + ".txt")
			if err != nil {
				t.Fatalf("%s: %v", jp, err)
			}
			all = append(all, loaded{rt, ver, strings.TrimSuffix(filepath.Base(jp), ".json"), jp, f, string(text)})
		}
	}
	return all
}

func recogniserFor(t testing.TB, l loaded) Recogniser {
	t.Helper()
	rec, ok := For(l.runtime, l.version)
	if !ok {
		t.Fatalf("%s/%s: no recogniser for folder %s-%s", l.runtime, l.name, l.runtime, l.version)
	}
	return rec
}

func normalise(w wantJSON) wantJSON {
	if len(w.DialogMarks) == 0 {
		w.DialogMarks = nil
	}
	if len(w.Notes) == 0 {
		w.Notes = nil
	}
	return w
}

// TestReplay reads every recorded screen and compares the whole reading with
// the fixture's want. A new fixture needs no code: copy a screen into
// testdata, write its <name>.json, and run once with -update.
func TestReplay(t *testing.T) {
	all := loadAll(t)
	if len(all) < 25 {
		t.Fatalf("only %d fixtures, want at least 25", len(all))
	}
	for _, l := range all {
		t.Run(l.runtime+"/"+l.name, func(t *testing.T) {
			got := normalise(toWant(recogniserFor(t, l).Read(l.snap())))
			if *update {
				l.fix.Want = got
				out, err := json.MarshalIndent(l.fix, "", " ")
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(l.jsonPath, append(out, '\n'), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want := normalise(l.fix.Want)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("%s: %s\n got  %+v\n want %+v", l.jsonPath, l.fix.Description, got, want)
			}
		})
	}
}

// TestWantCoversReading keeps wantJSON in step with Reading: a new field of
// Reading must be stored in the fixtures too.
func TestWantCoversReading(t *testing.T) {
	if a, b := reflect.TypeOf(Reading{}).NumField(), reflect.TypeOf(wantJSON{}).NumField(); a != b {
		t.Fatalf("Reading has %d fields, wantJSON %d", a, b)
	}
}

// TestFixtureCoverage checks that the fixtures show what the recognisers were
// written from, so pruning the set cannot go unnoticed.
func TestFixtureCoverage(t *testing.T) {
	type kind struct {
		name string
		test func(wantJSON) bool
	}
	common := []kind{
		{"empty composer", func(w wantJSON) bool { return w.Composer && w.ComposerEmpty }},
		{"composer with a hint", func(w wantJSON) bool { return w.Composer && w.ComposerEmpty && w.Placeholder }},
		{"composer with owner text", func(w wantJSON) bool { return w.Composer && !w.ComposerEmpty && w.ComposerText != "" }},
		{"composer with a wrapped text", func(w wantJSON) bool { return strings.Contains(w.ComposerText, "\n") }},
		{"composer with a pasted header", func(w wantJSON) bool { return strings.Contains(w.ComposerText, "[ataniq-msg id=") }},
		{"open dialog", func(w wantJSON) bool { return w.Dialog && !w.Composer }},
		{"agent working", func(w wantJSON) bool { return w.Working }},
		{"unknown screen", func(w wantJSON) bool { return !w.Known }},
	}
	byRuntime := map[string][]kind{
		"claude": append([]kind{
			{"blank empty composer", func(w wantJSON) bool { return w.Composer && w.ComposerEmpty && !w.Placeholder }},
			{"background shell", func(w wantJSON) bool { return w.BackgroundTask }},
			{"manual mode", func(w wantJSON) bool { return w.Mode == "manual" }},
			{"auto mode", func(w wantJSON) bool { return w.Mode == "auto" }},
		}, common...),
		"codex": common,
	}
	counts := map[string]int{}
	seen := map[string]map[string]bool{}
	for _, l := range loadAll(t) {
		counts[l.runtime]++
		if seen[l.runtime] == nil {
			seen[l.runtime] = map[string]bool{}
		}
		for _, k := range byRuntime[l.runtime] {
			if k.test(normalise(l.fix.Want)) {
				seen[l.runtime][k.name] = true
			}
		}
	}
	for rt, kinds := range byRuntime {
		if counts[rt] < 12 {
			t.Errorf("%s: %d fixtures, want at least 12", rt, counts[rt])
		}
		for _, k := range kinds {
			if !seen[rt][k.name] {
				t.Errorf("%s: no fixture shows: %s", rt, k.name)
			}
		}
	}
}

// TestFixturesAreScrubbed keeps home paths and the like out of testdata.
func TestFixturesAreScrubbed(t *testing.T) {
	err := filepath.WalkDir("testdata", func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		home := "/ho" + "me/"
		for _, line := range strings.Split(string(raw), "\n") {
			rest := line
			for {
				i := strings.Index(rest, home)
				if i < 0 {
					break
				}
				rest = rest[i+len(home):]
				if !strings.HasPrefix(rest, "user/") && rest != "user" {
					t.Errorf("%s: a home path that is not /home/user: %q", p, line)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestReplayLayoutVariants shows the reading does not depend on how the holder
// writes the rows: trimmed or padded with blanks to the full width, with or
// without a final newline.
func TestReplayLayoutVariants(t *testing.T) {
	for _, l := range loadAll(t) {
		rec := recogniserFor(t, l)
		base := rec.Read(l.snap())
		lines := strings.Split(strings.TrimSuffix(l.text, "\n"), "\n")
		padded := make([]string, len(lines))
		for i, ln := range lines {
			if n := l.fix.Cols - len([]rune(ln)); n > 0 {
				ln += strings.Repeat(" ", n)
			}
			padded[i] = ln
		}
		for name, text := range map[string]string{
			"padded":           strings.Join(padded, "\n"),
			"padded+newline":   strings.Join(padded, "\n") + "\n",
			"trimmed":          strings.Join(lines, "\n"),
			"trimmed+newline":  strings.Join(lines, "\n") + "\n",
			"original-as-read": l.text,
		} {
			got := rec.Read(l.fix.snapshot(text))
			if !reflect.DeepEqual(normalise(toWant(got)), normalise(toWant(base))) {
				t.Errorf("%s/%s (%s):\n got  %+v\n base %+v", l.runtime, l.name, name, got, base)
			}
		}
	}
}

// rows splits fixture text into its rows, dropping the final newline.
func rows(text string) []string {
	return strings.Split(strings.TrimSuffix(text, "\n"), "\n")
}

func join(r []string) string { return strings.Join(r, "\n") + "\n" }

// dialogRows are rows of a real open dialog of each runtime, as recorded.
var dialogRows = map[string][]string{
	"claude": {
		" Esc to cancel · Tab to amend",
		" ❯ 1. Yes",
		" Do you want to create approval-test.txt?",
	},
	"codex": {
		"  Press enter to confirm or esc to cancel",
		"› 1. Yes, proceed (y)",
		"  Would you like to run the following command?",
	},
}

// TestDialogRowOverComposerNeverEmpty pastes one row of a dialog over each of
// the bottom rows of every screen that shows an empty composer. The result is
// a dialog or an unknown screen, never an empty composer.
func TestDialogRowOverComposerNeverEmpty(t *testing.T) {
	n := 0
	for _, l := range loadAll(t) {
		if !l.fix.Want.ComposerEmpty {
			continue
		}
		rec := recogniserFor(t, l)
		base := rows(l.text)
		last := -1
		for i, r := range base {
			if strings.TrimSpace(r) != "" {
				last = i
			}
		}
		for _, dr := range dialogRows[l.runtime] {
			for i := last; i > last-7 && i >= 0; i-- {
				mut := append([]string(nil), base...)
				mut[i] = dr
				got := rec.Read(l.fix.snapshot(join(mut)))
				n++
				if got.ComposerEmpty {
					t.Errorf("%s/%s: row %d replaced by %q still reads as an empty composer: %+v", l.runtime, l.name, i, dr, got)
				}
				if got.Known && !got.Dialog {
					t.Errorf("%s/%s: row %d replaced by %q is known but not a dialog: %+v", l.runtime, l.name, i, dr, got)
				}
			}
		}
	}
	if n == 0 {
		t.Fatal("no mutation ran")
	}
}

// TestProseHigherUpDoesNotCount puts the marks of a dialog into the transcript
// far above the composer and checks the reading does not change.
func TestProseHigherUpDoesNotCount(t *testing.T) {
	prose := map[string][]string{
		"claude": {"● The dialog says Esc to cancel", "  Esc to cancel · Tab to amend", " Esc to cancel", " Do you want to continue?", " ❯ 1. Yes"},
		"codex":  {"• Press enter to confirm or esc to cancel", "  Press enter to confirm or esc to cancel", "› 1. Yes, proceed (y)", "  Would you like to run it?"},
	}
	n := 0
	for _, l := range loadAll(t) {
		if !l.fix.Want.Composer {
			continue
		}
		rec := recogniserFor(t, l)
		base := rows(l.text)
		last := -1
		for i, r := range base {
			if strings.TrimSpace(r) != "" {
				last = i
			}
		}
		want := rec.Read(l.snap())
		// Rows above the bottom rows: more than 20 rows above the last one.
		for i := 0; i < last-bottomRows; i++ {
			for _, p := range prose[l.runtime] {
				mut := append([]string(nil), base...)
				mut[i] = p
				n++
				got := rec.Read(l.fix.snapshot(join(mut)))
				if !reflect.DeepEqual(got, want) {
					t.Errorf("%s/%s: %q on row %d changed the reading:\n got  %+v\n want %+v", l.runtime, l.name, p, i, got, want)
				}
			}
		}
	}
	if n == 0 {
		t.Fatal("no mutation ran")
	}
}

// TestClaudeProseInsideBottomRows: transcript prose that mentions a mark, in
// the bottom rows, still does not make a dialog for claude, because a mark
// counts only at the start of a row.
func TestClaudeProseInsideBottomRows(t *testing.T) {
	rec, _ := For("claude", "2.1.325")
	for _, l := range loadAll(t) {
		if l.runtime != "claude" || l.name != "idle-after-turn" {
			continue
		}
		base := rows(l.text)
		for _, p := range []string{
			"● Press Esc to cancel · or Tab",
			"  Esc to cancel",
			"  Do you want to know more?",
			"  ❯ 1. not a choice list",
			"❯ 1. an owner prompt that starts like a list",
			"✻ Brewed for 1s · done 1:34 PM",
		} {
			mut := append([]string(nil), base...)
			mut[12] = p
			got := rec.Read(l.fix.snapshot(join(mut)))
			if got.Dialog || !got.ComposerEmpty {
				t.Errorf("%q in the bottom rows changed the reading: %+v", p, got)
			}
		}
	}
}

// TestUnknownScreens: screens that must not be placed.
func TestUnknownScreens(t *testing.T) {
	var idle = map[string]loaded{}
	for _, l := range loadAll(t) {
		if l.name == "idle-after-turn" {
			idle[l.runtime] = l
		}
	}
	for _, rt := range []string{"claude", "codex"} {
		l, ok := idle[rt]
		if !ok {
			t.Fatalf("no idle-after-turn fixture for %s", rt)
		}
		rec := recogniserFor(t, l)
		f := l.fix
		half := l.text[:len(l.text)/2]
		for half != "" && !isRuneStart(half[len(half)-1]) {
			half = half[:len(half)-1]
		}
		rnd := make([]byte, 4096)
		x := uint32(2463534242)
		for i := range rnd {
			x ^= x << 13
			x ^= x >> 17
			x ^= x << 5
			rnd[i] = byte(x)
		}
		rule := strings.Repeat("─", 500)
		bigRule := strings.Repeat(rule+"\n", 200)
		tooTall := strings.Repeat("x\n", 201)
		tooWide := strings.Repeat("y", 501)
		cases := []struct {
			name string
			snap Snapshot
		}{
			{"cut in half", f.snapshot(half)},
			{"top half", f.snapshot(join(rows(l.text)[:18]))},
			{"empty", f.snapshot("")},
			{"blank rows", f.snapshot(strings.Repeat("\n", 35))},
			{"500x200 rules", Snapshot{Text: bigRule, Cols: 500, Rows: 200, AltScreen: true}},
			{"random bytes", f.snapshot(string(rnd))},
			{"over 200 rows", Snapshot{Text: tooTall, Cols: 120, Rows: 201, AltScreen: true}},
			{"over 500 columns", Snapshot{Text: tooWide, Cols: 501, Rows: 36, AltScreen: true}},
			{"row wider than the screen", f.snapshot(l.text + tooWide)},
			{"more rows than the screen", Snapshot{Text: l.text + l.text, Cols: f.Cols, Rows: f.Rows, AltScreen: true}},
			{"zero size", Snapshot{Text: l.text}},
			{"escape in the text", f.snapshot(strings.Replace(l.text, "\n", "\n\x1b[2J", 3))},
			{"invalid UTF-8", f.snapshot(l.text + "\xff\xfe")},
			{"not on the alternate screen", func() Snapshot { s := f.snapshot(l.text); s.AltScreen = false; return s }()},
		}
		for _, c := range cases {
			got := rec.Read(c.snap)
			if got.Known {
				t.Errorf("%s: %s: read as known: %+v", rt, c.name, got)
			}
			if len(got.Notes) == 0 {
				t.Errorf("%s: %s: unknown without a note", rt, c.name)
			}
			z := got
			z.Notes = nil
			if !reflect.DeepEqual(z, Reading{}) {
				t.Errorf("%s: %s: unknown with other fields set: %+v", rt, c.name, got)
			}
		}
	}
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

// TestEveryPrefixIsSafe cuts each fixture at every row boundary: no cut that
// removes the footer or the status rows may still show an empty composer at
// the cut position, and nothing panics.
func TestEveryPrefixIsSafe(t *testing.T) {
	for _, l := range loadAll(t) {
		rec := recogniserFor(t, l)
		r := rows(l.text)
		for n := 0; n <= len(r); n++ {
			got := rec.Read(l.fix.snapshot(join(r[:n])))
			if got.ComposerEmpty && !got.Composer {
				t.Errorf("%s/%s: %d rows: empty composer without a composer", l.runtime, l.name, n)
			}
			if got.ComposerEmpty && got.Dialog {
				t.Errorf("%s/%s: %d rows: empty composer and a dialog", l.runtime, l.name, n)
			}
		}
	}
}

// TestFor: the version lookup.
func TestFor(t *testing.T) {
	for _, c := range []struct {
		rt, ver string
		ok      bool
	}{
		{"claude", "2.1.325", true},
		{"codex", "0.169", true},
		{"codex", "0.169.0", true},
		{"claude", "2.1.326", false},
		{"claude", "2.1", false},
		{"claude", "", false},
		{"codex", "0.170", false},
		{"codex", "0.169.1", false},
		{"codex", "0.16", false},
		{"gemini", "0.169", false},
		{"", "", false},
		{"Claude", "2.1.325", false},
		{"claude ", "2.1.325", false},
	} {
		rec, ok := For(c.rt, c.ver)
		if ok != c.ok || (rec == nil) == ok {
			t.Errorf("For(%q, %q) = %v, %v; want found=%v", c.rt, c.ver, rec, ok, c.ok)
		}
	}
}

// TestPastedMarkerAndHolds builds screens from fixtures: a composer holding
// each runtime's pasted-text marker, and the header check.
func TestPastedMarkerAndHolds(t *testing.T) {
	by := map[string]loaded{}
	for _, l := range loadAll(t) {
		if l.name == "idle-after-turn" {
			by[l.runtime] = l
		}
	}
	cases := []struct {
		rt, row string
		cursorX int
		row0    int
		marker  string
	}{
		{"claude", "❯ [ataniq-msg id=1 from=ataniq kind=test][Pasted text #1 +4 lines]", 60, 33, "[Pasted text #"},
		{"codex", "› [Pasted Content 1024 chars]", 30, 32, "[Pasted Content"},
	}
	for _, c := range cases {
		l := by[c.rt]
		r := rows(l.text)
		r[c.row0] = c.row
		s := l.fix.snapshot(join(r))
		s.Faint = nil
		s.CursorX, s.CursorY = c.cursorX, c.row0
		got := mustFor(t, c.rt).Read(s)
		if !got.Composer || got.ComposerEmpty || !got.PastedMarker || !strings.Contains(got.ComposerText, c.marker) {
			t.Errorf("%s: pasted marker not read: %+v", c.rt, got)
		}
		if !got.CursorInComposer {
			t.Errorf("%s: cursor not in the composer: %+v", c.rt, got)
		}
	}
	// A header that wraps over two rows is still held.
	for _, l := range loadAll(t) {
		if l.name != "typed-two-rows" {
			continue
		}
		got := mustFor(t, l.runtime).Read(l.snap())
		if !strings.Contains(got.ComposerText, "\n") {
			t.Fatalf("%s: fixture does not wrap: %q", l.runtime, got.ComposerText)
		}
		// The wrapped text, with its break and blanks, is found by its
		// squashed header.
		for _, h := range []string{
			"when a message starts with [ataniq-msg id=N], reply",
			"reply with exactly ACK N and nothing else, and use no tools",
			"approval to run it outside the sandbox. Use no other tool",
			"command in the current folder: touch approval-test.txt. The sandbox is read-only, so request approval",
		} {
			squashed := squash(got.ComposerText)
			if strings.Contains(squashed, squash(h)) && !got.Holds(h) {
				t.Errorf("%s: Holds(%q) false for text %q", l.runtime, h, got.ComposerText)
			}
		}
	}
}

func mustFor(t testing.TB, rt string) Recogniser {
	t.Helper()
	rec, ok := For(rt, map[string]string{"claude": "2.1.325", "codex": "0.169"}[rt])
	if !ok {
		t.Fatalf("no recogniser for %s", rt)
	}
	return rec
}

func TestHolds(t *testing.T) {
	r := Reading{Composer: true, ComposerText: "[ataniq-msg id=1 from=ata\nniq kind=test] reply with  exactly ACK 1"}
	for h, want := range map[string]bool{
		"[ataniq-msg id=1 from=ataniq kind=test]":  true,
		"[ataniq-msg id=1 from=ataniq kind=test] ": true,
		"[ataniq-msg\nid=1 from=ataniq kind=test]": true,
		"reply with exactly ACK 1":                 true,
		"":                                         false,
		"   \n":                                    false,
		"[ataniq-msg id=2":                         false,
		"ACK 11":                                   false,
	} {
		if got := r.Holds(h); got != want {
			t.Errorf("Holds(%q) = %v, want %v", h, got, want)
		}
	}
	if (Reading{Composer: true}).Holds("x") || (Reading{ComposerText: "x"}).Holds("x") {
		t.Error("Holds is true without composer text or composer")
	}
	if (Reading{}).Holds("") {
		t.Error("Holds is true for an empty header")
	}
}

// TestHintNeedsStyle: without style data, or with the cursor away from the
// start of the hint, hint text is owner text, never an empty composer.
func TestHintNeedsStyle(t *testing.T) {
	n := 0
	for _, l := range loadAll(t) {
		if !l.fix.Want.Placeholder {
			continue
		}
		rec := recogniserFor(t, l)
		s := l.snap()
		if got := rec.Read(s); !got.ComposerEmpty || !got.Placeholder || got.ComposerText != "" {
			t.Errorf("%s/%s: %+v", l.runtime, l.name, got)
		}
		noStyle := s
		noStyle.Faint = nil
		moved := s
		moved.CursorX += 3
		hidden := s
		hidden.CursorHidden = true
		for name, snap := range map[string]Snapshot{"no style": noStyle, "cursor moved": moved, "cursor hidden": hidden} {
			n++
			got := rec.Read(snap)
			if got.ComposerEmpty || got.Placeholder || got.ComposerText == "" {
				t.Errorf("%s/%s (%s): hint read as empty: %+v", l.runtime, l.name, name, got)
			}
		}
		// A faint run that covers only part of the text is not a hint.
		partial := s
		partial.Faint = nil
		for _, sp := range s.Faint {
			if sp.Row == s.CursorY {
				sp.To--
			}
			partial.Faint = append(partial.Faint, sp)
		}
		if got := rec.Read(partial); got.ComposerEmpty {
			t.Errorf("%s/%s: partly faint text read as empty: %+v", l.runtime, l.name, got)
		}
	}
	if n == 0 {
		t.Fatal("no fixture with a hint")
	}
}

// TestHostileSpans: any Faint value is safe.
func TestHostileSpans(t *testing.T) {
	for _, l := range loadAll(t) {
		rec := recogniserFor(t, l)
		s := l.snap()
		s.Faint = nil
		for i := 0; i < 10000; i++ {
			s.Faint = append(s.Faint, Span{Row: -i, From: i - 5000, To: 1 << 40})
		}
		rec.Read(s)
	}
}

// TestSizesAtTheCaps: a screen of exactly the caps is read, one over is not.
func TestSizesAtTheCaps(t *testing.T) {
	rec := mustFor(t, "claude")
	rule := strings.Repeat("─", 500)
	var b []string
	for i := 0; i < 196; i++ {
		b = append(b, "")
	}
	b = append(b, rule, "❯ ", rule, "  ⏸ manual mode on · ? for shortcuts")
	got := rec.Read(Snapshot{Text: strings.Join(b, "\n"), Cols: 500, Rows: 200, CursorX: 2, CursorY: 197, AltScreen: true})
	if !got.Known || !got.ComposerEmpty || !got.CursorInComposer || got.Mode != "manual" {
		t.Errorf("screen at the caps: %+v", got)
	}
	if rec.Read(Snapshot{Text: "x", Cols: 501, Rows: 200, AltScreen: true}).Known || rec.Read(Snapshot{Text: "x", Cols: 500, Rows: 201, AltScreen: true}).Known {
		t.Error("a size over the caps was read")
	}
}

// FuzzRead: any text and any cursor make no panic, and the invariants of
// Reading hold.
func FuzzRead(f *testing.F) {
	for _, l := range loadAll(f) {
		f.Add(byte(len(l.runtime)), l.text, l.fix.Cols, l.fix.Rows, l.fix.CursorX, l.fix.CursorY, l.fix.CursorHidden, l.fix.AltScreen)
	}
	f.Add(byte(0), "", 0, 0, 0, 0, false, false)
	f.Add(byte(1), strings.Repeat("─", 40)+"\n❯\n"+strings.Repeat("─", 40)+"\n  ⏸ manual mode on", 40, 5, 0, 1, false, true)
	f.Fuzz(func(t *testing.T, which byte, text string, cols, rows, cx, cy int, hidden, alt bool) {
		rts := [][2]string{{"claude", "2.1.325"}, {"codex", "0.169"}}
		rt := rts[int(which)%2]
		rec, ok := For(rt[0], rt[1])
		if !ok {
			t.Fatal("no recogniser")
		}
		s := Snapshot{Text: text, Cols: cols, Rows: rows, CursorX: cx, CursorY: cy, CursorHidden: hidden, AltScreen: alt}
		s.Faint = []Span{{Row: cy, From: cx, To: cx + 500}, {Row: -1, From: -1, To: 1 << 62}}
		got := rec.Read(s)
		if got.ComposerEmpty && (!got.Composer || got.Dialog) {
			t.Fatalf("ComposerEmpty without a composer, or with a dialog: %+v", got)
		}
		if got.Dialog && (got.Composer || len(got.DialogMarks) == 0) {
			t.Fatalf("a dialog with a composer or without marks: %+v", got)
		}
		if got.ComposerEmpty && got.ComposerText != "" {
			t.Fatalf("empty composer with text: %+v", got)
		}
		if !got.Known {
			z := got
			z.Notes = nil
			if !reflect.DeepEqual(z, Reading{}) {
				t.Fatalf("unknown with fields set: %+v", got)
			}
			if len(got.Notes) == 0 {
				t.Fatalf("unknown without a note: %+v", got)
			}
		}
		if got.CursorInComposer && !got.Composer {
			t.Fatalf("cursor in a composer that is not there: %+v", got)
		}
		got.Holds(text)
	})
}
