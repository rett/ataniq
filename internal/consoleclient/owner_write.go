package consoleclient

import (
	"encoding/json"

	"ataniq/internal/approval/card"
	"ataniq/internal/approval/limits"
	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/approval/record"
)

// This file holds the operations of [Owner] that change something: the
// decision, and the limit.

// jsonRaw marks bytes the wire package wrote as the params of a call, so that
// they go out as written.
func jsonRaw(b []byte) json.RawMessage { return json.RawMessage(b) }

// Decide sends the owner's decision once and returns the outcome the daemon
// answers with. The frame carries what the owner did and nothing else: the
// card, the digest of the binding the card showed, approve or deny, the
// sign-offs and a deny's reason. A decision the wire package refuses is
// refused here, and nothing is sent.
//
// It goes out on the connection that read the card, and only on it. When
// that connection has ended, the decision is dropped: nothing is sent and
// the error is [ErrConnectionLost]. When it was sent and no answer came back,
// the card is in doubt: the error is the failure, and a second decision on
// the card is refused with [ErrDecisionUnsettled] until the card is opened
// again. A card the daemon already answered with a final state is answered
// again with that outcome, and nothing is sent.
//
// The outcome is what the daemon said, its line cleaned.
func (o *Owner) Decide(d card.Decision) (card.Outcome, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if out, ok := o.settled[d.ID]; ok {
		return out, nil
	}
	ref, ok := o.cards[d.ID]
	if !ok {
		return card.Outcome{}, ErrCardNotOpen
	}
	if o.doubt[d.ID] {
		return card.Outcome{}, ErrDecisionUnsettled
	}
	if o.closed.Load() {
		return card.Outcome{}, ErrOwnerClosed
	}
	if o.conn == nil || o.conn.gen != ref.gen {
		return card.Outcome{}, ErrConnectionLost
	}
	frame := ownerwire.DecisionFrame{
		ID:            d.ID,
		BindingSHA256: d.BindingSHA256,
		Decision:      record.DecisionDeny,
		SignedOff:     append([]record.SignOff{}, d.SignedOff...),
		Reason:        d.Reason,
	}
	if d.Approve {
		frame.Decision = record.DecisionApprove
	}
	params, err := ownerwire.EncodeDecisionFrame(frame)
	if err != nil {
		return card.Outcome{}, err
	}
	raw, err := o.call(ownerwire.OpDecide, jsonRaw(params))
	if err != nil {
		if _, refused := err.(*record.Error); !refused {
			// The frame may have gone out, and it may have been recorded.
			o.doubt[d.ID] = true
		}
		return card.Outcome{}, err
	}
	res, err := ownerwire.DecodeDecideResult(raw)
	if err != nil {
		o.doubt[d.ID] = true
		return card.Outcome{}, o.badAnswer()
	}
	out := card.Outcome{State: res.State, Line: clean(res.Line), Held: res.Held}
	if out.State != record.StatePending {
		o.settled[d.ID] = out
	}
	return out, nil
}

// Limit reads a project's daily card limit.
func (o *Owner) Limit(project string) (limits.View, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	params, err := ownerwire.EncodeLimitParams(ownerwire.LimitParams{Project: project})
	if err != nil {
		return limits.View{}, err
	}
	raw, err := o.ask(ownerwire.OpLimit, jsonRaw(params))
	if err != nil {
		return limits.View{}, err
	}
	return o.limitOf(raw, project)
}

// SetLimit asks the daemon to change a project's limit. The rules are the
// daemon's: a change it refuses comes back as a *record.Error with the code
// limit-refused and the reason of the refusal, and no view.
func (o *Owner) SetLimit(project string, c limits.Change) (limits.View, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	params, err := ownerwire.EncodeLimitSetParams(ownerwire.LimitSetParams{Project: project, To: c.To, Typed: c.Typed})
	if err != nil {
		return limits.View{}, err
	}
	raw, err := o.ask(ownerwire.OpLimitSet, jsonRaw(params))
	if err != nil {
		return limits.View{}, err
	}
	return o.limitOf(raw, project)
}

// limitOf decodes a limit's view and checks that it is the project asked
// for. The caller holds o.mu.
func (o *Owner) limitOf(raw json.RawMessage, project string) (limits.View, error) {
	v, err := ownerwire.DecodeLimitView(raw)
	if err != nil || v.Project != project {
		return limits.View{}, o.badAnswer()
	}
	out := limits.View{Project: v.Project, Limit: v.Limit, Suggestion: v.Suggestion, RaisedToday: v.RaisedToday}
	for _, d := range v.Days {
		out.Days = append(out.Days, limits.Day{Date: d.Date, Uses: d.Uses, ReachedLimit: d.ReachedLimit, Alert: d.Alert})
	}
	return out, nil
}
