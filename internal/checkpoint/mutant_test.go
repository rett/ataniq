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
	rejectMemoryMutant(t, scenario, diagnostic, func(h *checkpointtest.Harness, _ *cp.Memory) { mutate(h) })
}

func rejectMemoryMutant(t *testing.T, scenario, diagnostic string, mutate func(*checkpointtest.Harness, *cp.Memory)) {
	t.Helper()
	if os.Getenv("CHECKPOINT_MUTANT") == t.Name() {
		checkpointtest.Run(t, func(t *testing.T, k ed25519.PrivateKey) checkpointtest.Harness {
			h, w := newMemoryHarness(t, k)
			mutate(&h, w)
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
		saved := map[cp.Advance]cp.Receipt{}
		wrapConnect(h, func(c cp.Client) cp.Client {
			return adapterClient{c, func(ctx context.Context, q cp.Advance) (cp.Receipt, error) {
				r, err := c.Advance(ctx, q)
				if err == nil {
					saved[q] = r
				} else if prior, ok := saved[q]; ok && errors.Is(err, cp.ErrStale) {
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

type unscopedReader struct {
	cp.Client
	connect func(cp.Slot) (cp.Client, error)
}

func (c unscopedReader) Read(ctx context.Context, q cp.ReadRequest) (cp.View, error) {
	client, err := c.connect(q.Slot)
	if err != nil {
		return cp.View{}, err
	}
	return client.Read(ctx, q)
}

func TestUnscopedReadMutant(t *testing.T) {
	rejectMutant(t, "shared_home_key_slot_scope", "got <nil>, want checkpoint: refused", func(h *checkpointtest.Harness) {
		connect := h.Connect
		h.Connect = func(slot cp.Slot, home, session ed25519.PrivateKey, pin ed25519.PublicKey) (cp.Client, error) {
			c, err := connect(slot, home, session, pin)
			if err != nil {
				return nil, err
			}
			return unscopedReader{c, func(slot cp.Slot) (cp.Client, error) {
				return connect(slot, home, session, pin)
			}}, nil
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
				reader := h.Admin
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

func TestReusableRetiredSlotMutant(t *testing.T) {
	rejectMemoryMutant(t, "permanent_retired_slot", "fresh enrollment replaced retired slot", func(h *checkpointtest.Harness, w *cp.Memory) {
		admin := h.Admin
		h.Admin = ownerAdapter{Administration: admin, enroll: func(ctx context.Context, q cp.Enrollment) (cp.Receipt, error) {
			w.ForgetRetiredSlot(q.Slot)
			return admin.Enroll(ctx, q)
		}}
	})
}

func TestClaimAfterRetireMutant(t *testing.T) {
	rejectMutant(t, "permanent_retired_slot", "got <nil>, want checkpoint: retired", func(h *checkpointtest.Harness) {
		wrapConnect(h, func(c cp.Client) cp.Client {
			return claimClient{c, func(ctx context.Context, q cp.Claim) (cp.Receipt, error) {
				r, err := c.Claim(ctx, q)
				if errors.Is(err, cp.ErrRetired) {
					return r, nil
				}
				return r, err
			}}
		})
	})
}

func TestOwnerRetryMutants(t *testing.T) {
	for _, op := range []string{"enroll", "release", "retire"} {
		t.Run(op, func(t *testing.T) {
			rejectMutant(t, "conflicting_"+op, "got <nil>, want checkpoint: stale", func(h *checkpointtest.Harness) {
				a := ownerAdapter{Administration: h.Admin}
				saved := map[cp.ID]cp.Receipt{}
				replay := func(id cp.ID, call func() (cp.Receipt, error)) (cp.Receipt, error) {
					if r, ok := saved[id]; ok {
						return r, nil
					}
					r, err := call()
					if err == nil {
						saved[id] = r
					}
					return r, err
				}
				switch op {
				case "enroll":
					a.enroll = func(ctx context.Context, q cp.Enrollment) (cp.Receipt, error) {
						return replay(q.ID, func() (cp.Receipt, error) { return a.Administration.Enroll(ctx, q) })
					}
				case "release":
					a.release = func(ctx context.Context, q cp.Mutation) (cp.Receipt, error) {
						return replay(q.ID, func() (cp.Receipt, error) { return a.Administration.Release(ctx, q) })
					}
				case "retire":
					a.retire = func(ctx context.Context, q cp.Retirement) (cp.Receipt, error) {
						return replay(q.ID, func() (cp.Receipt, error) { return a.Administration.Retire(ctx, q) })
					}
				}
				h.Admin = a
			})
		})
	}
}

// An unchecked client returns the delivered acknowledgement even when its
// operation, evidence, result or signature disagrees with the request.
func TestUncheckedReceiptMutant(t *testing.T) {
	for _, field := range []string{"operation", "id", "request_digest", "predecessor", "head", "fence", "session", "epoch", "replay", "signer"} {
		t.Run(field, func(t *testing.T) {
			rejectMutant(t, "lying_advance_"+field, "got <nil>, want checkpoint: unknown mutation outcome", func(h *checkpointtest.Harness) {
				var delivered cp.Receipt
				transform := h.TransformNextReply
				h.TransformNextReply = func(change func(cp.Receipt) cp.Receipt) {
					transform(func(r cp.Receipt) cp.Receipt { delivered = change(r); return delivered })
				}
				wrapConnect(h, func(c cp.Client) cp.Client {
					return adapterClient{c, func(ctx context.Context, q cp.Advance) (cp.Receipt, error) {
						r, err := c.Advance(ctx, q)
						if delivered.Body != nil && errors.Is(err, cp.ErrUnknownOutcome) {
							return delivered, nil
						}
						return r, err
					}}
				})
			})
		})
	}
}
