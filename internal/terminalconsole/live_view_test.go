package terminalconsole

import (
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
)

const reselectReason = "the lead's epoch changed: select the lead again"

// stopForReselect puts the fake in the state leadattach gives it when a
// Welcome's lead or epoch differs from the pin, and lets the model see it.
func stopForReselect(r *liveRig) {
	r.t.Helper()
	r.f.set(func(s *LiveStatus) { s.Conn, s.Reason = LiveStoppedReselect, reselectReason })
	r.f.notify(LiveNotice{Code: "epoch-changed", Text: reselectReason})
	r.pump()
}

func TestCF03ReselectView(t *testing.T) {
	// The header at four sizes: two below the cockpit, which show it on the
	// first two rows above the session, and two at the cockpit's size, which
	// show it on the header row. In each the detach hint stays on row 1, and
	// the old lead and epoch are shown.
	header := func(t *testing.T, w, h int, role LiveRole) {
		t.Helper()
		f := newFake(role)
		r := newLiveRig(t, w, h, f, Options{})
		f.emit("last words")
		stopForReselect(r)
		rows := r.rows()
		if len(rows) != h {
			t.Fatalf("%d rows, want %d", len(rows), h)
		}
		if !strings.HasPrefix(rows[0], "q detach") {
			t.Fatalf("row 1 does not start with the detach hint: %q", rows[0])
		}
		if !strings.Contains(rows[0], "EPOCH CHANGED · select the lead again") {
			t.Fatalf("no reselect title on row 1: %q", rows[0])
		}
		top := strings.Join(rows[:2], "\n")
		for _, want := range []string{"scratch-lead", "epoch 7"} {
			if !strings.Contains(top, want) {
				t.Fatalf("%q missing from the header rows:\n%s", want, top)
			}
		}
		if strings.Contains(rows[0], "attached") {
			t.Fatalf("a stopped client shows as attached: %q", rows[0])
		}
		v := r.view()
		if !strings.Contains(v, "last words") {
			t.Fatalf("last screen missing:\n%s", v)
		}
		if w >= MinCols && !strings.Contains(v, "STOPPED, RESELECT") {
			t.Fatalf("banner missing:\n%s", v)
		}
	}
	for _, tc := range []struct {
		name string
		w, h int
	}{
		{"Header80x24", 80, 24},
		{"Header100x30", 100, 30},
		{"Header60x20", 60, 20},
		{"Header120x40", 120, 40},
	} {
		t.Run(tc.name, func(t *testing.T) {
			header(t, tc.w, tc.h, LiveWriter)
			header(t, tc.w, tc.h, LiveViewer)
		})
	}

	t.Run("PaneFrozenNoFocus", func(t *testing.T) {
		// The writer's focus is in the session when the stop comes: no key
		// reaches the holder, the first key returns to the console, and
		// Enter does not give the pane the focus again.
		f := newFake(LiveWriter)
		r := newLiveRig(t, 120, 40, f, Options{})
		r.focus()
		r.typ("a")
		if got := f.sink.keys; len(got) != 1 || got[0] != "a" {
			t.Fatalf("keys before the stop: %v", got)
		}
		f.emit("frozen")
		stopForReselect(r)
		r.typ("b")
		if r.m.focus != consoleFocus {
			t.Fatal("a stopped session kept the focus")
		}
		r.typ("c")
		for r.m.pane != paneSession {
			r.key(tabKey)
		}
		r.key(enterKey)
		if r.m.focus != consoleFocus {
			t.Fatal("Enter gave a stopped session the focus")
		}
		if !strings.Contains(r.m.notice, "Select the lead again") {
			t.Fatalf("notice %q", r.m.notice)
		}
		r.m.onPaste("pasted")
		r.size(130, 45)
		r.settle()
		if got := f.sink.keys; len(got) != 1 || len(f.sink.pastes) != 0 {
			t.Fatalf("a stopped session was sent keys %v, pastes %v", got, f.sink.pastes)
		}
		if got := f.sizes(); len(got) != 0 {
			t.Fatalf("a stopped session was asked for a size: %v", got)
		}
		// The screen stays as it was, dimmed, with the cursor not drawn in
		// the session and no mouse or paste modes asked for it.
		if !strings.Contains(r.view(), "frozen") {
			t.Fatalf("last screen lost:\n%s", r.view())
		}
		if v := r.m.View(); v.Cursor != nil {
			t.Fatalf("cursor %+v on a stopped session", v.Cursor)
		}
		if r.m.liveWriter() {
			t.Fatal("a stopped session counts as the writer")
		}
		if modes := r.m.liveModes(); modes.Mouse != uv.MouseModeNone || modes.BracketedPaste {
			t.Fatalf("modes %+v for a stopped session", modes)
		}
		buf := r.drawnPane()
		l := r.m.layout()
		if c := buf.CellAt(l.inner.Min.X, l.inner.Min.Y); c == nil || c.Style.Attrs&uv.AttrFaint == 0 {
			t.Fatalf("the stopped pane is not dimmed: %+v", c)
		}
	})

	t.Run("ResyncShowsRoleStateReason", func(t *testing.T) {
		for _, tc := range []struct {
			conn   LiveConn
			reason string
		}{
			{LiveResyncing, "snapshot check failed"},
			{LiveWaitingTicket, "waiting for a renewed ticket"},
		} {
			for _, size := range [][2]int{{80, 24}, {60, 20}, {100, 30}, {120, 40}} {
				for _, role := range []LiveRole{LiveWriter, LiveViewer} {
					f := newFake(role)
					r := newLiveRig(t, size[0], size[1], f, Options{})
					f.set(func(s *LiveStatus) { s.Conn, s.Reason = tc.conn, tc.reason })
					r.pump()
					rows := r.rows()
					top := strings.Join(rows[:2], "\n")
					if size[0] >= MinCols {
						// One header row at the cockpit's size.
						top = rows[0]
					}
					wantRole := "writer"
					if role == LiveViewer {
						wantRole = "read-only viewer"
					}
					for _, want := range []string{wantRole, tc.conn.String(), tc.reason} {
						if !strings.Contains(top, want) {
							t.Fatalf("%v %dx%d %s: %q missing from the header:\n%s", tc.conn, size[0], size[1], role, want, top)
						}
					}
					if strings.Contains(top, "EPOCH CHANGED") {
						t.Fatalf("a resync shows as a reselect stop:\n%s", top)
					}
				}
			}
		}
		// A resync is not a stop: the header goes back when it ends.
		f := newFake(LiveWriter)
		r := newLiveRig(t, 100, 30, f, Options{})
		f.set(func(s *LiveStatus) { s.Conn, s.Reason = LiveResyncing, "reason" })
		r.pump()
		f.set(func(s *LiveStatus) { s.Conn, s.Reason = LiveAttached, "" })
		r.pump()
		if row := r.rows()[0]; !strings.Contains(row, "ATANIQ / LIVE SCRATCH · cockpit") || strings.Contains(row, "resyncing") {
			t.Fatalf("header after a resync: %q", row)
		}
	})
}
