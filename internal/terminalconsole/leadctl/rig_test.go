package leadctl

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
)

// call is one call of the fake backend.
type call struct {
	op        string
	role      string
	epoch     uint64
	writer    bool
	messageID string
	body      string
}

// fakeBackend is a Backend for tests. It fails the test when a method is
// called anywhere but inside a command that the rig runs.
type fakeBackend struct {
	t     *testing.T
	mu    sync.Mutex
	inCmd bool

	listing Listing
	listErr error
	// answer gives the receipt or error of a command, by operation. A nil
	// function answers Done.
	answer func(c call) (Receipt, error)

	calls []call
	lists int
}

func newBackend(t *testing.T) *fakeBackend { return &fakeBackend{t: t} }

func (f *fakeBackend) guard(what string) {
	if !f.inCmd {
		f.t.Errorf("%s was called outside a command", what)
	}
}

func (f *fakeBackend) List() (Listing, error) {
	f.guard("List")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lists++
	return f.listing, f.listErr
}

func (f *fakeBackend) do(c call) (Receipt, error) {
	f.guard(c.op)
	f.mu.Lock()
	f.calls = append(f.calls, c)
	ans := f.answer
	f.mu.Unlock()
	if ans != nil {
		return ans(c)
	}
	return Receipt{CommandID: "c" + c.op, Op: Op(c.op), Status: Done}, nil
}

func (f *fakeBackend) Define(role string) (Receipt, error) {
	return f.do(call{op: "define", role: role})
}
func (f *fakeBackend) Start(role string) (Receipt, error) {
	return f.do(call{op: "start", role: role})
}
func (f *fakeBackend) Stop(role string, epoch uint64) (Receipt, error) {
	return f.do(call{op: "stop", role: role, epoch: epoch})
}
func (f *fakeBackend) Ticket(role string, epoch uint64, writer bool) (Receipt, error) {
	return f.do(call{op: "ticket", role: role, epoch: epoch, writer: writer})
}
func (f *fakeBackend) Send(role string, epoch uint64, id, body string) (Receipt, error) {
	return f.do(call{op: "inject", role: role, epoch: epoch, messageID: id, body: body})
}

func (f *fakeBackend) callsOf(op string) []call {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []call
	for _, c := range f.calls {
		if c.op == op {
			out = append(out, c)
		}
	}
	return out
}

func (f *fakeBackend) total() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// aLead is a lead of the listing that can be started.
func aLead(key string) Lead {
	return Lead{Key: key, Role: "scratch-" + key + "/lead", Defined: true, Provision: "done", State: "stopped",
		Unit: "ataniq-lead-scratch-" + key + ".service", Inject: true, Actions: OfferAll()}
}

// aRunning is a lead that is ready in epoch 3, with its one message free.
func aRunning(key string) Lead {
	l := aLead(key)
	l.State, l.Epoch = "ready", 3
	return l
}

// rig drives a Model: it runs the commands the model returns, synchronously,
// and feeds their answers back.
type rig struct {
	t    *testing.T
	m    *Model
	b    *fakeBackend
	w, h int
	// ticks counts the timers the model asked for; the rig does not fire
	// them: tick does.
	pending []tea.Cmd
	// actions collects every Action that was not None.
	actions []Action
	last    Action
}

func newRig(t *testing.T, b *fakeBackend, w, h int) *rig {
	r := &rig{t: t, b: b, w: w, h: h}
	r.m = New(Options{
		Backend: b,
		Random:  strings.NewReader(strings.Repeat("\x01\x02\x03\x04\x05\x06\x07\x08\x09\x0a\x0b\x0c", 8)),
		After: func(d time.Duration, fn func(time.Time) tea.Msg) tea.Cmd {
			return func() tea.Msg { return fn(time.Time{}) }
		},
	})
	r.m.SetSize(w, h)
	r.run(r.m.Init())
	return r
}

// run executes cmd and every command that follows, feeding each answer to
// the model. A timer is not run: it waits in r.pending.
func (r *rig) run(cmd tea.Cmd) {
	r.t.Helper()
	if cmd == nil {
		return
	}
	r.b.inCmd = true
	msg := cmd()
	r.b.inCmd = false
	switch v := msg.(type) {
	case nil:
	case tea.BatchMsg:
		for _, c := range v {
			r.run(c)
		}
	case tickMsg:
		r.pending = append(r.pending, func() tea.Msg { return v })
	default:
		r.feed(msg)
	}
}

func (r *rig) feed(msg tea.Msg) Action {
	r.t.Helper()
	cmd, act := r.m.Update(msg)
	r.last = act
	if act.Kind != None {
		r.actions = append(r.actions, act)
	}
	r.run(cmd)
	return act
}

// tick fires the timers that wait.
func (r *rig) tick() {
	r.t.Helper()
	p := r.pending
	r.pending = nil
	for _, c := range p {
		r.feed(c())
	}
}

func (r *rig) key(name string) Action {
	r.t.Helper()
	return r.feed(keyMsg(name))
}

func (r *rig) typ(s string) {
	r.t.Helper()
	for _, c := range s {
		r.key(string(c))
	}
}

func keyMsg(name string) tea.KeyPressMsg {
	switch name {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	case "ctrl+u":
		return tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "space":
		return tea.KeyPressMsg{Code: ' ', Text: " "}
	}
	rs := []rune(name)
	if len(rs) != 1 {
		panic("keyMsg: " + name)
	}
	return tea.KeyPressMsg{Code: rs[0], Text: name}
}

// draw draws the view into a buffer of the size of the rig.
func (r *rig) draw() *uv.Buffer {
	buf := uv.NewBuffer(r.w, r.h)
	r.m.Draw(buf, buf.Bounds())
	return buf
}

func rowText(buf *uv.Buffer, y int) string {
	var b strings.Builder
	for x := 0; x < buf.Bounds().Dx(); x++ {
		if c := buf.CellAt(x, y); c != nil {
			b.WriteString(c.Content)
		}
	}
	return strings.TrimRight(b.String(), " ")
}

func (r *rig) rows() []string {
	buf := r.draw()
	out := make([]string, buf.Bounds().Dy())
	for y := range out {
		out[y] = rowText(buf, y)
	}
	return out
}

func (r *rig) screen() string { return strings.Join(r.rows(), "\n") }

// seen fails the test unless every text is on the screen.
func (r *rig) seen(texts ...string) {
	r.t.Helper()
	s := r.screen()
	for _, x := range texts {
		if !strings.Contains(s, x) {
			r.t.Fatalf("%q is not on the screen:\n%s", x, s)
		}
	}
}

// notSeen fails the test when a text is on the screen.
func (r *rig) notSeen(texts ...string) {
	r.t.Helper()
	s := r.screen()
	for _, x := range texts {
		if strings.Contains(s, x) {
			r.t.Fatalf("%q is on the screen:\n%s", x, s)
		}
	}
}

var errLost = errors.New("connection reset by peer \x1b]0;pwned\a")
