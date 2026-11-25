package buildid

import (
	"errors"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
)

const (
	revOld = "1111111111111111111111111111111111111111"
	revNew = "2222222222222222222222222222222222222222"
)

// build returns the identity of a stamped build of revision rev.
func build(rev string, modified bool) Identity {
	v := "v0.0.14-0.20261124010203-" + rev[:12]
	if modified {
		v += "+dirty"
	}
	return Identity{Module: "ataniq", Version: v, Revision: rev, Modified: modified, GoVersion: "go1.27.2", Tags: "ataniq_canary"}
}

// writeRecord writes a holder's state file in a fresh folder and returns the
// folder.
func writeRecord(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, RecordFileName), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// oldRecord is holder.json as a holder built before build identities wrote it.
const oldRecord = `{"version":1,"role":"scratch/one","epoch":3,"launch_intent":"abc","runtime":"scratch","runtime_version":"0","profile_digest":"none","tree":"/t","pid":42,"pid_start_ticks":7,"started":"2026-11-20T10:00:00Z","cols":100,"rows":30,"agent":"/bin/sh","scratch":true}` + "\n"

// newRecord is holder.json of a holder that records its build.
func newRecord(id Identity) string {
	return `{"version":1,"role":"scratch/one","epoch":3,"pid":42,"scratch":true,"build":{"module":"` + id.Module +
		`","version":"` + id.Version + `","revision":"` + id.Revision + `","go":"go1.27.2","tags":"ataniq_canary"}}` + "\n"
}

// TestMixedPairs is the mixed-pair test: an old holder against a new launcher,
// a new holder against an old record, a new holder against an old launcher,
// and the same build, at launch and at attach. Each case is answered by name.
func TestMixedPairs(t *testing.T) {
	oldB, newB := build(revOld, false), build(revNew, false)
	exe := func(id Identity) func(string) (Identity, error) {
		return func(string) (Identity, error) { return id, nil }
	}
	for _, c := range []struct {
		name     string
		expected Identity
		launch   func(string) (Identity, error) // the executable's identity
		record   string                         // the running holder's state file
		why      string                         // "" is a match
	}{
		{"old holder against a new daemon", newB, exe(oldB), newRecord(oldB), WhyDiffer},
		{"new holder against an old record", newB, exe(newB), oldRecord, WhyNoRecord},
		{"new holder against an old daemon", oldB, exe(newB), newRecord(newB), WhyDiffer},
		{"same build", newB, exe(newB), newRecord(newB), ""},
	} {
		ch := &Checker{Expected: c.expected, Executable: c.launch}
		attach := ch.BeforeAttach(writeRecord(t, c.record))
		launch := ch.BeforeLaunch("/not/read")
		if c.why == "" {
			if attach != nil || launch != nil {
				t.Errorf("%s: attach %v, launch %v", c.name, attach, launch)
			}
			continue
		}
		var se *SkewError
		if !errors.As(attach, &se) || !errors.Is(attach, ErrSkew) || se.Why != c.why {
			t.Errorf("%s: attach %v, want %q", c.name, attach, c.why)
			continue
		}
		msg := attach.Error()
		if !strings.HasPrefix(msg, Reason+": ") || !strings.Contains(msg, "expected build "+c.expected.Short()) {
			t.Errorf("%s: %q does not name the reason and the expected build", c.name, msg)
		}
		if c.why == WhyNoRecord {
			if se.Holder != nil || !strings.Contains(msg, "holder build unknown") || !strings.Contains(msg, "older holder") {
				t.Errorf("%s: %q", c.name, msg)
			}
			// The old holder's executable, read at launch, is a build like
			// any other: here the new one, so the launch is not refused.
			if launch != nil {
				t.Errorf("%s: launch %v", c.name, launch)
			}
			continue
		}
		if !strings.Contains(msg, "holder build "+se.Holder.Short()) || se.Holder.Revision == c.expected.Revision {
			t.Errorf("%s: %q does not name the holder's build", c.name, msg)
		}
		if !errors.As(launch, &se) || se.Why != c.why {
			t.Errorf("%s: launch %v", c.name, launch)
		}
	}
}

// TestMatchRule: the module, the version, the revision and the modified flag
// are compared; the tags and the Go version are not; an unstamped build
// matches nothing, not even itself.
func TestMatchRule(t *testing.T) {
	b := build(revNew, false)
	same := b
	same.Tags, same.GoVersion = "ataniq_ownerchannel,ataniq_canary", "go1.28.0"
	if !Match(b, same) || Check(b, &same) != nil {
		t.Fatal("tags or the Go version are compared")
	}
	for name, mod := range map[string]func(*Identity){
		"module":   func(i *Identity) { i.Module = "other" },
		"version":  func(i *Identity) { i.Version += "x" },
		"revision": func(i *Identity) { i.Revision = revOld },
		"modified": func(i *Identity) { i.Modified = true },
	} {
		h := b
		mod(&h)
		if Match(b, h) {
			t.Errorf("a different %s matches", name)
		}
	}
	// The same revision, one modified: different builds.
	if Check(build(revNew, true), &b) == nil || Check(b, ptr(build(revNew, true))) == nil {
		t.Error("a modified build matches a clean one")
	}
	un := Identity{Module: "ataniq", Version: "(devel)"}
	var se *SkewError
	if err := Check(un, &un); !errors.As(err, &se) || se.Why != WhyUnstamped {
		t.Errorf("an unstamped build: %v", err)
	}
	if err := Check(b, &un); !errors.As(err, &se) || se.Why != WhyDiffer {
		t.Errorf("an unstamped holder: %v", err)
	}
}

func ptr(id Identity) *Identity { return &id }

// TestMessageBound: the message of the longest identities stays within 256
// bytes of printable ASCII without < > &, so a receipt can carry it.
func TestMessageBound(t *testing.T) {
	long := Identity{Module: strings.Repeat("m", MaxModule), Version: strings.Repeat("v", MaxVersion),
		Revision: strings.Repeat("a", MaxRevision), Modified: true}
	for _, why := range []string{WhyDiffer, WhyNoRecord, WhyUnstamped, WhyUnreadable, WhyNoStateFile} {
		e := &SkewError{Expected: long, Holder: &long, Why: why}
		msg := e.Error()
		if len(msg) > 256 || strings.ContainsAny(msg, "<>&") {
			t.Errorf("%d bytes: %q", len(msg), msg)
		}
		for _, r := range msg {
			if r < 0x20 || r > 0x7e {
				t.Errorf("%q has %q", msg, r)
			}
		}
	}
}

// TestFromBuildInfo reads the members from the build information and cleans
// the provenance.
func TestFromBuildInfo(t *testing.T) {
	bi := &debug.BuildInfo{GoVersion: "go1.27.2 X:boringcrypto", Main: debug.Module{Path: "ataniq", Version: "v0.0.14-0.20261124010203-222222222222+dirty"},
		Settings: []debug.BuildSetting{{Key: "-tags", Value: "ataniq_canary"}, {Key: "vcs.revision", Value: revNew}, {Key: "vcs.modified", Value: "true"}}}
	id, err := FromBuildInfo(bi)
	if err != nil {
		t.Fatal(err)
	}
	want := Identity{Module: "ataniq", Version: "v0.0.14-0.20261124010203-222222222222+dirty", Revision: revNew, Modified: true,
		GoVersion: "go1.27.2_X:boringcrypto", Tags: "ataniq_canary"}
	if id != want {
		t.Fatalf("%+v", id)
	}
	if got := id.Token(); got != "ataniq:v0.0.14-0.20261124010203-222222222222+dirty:rev="+revNew+":modified" {
		t.Fatalf("token %q", got)
	}
	if got := (Identity{Module: "ataniq", Version: "(devel)"}).Token(); got != "ataniq:_devel_:rev=none" {
		t.Fatalf("token %q", got)
	}
	for name, bad := range map[string]*debug.BuildInfo{
		"no main module":  {Main: debug.Module{Version: "v1"}},
		"no version":      {Main: debug.Module{Path: "ataniq"}},
		"odd revision":    {Main: debug.Module{Path: "ataniq", Version: "v1"}, Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "XYZ"}}},
		"spaced version":  {Main: debug.Module{Path: "ataniq", Version: "v1 two"}},
		"no information":  nil,
		"control in path": {Main: debug.Module{Path: "ata\nniq", Version: "v1"}},
	} {
		if _, err := FromBuildInfo(bad); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// TestReadsTheRunningExecutable: the identity read from this test binary's
// file, from /proc/<pid>/exe and from the runtime are one.
func TestReadsTheRunningExecutable(t *testing.T) {
	self, err := Self()
	if err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	fromFile, err := ReadExecutable(exe)
	if err != nil {
		t.Fatal(err)
	}
	fromProc, err := OfProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if fromFile != self || fromProc != self || self.Module != "ataniq" {
		t.Fatalf("self %+v file %+v proc %+v", self, fromFile, fromProc)
	}
	ch, err := NewChecker()
	if err != nil || ch.Expected != self {
		t.Fatalf("%+v %v", ch, err)
	}
	// A file that is not a Go executable has no identity.
	script := filepath.Join(t.TempDir(), "holder")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadExecutable(script); err == nil {
		t.Fatal("a script has an identity")
	}
	stamped := &Checker{Expected: build(revNew, false)}
	var se *SkewError
	if err := stamped.BeforeLaunch(script); !errors.As(err, &se) || se.Why != WhyUnreadable || se.Cause == nil {
		t.Fatalf("launch of a script: %v", err)
	}
	if err := stamped.BeforeLaunch(filepath.Join(t.TempDir(), "missing")); !errors.As(err, &se) || se.Why != WhyUnreadable {
		t.Fatalf("launch of a missing file: %v", err)
	}
	if _, err := OfProcess(0); err == nil {
		t.Fatal("pid 0 has an identity")
	}
}

// TestReadRecord reads the two members it needs and refuses what is not a
// holder's state file.
func TestReadRecord(t *testing.T) {
	b := build(revNew, false)
	rec, err := ReadRecord(writeRecord(t, newRecord(b)))
	if err != nil || rec.Epoch != 3 || rec.Build == nil || !Match(b, *rec.Build) {
		t.Fatalf("%+v %v", rec, err)
	}
	for name, content := range map[string]string{
		"old record": oldRecord,
		"null build": `{"epoch":2,"build":null}`,
	} {
		rec, err := ReadRecord(writeRecord(t, content))
		if err != nil || rec.Build != nil {
			t.Errorf("%s: %+v %v", name, rec, err)
		}
	}
	for name, content := range map[string]string{
		"not JSON":             "garbage",
		"an array":             "[1]",
		"a build of a string":  `{"build":"x"}`,
		"an unknown member":    `{"build":{"module":"ataniq","version":"v1","color":"red"}}`,
		"a bad revision":       `{"build":{"module":"ataniq","version":"v1","revision":"ZZ"}}`,
		"an oversized file":    `{"pad":"` + strings.Repeat("x", MaxRecordBytes) + `"}`,
		"a build without one":  `{"build":{}}`,
		"an epoch of a string": `{"epoch":"3"}`,
	} {
		if _, err := ReadRecord(writeRecord(t, content)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// A link in the place of the file is refused, and so is a folder.
	dir := t.TempDir()
	target := writeRecord(t, newRecord(b))
	if err := os.Symlink(filepath.Join(target, RecordFileName), filepath.Join(dir, RecordFileName)); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRecord(dir); err == nil {
		t.Error("a link was followed")
	}
	dir2 := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir2, RecordFileName), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRecord(dir2); err == nil {
		t.Error("a folder was read")
	}
	// No state file: the attach is refused, and says so.
	var se *SkewError
	if err := (&Checker{Expected: b}).BeforeAttach(t.TempDir()); !errors.As(err, &se) || se.Why != WhyNoStateFile {
		t.Errorf("no state file: %v", err)
	}
	if err := (&Checker{Expected: b}).BeforeAttach(writeRecord(t, "garbage")); !errors.As(err, &se) || se.Why != WhyUnreadable {
		t.Errorf("a bad state file: %v", err)
	}
}
