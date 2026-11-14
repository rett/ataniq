// Package dispatch coordinates one bounded effect without replaying uncertain work.
package dispatch

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
)

// Intent is an immutable binding. IDs are opaque, nonempty, whitespace-free
// strings; payload bytes are compared exactly, without normalization.
type Intent struct {
	Authority, Project, Command, ID, Attempt     string
	Runtime, Epoch, Action, Kind                 string
	Policy, Qualification, Authorization, Limits string
	Grant                                        string // empty when policy requires no one-use approval
	Payload                                      []byte
}

func cloneIntent(i Intent) Intent {
	i.Payload = append([]byte(nil), i.Payload...)
	return i
}

func sameIntent(a, b Intent) bool {
	ap, bp := a.Payload, b.Payload
	a.Payload, b.Payload = nil, nil
	return equalIdentity(a, b) && bytes.Equal(ap, bp)
}

func equalIdentity(a, b Intent) bool {
	return a.Authority == b.Authority && a.Project == b.Project && a.Command == b.Command &&
		a.ID == b.ID && a.Attempt == b.Attempt && a.Runtime == b.Runtime &&
		a.Epoch == b.Epoch && a.Action == b.Action && a.Kind == b.Kind &&
		a.Policy == b.Policy && a.Qualification == b.Qualification &&
		a.Authorization == b.Authorization && a.Limits == b.Limits && a.Grant == b.Grant
}

// Digest returns the SHA-256 digest of the exact payload.
func (i Intent) Digest() [32]byte { return sha256.Sum256(i.Payload) }

func (i Intent) validate() error {
	for _, s := range []string{i.Authority, i.Project, i.Command, i.ID, i.Attempt, i.Runtime, i.Epoch, i.Action, i.Kind, i.Policy, i.Qualification, i.Authorization, i.Limits} {
		if s == "" || strings.TrimSpace(s) != s || strings.ContainsAny(s, "\t\r\n ") || len(s) > 256 {
			return errors.New("invalid identity")
		}
	}
	if i.Grant != "" && (strings.ContainsAny(i.Grant, " \t\r\n") || len(i.Grant) > 256) {
		return errors.New("invalid grant")
	}
	switch i.Kind {
	case "queued", "tool", "child", "iteration", "retry":
	default:
		return errors.New("unsupported effect kind")
	}
	if len(i.Payload) > 1<<20 {
		return errors.New("payload too large")
	}
	return nil
}

// State records only evidence, never a claim about an unknown provider outcome.
type State string

const (
	Prepared  State = "prepared"
	Started   State = "started"
	Denied    State = "denied"
	Uncertain State = "uncertain"
)

type Receipt struct {
	Intent   Intent
	ID       string // identity of this evidence, independent of intent identity
	State    State
	Evidence string
}

type PrepareStatus uint8

const (
	PrepareUnknown PrepareStatus = iota
	PrepareNew
	PrepareDuplicate
	PrepareConflict
	PrepareRejected // proved no transaction committed
)

// Journal is the application journal, not qualification Campaign.Save.
// Prepare atomically stores the exact intent and consumes its exact-target grant
// after checking expiry, revocation and policy. New means a durable commit by
// this call. Duplicate returns the original receipt only for identical content;
// conflicting identity or grant use is retained as a contradiction. An error or
// unknown result may have committed and must fence the exact attempt across
// recreation until evidence-based reconciliation. Recovered
// prepared/dispatching entries are duplicates, not new work. Save must retain
// contradictory evidence and must not rewrite the immutable intent or grant.
// Save is idempotent for identical evidence under the same receipt ID. It
// retains differing content under that ID and contradictory outcomes under
// different IDs; duplicate inspection then reports uncertainty. A foreign
// immutable binding is a conflict and cannot alter the legitimate record.
// All methods must be safe for concurrent callers.
type Journal interface {
	Prepare(context.Context, Intent) (PrepareStatus, Receipt, error)
	Save(context.Context, Receipt) error
}

type StartStatus uint8

const (
	StartUnknown StartStatus = iota
	StartDenied              // proof that no start occurred
	StartAcknowledged
)

// Boundary is trusted to serialize the actual start with observed authority,
// epoch and revocation changes. Start obtains current authority and checks the
// exact policy, qualification, authorization, bounded limits and any required
// exact-target grant including its current expiry and revocation at that point.
// It returns promptly after acknowledgement, denial or uncertainty, without
// waiting for completion, and must not start later after returning, including
// after cancellation. It exposes no callback or reusable permit. Its guarantees
// cover only its own enforcement domain; external fencing requires integration.
type Boundary interface {
	Start(context.Context, Intent) (StartStatus, string, error)
}

type Coordinator struct {
	Journal  Journal
	Boundary Boundary
}

// Dispatch prepares exactly once, then requests one fresh boundary crossing.
// A returned error after preparation does not permit replay of this attempt.
func (c Coordinator) Dispatch(ctx context.Context, in Intent) (Receipt, error) {
	if c.Journal == nil || c.Boundary == nil {
		return Receipt{}, errors.New("missing port")
	}
	if err := in.validate(); err != nil {
		return Receipt{}, err
	}
	in = cloneIntent(in)
	status, receipt, err := c.Journal.Prepare(ctx, cloneIntent(in))
	if err != nil {
		return Receipt{Intent: in, State: Uncertain}, fmt.Errorf("prepare uncertain: %w", err)
	}
	// A port result is untrusted until its binding and state are checked. Never
	// disclose a foreign receipt, even on a conflict.
	valid := sameIntent(in, receipt.Intent)
	switch status {
	case PrepareDuplicate:
		if !valid || (receipt.State != Prepared && receipt.State != Started && receipt.State != Denied && receipt.State != Uncertain) {
			return Receipt{Intent: cloneIntent(in), State: Uncertain}, errors.New("invalid duplicate receipt")
		}
		receipt.Intent = cloneIntent(receipt.Intent)
		return receipt, nil
	case PrepareConflict:
		return Receipt{Intent: cloneIntent(in), State: Uncertain}, errors.New("intent or grant conflict")
	case PrepareRejected:
		return Receipt{Intent: cloneIntent(in), State: Denied}, errors.New("preparation rejected")
	case PrepareNew:
		if !valid || receipt.State != Prepared || receipt.Evidence != "" {
			return Receipt{Intent: cloneIntent(in), State: Uncertain}, errors.New("invalid new receipt")
		}
	default:
		return Receipt{Intent: in, State: Uncertain}, errors.New("prepare outcome uncertain")
	}
	// No admission token escapes the boundary call. Cancellation before the call
	// is a proved no-start; a boundary error is always uncertain.
	state, evidence := Denied, "context canceled before start"
	if ctx.Err() == nil {
		result, detail, startErr := c.Boundary.Start(ctx, cloneIntent(in))
		evidence = detail
		switch {
		case startErr != nil:
			state = Uncertain
		case result == StartAcknowledged:
			state = Started
		case result == StartDenied:
			state = Denied
		default:
			state = Uncertain
		}
	}
	receipt = Receipt{Intent: in, ID: "start", State: state, Evidence: evidence}
	if err := c.Journal.Save(ctx, Receipt{Intent: cloneIntent(in), ID: receipt.ID, State: state, Evidence: evidence}); err != nil {
		return Receipt{Intent: in, State: Uncertain, Evidence: evidence}, fmt.Errorf("save outcome uncertain: %w", err)
	}
	return receipt, nil
}
