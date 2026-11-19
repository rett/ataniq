//go:build linux && ataniq_canary

package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"ataniq/internal/leadholder/session"
)

// A start after a first one decides with the mark there. When the mark has
// gone before the launch, the launcher makes it again: it is there when the
// session starts, and the start does not prepare the folders.
func TestCanaryMarkMadeAgainWhenItWentAfterTheDecision(t *testing.T) {
	c := newCanaryRig(t)
	c.stopGrace = 200 * time.Millisecond
	digest := c.digest()
	mark := filepath.Join(c.state, leadHasRunMark)

	c.start(c.spec(digest).build())
	c.waitReady()
	c.stopAndWait()
	if _, err := os.Lstat(mark); err != nil {
		t.Fatalf("the first start left no mark: %v", err)
	}

	c.sys.reset()
	c.errbuf = &syncBuf{}
	c.fakeCgroup(true)
	c.sys.hook = map[string]func(){"OpenReport": func() { os.Remove(mark) }}
	d := c.deps()
	inner := d.canary.startSession
	var atStart bool
	d.canary.startSession = func(cfg session.Config) (*session.Session, error) {
		_, err := os.Lstat(mark)
		atStart = err == nil
		return inner(cfg)
	}
	c.done = make(chan int, 1)
	go func() { c.done <- run(c.spec(digest).build(), d) }()
	c.waitReady()
	if seen := c.sys.inputs(); len(seen) != 1 || seen[0].NoLeadHasRun {
		t.Errorf("the second start: %+v", seen)
	}
	if !atStart {
		t.Error("the mark that went after the decision was not made again before the session started")
	}
	c.stopAndWait()
}
