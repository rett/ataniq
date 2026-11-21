//go:build linux && ataniq_ownerchannel

package hostadapter

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ataniq/internal/approval/ownerchannel"
	"ataniq/internal/approval/ownerjournal"
	"ataniq/internal/approval/record"
	"ataniq/internal/intake"
	"ataniq/internal/intake/execconfig"
)

// execFeature adds a file the rules flag (a Makefile is execution config) and
// an ordinary one.
func execFeature(l *lab) {
	l.put("feature.txt", "one\n")
	l.put("Makefile", "test:\n\ttrue\n")
}

// A request file leads to a merge the host built and pinned, a suite run on
// it through the runner, a review of it and a pending record at the door with
// the summary and the digests; and until the owner decides, no ref moved.
func TestARequestFileLeadsToAPinnedMergeAReviewAndAPendingRecord(t *testing.T) {
	r := newRig(t, execFeature)
	before := r.lab.refs()
	p := r.card()

	b := p.Binding
	if b.Merge == nil || b.Summary == nil || len(b.Effects) != 1 {
		t.Fatalf("the binding is incomplete: %+v", b)
	}
	// The merge is the host's: two parents, the target's tip first.
	if b.Merge.Parents[0] != r.scene.target || b.Merge.Parents[1] != r.scene.feature {
		t.Errorf("the merge's parents are %v", b.Merge.Parents)
	}
	if got := r.lab.git("", "rev-list", "--parents", "-n1", b.Merge.Commit); got != b.Merge.Commit+" "+r.scene.target+" "+r.scene.feature {
		t.Errorf("the merge commit has the parents %q", got)
	}
	// Pinned under the pending prefix, at the merge, and by nothing else.
	pin := r.pinOfMerge(b.Merge.Commit)
	if !strings.HasPrefix(pin, intake.PendingPrefix) || strings.TrimPrefix(pin, intake.PendingPrefix) == "" {
		t.Fatalf("the merge is not pinned: refs\n%s", r.lab.refs())
	}
	after := r.lab.refs()
	if want := before + "\n" + pin + " " + b.Merge.Commit; sortedLines(after) != sortedLines(want) {
		t.Errorf("refs after the request:\n%s\nwant\n%s", after, want)
	}
	// The suite ran once, on the merge and the target's tip, with the gates.
	if r.suite.count() != 1 {
		t.Fatalf("the suite ran %d times", r.suite.count())
	}
	call := r.suite.calls[0]
	if call.Merge != b.Merge.Commit || call.Base != r.scene.target || call.Mirror != r.lab.dir || call.Project != "project-a" ||
		len(call.Gates) != 1 || call.Gates[0].Name != "suite" {
		t.Errorf("the suite was asked %+v", call)
	}
	// The summary is the intake's: its digest is the digest of an independent
	// review of the pin, and it names the changed paths and the flag.
	rev := r.reviewOf(pin)
	if b.Summary.ReviewDigest != rev.Digest() || b.Summary.Base != r.scene.target || b.Summary.Tip != b.Merge.Commit {
		t.Errorf("the summary is of %s..%s with digest %s, want digest %s", b.Summary.Base, b.Summary.Tip, b.Summary.ReviewDigest, rev.Digest())
	}
	var paths []string
	for _, pc := range b.Summary.Paths {
		paths = append(paths, string(pc.Path)+":"+pc.Status)
	}
	if strings.Join(paths, " ") != "Makefile:A feature.txt:A" || b.Summary.Files != 2 || b.Summary.Added != 3 || b.Summary.PathsTruncated {
		t.Errorf("summary paths %v files %d added %d", paths, b.Summary.Files, b.Summary.Added)
	}
	if len(b.Summary.Flagged) != 1 || b.Summary.Flagged[0].Kind != record.FlagExecConfig || string(b.Summary.Flagged[0].Path) != "Makefile" {
		t.Errorf("flagged %+v", b.Summary.Flagged)
	}
	set := b.Summary.ExecConfigSet
	if set.Matcher != execconfig.MatcherVersion || set.SHA256 != execconfig.Digest(execconfig.Default()) || set.Additions != 0 {
		t.Errorf("rule set %+v", set)
	}
	// One verification record, a pass for the merge, is the bound evidence,
	// and it is in the intake's store in the state folder.
	if len(b.Evidence) != 1 || b.Evidence[0].Kind != record.EvidenceSuite {
		t.Fatalf("evidence %+v", b.Evidence)
	}
	recs, err := r.o.Host.suiteRecords("project-a", b.Merge.Commit, b.Merge.Parents[0])
	if err != nil || len(recs) != 1 || !recs[0].rec.Passed || recs[0].id != b.Evidence[0].RecordID || recs[0].sha != b.Evidence[0].RecordSHA256 {
		t.Errorf("records %+v, %v", recs, err)
	}
	// Nothing moved, and nothing was replied: the card waits.
	if got := r.lab.ref(targetBranch); got != r.scene.target {
		t.Errorf("the target moved to %s", got)
	}
	if reply := r.replyOf("rq-test-01"); reply != "" {
		t.Errorf("a reply was written for a card that waits: %s", reply)
	}
	// The request file was taken: it is in the collected folder.
	if _, err := os.Stat(filepath.Join(r.drop, collectedDir, "rq-test-01.request")); err != nil {
		t.Errorf("the request was not moved to the collected folder: %v", err)
	}
	if _, err := os.Stat(filepath.Join(r.drop, "rq-test-01.request")); err == nil {
		t.Errorf("the request file is still in the drop folder")
	}
}

func sortedLines(s string) string {
	lines := strings.Split(s, "\n")
	for i := 1; i < len(lines); i++ {
		for j := i; j > 0 && lines[j-1] > lines[j]; j-- {
			lines[j-1], lines[j] = lines[j], lines[j-1]
		}
	}
	return strings.Join(lines, "\n")
}

// After the owner's decision there is one ref transaction with the old value:
// the target moves from its tip to the merge and no other ref changes but the
// pin, which the service takes away when the outcome is final. No hook runs
// and no checkout is made.
func TestAnApprovalMovesTheTargetByOneTransactionWithNoHookAndNoCheckout(t *testing.T) {
	r := newRig(t, execFeature)
	// A hook the host must not run: update-ref would run reference-transaction
	// and a receive would run update.
	marker := filepath.Join(t.TempDir(), "hook-ran")
	hooks := filepath.Join(r.lab.dir, "hooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{"reference-transaction", "update", "post-update", "pre-auto-gc"} {
		script := "#!/bin/sh\necho " + h + " >> " + marker + "\nexit 0\n"
		if err := os.WriteFile(filepath.Join(hooks, h), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	headBefore, _ := os.ReadFile(filepath.Join(r.lab.dir, "HEAD"))
	p := r.card()
	merge, pin := p.Binding.Merge.Commit, r.pinOfMerge(p.Binding.Merge.Commit)
	// The run of the fixtures above is the only thing the hooks may have seen.
	_ = os.Remove(marker)

	res, err := r.decide(p, true)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if res.State != record.StateApplied {
		t.Fatalf("the decision ended %s: %s", res.State, res.Line)
	}
	if got := r.lab.ref(targetBranch); got != merge {
		t.Errorf("the target is at %s, want the merge %s", got, merge)
	}
	// Every other ref is where it was, and the pin is gone.
	for _, line := range strings.Split(r.lab.refs(), "\n") {
		name, id, _ := strings.Cut(line, " ")
		switch {
		case name == targetBranch:
		case name == featureRef && id == r.scene.feature:
		case name == pin:
			t.Errorf("the pin of an applied merge is still there")
		default:
			t.Errorf("unexpected ref %s", line)
		}
	}
	if _, err := os.Stat(marker); err == nil {
		b, _ := os.ReadFile(marker)
		t.Errorf("a hook ran: %s", b)
	}
	if headAfter, _ := os.ReadFile(filepath.Join(r.lab.dir, "HEAD")); string(headAfter) != string(headBefore) {
		t.Errorf("HEAD changed")
	}
	// No checkout: the mirror holds no work tree, no index, no packed state
	// beyond git's own folders.
	entries, _ := os.ReadDir(r.lab.dir)
	for _, e := range entries {
		switch e.Name() {
		case "HEAD", "config", "objects", "refs", "hooks", "packed-refs":
		default:
			t.Errorf("the mirror holds %s", e.Name())
		}
	}
	// The ref update is the intake's: its record names the move.
	st, err := intake.OpenStore(filepath.Join(r.state, OwnerDirName, RecordsName))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	moves, err := st.Intakes()
	if err != nil || len(moves) != 1 || moves[0].Base != r.scene.target || moves[0].Tip != merge || moves[0].TargetRef != targetBranch {
		t.Errorf("intake records %+v, %v", moves, err)
	}
	if moves[0].Verification.Tip != merge || !moves[0].Verification.Passed {
		t.Errorf("the record's verification %+v", moves[0].Verification)
	}
	// The lead is told, in the one reply this build writes, which is a file
	// beside the request.
	reply := r.replyOf("rq-test-01")
	if !strings.Contains(reply, `"state":"applied"`) || !strings.Contains(reply, `"request_id":"rq-test-01"`) || !strings.Contains(reply, `"pending_id":"`+p.ID+`"`) {
		t.Errorf("reply: %s", reply)
	}
}

// A target that moved after the card was made gives nothing to apply. When it
// moves before the use is read again, the door refuses the mismatch; when it
// moves after the fresh read and before the update, the intake finds the base
// changed and the outcome is not-applied. Either way no ref is changed by the
// adapter and the owner's approval is spent.
func TestAMovedTargetAfterTheDecisionGivesNotApplied(t *testing.T) {
	r := newRig(t, execFeature)
	p := r.card()
	// A new commit on top of the target, as another writer's push.
	r.lab.load(r.scene.target)
	r.lab.put("other.txt", "x\n")
	moved := r.lab.commit("another writer\n", r.scene.target)
	r.fault = func(point string) error {
		if point == "fresh-read" {
			r.lab.setRef(targetBranch, moved)
		}
		return nil
	}
	pin := r.pinOfMerge(p.Binding.Merge.Commit)

	res, err := r.decide(p, true)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if res.State != record.StateNotApplied {
		t.Fatalf("the decision ended %s: %s", res.State, res.Line)
	}
	if got := r.lab.ref(targetBranch); got != moved {
		t.Errorf("the target is at %s, want the other writer's %s", got, moved)
	}
	// The merge was not written to the target, no intake record was made and
	// the lead's branch is as it was.
	st, err := intake.OpenStore(filepath.Join(r.state, OwnerDirName, RecordsName))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if moves, err := st.Intakes(); err != nil || len(moves) != 0 {
		t.Errorf("intake records %+v, %v", moves, err)
	}
	if got := r.lab.ref(featureRef); got != r.scene.feature {
		t.Errorf("the feature moved to %s", got)
	}
	if got := r.state0(p.ID); got != record.StateNotApplied {
		t.Errorf("the door holds %s", got)
	}
	if got := r.lab.ref(pin); got != "" {
		t.Errorf("the pin of a final request is still there at %s", got)
	}
	if reply := r.replyOf("rq-test-01"); !strings.Contains(reply, `"state":"not-applied"`) {
		t.Errorf("reply: %s", reply)
	}
}

// A target that moved before the use is read again is a mismatch at the door:
// the use is refused and the record voided. The target stays where the other
// writer put it.
func TestATargetThatMovedBeforeTheUseIsRefusedAtTheDoor(t *testing.T) {
	r := newRig(t, execFeature)
	p := r.card()
	r.lab.load(r.scene.target)
	r.lab.put("other.txt", "x\n")
	moved := r.lab.commit("another writer\n", r.scene.target)
	r.lab.setRef(targetBranch, moved)

	res, err := r.decide(p, true)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if res.State != record.StateVoid {
		t.Fatalf("the decision ended %s: %s", res.State, res.Line)
	}
	if got := r.lab.ref(targetBranch); got != moved {
		t.Errorf("the target is at %s", got)
	}
	if got := r.state0(p.ID); got != record.StateVoid {
		t.Errorf("the door holds %s", got)
	}
}

// A verification run after the card was made, and failed, withdraws the pass
// that was bound: the use sees evidence that is not the newest.
func TestALaterFailedRunWithdrawsTheBoundPass(t *testing.T) {
	r := newRig(t, execFeature)
	p := r.card()
	merge := p.Binding.Merge.Commit
	r.suite.set(SuiteResult{Passed: false, Exit: 1, Output: []byte("FAIL\n"), Started: start, Duration: time.Second}, nil)
	v, err := r.o.Host.Verify(context.Background(), ownerchannel.VerifyInput{Project: "project-a", RequestID: "rq-later-01", Merge: merge, Base: r.scene.target})
	if err != nil || v.Clean || v.Detail != ownerchannel.VerifyFailed {
		t.Fatalf("Verify: %+v, %v", v, err)
	}
	res, err := r.decide(p, true)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if res.State != record.StateVoid {
		t.Fatalf("the decision ended %s: %s", res.State, res.Line)
	}
	if got := r.lab.ref(targetBranch); got != r.scene.target {
		t.Errorf("the target moved to %s", got)
	}
}

// The journal is the real store, in the state folder of the daemon: a restart
// finds the card and what was journaled.
func TestTheJournalIsTheRealStoreInTheStateFolderAndSurvivesARestart(t *testing.T) {
	r := newRig(t, execFeature)
	p := r.card()
	jpath := filepath.Join(r.state, OwnerDirName, JournalName)
	st, err := os.Stat(jpath)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("the journal file: %v %v", st, err)
	}
	if st, err := os.Stat(filepath.Dir(jpath)); err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("the owner folder: %v %v", st, err)
	}
	// A second open of the same state folder is refused while this one holds
	// the journal.
	if o2, err := Open(context.Background(), r.config()); err == nil {
		_ = o2.Close()
		t.Fatalf("a second owner channel opened the same journal")
	}
	r.restart()
	l, err := r.o.Service.List(context.Background())
	if err != nil || len(l.Pending) != 1 || l.Pending[0].ID != p.ID {
		t.Fatalf("after a restart the list is %+v, %v", l, err)
	}
	res, err := r.decide(r.pending(p.ID), true)
	if err != nil || res.State != record.StateApplied {
		t.Fatalf("Decide after a restart: %+v, %v", res, err)
	}
	// The decision and its outcome are in the journal file, which the test
	// opens after the owner channel is closed.
	if err := r.o.Close(); err != nil {
		t.Fatal(err)
	}
	j, err := ownerjournal.Open(jpath)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := j.Entries(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	kinds := map[ownerjournal.Kind]int{}
	for _, e := range entries {
		kinds[e.Kind]++
	}
	if kinds[ownerjournal.KindCount] != 1 || kinds[ownerjournal.KindDecision] != 1 || kinds[ownerjournal.KindOutcome] != 1 {
		t.Errorf("journal entries by kind: %v", kinds)
	}
	r.open() // the cleanup closes it
}
