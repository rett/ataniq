package main

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	cp "ataniq/internal/checkpoint"
	"ataniq/internal/checkpoint/durable"
	"ataniq/internal/enrollment"
)

func TestOwnerSlotCeremony(t *testing.T) {
	c, path := fixture(t)
	var old *cp.Memory
	for _, epoch := range []uint64{1, 2} {
		c.Identity.Epoch = epoch
		pin, key, err := ed25519.GenerateKey(nil)
		must(t, err)
		homePin, home, err := ed25519.GenerateKey(nil)
		must(t, err)
		w, err := cp.NewMemory(c.Slot.Witness, epoch, key)
		must(t, err)
		must(t, w.Grant(c.Slot, homePin, true))
		root := enrollment.FileRoot{Path: t.TempDir()}
		must(t, os.Chmod(root.Path, 0700))
		admin := w.Administration()
		b, err := enrollment.Enroll(t.Context(), root, admin, c.Slot, epoch, home, pin, nil)
		must(t, err)
		view := func(w *cp.Memory) cp.State {
			t.Helper()
			v, err := w.Administration().Read(t.Context(), cp.ReadRequest{Slot: c.Slot, Challenge: cp.ID{1}})
			must(t, err)
			return v.Current
		}
		if epoch == 2 {
			if view(old).Status != cp.Retired || view(w).Status != cp.Active || b.Epoch != 2 {
				t.Fatal("replacement or tombstone missing")
			}
			return
		}
		old = w
		_, session, err := ed25519.GenerateKey(nil)
		must(t, err)
		client, err := w.Connect(c.Slot, home, session, pin)
		must(t, err)
		writer, err := enrollment.Start(t.Context(), root, b, client, session, nil)
		must(t, err)
		must(t, writer.Close())
		store := durable.Config{Pin: pin, HomePin: homePin}
		invoke := func(op string, extra ...string) (string, error) {
			t.Helper()
			cmd, ok := parseCommand(append([]string{op, "--config", path}, extra...))
			if !ok {
				t.Fatal("invalid usage", op, extra)
			}
			var out bytes.Buffer
			err := slotCommand(t.Context(), admin, c, store, cmd, &out)
			return out.String(), err
		}
		before := view(w)
		want := fmt.Sprintf("sequence=%d head=%s\n", before.Head.Sequence, before.Head.Digest)
		archive := filepath.Join(filepath.Dir(path), "archive")
		out, err := invoke("slot-read", "--archive-out", archive)
		must(t, err)
		raw, err := os.ReadFile(archive)
		must(t, err)
		st, err := os.Stat(archive)
		must(t, err)
		if out != want || string(raw) != want || st.Mode() != 0600 {
			t.Fatalf("archive %q / %q mode %v", out, raw, st.Mode())
		}
		must(t, os.WriteFile(archive, []byte("keep"), 0600))
		out, err = invoke("slot-read", "--archive-out", archive)
		if err == nil || out != "" {
			t.Fatal("replaced archive", out, err)
		}
		raw, err = os.ReadFile(archive)
		must(t, err)
		if string(raw) != "keep" {
			t.Fatal("archive changed")
		}

		c.Slot.Store += "-other"
		for _, op := range []string{"slot-read", "slot-release", "slot-retire"} {
			var extra []string
			if op != "slot-read" {
				extra = []string{"--asked", "fenced"}
			}
			if out, err := invoke(op, extra...); err == nil || out != "" {
				t.Fatal(op, out, err)
			}
		}
		c.Slot = before.Slot
		if view(w) != before {
			t.Fatal("mutated original slot after mismatch")
		}
		for _, op := range []string{"release", "retire"} {
			words := "writer fenced"
			out, err := invoke("slot-"+op, "--asked", words)
			must(t, err)
			after := view(w)
			if out != fmt.Sprintf("%s_receipt_digest=%s asked=%q\n", op, after.LastReceiptDigest, words) ||
				after.SessionKeyID != "" || (after.Status == cp.Retired) != (op == "retire") {
				t.Fatal(out, after)
			}
		}
	}
}

func TestOwnerSlotGuardUsesServiceSlot(t *testing.T) {
	// Even a different enrolled slot on this witness is outside this service.
	a := ownerAdmin{&guardedWitness{slot: cp.Slot{Store: "live"}}}
	m := cp.Mutation{Expected: cp.State{Slot: cp.Slot{Store: "other"}}}
	_, read := a.Read(t.Context(), cp.ReadRequest{Slot: m.Expected.Slot})
	_, release := a.Release(t.Context(), m)
	_, retire := a.Retire(t.Context(), cp.Retirement{Mutation: m})
	for _, err := range []error{read, release, retire} {
		var reason *cp.Reason
		if !errors.As(err, &reason) || reason.Kind != cp.Unauthorized {
			t.Fatal(err)
		}
	}
}

func TestSlotUsage(t *testing.T) {
	for _, args := range []string{"slot-release --config file", "slot-retire --config file --asked",
		"slot-read --config file --asked words", "slot-read --config file --archive-out file --archive-out copy",
		"slot-retire --config file --archive-out file", "slot-release --asked fenced"} {
		if _, ok := parseCommand(strings.Fields(args)); ok {
			t.Fatal(args)
		}
	}
}
