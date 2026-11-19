//go:build linux && ataniq_canary

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"ataniq/internal/leadholder/eventlog"
	"ataniq/internal/leadholder/sandboxstart"
)

// The counters add what the totals grew by, not the totals: a first copy and
// later ones, with payloads in between, end at the totals and no higher.
func TestCanaryCountersAddTheIncrease(t *testing.T) {
	c := newCanaryRig(t)
	digest := c.digest()
	c.script = "raw; print " + hexOSC("?") + "; sleep 300ms; print " + hexOSC("?") + "; print " + hexOSC("state=idle") +
		"; print " + hexOSC("?") + "; wait"
	c.start(c.spec(digest).build())
	c.waitReady()
	var got map[string]uint64
	desc := func() string { return "the counters of the first query" }
	poll(t, desc, func() bool {
		got = c.status().Counters
		return got[counterQueries] == 1
	})
	poll(t, func() string { return "the counters of the later payloads" }, func() bool {
		got = c.status().Counters
		return got[counterQueries] == 3 && got[counterReports] == 1
	})
	time.Sleep(10 * c.interval)
	if got := c.status().Counters; got[counterQueries] != 3 || got[counterReports] != 1 || got[counterInvalid] != 0 {
		t.Errorf("the counters after the copies: %v, want 3 queries and 1 report", got)
	}
	c.stopAndWait()
}

// The copy of the counters stops with the session.
func TestCanaryCounterPumpStopsWithTheSession(t *testing.T) {
	c := newCanaryRig(t)
	digest := c.digest()
	pumps := make(chan *counterPump, 1)
	d := c.deps()
	d.canary.pumpStarted = func(p *counterPump) { pumps <- p }
	c.done = make(chan int, 1)
	go func() { c.done <- run(c.spec(digest).build(), d) }()
	c.waitReady()
	pump := <-pumps
	select {
	case <-pump.stopped:
		t.Fatal("the pump stopped while the session ran")
	case <-time.After(5 * c.interval):
	}
	c.stopAndWait()
	select {
	case <-pump.stopped:
	case <-time.After(5 * time.Second):
		t.Error("the pump did not stop with the session")
	}
}

// The waiver is in the state file and the event log only when one was applied.
// (The start sequence always applies it in canary mode, as no record store is
// given, so the two functions are held directly.)
func TestStartedStateAndAnnouncementFollowTheWaiver(t *testing.T) {
	base := stateFile{Role: canaryRoleText}
	for _, tc := range []struct {
		name   string
		waived bool
	}{{"with a waiver", true}, {"without one", false}} {
		t.Run(tc.name, func(t *testing.T) {
			st := sandboxstart.Started{ProfileVersion: 3, ProfileDigest: "d", Notes: []string{"sample note"}}
			if tc.waived {
				st.Waived, st.Waiver = true, "sample waiver text"
			}
			file := startedState(base, st)
			if !file.SandboxStarted || file.ProfileDigest != "d" || file.ProfileVersion != 3 || len(file.StartNotes) != 1 {
				t.Errorf("state %+v", file)
			}
			if (file.Waiver != "") != tc.waived {
				t.Errorf("waiver %q with waived %v", file.Waiver, tc.waived)
			}

			dir := filepath.Join(t.TempDir(), "log")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			log, err := eventlog.Open(dir, eventlog.Options{Clock: time.Now})
			if err != nil {
				t.Fatal(err)
			}
			var buf bytes.Buffer
			announceStart(startContext{cfg: config{role: canaryRoleText}, out: holderOut{&buf}, log: log}, "1", st)
			if err := log.Close(); err != nil {
				t.Fatal(err)
			}
			b, _ := os.ReadFile(filepath.Join(dir, eventlog.FileName))
			if has := strings.Contains(string(b), "sandbox-started"); has != tc.waived {
				t.Errorf("event present %v with waived %v: %s", has, tc.waived, b)
			}
			if has := strings.Contains(buf.String(), "sample waiver text"); has != tc.waived {
				t.Errorf("waiver said %v with waived %v", has, tc.waived)
			}
			if !strings.Contains(buf.String(), "note of the start: sample note") {
				t.Errorf("the note was not said: %s", buf.String())
			}
		})
	}
}

// A stop signal cancels the sequence at once: the wait for the report line,
// which would last the whole time limit, ends with the signal.
func TestCanaryStopSignalCancelsTheStartPromptly(t *testing.T) {
	c := newCanaryRig(t)
	c.stopGrace = 200 * time.Millisecond
	c.in.TimeLimitSeconds = 60
	c.writeInputs()
	digest := c.digest()
	c.sys.block = true
	c.start(c.spec(digest).build())
	poll(t, func() string { return "the wait for the report line" }, func() bool { return c.sys.called("ReadLine") == 1 })
	begun := time.Now()
	c.sigc <- syscall.SIGTERM
	select {
	case code := <-c.done:
		if code != exitRefuse {
			t.Errorf("exit %d, want %d; stderr: %s", code, exitRefuse, c.errbuf.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the stop signal did not end the start")
	}
	if d := time.Since(begun); d > 10*time.Second {
		t.Errorf("the start ended %v after the signal", d)
	}
	c.noAgentNoClaim()
}

// A start that succeeded but whose signal came in the meantime ends with the
// session closed: the agent the sequence launched is gone, the lock is free.
func TestCanaryFailureAfterRunClosesTheSession(t *testing.T) {
	c := newCanaryRig(t)
	c.stopGrace = 200 * time.Millisecond
	digest := c.digest()
	// The last call of the sequence, with the child launched and every step
	// passed: the signal is taken by the holder before the sequence returns.
	c.sys.hook = map[string]func(){"Bound.Close": func() {
		c.sigc <- syscall.SIGTERM
		time.Sleep(300 * time.Millisecond)
	}}
	if code := c.runNow(c.spec(digest).build()); code != exitRefuse {
		t.Fatalf("exit %d, want %d; stderr: %s", code, exitRefuse, c.errbuf.String())
	}
	if !strings.Contains(c.errbuf.String(), "stopped by SIGTERM during the start") {
		t.Errorf("stderr: %s", c.errbuf.String())
	}
	if len(c.launches()) != 1 {
		t.Fatalf("%d launches", len(c.launches()))
	}
	c.noAgentNoClaim()
}
