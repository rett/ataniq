//go:build linux && ataniq_ownerchannel

package hostadapter

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"ataniq/internal/approval/door"
	"ataniq/internal/approval/ownerchannel"
	"ataniq/internal/approval/record"
	"ataniq/internal/exchange/gitenv"
)

// dropAgain puts a copy of a request file, and its note, in the drop folder.
func (r *rig) dropAgain(id string, b []byte) { r.dropFile(id, b) }

// A second file for an id that was taken is never answered: the reply of the
// pending request is written once, and the outcome of the first request must
// still reach the lead after the owner approves.
func TestASecondDropOfATakenIdKeepsTheFirstRequestsReply(t *testing.T) {
	r := newRig(t, execFeature)
	id, b := r.request("x", nil)
	r.dropFile(id, b)
	if n, err := r.o.Collector.Scan(context.Background()); err != nil || n != 1 {
		t.Fatalf("Scan took %d: %v", n, err)
	}
	st, ok := r.o.Service.Status(id)
	if !ok || st.State != ownerchannel.RequestPending {
		t.Fatalf("the request is %+v", st)
	}
	p := r.pending(st.PendingID)

	// The lead sees no reply and drops the file again.
	r.dropAgain(id, b)
	if n, err := r.o.Collector.Scan(context.Background()); err != nil || n != 1 {
		t.Fatalf("the second Scan took %d: %v", n, err)
	}
	if got := r.replyOf(id); got != "" {
		t.Fatalf("the second file was answered: %s", got)
	}
	for _, name := range []string{id + requestSuffix, id + noteSuffix} {
		if _, err := os.Lstat(filepath.Join(r.drop, name)); err == nil {
			t.Errorf("%s is still in the drop folder", name)
		}
	}
	aside, _ := filepath.Glob(filepath.Join(r.drop, collectedDir, id+".*.dup-*"))
	if len(aside) != 2 {
		t.Errorf("set aside: %v, want the request and the note", aside)
	}
	var logged int
	for _, l := range r.noticed() {
		if strings.Contains(l, "set aside") {
			logged++
		}
	}
	if logged != 1 {
		t.Errorf("%d lines logged for the dropped file: %v", logged, r.noticed())
	}
	// A third drop is set aside under a name of its own, not over the second.
	r.dropAgain(id, b)
	if _, err := r.o.Collector.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if again, _ := filepath.Glob(filepath.Join(r.drop, collectedDir, id+requestSuffix+".dup-*")); len(again) != 2 {
		t.Errorf("after a third drop: %v", again)
	}

	// The owner approves: the first request's outcome is the lead's reply.
	res, err := r.decide(p, true)
	if err != nil || res.State != record.StateApplied {
		t.Fatalf("Decide: %+v, %v", res, err)
	}
	if rp := r.readReply(id); rp.State != string(record.StateApplied) || rp.Code != "" || rp.PendingID != p.ID {
		t.Errorf("the reply after the approval: %+v", rp)
	}
	if got := r.lab.ref(targetBranch); got != p.Binding.Merge.Commit {
		t.Errorf("the target is at %s", got)
	}
}

// A crash between the link and the remove in move leaves the request file in
// both folders. The next scan finds the id taken and sets the drop folder's
// copy aside without a reply, so the reply of the request that went on is the
// only one, in both orders: the request still pending, and the reply already
// written.
func TestACrashBetweenTheLinkAndTheRemoveDoesNotTakeTheReply(t *testing.T) {
	r := newRig(t, execFeature)
	id, b := r.request("x", nil)
	r.dropFile(id, b)
	if _, err := r.o.Collector.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	st, _ := r.o.Service.Status(id)
	p := r.pending(st.PendingID)
	// The crash: the drop folder still holds a link to the collected file.
	crash := func() {
		t.Helper()
		if err := os.Link(filepath.Join(r.drop, collectedDir, id+requestSuffix), filepath.Join(r.drop, id+requestSuffix)); err != nil {
			t.Fatal(err)
		}
	}
	crash()
	if _, err := r.o.Collector.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := r.replyOf(id); got != "" {
		t.Fatalf("the left-over file was answered: %s", got)
	}
	if _, err := os.Lstat(filepath.Join(r.drop, id+requestSuffix)); err == nil {
		t.Errorf("the left-over file is still in the drop folder")
	}
	res, err := r.decide(p, true)
	if err != nil || res.State != record.StateApplied {
		t.Fatalf("Decide: %+v, %v", res, err)
	}
	if rp := r.readReply(id); rp.State != string(record.StateApplied) {
		t.Fatalf("the reply: %+v", rp)
	}
	// A left-over file after the reply is left alone, as any answered id.
	crash()
	before := r.replyOf(id)
	if _, err := r.o.Collector.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r.replyOf(id) != before {
		t.Errorf("the reply changed")
	}
}

// A request file that is a symbolic link is refused by the open itself, which
// is what holds when the file is swapped for a link after the folder was read.
func TestAnOpenOfASymbolicLinkIsRefusedByTheOpen(t *testing.T) {
	d := privateDir(t)
	target := filepath.Join(d, "target")
	if err := os.WriteFile(target, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(d, "rq-link-bb.request")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readSmall(link, 1024); !errors.Is(err, syscall.ELOOP) {
		t.Fatalf("readSmall of a symbolic link: %v, want ELOOP", err)
	}
	if b, err := readSmall(target, 1024); err != nil || string(b) != "{}" {
		t.Errorf("readSmall of the file: %q, %v", b, err)
	}
	// Through the collector: a link that appears after the directory was read
	// is a request that cannot be read, answered with a reply file.
	r := newRig(t, execFeature)
	id, _ := r.request("x", nil)
	if err := os.Symlink(target, filepath.Join(r.drop, id+requestSuffix)); err != nil {
		t.Fatal(err)
	}
	r.o.Collector.take(context.Background(), id)
	if rp := r.readReply(id); rp.Code != string(record.CodeRequestInvalid) {
		t.Errorf("reply %+v", rp)
	}
	if _, ok := r.o.Service.Status(id); ok {
		t.Errorf("the service saw a request read through a link")
	}
}

// movingGit makes a git for the host that, the first time it is asked to run
// update-ref after the flag file exists, first moves the target to moved with
// the real git, and then runs the asked command: another writer's push between
// the intake's inspection and its transaction.
func movingGit(t *testing.T, l *lab, flag, moved string) gitenv.Git {
	t.Helper()
	real := needGit(t).Path
	script := fmt.Sprintf(`#!/bin/sh
if [ -e '%[1]s' ]; then
  for a in "$@"; do
    if [ "$a" = update-ref ]; then
      rm -f '%[1]s'
      GIT_DIR='%[2]s' '%[3]s' update-ref '%[4]s' '%[5]s' || exit 99
      break
    fi
  done
fi
exec '%[3]s' "$@"
`, flag, l.dir, real, targetBranch, moved)
	path := filepath.Join(t.TempDir(), "git")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	g, err := gitenv.New(path)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// The ref transaction carries the old value. The target moves after the
// adapter's own inspection and after the intake's, between the review and the
// update itself: only the compare-and-swap of the transaction can catch that,
// so an Apply whose transaction lacks the old value moves the target over the
// other writer's commit and answers applied. With the pin intact, the same
// Apply is applied (the control).
func TestApplyMovesTheTargetOnlyFromTheOldValueInTheTransaction(t *testing.T) {
	r := newRig(t, execFeature)
	flag := filepath.Join(t.TempDir(), "move-now")
	r.lab.load(r.scene.target)
	r.lab.put("other.txt", "x\n")
	moved := r.lab.commit("another writer\n", r.scene.target)
	g := movingGit(t, r.lab, flag, moved)
	r.git = &g
	r.restart()
	p := r.card()
	merge := p.Binding.Merge.Commit
	in := ownerchannel.ApplyInput{Project: "project-a", Effects: []record.Effect{{Kind: record.EffectRef, Ref: targetBranch, Old: r.scene.target, New: merge}}}
	pin := r.pinOfMerge(merge)

	if err := os.WriteFile(flag, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := r.o.Host.Apply(context.Background(), in)
	if _, serr := os.Stat(flag); serr == nil {
		t.Fatalf("the other writer never moved the target: the test did not run its case")
	}
	if err != nil || got != door.OutcomeNotApplied {
		t.Fatalf("Apply with the target moved under the transaction = %q, %v, want not-applied", got, err)
	}
	if now := r.lab.ref(targetBranch); now != moved {
		t.Errorf("the target is at %s, want the other writer's %s", now, moved)
	}
	if r.lab.ref(pin) != merge {
		t.Errorf("the pin changed")
	}
	if st, err := r.o.Host.Mirror(context.Background(), in); err != nil || st != door.OutcomeUnknown {
		t.Errorf("Mirror after the move: %q, %v, want unknown (the target is at a third value)", st, err)
	}

	// The control: nothing moves, the same Apply is applied.
	r2 := newRig(t, execFeature)
	g2 := movingGit(t, r2.lab, filepath.Join(t.TempDir(), "never"), moved)
	r2.git = &g2
	r2.restart()
	p2 := r2.card()
	in2 := ownerchannel.ApplyInput{Project: "project-a", Effects: []record.Effect{{Kind: record.EffectRef, Ref: targetBranch, Old: r2.scene.target, New: p2.Binding.Merge.Commit}}}
	if got, err := r2.o.Host.Apply(context.Background(), in2); err != nil || got != door.OutcomeApplied {
		t.Fatalf("the control: Apply = %q, %v, want applied", got, err)
	}
	if now := r2.lab.ref(targetBranch); now != p2.Binding.Merge.Commit {
		t.Errorf("the control's target is at %s", now)
	}
}

// Where nothing was written, Apply says not-applied, not unknown: the review
// the intake will not make (the repository's own attributes) before any move,
// and a transaction that git refused before it was prepared (a ref lock held
// by another writer) with the target still at the old value.
func TestApplyReportsNotAppliedWhereNothingWasWritten(t *testing.T) {
	ctx := context.Background()
	t.Run("the repository's attributes appeared after the card", func(t *testing.T) {
		r := newRig(t, execFeature)
		p := r.card()
		if err := os.MkdirAll(filepath.Join(r.lab.dir, "info"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(r.lab.dir, "info", "attributes"), []byte("*.txt -diff\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		before := r.lab.refs()
		in := ownerchannel.ApplyInput{Project: "project-a", Effects: []record.Effect{{Kind: record.EffectRef, Ref: targetBranch, Old: r.scene.target, New: p.Binding.Merge.Commit}}}
		got, err := r.o.Host.Apply(ctx, in)
		if err != nil || got != door.OutcomeNotApplied {
			t.Fatalf("Apply = %q, %v, want not-applied", got, err)
		}
		if r.lab.refs() != before {
			t.Errorf("a ref changed")
		}
	})
	t.Run("a ref lock is held by another writer", func(t *testing.T) {
		r := newRig(t, execFeature)
		p := r.card()
		lock := filepath.Join(r.lab.dir, filepath.FromSlash(targetBranch)+".lock")
		if err := os.WriteFile(lock, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		before := r.lab.refs()
		in := ownerchannel.ApplyInput{Project: "project-a", Effects: []record.Effect{{Kind: record.EffectRef, Ref: targetBranch, Old: r.scene.target, New: p.Binding.Merge.Commit}}}
		got, err := r.o.Host.Apply(ctx, in)
		if err != nil || got != door.OutcomeNotApplied {
			t.Fatalf("Apply = %q, %v, want not-applied", got, err)
		}
		if r.lab.refs() != before {
			t.Errorf("a ref changed")
		}
		// With the lock gone the same use applies: the refusal was the lock.
		if err := os.Remove(lock); err != nil {
			t.Fatal(err)
		}
		if got, err := r.o.Host.Apply(ctx, in); err != nil || got != door.OutcomeApplied {
			t.Errorf("after the lock: Apply = %q, %v, want applied", got, err)
		}
	})
}

// takenLost plants a request as a stop between the move and the service leaves
// it: in the collected folder only, with no reply and no status.
func (r *rig) takenLost(id string, b []byte) {
	r.t.Helper()
	if err := os.WriteFile(filepath.Join(r.drop, collectedDir, id+requestSuffix), b, 0o600); err != nil {
		r.t.Fatal(err)
	}
}

// A request taken into the collected folder that the service never saw, with
// no reply, is answered request-invalid once by the first scan of the next
// collector, with a line that asks for a new id; the second scan, and a reply
// that exists, change nothing, and the suite never runs for it.
func TestARequestLostBetweenTheMoveAndTheServiceIsAnsweredOnce(t *testing.T) {
	r := newRig(t, execFeature)
	lost, b := r.request("x", nil)
	r.takenLost(lost, b)
	// A taken request that has a reply (a refusal of a stop before this one).
	answered, bAnswered := r.request("x", nil)
	r.takenLost(answered, bAnswered)
	if err := r.o.Host.mailbox.Reply(context.Background(), ownerchannel.Reply{
		Project: "project-a", RequestID: answered, Code: record.CodeFeatureMoved, Line: "an earlier reply",
	}); err != nil {
		t.Fatal(err)
	}
	// The same request with a copy left in the drop folder (a stop between
	// the link and the remove).
	both, bBoth := r.request("x", nil)
	r.takenLost(both, bBoth)
	r.dropFile(both, bBoth)

	r.restart()
	before := r.replyOf(answered)
	if before == "" {
		t.Fatal("the earlier reply is missing")
	}
	if _, err := r.o.Collector.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{lost, both} {
		rp := r.readReply(id)
		if rp.Code != string(record.CodeRequestInvalid) || rp.State != "" || rp.PendingID != "" || !strings.Contains(rp.Line, "new request id") {
			t.Errorf("the reply for %s: %+v", id, rp)
		}
	}
	if r.replyOf(answered) != before {
		t.Errorf("a reply that existed was written over: %s", r.replyOf(answered))
	}
	if _, err := os.Lstat(filepath.Join(r.drop, both+requestSuffix)); err == nil {
		t.Errorf("the left-over copy is still in the drop folder")
	}
	if r.suite.count() != 0 {
		t.Errorf("the suite ran for a lost request")
	}
	if _, ok := r.o.Service.Status(lost); ok {
		t.Errorf("the service saw the lost request")
	}
	// Once: the next Scan writes nothing, and a request taken after the
	// first Scan and left unanswered is not swept (nothing of this process
	// is lost that way).
	first := r.replyOf(lost)
	late, bLate := r.request("x", nil)
	r.takenLost(late, bLate)
	if _, err := r.o.Collector.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r.replyOf(lost) != first || r.replyOf(late) != "" {
		t.Errorf("a later Scan answered: lost %q late %q", r.replyOf(lost), r.replyOf(late))
	}
}

// A taken request whose card the door holds is no lost request: after a
// restart, which forgets the service's statuses, the first scan leaves it to
// its own outcome, and the owner's decision is still its reply.
func TestARequestTheDoorHoldsIsNotAnsweredAsLostAfterARestart(t *testing.T) {
	r := newRig(t, execFeature)
	p := r.card()
	var id string
	for _, e := range func() []string {
		m, _ := filepath.Glob(filepath.Join(r.drop, collectedDir, "*"+requestSuffix))
		return m
	}() {
		id = strings.TrimSuffix(filepath.Base(e), requestSuffix)
	}
	if id == "" {
		t.Fatal("no taken request")
	}
	r.restart()
	if _, ok := r.o.Service.Status(id); ok {
		t.Fatal("the restart kept the status")
	}
	if _, err := r.o.Collector.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := r.replyOf(id); got != "" {
		t.Fatalf("a request the door holds was answered: %s", got)
	}
	res, err := r.decide(p, true)
	if err != nil || res.State != record.StateApplied {
		t.Fatalf("Decide: %+v, %v", res, err)
	}
	if rp := r.readReply(id); rp.State != string(record.StateApplied) {
		t.Errorf("the reply: %+v", rp)
	}
}

// A collector that was not given the service's knowledge, and one whose door
// cannot be listed, answer nothing: they cannot tell a lost request from one
// in flight.
func TestTheSweepAnswersNothingWhenItCannotTellWhatTheServiceKnows(t *testing.T) {
	r := newRig(t, execFeature)
	id, b := r.request("x", nil)
	r.takenLost(id, b)
	r.o.Collector.known = nil
	if _, err := r.o.Collector.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := r.replyOf(id); got != "" {
		t.Errorf("answered without the service's knowledge: %s", got)
	}
	r.o.Collector.known = func(context.Context, string) (bool, error) { return false, errors.New("the door is down") }
	if _, err := r.o.Collector.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := r.replyOf(id); got != "" {
		t.Errorf("answered when the door could not be listed: %s", got)
	}
	// The sweep is not spent: the next Scan, with the door back, answers.
	r.o.Collector.known = func(context.Context, string) (bool, error) { return false, nil }
	if _, err := r.o.Collector.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if rp := r.readReply(id); rp.Code != string(record.CodeRequestInvalid) {
		t.Errorf("the reply: %+v", rp)
	}
}
