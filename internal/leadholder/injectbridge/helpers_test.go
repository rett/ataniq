//go:build linux && ataniq_canary

package injectbridge

import (
	"context"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"ataniq/internal/inject"
	"ataniq/internal/inject/idle"
	"ataniq/internal/leadholder/eventlog"
	"ataniq/internal/leadholder/fakeagent"
	"ataniq/internal/leadholder/ptyhost"
	"ataniq/internal/leadholder/session"
	"ataniq/internal/terminalconsole/sessionpane"
)

// The tests start no agent: a session's child is this test binary, run again
// as the scripted fake agent.
func TestMain(m *testing.M) {
	fakeagent.RunIfChild()
	os.Exit(m.Run())
}

const patience = 20 * time.Second

// A pair that is in no tested table, in neutral words.
const (
	sampleRuntime = "sample-runtime"
	sampleVersion = "0.0.1"
)

func hx(s string) string { return hex.EncodeToString([]byte(s)) }

// startSession runs script as the fake agent in a real session; the test's
// cleanup closes it.
func startSession(t *testing.T, script string, mod ...func(*session.Config)) *session.Session {
	t.Helper()
	root := t.TempDir()
	logDir, stateDir := filepath.Join(root, "log"), filepath.Join(root, "state")
	for _, d := range []string{logDir, stateDir} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	log, err := eventlog.Open(logDir, eventlog.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { log.Close() }) // after the session's own cleanup
	argv, env := fakeagent.Command(script)
	cfg := session.Config{
		Role: "proj/lead", Epoch: "e1",
		Argv: argv, Env: env, Cols: 100, Rows: 30,
		RoleLock: filepath.Join(root, "lead.lock"), StateDir: stateDir, Log: log,
		StopGrace: 300 * time.Millisecond,
		PTY:       ptyhost.Config{NoCgroup: true},
	}
	for _, m := range mod {
		m(&cfg)
	}
	s, err := session.Start(cfg)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// screenText is the session's screen as text, read with no fence.
func screenText(s *session.Session) string {
	var out string
	s.Screen(func(p *sessionpane.Pane, _ uint64) { out = p.TextSnapshot().Text })
	return out
}

// poll calls f until it returns true or the patience ends.
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

func waitScreen(t *testing.T, s *session.Session, want string) {
	t.Helper()
	var last string
	poll(t, func() string {
		return "the screen to show " + strconv.Quote(want) + "; last screen " + strconv.Quote(last)
	}, func() bool {
		last = screenText(s)
		return strings.Contains(last, want)
	})
}

var rxRe = regexp.MustCompile(`RX:([0-9a-f]+)`)

// received decodes the bytes the fake agent's echo step reported on the
// screen: everything the agent has read from its terminal.
func received(s *session.Session) []byte {
	var all []byte
	for _, m := range rxRe.FindAllStringSubmatch(screenText(s), -1) {
		if b, err := hex.DecodeString(m[1]); err == nil {
			all = append(all, b...)
		}
	}
	return all
}

// waitReceived waits until the agent has reported at least as many bytes as
// want has, and fails unless they are exactly want.
func waitReceived(t *testing.T, s *session.Session, want string) {
	t.Helper()
	var got []byte
	poll(t, func() string {
		return "the agent to receive " + strconv.Quote(want) + "; got " + strconv.Quote(string(got))
	}, func() bool {
		got = received(s)
		return len(got) >= len(want)
	})
	if string(got) != want {
		t.Fatalf("the agent received %q, want %q", got, want)
	}
}

// onlyProbeReceived shows that nothing was written to the agent before now:
// it types one key as the writer and requires the agent's whole input, from
// its start, to be that key.
func onlyProbeReceived(t *testing.T, s *session.Session) {
	t.Helper()
	s.Input([]byte("z"))
	waitReceived(t, s, "z")
}

// testCtx is a context that ends well after any test should.
func testCtx(t testing.TB) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*patience)
	t.Cleanup(cancel)
	return ctx
}

// inFrozen takes the bridge's lock, settles and runs fn inside one Frozen
// call, as the library does. Every step must succeed.
func inFrozen(t testing.TB, b *Bridge, fn func(v inject.View, write func([]byte) error)) {
	t.Helper()
	ctx := testCtx(t)
	err := b.WithInputLock(ctx, func(l inject.Locked) error {
		if _, err := l.Settle(ctx, 0, 50*time.Millisecond, 2*time.Second); err != nil {
			t.Fatalf("Settle: %v", err)
		}
		return l.Frozen(time.Now().Add(patience), func(v inject.View, write func([]byte) error) error {
			fn(v, write)
			return nil
		})
	})
	if err != nil {
		t.Fatalf("WithInputLock: %v", err)
	}
}

// testedPairs asks the library for its tested table, so that no file of this
// package names a runtime.
func testedPairs(t testing.TB) []idle.Entry {
	t.Helper()
	pairs := idle.Tested()
	if len(pairs) == 0 {
		t.Fatal("the library's tested table is empty")
	}
	for _, p := range pairs {
		if _, ok := idle.Lookup(p.Runtime, p.Version); !ok {
			t.Fatalf("the table lists a pair that Lookup does not find: %q %q", p.Runtime, p.Version)
		}
	}
	return pairs
}

// sampleText is a message for the id, in the header's form.
func sampleText(id string) string {
	return "[ataniq-msg id=" + id + " from=sample kind=note]\nsample body 4f1c"
}

// holdingTheInputLock runs fn while the session's input lock is held by the
// test: anything in fn that waited for the lock would not return.
func holdingTheInputLock(t *testing.T, s *session.Session, fn func()) {
	t.Helper()
	err := s.WithInputLock(func(session.AgentWriter) error {
		fn()
		return nil
	})
	if err != nil {
		t.Fatalf("WithInputLock: %v", err)
	}
}
