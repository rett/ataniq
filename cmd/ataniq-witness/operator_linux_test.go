package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"ataniq/internal/assignment"
	cp "ataniq/internal/checkpoint"
	"ataniq/internal/checkpoint/durable"
	"ataniq/internal/checkpoint/link"
	"ataniq/internal/seatgate"
)

func TestWitnessProvisionFromOperatorGuide(t *testing.T) {
	data, err := os.ReadFile("../../docs/authority-host-witness.md")
	must(t, err)
	_, example, ok := strings.Cut(string(data), "```json\n")
	if !ok {
		t.Fatal("missing configuration example")
	}
	example, _, ok = strings.Cut(example, "\n```")
	if !ok {
		t.Fatal("incomplete configuration example")
	}
	var c config
	must(t, seatgate.DecodeStrict([]byte(example), &c))
	if c.Guard != "/var/lib/authority-host" || c.State != "/var/lib/ataniq-witness/store" || c.Socket != "/run/ataniq-witness/owner.sock" {
		t.Fatalf("guide paths do not match the service: %s %s %s", c.Guard, c.State, c.Socket)
	}
	f, path := fixture(t)
	c.Guard, c.State, c.Seed, c.Socket = f.Guard, f.State, f.Seed, f.Socket
	c.ServiceTLS, c.OwnerTLS = f.ServiceTLS, f.OwnerTLS
	c.ServicePin, c.OwnerPin, c.HomePin = f.ServicePin, f.OwnerPin, f.HomePin
	c.SigningKey, c.HomeKey = f.SigningKey, f.HomeKey
	writeConfig(t, path, c)
	call(t, path, "init", 0)
	call(t, path, "check", 0)
	call(t, path, "init", 1)
}

func TestWitnessOfflinePublication(t *testing.T) {
	c, path := fixture(t)
	call(t, path, "init", 0)
	journal := filepath.Join(c.State, "journal.v1")
	staged := filepath.Join(c.State, ".create-fixture-journal.v1")
	original, err := os.ReadFile(journal)
	must(t, err)
	must(t, os.Rename(journal, staged))
	call(t, path, "run", 1)
	call(t, path, "init", 1)
	_, store, err := loadConfig(path)
	must(t, err)
	w, err := durable.OpenForRecovery(store)
	must(t, err)
	call(t, path, "complete-publication", 1) // Inspector still holds the lock.
	must(t, w.Close())
	call(t, path, "complete-publication", 0)
	call(t, path, "complete-publication", 1)
	got, err := os.ReadFile(journal)
	must(t, err)
	if !bytes.Equal(original, got) {
		t.Fatal("published different journal")
	}
	call(t, path, "check", 0)
}

func privateListener(t *testing.T, path string) *net.UnixListener {
	t.Helper()
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM) {
		t.Skipf("private Unix socket denied: %v", err)
	}
	must(t, err)
	return l
}

// Exercise the attended procedure with real pins and the two TLS links.
// Substitute a private Unix listener for the home transport.
func TestWitnessAttendedEnrollment(t *testing.T) {
	c, path := fixture(t)
	pub, homeKey, err := ed25519.GenerateKey(rand.Reader)
	must(t, err)
	_, sessionKey, err := ed25519.GenerateKey(rand.Reader)
	must(t, err)
	homeID, err := seatgate.WriteIdentityFiles(filepath.Join(filepath.Dir(path), "home-tls"), "fixture")
	must(t, err)
	c.HomePin, c.HomeKey = homeID.Fingerprint(), hex.EncodeToString(pub)
	writeConfig(t, path, c)
	call(t, path, "init", 0)
	_, store, err := loadConfig(path)
	must(t, err)
	w, err := durable.Open(store)
	must(t, err)
	defer w.Close()
	must(t, w.Grant(c.Slot, pub, true))
	id, err := loadTLS(c.ServiceTLS, c.ServicePin)
	must(t, err)
	cert := seatgate.ServerTLS(id, nil).Certificates[0].Certificate[0]
	home, err := link.NewEndpoint(homeID, cert, store.Pin, c.Identity.Epoch, c.Slot, link.TrustSources{netip.MustParsePrefix("192.0.2.1/32")})
	must(t, err)
	hl := privateListener(t, filepath.Join(filepath.Dir(path), "home.sock"))
	defer hl.Close()
	ol := privateListener(t, c.Socket)
	homeDone := make(chan error, 1)
	go func() { homeDone <- home.Serve(hl) }()
	defer func() {
		hl.Close()
		if err := <-homeDone; !errors.Is(err, net.ErrClosed) {
			t.Error(err)
		}
	}()

	server, err := link.NewServer(id, store.Pin, link.Remote{Network: "unix", Address: hl.Addr().String(), ServerPin: c.HomePin}, c.Slot)
	must(t, err)
	g := &guardedWitness{Witness: w, dir: c.Guard, turn: make(chan struct{}, 1)}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	done := make(chan error, 1)
	go func() { done <- serve(ctx, server, g, ol, c.OwnerPin, uint32(os.Geteuid())) }()
	defer func() { cancel(); must(t, <-done) }()

	owner, err := loadTLS(c.OwnerTLS, c.OwnerPin)
	must(t, err)
	remote := link.Remote{Network: "unix", Address: c.Socket, ServerPin: c.ServicePin, WitnessPin: store.Pin, Epoch: c.Identity.Epoch}
	admin, err := remote.Administration(owner)
	must(t, err)
	kid, err := cp.KeyID(pub)
	must(t, err)
	header := assignment.Header{Schema: 1, Authority: c.Slot.Authority, Store: c.Slot.Store, Incarnation: "first", LedgerID: "ledger", KeyID: kid}
	raw, err := header.Bytes()
	must(t, err)
	enrollment := cp.Enrollment{ID: cp.ID{1}, Slot: c.Slot, HeaderBytes: raw, HomePublicKey: pub}
	enrollment.ProofOfPossession, err = cp.ProveEnrollment(homeKey, enrollment)
	must(t, err)
	_, err = admin.Enroll(ctx, enrollment)
	must(t, err)
	cl, err := home.Connect(ctx, c.Slot, homeKey, sessionKey)
	must(t, err)
	q := cp.ReadRequest{Slot: c.Slot, Challenge: cp.ID{2}}
	v, err := cl.Read(ctx, q)
	must(t, err)
	must(t, os.WriteFile(filepath.Join(c.Guard, "decisions.inhibit"), nil, 0600))
	q.Challenge = cp.ID{3}
	inhibited, err := cl.Read(ctx, q)
	if !errors.Is(err, cp.ErrUnavailable) || !errors.Is(cp.VerifyView(store.Pin, q, inhibited), cp.ErrUnavailable) {
		t.Fatal("missing signed inhibited view", err)
	}
	retire := cp.Retirement{Mutation: cp.Mutation{ID: cp.ID{4}, Expected: v.Current}, DispositionDigest: cp.RequestDigest([]byte("owner disposition"))}
	if _, err = admin.Retire(ctx, retire); !errors.Is(err, cp.ErrUnavailable) {
		t.Fatal(err)
	}
	must(t, os.Remove(filepath.Join(c.Guard, "decisions.inhibit")))
	_, err = admin.Retire(ctx, retire)
	must(t, err)
	q.Challenge = cp.ID{5}
	v, err = admin.Read(ctx, q)
	must(t, err)
	if v.Current.Status != cp.Retired {
		t.Fatal("missing retirement tombstone")
	}
}
