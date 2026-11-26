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

type ownerAdapter struct {
	cp.Administration
	enroll  func(context.Context, cp.Enrollment) (cp.Receipt, error)
	release func(context.Context, cp.Mutation) (cp.Receipt, error)
	retire  func(context.Context, cp.Retirement) (cp.Receipt, error)
}

func (a ownerAdapter) Enroll(ctx context.Context, q cp.Enrollment) (cp.Receipt, error) {
	if a.enroll != nil {
		return a.enroll(ctx, q)
	}
	return a.Administration.Enroll(ctx, q)
}
func (a ownerAdapter) Release(ctx context.Context, q cp.Mutation) (cp.Receipt, error) {
	if a.release != nil {
		return a.release(ctx, q)
	}
	return a.Administration.Release(ctx, q)
}
func (a ownerAdapter) Retire(ctx context.Context, q cp.Retirement) (cp.Receipt, error) {
	if a.retire != nil {
		return a.retire(ctx, q)
	}
	return a.Administration.Retire(ctx, q)
}
func TestOwnerCASMutant(t *testing.T) {
	for _, op := range []string{"release", "retire"} {
		t.Run(op, func(t *testing.T) {
			rejectMutant(t, "stale_owner_"+op, "got <nil>, want checkpoint: stale", func(h *checkpointtest.Harness) {
				var reader cp.Client
				wrapConnect(h, func(c cp.Client) cp.Client { reader = c; return c })
				a := ownerAdapter{Administration: h.Admin}
				current := func(ctx context.Context, s cp.Slot) cp.State {
					v, err := reader.Read(ctx, cp.ReadRequest{Slot: s, Challenge: cp.ID{1}})
					if err != nil {
						t.Fatal(err)
					}
					return v.Current
				}
				if op == "release" {
					a.release = func(ctx context.Context, m cp.Mutation) (cp.Receipt, error) {
						m.Expected = current(ctx, m.Expected.Slot)
						return a.Administration.Release(ctx, m)
					}
				} else {
					a.retire = func(ctx context.Context, q cp.Retirement) (cp.Receipt, error) {
						q.Expected = current(ctx, q.Expected.Slot)
						return a.Administration.Retire(ctx, q)
					}
				}
				h.Admin = a
			})
		})
	}
}

func TestTombstoneMutants(t *testing.T) {
	for _, op := range []string{"release", "retire", "ledger", "incarnation", "key"} {
		t.Run(op, func(t *testing.T) {
			expected := "got <nil>, want checkpoint: stale"
			if op == "release" || op == "retire" {
				expected = "got <nil>, want checkpoint: retired"
			}
			rejectMutant(t, "tombstone_"+op, expected, func(h *checkpointtest.Harness) {
				a := ownerAdapter{Administration: h.Admin}
				var tombstone cp.Receipt
				a.retire = func(ctx context.Context, q cp.Retirement) (cp.Receipt, error) {
					if op == "retire" && tombstone.Body != nil {
						return tombstone, nil
					}
					r, err := a.Administration.Retire(ctx, q)
					if err == nil {
						tombstone = r
					}
					return r, err
				}
				if op == "release" {
					a.release = func(ctx context.Context, q cp.Mutation) (cp.Receipt, error) {
						if tombstone.Body != nil {
							return tombstone, nil
						}
						return a.Administration.Release(ctx, q)
					}
				}
				if op != "release" && op != "retire" {
					a.enroll = func(ctx context.Context, q cp.Enrollment) (cp.Receipt, error) {
						r, err := a.Administration.Enroll(ctx, q)
						if tombstone.Body != nil && errors.Is(err, cp.ErrStale) {
							return tombstone, nil
						}
						return r, err
					}
				}
				h.Admin = a
			})
		})
	}
}
