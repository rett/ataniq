package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	cp "ataniq/internal/checkpoint"
	"ataniq/internal/enrollment"
	"ataniq/internal/seatgate"
)

func ceremonyConfig(t *testing.T) (string, map[string]any, ed25519.PrivateKey, seatgate.Identity) {
	t.Helper()
	path, m, witness, tls := witnessConfig(t)
	base := filepath.Dir(path)
	m["state"], m["binding"] = base+"/state", base+"/binding"
	must(t, os.Mkdir(base+"/state", 0700))
	writeConfig(t, path, m)
	return path, m, witness, tls
}

func cli(t *testing.T, want int, args ...string) (string, string) {
	t.Helper()
	var out, diag bytes.Buffer
	code := run(args, &out, &diag, 1000)
	if code != want || (diag.Len() == 0) != (want == 0) {
		t.Fatalf("%s: exit %d, stdout %q, stderr %q", args[0], code, out.String(), diag.String())
	}
	return out.String(), diag.String()
}

func TestEnrollAndStatus(t *testing.T) {
	path, m, witness, _ := ceremonyConfig(t)
	dir := filepath.Dir(path)
	state := m["state"].(string)
	if got := call(t, path, "status"); got != "home_key=false tls_key=false tls_cert=false\nprepared=false binding=absent\nledger=absent\nretired=false\n" {
		t.Fatal(got)
	}
	call(t, path, "identity-init")
	c, err := loadConfig(path, fullPhase)
	must(t, err)
	prepared := dir + "/prepared.json"
	args := []string{"--config", path, "--out", prepared}
	out, _ := cli(t, 0, append([]string{"enroll", "prepare"}, args...)...)
	raw := read(t, prepared)
	p, err := enrollment.DecodePrepared(raw)
	must(t, err)
	if out != fmt.Sprintf("prepared_sha256=%x\n", sha256.Sum256(raw)) {
		t.Fatal(out)
	}
	fi, err := os.Lstat(prepared)
	must(t, err)
	if fi.Mode() != 0600 {
		t.Fatal(fi.Mode())
	}
	args[3] = dir + "/second"
	cli(t, 1, append([]string{"enroll", "prepare"}, args...)...)
	if _, err := os.Lstat(args[3]); !os.IsNotExist(err) {
		t.Fatalf("second prepare produced a file: %v", err)
	}
	must(t, os.WriteFile(args[3], []byte("existing"), 0600))
	cli(t, 1, append([]string{"enroll", "resume"}, args...)...)
	if string(read(t, args[3])) != "existing" {
		t.Fatal("resume overwrote its destination")
	}
	args[3] = dir + "/resumed.json"
	got, _ := cli(t, 0, append([]string{"enroll", "resume"}, args...)...)
	if got != out || !bytes.Equal(read(t, args[3]), raw) {
		t.Fatal("resume changed the request or digest")
	}
	before := names(t, state)
	got = call(t, path, "status")
	if got != "home_key=true tls_key=true tls_cert=true\nprepared=true binding=absent\nledger_head_sequence=0\nretired=false\n" || !slices.Equal(before, names(t, state)) {
		t.Fatal(got)
	}
	w, err := cp.NewMemory(c.Slot.Witness, c.Epoch, witness)
	must(t, err)
	home, err := loadHomeKey(c)
	must(t, err)
	must(t, w.Grant(c.Slot, home.Public().(ed25519.PublicKey), true))
	receipt, err := enrollment.Submit(t.Context(), w.Administration(), p)
	must(t, err)
	receiptPath := dir + "/receipt"
	args = []string{"enroll", "finalize", "--receipt", receiptPath, "--prepared", prepared, "--config", path}
	must(t, os.WriteFile(receiptPath, receipt[:len(receipt)-1], 0600))
	cli(t, 1, args...)
	must(t, os.WriteFile(receiptPath, receipt, 0600))
	m["epoch"] = "2"
	writeConfig(t, path, m)
	cli(t, 1, args...)
	m["epoch"] = "1"
	writeConfig(t, path, m)
	out, _ = cli(t, 0, args...)
	b, err := enrollment.ReadBinding(c.Binding)
	must(t, err)
	want := fmt.Sprintf("ledger_id=%s enrollment_id=%x receipt_digest=%s\n", b.Header.LedgerID, b.EnrollmentID, cp.ReceiptDigest(b.Receipt))
	if out != want {
		t.Fatal(out)
	}
	out, _ = cli(t, 0, args...)
	if out != want {
		t.Fatal(out)
	}
	if got := call(t, path, "status"); got != "home_key=true tls_key=true tls_cert=true\nprepared=false binding=valid\nledger_head_sequence=0\nretired=false\n" {
		t.Fatal(got)
	}

	// The binding must describe this configuration and this physical ledger.
	m["epoch"] = "2"
	writeConfig(t, path, m)
	got, _ = cli(t, 1, "status", "--config", path)
	if !strings.Contains(got, "binding=invalid") {
		t.Fatal(got)
	}
	m["epoch"] = "1"
	writeConfig(t, path, m)
	ledger := state + "/events.v1.jsonl"
	must(t, os.Rename(ledger, ledger+".saved"))
	must(t, os.WriteFile(ledger, read(t, ledger+".saved"), 0600))
	got, _ = cli(t, 1, "status", "--config", path)
	if !strings.Contains(got, "binding=invalid") {
		t.Fatal(got)
	}
}
