package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	cp "ataniq/internal/checkpoint"
	"ataniq/internal/checkpoint/durable"
	"ataniq/internal/seatgate"
)

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func fixture(t *testing.T) (config, string) {
	t.Helper()
	dir := t.TempDir()
	c := config{Identity: durable.Identity{Witness: "witness", Store: "directory", Incarnation: "first", Epoch: 7},
		Slot:  cp.Slot{Witness: "witness", Authority: "authority", Store: "ledger"},
		State: filepath.Join(dir, "state"), Seed: filepath.Join(dir, "seed"), Guard: filepath.Join(dir, "guard"),
		Socket: filepath.Join(dir, "admin.sock"), ServiceTLS: filepath.Join(dir, "tls"), OwnerTLS: filepath.Join(dir, "owner"),
		Home: "192.0.2.4:7443", HomePin: strings.Repeat("a", 64)}
	must(t, os.Mkdir(c.State, 0700))
	must(t, os.Mkdir(c.Guard, 0755))
	service, err := seatgate.WriteIdentityFiles(c.ServiceTLS, "witness")
	must(t, err)
	owner, err := seatgate.WriteIdentityFiles(c.OwnerTLS, "owner")
	must(t, err)
	c.ServicePin, c.OwnerPin = service.Fingerprint(), owner.Fingerprint()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	must(t, err)
	c.SigningKey = hex.EncodeToString(pub)
	must(t, os.WriteFile(c.Seed, key.Seed(), 0600))
	pub, _, err = ed25519.GenerateKey(rand.Reader)
	must(t, err)
	c.HomeKey = hex.EncodeToString(pub)
	path := filepath.Join(dir, "config")
	writeConfig(t, path, c)
	return c, path
}
func writeConfig(t *testing.T, path string, c config) {
	t.Helper()
	b, err := json.Marshal(c)
	must(t, err)
	must(t, os.WriteFile(path, b, 0600))
}
func call(t *testing.T, path, op string, want int) string {
	t.Helper()
	var b bytes.Buffer
	if code := run(t.Context(), []string{op, "--config", path}, &b, &b); code != want {
		t.Fatalf("%s: exit %d, %s", op, code, &b)
	}
	return b.String()
}

func TestWitnessInitAndReadOnlyCheck(t *testing.T) {
	c, path := fixture(t)
	call(t, path, "check", 1)
	call(t, path, "init", 0)
	call(t, path, "init", 1)
	_, store, err := loadConfig(path)
	must(t, err)
	w, err := durable.Open(store)
	must(t, err)
	defer w.Close()
	before, err := os.Stat(filepath.Join(c.State, "journal.v1"))
	must(t, err)
	if got := call(t, path, "check", 0); got != "witness check ok\n" {
		t.Fatal(got)
	}
	after, err := os.Stat(filepath.Join(c.State, "journal.v1"))
	must(t, err)
	if before.ModTime() != after.ModTime() {
		t.Fatal("check wrote journal")
	}
	must(t, os.Chmod(c.Seed, 0640))
	call(t, path, "check", 1)
	must(t, os.Chmod(c.Seed, 0600))
	c.SigningKey = c.HomeKey
	writeConfig(t, path, c)
	call(t, path, "check", 1)
}

func TestWitnessRefusesUnsafeInputs(t *testing.T) {
	c, path := fixture(t)
	b, err := os.ReadFile(path)
	must(t, err)
	for _, extra := range []string{`,"Socket":"private-value"}`, `,"socket":"private-value"}`, `,"extra":"private-value"}`, `} {}`} {
		must(t, os.WriteFile(path, append(bytes.Clone(b[:len(b)-1]), extra...), 0600))
		got := call(t, path, "init", 1)
		if strings.Contains(got, "private-value") || strings.Contains(got, c.HomePin) {
			t.Fatal("diagnostic exposed input")
		}
	}
	c.Socket = "tcp://192.0.2.5:80"
	writeConfig(t, path, c)
	for _, op := range []string{"status", "acknowledge", "run"} {
		call(t, path, op, 1)
	}
	c.Socket = filepath.Join(c.State, "sock")
	writeConfig(t, path, c)
	must(t, os.Chmod(c.State, 0750))
	call(t, path, "init", 1)
	must(t, os.Chmod(c.State, 0700))
	must(t, os.Link(c.Seed, c.Seed+"-alias"))
	call(t, path, "init", 1)
}

func TestWitnessGuardChangesBetweenRequests(t *testing.T) {
	c, path := fixture(t)
	_, store, err := loadConfig(path)
	must(t, err)
	w, err := durable.Create(store)
	must(t, err)
	defer w.Close()
	g := &guardedWitness{Witness: w, dir: c.Guard, turn: make(chan struct{}, 1)}
	must(t, w.Grant(c.Slot, store.HomePin, true))
	a := ownerAdmin{g}
	ctx := t.Context()
	guard := filepath.Join(c.Guard, "decisions.inhibit")
	must(t, os.WriteFile(guard, nil, 0600))
	_, err = g.SettleHome(ctx, 7, c.Slot, cp.ID{1}, store.HomePin)
	if !errors.Is(err, cp.ErrUnavailable) {
		t.Fatal(err)
	}
	_, err = a.Settle(ctx, 7, c.Slot, cp.ID{2})
	if !errors.Is(err, cp.ErrUnavailable) {
		t.Fatal(err)
	}
	if !errors.Is(a.AcknowledgeStorageError(ctx), cp.ErrUnavailable) {
		t.Fatal("acknowledged during switch")
	}
	must(t, os.Remove(guard))
	_, err = a.Settle(ctx, 7, c.Slot, cp.ID{2})
	if !errors.Is(err, cp.ErrNotApplied) {
		t.Fatal(err)
	}
	must(t, os.Remove(c.Guard))
	r, err := a.Status(ctx, cp.ID{4})
	must(t, err)
	st, err := cp.VerifyDirectoryStatus(store.Pin, 7, cp.ID{4}, r)
	must(t, err)
	if st.Inhibition != cp.ServiceInhibited || st.Fences != 1 {
		t.Fatal(st)
	}
}

func TestWitnessOwnerSocketRecovery(t *testing.T) {
	c, path := fixture(t)
	call(t, path, "init", 0)
	marker := filepath.Join(c.State, "poisoned")
	must(t, os.WriteFile(marker, nil, 0600))
	call(t, path, "run", 1)
	_, store, err := loadConfig(path)
	must(t, err)
	w, err := durable.OpenForRecovery(store)
	must(t, err)
	defer w.Close()
	server, err := serviceConfig(c, store)
	must(t, err)
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: c.Socket, Net: "unix"})
	if errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM) {
		t.Skipf("private Unix socket denied: %v", err)
	}
	must(t, err)
	g := &guardedWitness{Witness: w, dir: c.Guard, recoveryOnly: true, turn: make(chan struct{}, 1)}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, server, g, ln, c.OwnerPin, uint32(os.Geteuid())) }()
	defer func() { cancel(); must(t, <-done) }()
	if got := call(t, path, "status", 1); !strings.Contains(got, `inhibition="storage error"`) {
		t.Fatal(got)
	}
	call(t, path, "acknowledge", 0)
	if got := call(t, path, "status", 0); !strings.Contains(got, `inhibition="service inhibited"`) {
		t.Fatal(got)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}
