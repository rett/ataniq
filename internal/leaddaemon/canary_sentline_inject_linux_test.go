//go:build linux && ataniq_canary

package leaddaemon

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"ataniq/internal/leadcanary"
	"ataniq/internal/leadholder/wire"
	"ataniq/internal/leadregistry"
)

// TestCanaryInjectWhoseSentLineCannotBeWritten runs the whole inject operation
// with an evidence file that takes no line: nothing is sent, the message is
// refused as unavailable (its receipt's code is failed, the evidence line's
// evidence-unwritable) and is not spent, the log says so, and the holder never
// has a frame.
func TestCanaryInjectWhoseSentLineCannotBeWritten(t *testing.T) {
	c := newCanaryEnv(t)
	started := c.up(submitted)
	if err := c.running.stop(); err != nil {
		t.Fatal(err)
	}
	ev, err := leadcanary.OpenEvidence(c.cfg, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if err := ev.Close(); err != nil { // takes no line from here on
		t.Fatal(err)
	}
	adm, err := leadcanary.NewAdmission(c.cfg.Role)
	if err != nil {
		t.Fatal(err)
	}
	var log strings.Builder
	c.prepare(func(reg *leadregistry.Registry) {
		x := &canaryExt{
			cfg: c.cfg, role: c.cfg.Role, holderDir: c.holderDir, reg: reg, ev: ev, adm: adm,
			dial: 2 * time.Second, wait: 5 * time.Second, spent: map[uint64]bool{},
			logf:  func(f string, a ...any) { fmt.Fprintf(&log, f+"\n", a...) },
			build: sameHolderBuild(),
		}
		_, fail := x.inject(context.Background(), CanaryInjectParams{Role: canaryRoleText, MessageID: "msg-one", Body: "body text\n"})
		if fail == nil || fail.Code != CodeUnavailable || fail.Message != msgEvidence {
			t.Fatalf("the answer is %+v, want unavailable with the evidence's text", fail)
		}
		if x.spent[started.Epoch] {
			t.Fatal("the epoch is spent for a message that was not sent")
		}
		if !strings.Contains(log.String(), "the inject-sent line could not be written, so nothing was sent") {
			t.Fatalf("the log does not say what happened:\n%s", log.String())
		}
		if n := len(c.holder.requests(wire.OpInject)); n != 0 {
			t.Fatalf("%d frames reached the holder", n)
		}
		// The epoch is free: with an evidence that takes lines the message goes.
		ev2, err := leadcanary.OpenEvidence(c.cfg, time.Now)
		if err != nil {
			t.Fatal(err)
		}
		defer ev2.Close()
		x.ev = ev2
		got, fail := x.inject(context.Background(), CanaryInjectParams{Role: canaryRoleText, MessageID: "msg-two", Body: "body text\n"})
		if fail != nil {
			t.Fatalf("the answer is an error: %+v", fail)
		}
		if res, ok := got.(CanaryInjectResult); !ok || res.Result.Outcome != "submitted" {
			t.Fatalf("result %+v", got)
		}
		if !x.spent[started.Epoch] {
			t.Fatal("the epoch is not spent after the message went")
		}
		if _, fail := x.inject(context.Background(), CanaryInjectParams{Role: canaryRoleText, MessageID: "msg-three", Body: "body text\n"}); fail == nil || fail.Message != msgSecond {
			t.Fatalf("a third message: %+v", fail)
		}
	})
}
