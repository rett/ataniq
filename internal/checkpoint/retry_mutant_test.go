package checkpoint_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"

	cp "ataniq/internal/checkpoint"
	"ataniq/internal/checkpoint/checkpointtest"
)

func TestLostAckRetryMutants(t *testing.T) {
	for _, op := range []string{"enroll", "claim", "release", "retire"} {
		for _, fault := range []string{"inhibited", "bytes"} {
			t.Run(op+" "+fault, func(t *testing.T) {
				diag := fmt.Sprintf("exact %s retry changed receipt bytes (inhibited=false)", op)
				if fault == "inhibited" {
					diag = fmt.Sprintf("exact %s retry (inhibited=true): %v", op, cp.ErrUnavailable)
				}
				rejectMutant(t, "lost_ack_"+op, diag, func(h *checkpointtest.Harness) {
					inhibited := false
					inhibit := h.Inhibit
					h.Inhibit = func(on bool) { inhibited = on; inhibit(on) }
					var lost cp.ID
					retry := func(id cp.ID, call func() (cp.Receipt, error)) (cp.Receipt, error) {
						if id == lost && fault == "inhibited" && inhibited {
							return cp.Receipt{}, cp.ErrUnavailable
						}
						r, err := call()
						if errors.Is(err, cp.ErrUnknownOutcome) {
							lost = id
						} else if err == nil && id == lost && fault == "bytes" && !inhibited {
							r.Signature = append(bytes.Clone(r.Signature), '=')
						}
						return r, err
					}
					a := ownerAdapter{Administration: h.Admin}
					switch op {
					case "enroll":
						a.enroll = func(ctx context.Context, q cp.Enrollment) (cp.Receipt, error) {
							return retry(q.ID, func() (cp.Receipt, error) { return a.Administration.Enroll(ctx, q) })
						}
					case "release":
						a.release = func(ctx context.Context, q cp.Mutation) (cp.Receipt, error) {
							return retry(q.ID, func() (cp.Receipt, error) { return a.Administration.Release(ctx, q) })
						}
					case "retire":
						a.retire = func(ctx context.Context, q cp.Retirement) (cp.Receipt, error) {
							return retry(q.ID, func() (cp.Receipt, error) { return a.Administration.Retire(ctx, q) })
						}
					case "claim":
						wrapConnect(h, func(c cp.Client) cp.Client {
							return claimClient{c, func(ctx context.Context, q cp.Claim) (cp.Receipt, error) {
								return retry(q.ID, func() (cp.Receipt, error) { return c.Claim(ctx, q) })
							}}
						})
					}
					h.Admin = a
				})
			})
		}
	}
}

func TestReplacementClaimRetryMutant(t *testing.T) {
	rejectMutant(t, "lost_ack_claim", "got <nil>, want checkpoint: refused", func(h *checkpointtest.Harness) {
		wrapConnect(h, func(c cp.Client) cp.Client {
			return claimClient{c, func(ctx context.Context, q cp.Claim) (cp.Receipt, error) {
				r, err := c.Claim(ctx, q)
				if errors.Is(err, cp.ErrRefused) {
					v, readErr := c.Read(ctx, cp.ReadRequest{Slot: q.Expected.Slot, ReceiptID: q.ID, Challenge: cp.ID{1}})
					if readErr == nil && v.Prior != nil {
						return *v.Prior, nil
					}
				}
				return r, err
			}}
		})
	})
}
