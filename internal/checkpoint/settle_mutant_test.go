package checkpoint_test

import (
	"context"
	"testing"

	cp "ataniq/internal/checkpoint"
	"ataniq/internal/checkpoint/checkpointtest"
)

type faultySettlement struct {
	cp.Administration
	forget bool
}

func (a faultySettlement) Settle(ctx context.Context, epoch uint64, s cp.Slot, id cp.ID) (cp.Receipt, error) {
	if a.forget {
		return cp.Receipt{}, cp.ErrNotApplied
	}
	r, err := a.Administration.Settle(ctx, epoch, s, id)
	if err == nil {
		r = cp.Receipt{}
	}
	return r, err
}
func TestSettlementMutants(t *testing.T) {
	for _, fault := range []struct {
		name, diagnostic string
		forget           bool
	}{
		{"forget", "owner fence did not stop late claim", true},
		{"history", "settlement lost committed receipt", false},
	} {
		t.Run(fault.name, func(t *testing.T) {
			rejectMutant(t, "settle", fault.diagnostic, func(h *checkpointtest.Harness) { h.Admin = faultySettlement{h.Admin, fault.forget} })
		})
	}
}
