package intake

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"ataniq/internal/exchange/gitenv"
	"ataniq/internal/exchange/leadtext"
	"ataniq/internal/exchange/sandboxplan"
)

// Errors of [Integrate]. Test them with errors.Is.
var (
	// ErrNotVerified means that the newest verification record for the tip
	// is missing, or is not a clean pass. A later failed run withdraws an
	// earlier pass.
	ErrNotVerified = errors.New("intake: the tip has no passing verification")
	// ErrMoved means that the lead branch or the upstream branch is not
	// where the owner looked when the sign-off was made, or that the
	// upstream branch moved while the integration was under way. Nothing
	// has been moved by the integration: the upstream branch was found at
	// neither the base nor the tip, or is gone.
	ErrMoved = errors.New("intake: a branch moved after the sign-off")
	// ErrOutcomeUnknown means that the update of the upstream branch failed
	// and the branch could not be read afterwards: it may have moved, and no
	// record was written. The owner must look at the branch and decide.
	ErrOutcomeUnknown = errors.New("intake: the update failed and the outcome is unknown")
	// ErrNothingToIntegrate is matched by the error of an integration whose
	// lead branch is at the target already.
	ErrNothingToIntegrate = errors.New("nothing to integrate")
	// ErrRecordMissing is matched by a *[RecordError].
	ErrRecordMissing = errors.New("intake: the branch moved but its record is missing")
)

// RecordError is returned when the upstream branch was moved and the intake
// record could not be written. The branch has moved and the record is
// missing. Record holds the record's content, so that it can be written again
// by hand; [Integrate] returns it as its first result too.
type RecordError struct {
	Record IntakeRecord
	Err    error
}

// Error says what happened and what is missing. It holds object ids and
// escaped ref names.
func (e *RecordError) Error() string {
	return fmt.Sprintf("intake: the branch %s moved from %s to %s but the intake record could not be written (the record is missing): %v",
		leadtext.Escape(e.Record.TargetRef), e.Record.Base, e.Record.Tip, e.Err)
}

// Unwrap returns the write error.
func (e *RecordError) Unwrap() error { return e.Err }

// Is matches [ErrRecordMissing].
func (e *RecordError) Is(target error) bool { return target == ErrRecordMissing }

// IntegrateRequest is one integration.
type IntegrateRequest struct {
	// Project is the name of the project ([sandboxplan.ValidName]). Only
	// verification records of this project count.
	Project string
	// Review names what to review: BaseRef is the target branch
	// refs/heads/<target>, TipRef the lead branch under refs/leads/.
	Review Request
	// SignOff is what the owner signed.
	SignOff SignOff
	// Store holds the verification records and receives the intake record.
	Store *Store
	// Now gives the time of the record. Nil means time.Now.
	Now func() time.Time

	// afterChecks, when set, is called after every check and before the
	// target is moved. Tests use it to move a ref at that moment.
	afterChecks func()
}

// Integrate moves the upstream branch to the tip of the lead branch, if the
// owner signed off this very review and the tip was verified, and records it.
// It is step 5 of the reviewed intake. In order:
//
//  1. It inspects again, with a fresh [Inspect] of req.Review, which reads
//     the refs as they are now. A tip that does not contain the target is
//     refused with a *[NotFastForwardError], which here also matches
//     [ErrMoved]: a review that was signed had a target that the tip
//     contained. A target that does not exist is [ErrNoRef]; this function
//     never creates a branch.
//  2. The fresh review's tip and digest must equal the sign-off's, else
//     [ErrMoved]; the error names the old and the new commit ids.
//  3. [Review.CheckSignOff] must pass.
//  4. The newest verification record of the store for this project and tip
//     must be a clean pass, else [ErrNotVerified]. Any passing record for
//     the project and the tip counts, whatever gates it ran: the plan of
//     the record is not compared with the project's gates. The gates that
//     were run are in the returned record (Verification.Gates and
//     Verification.PlanDigest), and the command that calls Integrate must
//     show them to the owner before the sign-off, with the review output,
//     and after it, with the result.
//  5. The target is moved with one compare-and-swap, git update-ref with the
//     old value being the base the review saw, in one transaction on standard
//     input. No reflog message is written: the intake record is the record.
//     It fails when the target is no longer at the base, and then the error
//     is [ErrMoved] and nothing has changed. After any failure of the update
//     (a status, a time-out, a cancelled context) the target is read again,
//     on a context of its own with a short time limit, because the update may
//     have been committed first. At the base: nothing moved, and the error is
//     the original one. At the tip: the branch moved, and the record is
//     written as in step 6, with no error if it was written; this cannot be
//     told apart from the same tip put there by someone else after the
//     inspection, and a record of a move that is there is the safe side. At
//     any other id, or gone: [ErrMoved]. Unreadable: an error that says that
//     the outcome is unknown, and that the branch may have moved.
//  6. The intake record is written. If that fails, the error is a
//     *[RecordError] that says the branch moved and the record is missing,
//     and carries the record. If the record is written but the folder could
//     not be synced afterwards, the record is there: Integrate returns it
//     with an error that matches [ErrNotSynced], a warning that the caller
//     shows, not a failure.
//
// It writes no ref but the target. It never creates, deletes or forces a
// ref, and runs no checkout, merge, pull, reset, worktree or push: the
// owner's working checkout is not touched.
func Integrate(ctx context.Context, src Source, req IntegrateRequest) (IntakeRecord, error) {
	if req.Store == nil {
		return IntakeRecord{}, errors.New("intake: no records store")
	}
	if !sandboxplan.ValidName(req.Project) {
		return IntakeRecord{}, errors.New("intake: the name of the project is not valid")
	}
	now := req.Now
	if now == nil {
		now = time.Now
	}
	rev, err := Inspect(ctx, src, req.Review)
	if err != nil {
		var nf *NotFastForwardError
		if errors.As(err, &nf) {
			return IntakeRecord{}, fmt.Errorf("%w: the sign-off is for tip %s; the target is now at %s and %s at %s, which does not contain it: %w",
				ErrMoved, req.SignOff.Tip, nf.Base, leadtext.Escape(req.Review.TipRef), nf.Tip, err)
		}
		return IntakeRecord{}, err
	}
	if rev.Base == rev.Tip {
		return IntakeRecord{}, fmt.Errorf("intake: the lead branch is at the target: %w", ErrNothingToIntegrate)
	}
	if rev.Tip != req.SignOff.Tip {
		return IntakeRecord{}, fmt.Errorf("%w: the sign-off is for tip %s and %s is now at %s",
			ErrMoved, req.SignOff.Tip, leadtext.Escape(rev.TipRef), rev.Tip)
	}
	if digest := rev.Digest(); digest != req.SignOff.Digest {
		return IntakeRecord{}, fmt.Errorf("%w: the sign-off is for review %s and the review of tip %s against the target at %s is now %s",
			ErrMoved, req.SignOff.Digest, rev.Tip, rev.Base, digest)
	}
	if err := rev.CheckSignOff(req.SignOff); err != nil {
		return IntakeRecord{}, err
	}
	ver, err := newestVerification(req.Store, req.Project, rev.Tip)
	if err != nil {
		return IntakeRecord{}, err
	}
	if req.afterChecks != nil {
		req.afterChecks()
	}
	lim, err := req.Review.Limits.resolved()
	if err != nil {
		return IntakeRecord{}, err
	}
	atTip, moveErr := newRunner(src, lim).moveTarget(ctx, rev.BaseRef, rev.Base, rev.Tip)
	if moveErr != nil && !atTip {
		return IntakeRecord{}, moveErr
	}
	signed := slices.Clone(req.SignOff.Paths)
	slices.Sort(signed)
	signed = slices.Compact(signed)
	rec := IntakeRecord{
		Version: recordVersion, Time: now().UTC(), Project: req.Project,
		TargetRef: rev.BaseRef, TipRef: rev.TipRef, Base: rev.Base, Tip: rev.Tip,
		ReviewDigest: rev.Digest(), Commits: len(rev.Commits), Paths: len(rev.Changes),
		SignedPaths: signed, Verification: ver,
	}
	if err := req.Store.putIntake(rec); err != nil {
		if errors.Is(err, ErrNotSynced) {
			// The record is in the folder; only its durability is in doubt.
			return rec, err
		}
		if moveErr != nil {
			err = fmt.Errorf("%w (the update ended with an error, %v, and the branch was then found at the tip)", err, moveErr)
		}
		return rec, &RecordError{Record: rec, Err: err}
	}
	return rec, nil
}

// newestVerification returns the newest verification record of the project
// for the tip, which must be a clean pass.
func newestVerification(st *Store, project, tip string) (VerificationRecord, error) {
	all, err := st.Verifications(tip)
	if err != nil {
		return VerificationRecord{}, err
	}
	all = slices.DeleteFunc(all, func(v VerificationRecord) bool { return v.Project != project })
	if len(all) == 0 {
		return VerificationRecord{}, fmt.Errorf("%w: no verification record for tip %s", ErrNotVerified, tip)
	}
	newest := all[len(all)-1]
	if !newest.passing() {
		return VerificationRecord{}, fmt.Errorf("%w: the newest of %d record(s) for tip %s is not a clean pass (passed %v, exit %d, stopped %q, output cut %v)",
			ErrNotVerified, len(all), tip, newest.Passed, newest.Exit, newest.Stopped, newest.OutputCut)
	}
	return newest, nil
}

// rereadTimeout is the time limit of the look at the target after a failed
// update. It is a limit of its own: the call's time may be used up.
const rereadTimeout = 10 * time.Second

// moveTarget sets target from base to tip with one compare-and-swap. The
// transaction is on standard input, NUL separated: update-ref refuses the
// update when the ref is not at base, and it never creates the ref, because
// the old value is not the zero id.
//
// After any failure of the update (a non-zero status, a time-out, a cancelled
// context, any other error) the transaction may already have been committed,
// so the ref is read again, on a context that is not cancelled and with a
// time limit of its own. If the ref is at base, nothing moved and the
// original error is returned. If it is at tip, the branch moved: moveTarget
// returns true and the error that the update ended with, and the caller
// records the move. If it is elsewhere, or gone, the error is [ErrMoved].
// If it cannot be read, the error says that the outcome is unknown.
func (r *runner) moveTarget(ctx context.Context, target, base, tip string) (atTip bool, err error) {
	if err := checkRefName(target, "refs/heads/"); err != nil {
		return false, err
	}
	if !gitenv.IsObjectID(base) || !gitenv.IsObjectID(tip) || len(base) != len(tip) {
		return false, errors.New("intake: the ids to move between are not full object ids")
	}
	var in bytes.Buffer
	in.WriteString("start\x00")
	in.WriteString("update " + target + "\x00" + tip + "\x00" + base + "\x00")
	in.WriteString("prepare\x00commit\x00")
	// The transaction prints "start: ok" and so on; it is read and dropped.
	_, err = r.git(ctx, []string{"update-ref", "--stdin", "-z", "--no-deref", "--end-of-options"}, &in, smallOutput, nil)
	if err == nil {
		return false, nil
	}
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rereadTimeout)
	defer cancel()
	again := &runner{src: r.src, deadline: time.Now().Add(rereadTimeout)}
	id, found, rerr := again.refID(rctx, target)
	switch {
	case rerr != nil:
		return false, fmt.Errorf("%w: moving %s failed (%w) and its state could not be read, so it may have moved: %v",
			ErrOutcomeUnknown, leadtext.Escape(target), err, rerr)
	case !found:
		return false, fmt.Errorf("%w: %s no longer exists", ErrMoved, leadtext.Escape(target))
	case id == tip:
		return true, err
	case id != base:
		return false, fmt.Errorf("%w: %s was at %s and is now at %s; nothing was changed", ErrMoved, leadtext.Escape(target), base, id)
	}
	return false, fmt.Errorf("intake: moving %s failed: %w", leadtext.Escape(target), err)
}

// refID returns the id that the ref with exactly this name points at.
func (r *runner) refID(ctx context.Context, name string) (id string, found bool, err error) {
	out, err := r.git(ctx, []string{
		"for-each-ref", "--format=%(refname) %(objectname)", "--end-of-options", name,
	}, nil, smallOutput, nil)
	if err != nil {
		return "", false, err
	}
	for _, line := range strings.Split(strings.TrimSuffix(string(out), "\n"), "\n") {
		n, got, ok := strings.Cut(line, " ")
		if ok && n == name && gitenv.IsObjectID(got) {
			return got, true, nil
		}
	}
	return "", false, nil
}
