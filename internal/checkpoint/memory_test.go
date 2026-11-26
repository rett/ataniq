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
	return checkpointtest.Harness{Admin: w.Administration(), TransformNextReply: w.TransformNextReply, Grant: w.Grant, SetReceiptLimit: w.SetReceiptLimit, Connect: w.Connect, Revoke: w.Revoke, Inhibit: w.Inhibit, LoseNextReply: w.LoseNextReply}, w
}
