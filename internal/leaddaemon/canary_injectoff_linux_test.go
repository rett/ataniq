//go:build linux && ataniq_canary

package leaddaemon

import (
	"testing"

	"ataniq/internal/leadholder/wire"
)

// TestCanaryInjectOffIsRefusedAtOnce: with "inject": false in the config the
// daemon refuses a message before anything else, sends nothing to the holder
// and does not spend the epoch's message.
func TestCanaryInjectOffIsRefusedAtOnce(t *testing.T) {
	c := newCanaryEnv(t)
	c.cfg.Inject = false
	started := c.up(submitted)
	for _, body := range []string{
		"body text\n",
		"", // the config is judged before the message is
	} {
		_, err := c.inject("msg-one", body)
		ce := wantCode(t, err, CodeRefused)
		if ce.Message != msgInjectOff {
			t.Fatalf("message %q, want %q", ce.Message, msgInjectOff)
		}
		if l := c.lastEvidence(); l["event"] != "inject-refused" || l["code"] != "inject-off" ||
			l["message_id"] != "msg-one" || l["epoch"] != float64(started.Epoch) {
			t.Fatalf("line %v", l)
		}
	}
	if n := len(c.holder.requests("")); n != 0 {
		t.Fatalf("%d requests reached the holder", n)
	}
	for _, l := range c.evidence() {
		if l["event"] == "inject-sent" {
			t.Fatal("inject-sent was written with injection off")
		}
	}

	// The message is not spent: the same epoch takes it once the daemon runs
	// with a config that turns injection on.
	c.cfg.Inject = true
	c.restart()
	got, err := c.inject("msg-one", "body text\n")
	if err != nil || got.Epoch != started.Epoch || got.Result.Outcome != wire.InjectOutcomeSubmitted {
		t.Fatalf("%+v %v", got, err)
	}
}

// TestCanaryInjectOffBeforeTheRoleRuns: the refusal does not wait for a role.
func TestCanaryInjectOffBeforeTheRoleRuns(t *testing.T) {
	c := newCanaryEnv(t)
	c.cfg.Inject = false
	c.start()
	_, err := c.inject("msg-one", "body text\n")
	if ce := wantCode(t, err, CodeRefused); ce.Message != msgInjectOff {
		t.Fatalf("message %q", ce.Message)
	}
	if l := c.lastEvidence(); l["event"] != "inject-refused" || l["code"] != "inject-off" {
		t.Fatalf("line %v", l)
	}
}
