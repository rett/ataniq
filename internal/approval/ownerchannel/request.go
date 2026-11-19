package ownerchannel

import (
	"context"
	"errors"
	"strings"

	"ataniq/internal/approval/card"
	"ataniq/internal/approval/door"
	"ataniq/internal/approval/limits"
	"ataniq/internal/approval/ownerjournal"
	"ataniq/internal/approval/record"
)

// RequestState is where a request stands before it has a pending record,
// or that it has one. The states before the record are the daemon's, as the
// design lists them (collected, verifying, refused).
type RequestState string

// The states of a request.
const (
	// RequestCollected: the daemon read the request and it waits for its
	// turn, or the door is looking at it.
	RequestCollected RequestState = "collected"
	// RequestVerifying: the host is building the merge, running the suite
	// on it and reviewing it. One request does this at a time.
	RequestVerifying RequestState = "verifying"
	// RequestRefused: the request ended with a code and a reply, and has
	// no card.
	RequestRefused RequestState = "refused"
	// RequestPending: the door wrote the pending record, and the card
	// waits for the owner.
	RequestPending RequestState = "pending"
)

// RequestStatus is the state of one request.
type RequestStatus struct {
	RequestID string
	State     RequestState
	// PendingID is the pending record's id, when State is pending.
	PendingID string
	// Code and Detail say why a refused request was refused. Detail is
	// host-written.
	Code   record.Code
	Detail string
}

// maxStatus is how many request states are kept.
const maxStatus = 256

// Status returns the state of a request this service has handled since it
// started. A request is forgotten when 256 newer ones have been handled.
func (s *Service) Status(requestID string) (RequestStatus, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.status[requestID]
	return st, ok
}

func (s *Service) setStatus(st RequestStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.status[st.RequestID]; !ok {
		s.statusOrder = append(s.statusOrder, st.RequestID)
		if len(s.statusOrder) > maxStatus {
			delete(s.status, s.statusOrder[0])
			s.statusOrder = s.statusOrder[1:]
		}
	}
	s.status[st.RequestID] = st
}

// pinKey makes the key of a request's pin from the digest of the request's
// bytes, which the door gave. It is host-made: the lead's request id is
// never a ref name.
func pinKey(requestSHA256 string) string {
	if len(requestSHA256) > 32 {
		requestSHA256 = requestSHA256[:32]
	}
	return requestSHA256
}

// Submit takes one request through the host's steps: the door admits it,
// the host builds the merge, runs the suite on it, reviews it, and the door
// plans it into a pending record. One request is built and verified at a
// time; the others wait in the state collected.
//
// A request that ends without a card is refused with a code, answered in
// the lead's mailbox, counted, and returned as a status in the state
// refused: that is a result, not an error. An error is a failure the host
// could not answer for (the journal, a call its caller cancelled, the door
// out of reach); the request then has no reply. A request that [Service.Close]
// stops while it waits for its turn or is being verified is not that: it is
// refused request-invalid with a line that says the host stopped, answered
// and counted like any refusal.
//
// Anything but a clean verification refuses the request verification-failed
// and makes no card. A request that fails before its plan is ended at the
// door through [Config.Ender] when there is one.
func (s *Service) Submit(ctx context.Context, in Collected) (RequestStatus, error) {
	ctx, done, err := s.begin(ctx)
	if err != nil {
		return RequestStatus{}, err
	}
	defer done()

	req, err := record.DecodeRequest(in.Request)
	if err != nil {
		c, _ := code(err)
		return RequestStatus{State: RequestRefused, Code: c, Detail: detailOf(err)}, nil
	}
	if in.Project == "" || in.At.IsZero() {
		return RequestStatus{}, errors.New("ownerchannel: a collected request names its project and the time it was collected")
	}
	r := &flow{s: s, in: in, req: req}
	r.status = RequestStatus{RequestID: req.RequestID, State: RequestCollected}
	s.setStatus(r.status)

	adm, err := s.cfg.Door.Admit(ctx, door.RecordOf(in.Request))
	if err != nil {
		if e, ok := doorRefusal(err); ok {
			// The door took nothing: there is no request to end.
			return r.finish(ctx, e.Code, e.Detail)
		}
		return RequestStatus{}, wrapDoor(err)
	}
	r.adm = adm
	if err := s.fault("admitted"); err != nil {
		return RequestStatus{}, err
	}
	if req.Verb != record.VerbFeatureFinish {
		return r.refuse(ctx, record.CodeVerbNotRequestable, "this service handles a feature finish only")
	}

	select {
	case s.slot <- struct{}{}:
		defer func() { <-s.slot }()
	case <-ctx.Done():
		return r.failed(ctx, ctx.Err(), "")
	}
	r.status.State = RequestVerifying
	s.setStatus(r.status)
	return r.build(ctx)
}

// flow is one request on its way.
type flow struct {
	s      *Service
	in     Collected
	req    record.Request
	adm    door.Admission
	status RequestStatus
	// pinned is the merge commit that holds the pin, when one does.
	pinned string
}

func (f *flow) key() string { return pinKey(f.adm.RequestSHA256) }

// build runs the steps that take the one verification slot.
func (f *flow) build(ctx context.Context) (RequestStatus, error) {
	s := f.s
	// The name is checked before the intake is asked to resolve anything
	// from it. Decoding the request checked it already; this is the check
	// of the one place that hands it on.
	if len(f.req.Argv) != 1 || !record.ValidFeatureName(f.req.Argv[0]) {
		return f.refuse(ctx, record.CodeFeatureName, "the feature name is not 1 to 48 lower-case letters, digits and dashes")
	}
	res, err := s.cfg.Intake.Resolve(ctx, ResolveInput{Project: f.in.Project, Request: f.req})
	if err != nil {
		return f.failed(ctx, err, "the host could not read the feature and the target")
	}
	if res.FeatureTip != f.req.FeatureSHA {
		return f.refuse(ctx, record.CodeFeatureMoved, "the feature's tip is not the one the request names")
	}
	built, err := s.cfg.Merger.Build(ctx, MergeInput{
		Project:    f.in.Project,
		Feature:    f.req.Argv[0],
		Target:     strings.TrimPrefix(res.TargetRef, "refs/heads/"),
		TargetTip:  res.TargetTip,
		FeatureTip: res.FeatureTip,
		RequestID:  f.req.RequestID,
		Collected:  f.in.At,
		Key:        f.key(),
	})
	switch {
	case err == nil:
		f.pinned = built.Commit
	case errors.Is(err, ErrPinUnclear):
		// The merge came back, but the pin may or may not be there. Look,
		// and take away whatever is, before going on.
		f.pinned = built.Commit
		f.unpin(ctx)
		return f.refuse(ctx, record.CodeRequestInvalid, "the host could not pin the merge")
	case errors.Is(err, ErrNothingToMerge):
		return f.refuse(ctx, record.CodeRequestInvalid, "the target already holds the feature")
	default:
		if e, ok := doorRefusal(err); ok {
			return f.refuse(ctx, e.Code, e.Detail)
		}
		return f.failed(ctx, err, "the host could not build the merge")
	}
	if err := s.fault("merge-built"); err != nil {
		return RequestStatus{}, err
	}

	v, err := s.cfg.Verifier.Verify(ctx, VerifyInput{
		Project: f.in.Project, RequestID: f.req.RequestID, Merge: built.Commit, Base: res.TargetTip,
	})
	if ctx.Err() != nil {
		return f.failed(ctx, ctx.Err(), "")
	}
	if err != nil || !v.Clean || !suiteEvidence(v.Evidence) {
		return f.refuse(ctx, record.CodeVerificationFailed, verifyDetail(err, v))
	}
	if err := s.fault("verified"); err != nil {
		return RequestStatus{}, err
	}

	summary, err := s.cfg.Intake.Review(ctx, ReviewInput{Project: f.in.Project, Base: res.TargetTip, Merge: built.Commit})
	if err != nil {
		return f.failed(ctx, err, "the host could not review the merge")
	}
	if summary.Base != res.TargetTip || summary.Tip != built.Commit {
		return f.refuse(ctx, record.CodeBindingMismatch, "the review is not of the merge that was built")
	}
	now, err := s.cfg.Intake.TargetTip(ctx, f.in.Project, res.TargetRef)
	if err != nil {
		return f.failed(ctx, err, "the host could not read the target's tip")
	}

	rec, err := s.cfg.Door.Plan(ctx, door.PlanInput{
		Request:      door.RecordOf(f.in.Request),
		Note:         f.in.Note,
		TargetRef:    res.TargetRef,
		Base:         res.TargetTip,
		Feature:      res.FeatureTip,
		Merge:        built.Commit,
		Summary:      &summary,
		Evidence:     []record.Evidence{v.Evidence},
		MergeParents: []string{built.Parents[0], built.Parents[1]},
		TargetNow:    now,
	})
	if err != nil {
		if e, ok := doorRefusal(err); ok {
			return f.refuse(ctx, e.Code, e.Detail)
		}
		// The plan may have been written. The pin stays: the pending record
		// needs the merge, and the next tick finds the record in the door's
		// list.
		return RequestStatus{}, wrapDoor(err)
	}
	p, err := s.remember(rec)
	if err != nil {
		return RequestStatus{}, err
	}
	if err := s.fault("planned"); err != nil {
		return RequestStatus{}, err
	}
	if err := s.countPending(ctx, f.in.Project, f.req.RequestID, p, false); err != nil {
		return RequestStatus{}, err
	}
	f.status = RequestStatus{RequestID: f.req.RequestID, State: RequestPending, PendingID: p.ID}
	s.setStatus(f.status)
	return f.status, nil
}

// suiteEvidence reports whether e is a well-formed suite record.
func suiteEvidence(e record.Evidence) bool {
	return e.Kind == record.EvidenceSuite && e.RecordID != "" && record.IsLowerHex(e.RecordSHA256, 64)
}

// verifyDetail makes the line for a verification that was not clean, from
// the closed list of words.
func verifyDetail(err error, v Verification) string {
	word := v.Detail
	switch word {
	case VerifyFailed, VerifyStopped, VerifyOutputCut, VerifyCouldNotRun:
	default:
		word = VerifyCouldNotRun
	}
	if err != nil {
		word = VerifyCouldNotRun
	}
	return "the suite did not pass cleanly on the merge: " + word
}

// failed ends a request for a failure of the host. A call its caller
// cancelled is ended at the door and returned as it is, with no reply. A
// call that [Service.Close] cancelled is refused like any other: the lead
// gets a reply, and the request is counted, so that a request does not
// disappear because the service stopped. Any other failure refuses the
// request request-invalid with the host-written reason, since the design has
// no code for the host's own failure (a point for the joint contract).
func (f *flow) failed(ctx context.Context, err error, reason string) (RequestStatus, error) {
	if ctx.Err() != nil && f.s.ctx.Err() != nil {
		return f.refuse(ctx, record.CodeRequestInvalid, "the host stopped before it was done with the request")
	}
	if ctx.Err() != nil {
		f.unpin(ctx)
		f.end(ctx)
		return RequestStatus{}, err
	}
	return f.refuse(ctx, record.CodeRequestInvalid, reason)
}

// refuse ends a request that the door admitted: it takes away the pin, ends
// the request at the door, and finishes it with the code.
func (f *flow) refuse(ctx context.Context, c record.Code, detail string) (RequestStatus, error) {
	f.unpin(ctx)
	f.end(ctx)
	return f.finish(ctx, c, detail)
}

// finish counts a refused request, answers the lead and records the state.
func (f *flow) finish(ctx context.Context, c record.Code, detail string) (RequestStatus, error) {
	s := f.s
	ctx = context.WithoutCancel(ctx)
	f.status = RequestStatus{RequestID: f.req.RequestID, State: RequestRefused, Code: c, Detail: detail}
	s.setStatus(f.status)
	reason := "no card: " + string(c)
	if err := s.appendCount(ctx, ownerjournal.Count{
		Project: f.in.Project, At: s.now(), RequestID: f.req.RequestID, Reason: reason,
	}, false); err != nil {
		return f.status, err
	}
	s.reply(ctx, Reply{Project: f.in.Project, RequestID: f.req.RequestID, Code: c, Line: line(detail)})
	return f.status, nil
}

// end ends the request at the door, when the door has a way to.
func (f *flow) end(ctx context.Context) {
	if f.s.cfg.Ender == nil {
		return
	}
	if err := f.s.cfg.Ender.EndRequest(context.WithoutCancel(ctx), f.req.RequestID); err != nil {
		f.s.notice("the request could not be ended at the door: " + line(err.Error()))
	}
}

// unpin looks at the request's pin and takes it away.
func (f *flow) unpin(ctx context.Context) {
	if f.pinned == "" {
		return
	}
	f.s.unpin(ctx, f.key())
	f.pinned = ""
}

// unpin takes away the pin of key when there is one: whatever commit it
// holds, since the key is the host's own.
func (s *Service) unpin(ctx context.Context, key string) {
	ctx = context.WithoutCancel(ctx)
	at, err := s.cfg.Merger.Pinned(ctx, key)
	if err != nil {
		s.notice("the pin could not be read: " + line(err.Error()))
		return
	}
	if at == "" {
		return
	}
	if err := s.cfg.Merger.Unpin(ctx, key, at); err != nil {
		s.notice("the pin could not be removed: " + line(err.Error()))
	}
}

// countPending journals, for a request that has a pending record, whether
// it would have met the terms of the first standing approval.
func (s *Service) countPending(ctx context.Context, project, requestID string, p record.Pending, locked bool) error {
	c := ownerjournal.Count{Project: project, At: s.now(), RequestID: requestID, PendingID: p.ID}
	if in, ok := card.BindingCountInput(p.Binding); ok {
		counted := limits.Count(in)
		c.Met, c.Reason = counted.Met, counted.Reason
	} else {
		c.Reason = "a prune has no summary to count"
	}
	return s.appendCount(ctx, c, locked)
}

// appendCount journals a count. A reason longer than the journal takes is
// cut. locked says that the caller holds dmu.
func (s *Service) appendCount(ctx context.Context, c ownerjournal.Count, locked bool) error {
	c.Reason = line(c.Reason)
	if len(c.Reason) > 500 {
		c.Reason = c.Reason[:500]
	}
	if err := s.journal().AppendCount(c); err != nil {
		if locked {
			return s.jerr(ctx, err)
		}
		return s.jfix(ctx, err)
	}
	return nil
}

// reply puts a reply in the lead's mailbox. A failure is a notice: the
// request has ended whatever the mailbox says, and the next tick tries
// again for the ones that ended with a pin to take away.
func (s *Service) reply(ctx context.Context, r Reply) bool {
	if err := s.cfg.Mailbox.Reply(ctx, r); err != nil {
		s.notice("a reply to the lead's mailbox failed: " + line(err.Error()))
		return false
	}
	return true
}

// remember decodes a pending record the door wrote and keeps it.
func (s *Service) remember(rec door.Record) (record.Pending, error) {
	p, err := record.DecodePending(rec.Bytes)
	if err != nil {
		return record.Pending{}, err
	}
	s.mu.Lock()
	s.pending[p.ID] = pendingCache{sha: rec.SHA256, bytes: append([]byte(nil), rec.Bytes...), pending: p}
	s.mu.Unlock()
	return p, nil
}

// wrapDoor says that a call to the door failed, without saying what the
// door did.
func wrapDoor(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return errors.Join(ErrDoorUnavailable, err)
}

// detailOf returns the host-written detail of a refusal.
func detailOf(err error) string {
	if e, ok := doorRefusal(err); ok {
		return e.Detail
	}
	return ""
}

// line makes a one-line host-written text of s: no control character, at
// most 500 bytes, cut on a character boundary.
func line(s string) string {
	b := make([]rune, 0, len(s))
	for _, r := range s {
		if record.IsControl(r) {
			r = ' '
		}
		b = append(b, r)
		if len(b) >= 500 {
			break
		}
	}
	out := string(b)
	for len(out) > 500 {
		b = b[:len(b)-1]
		out = string(b)
	}
	return out
}
