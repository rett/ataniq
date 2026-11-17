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
	// has been moved by the integration.
	ErrMoved = errors.New("intake: a branch moved after the sign-off")
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
//     must be a clean pass, else [ErrNotVerified].
//  5. The target is moved with one compare-and-swap, git update-ref with the
//     old value being the base the review saw, in one transaction on standard
//     input. No reflog message is written: the intake record is the record.
//     It fails when the target is no longer at the base, and then the error
//     is [ErrMoved] and nothing has changed.
//  6. The intake record is written. If that fails, the error is a
//     *[RecordError] that says the branch moved and the record is missing,
//     and carries the record.
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
		return IntakeRecord{}, errors.New("intake: the lead branch is at the target: nothing to integrate")
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
	if err := newRunner(src, lim).moveTarget(ctx, rev.BaseRef, rev.Base, rev.Tip); err != nil {
		return IntakeRecord{}, err
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

// moveTarget sets target from base to tip with one compare-and-swap. The
// transaction is on standard input, NUL separated: update-ref refuses the
// update when the ref is not at base, and it never creates the ref, because
// the old value is not the zero id. A failure is checked once more against
// the ref: if the ref is somewhere else, the error is [ErrMoved].
func (r *runner) moveTarget(ctx context.Context, target, base, tip string) error {
	if err := checkRefName(target, "refs/heads/"); err != nil {
		return err
	}
	if !gitenv.IsObjectID(base) || !gitenv.IsObjectID(tip) || len(base) != len(tip) {
		return errors.New("intake: the ids to move between are not full object ids")
	}
	var in bytes.Buffer
	in.WriteString("start\x00")
	in.WriteString("update " + target + "\x00" + tip + "\x00" + base + "\x00")
	in.WriteString("prepare\x00commit\x00")
	// The transaction prints "start: ok" and so on; it is read and dropped.
	_, err := r.git(ctx, []string{"update-ref", "--stdin", "-z", "--no-deref", "--end-of-options"}, &in, smallOutput, nil)
	if err == nil {
		return nil
	}
	var ge *gitExit
	if !errors.As(err, &ge) {
		return err
	}
	id, found, rerr := r.refID(ctx, target)
	switch {
	case rerr != nil:
		return fmt.Errorf("intake: moving %s failed (%w) and its state could not be read: %v", leadtext.Escape(target), err, rerr)
	case !found:
		return fmt.Errorf("%w: %s no longer exists", ErrMoved, leadtext.Escape(target))
	case id != base:
		return fmt.Errorf("%w: %s was at %s and is now at %s; nothing was changed", ErrMoved, leadtext.Escape(target), base, id)
	}
	return fmt.Errorf("intake: moving %s failed: %w", leadtext.Escape(target), err)
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
