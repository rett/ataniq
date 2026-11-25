package main

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

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

// writeHolderRecord writes holder.json in the state folder of l as the holder
// of the epoch started with the launch intent writes it, recording build:
// what a holder reads from its own running executable.
func writeHolderRecord(t *testing.T, l layout, epoch uint64, intent string, build buildid.Identity) {
	t.Helper()
	if err := os.MkdirAll(l.state, 0o700); err != nil {
		t.Error(err)
		return
	}
	content := `{"version":1,"role":"scratch/` + l.name + `","epoch":` + strconv.FormatUint(epoch, 10) + `,"launch_intent":"` + intent +
		`","scratch":true,"build":{"module":"` + build.Module + `","version":"` + build.Version + `","revision":"` + build.Revision + `"}}`
	if err := os.WriteFile(filepath.Join(l.state, buildid.RecordFileName), []byte(content), 0o600); err != nil {
		t.Error(err)
	}
}

// stopCommand is what a refusal of a started unit names to stop it.
const stopCommand = "stop it with: systemctl --user stop ataniq-scratch-demo"

// TestStartChecksTheHolderThatStarted: the holder executable is the expected
// build when it is checked, and the holder that then starts (the file was
// replaced in between) records another build from its running executable. In
// unit mode the start is refused holder-version-skew and nothing says
// started, and the unit is not stopped: the refusal names the command that
// stops it. The holder of the expected build is counted started.
func TestStartChecksTheHolderThatStarted(t *testing.T) {
	for _, tc := range []struct {
		name     string
		recorded buildid.Identity
		ok       bool
	}{{"a holder rebuilt in between", otherBuild, false}, {"the checked build", testBuild, true}} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, "demo")
			holder := r.e.holderFile(t)
			start := r.fl.runFn
			r.fl.runFn = func(argv []string) (int, error) {
				if argv[0] == "systemd-run" {
					writeHolderRecord(t, r.l, 1, argAfter(argv, "--launch-intent"), tc.recorded)
				}
				return start(argv)
			}
			a, out, errout := r.e.newApp(r.fl)
			a.build.Record = nil // buildid.ReadRecord
			code := a.run([]string{"start", "demo", "--holder", holder, "--", "/bin/agent"})
			runs := r.fl.runList()
			if tc.ok {
				if code != exitOK || len(runs) != 1 || !strings.Contains(out.String(), "scratch agent started") {
					t.Fatalf("exit %d %q, runs %q", code, errout.String(), runs)
				}
				return
			}
			msg := errout.String()
			if code != exitFail || !strings.HasPrefix(msg, "refused: holder-version-skew: the holder is another build") ||
				!strings.Contains(msg, "holder build "+otherBuild.Short()) || !strings.Contains(msg, "expected build "+testBuild.Short()) ||
				!strings.Contains(msg, msgStartedSkew+"; its unit ataniq-scratch-demo is left running (epoch 1 is kept): "+stopCommand) {
				t.Fatalf("exit %d %q", code, msg)
			}
			if strings.Contains(out.String(), "started") {
				t.Fatalf("out %q", out.String())
			}
			if len(runs) != 1 {
				t.Fatalf("the unit was acted on: runs %q", runs)
			}
		})
	}
}

// TestStartDoesNotStopAHolderWhoseBuildIsNotConfirmed is the reviewer's case
// of a read that fails after the holder answered: a holder.json that cannot
// be read, or that is not of this launch, confirms nothing. The start is not
// counted started and says why, and the unit, which may be the expected
// build, is left running with the command that stops it.
func TestStartDoesNotStopAHolderWhoseBuildIsNotConfirmed(t *testing.T) {
	for name, content := range map[string]string{
		"an unreadable file": "garbage",
		"a file of another launch": `{"version":1,"role":"scratch/demo","epoch":1,"launch_intent":"old","scratch":true,"build":{"module":"` +
			otherBuild.Module + `","version":"` + otherBuild.Version + `","revision":"` + otherBuild.Revision + `"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, "demo")
			start := r.fl.runFn
			r.fl.runFn = func(argv []string) (int, error) {
				if argv[0] == "systemd-run" {
					if err := os.MkdirAll(r.l.state, 0o700); err != nil {
						t.Error(err)
					}
					if err := os.WriteFile(filepath.Join(r.l.state, buildid.RecordFileName), []byte(content), 0o600); err != nil {
						t.Error(err)
					}
				}
				return start(argv)
			}
			a, out, errout := r.e.newApp(r.fl)
			a.build.Record = nil // buildid.ReadRecord
			code := a.run([]string{"start", "demo", "--holder", r.e.holderFile(t), "--", "/bin/agent"})
			msg, runs := errout.String(), r.fl.runList()
			if code != exitFail || strings.Contains(out.String(), "started") || strings.Contains(msg, "refused:") ||
				!strings.Contains(msg, "the holder answered, but the build it started as cannot be confirmed") ||
				!strings.Contains(msg, "its unit ataniq-scratch-demo is left running (epoch 1 is kept): "+stopCommand) {
				t.Fatalf("exit %d %q out %q", code, msg, out.String())
			}
			if len(runs) != 1 {
				t.Fatalf("the unit was acted on: runs %q", runs)
			}
		})
	}
}

// TestStartDirectChecksTheHolderThatStarted: with --direct, a holder that
// records another build is refused holder-version-skew and, being this
// command's own child in its foreground, is ended as after any failed start,
// with its epoch given back.
func TestStartDirectChecksTheHolderThatStarted(t *testing.T) {
	r, ch := directRig(t, true, 0)
	start := r.fl.startFn
	r.fl.startFn = func(argv, env []string) (child, error) {
		writeHolderRecord(t, r.l, 1, argAfter(argv, "--launch-intent"), otherBuild)
		return start(argv, env)
	}
	a, out, errout := r.e.newApp(r.fl)
	a.build.Record = nil // buildid.ReadRecord
	code := a.run([]string{"start", "demo", "--direct", "--holder", r.e.holderFile(t), "--", "/bin/agent"})
	msg := errout.String()
	if code != exitFail || !strings.HasPrefix(msg, "refused: holder-version-skew: the holder is another build") ||
		!strings.Contains(msg, msgStartedSkew+"; it was ended") || strings.Contains(out.String(), "started") {
		t.Fatalf("exit %d %q out %q", code, msg, out.String())
	}
	ch.mu.Lock()
	signaled := append([]os.Signal(nil), ch.signaled...)
	ch.mu.Unlock()
	if len(signaled) != 1 || signaled[0] != syscall.SIGTERM {
		t.Fatalf("signals %v", signaled)
	}
	if r.epochFile() != "0\n" && r.epochFile() != "" {
		t.Fatalf("epoch file %q: the epoch was not given back", r.epochFile())
	}
}

// TestStartChecksTheHolderThatDidNotAnswer is the reviewer's case of the
// standalone launcher: a holder rebuilt between the check and the start
// writes its holder.json for this launch from its own running executable and
// never answers its status. The start does not end as a holder that did not
// answer: the file confirms another build, so the start is refused
// holder-version-skew, and the unit is left running with the command that
// stops it. A file of this launch that records the checked build, and no file
// at all, confirm nothing: the start fails as before, and the unit is left
// for the owner.
func TestStartChecksTheHolderThatDidNotAnswer(t *testing.T) {
	for _, tc := range []struct {
		name     string
		recorded *buildid.Identity
		skew     bool
	}{{"a holder rebuilt in between", &otherBuild, true}, {"the checked build", &testBuild, false}, {"no holder.json", nil, false}} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, "demo")
			r.fl.runFn = func(argv []string) (int, error) {
				if argv[0] == "systemd-run" && tc.recorded != nil {
					writeHolderRecord(t, r.l, 1, argAfter(argv, "--launch-intent"), *tc.recorded) // and nothing listens
				}
				return 0, nil
			}
			a, out, errout := r.e.newApp(r.fl)
			a.build.Record = nil // buildid.ReadRecord
			a.lim.startWait = 100 * time.Millisecond
			code := a.run([]string{"start", "demo", "--holder", r.e.holderFile(t), "--", "/bin/agent"})
			msg, runs := errout.String(), r.fl.runList()
			if code != exitFail || strings.Contains(out.String(), "started") || len(runs) != 1 {
				t.Fatalf("exit %d %q out %q runs %q", code, msg, out.String(), runs)
			}
			if !tc.skew {
				if strings.Contains(msg, "holder-version-skew") || !strings.Contains(msg, "did not answer within") {
					t.Fatalf("%q", msg)
				}
				return
			}
			if !strings.HasPrefix(msg, "refused: holder-version-skew: the holder is another build") ||
				!strings.Contains(msg, msgStartedSkew+" (it did not answer within 100ms); its unit ataniq-scratch-demo is left running (epoch 1 is kept): "+stopCommand) {
				t.Fatalf("%q", msg)
			}
		})
	}
}

// TestStartDoesNotTakeTheFileOfAnEarlierLaunchOfTheEpoch is the reviewer's
// two-launch case: a direct holder of another build writes its holder.json
// for epoch 1 and ends before it answers, so the start gives epoch 1 back.
// The next start, in unit mode, takes epoch 1 again with a launch intent of
// its own, and its holder does not answer in time. The file the first holder
// left is of the epoch but not of this launch: it confirms nothing, and the
// second start fails as a holder that did not answer, with no word of
// another build.
func TestStartDoesNotTakeTheFileOfAnEarlierLaunchOfTheEpoch(t *testing.T) {
	r := newRig(t, "demo")
	holder := r.e.holderFile(t)
	r.fl.startFn = func(argv, env []string) (child, error) {
		writeHolderRecord(t, r.l, 1, argAfter(argv, "--launch-intent"), otherBuild)
		ch := newFakeChild(1)
		ch.finish() // it ends before it answers
		return ch, nil
	}
	a, _, errout := r.e.newApp(r.fl)
	a.build.Record = nil // buildid.ReadRecord
	if code := a.run([]string{"start", "demo", "--direct", "--holder", holder, "--", "/bin/agent"}); code != exitFail ||
		!strings.Contains(errout.String(), "the holder ended before it answered") {
		t.Fatalf("the direct start: exit %d %q", code, errout.String())
	}
	if r.epochFile() != "0\n" && r.epochFile() != "" {
		t.Fatalf("epoch file %q: the epoch was not given back", r.epochFile())
	}
	r.fl.runFn = func([]string) (int, error) { return 0, nil } // nothing listens, nothing is written
	a, out, errout := r.e.newApp(r.fl)
	a.build.Record = nil // buildid.ReadRecord
	a.lim.startWait = 100 * time.Millisecond
	code := a.run([]string{"start", "demo", "--holder", holder, "--", "/bin/agent"})
	msg, runs := errout.String(), r.fl.runList()
	if code != exitFail || strings.Contains(out.String(), "started") || strings.Contains(msg, "holder-version-skew") ||
		!strings.Contains(msg, "did not answer within 100ms") || len(runs) != 1 || argAfter(runs[0], "--epoch") != "1" {
		t.Fatalf("the unit start: exit %d %q runs %q", code, msg, runs)
	}
}

// TestStartDirectChecksTheHolderThatDidNotAnswer: with --direct, a silent
// holder whose holder.json of this launch confirms another build is ended and
// the start is refused holder-version-skew, with its epoch given back.
func TestStartDirectChecksTheHolderThatDidNotAnswer(t *testing.T) {
	r, ch := directRig(t, false, -1)
	start := r.fl.startFn
	r.fl.startFn = func(argv, env []string) (child, error) {
		writeHolderRecord(t, r.l, 1, argAfter(argv, "--launch-intent"), otherBuild)
		return start(argv, env)
	}
	a, out, errout := r.e.newApp(r.fl)
	a.build.Record = nil // buildid.ReadRecord
	a.lim.startWait = 80 * time.Millisecond
	code := a.run([]string{"start", "demo", "--direct", "--holder", r.e.holderFile(t), "--", "/bin/agent"})
	msg := errout.String()
	if code != exitFail || !strings.HasPrefix(msg, "refused: holder-version-skew: the holder is another build") ||
		!strings.Contains(msg, msgStartedSkew+" (it did not answer within 80ms); it was ended") || strings.Contains(out.String(), "started") {
		t.Fatalf("exit %d %q out %q", code, msg, out.String())
	}
	ch.mu.Lock()
	signaled := append([]os.Signal(nil), ch.signaled...)
	ch.mu.Unlock()
	if len(signaled) == 0 || signaled[0] != syscall.SIGTERM {
		t.Fatalf("signals %v", signaled)
	}
	if r.epochFile() != "0\n" && r.epochFile() != "" {
		t.Fatalf("epoch file %q: the epoch was not given back", r.epochFile())
	}
}
