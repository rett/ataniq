package checkpoint_test

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"testing"

	cp "ataniq/internal/checkpoint"
	"ataniq/internal/checkpoint/checkpointtest"
)

type adapterClient struct {
	cp.Client
	advance func(context.Context, cp.Advance) (cp.Receipt, error)
}

func (c adapterClient) Advance(ctx context.Context, q cp.Advance) (cp.Receipt, error) {
	return c.advance(ctx, q)
}

func wrapConnect(h *checkpointtest.Harness, wrap func(cp.Client) cp.Client) {
	connect := h.Connect
	h.Connect = func(s cp.Slot, home, session ed25519.PrivateKey, pin ed25519.PublicKey) (cp.Client, error) {
		c, err := connect(s, home, session, pin)
		if err != nil {
			return nil, err
		}
		return wrap(c), nil
	}
}

func TestWrappedAdapterContract(t *testing.T) {
	checkpointtest.Run(t, func(t *testing.T, k ed25519.PrivateKey) checkpointtest.Harness {
		h := memoryHarness(t, k)
		wrapConnect(&h, func(c cp.Client) cp.Client {
			return adapterClient{c, func(ctx context.Context, q cp.Advance) (cp.Receipt, error) {
				r, err := c.Advance(ctx, q)
				if err != nil {
					return r, fmt.Errorf("advance: %w", err)
				}
				return r, nil
			}}
		})
		return h
	})
}
