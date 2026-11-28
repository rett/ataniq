package checkpoint_test

import (
	"context"
	"encoding/json"
	"errors"
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

type settleAdmin struct {
	cp.Administration
	settle func(context.Context, uint64, cp.Slot, cp.ID) (cp.Receipt, error)
}

func (a settleAdmin) Settle(ctx context.Context, epoch uint64, s cp.Slot, id cp.ID) (cp.Receipt, error) {
	return a.settle(ctx, epoch, s, id)
}

type readOnlySettler struct {
	cp.Client
	admin cp.Administration
}

func (c readOnlySettler) Settle(ctx context.Context, epoch uint64, s cp.Slot, id cp.ID) (cp.Receipt, error) {
	return c.admin.Settle(ctx, epoch, s, id)
}

func TestSettlementBoundaryMutants(t *testing.T) {
	t.Run("readonly", func(t *testing.T) {
		rejectMutant(t, "settle_readonly", "read-only settlement", func(h *checkpointtest.Harness) {
			wrapConnect(h, func(c cp.Client) cp.Client { return readOnlySettler{c, h.Admin} })
		})
	})
	for _, op := range []string{"enroll", "release", "retire"} {
		t.Run(op, func(t *testing.T) {
			rejectMutant(t, "settle_owner_history", "lost committed "+op+" receipt", func(h *checkpointtest.Harness) {
				a := h.Admin
				h.Admin = settleAdmin{a, func(ctx context.Context, epoch uint64, s cp.Slot, id cp.ID) (cp.Receipt, error) {
					r, err := a.Settle(ctx, epoch, s, id)
					var ack struct{ Operation string }
					if err == nil && json.Unmarshal(r.Body, &ack) == nil && ack.Operation == op {
						return cp.Receipt{}, cp.ErrNotApplied
					}
					return r, err
				}}
			})
		})
	}
	t.Run("unenrolled", func(t *testing.T) {
		rejectMutant(t, "settle_before_enroll", "late enrollment escaped fence", func(h *checkpointtest.Harness) {
			a := h.Admin
			h.Admin = settleAdmin{a, func(ctx context.Context, epoch uint64, s cp.Slot, id cp.ID) (cp.Receipt, error) {
				if _, err := a.Read(ctx, cp.ReadRequest{Slot: s, Challenge: cp.ID{1}}); err != nil {
					return cp.Receipt{}, cp.ErrNotApplied
				}
				return a.Settle(ctx, epoch, s, id)
			}}
		})
	})
	t.Run("refence", func(t *testing.T) {
		rejectMutant(t, "settle_refence", "repeated settlement", func(h *checkpointtest.Harness) {
			a := h.Admin
			var fenced cp.ID
			h.Admin = settleAdmin{a, func(ctx context.Context, epoch uint64, s cp.Slot, id cp.ID) (cp.Receipt, error) {
				if id == fenced {
					return cp.Receipt{}, cp.ErrStale
				}
				r, err := a.Settle(ctx, epoch, s, id)
				if errors.Is(err, cp.ErrNotApplied) {
					fenced = id
				}
				return r, err
			}}
		})
	})
	t.Run("changed bytes", func(t *testing.T) {
		rejectMemoryMutant(t, "settle_changed_bytes", "changed claim escaped fence", func(h *checkpointtest.Harness, w *cp.Memory) {
			blocked := map[cp.ID]string{}
			wrapConnect(h, func(c cp.Client) cp.Client {
				return claimClient{c, func(ctx context.Context, q cp.Claim) (cp.Receipt, error) {
					session := string(q.NewSessionPublicKey)
					if old, ok := blocked[q.ID]; ok && old != session {
						w.ForgetFence(q.Expected.Slot, q.ID)
					}
					r, err := c.Claim(ctx, q)
					if errors.Is(err, cp.ErrNotApplied) {
						blocked[q.ID] = session
					}
					return r, err
				}}
			})
		})
	})
	for _, guard := range []string{"inhibited", "limit"} {
		t.Run(guard, func(t *testing.T) {
			rejectMutant(t, "settle_"+guard, "new fence during "+guard, func(h *checkpointtest.Harness) {
				if guard == "inhibited" {
					h.Inhibit = func(bool) {}
				} else {
					h.SetReceiptLimit = func(uint64) {}
				}
			})
		})
	}
	t.Run("restart", func(t *testing.T) {
		rejectMemoryMutant(t, "settle_restart", "restart lost fence", func(h *checkpointtest.Harness, w *cp.Memory) {
			a := h.Admin
			var slot cp.Slot
			var fenced cp.ID
			h.Admin = settleAdmin{a, func(ctx context.Context, epoch uint64, s cp.Slot, id cp.ID) (cp.Receipt, error) {
				r, err := a.Settle(ctx, epoch, s, id)
				if errors.Is(err, cp.ErrNotApplied) {
					slot, fenced = s, id
				}
				return r, err
			}}
			reopen := h.Restart
			h.Restart = func() checkpointtest.Harness { w.ForgetFence(slot, fenced); return reopen() }
		})
	})
}

type settleClient struct {
	cp.Client
	settle cp.Settler
}

func (c settleClient) Settle(ctx context.Context, epoch uint64, s cp.Slot, id cp.ID) (cp.Receipt, error) {
	return c.settle.Settle(ctx, epoch, s, id)
}

func TestSettleAuthorityMutants(t *testing.T) {
	for _, role := range []string{"owner", "home"} {
		t.Run(role, func(t *testing.T) {
			rejectMutant(t, "settle_epoch_"+role, "wrong epoch", func(h *checkpointtest.Harness) {
				admin := h.Admin
				ignoreEpoch := settleAdmin{admin, func(ctx context.Context, _ uint64, s cp.Slot, id cp.ID) (cp.Receipt, error) {
					return admin.Settle(ctx, 1, s, id)
				}}
				if role == "owner" {
					h.Admin = ignoreEpoch
				} else {
					wrapConnect(h, func(c cp.Client) cp.Client { return settleClient{c, ignoreEpoch} })
				}
			})
		})
	}
	t.Run("foreign", func(t *testing.T) {
		rejectMutant(t, "settle_foreign_home", "foreign home settlement", func(h *checkpointtest.Harness) {
			wrapConnect(h, func(c cp.Client) cp.Client { return settleClient{c, h.Admin} })
		})
	})
}
