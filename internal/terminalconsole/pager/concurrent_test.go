package pager

import (
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
)

// sharedSource is a Source that is safe for concurrent use, as Source asks,
// and that notes how many calls were inside it at once.
type sharedSource struct {
	pages []tpage

	mu           sync.Mutex
	inside, most int
	calls        int
	// gate, when not nil, holds every call that comes in until it is
	// closed. Each such call first sends on entered.
	gate    chan struct{}
	entered chan struct{}
}

func (s *sharedSource) Page(n int) ([]string, []bool, int, error) {
	s.mu.Lock()
	s.calls++
	s.inside++
	s.most = max(s.most, s.inside)
	gate, entered := s.gate, s.entered
	s.mu.Unlock()
	if gate != nil {
		entered <- struct{}{}
		<-gate
	}
	s.mu.Lock()
	s.inside--
	s.mu.Unlock()
	return s.pages[n].lines, s.pages[n].cont, len(s.pages), nil
}

// parent drives a pager as Bubble Tea does: every command runs on a
// goroutine of its own, and its answer comes back to the one goroutine
// that calls Update and Draw. The rig of the other tests runs a command to
// its end before it goes on, so that nothing there happens at once.
type parent struct {
	t       *testing.T
	m       *Model
	answers chan tea.Msg
	// flying is the number of commands whose answer was not taken yet.
	flying int
	buf    *uv.Buffer
}

func newParent(t *testing.T, src Source, w, h int) *parent {
	t.Helper()
	p := &parent{t: t, m: New(src, Options{Colors: testColors}), answers: make(chan tea.Msg), buf: uv.NewBuffer(w, h)}
	p.start(p.m.Init())
	p.start(p.m.SetSize(w, h))
	return p
}

// start runs cmd on a goroutine of its own.
func (p *parent) start(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	p.flying++
	go func() { p.answers <- cmd() }()
}

// key hands a key to the pager and starts the command it returns.
func (p *parent) key(k tea.KeyPressMsg) {
	p.t.Helper()
	cmd, act := p.m.Update(k)
	if act != None {
		p.t.Fatalf("key %s: action %d", k.String(), act)
	}
	p.start(cmd)
}

// take hands an answer to the pager. With wait unset it takes one only if
// one has come, and reports whether it did.
func (p *parent) take(wait bool) bool {
	p.t.Helper()
	var msg tea.Msg
	if wait {
		select {
		case msg = <-p.answers:
		case <-time.After(time.Minute):
			p.t.Fatal("a command of the pager does not return")
		}
	} else {
		select {
		case msg = <-p.answers:
		default:
			return false
		}
	}
	p.flying--
	cmd, act := p.m.Update(msg)
	if act != None {
		p.t.Fatalf("a page answer asked the parent for action %d", act)
	}
	p.start(cmd)
	return true
}

// settle takes answers until no command is running.
func (p *parent) settle() {
	p.t.Helper()
	for p.flying > 0 {
		p.take(true)
	}
}

// first draws the pager and returns the first row of the patch.
func (p *parent) first() string {
	p.m.Draw(p.buf, p.buf.Bounds())
	return strings.TrimRight(rowText(p.buf, 1, marginCells), " ")
}

func (p *parent) await(c chan struct{}, what string) {
	p.t.Helper()
	select {
	case <-c:
	case <-time.After(time.Minute):
		p.t.Fatal(what)
	}
}

// When the reader goes to the top while a page is being read, the pager
// asks for the first page without waiting: two calls of Page run at once.
// The answer to the request it gave up is dropped, whichever comes first.
func TestTwoPagesAreReadAtOnce(t *testing.T) {
	src := &sharedSource{pages: paginate(numbered(2000), 20, 0)}
	p := newParent(t, src, 40, 12)
	p.settle()
	for i := 0; i < 30; i++ {
		p.key(keyEnd)
		p.settle()
	}
	if p.m.lo == 0 {
		t.Fatal("after 30 times End the first page is still held")
	}
	gate, entered := make(chan struct{}), make(chan struct{})
	src.mu.Lock()
	src.gate, src.entered = gate, entered
	src.mu.Unlock()

	p.key(keyEnd)
	if p.flying != 1 {
		t.Fatal("End asked for no page")
	}
	p.await(entered, "the page that End asked for is not read")
	p.key(keyHome)
	if p.flying != 2 {
		t.Fatal("Home asked for no page while another was being read")
	}
	p.await(entered, "the page that Home asked for is not read while another is")
	src.mu.Lock()
	most := src.most
	src.gate = nil
	src.mu.Unlock()
	if most != 2 {
		t.Fatalf("%d calls of Page ran at once, want 2", most)
	}
	close(gate)
	p.settle()
	if got := p.first(); got != "l000" || p.m.Err() != nil || p.m.lo != 0 {
		t.Fatalf("after Home the first row is %q, the first page held %d, err %v", got, p.m.lo, p.m.Err())
	}
}

// Keys are handled and the pager is drawn while commands run, as in the
// program. Under the race detector this fails if a command touches what
// Update and Draw use.
func TestKeysWhileCommandsRun(t *testing.T) {
	src := &sharedSource{pages: paginate(numbered(2000), 20, 0)}
	p := newParent(t, src, 40, 12)
	keys := []tea.KeyPressMsg{keyEnd, keyEnd, keyPgDn, keyHome, keyJ, keyEnd, keyPgUp, keyEnd, keyEnd, keyK, keyHome, keyPgDn, keyEnd}
	for i := 0; i < 2000; i++ {
		p.key(keys[i%len(keys)])
		p.first()
		for p.take(false) {
		}
		if i%len(keys) == 0 {
			// Let what is read come in now and then, so that the keys get
			// further than the first pages.
			p.settle()
		}
	}
	p.settle()
	if p.m.Err() != nil {
		t.Fatalf("err %v", p.m.Err())
	}
	p.key(keyHome)
	p.settle()
	if got := p.first(); got != "l000" {
		t.Fatalf("after Home the first row is %q", got)
	}
	for i := 0; !p.m.Reached(); i++ {
		if i > 200 {
			t.Fatal("End never reaches the end")
		}
		p.key(keyEnd)
		p.settle()
	}
	if got := p.first(); got != "l1990" {
		t.Fatalf("at the end the first row is %q", got)
	}
}
