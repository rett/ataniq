//go:build linux && ataniq_ownerchannel

package hostadapter

import (
	"context"
	"errors"
	"time"

	"ataniq/internal/approval/ownerchannel"
	"ataniq/internal/approval/record"
	"ataniq/internal/intake"
	"ataniq/internal/intake/verify"
)

// Suite runs the project's gates on a fresh checkout of a merge, in the
// verification sandbox, and says how the run ended. It is the one thing of
// the host that this package does not build: the sandbox of package verify
// takes a tip under refs/leads/, and the merge lives under
// refs/ataniq/pending/, so the real suite is for a later change. The tests of
// this package use a fake.
type Suite interface {
	Run(ctx context.Context, req SuiteRequest) (SuiteResult, error)
}

// SuiteRequest names what to run.
type SuiteRequest struct {
	Project string
	// Mirror is the bare mirror the merge is in.
	Mirror string
	// Merge is the commit to check out, and Base the target's tip it was
	// built on.
	Merge string
	Base  string
	// Gates are run in order, in the root of the checkout.
	Gates []verify.Gate
}

// SuiteResult is how a run ended, in the fields of [verify.Result] that a
// run decides. Passed is true only for a run that exited 0 by itself, was
// not stopped and whose output was not cut.
type SuiteResult struct {
	Passed bool
	Exit   int
	// Stopped is "", "timeout", "output-limit" or "error", as in
	// [verify.Result].
	Stopped string
	// Output is the whole output, raw text a lead influenced. It is kept
	// only as a digest and a length in the verification record, and as the
	// file the intake's store writes beside it.
	Output    []byte
	OutputCut bool
	Started   time.Time
	Duration  time.Duration
}

// Verify implements [ownerchannel.Verifier]: it runs the suite on the merge
// and keeps the result as a verification record of the intake's store. Every
// run is kept, a failed one too, since a later failed run withdraws an
// earlier pass. A run that is not clean says why in one word of the closed
// list: output-cut for a cut output, stopped for a run that was stopped,
// failed for a gate that did not exit 0, and could-not-run for a suite that
// returned an error or an answer that does not describe the merge.
func (h *Host) Verify(ctx context.Context, in ownerchannel.VerifyInput) (ownerchannel.Verification, error) {
	p, err := h.project(in.Project)
	if err != nil {
		return ownerchannel.Verification{}, err
	}
	if !record.ValidRequestID(in.RequestID) || !record.ValidObjectID(in.Merge) {
		return ownerchannel.Verification{}, errors.New("hostadapter: the verification names no valid request or merge")
	}
	res, err := h.cfg.Suite.Run(ctx, SuiteRequest{
		Project: p.Name, Mirror: p.Mirror, Merge: in.Merge, Base: in.Base, Gates: append([]verify.Gate(nil), p.Gates...),
	})
	if err != nil {
		return ownerchannel.Verification{Detail: ownerchannel.VerifyCouldNotRun}, nil
	}
	// The record's TipRef is a label that names the request, in the lead
	// namespace the intake's store takes; no ref of that name exists and the
	// intake compares none.
	vr := verify.Result{
		Tip: in.Merge, TipRef: "refs/leads/" + p.Role + "/heads/merge/" + in.RequestID,
		PlanDigest: verify.PlanDigest(p.Gates),
		Passed:     res.Passed && res.Exit == 0 && res.Stopped == "" && !res.OutputCut,
		Exit:       res.Exit, Stopped: res.Stopped, Output: res.Output, OutputCut: res.OutputCut,
		Started: res.Started, Duration: res.Duration,
	}
	rec, err := h.store.PutVerification(p.Name, vr, p.Gates)
	if err != nil && !errors.Is(err, intake.ErrNotSynced) {
		return ownerchannel.Verification{Detail: ownerchannel.VerifyCouldNotRun}, nil
	}
	if !vr.Passed {
		return ownerchannel.Verification{Detail: notCleanWord(res)}, nil
	}
	id, sha, err := recordIdentity(rec)
	if err != nil {
		return ownerchannel.Verification{Detail: ownerchannel.VerifyCouldNotRun}, nil
	}
	return ownerchannel.Verification{
		Clean:    true,
		Evidence: record.Evidence{Kind: record.EvidenceSuite, RecordID: id, RecordSHA256: sha},
	}, nil
}

// notCleanWord is the word for a run that was not clean.
func notCleanWord(res SuiteResult) string {
	switch {
	case res.OutputCut || res.Stopped == verify.StoppedOutputLimit:
		return ownerchannel.VerifyOutputCut
	case res.Stopped != "":
		return ownerchannel.VerifyStopped
	}
	return ownerchannel.VerifyFailed
}
