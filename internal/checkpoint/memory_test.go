package checkpoint_test

import (
	"crypto/ed25519"
	"testing"

	"ataniq/internal/checkpoint"
	"ataniq/internal/checkpoint/checkpointtest"
)

func TestMemoryContract(t *testing.T) {
	checkpointtest.Run(t, memoryHarness)
}

func memoryHarness(t *testing.T, key ed25519.PrivateKey) checkpointtest.Harness {
	t.Helper()
	h, _ := newMemoryHarness(t, key)
	return h
}

func newMemoryHarness(t *testing.T, key ed25519.PrivateKey) (checkpointtest.Harness, *checkpoint.Memory) {
	t.Helper()
	w, err := checkpoint.NewMemory("witness", 1, key)
	if err != nil {
		t.Fatal(err)
	}
	return harnessForMemory(w), w
}

func harnessForMemory(w *checkpoint.Memory) checkpointtest.Harness {
	return checkpointtest.Harness{
		Restart: func() checkpointtest.Harness { return harnessForMemory(w.ReopenForTest()) },
		Admin:   w.Administration(), Grant: w.Grant, Connect: w.Connect, Revoke: w.Revoke,
		Inhibit: w.Inhibit, LoseNextReply: w.LoseNextReply, SetReceiptLimit: w.SetReceiptLimit,
		TransformNextReply: w.TransformNextReply, RawSubmit: w.RawSubmit,
	}
}
