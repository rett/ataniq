package checkpoint_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"os"
	"os/exec"
	"testing"

	cp "ataniq/internal/checkpoint"
	"ataniq/internal/checkpoint/checkpointtest"
)

// Run one case of the exported suite against a deliberately faulty adapter.
// Require the case's own diagnostic so setup failures cannot kill a mutant.
func rejectMutant(t *testing.T, scenario, diagnostic string, mutate func(*checkpointtest.Harness)) {
	t.Helper()
	if os.Getenv("CHECKPOINT_MUTANT") == t.Name() {
		checkpointtest.Run(t, func(t *testing.T, k ed25519.PrivateKey) checkpointtest.Harness {
			h := memoryHarness(t, k)
			mutate(&h)
			return h
		})
		return
	}
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^"+t.Name()+"$/^"+scenario+"$")
	cmd.Env = append(os.Environ(), "CHECKPOINT_MUTANT="+t.Name())
	output, err := cmd.CombinedOutput()
	var failed *exec.ExitError
	if !errors.As(err, &failed) || failed.ExitCode() != 1 || !bytes.Contains(output, []byte(diagnostic)) {
		t.Fatalf("suite missed mutant or failed elsewhere: %v\n%s", err, output)
	}
}

func TestReplacementRetryMutant(t *testing.T) {
	rejectMutant(t, "replacement_retry", "got <nil>, want checkpoint: stale", func(h *checkpointtest.Harness) {
		saved := map[cp.ID]cp.Receipt{}
		wrapConnect(h, func(c cp.Client) cp.Client {
			return adapterClient{c, func(ctx context.Context, q cp.Advance) (cp.Receipt, error) {
				r, err := c.Advance(ctx, q)
				if err == nil {
					saved[q.ID] = r
				} else if prior, ok := saved[q.ID]; ok && errors.Is(err, cp.ErrStale) {
					return prior, nil
				}
				return r, err
			}}
		})
	})
}

type historicalClient struct {
	cp.Client
	pin ed25519.PublicKey
}

func (c historicalClient) Read(ctx context.Context, q cp.ReadRequest) (cp.View, error) {
	v, err := c.Client.Read(ctx, q)
	if err == nil && v.Prior != nil {
		a, err := cp.VerifyReceipt(c.pin, *v.Prior)
		if err != nil {
			return cp.View{}, err
		}
		v.Current.Head = a.State.Head
	}
	return v, err
}
func TestHistoricalHeadMutant(t *testing.T) {
	rejectMutant(t, "historical_head", "historical lookup changed the current head", func(h *checkpointtest.Harness) {
		connect := h.Connect
		h.Connect = func(s cp.Slot, home, session ed25519.PrivateKey, pin ed25519.PublicKey) (cp.Client, error) {
			c, err := connect(s, home, session, pin)
			return historicalClient{c, pin}, err
		}
	})
}
