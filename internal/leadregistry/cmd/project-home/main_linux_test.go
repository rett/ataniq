package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"ataniq/internal/instanceid"
	"ataniq/internal/leadregistry"
)

func TestExplicitEnrollment(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	args := []string{"--state-dir", root, "--home", "self", "sample"}
	var out bytes.Buffer
	if err := enroll(args, &out); !errors.Is(err, instanceid.ErrMissing) {
		t.Fatal(err)
	}
	id, err := leadregistry.InitInstance(root, "registry", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := enroll(args, &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != "project=sample home_authority="+id.Authority+"\n" {
		t.Fatal(out.String())
	}
	path := filepath.Join(root, "registry", leadregistry.FileName)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := enroll(args, &out); err != nil {
		t.Fatal(err)
	}
	peer, err := leadregistry.InitInstance(filepath.Join(t.TempDir(), "peer"), "registry", "")
	if err != nil {
		t.Fatal(err)
	}
	args[3] = peer.Authority
	if err := enroll(args, &out); err == nil {
		t.Fatal("replaced enrollment")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("retry changed the journal")
	}
}
