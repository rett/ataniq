//go:build linux && ataniq_canary

package leaddaemon

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"ataniq/internal/leadcanary"
	"ataniq/internal/leadholder/wire"
)

// TestCanarySentLineIsWrittenWhileTheHolderWorks: the inject-sent line is in
// the evidence while the holder still holds the request frame and has not
// answered, so a daemon that is killed during the wait leaves the epoch spent
// for the daemon that starts next. spentEpochs is what that daemon reads.
func TestCanarySentLineIsWrittenWhileTheHolderWorks(t *testing.T) {
	c := newCanaryEnv(t)
	c.start()
	c.define()
	started := c.startRole()
	release := make(chan struct{})
	held := make(chan struct{}, 1)
	c.holder = newFakeHolder(t, c.holderDir, func(req wire.Request, conn net.Conn) {
		held <- struct{}{}
		<-release
		submitted(req, conn)
	})
	// Registered after the holder's own cleanup, so it runs before it: a test
	// that fails below still lets the holder go.
	var once sync.Once
	letGo := func() { once.Do(func() { close(release) }) }
	t.Cleanup(letGo)

	type answer struct {
		got CanaryInjectResult
		err error
	}
	done := make(chan answer, 1)
	go func() {
		got, err := c.inject("msg-one", "body text\n")
		done <- answer{got, err}
	}()
	select {
	case <-held:
	case a := <-done:
		t.Fatalf("the inject ended before the holder had the frame: %+v %v", a.got, a.err)
	case <-time.After(10 * time.Second):
		t.Fatal("the frame did not reach the holder in 10 s")
	}

	// The holder has the frame and holds its answer. The line follows the
	// frame by one synced write, so the check allows it a moment; the holder
	// answers nothing meanwhile.
	path := c.cfg.EvidencePath()
	deadline := time.Now().Add(5 * time.Second)
	for {
		spent, err := spentEpochs(path, canaryRoleText)
		if err != nil {
			t.Fatalf("the evidence cannot be read back: %v", err)
		}
		if spent[started.Epoch] {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the frame reached the holder and the evidence does not spend epoch %d: %v", started.Epoch, spent)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if l := c.lastEvidence(); l["event"] != "inject-sent" || l["message_id"] != "msg-one" || l["epoch"] != float64(started.Epoch) {
		t.Fatalf("the last line while the holder works is %v", l)
	}
	select {
	case a := <-done:
		t.Fatalf("the inject ended while the holder held its answer: %+v %v", a.got, a.err)
	default:
	}

	letGo()
	select {
	case a := <-done:
		if a.err != nil || a.got.Result.Outcome != wire.InjectOutcomeSubmitted {
			t.Fatalf("%+v %v", a.got, a.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the inject did not end in 10 s")
	}
	// One inject-sent line, and the result after it.
	ev := c.events()
	if n := len(ev); n < 2 || ev[n-2] != "inject-sent" || ev[n-1] != "inject-result" {
		t.Fatalf("events %v", ev)
	}
	sent := 0
	for _, e := range ev {
		if e == "inject-sent" {
			sent++
		}
	}
	if sent != 1 {
		t.Fatalf("%d inject-sent lines: %v", sent, ev)
	}
}

// TestCallHolderThenCallsWrittenAfterTheFrame: written runs once, when the
// holder has the whole frame and before the answer is read, and not at all
// when nothing reached the holder.
func TestCallHolderThenCallsWrittenAfterTheFrame(t *testing.T) {
	c := newCanaryEnv(t)
	req := wire.Request{Op: wire.OpInject, Epoch: 1, MessageID: "msg-one", Text: "body text\n", CommandID: "sample-1"}

	calls := 0
	_, wrote, err := callHolderThen(context.Background(), c.holderDir, req, time.Second, time.Second, func() { calls++ })
	if wrote || err == nil || calls != 0 {
		t.Fatalf("no holder: wrote %v, err %v, %d calls", wrote, err, calls)
	}

	release := make(chan struct{})
	holder := newFakeHolder(t, c.holderDir, func(req wire.Request, conn net.Conn) {
		<-release
		submitted(req, conn)
	})
	var once sync.Once
	letGo := func() { once.Do(func() { close(release) }) }
	t.Cleanup(letGo)
	seen := -1
	resp, wrote, err := callHolderThen(context.Background(), c.holderDir, req, 2*time.Second, 10*time.Second, func() {
		calls++
		// The holder reads the frame the daemon has written; it has not
		// answered, since its answer waits for the line below.
		deadline := time.Now().Add(5 * time.Second)
		for len(holder.requests(wire.OpInject)) == 0 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		seen = len(holder.requests(wire.OpInject))
		letGo()
	})
	if err != nil || !wrote || !resp.OK || resp.Inject == nil {
		t.Fatalf("%+v wrote %v err %v", resp, wrote, err)
	}
	if calls != 1 || seen != 1 {
		t.Fatalf("%d calls, %d requests at the holder when written ran", calls, seen)
	}
}

// TestCanarySentLineThatCannotBeWritten: when the evidence takes no line the
// message is still a sent one. The line is tried again, and when that fails
// too the log says that the epoch is spent in this daemon only.
func TestCanarySentLineThatCannotBeWritten(t *testing.T) {
	c := newCanaryEnv(t)
	open := func() *leadcanary.Evidence {
		t.Helper()
		ev, err := leadcanary.OpenEvidence(c.cfg, time.Now)
		if err != nil {
			t.Fatal(err)
		}
		return ev
	}
	var log strings.Builder
	ext := func(ev *leadcanary.Evidence) *canaryExt {
		log.Reset()
		return &canaryExt{ev: ev, logf: func(format string, a ...any) { fmt.Fprintf(&log, format+"\n", a...) }}
	}
	const lost = "the message of epoch 3 is sent and the evidence has no inject-sent line for it"

	// A closed file takes no line: neither try writes one.
	closed := open()
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	x := ext(closed)
	if x.recordSent("msg-one", 3) {
		t.Fatal("a line was written to a closed file")
	}
	if !strings.Contains(log.String(), "canary evidence: inject-sent: ") || strings.Contains(log.String(), lost) {
		t.Fatalf("log after the first try:\n%s", log.String())
	}
	x.sentUnrecorded("msg-one", 3)
	if !strings.Contains(log.String(), lost) || !strings.Contains(log.String(), "stop the role before the daemon is restarted") {
		t.Fatalf("the log does not say what is lost:\n%s", log.String())
	}
	if spent, err := spentEpochs(c.cfg.EvidencePath(), canaryRoleText); err != nil || len(spent) != 0 {
		t.Fatalf("spent %v, err %v", spent, err)
	}

	// The second try succeeds: the line is there and nothing is lost.
	ev := open()
	defer ev.Close()
	x = ext(ev)
	x.sentUnrecorded("msg-one", 3)
	if log.Len() != 0 {
		t.Fatalf("log:\n%s", log.String())
	}
	spent, err := spentEpochs(c.cfg.EvidencePath(), canaryRoleText)
	if err != nil || len(spent) != 1 || !spent[3] {
		t.Fatalf("spent %v, err %v", spent, err)
	}
}
