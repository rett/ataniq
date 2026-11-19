package doortest

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"ataniq/internal/approval/door"
	"ataniq/internal/approval/record"
)

// Admit takes a request. Its checks, in order: the bytes against their
// digest and the form (request-invalid, record-too-large,
// verb-not-requestable, feature-name); the assignment (not-assigned); the
// epoch (previous-epoch); a request id used before (request-invalid); a
// live request for the same verb and target (request-duplicate); the
// repeat throttle (request-throttled); the requests of the last minute
// (request-rate); the requests not yet final (request-pending-cap).
func (d *Door) Admit(ctx context.Context, in door.Record) (door.Admission, error) {
	if err := ctx.Err(); err != nil {
		return door.Admission{}, err
	}
	req, err := decodeRequest(in)
	if err != nil {
		return door.Admission{}, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.now()
	d.sweep(now)
	if err := d.seatTakes(&req); err != nil {
		return door.Admission{}, err
	}
	if d.requests[req.RequestID] != nil {
		return door.Admission{}, refuse(record.CodeRequestInvalid, "the request id was used before")
	}
	target := targetOf(&req)
	live := 0
	for _, r := range d.requests {
		if !d.live(r) {
			continue
		}
		live++
		if r.target == target {
			return door.Admission{}, refuse(record.CodeRequestDuplicate, "a request for this verb and target is not yet final")
		}
	}
	if d.throttle(target, now) {
		return door.Admission{}, refuse(record.CodeRequestThrottled, "this verb and target was asked for too often; wait ten minutes")
	}
	d.admitted = since(d.admitted, now.Add(-time.Minute))
	if len(d.admitted) >= RequestsPerMinute {
		return door.Admission{}, refuse(record.CodeRequestRate, "the seat had its requests for this minute")
	}
	if live >= MaxNotFinal {
		return door.Admission{}, refuse(record.CodeRequestPendingCap, "too many of the seat's requests are not yet final")
	}
	d.admitted = append(d.admitted, now)
	d.asked[target] = append(d.asked[target], now)
	d.requests[req.RequestID] = &request{req: req, sha256: in.SHA256, target: target}
	return door.Admission{RequestID: req.RequestID, RequestSHA256: in.SHA256}, nil
}

// Plan writes the pending record for an admitted request. It refuses a
// request that was not admitted, that has a record already or that has
// ended (request-invalid), a feature tip other than the request's
// (feature-moved), and a merge whose parents are not the base and the
// feature, or a target that is no longer at the base (binding-mismatch).
// What the package record refuses in a pending record is refused with
// record's code. A request whose plan is refused has ended: the lead asks
// again, with a new id.
func (d *Door) Plan(ctx context.Context, in door.PlanInput) (door.Record, error) {
	if err := ctx.Err(); err != nil {
		return door.Record{}, err
	}
	req, err := decodeRequest(in.Request)
	if err != nil {
		return door.Record{}, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.now()
	d.sweep(now)
	r := d.requests[req.RequestID]
	switch {
	case r == nil || r.sha256 != in.Request.SHA256:
		return door.Record{}, refuse(record.CodeRequestInvalid, "the request was not admitted")
	case r.pendingID != "":
		return door.Record{}, refuse(record.CodeRequestInvalid, "the request has a pending record already")
	}
	if err := d.seatTakes(&req); err != nil {
		r.ended = true
		return door.Record{}, err
	}
	if r.ended {
		return door.Record{}, refuse(record.CodeRequestInvalid, "the request has ended")
	}
	out, p, err := d.write(&req, &in, now)
	if err != nil {
		r.ended = true
		return door.Record{}, err
	}
	rec := door.RecordOf(out)
	d.number++
	r.pendingID = p.ID
	d.entries[p.ID] = &entry{pending: p, pendingRec: rec, state: record.StatePending}
	d.order = append(d.order, p.ID)
	return clone(rec), nil
}

// write makes the pending record for req from the host's findings and
// returns its canonical bytes with the record as the decoder reads them
// back.
func (d *Door) write(req *record.Request, in *door.PlanInput, now time.Time) ([]byte, record.Pending, error) {
	holder := d.seat.Holder
	b := record.Binding{
		Project: d.seat.Project,
		Subject: record.Subject{
			Kind:     record.SubjectSeat,
			RoleName: d.seat.RoleName,
			Epoch:    d.seat.Epoch,
			SeatRev:  d.seat.SeatRev,
			Holder:   &holder,
		},
		Verb:             req.Verb,
		Argv:             req.Argv,
		Class:            record.ClassRefMove,
		PlanToken:        d.planToken,
		Evidence:         in.Evidence,
		RequestedAt:      record.FormatTime(now),
		PendingExpiresAt: record.FormatTime(now.Add(PendingLife)),
	}
	if b.Evidence == nil {
		b.Evidence = []record.Evidence{}
	}
	if req.Verb == record.VerbFeatureFinish {
		switch {
		case in.Feature != req.FeatureSHA:
			return nil, record.Pending{}, refuse(record.CodeFeatureMoved, "the feature's tip is not the one the request names")
		case len(in.MergeParents) != 2 || in.MergeParents[0] != in.Base || in.MergeParents[1] != in.Feature:
			return nil, record.Pending{}, refuse(record.CodeBindingMismatch, "the merge's parents are not the target's tip and the feature's tip")
		case in.TargetNow != in.Base:
			return nil, record.Pending{}, refuse(record.CodeBindingMismatch, "the target is no longer at the tip the merge was built on")
		}
		b.Effects = []record.Effect{{Kind: record.EffectRef, Ref: in.TargetRef, Old: in.Base, New: in.Merge}}
		b.Merge = &record.Merge{Commit: in.Merge, Parents: []string{in.Base, in.Feature}, BuiltBy: record.BuiltByHost}
		b.Summary = in.Summary
	} else {
		b.Effects = in.Deletes
	}
	text := planText(d.command, &b)
	b.PlanSHA256 = record.PlanDigest(text)
	digest, err := record.BindingDigest(b)
	if err != nil {
		return nil, record.Pending{}, err
	}
	out, err := record.EncodePending(record.Pending{
		Schema:        record.SchemaPending,
		ID:            fmt.Sprintf("ap-%s-%04d", now.UTC().Format("20060102"), d.number),
		Planner:       d.planner,
		RequestID:     req.RequestID,
		RequestSHA256: record.Digest(in.Request.Bytes),
		Binding:       b,
		BindingSHA256: digest,
		PlanText:      record.Text(text),
		Note:          in.Note,
	})
	if err != nil {
		return nil, record.Pending{}, err
	}
	// Reading the bytes back gives a record that shares nothing with the
	// caller's input.
	p, err := record.DecodePending(out)
	if err != nil {
		return nil, record.Pending{}, err
	}
	return out, p, nil
}

// planText returns the stand-in's plan for a binding. It is made of the
// bound values only, so that making it again at a use gives the same text.
func planText(command string, b *record.Binding) string {
	line := command + " " + b.Verb
	for _, a := range b.Argv {
		line += " " + a
	}
	if b.Verb != record.VerbFeatureFinish || len(b.Argv) != 1 || len(b.Effects) != 1 {
		return line
	}
	ref := b.Effects[0].Ref
	return line + "\n  merge feature/" + b.Argv[0] + " into " + strings.TrimPrefix(ref, "refs/heads/") + " (--no-ff)\n  move " + ref + "\n"
}

// Decide takes the owner's decision. Its checks, in order: the bytes
// against their digest and the form (decision-invalid, record-too-large);
// the id (decision-invalid); the binding digest (binding-mismatch). A
// record that has a decision answers what it answered the first time when
// this is the same decision (the same digest), and refuses another one
// (decision-invalid) without changing anything.
// Otherwise: the record's state (approval-expired, approval-void,
// previous-epoch: the seat's revision and epoch are checked here, since a
// record of another revision or epoch is void); the planner, the project,
// the subject and the plan token repeated beside the digest
// (binding-mismatch, naming the field); the decision's own times
// (decision-invalid, approval-expired); and for an approve the sign-offs
// against the flagged list (decision-invalid).
func (d *Door) Decide(ctx context.Context, decision door.Record) (door.Decided, error) {
	if err := ctx.Err(); err != nil {
		return door.Decided{}, err
	}
	if record.Digest(decision.Bytes) != decision.SHA256 {
		return door.Decided{}, refuse(record.CodeDecisionInvalid, "the decision's bytes do not have the digest given with them")
	}
	dec, err := record.DecodeDecision(decision.Bytes)
	if err != nil {
		return door.Decided{}, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.now()
	d.sweep(now)
	e := d.entries[dec.ID]
	if e == nil {
		return door.Decided{}, refuse(record.CodeDecisionInvalid, "no pending record has decision.id")
	}
	if dec.BindingSHA256 != e.pending.BindingSHA256 {
		return door.Decided{}, refuse(record.CodeBindingMismatch, "decision.binding_sha256 is not the digest of the record's binding")
	}
	if e.decision != nil {
		if decision.SHA256 == e.decided.DecisionSHA256 {
			return e.decided, nil
		}
		return door.Decided{}, refuse(record.CodeDecisionInvalid, "the record has another decision")
	}
	if e.state != record.StatePending {
		return door.Decided{}, ended(e)
	}
	b := &e.pending.Binding
	if field := differs(&dec, &e.pending); field != "" {
		return door.Decided{}, refuse(record.CodeBindingMismatch, "decision."+field+" is not the record's")
	}
	decidedAt, _ := record.ParseTime(dec.DecidedAt)
	expiresAt, _ := record.ParseTime(dec.ExpiresAt)
	switch {
	case !expiresAt.After(decidedAt):
		return door.Decided{}, refuse(record.CodeDecisionInvalid, "decision.expires_at is not after decision.decided_at")
	case !now.Before(expiresAt):
		return door.Decided{}, refuse(record.CodeApprovalExpired, "the decision's own time ran out before it arrived")
	}
	state := record.StateDenied
	if dec.Decision == record.DecisionApprove {
		var flagged []record.Flag
		if b.Summary != nil {
			flagged = b.Summary.Flagged
		}
		if !record.SignOffsEqual(flagged, dec.SignedOff) {
			return door.Decided{}, refuse(record.CodeDecisionInvalid, "decision.signed_off is not the record's flagged list")
		}
		state = record.StateApproved
	}
	e.move(state, "")
	e.decision = &dec
	e.decisionRec = clone(decision)
	e.decided = door.Decided{State: state, DecisionSHA256: decision.SHA256}
	return e.decided, nil
}

// differs names the first field a decision repeats beside the digest that
// is not the pending record's, or returns the empty string.
func differs(dec *record.Decision, p *record.Pending) string {
	s := p.Binding.Subject
	switch {
	case dec.Planner != p.Planner:
		return "planner"
	case dec.Project != p.Binding.Project:
		return "project"
	case dec.Subject.Kind != s.Kind:
		return "subject.kind"
	case dec.Subject.RoleName != s.RoleName:
		return "subject.role_name"
	case dec.Subject.Epoch != s.Epoch:
		return "subject.epoch"
	case dec.Subject.SeatRev != s.SeatRev:
		return "subject.seat_rev"
	case dec.PlanToken != p.Binding.PlanToken:
		return "plan_token"
	}
	return ""
}

// Use consumes an approval. All of it happens under the stand-in's one
// lock, so of two uses at once one is refused. Its checks, in order: the
// id (approval-void); the state (approval-consumed for a record that was
// used, approval-expired, previous-epoch, approval-void); the fresh refs,
// review digest and rule-set digest against the binding, and the plan made
// again against the bound plan digest (binding-mismatch; the record becomes
// void); the evidence (evidence-changed; the record becomes void).
func (d *Door) Use(ctx context.Context, in door.UseInput) (door.Used, error) {
	if err := ctx.Err(); err != nil {
		return door.Used{}, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sweep(d.now())
	e := d.entries[in.ID]
	if e == nil {
		return door.Used{}, refuse(record.CodeApprovalVoid, "no pending record has this id")
	}
	if !usable(e.state) {
		return door.Used{}, ended(e)
	}
	b := &e.pending.Binding
	if what := stale(b, &in.Fresh); what != "" {
		e.move(record.StateVoid, record.CodeBindingMismatch)
		return door.Used{}, refuse(record.CodeBindingMismatch, what+" is not what the approval is bound to")
	}
	if record.PlanDigest(planText(d.command, b)) != b.PlanSHA256 {
		e.move(record.StateVoid, record.CodeBindingMismatch)
		return door.Used{}, refuse(record.CodeBindingMismatch, "the plan made again is not the plan that was approved")
	}
	if !evidenceStands(b.Evidence, in.Fresh.Evidence) {
		e.move(record.StateVoid, record.CodeEvidenceChanged)
		return door.Used{}, refuse(record.CodeEvidenceChanged, "a bound evidence record is missing, changed or no longer the newest")
	}
	e.move(record.StateConsumed, "")
	return door.Used{
		State:         record.StateConsumed,
		BindingSHA256: e.pending.BindingSHA256,
		Effects:       cloneEffects(b.Effects),
	}, nil
}

// stale names the first fresh value that differs from the binding, or
// returns the empty string. Every ref an effect names must be among the
// fresh refs, at the effect's old value, each time it is named.
func stale(b *record.Binding, fresh *door.Fresh) string {
	for _, effect := range b.Effects {
		found := false
		for _, ref := range fresh.Refs {
			if ref.Ref != effect.Ref {
				continue
			}
			if ref.Value != effect.Old {
				return "a ref's value"
			}
			found = true
		}
		if !found {
			return "a ref's value (it was not given)"
		}
	}
	review, rules := "", ""
	if b.Summary != nil {
		review, rules = b.Summary.ReviewDigest, b.Summary.ExecConfigSet.SHA256
	}
	switch {
	case fresh.ReviewDigest != review:
		return "the review digest"
	case fresh.RuleSetSHA256 != rules:
		return "the rule-set digest"
	}
	return ""
}

// evidenceStands reports whether every bound evidence record is among the
// fresh ones with the same digest and still the newest.
func evidenceStands(bound []record.Evidence, fresh []door.EvidenceState) bool {
	for _, want := range bound {
		ok := false
		for _, got := range fresh {
			if got.RecordID != want.RecordID {
				continue
			}
			if got.RecordSHA256 != want.RecordSHA256 || !got.Newest {
				return false
			}
			ok = true
		}
		if !ok {
			return false
		}
	}
	return true
}

// Settle records the outcome of a used approval. Its checks, in order: the
// outcome (record-invalid for a word that is not one of the three); the id
// (approval-void); the binding digest (binding-mismatch). A record already
// in the outcome's state answers the same again. A consumed record takes
// any outcome, and an unknown one takes applied or not-applied. Anything
// else is refused by the record's state: approval-consumed for a record
// settled otherwise, approval-expired, previous-epoch or approval-void for
// one that was never used.
func (d *Door) Settle(ctx context.Context, in door.SettleInput) (door.Settled, error) {
	if err := ctx.Err(); err != nil {
		return door.Settled{}, err
	}
	if !in.Outcome.Valid() {
		return door.Settled{}, refuse(record.CodeRecordInvalid, "the outcome is not applied, not-applied or unknown")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sweep(d.now())
	e := d.entries[in.ID]
	if e == nil {
		return door.Settled{}, refuse(record.CodeApprovalVoid, "no pending record has this id")
	}
	if in.BindingSHA256 != e.pending.BindingSHA256 {
		return door.Settled{}, refuse(record.CodeBindingMismatch, "the digest is not the digest of the record's binding")
	}
	to := in.Outcome.State()
	if e.state != to {
		if !mayMove(e.state, to) {
			return door.Settled{}, ended(e)
		}
		e.move(to, "")
	}
	return door.Settled{State: e.state}, nil
}

// List returns one entry for every pending record, in the order they were
// written.
func (d *Door) List(ctx context.Context) ([]door.Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sweep(d.now())
	out := make([]door.Entry, 0, len(d.order))
	for _, id := range d.order {
		out = append(out, d.entries[id].line())
	}
	return out, nil
}

// Show returns one pending record with its entry and its decision. An id
// with no record is refused approval-void.
func (d *Door) Show(ctx context.Context, id string) (door.Shown, error) {
	if err := ctx.Err(); err != nil {
		return door.Shown{}, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sweep(d.now())
	e := d.entries[id]
	if e == nil {
		return door.Shown{}, refuse(record.CodeApprovalVoid, "no pending record has this id")
	}
	return door.Shown{Entry: e.line(), Pending: clone(e.pendingRec), Decision: clone(e.decisionRec)}, nil
}

// line returns e as an entry of the listing.
func (e *entry) line() door.Entry {
	out := door.Entry{
		ID:            e.pending.ID,
		RequestID:     e.pending.RequestID,
		Verb:          e.pending.Binding.Verb,
		State:         e.state,
		ReasonCode:    e.reason,
		ExpiresAt:     e.pending.Binding.PendingExpiresAt,
		BindingSHA256: e.pending.BindingSHA256,
		PendingSHA256: e.pendingRec.SHA256,
	}
	if e.decision != nil {
		out.DecisionSHA256 = e.decisionRec.SHA256
		if e.decision.Decision == record.DecisionApprove {
			out.ExpiresAt = e.decision.ExpiresAt
		}
	}
	return out
}

// decodeRequest checks a request's bytes against their digest and reads
// them.
func decodeRequest(r door.Record) (record.Request, error) {
	if record.Digest(r.Bytes) != r.SHA256 {
		return record.Request{}, refuse(record.CodeRequestInvalid, "the request's bytes do not have the digest given with them")
	}
	return record.DecodeRequest(r.Bytes)
}

// seatTakes checks a request against the seat: the role has an assignment,
// and the request names the seat's present epoch.
func (d *Door) seatTakes(req *record.Request) error {
	switch {
	case d.seat.Unassigned:
		return refuse(record.CodeNotAssigned, "the role has no assignment")
	case req.Epoch != d.seat.Epoch:
		return refuse(record.CodePreviousEpoch, "request.epoch is not the seat's epoch")
	}
	return nil
}

// targetOf returns the key of what a request is for: its verb and its
// arguments. Two requests with one key ask for the same thing.
func targetOf(req *record.Request) string {
	return req.Verb + "\x00" + strings.Join(req.Argv, "\x00")
}

// throttle reports whether a request for target is refused by the repeat
// throttle at now, and starts the throttle when the request is one too
// many.
func (d *Door) throttle(target string, now time.Time) bool {
	if until, ok := d.throttled[target]; ok {
		if now.Before(until) {
			return true
		}
		// The throttle is over, and the count starts again.
		delete(d.throttled, target)
		delete(d.asked, target)
	}
	d.asked[target] = since(d.asked[target], now.Add(-RepeatWindow))
	if len(d.asked[target]) >= RepeatLimit {
		d.throttled[target] = now.Add(ThrottleFor)
		return true
	}
	return false
}

// since returns the times of ts that are after t.
func since(ts []time.Time, t time.Time) []time.Time {
	out := ts[:0]
	for _, at := range ts {
		if at.After(t) {
			out = append(out, at)
		}
	}
	return out
}

// clone returns r with bytes of its own, so that a caller cannot change
// what the stand-in keeps, nor the stand-in what a caller holds.
func clone(r door.Record) door.Record {
	return door.Record{Bytes: bytes.Clone(r.Bytes), SHA256: r.SHA256}
}

// cloneEffects returns a copy of effects that shares nothing with it.
func cloneEffects(effects []record.Effect) []record.Effect {
	out := make([]record.Effect, len(effects))
	for i, e := range effects {
		out[i] = e
		if e.MergedInto != nil {
			merged := *e.MergedInto
			out[i].MergedInto = &merged
		}
	}
	return out
}
