package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	cp "ataniq/internal/checkpoint"
	"ataniq/internal/checkpoint/durable"
	"ataniq/internal/enrollment"
	"ataniq/internal/seatgate"
)

func prepareOnHome(t *testing.T, c config, home ed25519.PrivateKey, configPath string) (command, enrollment.Prepared, enrollment.FileRoot) {
	t.Helper()
	dir := filepath.Dir(configPath)
	root := enrollment.FileRoot{Path: filepath.Join(dir, "home-ledger")}
	must(t, os.Mkdir(root.Path, 0700))
	witness, err := hex.DecodeString(c.SigningKey)
	must(t, err)
	p, err := enrollment.Prepare(t.Context(), root, c.Slot, c.Identity.Epoch, home, witness, nil)
	must(t, err)
	cmd := command{op: "enroll-submit", configPath: configPath, prepared: filepath.Join(dir, "prepared.json"), receiptOut: filepath.Join(dir, "receipt.bin")}
	cmd.expectDigest, err = enrollment.WritePrepared(cmd.prepared, p)
	must(t, err)
	return cmd, p, root
}

func enrollmentFixture(t *testing.T) (config, durable.Config, command, enrollment.Prepared, enrollment.FileRoot) {
	t.Helper()
	c, configPath := fixture(t)
	pub, home, err := ed25519.GenerateKey(rand.Reader)
	must(t, err)
	c.HomeKey = hex.EncodeToString(pub)
	writeConfig(t, configPath, c)
	cmd, p, root := prepareOnHome(t, c, home, configPath)
	c, store, err := loadConfig(configPath)
	must(t, err)
	return c, store, cmd, p, root
}

// Exercise the complete submission against the real owner backend even on
// hosts that deny listen(2). Only the socket exchange is substituted.
func TestEnrollSubmitReceiptAndReplay(t *testing.T) {
	c, store, cmd, p, root := enrollmentFixture(t)
	w, err := durable.Create(store)
	must(t, err)
	defer w.Close()
	must(t, w.Grant(c.Slot, store.HomePin, true))
	admin := ownerAdmin{&guardedWitness{Witness: w, slot: c.Slot, dir: filepath.Join(filepath.Dir(cmd.configPath), guardDirectory), turn: make(chan struct{}, 1)}}
	// The client has no service TLS key, signing seed or state at these
	// paths. Its only private key on disk is the local owner's TLS key.
	c.Seed += "-absent"
	c.ServiceTLS += "-absent"
	c.State += "-absent"
	writeConfig(t, cmd.configPath, c)
	c, store, err = loadConfig(cmd.configPath)
	must(t, err)
	connect := func(c config, store durable.Config) (cp.RecoveryAdministration, error) {
		_, err := ownerAdministration(c, store)
		return admin, err
	}
	submit := func(cmd command) ([]byte, string) {
		t.Helper()
		var out bytes.Buffer
		must(t, enrollSubmit(t.Context(), c, store, cmd, &out, connect))
		b, err := os.ReadFile(cmd.receiptOut)
		must(t, err)
		st, err := os.Stat(cmd.receiptOut)
		must(t, err)
		if st.Mode() != 0600 {
			t.Fatalf("receipt mode %v", st.Mode())
		}
		return b, out.String()
	}
	first, firstOut := submit(cmd)
	cmd.receiptOut += "-replay"
	cmd.expectDigest = ""
	second, secondOut := submit(cmd)
	if !bytes.Equal(first, second) || firstOut != secondOut {
		t.Fatal("replay changed the receipt or directory status")
	}
	b, err := enrollment.Finalize(t.Context(), root, p, first, store.Pin)
	must(t, err)
	raw, err := os.ReadFile(cmd.prepared)
	must(t, err)
	var status bytes.Buffer
	must(t, printStatus(t.Context(), admin, store.Pin, c.Identity.Epoch, &status))
	want := fmt.Sprintf("prepared_digest=%x\nreceipt_digest=%s\n%s", sha256.Sum256(raw), cp.ReceiptDigest(b.Receipt), &status)
	if firstOut != want || !strings.Contains(status.String(), "sequence=2 ") || !strings.Contains(status.String(), "slots=1 receipts=1 ") {
		t.Fatal(firstOut)
	}
	// A competing publisher can create the name after the preflight.
	cmd.receiptOut += "-race"
	err = enrollSubmit(t.Context(), c, store, cmd, io.Discard, func(config, durable.Config) (cp.RecoveryAdministration, error) {
		must(t, os.WriteFile(cmd.receiptOut, []byte("existing"), 0600))
		return admin, nil
	})
	if !errors.Is(err, errReceiptExists) {
		t.Fatal(err)
	}
	got, err := os.ReadFile(cmd.receiptOut)
	must(t, err)
	if string(got) != "existing" {
		t.Fatal("overwrote competing receipt")
	}
	// A later slot mutation supersedes the live acknowledgement. An exact
	// replay still exports the historical receipt and reports today's status.
	view, err := admin.Read(t.Context(), cp.ReadRequest{Slot: c.Slot, Challenge: cp.ID{1}})
	must(t, err)
	_, err = admin.Retire(t.Context(), cp.Retirement{Mutation: cp.Mutation{ID: cp.ID{2}, Expected: view.Current}, DispositionDigest: cp.RequestDigest([]byte("retired by owner"))})
	must(t, err)
	cmd.receiptOut += "-historical"
	historical, snapshot := submit(cmd)
	if !bytes.Equal(first, historical) || !strings.Contains(snapshot, "sequence=3 ") || !strings.Contains(snapshot, "slots=1 receipts=2 ") {
		t.Fatal(snapshot)
	}
}

func TestEnrollSubmitPreflight(t *testing.T) {
	c, _, cmd, _, _ := enrollmentFixture(t)
	raw, err := os.ReadFile(cmd.prepared)
	must(t, err)
	other, _, err := ed25519.GenerateKey(rand.Reader)
	must(t, err)
	for _, tc := range []struct {
		name, diagnostic string
		change           func(*config, *command)
		raw              []byte
	}{
		{"owner comparison", "digest read on the home device", func(_ *config, cmd *command) { cmd.expectDigest = strings.Repeat("f", 64) }, nil},
		{"witness", "slot.witness", func(c *config, _ *command) { c.Slot.Witness, c.Identity.Witness = "other", "other" }, nil},
		{"authority", "slot.authority", func(c *config, _ *command) { c.Slot.Authority += "-other" }, nil},
		{"store", "slot.store", func(c *config, _ *command) { c.Slot.Store += "-other" }, nil},
		{"epoch", "epoch", func(c *config, _ *command) { c.Identity.Epoch++ }, nil},
		{"witness key", "witness_key", func(c *config, _ *command) { c.SigningKey = hex.EncodeToString(other) }, nil},
		{"home key", "home_public_key", func(c *config, _ *command) { c.HomeKey = hex.EncodeToString(other) }, nil},
		{"occupied", "receipt-out already exists", func(_ *config, cmd *command) { cmd.receiptOut = cmd.prepared }, nil},
		{"oversize", "192 KiB", nil, bytes.Repeat([]byte(" "), 192<<10+1)},
		{"newline", "invalid signed Prepared", nil, append(bytes.Clone(raw), '\n')},
		{"duplicate", "invalid signed Prepared", nil, bytes.Replace(raw, []byte(`"version":1`), []byte(`"version":1,"version":1`), 1)},
		{"version", "invalid signed Prepared", nil, bytes.Replace(raw, []byte(`"version":1`), []byte(`"version":2`), 1)},
		{"missing version", "invalid signed Prepared", nil, bytes.Replace(raw, []byte(`"version":1,`), nil, 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, cmd := c, cmd
			if tc.change != nil {
				tc.change(&c, &cmd)
			}
			if tc.raw != nil {
				cmd.prepared = filepath.Join(t.TempDir(), "prepared")
				must(t, os.WriteFile(cmd.prepared, tc.raw, 0600))
			}
			// With the owner's TLS identity absent, a specific preflight
			// diagnosis proves the CLI refused before opening the owner link.
			c.OwnerTLS += "-absent"
			writeConfig(t, cmd.configPath, c)
			args := []string{"enroll-submit", "--prepared", cmd.prepared, "--config", cmd.configPath, "--expect-digest", cmd.expectDigest, "--receipt-out", cmd.receiptOut}
			var out, diag bytes.Buffer
			if code := run(t.Context(), "/", args, &out, &diag); code != 1 || !strings.Contains(diag.String(), tc.diagnostic) {
				t.Fatalf("exit %d: %s", code, &diag)
			}
			if tc.raw == nil && out.String() != fmt.Sprintf("prepared_digest=%x\n", sha256.Sum256(raw)) || tc.raw != nil && out.Len() != 0 {
				t.Fatal(out.String())
			}
		})
	}
}

func TestEnrollSubmitRejectsKeyAndStoreTransferPaths(t *testing.T) {
	c, store, cmd, _, _ := enrollmentFixture(t)
	for _, name := range []string{c.Seed, filepath.Join(c.ServiceTLS, seatgate.KeyFile), filepath.Join(c.OwnerTLS, seatgate.KeyFile), filepath.Join(c.State, "journal.v1")} {
		for _, input := range []bool{true, false} {
			trial := cmd
			if input {
				trial.prepared = name
			} else {
				trial.receiptOut = name
			}
			err := enrollSubmit(t.Context(), c, store, trial, io.Discard, func(config, durable.Config) (cp.RecoveryAdministration, error) {
				t.Fatal("opened owner link")
				return nil, nil
			})
			if err == nil || !strings.Contains(err.Error(), "overlaps witness state or a private key") {
				t.Fatal(err)
			}
		}
	}
}

func TestEnrollSubmitUsage(t *testing.T) {
	line := "ataniq-witness enroll-submit --config FILE --prepared FILE --receipt-out FILE [--expect-digest HEX]"
	var out bytes.Buffer
	if run(t.Context(), "/", []string{"help"}, &out, io.Discard) != 0 || !strings.Contains(out.String(), line) {
		t.Fatal(out.String())
	}
	base := strings.Fields("enroll-submit --config config --prepared request --receipt-out receipt")
	for _, args := range [][]string{base[:3], base[:5], base[:6], append(base, "--config", "other"), append(base, "--expect-digest"), append(base, "--expect-digest", ""), append(base, "--extra", "value")} {
		if run(t.Context(), "/", args, io.Discard, io.Discard) != 2 {
			t.Fatalf("accepted invalid arguments %q", args)
		}
	}
}
