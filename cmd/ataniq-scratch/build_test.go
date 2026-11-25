package main

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"ataniq/internal/leadholder/buildid"
)

// otherBuild is a build of another revision than testBuild.
var otherBuild = buildid.Identity{Module: "ataniq", Version: "v0.0.13-0.20261120010203-111111111111",
	Revision: "1111111111111111111111111111111111111111"}

// TestStartRefusesAHolderOfAnotherBuild: a holder binary of another build is
// refused by name before anything is made, taken or started, and the
// refusal names both builds.
func TestStartRefusesAHolderOfAnotherBuild(t *testing.T) {
	r := newRig(t, "demo")
	holder := r.e.holderFile(t)
	a, out, errout := r.e.newApp(r.fl)
	var asked string
	a.build.Executable = func(p string) (buildid.Identity, error) { asked = p; return otherBuild, nil }
	if code := a.run([]string{"start", "demo", "--holder", holder, "--", "/bin/agent"}); code != exitFail {
		t.Fatalf("exit %d %q", code, errout.String())
	}
	msg := errout.String()
	if !strings.HasPrefix(msg, "refused: holder-version-skew: the holder is another build") ||
		!strings.Contains(msg, "holder build "+otherBuild.Short()) || !strings.Contains(msg, "expected build "+testBuild.Short()) {
		t.Fatalf("refusal %q", msg)
	}
	if asked != holder || out.String() != "" || len(r.fl.runList()) != 0 {
		t.Fatalf("asked %q, out %q, runs %v", asked, out.String(), r.fl.runList())
	}
	// Nothing was made: no epoch was taken.
	if _, err := os.Stat(r.l.epochFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the epoch file: %v", err)
	}
}

// TestStartRefusesAHolderWhoseBuildCannotBeRead: the real reader finds no
// build in the stand-in script, and the start is refused by the same name.
func TestStartRefusesAHolderWhoseBuildCannotBeRead(t *testing.T) {
	r := newRig(t, "demo")
	holder := r.e.holderFile(t)
	a, _, errout := r.e.newApp(r.fl)
	a.build.Executable = nil // buildid.ReadExecutable
	if code := a.run([]string{"start", "demo", "--holder", holder, "--", "/bin/agent"}); code != exitFail {
		t.Fatalf("exit %d %q", code, errout.String())
	}
	if !strings.HasPrefix(errout.String(), "refused: holder-version-skew: the holder's build cannot be read") ||
		strings.Contains(errout.String(), holder) || len(r.fl.runList()) != 0 {
		t.Fatalf("refusal %q runs %v", errout.String(), r.fl.runList())
	}
}

// TestTicketChecksTheRunningHolder is the mixed-pair test of attach, through
// the real reader of holder.json: an old holder that records no build, a
// holder of another build, a state file of another epoch, and the same build
// of the epoch. Only the same build of the epoch gets a ticket; the others are
// refused by name and the holder is asked nothing.
func TestTicketChecksTheRunningHolder(t *testing.T) {
	epochRecord := func(epoch int, id buildid.Identity) string {
		return `{"version":1,"role":"scratch/tk","epoch":` + strconv.Itoa(epoch) + `,"scratch":true,"build":{"module":"` + id.Module +
			`","version":"` + id.Version + `","revision":"` + id.Revision + `"}}`
	}
	newRecord := func(id buildid.Identity) string { return epochRecord(1, id) }
	for _, c := range []struct {
		name, record, want string
	}{
		{"old holder, no build recorded", `{"version":1,"role":"scratch/tk","epoch":1,"scratch":true}`, "holder-version-skew: the holder records no build (an older holder)"},
		{"holder of another build", newRecord(otherBuild), "holder-version-skew: the holder is another build"},
		{"no state file", "", "holder-version-skew: the holder has no state file"},
		{"state file of the epoch before", epochRecord(0, testBuild), "stale-epoch: the holder's state file is of epoch 0, not of epoch 1"},
		{"same build", newRecord(testBuild), ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			e, l, h := started(t, "tk", 1)
			if err := os.MkdirAll(l.state, 0o700); err != nil {
				t.Fatal(err)
			}
			if c.record != "" {
				if err := os.WriteFile(filepath.Join(l.state, buildid.RecordFileName), []byte(c.record), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			a, out, errout := e.newApp(&fakeLauncher{})
			a.build.Record = nil // buildid.ReadRecord
			code := a.run([]string{"ticket", "tk"})
			if c.want == "" {
				if code != exitOK || len(h.requests()) != 1 {
					t.Fatalf("exit %d %q, requests %v", code, errout.String(), h.requests())
				}
				return
			}
			msg := errout.String()
			if code != exitFail || !strings.HasPrefix(msg, "refused: "+c.want) ||
				strings.HasPrefix(c.want, "holder-version-skew") && !strings.Contains(msg, "expected build "+testBuild.Short()) {
				t.Fatalf("exit %d %q", code, msg)
			}
			if len(h.requests()) != 0 || out.String() != "" {
				t.Fatalf("the holder was asked: %v, out %q", h.requests(), out.String())
			}
			if _, err := os.Stat(l.ticketFile); err == nil {
				t.Fatal("a ticket file was written")
			}
		})
	}
}

// TestStatusAndStopAreNotChecked: status and stop reach a holder of any
// build, so an old holder can always be looked at and stopped.
func TestStatusAndStopAreNotChecked(t *testing.T) {
	e, _, h := started(t, "tk", 1)
	a, _, errout := e.newApp(&fakeLauncher{})
	a.build.Record = func(string, uint64) (buildid.Record, error) { return buildid.Record{}, nil }
	a.build.Executable = func(string) (buildid.Identity, error) { return otherBuild, nil }
	if code := a.run([]string{"status", "tk"}); code != exitOK {
		t.Fatalf("status: %d %q", code, errout.String())
	}
	a, _, errout = e.newApp(&fakeLauncher{runFn: func([]string) (int, error) { return 3, nil }})
	a.build.Record = func(string, uint64) (buildid.Record, error) { return buildid.Record{}, nil }
	if code := a.run([]string{"stop", "tk"}); code != exitOK {
		t.Fatalf("stop: %d %q", code, errout.String())
	}
	if n := len(h.requests()); n != 2 {
		t.Fatalf("%d requests", n)
	}
}
