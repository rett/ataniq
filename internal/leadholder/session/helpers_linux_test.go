//go:build linux

package session

import (
	"bytes"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"

	"ataniq/internal/leadholder/eventlog"
	"ataniq/internal/leadholder/fakeagent"
	"ataniq/internal/leadholder/ptyhost"
	"ataniq/internal/terminalconsole/sessionpane"
)

func TestMain(m *testing.M) {
	fakeagent.RunIfChild()
	os.Exit(m.Run())
}

const patience = 20 * time.Second

// testEnv is the folders and the event log of one test.
type testEnv struct {
	t        *testing.T
	logDir   string
	stateDir string
	lock     string
	log      *eventlog.Log
}

func newEnv(t *testing.T) *testEnv {
	t.Helper()
	root := t.TempDir()
	e := &testEnv{t: t, logDir: filepath.Join(root, "log"), stateDir: filepath.Join(root, "state"), lock: filepath.Join(root, "lead.lock")}
	for _, d := range []string{e.logDir, e.stateDir} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	l, err := eventlog.Open(e.logDir, eventlog.Options{})
	if err != nil {
		t.Fatal(err)
	}
	e.log = l
	t.Cleanup(func() { l.Close() }) // runs after the sessions' own cleanups
	return e
}

func (e *testEnv) config(script string) Config {
	argv, env := fakeagent.Command(script)
	return Config{
		Role: "proj/lead", Epoch: "e1",
		Argv: argv, Env: env, Cols: 100, Rows: 30,
		RoleLock: e.lock, StateDir: e.stateDir, Log: e.log,
		StopGrace: 300 * time.Millisecond,
		PTY:       ptyhost.Config{NoCgroup: true},
	}
}

// start runs a script as the fake agent; cleanup closes the session.
func (e *testEnv) start(script string, mod ...func(*Config)) *Session {
	e.t.Helper()
	cfg := e.config(script)
	for _, m := range mod {
		m(&cfg)
	}
	s, err := Start(cfg)
	if err != nil {
		e.t.Fatalf("Start: %v", err)
	}
	e.t.Cleanup(func() { s.Close() })
	return s
}

func (e *testEnv) events() []eventlog.Event {
	e.t.Helper()
	evs, err := eventlog.Read(e.logDir)
	if err != nil {
		e.t.Fatalf("read the event log: %v", err)
	}
	return evs
}

func eventsNamed(evs []eventlog.Event, name eventlog.EventName) []eventlog.Event {
	var out []eventlog.Event
	for _, ev := range evs {
		if ev.Event == name {
			out = append(out, ev)
		}
	}
	return out
}

func screenText(s *Session) string {
	var out string
	s.Screen(func(p *sessionpane.Pane, _ uint64) { out = paneText(p) })
	return out
}

func paneText(p *sessionpane.Pane) string {
	cols, rows := p.Size()
	out, _ := p.Text(uv.Pos(0, 0), uv.Pos(cols-1, rows-1), 1<<20)
	return out
}

func position(s *Session) uint64 {
	var pos uint64
	s.Screen(func(_ *sessionpane.Pane, p uint64) { pos = p })
	return pos
}

// poll calls f until it returns true or the deadline passes.
func poll(t testing.TB, what func() string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(patience)
	for !f() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func waitScreen(t *testing.T, s *Session, want string) {
	t.Helper()
	var last string
	poll(t, func() string {
		return "the screen to show " + strconv.Quote(want) + "; last screen " + strconv.Quote(last)
	}, func() bool {
		last = screenText(s)
		return strings.Contains(last, want)
	})
}

func waitDone(t *testing.T, s *Session) {
	t.Helper()
	select {
	case <-s.Done():
	case <-time.After(patience):
		t.Fatalf("the session did not end; facts %+v", s.Facts())
	}
}

var rxRe = regexp.MustCompile(`RX:([0-9a-f]+)`)

// rxBytes decodes the bytes the fake agent's echo step reported on the screen.
func rxBytes(screen string) []byte {
	var all []byte
	for _, m := range rxRe.FindAllStringSubmatch(screen, -1) {
		if b, err := hex.DecodeString(m[1]); err == nil {
			all = append(all, b...)
		}
	}
	return all
}

// waitRX waits until the agent has reported exactly want, and no more.
func waitRX(t *testing.T, s *Session, want string) {
	t.Helper()
	var got []byte
	poll(t, func() string {
		return "the agent to receive " + strconv.Quote(want) + "; got " + strconv.Quote(string(got))
	}, func() bool {
		got = rxBytes(screenText(s))
		return len(got) >= len(want)
	})
	if string(got) != want {
		t.Fatalf("the agent received %q, want %q", got, want)
	}
}

// capture records the chunks a subscriber sees.
type capture struct {
	mu     sync.Mutex
	chunks [][]byte
	poss   []uint64
}

func (c *capture) fn(pos uint64, chunk []byte) {
	c.mu.Lock()
	c.chunks = append(c.chunks, append([]byte(nil), chunk...))
	c.poss = append(c.poss, pos)
	c.mu.Unlock()
}

func (c *capture) all() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return bytes.Join(c.chunks, nil)
}

func countFDs(t testing.TB) int {
	t.Helper()
	es, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	return len(es)
}

// settled waits for the descriptors and goroutines to fall back to the given
// counts.
func settled(t testing.TB, fds0, gos0 int) {
	t.Helper()
	deadline := time.Now().Add(patience)
	for {
		fds, gos := countFDs(t), runtime.NumGoroutine()
		if fds <= fds0 && gos <= gos0 {
			return
		}
		if time.Now().After(deadline) {
			buf := make([]byte, 1<<16)
			buf = buf[:runtime.Stack(buf, true)]
			t.Fatalf("descriptors %d -> %d, goroutines %d -> %d\n%s", fds0, fds, gos0, gos, buf)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// gone reports whether no live process has the pid (a zombie is not live).
func gone(pid int) bool {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return true
	}
	i := bytes.LastIndexByte(b, ')')
	return i < 0 || i+2 >= len(b) || b[i+2] == 'Z' || b[i+2] == 'X'
}

func readPid(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	return n
}
