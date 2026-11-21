//go:build linux && ataniq_ownerchannel

package hostadapter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"ataniq/internal/approval/door"
	"ataniq/internal/approval/ownerchannel"
	"ataniq/internal/approval/record"
	"ataniq/internal/intake"
	"ataniq/internal/intake/execconfig"
)

// Fresh implements [ownerchannel.Intake]: what the door compares with the
// binding just before a use. The refs are read by their exact names; the
// review is made again, in merge mode, and its digest is returned; the digest
// of the rule set is the one in force now; each bound verification record is
// read again from the records folder.
//
// A review that cannot be made because the target moved or the pin is gone
// has no digest here, and the door then refuses the use as it refuses any
// mismatch. A failure to read the mirror or the records is an error, and
// nothing is used.
func (h *Host) Fresh(ctx context.Context, in ownerchannel.FreshInput) (door.Fresh, error) {
	p, err := h.project(in.Project)
	if err != nil {
		return door.Fresh{}, err
	}
	b := in.Pending.Binding
	out := door.Fresh{RuleSetSHA256: execconfig.Digest(p.Rules)}
	names := make([]string, 0, len(b.Effects))
	for _, e := range b.Effects {
		names = append(names, e.Ref)
	}
	got, err := h.gitCall(p).refs(ctx, names...)
	if err != nil {
		return door.Fresh{}, err
	}
	for _, e := range b.Effects {
		if info, ok := got[e.Ref]; ok {
			out.Refs = append(out.Refs, door.RefValue{Ref: e.Ref, Value: info.id})
		}
	}
	if b.Summary != nil {
		rev, err := h.inspect(ctx, p, b.Summary.Tip)
		switch {
		case err == nil:
			out.ReviewDigest = rev.Digest()
		default:
			if _, moved := changedError(err, record.CodeReviewChanged); !moved {
				return door.Fresh{}, mapReviewError(err, record.CodeReviewChanged)
			}
		}
	}
	if len(b.Evidence) > 0 {
		if b.Merge == nil {
			return door.Fresh{}, errors.New("hostadapter: a binding with evidence has no merge")
		}
		recs, err := h.suiteRecords(in.Project, b.Merge.Commit)
		if err != nil {
			return door.Fresh{}, err
		}
		for _, e := range b.Evidence {
			st := door.EvidenceState{RecordID: e.RecordID}
			if i := slices.IndexFunc(recs, func(r suiteRecord) bool { return r.id == e.RecordID }); i >= 0 {
				st.RecordSHA256 = recs[i].sha
				st.Newest = i == len(recs)-1
			}
			out.Evidence = append(out.Evidence, st)
		}
	}
	return out, nil
}

// suiteRecord is a verification record with its identity as evidence.
type suiteRecord struct {
	id, sha string
	rec     intake.VerificationRecord
}

// recordIdentity makes the id and the digest a verification record has as
// evidence: the digest is the SHA-256 of the record as the intake's store
// writes it, and the id is made from it, so that a record that was changed
// is not the one that was bound.
func recordIdentity(rec intake.VerificationRecord) (id, sha string, err error) {
	b, err := json.Marshal(rec)
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256(b)
	sha = hex.EncodeToString(sum[:])
	return "suite-" + sha[:16], sha, nil
}

// suiteRecords returns the records of the project for the tip, oldest first.
func (h *Host) suiteRecords(project, tip string) ([]suiteRecord, error) {
	all, err := h.store.Verifications(tip)
	if err != nil {
		return nil, err
	}
	var out []suiteRecord
	for _, r := range all {
		if r.Project != project {
			continue
		}
		id, sha, err := recordIdentity(r)
		if err != nil {
			return nil, err
		}
		out = append(out, suiteRecord{id: id, sha: sha, rec: r})
	}
	return out, nil
}

// Apply implements [ownerchannel.Intake]: it moves the target from the old
// value to the merge in one transaction, through [intake.Integrate], and
// touches no other ref, no checkout and no hook.
//
// The result says what happened to the ref. A move that the intake refused
// before it wrote anything, the target having moved among them, is
// not-applied, and nothing changed. A move that is certain is applied, also
// when the intake's record of it was written with a warning. A move whose
// outcome could not be read afterwards, and any failure the adapter cannot
// place, is unknown, with the error, and the service waits for the owner.
func (h *Host) Apply(ctx context.Context, in ownerchannel.ApplyInput) (door.Outcome, error) {
	p, err := h.project(in.Project)
	if err != nil {
		return door.OutcomeUnknown, err
	}
	if len(in.Effects) != 1 {
		return door.OutcomeUnknown, errors.New("hostadapter: a feature finish has one effect")
	}
	e := in.Effects[0]
	if e.Ref != p.Target || !record.ValidObjectID(e.Old) || !record.ValidObjectID(e.New) || isZeroID(e.Old) || isZeroID(e.New) {
		return door.OutcomeUnknown, errors.New("hostadapter: the effect is not a move of the project's target")
	}
	rev, err := h.inspect(ctx, p, e.New)
	if err != nil {
		if _, moved := changedError(err, record.CodeReviewChanged); moved {
			return door.OutcomeNotApplied, nil
		}
		return door.OutcomeUnknown, err
	}
	if rev.Base != e.Old || rev.Tip != e.New {
		return door.OutcomeNotApplied, nil
	}
	// The door has checked the owner's sign-offs against the flagged list;
	// the intake checks them again against the review it makes itself, so
	// the sign-off handed over names every flagged path of that review.
	paths := make([]string, 0, len(rev.Flagged))
	for _, f := range rev.Flagged {
		if !slices.Contains(paths, f.Path) {
			paths = append(paths, f.Path)
		}
	}
	_, err = intake.Integrate(ctx, h.source(p), intake.IntegrateRequest{
		Project: p.Name,
		Review:  intake.Request{BaseRef: p.Target, TipRef: rev.TipRef, Rules: p.Rules, Merge: true, Limits: h.cfg.Limits},
		SignOff: intake.SignOff{Tip: rev.Tip, Digest: rev.Digest(), Change: true, Paths: paths},
		Store:   h.store,
		Now:     h.cfg.now,
	})
	var se *intake.SignOffError
	var re *intake.RecordError
	switch {
	case err == nil:
		return door.OutcomeApplied, nil
	case errors.Is(err, intake.ErrOutcomeUnknown):
		return door.OutcomeUnknown, err
	case errors.As(err, &re), errors.Is(err, intake.ErrRecovered), errors.Is(err, intake.ErrNotSynced):
		// The target moved. The intake's own record is missing or was not
		// synced: a warning for the operator, not a doubt about the ref.
		h.cfg.notice("the target moved, and the intake's record of it came with a warning")
		return door.OutcomeApplied, nil
	case errors.Is(err, intake.ErrMoved), errors.Is(err, intake.ErrNothingToIntegrate), errors.Is(err, intake.ErrNoRef),
		errors.Is(err, intake.ErrNotVerified), errors.Is(err, intake.ErrNotMerge), errors.Is(err, intake.ErrAttributes),
		errors.As(err, &se):
		// Refused before anything was written.
		return door.OutcomeNotApplied, nil
	}
	return door.OutcomeUnknown, err
}

// isZeroID reports whether an object id is all zeros, which stands for no
// object: a move from it creates a ref and a move to it deletes one.
func isZeroID(id string) bool { return strings.Trim(id, "0") == "" }

// Mirror implements [ownerchannel.Intake]: what the mirror shows for the
// effects. Every ref at its new value is applied, every ref at its old value
// is not-applied, and anything else (a ref elsewhere, a ref gone, a list of
// effects that is not one) is unknown. It writes nothing and never applies.
func (h *Host) Mirror(ctx context.Context, in ownerchannel.ApplyInput) (door.Outcome, error) {
	p, err := h.project(in.Project)
	if err != nil {
		return door.OutcomeUnknown, err
	}
	if len(in.Effects) == 0 {
		return door.OutcomeUnknown, nil
	}
	names := make([]string, 0, len(in.Effects))
	for _, e := range in.Effects {
		names = append(names, e.Ref)
	}
	got, err := h.gitCall(p).refs(ctx, names...)
	if err != nil {
		return door.OutcomeUnknown, err
	}
	atNew, atOld := 0, 0
	for _, e := range in.Effects {
		info, ok := got[e.Ref]
		switch {
		case !ok:
		case info.id == e.New && info.id != e.Old:
			atNew++
		case info.id == e.Old && info.id != e.New:
			atOld++
		}
	}
	switch {
	case atNew == len(in.Effects):
		return door.OutcomeApplied, nil
	case atOld == len(in.Effects):
		return door.OutcomeNotApplied, nil
	}
	return door.OutcomeUnknown, nil
}
