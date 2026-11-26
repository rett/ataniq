package durable

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"slices"

	cp "ataniq/internal/checkpoint"
)

// Connect provides an in-process home capability. The network link instead
// uses CheckHome, ReadHome, SettleHome and RawSubmit with its transport auth.
func (w *Witness) Connect(slot cp.Slot, home, session ed25519.PrivateKey, pin ed25519.PublicKey) (cp.Client, error) {
	for _, key := range []ed25519.PrivateKey{home, session} {
		if len(key) != ed25519.PrivateKeySize || !key.Equal(ed25519.NewKeyFromSeed(key.Seed())) {
			return nil, &cp.Reason{Kind: cp.InvalidRequest}
		}
	}
	if !bytes.Equal(w.pin, pin) {
		return nil, &cp.Reason{Kind: cp.Unauthorized}
	}
	h := home.Public().(ed25519.PublicKey)
	s := session.Public().(ed25519.PublicKey)
	if err := w.CheckHome(context.Background(), slot, h); err != nil {
		return nil, err
	}
	if bytes.Equal(h, s) || bytes.Equal(s, pin) {
		return nil, &cp.Reason{Kind: cp.KeyRoleConflict}
	}
	if _, err := cp.KeyID(s); err != nil {
		return nil, err
	}
	home, session = slices.Clone(home), slices.Clone(session)
	return &client{w: w, slot: slot, home: h, session: s, submit: w.RawSubmit, proofs: func(b []byte) ([]byte, []byte, error) {
		hs, err := cp.Sign(cp.HomeNamespace, home, b)
		if err != nil {
			return nil, nil, err
		}
		ss, err := cp.Sign(cp.SessionNamespace, session, b)
		return hs, ss, err
	}}, nil
}

type client struct {
	w             *Witness
	slot          cp.Slot
	home, session ed25519.PublicKey
	proofs        func([]byte) ([]byte, []byte, error)
	submit        func(context.Context, cp.Slot, ed25519.PublicKey, string, []byte, []byte, []byte) (cp.Receipt, error)
}

func (c *client) Read(ctx context.Context, q cp.ReadRequest) (cp.View, error) {
	if q.Slot != c.slot {
		return cp.View{}, &cp.Reason{Kind: cp.Unauthorized}
	}
	return c.w.ReadHome(ctx, q, c.home)
}
func (c *client) Settle(ctx context.Context, epoch uint64, slot cp.Slot, id cp.ID) (cp.Receipt, error) {
	if slot != c.slot {
		return cp.Receipt{}, &cp.Reason{Kind: cp.Unauthorized}
	}
	return c.w.SettleHome(ctx, epoch, slot, id, c.home)
}
func (c *client) Claim(ctx context.Context, q cp.Claim) (cp.Receipt, error) {
	if !bytes.Equal(c.session, q.NewSessionPublicKey) {
		return cp.Receipt{}, &cp.Reason{Kind: cp.InvalidRequest}
	}
	return c.mutate(ctx, "claim", q.Expected, q)
}
func (c *client) Advance(ctx context.Context, q cp.Advance) (cp.Receipt, error) {
	return c.mutate(ctx, "advance", q.Expected, q)
}
func (c *client) mutate(ctx context.Context, op string, s cp.State, intent cp.Intent) (cp.Receipt, error) {
	if s.Slot != c.slot {
		return cp.Receipt{}, &cp.Reason{Kind: cp.Unauthorized}
	}
	b, err := intent.Body("")
	if err != nil {
		return cp.Receipt{}, err
	}
	hs, ss, err := c.proofs(b)
	if err != nil {
		return cp.Receipt{}, err
	}
	r, err := c.submit(ctx, c.slot, c.home, op, b, hs, ss)
	if err != nil {
		return cp.Receipt{}, err
	}
	if err := cp.MatchReceipt(c.w.pin, s.Epoch, op, b, r); err != nil {
		return cp.Receipt{}, err
	}
	return r, nil
}
