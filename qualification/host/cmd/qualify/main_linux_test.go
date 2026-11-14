//go:build linux && amd64

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestNoImplicitHostOperations(t *testing.T) {
	for _, args := range [][]string{nil, {"run"}, {"run", "--config", "/absent"}, {"reconcile", "--campaign", "/absent", "--attempt", "0123456789abcdef0123456789abcdef"}, {"run", "--allow-host-operations", "--config", "relative"}, {"private-inject", "relative"}, {"inspect", "--allow-host-operations", "--campaign", "/absent"}} {
		var b bytes.Buffer
		if run(args, &b) == 0 {
			t.Fatalf("accepted %q", args)
		}
		if b.Len() != 0 {
			t.Fatal("rejected input echoed")
		}
	}
}

func TestR1EntryUsesNormalExplicitRunAdmission(t *testing.T) {
	path := t.TempDir()
	config := map[string]any{
		"version": 1, "campaign": filepath.Join(path, "campaign"),
		"driver": filepath.Join(path, "driver"), "probe": filepath.Join(path, "probe"),
		"bwrap": filepath.Join(path, "bwrap"), "bpf": filepath.Join(path, "bpf"),
		"systemctl": filepath.Join(path, "systemctl"), "systemd_run": filepath.Join(path, "systemd-run"),
		"busctl": filepath.Join(path, "busctl"), "role": "candidate", "role_id": "0011223344556677",
		"run_id": "0123456789abcdef", "request_id": "fedcba9876543210", "case": "r1-entry", "reviewed_bpf": true,
	}
	b, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(path, "config.json")
	if err := os.WriteFile(file, b, 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if code := run([]string{"run", "--config", file}, &out); code != 2 || out.Len() != 0 {
		t.Fatal("implicit entry operation admitted")
	}
	// The normal decoder admits the fixed profile, then fails at exact running
	// driver identity before campaign open or any host operation.
	if code := run([]string{"run", "--allow-host-operations", "--config", file}, &out); code != 4 || !bytes.Contains(out.Bytes(), []byte(`"stage":"driver-identity"`)) {
		t.Fatal(code, out.String())
	}
}
