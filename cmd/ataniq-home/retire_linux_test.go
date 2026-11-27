package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRetireKeepsLedgerAndBlocksInstallation(t *testing.T) {
	f := newPilot(t)
	_, err := f.run(t, command{events: 1}, nil)
	must(t, err)
	ledger := filepath.Join(f.c.State, "events.v1.jsonl")
	before := read(t, ledger)
	binding := read(t, f.c.Binding)
	state := f.state(t)
	obs := read(t, filepath.Join(f.c.State, observationFile))
	// Retiring and reading history work offline, even after removing the peer cert.
	must(t, os.Rename(f.c.WitnessCert, f.c.WitnessCert+".saved"))
	for range 2 {
		if got := call(t, f.path, "retire"); got != "retired=true\n" {
			t.Fatal(got)
		}
	}
	marker := filepath.Join(filepath.Dir(f.c.Binding), retiredMarker)
	info, err := os.Lstat(marker)
	must(t, err)
	if info.Mode() != 0600 || info.Size() != 0 {
		t.Fatal(info)
	}
	if got := call(t, f.path, "status"); !strings.Contains(got, "ledger_head_sequence=1\nretired=true\nlast_observation=agree\n") {
		t.Fatal(got)
	}
	for _, args := range [][]string{{"serve"}, {"serve", "--check"}, {"observe"}, {"enroll", "prepare", "--out", f.path + ".out"}, {"enroll", "resume", "--out", f.path + ".out"}, {"enroll", "finalize", "--prepared", "none", "--receipt", "none"}} {
		_, diag := cli(t, 1, append(args, "--config", f.path)...)
		if !strings.Contains(diag, errRetired.Error()) {
			t.Fatal(args, diag)
		}
	}
	if !bytes.Equal(before, read(t, ledger)) || !bytes.Equal(binding, read(t, f.c.Binding)) || !bytes.Equal(obs, read(t, filepath.Join(f.c.State, observationFile))) || state != f.state(t) {
		t.Fatal("retire changed local or witness evidence")
	}
}

func TestRetireRespectsActiveHome(t *testing.T) {
	f := newPilot(t)
	guard, err := lockInstallation(f.c.Binding, false)
	must(t, err)
	_, diag := cli(t, 1, "retire", "--config", f.path)
	if !strings.Contains(diag, "home installation busy") {
		t.Fatal(diag)
	}
	present, err := exists(filepath.Join(filepath.Dir(f.c.Binding), retiredMarker))
	must(t, err)
	if present {
		t.Fatal("retired an active home")
	}
	must(t, guard.Close())
	call(t, f.path, "retire")
}

func TestRetirePreservesUnexpectedMarker(t *testing.T) {
	f := newPilot(t)
	target := filepath.Join(filepath.Dir(f.c.Binding), "unrelated")
	must(t, os.WriteFile(target, []byte("keep"), 0600))
	must(t, os.Symlink(target, filepath.Join(filepath.Dir(f.c.Binding), retiredMarker)))
	cli(t, 1, "retire", "--config", f.path)
	_, diag := cli(t, 1, "serve", "--check", "--config", f.path)
	if !strings.Contains(diag, "home retired") || string(read(t, target)) != "keep" {
		t.Fatal(diag)
	}
}

func TestStatusStrictObservation(t *testing.T) {
	f := newPilot(t)
	archive := f.path + ".archive"
	must(t, os.WriteFile(archive, []byte("sequence=0 head="+strings.Repeat("a", 64)+"\n"), 0600))
	_, err := f.run(t, command{events: 1, archive: archive}, nil)
	if err == nil {
		t.Fatal("archive mismatch accepted")
	}
	if got := call(t, f.path, "status"); !strings.Contains(got, "last_observation=DISAGREE home/archive, witness receipt/archive\n") {
		t.Fatal(got)
	}
	path := filepath.Join(f.c.State, observationFile)
	good := read(t, path)
	var o observation
	must(t, json.Unmarshal(good, &o))
	for _, bad := range [][]byte{append(bytes.Clone(good), '\n'), bytes.Replace(good, []byte(`"home":`), []byte(`"extra":false,"home":`), 1), bytes.Replace(good, []byte(`"result":`), []byte(`"result":"agree","result":`), 1), bytes.Replace(good, []byte(o.Result), []byte("agree"), 1)} {
		must(t, os.WriteFile(path, bad, 0600))
		out, diag := cli(t, 1, "status", "--config", f.path)
		if strings.Contains(out, "last_observation=") || !strings.Contains(diag, "observation.json") {
			t.Fatal(out, diag)
		}
	}
	must(t, os.WriteFile(path, good, 0600))
	must(t, os.Chmod(path, 0644))
	cli(t, 1, "status", "--config", f.path)
}

func TestRetireRequiresFreshRootAndIdentity(t *testing.T) {
	f := newPilot(t)
	call(t, f.path, "retire")
	var m map[string]any
	must(t, json.Unmarshal(read(t, f.path), &m))
	delete(m, "binding")
	writeConfig(t, f.path, m)
	_, diag := cli(t, 1, "serve", "--check", "--config", f.path)
	if !strings.Contains(diag, "home retired") {
		t.Fatal(diag)
	}
	fresh := filepath.Join(filepath.Dir(f.path), "epoch-two")
	must(t, os.Mkdir(fresh, 0700))
	m["state"] = filepath.Join(fresh, "state")
	m["binding"] = filepath.Join(fresh, "binding")
	m["epoch"] = "2"
	must(t, os.Mkdir(m["state"].(string), 0700))
	writeConfig(t, f.path, m)
	_, diag = cli(t, 1, "serve", "--check", "--config", f.path)
	if !strings.Contains(diag, "home retired") {
		t.Fatal("old seed admitted", diag)
	}
	m["home_key"], m["tls_dir"] = filepath.Join(fresh, "seed"), filepath.Join(fresh, "tls")
	writeConfig(t, f.path, m)
	call(t, f.path, "identity-init")
	cli(t, 0, "serve", "--check", "--config", f.path)
	cli(t, 0, "enroll", "prepare", "--config", f.path, "--out", filepath.Join(fresh, "prepared"))
	m["state"] = f.c.State
	writeConfig(t, f.path, m)
	_, diag = cli(t, 1, "serve", "--check", "--config", f.path)
	if !strings.Contains(diag, "home retired") {
		t.Fatal("old root admitted", diag)
	}
}
