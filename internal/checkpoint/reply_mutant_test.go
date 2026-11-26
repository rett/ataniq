package checkpoint_test

import (
	"context"
	"crypto/ed25519"
	"errors"
	"testing"

	cp "ataniq/internal/checkpoint"
	"ataniq/internal/checkpoint/checkpointtest"
)

// Model a verifier that accepts a specific discrepancy between the genuine
// acknowledgement and the delivered one. Memory still commits the real intent.
func acceptAlteredReply(h *checkpointtest.Harness, accept func(ed25519.PublicKey, cp.Receipt, cp.Receipt) bool) {
	var original, delivered cp.Receipt
	transform := h.TransformNextReply
	h.TransformNextReply = func(change func(cp.Receipt) cp.Receipt) {
		transform(func(r cp.Receipt) cp.Receipt {
			original, delivered = r, change(r)
			return delivered
		})
	}
	connect := h.Connect
	h.Connect = func(s cp.Slot, home, session ed25519.PrivateKey, pin ed25519.PublicKey) (cp.Client, error) {
		c, err := connect(s, home, session, pin)
		if err != nil {
			return nil, err
		}
		return adapterClient{c, func(ctx context.Context, q cp.Advance) (cp.Receipt, error) {
			r, err := c.Advance(ctx, q)
			if errors.Is(err, cp.ErrUnknownOutcome) && delivered.Body != nil && accept(pin, original, delivered) {
				return delivered, nil
			}
			return r, err
		}}, nil
	}
}

func TestReceiptFieldMutants(t *testing.T) {
	for _, field := range []string{"revision", "home", "witness_slot"} {
		t.Run(field, func(t *testing.T) {
			rejectMutant(t, "lying_advance_"+field, "got <nil>, want checkpoint: unknown mutation outcome", func(h *checkpointtest.Harness) {
				acceptAlteredReply(h, func(pin ed25519.PublicKey, original, delivered cp.Receipt) bool {
					want, err := cp.VerifyReceipt(pin, original)
					if err != nil {
						return false
					}
					got, err := cp.VerifyReceipt(pin, delivered)
					if err != nil {
						return false
					}
					// The digest is derived from the signed bytes.
					got.State.LastReceiptDigest = want.State.LastReceiptDigest
					switch field {
					case "revision":
						got.State.Revision = want.State.Revision
					case "home":
						got.State.HomeKeyID = want.State.HomeKeyID
						got.State.Header.KeyID = want.State.Header.KeyID
					case "witness_slot":
						got.State.Slot.Witness = want.State.Slot.Witness
					}
					return got == want
				})
			})
		})
	}
}
