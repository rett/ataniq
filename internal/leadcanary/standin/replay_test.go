//go:build linux && ataniq_canary

package standin

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"

	"ataniq/internal/inject/osc7501"
	"ataniq/internal/terminalconsole/sessionpane"
)

const (
	sampleReport = "state=idle:app=sample-app"
	da1Reply     = "\x1b[?62;4c"
	// bothQueries is what replay mode writes first, before any screen.
	bothQueries = "\x1b]7501;?\x1b\\\x1b[c"
)

// replayDir is the replay folder of a home.
func replayDir(r testRepo) string { return filepath.Join(r.Home, ReplayDir) }

// copyFixture copies a recorded screen of the injection library to a file of
// the replay folder: <name>.json and <name>.txt.
func copyFixture(t testing.TB, from, dir, to string) {
	t.Helper()
	for _, ext := range []string{".json", ".txt"} {
		b, err := os.ReadFile(fixture(t, from) + ext)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, to+ext), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// writeReplay makes the replay folder of the repo's home: the idle screen and
// the collapsed-paste screen of the injection library's fixtures, the report
// and, when on is true, the switch file.
func writeReplay(t testing.TB, r testRepo, on bool) {
	t.Helper()
	dir := replayDir(r)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	copyFixture(t, "idle-before-paste", dir, "1")
	copyFixture(t, "collapsed-paste-footer-a", dir, "2")
	if err := os.WriteFile(filepath.Join(dir, ReportFile), []byte(sampleReport+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if on {
		if err := os.WriteFile(filepath.Join(dir, SwitchFile), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func mustLoad(t testing.TB, path string) *snapshot {
	t.Helper()
	s, err := loadSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// orderWritten reports whether order.txt holds its line: the file exists
// before its content is written, so its existence alone does not say it.
func orderWritten(r testRepo) bool {
	b, err := os.ReadFile(filepath.Join(replayDir(r), OrderFile))
	return err == nil && len(b) > 0
}

func readOrder(t testing.TB, r testRepo) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(replayDir(r), OrderFile))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The full path: queries, the first screen, the idle report once the query is
// answered, the second screen after a paste, and the commit after a CR.
func TestReplayEndToEnd(t *testing.T) {
	r := newRepo(t)
	writeReplay(t, r, true)
	first := r.head(t)
	one := mustLoad(t, filepath.Join(replayDir(r), FirstScreen))
	two := mustLoad(t, filepath.Join(replayDir(r), SecondScreen))
	run := startRun(t, r, 0)

	// The queries come first, and then exactly the first screen.
	off := run.pty.waitFor(t, 0, bothQueries)
	want1 := one.draw(false)
	off = run.pty.waitFor(t, off-len(bothQueries), bothQueries+string(want1))
	if !bytes.HasPrefix(run.pty.output(), []byte(bothQueries+string(want1))) {
		t.Fatalf("the output does not start with the queries and the first screen: %q", run.pty.output())
	}
	if bytes.Contains(run.pty.output(), []byte("\x1b]7501;state=")) {
		t.Fatal("a report was written before the query was answered")
	}
	// The terminal answers as a holder does: the 7501 reply, then DA1.
	run.pty.send(t, osc7501.QueryReply+da1Reply)
	report := "\x1b]7501;" + sampleReport + "\x1b\\"
	off = run.pty.waitFor(t, off, report)
	waitUntil(t, "order.txt", func() bool { return orderWritten(r) })
	if got := readOrder(t, r); got != "7501-first\n" {
		t.Fatalf("order.txt %q", got)
	}
	// The first screen, as a terminal shows it.
	pane := sessionpane.New(one.Cols, one.Rows, sessionpane.Options{})
	defer pane.Close()
	pane.Write(run.pty.output())
	checkScreen(t, pane, one)

	// A paste draws the second screen. A CR then submits it.
	run.pty.send(t, pasteOf(message("")))
	want2 := two.draw(one.AltScreen)
	off = run.pty.waitFor(t, off, string(want2))
	if r.head(t) != first {
		t.Fatal("the paste alone committed")
	}
	pane.Write(run.pty.output()[len(bothQueries)+len(want1):])
	checkScreen(t, pane, two)

	run.pty.send(t, "\r")
	waitUntil(t, "the commit", func() bool { return r.committed(t) })
	if b, _ := os.ReadFile(filepath.Join(r.Dir, "canary-token.txt")); string(b) != testToken+"\n" {
		t.Fatalf("file %q", b)
	}
	if got := runGit(t, r.Dir, "log", "-1", "--format=%s"); got != "canary: token" {
		t.Fatalf("subject %q", got)
	}
	// Replay mode holds no screen of its own: nothing but the files' screens,
	// the queries and the one report was written.
	all := run.pty.output()
	rest := bytes.Replace(all, []byte(bothQueries), nil, 1)
	rest = bytes.Replace(rest, want1, nil, 1)
	rest = bytes.Replace(rest, []byte(report), nil, 1)
	rest = bytes.Replace(rest, want2, nil, 1)
	if len(rest) != 0 {
		t.Fatalf("replay mode wrote more than its screens: %q", rest)
	}
	if err := run.finish(t); err != nil {
		t.Fatal(err)
	}
}

func TestReplayTypedKeysAndAPasteWithoutATokenCommitNothing(t *testing.T) {
	r := newRepo(t)
	writeReplay(t, r, true)
	first := r.head(t)
	run := startRun(t, r, 0)
	run.pty.waitFor(t, 0, bothQueries)
	run.pty.send(t, "typed\r"+pasteOf("no token here")+"\r"+pasteOf(message(""))+"x")
	run.pty.send(t, "ZZ")
	time.Sleep(200 * time.Millisecond)
	if err := run.finish(t); err != nil {
		t.Fatal(err)
	}
	if r.head(t) != first || r.branch(t) != "develop" {
		t.Fatal("the repository changed")
	}
	if bytes.Contains(run.pty.output(), []byte("typed")) {
		t.Fatal("replay mode echoed typed keys")
	}
}

func TestReplayRunsNoTextFromTheMessage(t *testing.T) {
	r := newRepo(t)
	writeReplay(t, r, true)
	pwned := filepath.Join(t.TempDir(), "pwned")
	run := startRun(t, r, 0)
	run.pty.waitFor(t, 0, bothQueries)
	run.pty.send(t, pasteOf(message("$(touch "+pwned+")\r`touch "+pwned+"`\rcanary-token: "+testToken+"; touch "+pwned)))
	run.pty.send(t, "\r")
	waitUntil(t, "the commit", func() bool { return r.committed(t) })
	if err := run.finish(t); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(pwned); err == nil {
		t.Fatal("text of the message ran")
	}
}

// What order.txt says is the order the replies reached the stand-in, whatever
// the order of its own writes.
func TestReplayRecordsTheOrderOfTheReplies(t *testing.T) {
	for _, c := range []struct {
		name  string
		steps []string // sent one after another
		want  string
		// report says whether the idle report is expected.
		report bool
	}{
		{"7501 before DA1, one write", []string{osc7501.QueryReply + da1Reply}, Order7501First, true},
		{"7501 before DA1, two writes", []string{osc7501.QueryReply, da1Reply}, Order7501First, true},
		{"7501 only", []string{osc7501.QueryReply}, Order7501First, true},
		{"DA1 before 7501, one write", []string{da1Reply + osc7501.QueryReply}, OrderDA1First, true},
		{"DA1 before 7501, two writes", []string{da1Reply, osc7501.QueryReply}, OrderDA1First, true},
		{"DA1 and no 7501 reply", []string{da1Reply}, OrderNo7501Reply, false},
		{"no reply at all", nil, OrderNo7501Reply, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newRepo(t)
			writeReplay(t, r, true)
			run := startRun(t, r, 300*time.Millisecond)
			run.pty.waitFor(t, 0, bothQueries)
			for _, s := range c.steps {
				run.pty.send(t, s)
				time.Sleep(50 * time.Millisecond)
			}
			waitUntil(t, "order.txt", func() bool { return orderWritten(r) })
			if got := readOrder(t, r); got != c.want+"\n" {
				t.Fatalf("order.txt %q, want %q", got, c.want)
			}
			report := "\x1b]7501;" + sampleReport + "\x1b\\"
			if c.report {
				run.pty.waitFor(t, 0, report)
			} else {
				time.Sleep(100 * time.Millisecond)
				if bytes.Contains(run.pty.output(), []byte(report)) {
					t.Fatal("an idle report was written without an answer to the query")
				}
			}
			if err := run.finish(t); err != nil {
				t.Fatal(err)
			}
			if n := bytes.Count(run.pty.output(), []byte(report)); c.report && n != 1 {
				t.Fatalf("%d idle reports, want one", n)
			}
		})
	}
}

// A 7501 reply that comes after the order was recorded as "no reply" is still
// answered with the report, and the file is rewritten with the order the
// replies came in.
func TestReplayLateReplyGetsTheReportAndRewritesTheOrder(t *testing.T) {
	r := newRepo(t)
	writeReplay(t, r, true)
	run := startRun(t, r, 100*time.Millisecond)
	run.pty.waitFor(t, 0, bothQueries)
	waitUntil(t, "order.txt", func() bool { return orderWritten(r) })
	if got := readOrder(t, r); got != "no-7501-reply\n" {
		t.Fatalf("order.txt %q before the reply", got)
	}
	run.pty.send(t, osc7501.QueryReply)
	run.pty.waitFor(t, 0, "\x1b]7501;"+sampleReport+"\x1b\\")
	if got := readOrder(t, r); got != "7501-first\n" {
		t.Fatalf("order.txt %q", got)
	}
	if err := run.finish(t); err != nil {
		t.Fatal(err)
	}
}

// Without report.txt the report has no app, so no runtime name is in the code.
func TestReplayReportWithoutAFileNamesNoApp(t *testing.T) {
	r := newRepo(t)
	writeReplay(t, r, true)
	if err := os.Remove(filepath.Join(replayDir(r), ReportFile)); err != nil {
		t.Fatal(err)
	}
	run := startRun(t, r, 0)
	run.pty.waitFor(t, 0, bothQueries)
	run.pty.send(t, osc7501.QueryReply)
	run.pty.waitFor(t, 0, "\x1b]7501;state=idle\x1b\\")
	if err := run.finish(t); err != nil {
		t.Fatal(err)
	}
}

// The mode is chosen from the switch file alone.
func TestModeIsChosenByTheSwitchFile(t *testing.T) {
	r := newRepo(t)
	writeReplay(t, r, false)
	if ReplayMode(r.Home) {
		t.Fatal("replay mode without the switch file")
	}
	run := startRun(t, r, 0)
	run.pty.waitFor(t, 0, "> ")
	if bytes.Contains(run.pty.output(), []byte("7501")) || bytes.Contains(run.pty.output(), []byte("\x1b[c")) {
		t.Fatalf("plain mode sent a query: %q", run.pty.output())
	}
	if err := run.finish(t); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(replayDir(r), OrderFile)); err == nil {
		t.Fatal("plain mode wrote order.txt")
	}

	// The switch file written later gives replay mode at the next start.
	if err := os.WriteFile(filepath.Join(replayDir(r), SwitchFile), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if !ReplayMode(r.Home) {
		t.Fatal("no replay mode with the switch file")
	}
	run = startRun(t, r, 0)
	run.pty.waitFor(t, 0, bothQueries)
	if bytes.Contains(run.pty.output(), []byte("> ")) {
		t.Fatalf("replay mode drew the plain prompt: %q", run.pty.output())
	}
	if err := run.finish(t); err != nil {
		t.Fatal(err)
	}
}

func TestReplayModeNeedsARegularSwitchFile(t *testing.T) {
	r := newRepo(t)
	writeReplay(t, r, false)
	sw := filepath.Join(replayDir(r), SwitchFile)
	target := filepath.Join(t.TempDir(), "on")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, sw); err != nil {
		t.Fatal(err)
	}
	if ReplayMode(r.Home) {
		t.Fatal("a link is a switch")
	}
	os.Remove(sw)
	if err := os.Mkdir(sw, 0o700); err != nil {
		t.Fatal(err)
	}
	if ReplayMode(r.Home) {
		t.Fatal("a folder is a switch")
	}
	if ReplayMode("") {
		t.Fatal("no home is replay mode")
	}
}

// A replay folder that cannot be read is an error before anything is drawn.
func TestReplayWithBadFilesFailsBeforeDrawing(t *testing.T) {
	for _, c := range []struct {
		name   string
		damage func(t *testing.T, dir string)
	}{
		{"no first screen", func(t *testing.T, dir string) { os.Remove(filepath.Join(dir, "1.json")) }},
		{"no second screen", func(t *testing.T, dir string) { os.Remove(filepath.Join(dir, "2.json")) }},
		{"no text file", func(t *testing.T, dir string) { os.Remove(filepath.Join(dir, "1.txt")) }},
		{"JSON that is not JSON", func(t *testing.T, dir string) {
			os.WriteFile(filepath.Join(dir, "2.json"), []byte("{"), 0o600)
		}},
		{"a report that is not idle", func(t *testing.T, dir string) {
			os.WriteFile(filepath.Join(dir, ReportFile), []byte("state=working:app=x"), 0o600)
		}},
		{"a report that is not a report", func(t *testing.T, dir string) {
			os.WriteFile(filepath.Join(dir, ReportFile), []byte("hello"), 0o600)
		}},
		{"a report with an escape in it", func(t *testing.T, dir string) {
			os.WriteFile(filepath.Join(dir, ReportFile), []byte("state=idle:x=\x1b\\\x1b]0;t"), 0o600)
		}},
		{"a report with an unknown key that holds an escape", func(t *testing.T, dir string) {
			os.WriteFile(filepath.Join(dir, ReportFile), []byte("state=idle:x=\x07"), 0o600)
		}},
		{"a link for a screen", func(t *testing.T, dir string) {
			os.Remove(filepath.Join(dir, "1.json"))
			os.Symlink(filepath.Join(dir, "2.json"), filepath.Join(dir, "1.json"))
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newRepo(t)
			writeReplay(t, r, true)
			c.damage(t, replayDir(r))
			p := openPTY(t)
			var out bytes.Buffer
			err := Run(t.Context(), Config{Dir: r.Dir, Home: r.Home, In: p.slave, Out: &out, TermFD: int(p.slave.Fd())})
			if err == nil {
				t.Fatal("Run accepted a damaged replay folder")
			}
			if out.Len() != 0 || len(p.output()) != 0 {
				t.Fatalf("something was drawn: %q %q", out.Bytes(), p.output())
			}
		})
	}
}

func TestLoadSnapshotChecks(t *testing.T) {
	good := map[string]any{
		"cols": 40, "rows": 10, "cursor_x": 1, "cursor_y": 1,
		"text":  "one\ntwo\n",
		"faint": []map[string]int{{"row": 0, "from": 0, "to": 3}},
	}
	with := func(k string, v any) map[string]any {
		m := map[string]any{}
		for a, b := range good {
			m[a] = b
		}
		if v == nil {
			delete(m, k)
		} else {
			m[k] = v
		}
		return m
	}
	for _, c := range []struct {
		name string
		snap map[string]any
		ok   bool
	}{
		{"good", good, true},
		{"unknown fields are ignored", with("source", "x"), true},
		{"too few columns", with("cols", 10), false},
		{"too many columns", with("cols", 501), false},
		{"too few rows", with("rows", 4), false},
		{"too many rows", with("rows", 201), false},
		{"a cursor outside", with("cursor_x", 40), false},
		{"a negative cursor", with("cursor_y", -1), false},
		{"more rows of text than the screen", with("text", strings.Repeat("x\n", 11)), false},
		{"a row wider than the screen", with("text", strings.Repeat("x", 41)), false},
		{"an escape in the text", with("text", "a\x1b[2Jb"), false},
		{"a span past the end of its text", with("faint", []map[string]int{{"row": 0, "from": 0, "to": 4}}), true},
		{"a span on a row after the text", with("faint", []map[string]int{{"row": 2, "from": 0, "to": 1}}), true},
		{"a span past the columns", with("faint", []map[string]int{{"row": 0, "from": 0, "to": 41}}), false},
		{"a span below the screen", with("faint", []map[string]int{{"row": 10, "from": 0, "to": 1}}), false},
		{"a span backwards", with("faint", []map[string]int{{"row": 0, "from": 2, "to": 1}}), false},
		{"a negative span", with("faint", []map[string]int{{"row": 0, "from": -1, "to": 1}}), false},
		{"no faint spans", with("faint", nil), true},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			b, err := json.Marshal(c.snap)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "1.json")
			if err := os.WriteFile(path, b, 0o600); err != nil {
				t.Fatal(err)
			}
			_, err = loadSnapshot(path)
			if (err == nil) != c.ok {
				t.Fatalf("loadSnapshot: %v, want ok=%v", err, c.ok)
			}
		})
	}
}

// The text beside the JSON is read when the JSON has none, and a "text" field
// of the JSON wins.
func TestLoadSnapshotTextSources(t *testing.T) {
	dir := t.TempDir()
	meta := `{"cols": 40, "rows": 10, "cursor_x": 0, "cursor_y": 0, "title": "t"`
	os.WriteFile(filepath.Join(dir, "a.json"), []byte(meta+`}`), 0o600)
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("from the file\n"), 0o600)
	os.WriteFile(filepath.Join(dir, "b.json"), []byte(meta+`, "text": "from the json"}`), 0o600)
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("from the file\n"), 0o600)
	if s := mustLoad(t, filepath.Join(dir, "a.json")); string(s.lines[0]) != "from the file" {
		t.Fatalf("a: %q", string(s.lines[0]))
	}
	if s := mustLoad(t, filepath.Join(dir, "b.json")); string(s.lines[0]) != "from the json" {
		t.Fatalf("b: %q", string(s.lines[0]))
	}
}

// drawn feeds the bytes of s.draw to a pane of the screen's size.
func drawn(t testing.TB, s *snapshot) *sessionpane.Pane {
	t.Helper()
	p := sessionpane.New(s.Cols, s.Rows, sessionpane.Options{})
	t.Cleanup(p.Close)
	p.SetGraphemeWidths(true)
	p.Write(s.draw(false))
	return p
}

// checkScreen compares what the pane shows with the snapshot: every
// character in its cell, the faint attribute exactly where the spans say, the
// cursor, its visibility, the title and the bracketed paste mode.
func checkScreen(t testing.TB, p *sessionpane.Pane, s *snapshot) {
	t.Helper()
	cols, rows := p.Size()
	if cols != s.Cols || rows != s.Rows {
		t.Fatalf("pane is %dx%d, snapshot %dx%d", cols, rows, s.Cols, s.Rows)
	}
	buf := uv.NewBuffer(cols, rows)
	p.Draw(buf, buf.Bounds())
	faintAt := func(y, off int) bool {
		for _, sp := range s.Faint {
			if sp.Row == y && off >= sp.From && off < sp.To {
				return true
			}
		}
		return false
	}
	for y := 0; y < rows; y++ {
		var line []rune
		if y < len(s.lines) {
			line = s.lines[y]
		}
		off := 0
		for x := 0; x < cols; {
			c := buf.CellAt(x, y)
			if c.Width == 0 {
				x++
				continue
			}
			runes := []rune(c.Content)
			blank := c.Content == "" || c.Content == " "
			switch {
			case off >= len(line):
				if !blank {
					t.Errorf("row %d column %d: %q where the text has ended", y, x, c.Content)
				}
			case blank:
				if line[off] != ' ' {
					t.Errorf("row %d column %d: blank where the text has %q", y, x, line[off])
				}
				if faintAt(y, off) != (c.Style.Attrs&uv.AttrFaint != 0) {
					t.Errorf("row %d column %d (character %d, blank): faint is %v", y, x, off, c.Style.Attrs&uv.AttrFaint != 0)
				}
				off++
			default:
				if off+len(runes) > len(line) || string(line[off:off+len(runes)]) != c.Content {
					t.Errorf("row %d column %d: %q, the text has %q at character %d", y, x, c.Content, string(line[off:min(len(line), off+len(runes))]), off)
					off = len(line)
					break
				}
				if faintAt(y, off) != (c.Style.Attrs&uv.AttrFaint != 0) {
					t.Errorf("row %d column %d (character %d, %q): faint is %v", y, x, off, c.Content, c.Style.Attrs&uv.AttrFaint != 0)
				}
				off += len(runes)
			}
			x += max(c.Width, 1)
		}
		if off < len(line) {
			t.Errorf("row %d: %d characters of the text are not on the screen", y, len(line)-off)
		}
	}
	pos, visible := p.Cursor()
	if pos.X != s.CursorX || pos.Y != s.CursorY || visible == s.CursorHidden {
		t.Errorf("cursor at %d,%d visible=%v; snapshot %d,%d hidden=%v", pos.X, pos.Y, visible, s.CursorX, s.CursorY, s.CursorHidden)
	}
	if got, want := p.Title(), cleanTitle(s.Title); got != want {
		t.Errorf("title %q, want %q", got, want)
	}
	if !p.Modes().BracketedPaste {
		t.Error("bracketed paste is not on")
	}
}

// Every recorded screen of the injection library is drawn as it was recorded.
func TestDrawEveryRecordedScreen(t *testing.T) {
	root := fixtureRoot
	paths, err := filepath.Glob(filepath.Join(root, "*", "*.json"))
	if err != nil || len(paths) < 25 {
		t.Fatalf("%d fixtures under %s: %v", len(paths), root, err)
	}
	var faint, wide int
	for _, path := range paths {
		s := mustLoad(t, path)
		faint += len(s.Faint)
		for _, line := range s.lines {
			for _, r := range line {
				if r >= 0x1100 {
					wide++
					break
				}
			}
		}
		t.Run(filepath.Base(filepath.Dir(path))+"/"+strings.TrimSuffix(filepath.Base(path), ".json"), func(t *testing.T) {
			checkScreen(t, drawn(t, s), s)
		})
	}
	// The test means something only if the screens it draws have both.
	if faint == 0 || wide == 0 {
		t.Fatalf("the recorded screens hold %d faint spans and %d rows of wide characters", faint, wide)
	}
}

// Faint spans are counted in characters and the cursor in cells: after wide
// characters the two differ, and each lands where it belongs.
func TestDrawFaintSpansAndWideCharactersAtTheRightCells(t *testing.T) {
	s := &snapshot{Cols: 40, Rows: 8, CursorX: 9, CursorY: 2, Title: "wide"}
	if err := s.check("ab漢字cd\nあxいy\nplain\n"); err != nil {
		t.Fatal(err)
	}
	// "ab" plain, the two wide characters faint, "cd" plain; "x" faint on the
	// next row between two wide characters; the whole third row faint.
	s.Faint = []span{{0, 2, 4}, {1, 1, 2}, {2, 0, 5}}
	if err := s.check("ab漢字cd\nあxいy\nplain\n"); err != nil {
		t.Fatal(err)
	}
	pane := sessionpane.New(s.Cols, s.Rows, sessionpane.Options{})
	defer pane.Close()
	pane.Write(s.draw(false))
	buf := uv.NewBuffer(s.Cols, s.Rows)
	pane.Draw(buf, buf.Bounds())

	cell := func(x, y int, content string, width int, faint bool) {
		t.Helper()
		c := buf.CellAt(x, y)
		if c.Content != content || c.Width != width || (c.Style.Attrs&uv.AttrFaint != 0) != faint {
			t.Errorf("cell %d,%d: %q width %d faint %v; want %q width %d faint %v",
				x, y, c.Content, c.Width, c.Style.Attrs&uv.AttrFaint != 0, content, width, faint)
		}
	}
	cell(0, 0, "a", 1, false)
	cell(1, 0, "b", 1, false)
	cell(2, 0, "漢", 2, true)
	if c := buf.CellAt(3, 0); c.Width != 0 {
		t.Errorf("cell 3,0 is not the second half of a wide character: %+v", c)
	}
	cell(4, 0, "字", 2, true)
	cell(6, 0, "c", 1, false)
	cell(7, 0, "d", 1, false)
	cell(0, 1, "あ", 2, false)
	cell(2, 1, "x", 1, true)
	cell(3, 1, "い", 2, false)
	cell(5, 1, "y", 1, false)
	for x := 0; x < 5; x++ {
		cell(x, 2, "plain"[x:x+1], 1, true)
	}
	cell(5, 2, " ", 1, false)
	if pos, visible := pane.Cursor(); pos.X != 9 || pos.Y != 2 || !visible {
		t.Errorf("cursor %v visible=%v, want 9,2 visible", pos, visible)
	}
	if pane.Title() != "wide" {
		t.Errorf("title %q", pane.Title())
	}
	checkScreen(t, pane, s)
}

// The drawing bytes: the alternate screen is entered once, not again for a
// second screen that is also on it, and left for one that is not; the cursor
// is shown or hidden as the snapshot says; faint is SGR 2.
func TestDrawBytes(t *testing.T) {
	s := &snapshot{Cols: 40, Rows: 8, AltScreen: true, CursorHidden: true, Title: "t\x1b]x\x07"}
	if err := s.check("ab\ncd"); err != nil {
		t.Fatal(err)
	}
	s.Faint = []span{{1, 1, 2}}
	first := string(s.draw(false))
	if !strings.Contains(first, "\x1b[?1049h") || !strings.Contains(first, "\x1b[?2004h") {
		t.Fatalf("first draw %q", first)
	}
	if strings.Contains(first, "\x1b[?25h") || !strings.Contains(first, "\x1b[?25l") {
		t.Fatalf("a hidden cursor was shown: %q", first)
	}
	if !strings.Contains(first, "\x1b[2;1Hc\x1b[2md\x1b[0m") {
		t.Fatalf("the faint span is not SGR 2 on the right character: %q", first)
	}
	if !strings.Contains(first, "\x1b]2;t]x\x1b\\") {
		t.Fatalf("the title is not cleaned: %q", first)
	}
	if again := string(s.draw(true)); strings.Contains(again, "\x1b[?1049") {
		t.Fatalf("a screen already on the alternate screen enters it again: %q", again)
	}
	s.AltScreen, s.CursorHidden = false, false
	left := string(s.draw(true))
	if !strings.Contains(left, "\x1b[?1049l") || !strings.HasSuffix(left, "\x1b[1;1H\x1b[?25h") {
		t.Fatalf("leaving: %q", left)
	}
}
