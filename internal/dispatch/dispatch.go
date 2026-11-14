// Package dispatch coordinates one bounded effect without replaying uncertain work.
package dispatch

import (
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
// unknown result may have committed and must never authorize start. Recovered
// prepared/dispatching entries are duplicates, not new work. Save must retain
// contradictory evidence and must not rewrite the immutable intent or grant.
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
// exact policy, qualification, authorization and bounded limits at that point.
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
	in.Payload = append([]byte(nil), in.Payload...)
	status, receipt, err := c.Journal.Prepare(ctx, in)
	if err != nil {
		return Receipt{Intent: in, State: Uncertain}, fmt.Errorf("prepare uncertain: %w", err)
	}
	switch status {
	case PrepareDuplicate:
		return receipt, nil
	case PrepareConflict:
		return receipt, errors.New("intent or grant conflict")
	case PrepareRejected:
		return receipt, errors.New("preparation rejected")
	case PrepareNew:
	default:
		return Receipt{Intent: in, State: Uncertain}, errors.New("prepare outcome uncertain")
	}
	// No admission token escapes the boundary call. Cancellation before the call
	// is a proved no-start; a boundary error is always uncertain.
	state, evidence := Denied, "context canceled before start"
	if ctx.Err() == nil {
		result, detail, startErr := c.Boundary.Start(ctx, in)
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
	receipt = Receipt{Intent: in, State: state, Evidence: evidence}
	if err := c.Journal.Save(ctx, receipt); err != nil {
		return Receipt{Intent: in, State: Uncertain, Evidence: evidence}, fmt.Errorf("save outcome uncertain: %w", err)
	}
	return receipt, nil
}
