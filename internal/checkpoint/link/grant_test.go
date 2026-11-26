package link

import (
	"context"
	"crypto/ed25519"
	"errors"
	"testing"
	"time"

	cp "ataniq/internal/checkpoint"
)

type blockedGrant struct {
	Witness
	entered chan struct{}
}

func (w blockedGrant) CheckHome(ctx context.Context, _ cp.Slot, _ ed25519.PublicKey) error {
	close(w.entered)
	<-ctx.Done()
	return ctx.Err()
}
func TestGrantLookupCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	w := blockedGrant{entered: make(chan struct{})}
	done := make(chan error, 1)
	go func() { done <- authenticate(ctx, w, cp.Slot{}, nil, nil, nil) }()
	select {
	case <-w.entered:
	case <-time.After(time.Second):
		t.Fatal("grant lookup did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("grant lookup did not stop")
	}
}
