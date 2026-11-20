//go:build linux && ataniq_canary

package leadcanary_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"ataniq/internal/leadcanary"
)

// A hard link to the config inside the state root would let the folder that
// the config governs hold the file, unseen by the check on the resolved path.
func TestReadConfigRefusesAFileWithAnotherLink(t *testing.T) {
	f := newFixture(t)
	cfg := f.config(t)
	good, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(f.conf, "canary.json")
	writeFile(t, p, good, 0o600)
	if _, err := leadcanary.ReadConfig(p); err != nil {
		t.Fatalf("the file with one link: %v", err)
	}
	inside := filepath.Join(f.state, "alias.json")
	if err := os.Link(p, inside); err != nil {
		t.Skipf("hard links are not available here: %v", err)
	}
	_, err = leadcanary.ReadConfig(p)
	wantConfigError(t, err, "file")
	// The link, not something else, is the cause.
	if err := os.Remove(inside); err != nil {
		t.Fatal(err)
	}
	if _, err := leadcanary.ReadConfig(p); err != nil {
		t.Fatalf("after the link was removed: %v", err)
	}
}

func TestOpenEvidenceRefusesAFileWithAnotherLink(t *testing.T) {
	f := newFixture(t)
	cfg := f.config(t)
	writeFile(t, cfg.EvidencePath(), nil, 0o600)
	other := filepath.Join(f.dir, "other.jsonl")
	if err := os.Link(cfg.EvidencePath(), other); err != nil {
		t.Skipf("hard links are not available here: %v", err)
	}
	if ev, err := leadcanary.OpenEvidence(cfg, nil); err == nil {
		ev.Close()
		t.Fatal("opened an evidence file with two links")
	}
	if err := os.Remove(other); err != nil {
		t.Fatal(err)
	}
	ev, err := leadcanary.OpenEvidence(cfg, nil)
	if err != nil {
		t.Fatalf("after the link was removed: %v", err)
	}
	ev.Close()
}
