package inject

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"ataniq/internal/inject/screen"
)

// The screens of the fake are laid out as the recorded fixtures of package
// screen are. This test reads the fixtures and the fake's screen with the
// real recogniser, and the fields that say what the screen shows must agree:
// the layout, the cursor row and the last rows of the screen. If a recogniser
// changes, the fake has to follow the fixtures and not the other way round.

type wantJSON struct {
	Known            bool     `json:"known"`
	Composer         bool     `json:"composer"`
	ComposerEmpty    bool     `json:"composer_empty"`
	CursorInComposer bool     `json:"cursor_in_composer"`
	Dialog           bool     `json:"dialog"`
	DialogMarks      []string `json:"dialog_marks"`
	BackgroundTask   bool     `json:"background_task"`
	Mode             string   `json:"mode"`
	PastedMarker     bool     `json:"pasted_marker"`
	StateHidden      bool     `json:"state_hidden"`
	Working          bool     `json:"working"`
	Placeholder      bool     `json:"placeholder"`
}

type fixtureJSON struct {
	Cols    int      `json:"cols"`
	Rows    int      `json:"rows"`
	CursorX int      `json:"cursor_x"`
	CursorY int      `json:"cursor_y"`
	Want    wantJSON `json:"want"`
}

func loadFixture(t *testing.T, dir, name string) (fixtureJSON, []string) {
	t.Helper()
	base := filepath.Join("screen", "testdata", dir, name)
	raw, err := os.ReadFile(base + ".json")
	if err != nil {
		t.Fatal(err)
	}
	var fx fixtureJSON
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	txt, err := os.ReadFile(base + ".txt")
	if err != nil {
		t.Fatal(err)
	}
	return fx, strings.Split(strings.TrimSuffix(string(txt), "\n"), "\n")
}

func wantOf(r screen.Reading) wantJSON {
	return wantJSON{
		Known: r.Known, Composer: r.Composer, ComposerEmpty: r.ComposerEmpty, CursorInComposer: r.CursorInComposer,
		Dialog: r.Dialog, DialogMarks: r.DialogMarks, BackgroundTask: r.BackgroundTask, Mode: r.Mode,
		PastedMarker: r.PastedMarker, StateHidden: r.StateHidden, Working: r.Working, Placeholder: r.Placeholder,
	}
}

func TestFakeScreensMatchFixtures(t *testing.T) {
	cases := []struct {
		name    string
		rt      string
		dir     string
		fixture string
		setup   func(*fake)
		// cursor says whether the cursor position must equal the fixture's.
		cursor bool
		// tail is how many last rows of the screen must equal the fixture's.
		tail int
	}{
		{"claude idle", "claude", "claude-2.1.325", "idle-hint-manual", func(f *fake) {}, true, 4},
		{"claude working", "claude", "claude-2.1.325", "working-spinner", func(f *fake) { f.setMode(mWorking) }, true, 4},
		{"claude background shell", "claude", "claude-2.1.325", "background-shell-a", func(f *fake) { f.setMode(mBackground) }, false, 0},
		{"claude dialog", "claude", "claude-2.1.325", "dialog-write-open", func(f *fake) { f.setMode(mDialog) }, false, 0},
		{"claude collapsed paste", "claude", "claude-2.1.325", "collapsed-paste-footer-a", func(f *fake) { f.withCollapsedPaste() }, false, 0},
		{"claude pasted header", "claude", "claude-2.1.325", "pasted-header-accepted", func(f *fake) {
			f.withDraft("[ataniq-msg id=1 from=ataniq kind=test] reply with exactly ACK 1", 0)
		}, true, 4},
		{"codex idle", "codex", "codex-0.169", "idle-before-paste", func(f *fake) {}, true, 4},
		{"codex working", "codex", "codex-0.169", "working-line", func(f *fake) { f.setMode(mWorking) }, true, 4},
		{"codex dialog", "codex", "codex-0.169", "dialog-command-open", func(f *fake) { f.setMode(mDialog) }, false, 6},
		{"codex pasted header", "codex", "codex-0.169", "pasted-header-accepted", func(f *fake) {
			f.withDraft("[ataniq-msg id=1 from=ataniq kind=test] reply with exactly ACK 1", 0)
		}, true, 4},
		{"codex collapsed paste", "codex", "codex-0.169", "tall-composer-a", func(f *fake) { f.withCollapsedPaste() }, false, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fx, fxRows := loadFixture(t, c.dir, c.fixture)
			f := newFake(t, c.rt)
			c.setup(f)
			snap := f.emu.snap
			if snap.Cols != fx.Cols || snap.Rows != fx.Rows {
				t.Errorf("size %dx%d, fixture %dx%d", snap.Cols, snap.Rows, fx.Cols, fx.Rows)
			}
			rec, _ := screen.For(c.rt, f.version)
			got := wantOf(rec.Read(snap))
			want := fx.Want
			// The fixtures that hold the owner's own text differ in what the
			// text says, not in what the screen shows.
			if !reflect.DeepEqual(got, want) {
				t.Errorf("fake screen reads %+v\nfixture %s reads %+v", got, c.fixture, want)
			}
			if c.cursor && (snap.CursorY != fx.CursorY || snap.CursorX != fx.CursorX && (len(f.draft) == 0 && f.marker == "")) {
				t.Errorf("cursor (%d,%d), fixture (%d,%d)", snap.CursorX, snap.CursorY, fx.CursorX, fx.CursorY)
			}
			if c.tail > 0 {
				mine := strings.Split(snap.Text, "\n")
				for i := 1; i <= c.tail; i++ {
					a, b := strings.TrimRight(mine[len(mine)-i], " "), strings.TrimRight(fxRows[len(fxRows)-i], " ")
					// The rows that carry the owner's text, or a hint of the
					// moment, are not compared.
					if a != b && !strings.Contains(c.name, "pasted header") && !strings.Contains(c.name, "working") {
						t.Errorf("row -%d is %q, fixture %q", i, a, b)
					}
				}
			}
		})
	}
}
