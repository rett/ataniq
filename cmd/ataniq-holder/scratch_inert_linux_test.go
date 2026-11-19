//go:build linux

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ataniq/internal/leadholder/wire"
)

// Scratch mode, in either build, never waives an assertion and never opens
// injection: the state file has no field of canary mode, the event log has no
// entry of a started sandbox, and inject answers disabled.
func TestScratchModeNeverWaivesAndNeverInjects(t *testing.T) {
	r := newRig(t)
	r.start(r.spec("say READY; wait").build())
	r.waitReady()
	raw, err := os.ReadFile(filepath.Join(r.state, StateFileName))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"waiver", "canary", "sandbox_started", "profile_version", "start_notes"} {
		if bytes.Contains(raw, []byte(`"`+key+`"`)) {
			t.Errorf("the state file of scratch mode has the key %q: %s", key, raw)
		}
	}
	if !bytes.Contains(raw, []byte(`"scratch":true`)) {
		t.Errorf("the state file does not say scratch: %s", raw)
	}
	for _, req := range []wire.Request{
		{Op: wire.OpInject, Epoch: testEpoch},
		{Op: wire.OpInject, Epoch: testEpoch, MessageID: "m1", Text: "[ataniq-msg id=m1 from=sample]\nhello"},
	} {
		if resp := r.mustControl(req); resp.OK || resp.Code != wire.CodeDisabled || resp.Inject != nil {
			t.Errorf("inject in scratch mode: %+v, want %s", resp, wire.CodeDisabled)
		}
	}
	if resp := r.mustControl(wire.Request{Op: wire.OpStop, Epoch: testEpoch}); !resp.OK {
		t.Fatalf("stop: %+v", resp)
	}
	if code := r.wait(); code != exitOK {
		t.Fatalf("exit %d; stderr: %s", code, r.errbuf.String())
	}
	log := string(r.logBytes())
	for _, word := range []string{"sandbox-started", "assertion-10-waived", "waive"} {
		if strings.Contains(log, word) {
			t.Errorf("the event log of scratch mode says %q: %s", word, log)
		}
	}
	if strings.Contains(r.errbuf.String(), "waive") {
		t.Errorf("stderr of scratch mode: %s", r.errbuf.String())
	}
	ents, _ := os.ReadDir(r.state)
	for _, e := range ents {
		if e.Name() == "lead-has-run" {
			t.Error("scratch mode wrote the mark of canary mode")
		}
	}
}

// --version prints the name, the version and, in a build with the tag, the
// tag; it starts nothing.
func TestVersion(t *testing.T) {
	r := newRig(t)
	var out syncBuf
	d := r.deps()
	d.stdout = &out
	if code := run([]string{"--version"}, d); code != exitOK {
		t.Fatalf("exit %d; stderr: %s", code, r.errbuf.String())
	}
	if got, want := out.String(), "ataniq-holder "+version+versionSuffix+"\n"; got != want {
		t.Errorf("--version prints %q, want %q", got, want)
	}
	if r.errbuf.String() != "" {
		t.Errorf("stderr: %s", r.errbuf.String())
	}
	nothingStarted(t, r)
	// With anything else on the line it is not the version request.
	if code := r.runNow([]string{"--version", "x"}); code != exitRefuse {
		t.Errorf("--version x: exit %d", code)
	}
}
