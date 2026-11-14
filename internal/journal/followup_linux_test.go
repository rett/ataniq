package journal

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"ataniq/internal/dispatch"
)

func TestRepeatedGrantConflictKeepsOneEvent(t *testing.T) {
	path, options := fixture(t)
	store := mustCreate(t, path, options)
	holder := testIntent()
	holder.ID = "holder"
	holder.Grant = "approval"
	owner := register(t, store, holder)
	if status, _, err := owner.Prepare(context.Background(), holder); err != nil || status != dispatch.PrepareNew {
		t.Fatal(status, err)
	}
	contender := cloneIntent(holder)
	contender.ID = "contender"
	attempt := register(t, store, contender)
	boundary := &fakeSink{}
	coordinator := dispatch.Coordinator{Journal: attempt, Boundary: boundary}
	for n := 0; n < 20; n++ {
		receipt, err := coordinator.Dispatch(context.Background(), contender)
		if err == nil || receipt.State != dispatch.Uncertain {
			t.Fatal(receipt, err)
		}
	}
	snapshot, err := store.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	binding, err := encodeIntent(holder)
	if err != nil {
		t.Fatal(err)
	}
	conflicts := 0
	for _, event := range snapshot.Events {
		if event.IntentID == contender.ID && event.Kind == "grant-conflict" {
			conflicts++
			if !bytes.Equal(event.Detail, binding) {
				t.Fatal("conflict evidence changed")
			}
		}
	}
	if conflicts != 1 || boundary.calls.Load() != 0 || boundary.effects.Load() != 0 {
		t.Fatalf("conflicts=%d calls=%d effects=%d", conflicts, boundary.calls.Load(), boundary.effects.Load())
	}
}

func TestRetiringReleasePreservesEntryError(t *testing.T) {
	path, options := fixture(t)
	store := mustCreate(t, path, options)
	attempt := register(t, store, testIntent())
	store.fault = failAt("release-before")
	if err := attempt.Release(context.Background()); err == nil {
		t.Fatal("missing injected release failure")
	}
	store.fault = nil
	store.gate <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := attempt.Release(ctx)
	<-store.gate
	if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrInactive) {
		t.Fatalf("lost entry error: %v", err)
	}
	if err := attempt.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	record := inspect(t, store, testIntent().ID)
	if record.Active || !record.Uncertain {
		t.Fatal("active unknown retirement was cleared unsafely", record)
	}
}

func TestClosePreservesCommittedRetirement(t *testing.T) {
	for _, state := range []dispatch.State{dispatch.Denied, dispatch.Started} {
		t.Run(string(state), func(t *testing.T) {
			path, options := fixture(t)
			store := mustCreate(t, path, options)
			intent := testIntent()
			attempt := register(t, store, intent)
			if status, _, err := attempt.Prepare(context.Background(), intent); err != nil || status != dispatch.PrepareNew {
				t.Fatal(status, err)
			}
			if err := attempt.Save(context.Background(), dispatch.Receipt{Intent: intent, ID: "start", State: state, Evidence: "proof"}); err != nil {
				t.Fatal(err)
			}
			store.fault = failAt("release-after")
			if err := attempt.Release(context.Background()); err == nil {
				t.Fatal("missing lost response")
			}
			store.fault = nil
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := Open(context.Background(), path, options)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			record := inspect(t, reopened, intent.ID)
			if record.Active || record.Uncertain != (state == dispatch.Started) || len(record.Receipts) != 1 {
				t.Fatal("committed retirement changed", record)
			}
			snapshot, err := reopened.Snapshot(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			for _, event := range snapshot.Events {
				if event.IntentID == intent.ID && event.Kind == "closed" {
					t.Fatal("closed event added to already retired generation")
				}
			}
		})
	}
}

func TestCloseInactiveRequiresExactProof(t *testing.T) {
	path, options := fixture(t)
	store, err := Create(context.Background(), path, options)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close() // The injected proof mismatch intentionally makes Close fail.
	intent := testIntent()
	attempt := register(t, store, intent)
	if status, _, err := attempt.Prepare(context.Background(), intent); err != nil || status != dispatch.PrepareNew {
		t.Fatal(status, err)
	}
	if err := attempt.Save(context.Background(), dispatch.Receipt{Intent: intent, ID: "start", State: dispatch.Denied}); err != nil {
		t.Fatal(err)
	}
	store.fault = failAt("release-after")
	if err := attempt.Release(context.Background()); err == nil {
		t.Fatal("missing lost response")
	}
	store.fault = nil
	if _, err := store.db.Exec("UPDATE attempts SET generation=? WHERE id=?", bytes.Repeat([]byte{1}, 16), []byte(intent.ID)); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); !errors.Is(err, ErrInactive) {
		t.Fatalf("mismatched proof accepted: %v", err)
	}
}
