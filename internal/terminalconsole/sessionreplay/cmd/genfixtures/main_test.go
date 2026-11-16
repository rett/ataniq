package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestRunWritesTheCommittedFixtures(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, "stale.jsonl")
	other := filepath.Join(dir, "notes.txt")
	for _, p := range []string{stale, other} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := run(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); err == nil {
		t.Error("stale recording kept")
	}
	if _, err := os.Stat(other); err != nil {
		t.Error("a file that is not a recording was removed")
	}
	committed, err := filepath.Glob(filepath.Join("..", "..", "testdata", "*.jsonl"))
	if err != nil || len(committed) == 0 {
		t.Fatalf("no committed fixtures (%v)", err)
	}
	for _, p := range committed {
		want, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(dir, filepath.Base(p)))
		if err != nil || !bytes.Equal(got, want) {
			t.Errorf("%s: differs from the committed file (%v)", filepath.Base(p), err)
		}
	}
	written, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if len(written) != len(committed) {
		t.Errorf("wrote %d recordings, %d committed", len(written), len(committed))
	}
}
