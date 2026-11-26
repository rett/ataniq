package checkpoint_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
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

func TestOverlappingSessionMutants(t *testing.T) {
	for _, role := range []string{"home", "witness"} {
		t.Run(role, func(t *testing.T) {
			rejectMemoryMutant(t, "session_equals_"+role, "got <nil>, want checkpoint: refused", func(h *checkpointtest.Harness, w *cp.Memory) {
				h.Connect = func(slot cp.Slot, home, session ed25519.PrivateKey, pin ed25519.PublicKey) (cp.Client, error) {
					c, err := w.Connect(slot, home, session, pin)
					conflicting := home.Public().(ed25519.PublicKey)
					if role == "witness" {
						conflicting = pin
					}
					var reason *cp.Reason
					if errors.As(err, &reason) && reason.Kind == cp.KeyRoleConflict && bytes.Equal(session.Public().(ed25519.PublicKey), conflicting) {
						return w.ClientWithOverlappingRoles(slot, home, session, pin), nil
					}
					return c, err
				}
			})
		})
	}
}
