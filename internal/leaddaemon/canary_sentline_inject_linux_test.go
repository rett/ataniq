//go:build linux && ataniq_canary

package leaddaemon

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"ataniq/internal/leadcanary"
	"ataniq/internal/leadregistry"
)

// TestCanaryInjectWhoseSentLineCannotBeWritten runs the whole inject operation
// with an evidence file that takes no line: the message is answered as sent,
// the epoch is spent, the log says what is lost, and a second message is
// refused.
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
			logf: func(f string, a ...any) { fmt.Fprintf(&log, f+"\n", a...) },
		}
		got, fail := x.inject(context.Background(), CanaryInjectParams{Role: canaryRoleText, MessageID: "msg-one", Body: "body text\n"})
		if fail != nil {
			t.Fatalf("the answer is an error: %+v", fail)
		}
		res, ok := got.(CanaryInjectResult)
		if !ok || res.Result.Outcome != "submitted" {
			t.Fatalf("result %+v", got)
		}
		if !x.spent[started.Epoch] {
			t.Fatal("the epoch is not spent")
		}
		if !strings.Contains(log.String(), "stop the role before the daemon is restarted") {
			t.Fatalf("the log does not say what is lost:\n%s", log.String())
		}
		if _, fail := x.inject(context.Background(), CanaryInjectParams{Role: canaryRoleText, MessageID: "msg-two", Body: "body text\n"}); fail == nil || fail.Message != msgSecond {
			t.Fatalf("a second message: %+v", fail)
		}
	})
}
