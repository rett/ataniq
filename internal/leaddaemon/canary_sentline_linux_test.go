//go:build linux && ataniq_canary

package leaddaemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
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
	// The daemon waits for the holder far longer than the test holds it, so
	// that every check below is made during the wait, however slow the run.
	cfg := c.daemonConfig()
	cfg.Canary.InjectWait = 15 * time.Second
	c.running = c.startDaemon(cfg)
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
	l := c.lastEvidence()
	if l["event"] != "inject-sent" || l["message_id"] != "msg-one" || l["epoch"] != float64(started.Epoch) {
		t.Fatalf("the last line while the holder works is %v", l)
	}
	// The line carries the id of the frame the holder has.
	reqs := c.holder.requests(wire.OpInject)
	if len(reqs) != 1 || l["effect_id"] == "" || l["effect_id"] != reqs[0].CommandID {
		t.Fatalf("effect id %v, requests %+v", l["effect_id"], reqs)
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

// TestCallHolderThenCallsWrittenAfterTheFrame: the record callback runs once,
// before the socket is dialled and so before the holder has any frame, and the
// frame follows it; when the callback fails nothing is dialled or written, and
// when nothing can be dialled the callback has run and the stage says that
// nothing was sent. (The name is the gate's from the time the line was written
// after the frame.)
func TestCallHolderThenCallsWrittenAfterTheFrame(t *testing.T) {
	c := newCanaryEnv(t)
	req := wire.Request{Op: wire.OpInject, Epoch: 1, MessageID: "msg-one", Text: "body text\n", CommandID: "sample-1"}

	calls := 0
	_, st, err := callHolderThen(context.Background(), c.holderDir, req, time.Second, time.Second, func() error { calls++; return nil })
	if st != sendUnsent || err == nil || calls != 1 {
		t.Fatalf("no holder: stage %v, err %v, %d calls", st, err, calls)
	}

	release := make(chan struct{})
	holder := newFakeHolder(t, c.holderDir, func(req wire.Request, conn net.Conn) {
		<-release
		submitted(req, conn)
	})
	var once sync.Once
	letGo := func() { once.Do(func() { close(release) }) }
	t.Cleanup(letGo)

	// A record that fails: no frame reaches the holder, and the holder is not
	// dialled at all.
	calls = 0
	boom := errors.New("the evidence takes no line")
	_, st, err = callHolderThen(context.Background(), c.holderDir, req, time.Second, time.Second, func() error { calls++; return boom })
	if st != sendNotRecorded || !errors.Is(err, boom) || calls != 1 {
		t.Fatalf("failed record: stage %v, err %v, %d calls", st, err, calls)
	}
	time.Sleep(50 * time.Millisecond)
	if n := len(holder.requests(wire.OpInject)); n != 0 {
		t.Fatalf("%d frames reached the holder after a record that failed", n)
	}

	// A record that works runs while the holder has no frame, and the frame
	// follows; the holder answers once the record is done.
	calls = 0
	seen := -1
	go func() {
		// Lets the holder answer once the frame has reached it.
		deadline := time.Now().Add(5 * time.Second)
		for len(holder.requests(wire.OpInject)) == 0 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		letGo()
	}()
	resp, st, err := callHolderThen(context.Background(), c.holderDir, req, 2*time.Second, 10*time.Second, func() error {
		calls++
		seen = len(holder.requests(wire.OpInject))
		return nil
	})
	if err != nil || st != sendWritten || !resp.OK || resp.Inject == nil {
		t.Fatalf("%+v stage %v err %v", resp, st, err)
	}
	if calls != 1 || seen != 0 {
		t.Fatalf("%d calls, %d requests at the holder when the record ran", calls, seen)
	}
	if n := len(holder.requests(wire.OpInject)); n != 1 {
		t.Fatalf("%d frames reached the holder", n)
	}
}

// TestCanarySentLineThatCannotBeWritten: appendSent reports a closed file's
// refusal, logs it and writes nothing; on a file that takes lines it writes the
// inject-sent line with the effect id.
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

	// A closed file takes no line.
	closed := open()
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	x := ext(closed)
	if err := x.appendSent("msg-one", 3, "lead-inject-3-aabbcc-1"); err == nil {
		t.Fatal("a line was written to a closed file")
	}
	if !strings.Contains(log.String(), "canary evidence: inject-sent: ") {
		t.Fatalf("log:\n%s", log.String())
	}
	if spent, err := spentEpochs(c.cfg.EvidencePath(), canaryRoleText); err != nil || len(spent) != 0 {
		t.Fatalf("spent %v, err %v", spent, err)
	}

	// An open file takes it, with the effect id.
	ev := open()
	defer ev.Close()
	x = ext(ev)
	if err := x.appendSent("msg-one", 3, "lead-inject-3-aabbcc-1"); err != nil || log.Len() != 0 {
		t.Fatalf("err %v, log:\n%s", err, log.String())
	}
	spent, err := spentEpochs(c.cfg.EvidencePath(), canaryRoleText)
	if err != nil || len(spent) != 1 || !spent[3] {
		t.Fatalf("spent %v, err %v", spent, err)
	}
	data, err := os.ReadFile(c.cfg.EvidencePath())
	if err != nil || !strings.Contains(string(data), `"effect_id":"lead-inject-3-aabbcc-1"`) {
		t.Fatalf("err %v, evidence:\n%s", err, data)
	}
}
