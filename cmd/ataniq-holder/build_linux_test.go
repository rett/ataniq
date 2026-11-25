//go:build linux

package main

import (
	"errors"
	"os"
	"strings"
	"syscall"
	"testing"

	"ataniq/internal/leadholder/buildid"
	"ataniq/internal/leadholder/eventlog"
)

// TestStateFileRecordsTheBuildThatRuns: holder.json and the start event of the
// event log record the build identity of the executable that runs, as
// /proc/<pid>/exe gives it, and a launcher reads it back from holder.json.
func TestStateFileRecordsTheBuildThatRuns(t *testing.T) {
	r := newRig(t)
	r.start(r.spec("raw; say UP; wait").build())
	r.waitReady()
	want, err := buildid.OfProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	st := r.readState()
	if st.Build != want || st.Build.Module != "ataniq" {
		t.Fatalf("holder.json build %+v, want %+v", st.Build, want)
	}
	rec, err := buildid.ReadRecord(r.state)
	if err != nil || rec.Build == nil || *rec.Build != want || rec.Epoch != testEpoch {
		t.Fatalf("read back %+v %v", rec, err)
	}
	r.sigc <- syscall.SIGTERM
	if code := r.wait(); code != exitOK {
		t.Fatalf("exit %d; stderr: %s", code, r.errbuf.String())
	}
	evs, err := eventlog.Read(r.state)
	if err != nil || len(evs) == 0 {
		t.Fatalf("event log: %v", err)
	}
	if evs[0].Event != eventlog.EventStart || evs[0].Build != want.Token() || evs[0].Build == "" {
		t.Fatalf("start event %+v, want build %q", evs[0], want.Token())
	}
}

// TestUnreadableOwnBuildIsRefused: a holder that cannot read its own build
// identity starts nothing and writes nothing.
func TestUnreadableOwnBuildIsRefused(t *testing.T) {
	r := newRig(t)
	r.build = func() (buildid.Identity, error) { return buildid.Identity{}, errors.New("no build information") }
	if code := r.runNow(r.spec("raw; say UP; wait").build()); code != exitRefuse {
		t.Fatalf("exit %d; stderr: %s", code, r.errbuf.String())
	}
	if !strings.Contains(r.errbuf.String(), "refused: cannot read this holder's own build identity") {
		t.Fatalf("stderr %q", r.errbuf.String())
	}
	nothingStarted(t, r)
}
