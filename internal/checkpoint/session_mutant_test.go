package checkpoint_test

import (
	"context"
	"testing"

	cp "ataniq/internal/checkpoint"
	"ataniq/internal/checkpoint/checkpointtest"
)

type claimClient struct {
	cp.Client
	claim func(context.Context, cp.Claim) (cp.Receipt, error)
}

func (c claimClient) Claim(ctx context.Context, q cp.Claim) (cp.Receipt, error) {
	return c.claim(ctx, q)
}

func TestLastClaimOnlyMutant(t *testing.T) {
	rejectMemoryMutant(t, "session_claim_history", "got <nil>, want checkpoint: stale", func(h *checkpointtest.Harness, w *cp.Memory) {
		wrapConnect(h, func(c cp.Client) cp.Client {
			return claimClient{c, func(ctx context.Context, q cp.Claim) (cp.Receipt, error) {
				r, err := c.Claim(ctx, q)
				if err == nil {
					w.KeepLastClaim(q.Expected.Slot)
				}
				return r, err
			}}
		})
	})
}
