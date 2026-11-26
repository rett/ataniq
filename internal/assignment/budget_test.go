package assignment

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
)

// Saturate every fixed-width slot, including evidence digests in the public
// envelope, the signature and the terminating LF. Variable slots are empty.
func TestFollowupFrameBudget(t *testing.T) {
	digest := strings.Repeat("f", 64)
	n := uint64(math.MaxUint64)
	dec := fmt.Sprint(n)
	head := Head{Generation: n, Revision: n, Phase: PhasePartiallyFenced}
	b := Binding{Kind: "managed", RoleEpoch: n}
	p := PublicEnvelope{Format: Format, Algorithm: "Ed25519", Binding: PublicBinding{Kind: "managed"}, OldBinding: PublicBinding{Kind: "managed"},
		CommitRevision: dec, EventID: digest, EvidenceDigests: []string{}, Generation: dec, InventoryDigest: digest,
		Kind: QuarantineAcknowledged, Phase: PhasePartiallyFenced, PreviousGeneration: dec, PreviousPublicDigest: digest, PreviousRevision: dec,
		Revision: dec, Schema: 1}
	e := Event{Schema: 1, Sequence: n, PreviousHash: digest, RequestDigest: digest, Signature: strings.Repeat("A", 262),
		Command: Command{Kind: QuarantineAcknowledged, Candidate: b, Expected: head, HoldStart: n, EarliestRetirement: n},
		Result:  Result{Disposition: "aborted-closed", EventID: digest, Head: head}, Public: p}
	frame, err := e.RecordBytes()
	if err != nil {
		t.Fatal(err)
	}
	base := len(frame)
	// Include a full hash-valued subject and the longest fixed outcome even
	// though the variable-slot allowance already covers both when supplied.
	proof := Evidence{Digest: digest, Kind: "owner-disposition", Scope: head, ScopeEventID: digest, Subject: digest, Outcome: "stopped-disabled-preserved"}
	for range maxFollowupProofs {
		e.Command.Evidence = append(e.Command.Evidence, proof)
		e.Public.EvidenceDigests = append(e.Public.EvidenceDigests, digest)
	}
	frame, err = e.RecordBytes()
	if err != nil || base > baseFrameAllowance || len(frame)-base > maxFollowupProofs*proofFrameAllowance {
		t.Fatalf("fixed frame %d, %d proofs add %d: %v", base, maxFollowupProofs, len(frame)-base, err)
	}
}

func TestRejectUnfinishableIntake(t *testing.T) {
	s := fresh(t)
	for _, v := range []string{strings.Repeat("x", 9500), strings.Repeat("\x00", MaxReferenceBytes/6+1)} {
		refused(t, s.ledger, next(s.ledger, key, Seed, v, ""), ErrLimit)
	}
	h := header
	h.Store = strings.Repeat("s", MaxReferenceBytes+1)
	if _, err := NewLedger(h); !errors.Is(err, ErrLimit) {
		t.Fatal(err)
	}
	s = seeded(t)
	c := next(s.ledger, key, Freeze, "move", "")
	c.Candidate.Conversation = strings.Repeat("x", MaxReferenceBytes+1)
	refused(t, s.ledger, c, ErrLimit)
	s.steps(t, "move", forwardSteps[:4])
	c = next(s.ledger, key, FenceReceipt, "move", "worker")
	c.Evidence[0].Issuer = strings.Repeat("i", 40000)
	refused(t, s.ledger, c, ErrLimit)
}

func TestIntakeCapacityIsAtomic(t *testing.T) {
	for _, kind := range []Kind{Freeze, Inventory, ConfirmReceipt} {
		for _, resource := range []string{"routes", "outstanding effects"} {
			t.Run(string(kind)+"/"+resource, func(t *testing.T) {
				s := seeded(t)
				id := "move"
				if kind == Inventory {
					s.steps(t, id, forwardSteps[:1])
				} else if kind == ConfirmReceipt {
					id = "seed"
				}
				c := next(s.ledger, key, kind, id, "worker")
				if kind == ConfirmReceipt {
					c.Evidence[0].Digest = sha256Hex([]byte("amended"))
				}
				proof := c.Evidence[len(c.Evidence)-1]
				var extra int
				if resource == "routes" {
					// Capture and refresh start with two routes, correction with one.
					extra = MaxRoutes - 1
					if kind == ConfirmReceipt {
						extra++
					}
				} else {
					proof.Kind, proof.Route, proof.Outcome = "effect", "worker", "outstanding"
					// Freeze and refresh already have the captured child.
					extra = MaxOutstandingEffects
					if kind == ConfirmReceipt {
						extra++
					}
				}
				for i := 0; i < extra; i++ {
					proof.Subject = fmt.Sprint("extra-", i)
					c.Evidence = append(c.Evidence, proof)
				}
				before, err := recoverAt(s.events, s.ledger.Checkpoint())
				if err != nil {
					t.Fatal(err)
				}
				e, _, err := Decide(s.ledger, c)
				if e != nil || !errors.Is(err, ErrCapacity) || !strings.Contains(err.Error(), resource+": observed 9, limit 8") {
					t.Fatal(e, err)
				}
				if !reflect.DeepEqual(before, s.ledger) {
					t.Fatal("capacity refusal changed the ledger")
				}
			})
		}
	}
}
