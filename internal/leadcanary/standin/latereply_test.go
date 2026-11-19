//go:build linux && ataniq_canary

package standin

import (
	"testing"
	"time"

	"ataniq/internal/inject/osc7501"
)

// order.txt says "no-7501-reply" only when no reply ever came: a reply after
// the wait replaces it, with DA1 first when DA1 came first.
func TestReplayOrderFileTellsTheTruthAboutALateReply(t *testing.T) {
	for _, c := range []struct {
		name string
		// early is sent before the wait ends; late is sent after.
		early, late string
		want        string
	}{
		{"7501 only", "", osc7501.QueryReply, Order7501First},
		{"DA1, then 7501", da1Reply, osc7501.QueryReply, OrderDA1First},
		{"DA1 and no 7501", da1Reply, "", OrderNo7501Reply},
		{"nothing", "", "", OrderNo7501Reply},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newRepo(t)
			writeReplay(t, r, true)
			run := startRun(t, r, 100*time.Millisecond)
			run.pty.waitFor(t, 0, bothQueries)
			if c.early != "" {
				run.pty.send(t, c.early)
			}
			waitUntil(t, "order.txt", func() bool { return orderWritten(r) })
			if got := readOrder(t, r); got != OrderNo7501Reply+"\n" {
				t.Fatalf("order.txt %q after the wait, want no reply", got)
			}
			if c.late != "" {
				run.pty.send(t, c.late)
				run.pty.waitFor(t, 0, "\x1b]7501;"+sampleReport+"\x1b\\")
			} else {
				time.Sleep(200 * time.Millisecond)
			}
			if got := readOrder(t, r); got != c.want+"\n" {
				t.Fatalf("order.txt %q, want %q", got, c.want)
			}
			if err := run.finish(t); err != nil {
				t.Fatal(err)
			}
		})
	}
}
