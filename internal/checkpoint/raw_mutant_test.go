package checkpoint_test

import (
	"context"
	"crypto/ed25519"
	"testing"

	cp "ataniq/internal/checkpoint"
	"ataniq/internal/checkpoint/checkpointtest"
)

// Give Memory a valid fixture proof in place of the one proof the mutant
// would accept. The other proof, request validation and commit run normally.
func TestRawProofMutants(t *testing.T) {
	for _, op := range []string{"claim", "advance"} {
		for _, role := range []string{"home", "session"} {
			for _, fault := range []string{"missing", "key", "namespace"} {
				name := "wrong " + role + " " + fault
				if fault == "missing" {
					name = "missing " + role
				}
				t.Run(op+" "+name, func(t *testing.T) {
					rejectMutant(t, "raw_"+op, op+" with "+name+": got <nil>, want ErrRefused", func(h *checkpointtest.Harness) {
						allowRawProof(h, op, role, fault)
					})
				})
			}
		}
	}
}

func allowRawProof(h *checkpointtest.Harness, op, role, fault string) {
	var home, session ed25519.PrivateKey
	connect := h.Connect
	h.Connect = func(s cp.Slot, hk, sk ed25519.PrivateKey, pin ed25519.PublicKey) (cp.Client, error) {
		home, session = hk, sk
		return connect(s, hk, sk, pin)
	}
	submit := h.RawSubmit
	h.RawSubmit = func(ctx context.Context, slot cp.Slot, pin ed25519.PublicKey, operation string, body, hs, ss []byte) (cp.Receipt, error) {
		proof, signer, other := &hs, home, session
		namespace, alternate := cp.HomeNamespace, cp.SessionNamespace
		if role == "session" {
			proof, signer, other = &ss, session, home
			namespace, alternate = cp.SessionNamespace, cp.HomeNamespace
		}
		var accepts bool
		switch fault {
		case "missing":
			accepts = len(*proof) == 0
		case "key":
			accepts = cp.VerifySignature(namespace, other.Public().(ed25519.PublicKey), body, *proof) == nil
		case "namespace":
			accepts = cp.VerifySignature(alternate, signer.Public().(ed25519.PublicKey), body, *proof) == nil
		}
		if operation == op && accepts {
			var err error
			*proof, err = cp.Sign(namespace, signer, body)
			if err != nil {
				return cp.Receipt{}, err
			}
		}
		return submit(ctx, slot, pin, operation, body, hs, ss)
	}
}

func TestMissingRawSubmitHook(t *testing.T) {
	rejectMutant(t, "raw_claim", "Harness.RawSubmit is missing", func(h *checkpointtest.Harness) {
		h.RawSubmit = nil
	})
}
