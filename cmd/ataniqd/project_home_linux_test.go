package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ataniq/internal/instanceid"
	"ataniq/internal/leaddaemon"
	"ataniq/internal/leadregistry"
)

func TestProjectHomeEnrollment(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	args := []string{"project-home", "--state-dir", root, "--home", "self", "sample"}
	ta := newTestApp(t, nil)
	if code := ta.run(args); code != leaddaemon.ExitState || !strings.Contains(ta.err.String(), instanceid.ErrMissing.Error()) {
		t.Fatalf("missing identity: exit %d: %s", code, ta.err.String())
	}
	id, err := leadregistry.InitInstance(root, leadregistry.DirName, "")
	if err != nil {
		t.Fatal(err)
	}
	if code := ta.run(args); code != 0 {
		t.Fatalf("enroll: exit %d: %s", code, ta.err.String())
	}
	if ta.out.String() != "project=sample home_authority="+id.Authority+"\n" {
		t.Fatal(ta.out.String())
	}
	path := filepath.Join(root, leadregistry.DirName, leadregistry.FileName)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if code := ta.run(args); code != 0 {
		t.Fatalf("enroll: exit %d: %s", code, ta.err.String())
	}
	peer, err := leadregistry.InitInstance(filepath.Join(t.TempDir(), "peer"), leadregistry.DirName, "")
	if err != nil {
		t.Fatal(err)
	}
	args[4] = peer.Authority
	if code := ta.run(args); code != leaddaemon.ExitState || !strings.Contains(ta.err.String(), "different assignment") {
		t.Fatalf("conflicting enrollment: exit %d: %s", code, ta.err.String())
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("retry changed the journal")
	}
}

func TestProjectHomeExecutorConfirmation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	id, err := leadregistry.InitInstance(root, leadregistry.DirName, "")
	if err != nil {
		t.Fatal(err)
	}
	peer := strings.Repeat("a", 64)
	ta := newTestApp(t, nil)
	enroll := func(home string, flags ...string) int {
		ta.err.Reset()
		args := append([]string{"project-home", "--state-dir", root, "--home", home}, flags...)
		return ta.run(append(args, "sample"))
	}
	if code := enroll(peer); code != 0 {
		t.Fatal(ta.err.String())
	}
	for _, home := range []string{"self", id.Authority} {
		if code := enroll(home); code != leaddaemon.ExitState || !strings.Contains(ta.err.String(), "--confirm-self") || !strings.Contains(ta.err.String(), peer) {
			t.Fatalf("unconfirmed claim: exit %d: %s", code, ta.err.String())
		}
	}
	if code := enroll("self", "--confirm-self"); code != leaddaemon.ExitState || !strings.Contains(ta.err.String(), "different assignment") {
		t.Fatalf("confirmation alone replaced a home: exit %d: %s", code, ta.err.String())
	}
}

func TestProjectHomeUsage(t *testing.T) {
	for _, args := range [][]string{
		{}, {"--home", "self", "sample"}, {"--state-dir", "/sample", "sample"},
		{"--state-dir", "/sample", "--home", "self", "sample/lead"},
		{"--state-dir", "/sample", "--home", "self", "sample", "extra"},
	} {
		ta := newTestApp(t, nil)
		if code := ta.run(append([]string{"project-home"}, args...)); code != leaddaemon.ExitUsage || !strings.Contains(ta.err.String(), projectHomeUsage) {
			t.Errorf("%v: exit %d: %s", args, code, ta.err.String())
		}
	}
}
