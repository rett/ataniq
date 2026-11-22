package pairview

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// The synthetic code of every test. It is never a real code.
const (
	synthCode   = "SYNTH-TEST-CODE-0123"
	testOrigin  = "https://app.example"
	secretError = "boom-secret-error-text"
)

var epoch = time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)

// testPID is the pending id of every test: 16 lowercase hex characters.
const testPID = "0123456789abcdef"

var sixWords = [6]string{"lemon", "river", "tiger", "ocean", "maple", "quartz"}

// fakeBackend is a Backend for tests.
type fakeBackend struct {
	mu       sync.Mutex
	calls    []string
	replaces []bool

	offer    Offer
	startErr error
	state    State
	statErr  error
	paired   Paired
	ids      []string // the ids confirm and reject were called with, as "confirm:id"
	confErr  error
	rejErr   error
}

func (f *fakeBackend) log(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, s)
}

func (f *fakeBackend) count(s string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c == s {
			n++
		}
	}
	return n
}

func (f *fakeBackend) PairStart(_ context.Context, replace bool) (Offer, error) {
	f.log("start")
	f.mu.Lock()
	f.replaces = append(f.replaces, replace)
	f.mu.Unlock()
	return f.offer, f.startErr
}

func (f *fakeBackend) PairStatus(context.Context) (State, error) {
	f.log("status")
	return f.state, f.statErr
}

func (f *fakeBackend) PairConfirm(_ context.Context, id string) (Paired, error) {
	f.log("confirm")
	f.mu.Lock()
	f.ids = append(f.ids, "confirm:"+id)
	f.mu.Unlock()
	return f.paired, f.confErr
}

func (f *fakeBackend) PairReject(_ context.Context, id string) error {
	f.log("reject")
	f.mu.Lock()
	f.ids = append(f.ids, "reject:"+id)
	f.mu.Unlock()
	return f.rejErr
}

// rig drives a Model with a fake clock: the commands that the model returns
// are run, and each answer goes back in. A timer that the model asks for is
// held until the test fires it.
type rig struct {
	t      *testing.T
	m      *Model
	b      *fakeBackend
	now    time.Time
	timers []timer
	last   Action
}

type timer struct {
	d  time.Duration
	fn func(time.Time) tea.Msg
}

func newRig(t *testing.T, b *fakeBackend, origin string, w, h int) *rig {
	t.Helper()
	r := &rig{t: t, b: b, now: epoch}
	b.offer = Offer{Code: synthCode, ExpiresAt: epoch.Add(10 * time.Minute)}
	r.m = New(Options{
		Backend: b, Origin: origin, Colors: darkColors(),
		Now: func() time.Time { return r.now },
		After: func(d time.Duration, fn func(time.Time) tea.Msg) tea.Cmd {
			return func() tea.Msg {
				r.timers = append(r.timers, timer{d, fn})
				return nil
			}
		},
	})
	r.m.SetSize(w, h)
	return r
}

func darkColors() Colors {
	return Colors{Fg: color.RGBA{0xe6, 0xe6, 0xe6, 0xff}, Bg: color.RGBA{0x0e, 0x0f, 0x12, 0xff},
		Dim: color.RGBA{0x8a, 0x90, 0x9a, 0xff}, Accent: color.RGBA{0x4f, 0xc3, 0xf7, 0xff}, Warn: color.RGBA{0xf0, 0xb4, 0x29, 0xff}}
}

func lightColors() Colors {
	return Colors{Fg: color.RGBA{0x1a, 0x1a, 0x1a, 0xff}, Bg: color.RGBA{0xfa, 0xfa, 0xfa, 0xff},
		Dim: color.RGBA{0x5c, 0x63, 0x70, 0xff}, Accent: color.RGBA{0x0b, 0x5c, 0xad, 0xff}, Warn: color.RGBA{0x9a, 0x5b, 0x00, 0xff}}
}

func (r *rig) pump(cmd tea.Cmd) {
	r.t.Helper()
	for cmd != nil {
		msg := cmd()
		if msg == nil {
			return
		}
		c, act := r.m.Update(msg)
		r.last = act
		cmd = c
	}
}

func (r *rig) init() { r.t.Helper(); r.pump(r.m.Init()) }

func (r *rig) send(msg tea.Msg) {
	r.t.Helper()
	c, act := r.m.Update(msg)
	r.last = act
	r.pump(c)
}

func (r *rig) press(s string) {
	r.t.Helper()
	switch s {
	case "enter":
		r.send(tea.KeyPressMsg{Code: tea.KeyEnter})
	case "esc":
		r.send(tea.KeyPressMsg{Code: tea.KeyEscape})
	case "ctrl+c":
		r.send(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	default:
		r.send(tea.KeyPressMsg{Code: rune(s[0]), Text: s})
	}
}

// fire advances the clock by the oldest timer's wait and delivers it.
func (r *rig) fire() {
	r.t.Helper()
	if len(r.timers) == 0 {
		r.t.Fatal("no timer is waiting")
	}
	tm := r.timers[0]
	r.timers = r.timers[1:]
	r.now = r.now.Add(tm.d)
	r.send(tm.fn(r.now))
}

// start opens the pairing from the idle screen and takes the offer.
func (r *rig) start() {
	r.t.Helper()
	r.init()
	r.press("enter")
	if r.m.screen != scWaiting {
		r.t.Fatalf("screen %v after start, want waiting", r.m.screen)
	}
}

// pending moves the daemon to the pending phase and polls once.
func (r *rig) pending() {
	r.t.Helper()
	r.b.state = State{Phase: Pending, PendingID: testPID, Words: sixWords, ExpiresAt: r.now.Add(5 * time.Minute)}
	r.fire()
	if r.m.screen != scPending {
		r.t.Fatalf("screen %v after the poll, want pending", r.m.screen)
	}
}

func cellRows(buf *uv.Buffer, w, h int, mask bool) []string {
	rows := make([]string, h)
	for y := range rows {
		var b strings.Builder
		for x := 0; x < w; x++ {
			c := buf.CellAt(x, y)
			switch {
			case c == nil || c.Content == "":
				b.WriteByte(' ')
			case mask && c.Style.Bg == qrLight && c.Style.Fg == qrDark:
				b.WriteString("▒")
			default:
				b.WriteString(c.Content)
			}
		}
		rows[y] = strings.TrimRight(b.String(), " ")
	}
	return rows
}

// draw returns the model's cells at w by h.
func draw(m *Model, w, h int) *uv.Buffer {
	m.SetSize(w, h)
	buf := uv.NewBuffer(w, h)
	m.Draw(buf, buf.Bounds())
	return buf
}

// text returns the drawn rows as text with the QR modules masked, so that
// nothing of the code is in it.
func text(m *Model, w, h int) string {
	return strings.Join(cellRows(draw(m, w, h), w, h, true), "\n") + "\n"
}

func (r *rig) see(w, h int, wants ...string) {
	r.t.Helper()
	s := text(r.m, w, h)
	for _, x := range wants {
		if !strings.Contains(s, x) {
			r.t.Fatalf("%q is not on the screen:\n%s", x, s)
		}
	}
}

func (r *rig) notSee(w, h int, nots ...string) {
	r.t.Helper()
	s := text(r.m, w, h)
	for _, x := range nots {
		if strings.Contains(s, x) {
			r.t.Fatalf("%q is on the screen:\n%s", x, s)
		}
	}
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to write it)", err)
	}
	if string(want) != got {
		t.Errorf("%s differs from its golden:\n--- got\n%s--- want\n%s", name, got, string(want))
	}
}

var errBackend = errors.New(secretError)

func (r *rig) callList() string { return fmt.Sprint(r.b.calls) }
