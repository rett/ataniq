//go:build linux && ataniq_ownerchannel

package hostadapter

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ataniq/internal/approval/card"
	"ataniq/internal/approval/door"
	"ataniq/internal/approval/limits"
	"ataniq/internal/approval/ownerchannel"
	"ataniq/internal/approval/ownerjournal"
	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/approval/record"
	"ataniq/internal/intake"
)

// errStop is the stop a test injects between two steps of the service, as a
// crash would be.
var errStop = errors.New("e2e: the host stopped here")

// stopAt makes the service stop the first time it reaches the point.
func (j *joined) stopAt(point string) {
	var fired atomic.Bool
	j.setFault(func(p string) error {
		if p == point && fired.CompareAndSwap(false, true) {
			return errStop
		}
		return nil
	})
}

// clearStop lets the service run through every point again.
func (j *joined) clearStop() { j.setFault(nil) }

// target is the target branch's tip now.
func (j *joined) target() string { return j.lab.ref(targetBranch) }

// replyState returns the lead's reply for the request as it was written.
func (j *joined) reply(id string) string { return j.replyOf(id) }

// kinds counts the entries of the journal by kind. It stops the daemon, which
// holds the journal, so a test calls it last.
func kinds(es []ownerjournal.Entry) map[ownerjournal.Kind]int {
	out := map[ownerjournal.Kind]int{}
	for _, e := range es {
		out[e.Kind]++
	}
	return out
}

// The full cycle, from the owner's keys to the mirror: the console's model
// reads a card through the console's client and the daemon, the owner signs
// off the one flagged path and types the code, and the ref moves. The code
// and the plan token are then looked for in every place of the host that the
// test can read (OC23, the host's part).
//
// The plan token is not absent everywhere: it is on the card the owner reads
// and in the decision record, which repeats it so that a refusal can name the
// field that differs (the record's own comment says so), and so in the entry
// of the journal that holds that record. The journal is the host's file, in a
// folder no sandbox reaches (OC1, which only a seated lead can show). The
// places a lead or its sandbox could read are the reply, the request and the
// note in the drop folder, the intake's records and the mirror, and none of
// them holds it. The confirm code is in no place at all, on the wire or off
// it: the console draws it and keeps it in its model.
func TestOwnerChannelOC23FullCycleAndWhereTheCodeAndTheTokenAre(t *testing.T) {
	j := newJoined(t, execFeature)
	reqID, cid := j.card2()
	p := j.pendingOf(cid)
	tp := j.startTap()
	console := j.tapped(tp)
	md := newModel(t, console, j.clock, "7Q4K")
	md.readAndSign()
	md.key("a")
	if got := md.code(); got != "7Q4K" {
		t.Fatalf("the model drew the code %q, want the one its reader made:\n%s", got, md.screen())
	}
	md.typ("7Q4K")
	md.key("enter")
	if s := md.screen(); !strings.Contains(s, "applied: the ref was moved") {
		t.Fatalf("the model does not show the outcome:\n%s", s)
	}
	if md.code() != "" {
		t.Errorf("the code is still drawn after the decision")
	}

	// The ref moved, once, to the merge the card showed, and the pin is gone.
	merge := mergeOf(p)
	if got := j.target(); got != merge {
		t.Fatalf("the target is at %s, want the merge %s", got, merge)
	}
	if got := j.lab.git("", "rev-list", "--parents", "-n1", merge); got != merge+" "+j.scene.target+" "+j.scene.feature {
		t.Errorf("the merge has the parents %q", got)
	}
	if pins := j.pins(); len(pins) != 0 {
		t.Errorf("pins left: %v", pins)
	}
	if n := j.intakeMoves(); n != 1 {
		t.Errorf("%d ref updates were recorded", n)
	}
	if got := j.state0(cid); got != record.StateApplied {
		t.Errorf("the door holds %s", got)
	}
	reply := j.reply(reqID)
	if !strings.Contains(reply, `"state":"applied"`) {
		t.Errorf("the lead's reply: %s", reply)
	}

	// Look for the code and the token.
	const code = "7Q4K"
	up, down := tp.seen()
	if len(up) == 0 || len(down) == 0 {
		t.Fatalf("the tap saw nothing: %d, %d", len(up), len(down))
	}
	upText, downText := unwrapped(up), unwrapped(down)
	if strings.Contains(upText, code) || strings.Contains(downText, code) {
		t.Errorf("the confirm code crossed the wire")
	}
	if strings.Contains(upText, e2eToken) {
		t.Errorf("the console sent the plan token to the daemon")
	}
	if !strings.Contains(downText, e2eToken) {
		t.Errorf("the plan token was not on the card the daemon sent: the search proves nothing")
	}
	// The one decision frame carries what the owner did and nothing else.
	frames := decideParams(t, up)
	if len(frames) != 1 {
		t.Fatalf("the console sent %d decision frames, want 1", len(frames))
	}
	for k := range frames[0] {
		switch k {
		case "id", "binding_sha256", "decision", "signed_off", "reason":
		default:
			t.Errorf("the decision frame has the field %q", k)
		}
	}
	shownBy := j.stand
	shown, err := shownBy.Show(context.Background(), cid)
	if err != nil {
		t.Fatal(err)
	}
	dec, err := record.DecodeDecision(shown.Decision.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if dec.Confirmed != record.ConfirmedCode {
		t.Errorf("the decision stores confirmed %q, want %q", dec.Confirmed, record.ConfirmedCode)
	}
	if dec.PlanToken != e2eToken {
		t.Errorf("the decision's own copy of the token is %q", dec.PlanToken)
	}
	for _, b := range [][]byte{shown.Decision.Bytes, shown.Pending.Bytes} {
		if strings.Contains(string(b), code) {
			t.Errorf("the code is in a record of the door")
		}
	}

	entries := j.journalEntries() // stops the daemon: the journal is read after it
	files := readableFiles(t, j.state, j.drop, j.lab.dir)
	if h := holders(files, code); len(h) != 0 {
		t.Errorf("the confirm code is in %v", h)
	}
	for _, e := range entries {
		if strings.Contains(string(e.Body), code) || strings.Contains(string(e.Record), code) {
			t.Errorf("the confirm code is in the journal entry %d (%s)", e.Seq, e.Kind)
		}
	}
	if strings.Contains(j.logs.String(), code) || strings.Contains(strings.Join(j.noticed(), "\n"), code) {
		t.Errorf("the confirm code is in a log line or a notice")
	}
	// The token: only the journal's files may hold it, and in the journal
	// only the entries that hold a decision record.
	for name := range files {
		if strings.Contains(name, "journal.db") {
			continue
		}
		if bytesHold(files[name], e2eToken) {
			t.Errorf("the plan token is in %s", name)
		}
	}
	withToken := 0
	for _, e := range entries {
		if strings.Contains(string(e.Body), e2eToken) {
			t.Errorf("the plan token is in the body of the journal entry %d (%s)", e.Seq, e.Kind)
		}
		if strings.Contains(string(e.Record), e2eToken) {
			withToken++
			if e.Kind != ownerjournal.KindDecision {
				t.Errorf("the plan token is in a journal entry of kind %s", e.Kind)
			}
		}
	}
	if withToken != 1 {
		t.Errorf("%d journal entries hold the decision record's copy of the token, want 1", withToken)
	}
	// What the decision entry holds is the decision of the door, byte for
	// byte, with no code in it.
	for _, e := range entries {
		if e.Kind == ownerjournal.KindDecision && string(e.Record) != string(shown.Decision.Bytes) {
			t.Errorf("the journal's decision is not the one the door holds")
		}
	}
	if k := kinds(entries); k[ownerjournal.KindDecision] != 1 || k[ownerjournal.KindDelivered] != 1 || k[ownerjournal.KindOutcome] != 1 {
		t.Errorf("journal kinds %v", k)
	}
}

func bytesHold(b []byte, s string) bool { return strings.Contains(string(b), s) }

// feature returns the lead's ref of a feature.
func featureRefOf(name string) string { return "refs/leads/role-a/heads/feature/" + name }

// addFeature makes a second feature branch from the scene's root that adds one
// file, and returns its tip.
func (j *joined) addFeature(name, file, content string) string {
	j.t.Helper()
	j.lab.load(j.scene.root)
	j.lab.put(file, content)
	tip := j.lab.commit("feature "+name+"\n", j.scene.root)
	j.lab.setRef(featureRefOf(name), tip)
	return tip
}

// pushOn adds a commit to the lead's feature x, as the lead pushing more work
// would, and returns the new tip.
func (j *joined) pushOn(file string) string {
	j.t.Helper()
	j.lab.load(j.lab.ref(featureRef))
	j.lab.put(file, "later\n")
	tip := j.lab.commit("later work\n", j.lab.ref(featureRef))
	j.lab.setRef(featureRef, tip)
	return tip
}

// isAncestor reports whether a is in the history of b.
func (j *joined) isAncestor(a, b string) bool {
	_, err := j.lab.try("", "merge-base", "--is-ancestor", a, b)
	return err == nil
}

// moveTarget adds a commit by another writer on top of the target's tip and
// moves the target there.
func (j *joined) moveTarget() string {
	j.t.Helper()
	j.lab.load(j.target())
	j.lab.put("other-writer.txt", "x\n")
	moved := j.lab.commit("another writer\n", j.target())
	j.lab.setRef(targetBranch, moved)
	return moved
}

// treeHas reports whether the commit's tree holds the path.
func (j *joined) treeHas(commit, path string) bool {
	out, err := j.lab.try("", "ls-tree", "-r", "--name-only", commit)
	if err != nil {
		j.t.Fatal(err)
	}
	for _, l := range strings.Split(out, "\n") {
		if l == path {
			return true
		}
	}
	return false
}

// OC4: the lead moves its feature branch, and separately withdraws the
// request, after the card is open and before the owner confirms. A moved
// branch changes nothing on the card, and what is applied is the bound tip of
// the feature; a withdrawal closes the card and writes no decision. (The part
// of OC4 that is the model's, the typed text dropped when the card ends under
// the owner's hands, is the console's own test; the host's part is that the
// card and the record do not change and that no decision is journaled.)
func TestOwnerChannelOC4TheFeatureMovesOrTheRequestIsWithdrawnWithTheCardOpen(t *testing.T) {
	t.Run("the lead moves its feature branch", func(t *testing.T) {
		j := newJoined(t, execFeature)
		_, cid := j.card2()
		o := j.decider()
		before, err := o.Open(cid)
		if err != nil {
			t.Fatal(err)
		}
		later := j.pushOn("later.txt")
		after, err := o.Open(cid)
		if err != nil {
			t.Fatal(err)
		}
		if after.BindingSHA256 != before.BindingSHA256 || after.Merge.FeatureTip() != j.scene.feature || after.Differs != nil {
			t.Errorf("the card changed when the branch moved: binding %s -> %s, feature tip %s, differs %v",
				before.BindingSHA256, after.BindingSHA256, after.Merge.FeatureTip(), after.Differs)
		}
		out, err := j.approve(o, cid)
		if err != nil || out.State != record.StateApplied {
			t.Fatalf("approve: %+v, %v", out, err)
		}
		merge := j.target()
		if got := j.lab.git("", "rev-list", "--parents", "-n1", merge); got != merge+" "+j.scene.target+" "+j.scene.feature {
			t.Errorf("the merge has the parents %q, want the bound tips", got)
		}
		if j.isAncestor(later, merge) || j.treeHas(merge, "later.txt") {
			t.Errorf("work the lead pushed after the card was made went into the merge")
		}
		if got := j.lab.ref(featureRef); got != later {
			t.Errorf("the lead's branch is at %s: the host moved it", got)
		}
	})
	t.Run("the lead withdraws the request", func(t *testing.T) {
		j := newJoined(t, execFeature)
		reqID, cid := j.card2()
		o := j.decider()
		c, d := j.approval(o, cid)
		if err := j.stand.Withdraw(cid); err != nil {
			t.Fatal(err)
		}
		ended, err := o.Open(cid)
		if err != nil {
			t.Fatal(err)
		}
		if !ended.Ended() || ended.After.State != record.StateWithdrawn {
			t.Fatalf("the card after the withdrawal: ended %v, state %q", ended.Ended(), ended.After.State)
		}
		for _, k := range ended.Keys() {
			if k.Action == card.ActionApprove || k.Action == card.ActionDeny {
				t.Errorf("an ended card offers %q", k.Key)
			}
		}
		if ok, _ := ended.MayApprove(j.reading(o, ended, true), j.clock.Now()); ok {
			t.Errorf("an ended card may be approved")
		}
		if ids := j.pendingIDs(o); len(ids) != 0 {
			t.Errorf("the withdrawn card is still listed as pending: %v", ids)
		}
		// A decision made before the card ended is not taken.
		o.Activity(j.clock.Now())
		if out, err := o.Decide(d); err == nil {
			t.Fatalf("a decision on a withdrawn card was answered: %+v", out)
		} else {
			wantRefused(t, err, record.CodeApprovalVoid)
		}
		if got := j.target(); got != j.scene.target {
			t.Errorf("the target moved to %s", got)
		}
		_ = c
		_ = reqID
		entries := j.journalEntries()
		if k := kinds(entries); k[ownerjournal.KindDecision] != 0 {
			t.Errorf("a decision was journaled for a withdrawn card: %v", k)
		}
		if n := j.intakeMovesAfterStop(); n != 0 {
			t.Errorf("%d ref updates were recorded", n)
		}
	})
}

// intakeMovesAfterStop counts the recorded ref updates once the daemon is
// stopped, by opening the records folder as the daemon's service did.
func (j *joined) intakeMovesAfterStop() int {
	j.t.Helper()
	j.stopDaemon()
	st, err := intake.OpenStore(filepath.Join(j.state, OwnerDirName, RecordsName))
	if err != nil {
		j.t.Fatal(err)
	}
	defer st.Close()
	moves, err := st.Intakes()
	if err != nil {
		j.t.Fatal(err)
	}
	return len(moves)
}

// OC5: the target ref moves after the card, and the owner's decision follows.
// Before the host reads the refs again for the use, the door refuses the
// mismatch and the record is void; after that read and before the update, the
// transaction finds the old value gone and nothing is written. Either way the
// target stays where the other writer put it.
func TestOwnerChannelOC5TheTargetMovesAfterTheCard(t *testing.T) {
	t.Run("before the use is read again", func(t *testing.T) {
		j := newJoined(t, execFeature)
		_, cid := j.card2()
		o := j.decider()
		moved := j.moveTarget()
		out, err := j.approve(o, cid)
		if err != nil || out.State != record.StateVoid {
			t.Fatalf("approve: %+v, %v, want void", out, err)
		}
		if got := j.target(); got != moved {
			t.Errorf("the target is at %s, want the other writer's %s", got, moved)
		}
		shown, err := j.stand.Show(context.Background(), cid)
		if err != nil || shown.Entry.State != record.StateVoid || shown.Entry.ReasonCode != record.CodeBindingMismatch {
			t.Errorf("the door holds %+v (%v), want void for binding-mismatch", shown.Entry, err)
		}
		if n := j.intakeMovesAfterStop(); n != 0 {
			t.Errorf("%d ref updates were recorded", n)
		}
	})
	t.Run("between the read and the update", func(t *testing.T) {
		j := newJoined(t, execFeature)
		_, cid := j.card2()
		o := j.decider()
		var moved string
		j.setFault(func(point string) error {
			if point == "fresh-read" {
				moved = j.moveTarget()
			}
			return nil
		})
		out, err := j.approve(o, cid)
		if err != nil || out.State != record.StateNotApplied {
			t.Fatalf("approve: %+v, %v, want not-applied", out, err)
		}
		if moved == "" || j.target() != moved {
			t.Errorf("the target is at %s, want the other writer's %s", j.target(), moved)
		}
		if n := j.intakeMovesAfterStop(); n != 0 {
			t.Errorf("%d ref updates were recorded", n)
		}
		if len(j.pins()) != 0 {
			t.Errorf("a pin is left after a final outcome: %v", j.pins())
		}
	})
}

// OC6: one decision, used twice. Sending the frame again over the wire, handing
// the same decision to the door again, asking the door for the use again and
// applying the same ref update again each end in one effect: the ref moved
// once. Restoring the store's state file from a copy cannot be shown with the
// stand-in, which keeps nothing on disk: that part is for step 2b, against
// the real door.
func TestOwnerChannelOC6OneDecisionUsedTwice(t *testing.T) {
	ctx := context.Background()
	j := newJoined(t, execFeature)
	_, cid := j.card2()
	p := j.pendingOf(cid)
	w := j.dialWire(true)
	w.beat()
	f := frame(p, true)
	res, err := w.decide(f)
	if err != nil || res.State != record.StateApplied {
		t.Fatalf("the first decision: %+v, %v", res, err)
	}
	merge := j.target()
	if merge != mergeOf(p) {
		t.Fatalf("the target is at %s", merge)
	}

	// The same frame again over the wire.
	res, err = w.decide(f)
	if err == nil {
		t.Fatalf("the same frame was answered again: %+v", res)
	}
	wantRefused(t, err, record.CodeApprovalConsumed)

	// The same decision again at the door, and a second use.
	shown, err := j.stand.Show(ctx, cid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.stand.Decide(ctx, door.RecordOf(shown.Decision.Bytes)); err != nil {
		t.Logf("the door answered the same decision again with %v", err)
	}
	fresh, err := j.o.Host.Fresh(ctx, ownerchannel.FreshInput{Project: "project-a", Pending: p})
	if err != nil {
		t.Fatal(err)
	}
	_, err = j.stand.Use(ctx, door.UseInput{ID: cid, Fresh: fresh})
	wantRefused(t, err, record.CodeApprovalConsumed)

	// A second intake: the same update from the same old value.
	in := ownerchannel.ApplyInput{Project: "project-a", Effects: []record.Effect{{Kind: record.EffectRef, Ref: targetBranch, Old: j.scene.target, New: merge}}}
	if got, err := j.o.Host.Apply(ctx, in); err == nil && got == door.OutcomeApplied {
		t.Errorf("a second apply of the same update said applied")
	}
	if got := j.target(); got != merge {
		t.Errorf("the target is at %s, want the merge once", got)
	}
	if got := j.state0(cid); got != record.StateApplied {
		t.Errorf("the door holds %s", got)
	}
	if n := j.intakeMovesAfterStop(); n != 1 {
		t.Errorf("%d ref updates were recorded, want 1", n)
	}
}

// OC7: a pending request and, separately, an approved one expire. The first
// ends as expired and a decision on it is refused; the second, whose approval
// the door holds but the host did not use before the time ran out, is refused
// at the use as approval-expired and ends expired, and the ref does not move.
// The lead is told in both.
func TestOwnerChannelOC7ExpiryOfAPendingAndOfAnApprovedRequest(t *testing.T) {
	t.Run("pending", func(t *testing.T) {
		j := newJoined(t, execFeature)
		reqID, cid := j.card2()
		o := j.decider()
		_, d := j.approval(o, cid)
		// A pending card lasts a day.
		j.clock.Advance(25 * time.Hour)
		if err := j.o.Service.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
		if got := j.state0(cid); got != record.StateExpired {
			t.Fatalf("the door holds %s, want expired", got)
		}
		if ids := j.pendingIDs(o); len(ids) != 0 {
			t.Errorf("an expired card is listed as pending: %v", ids)
		}
		o.Activity(j.clock.Now())
		if out, err := o.Decide(d); err == nil {
			t.Fatalf("a decision on an expired card was answered: %+v", out)
		} else {
			wantRefused(t, err, record.CodeApprovalExpired)
		}
		if got := j.target(); got != j.scene.target {
			t.Errorf("the target moved to %s", got)
		}
		if reply := j.reply(reqID); !strings.Contains(reply, `"state":"expired"`) {
			t.Errorf("the lead's reply: %s", reply)
		}
	})
	t.Run("approved", func(t *testing.T) {
		j := newJoined(t, execFeature)
		reqID, cid := j.card2()
		o := j.decider()
		j.stopAt("delivered")
		if out, err := j.approve(o, cid); err == nil {
			t.Fatalf("the stop was not reached: %+v", out)
		}
		if got := j.state0(cid); got != record.StateApproved {
			t.Fatalf("the door holds %s, want approved and not used", got)
		}
		j.clearStop()
		j.clock.Advance(31 * time.Minute)
		j.restartDaemon()
		if got := j.state0(cid); got != record.StateExpired {
			t.Errorf("the door holds %s, want expired", got)
		}
		if got := j.target(); got != j.scene.target {
			t.Errorf("the target moved to %s", got)
		}
		if reply := j.reply(reqID); !strings.Contains(reply, `"state":"expired"`) {
			t.Errorf("the lead's reply: %s", reply)
		}
		entries := j.journalEntries()
		// The use was refused before it was made: no use entry, and the one
		// outcome says expired.
		if k := kinds(entries); k[ownerjournal.KindUse] != 0 || k[ownerjournal.KindOutcome] != 1 {
			t.Errorf("journal kinds %v", k)
		}
		for _, e := range entries {
			if e.Kind == ownerjournal.KindOutcome && !strings.Contains(string(e.Body), `"state":"expired"`) {
				t.Errorf("the outcome entry is %s", e.Body)
			}
		}
	})
}

// OC9 (the host's part): the service is stopped at each step of a decision,
// the daemon is started again on the same journal, door and mirror, and the
// real adapter finds out from the refs what happened. The decision is
// delivered once, nothing is applied twice, and the approval ends where the
// mirror shows: a stop before the door's use ends in not-applied (nothing was
// written, and the lead asks again), any other in applied. A stop during a
// verification run is OC21, and the stop of the whole daemon at a seated
// lead's request is step 2b.
func TestOwnerChannelOC9StopsAtEachCrashPoint(t *testing.T) {
	points := []struct {
		point string
		state record.State
	}{
		{"decision-journaled", record.StateApplied},
		{"door-decided", record.StateApplied},
		{"delivered", record.StateApplied},
		{"fresh-read", record.StateApplied},
		{"door-used", record.StateNotApplied},
		{"use-journaled", record.StateNotApplied},
		{"applied", record.StateApplied},
		{"settled", record.StateApplied},
		{"outcome-journaled", record.StateApplied},
	}
	for _, c := range points {
		t.Run(c.point, func(t *testing.T) {
			j := newJoined(t, execFeature)
			reqID, cid := j.card2()
			p := j.pendingOf(cid)
			o := j.decider()
			j.stopAt(c.point)
			if out, err := j.approve(o, cid); err == nil {
				t.Fatalf("the service did not stop at %s: %+v", c.point, out)
			}
			j.clearStop()
			j.restartDaemon()

			wantMoves, wantTarget := 1, mergeOf(p)
			if c.state == record.StateNotApplied {
				wantMoves, wantTarget = 0, j.scene.target
			}
			check := func(when string) {
				t.Helper()
				if got := j.state0(cid); got != c.state {
					t.Errorf("%s: the door holds %s, want %s", when, got, c.state)
				}
				if got := j.target(); got != wantTarget {
					t.Errorf("%s: the target is at %s, want %s", when, got, wantTarget)
				}
				if n := j.intakeMoves(); n != wantMoves {
					t.Errorf("%s: %d ref updates, want %d", when, n, wantMoves)
				}
				if pins := j.pins(); len(pins) != 0 {
					t.Errorf("%s: pins left: %v", when, pins)
				}
				if reply := j.reply(reqID); !strings.Contains(reply, `"state":"`+string(c.state)+`"`) {
					t.Errorf("%s: the lead's reply: %s", when, reply)
				}
			}
			check("after the restart")
			// Another restart changes nothing.
			j.restartDaemon()
			check("after a second restart")

			es := j.journalEntries()
			k := kinds(es)
			if k[ownerjournal.KindDecision] != 1 || k[ownerjournal.KindDelivered] != 1 || k[ownerjournal.KindUse] != 1 || k[ownerjournal.KindOutcome] != 1 {
				t.Errorf("journal kinds %v: want one decision, one delivery, one use and one outcome", k)
			}
			for _, e := range es {
				if e.Kind == ownerjournal.KindOutcome && !strings.Contains(string(e.Body), `"state":"`+string(c.state)+`"`) {
					t.Errorf("the outcome entry is %s", e.Body)
				}
			}
		})
	}
}

// M6 and OC9: a decision that was held at the day's limit is journaled and
// delivers nothing, before and after a restart: the door never sees it, the
// card stays pending with the mark, and no record of the door or of the host's
// summary holds the mark.
func TestOwnerChannelOC9AHeldDecisionIsNotDeliveredByARestart(t *testing.T) {
	ctx := context.Background()
	j := newJoined(t, execFeature)
	o := j.decider()
	o.Activity(j.clock.Now())
	// Lowering the limit to one is one key and needs no typing.
	if v, err := o.SetLimit("project-a", limits.Change{To: 1}); err != nil || v.Limit != 1 {
		t.Fatalf("SetLimit: %+v, %v", v, err)
	}
	_, first := j.card2()
	if out, err := j.approve(o, first); err != nil || out.State != record.StateApplied {
		t.Fatalf("the first approve: %+v, %v", out, err)
	}
	afterFirst := j.target()

	tip := j.addFeature("y", "y.txt", "y\n")
	j.clock.Advance(11 * time.Minute) // the door's repeat throttle is not the case here
	_, second := j.askFor("y", func(rq *record.Request) { rq.FeatureSHA = tip })
	if second == "" {
		t.Fatalf("the second request made no card")
	}
	out, err := j.approve(o, second)
	if err != nil || !out.Held || out.State != record.StatePending {
		t.Fatalf("the second approve: %+v, %v, want held and pending", out, err)
	}
	noDecision := func(when string) {
		t.Helper()
		shown, err := j.stand.Show(ctx, second)
		if err != nil {
			t.Fatal(err)
		}
		if len(shown.Decision.Bytes) != 0 || shown.Entry.State != record.StatePending {
			t.Errorf("%s: the door holds a decision or another state for the held card: %s %d bytes", when, shown.Entry.State, len(shown.Decision.Bytes))
		}
		if strings.Contains(strings.ToLower(string(shown.Pending.Bytes)), "held") {
			t.Errorf("%s: the held mark is in the pending record", when)
		}
		if got := j.target(); got != afterFirst {
			t.Errorf("%s: the target moved to %s", when, got)
		}
	}
	noDecision("after the hold")
	j.restartDaemon()
	noDecision("after a restart")
	o2 := j.decider()
	l, err := o2.List()
	if err != nil || len(l.Pending) != 1 || l.Pending[0].ID != second || !l.Pending[0].Held {
		t.Fatalf("the listing after the restart: %+v, %v", l.Pending, err)
	}
	// A deny is never held.
	c, err := o2.Open(second)
	if err != nil {
		t.Fatal(err)
	}
	dd, ok, why := c.Deny("no", j.clock.Now())
	if !ok {
		t.Fatalf("Deny: %s", why)
	}
	o2.Activity(j.clock.Now())
	if out, err := o2.Decide(dd); err != nil || out.Held || out.State != record.StateDenied {
		t.Fatalf("a deny of the held card: %+v, %v", out, err)
	}
	es := j.journalEntries()
	if k := kinds(es); k[ownerjournal.KindHeld] != 1 || k[ownerjournal.KindDelivered] != 2 {
		t.Errorf("journal kinds %v: want one held entry and the first approve and the deny delivered", k)
	}
}

// OC16: a flagged path in the change, approved without its sign-off, with a
// sign-off for a path that is not flagged, and with a sign-off of the wrong
// kind, by a crafted decision frame over the wire: each is refused
// decision-invalid, journaled as refused, and the door holds no decision. With
// the exact sign-offs the record lists them. (The console does not offer
// Approve in the first two cases: card.MayApprove says no.)
func TestOwnerChannelOC16MissingAndSurplusSignOff(t *testing.T) {
	j := newJoined(t, execFeature)
	_, cid := j.card2()
	p := j.pendingOf(cid)
	o := j.decider()
	c, err := o.Open(cid)
	if err != nil {
		t.Fatal(err)
	}
	if ok, why := c.MayApprove(j.reading(o, c, false), j.clock.Now()); ok || !strings.Contains(why, "sign") {
		t.Errorf("MayApprove for a card with a flagged path that is not signed off: %v, %q", ok, why)
	}
	extra := j.reading(o, c, true)
	extra.Toggle(record.SignOff{Kind: record.FlagExecConfig, Path: "feature.txt"})
	if ok, _ := c.MayApprove(extra, j.clock.Now()); ok {
		t.Errorf("the console may approve with a sign-off for a path that is not flagged")
	}
	// Only one connection may decide: the console's client lets go of the
	// place, and the crafted frames are sent from a connection of their own.
	_ = o.Close()

	w := j.dialWire(true)
	w.beat()
	exact := frame(p, true)
	if len(exact.SignedOff) != 1 || exact.SignedOff[0].Path != "Makefile" {
		t.Fatalf("the exact sign-offs are %+v", exact.SignedOff)
	}
	cases := []struct {
		name string
		off  []record.SignOff
	}{
		{"missing", []record.SignOff{}},
		{"surplus", append(append([]record.SignOff{}, exact.SignedOff...), record.SignOff{Kind: record.FlagExecConfig, Path: "feature.txt"})},
		{"only a path that is not flagged", []record.SignOff{{Kind: record.FlagExecConfig, Path: "feature.txt"}}},
		{"the wrong kind", []record.SignOff{{Kind: record.FlagOddPath, Path: "Makefile"}}},
	}
	for _, c := range cases {
		f := exact
		f.SignedOff = c.off
		if res, err := w.decide(f); err == nil {
			t.Fatalf("%s: the frame was accepted: %+v", c.name, res)
		} else {
			wantRefused(t, err, record.CodeDecisionInvalid)
		}
	}
	shown, err := j.stand.Show(context.Background(), cid)
	if err != nil || len(shown.Decision.Bytes) != 0 || shown.Entry.State != record.StatePending {
		t.Fatalf("the door after the refusals: %v %d bytes (%v)", shown.Entry.State, len(shown.Decision.Bytes), err)
	}
	if j.target() != j.scene.target {
		t.Fatalf("the target moved")
	}
	res, err := w.decide(exact)
	if err != nil || res.State != record.StateApplied {
		t.Fatalf("the exact frame: %+v, %v", res, err)
	}
	shown, err = j.stand.Show(context.Background(), cid)
	if err != nil {
		t.Fatal(err)
	}
	dec, err := record.DecodeDecision(shown.Decision.Bytes)
	if err != nil || len(dec.SignedOff) != 1 || dec.SignedOff[0] != (record.SignOff{Kind: record.FlagExecConfig, Path: "Makefile"}) {
		t.Errorf("the decision lists %+v (%v)", dec.SignedOff, err)
	}
	es := j.journalEntries()
	refused := 0
	for _, e := range es {
		if e.Kind == ownerjournal.KindRefused {
			refused++
			if !strings.Contains(string(e.Body), string(record.CodeDecisionInvalid)) {
				t.Errorf("a refused entry holds %s", e.Body)
			}
		}
	}
	if refused != len(cases) {
		t.Errorf("%d refused entries, want %d", refused, len(cases))
	}
}

// OC17: a decision frame with a wrong binding digest, an unknown id, a changed
// byte, and after the seat moved on (its epoch, then its revision) each is
// refused with its code, and the door's store holds no decision and the
// target does not move.
func TestOwnerChannelOC17WrongDigestEpochRevisionOrId(t *testing.T) {
	type tc struct {
		name string
		// prepare changes the world after the card; it returns the frame.
		frame func(j *joined, p record.Pending) ownerwire.DecisionFrame
		code  record.Code
	}
	cases := []tc{
		{"a wrong digest", func(j *joined, p record.Pending) ownerwire.DecisionFrame {
			f := frame(p, true)
			f.BindingSHA256 = strings.Repeat("0", 64)
			return f
		}, record.CodeBindingMismatch},
		{"one byte of the digest changed", func(j *joined, p record.Pending) ownerwire.DecisionFrame {
			f := frame(p, true)
			b := []byte(f.BindingSHA256)
			if b[10] == 'a' {
				b[10] = 'b'
			} else {
				b[10] = 'a'
			}
			f.BindingSHA256 = string(b)
			return f
		}, record.CodeBindingMismatch},
		{"an unknown id", func(j *joined, p record.Pending) ownerwire.DecisionFrame {
			f := frame(p, true)
			f.ID = "ap-20261120-0099"
			return f
		}, record.CodeDecisionInvalid},
		{"a changed byte of a sign-off", func(j *joined, p record.Pending) ownerwire.DecisionFrame {
			f := frame(p, true)
			f.SignedOff = []record.SignOff{{Kind: record.FlagExecConfig, Path: "Makefilf"}}
			return f
		}, record.CodeDecisionInvalid},
		{"another epoch", func(j *joined, p record.Pending) ownerwire.DecisionFrame {
			j.stand.SetEpoch(8)
			return frame(p, true)
		}, record.CodePreviousEpoch},
		{"another revision of the seat", func(j *joined, p record.Pending) ownerwire.DecisionFrame {
			j.stand.SetSeatRev(13)
			return frame(p, true)
		}, record.CodeApprovalVoid},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			j := newJoined(t, execFeature)
			_, cid := j.card2()
			p := j.pendingOf(cid)
			f := c.frame(j, p)
			w := j.dialWire(true)
			w.beat()
			res, err := w.decide(f)
			if err == nil {
				t.Fatalf("the frame was accepted: %+v", res)
			}
			wantRefused(t, err, c.code)
			shown, err := j.stand.Show(context.Background(), cid)
			if err != nil || len(shown.Decision.Bytes) != 0 {
				t.Errorf("the door holds a decision: %d bytes (%v)", len(shown.Decision.Bytes), err)
			}
			if j.target() != j.scene.target {
				t.Errorf("the target moved")
			}
			if n := j.intakeMovesAfterStop(); n != 0 {
				t.Errorf("%d ref updates were recorded", n)
			}
		})
	}
}

// putMany adds n files with the same content, named dir/f0000.txt and on, in
// one call of git.
func (l *lab) putMany(dir string, n int, content string) []string {
	l.t.Helper()
	id := l.git(content, "hash-object", "-w", "--stdin")
	var in strings.Builder
	var names []string
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("%s/f%04d.txt", dir, i)
		names = append(names, name)
		fmt.Fprintf(&in, "100644 %s\t%s\n", id, name)
	}
	l.git(in.String(), "update-index", "--index-info")
	return names
}

// putLink adds a symbolic link to the index.
func (l *lab) putLink(path, target string) { l.putMode("120000", path, target) }

// putGitlink adds a submodule entry at a commit id that need not exist.
func (l *lab) putGitlink(path, id string) {
	l.t.Helper()
	l.git("", "update-index", "--add", "--cacheinfo", "160000,"+id+","+path)
}

// remove takes a path out of the index.
func (l *lab) remove(path string) {
	l.t.Helper()
	l.git("0 "+strings.Repeat("0", 40)+"\t"+path+"\n", "update-index", "--index-info")
}

// numstat is the independent count of the change from base to tip: git's own
// numbers for each path, a binary file (by git's rule) as no lines.
func (l *lab) numstat(base, tip string) map[string][2]int {
	l.t.Helper()
	out := map[string][2]int{}
	for _, line := range strings.Split(l.git("", "diff", "--numstat", "--no-renames", base, tip), "\n") {
		if line == "" {
			continue
		}
		f := strings.SplitN(line, "\t", 3)
		var c [2]int
		c[0], _ = strconv.Atoi(f[0])
		c[1], _ = strconv.Atoi(f[1])
		out[f[2]] = c
	}
	return out
}

// sums returns the files, the added and the removed lines of a numstat, less
// the paths named, which git counts by a rule the intake does not follow.
func sums(m map[string][2]int, except ...string) (files, added, removed int) {
	for path, c := range m {
		files++
		if slices.Contains(except, path) {
			continue
		}
		added += c[0]
		removed += c[1]
	}
	return files, added, removed
}

const (
	subA = "1111111111111111111111111111111111111111"
	subB = "2222222222222222222222222222222222222222"
)

// knownScene is the history of the OC15 case: a root with a few files, a
// target that moved on, and a feature that holds a rename, a mode change to
// 100755, a submodule bump, a binary file whose first NUL is after the 8,000th
// byte, a new symbolic link, a deleted file, a changed text file and an
// execution-config path.
func knownScene(l *lab) scene {
	l.t.Helper()
	l.put("README.md", "readme\n")
	l.put("old-name.txt", "one\ntwo\nthree\n")
	l.put("gone.txt", "bye\n")
	l.put("tool.sh", "echo hi\n")
	l.put("notes.txt", "a\nb\n")
	l.putGitlink("vendor/sub", subA)
	s := scene{root: l.commit("root\n")}
	l.put("target-side.txt", "target\n")
	s.target = l.commit("target moves\n", s.root)
	l.load(s.root)
	l.remove("old-name.txt")
	l.put("new-name.txt", "one\ntwo\nthree\n")
	l.remove("gone.txt")
	id := l.git("", "rev-parse", s.root+":tool.sh")
	l.git("", "update-index", "--cacheinfo", "100755,"+id+",tool.sh")
	l.put("notes.txt", "a\nb\nc\nd\n")
	l.putGitlink("vendor/sub", subB)
	l.put("big.bin", strings.Repeat("a", 8100)+"\x00tail\n")
	l.putLink("link", "notes.txt")
	l.put("Makefile", "test:\n\ttrue\n")
	s.feature = l.commit("feature\n", s.root)
	l.setRef(targetBranch, s.target)
	l.setRef(featureRef, s.feature)
	return s
}

// OC15: the summary of a known change set, as the card shows it, equals what
// was written by hand and what git itself counts: the rename is a delete and
// an add, the mode change, the submodule bump, the binary file with a late
// NUL and the symbolic link are each in their own list, the deleted file is
// there, the execution-config path is flagged, and the owner can approve only
// with the sign-off of each flagged path (the symbolic link and the
// submodule are flagged too). A change of 5,000 paths is cut on the card and
// read in pages, and the card may not be approved before the last page.
func TestOwnerChannelOC15TheSummaryOfAKnownChangeSet(t *testing.T) {
	t.Run("a known change set", func(t *testing.T) {
		j := newJoinedScene(t, knownScene)
		_, cid := j.card2()
		o := j.decider()
		c, err := o.Open(cid)
		if err != nil {
			t.Fatal(err)
		}
		d := c.Diff
		if d == nil || d.Truncated || d.Base != j.scene.target || d.Tip != mergeOf(j.pendingOf(cid)) {
			t.Fatalf("the diff is %+v", d)
		}
		type row struct {
			path, status, oldMode, newMode string
			added, removed                 int64
		}
		want := []row{
			{"Makefile", "A", "000000", "100644", 2, 0},
			{"big.bin", "A", "000000", "100644", 0, 0},
			{"gone.txt", "D", "100644", "000000", 0, 1},
			{"link", "A", "000000", "120000", 1, 0},
			{"new-name.txt", "A", "000000", "100644", 3, 0},
			{"notes.txt", "M", "100644", "100644", 2, 0},
			{"old-name.txt", "D", "100644", "000000", 0, 3},
			{"tool.sh", "M", "100644", "100755", 0, 0},
			{"vendor/sub", "M", "160000", "160000", 0, 0},
		}
		var got []row
		for _, r := range d.Rows {
			got = append(got, row{lt(r.Path), r.Status, r.OldMode, r.NewMode, r.Added, r.Removed})
		}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("rows\n got  %v\n want %v", got, want)
		}
		// git counts the submodule bump as a line out and a line in, and a
		// file whose first NUL is after its 8,000th byte as text; the
		// intake counts the first as no lines and the second as binary, as
		// the design has it. Those two are left out of git's sums, after a
		// check that git does count them.
		ns := j.lab.numstat(j.scene.target, d.Tip)
		if ns["vendor/sub"] != [2]int{1, 1} || ns["big.bin"] != [2]int{1, 0} {
			t.Fatalf("git counts the submodule bump as %v and the late-NUL file as %v", ns["vendor/sub"], ns["big.bin"])
		}
		files, added, removed := sums(ns, "vendor/sub", "big.bin")
		if d.Files != 9 || d.Added != 8 || d.Removed != 4 {
			t.Errorf("totals: files %d added %d removed %d, want 9, 8 and 4", d.Files, d.Added, d.Removed)
		}
		if d.Files != int64(files) || d.Added != int64(added) || d.Removed != int64(removed) {
			t.Errorf("totals %d/%d/%d differ from git's own count %d/%d/%d", d.Files, d.Added, d.Removed, files, added, removed)
		}
		// New bytes: the new side of each added or changed blob; the mode
		// change alone and the submodule bump add none.
		if wantBytes := int64(14 + 8 + 9 + 12 + 8106); d.NewBytes != wantBytes {
			t.Errorf("new bytes %d, want %d", d.NewBytes, wantBytes)
		}
		lists := map[card.ListKind][]string{}
		for _, l := range c.Lists {
			for _, p := range l.Paths {
				lists[l.Kind] = append(lists[l.Kind], lt(p))
			}
		}
		for kind, want := range map[card.ListKind][]string{
			card.ListBinary: {"big.bin"}, card.ListSymlinks: {"link"},
			card.ListModeChanges: {"tool.sh"}, card.ListSubmodules: {"vendor/sub"},
		} {
			if fmt.Sprint(lists[kind]) != fmt.Sprint(want) {
				t.Errorf("%s list: %v, want %v", kind.Label(), lists[kind], want)
			}
		}
		var flagged []string
		for _, f := range c.Flagged {
			flagged = append(flagged, f.Kind+":"+lt(f.Path))
		}
		if fmt.Sprint(flagged) != fmt.Sprint([]string{"exec-config:Makefile", "symlink:link", "submodule:vendor/sub"}) {
			t.Errorf("flagged %v", flagged)
		}
		// Approve is not offered without every sign-off, and is with them.
		if ok, _ := c.MayApprove(j.reading(o, c, false), j.clock.Now()); ok {
			t.Errorf("the card may be approved with no sign-off")
		}
		if out, err := j.approve(o, cid); err != nil || out.State != record.StateApplied {
			t.Fatalf("approve: %+v, %v", out, err)
		}
		if got := j.target(); got != d.Tip {
			t.Errorf("the target is at %s, want %s", got, d.Tip)
		}
	})
	t.Run("5,000 paths", func(t *testing.T) {
		var names []string
		j := newJoinedScene(t, func(l *lab) scene {
			return newScene(l, func(l *lab) { names = l.putMany("many", 5000, "x\n") })
		})
		_, cid := j.card2()
		o := j.decider()
		c, err := o.Open(cid)
		if err != nil {
			t.Fatal(err)
		}
		if c.Diff == nil || !c.Diff.Truncated || c.Diff.Files != 5000 || len(c.Diff.Rows) >= 5000 || c.Diff.TruncatedLine() == "" {
			t.Fatalf("the diff: %+v", c.Diff)
		}
		files, added, _ := sums(j.lab.numstat(j.scene.target, c.Diff.Tip))
		if files != 5000 || c.Diff.Added != int64(added) {
			t.Errorf("git counts %d files and %d lines, the card %d and %d", files, added, c.Diff.Files, c.Diff.Added)
		}
		// Not approvable until every page is read, in any order.
		if ok, why := c.MayApprove(card.Reading{Opened: j.clock.Now().Add(-time.Second), Differs: true, Refs: true,
			Lists: [card.NumLists]bool{true, true, true, true}}, j.clock.Now()); ok || why != card.ReasonPathPages {
			t.Errorf("a card whose path list was not read: %v, %q", ok, why)
		}
		first, err := o.Page(cid, card.PagePaths, 0)
		if err != nil || first.Pages < 2 {
			t.Fatalf("page 0: %v, %d pages", err, first.Pages)
		}
		seen := map[string]bool{}
		rd := j.reading(o, card.Card{}, false) // an empty card: nothing to page
		rd.Paths = false
		for n := first.Pages - 1; n >= 0; n-- { // from the end, as a jump would
			pg, err := o.Page(cid, card.PagePaths, n)
			if err != nil {
				t.Fatalf("page %d: %v", n, err)
			}
			rd.ReadPage(pg)
			if n == 0 {
				if ok, _ := c.MayApprove(withBasics(rd, j), j.clock.Now()); !ok {
					t.Errorf("every page was read and the card may not be approved")
				}
			} else if ok, why := c.MayApprove(withBasics(rd, j), j.clock.Now()); ok || why != card.ReasonPathPages {
				t.Errorf("approvable after only the last %d pages: %v %q", first.Pages-n, ok, why)
			}
			for _, p := range pg.Paths {
				seen[string(p.Path)] = true
			}
		}
		if len(seen) != 5000 {
			t.Errorf("the pages hold %d distinct paths, want 5000", len(seen))
		}
		for _, n := range names {
			if !seen[n] {
				t.Fatalf("the path %s is in no page", n)
			}
		}
		// M4: the patch is longer than one page too.
		pg0, err := o.Page(cid, card.PagePatch, 0)
		if err != nil || pg0.Pages < 2 {
			t.Fatalf("patch page 0: %v, %d pages", err, pg0.Pages)
		}
		diffs := 0
		for n := 0; n < pg0.Pages; n++ {
			pg, err := o.Page(cid, card.PagePatch, n)
			if err != nil {
				t.Fatalf("patch page %d: %v", n, err)
			}
			for i, l := range pg.Patch {
				if strings.HasPrefix(l, "diff --git ") && (i == 0 || !pg.Cont[i-1]) {
					diffs++
				}
			}
		}
		if diffs != 5000 {
			t.Errorf("the patch holds %d file diffs, want 5000", diffs)
		}
	})
}

// withBasics completes a reading of the path pages with what the rest of a card
// without lists and flags needs.
func withBasics(r card.Reading, j *joined) card.Reading {
	r.Opened = j.clock.Now().Add(-time.Second)
	r.Differs, r.Refs = true, true
	r.Lists = [card.NumLists]bool{true, true, true, true}
	return r
}

// M4: a patch with a line longer than a page is read in pieces and joined
// byte for byte, and equals what git itself prints for the file.
func TestOwnerChannelM4ALongPatchLineIsReadInPieces(t *testing.T) {
	long := strings.Repeat("a", 300_000)
	j := newJoined(t, func(l *lab) { l.put("long.txt", long+"\n"); l.put("short.txt", "s\n") })
	_, cid := j.card2()
	o := j.decider()
	if _, err := o.Open(cid); err != nil {
		t.Fatal(err)
	}
	var lines []string
	var cont bool
	pages := 0
	for n := 0; ; n++ {
		pg, err := o.Page(cid, card.PagePatch, n)
		if err != nil {
			t.Fatalf("patch page %d: %v", n, err)
		}
		pages = pg.Pages
		for i, l := range pg.Patch {
			if cont && len(lines) > 0 {
				lines[len(lines)-1] += l
			} else {
				lines = append(lines, l)
			}
			cont = pg.Cont[i]
		}
		if n+1 >= pg.Pages {
			break
		}
	}
	if pages < 2 {
		t.Fatalf("a patch with a line of 300000 bytes is %d page", pages)
	}
	merge := j.target()
	_ = merge
	tip := j.pendingOf(cid).Binding.Merge.Commit
	want := j.lab.git("", "diff", "--no-renames", "--no-color", j.scene.target, tip)
	// The intake writes its own header line and abbreviates object ids
	// otherwise than git's default; the rest of the lines agree.
	notIndex := func(in []string) []string {
		var out []string
		for _, l := range in {
			if !strings.HasPrefix(l, "index ") {
				out = append(out, l)
			}
		}
		return out
	}
	start := 0
	for start < len(lines) && !strings.HasPrefix(lines[start], "diff --git ") {
		start++
	}
	got := notIndex(lines[start:])
	wantLines := notIndex(strings.Split(want, "\n"))
	if len(got) != len(wantLines) && len(got) != len(wantLines)-1 {
		t.Fatalf("the patch has %d lines, git's has %d", len(got), len(wantLines))
	}
	sawLong := false
	for _, l := range got {
		if l == "+"+long {
			sawLong = true
		}
	}
	if !sawLong {
		t.Errorf("the long line did not come back whole")
	}
	for i := range wantLines {
		if i < len(got) && got[i] != wantLines[i] {
			t.Errorf("line %d differs: %.60q and git's %.60q", i, got[i], wantLines[i])
			break
		}
	}
}

// lt returns the text of a lead's string as the card draws it, which for the
// plain names of these tests is the name.
func lt(t card.LeadText) string {
	s, _ := t.Line(1000)
	return s
}

// rawCommit writes a commit object from its text, as no git command would
// make it (control bytes in the names, a date far in the future, headers git
// does not write), and returns its id. The index is read for the tree.
func (l *lab) rawCommit(text func(tree string) string) string {
	l.t.Helper()
	tree := l.git("", "write-tree")
	return l.git(text(tree), "hash-object", "-t", "commit", "-w", "--stdin", "--literally")
}

// OC18: a lead pushes its own merge commit and names it as the feature tip: it
// is ordinary feature history, and the host builds a merge of its own with the
// target's tip first and the lead's commit second; nothing the lead made
// becomes the commit the target moves to. A feature that does not contain the
// target is the usual case. A feature whose merge conflicts is refused
// merge-conflict, with no card and no pin.
func TestOwnerChannelOC18ALeadMadeMergeNamedAsTheTip(t *testing.T) {
	t.Run("the lead's merge is feature history", func(t *testing.T) {
		var leadMerge string
		j := newJoinedScene(t, func(l *lab) scene {
			l.put("README.md", "readme\n")
			s := scene{root: l.commit("root\n")}
			l.put("target-side.txt", "target\n")
			s.target = l.commit("target moves\n", s.root)
			l.load(s.root)
			l.put("side.txt", "side\n")
			side := l.commit("side work\n", s.root)
			// The lead merges the target into its work, by hand, and
			// names that merge as the feature's tip.
			l.load(s.target)
			l.put("side.txt", "side\n")
			s.feature = l.commit("lead's own merge\n", s.target, side)
			leadMerge = s.feature
			l.setRef(targetBranch, s.target)
			l.setRef(featureRef, s.feature)
			return s
		})
		_, cid := j.card2()
		p := j.pendingOf(cid)
		merge := mergeOf(p)
		if merge == leadMerge {
			t.Fatalf("the lead's merge commit was taken as the merge")
		}
		if got := j.lab.git("", "rev-list", "--parents", "-n1", merge); got != merge+" "+j.scene.target+" "+leadMerge {
			t.Errorf("the host's merge has the parents %q, want the target's tip and the lead's merge", got)
		}
		if id := j.lab.git("", "log", "-1", "--format=%an <%ae>|%cn <%ce>", merge); id != "Host Identity <host@example.invalid>|Host Identity <host@example.invalid>" {
			t.Errorf("the merge's author and committer are %q", id)
		}
		o := j.decider()
		if out, err := j.approve(o, cid); err != nil || out.State != record.StateApplied {
			t.Fatalf("approve: %+v, %v", out, err)
		}
		if got := j.target(); got != merge || got == leadMerge {
			t.Errorf("the target is at %s, want the host's merge %s (the lead's is %s)", got, merge, leadMerge)
		}
	})
	t.Run("a feature that does not contain the target", func(t *testing.T) {
		j := newJoined(t, nil)
		_, cid := j.card2()
		merge := mergeOf(j.pendingOf(cid))
		if j.isAncestor(j.scene.target, j.scene.feature) {
			t.Fatalf("the scene's feature contains the target")
		}
		if got := j.lab.git("", "rev-list", "--parents", "-n1", merge); got != merge+" "+j.scene.target+" "+j.scene.feature {
			t.Errorf("the merge has the parents %q", got)
		}
	})
	t.Run("a feature whose merge conflicts", func(t *testing.T) {
		j := newJoinedScene(t, func(l *lab) scene {
			l.put("README.md", "one\ntwo\nthree\n")
			s := scene{root: l.commit("root\n")}
			l.put("README.md", "one\ntarget\nthree\n")
			s.target = l.commit("target edits\n", s.root)
			l.load(s.root)
			l.put("README.md", "one\nfeature\nthree\n")
			s.feature = l.commit("feature edits\n", s.root)
			l.setRef(targetBranch, s.target)
			l.setRef(featureRef, s.feature)
			return s
		})
		before := j.lab.refs()
		id := j.ask()
		st, ok := j.o.Service.Status(id)
		if !ok || st.State != ownerchannel.RequestRefused || st.Code != record.CodeMergeConflict || st.PendingID != "" {
			t.Fatalf("status %+v, want refused merge-conflict with no card", st)
		}
		if got := j.lab.refs(); got != before {
			t.Errorf("refs changed:\n%s\nwant\n%s", got, before)
		}
		if rp := j.readReply(id); rp.Code != string(record.CodeMergeConflict) || rp.State != "" {
			t.Errorf("reply %+v", rp)
		}
		if l, err := j.stand.List(context.Background()); err != nil || len(l) != 0 {
			t.Errorf("the door lists %d records (%v)", len(l), err)
		}
	})
}

// OC33: feature commits with hostile author and committer names, dates far in
// the future, a header git does not write and a message full of escapes.
// The host's merge has the host's author, committer, dates and message and
// nothing else: its raw commit has no extra header, and what git prints of it
// in a plain terminal holds no byte of the lead's but the feature's name.
func TestOwnerChannelOC33HostileCommitHeaders(t *testing.T) {
	const evilName = "Evil\x1b]0;pwn\x07 Name"
	j := newJoinedScene(t, func(l *lab) scene {
		l.put("README.md", "readme\n")
		s := scene{root: l.commit("root\n")}
		l.put("target-side.txt", "target\n")
		s.target = l.commit("target moves\n", s.root)
		l.load(s.root)
		l.put("feature.txt", "one\n")
		s.feature = l.rawCommit(func(tree string) string {
			return "tree " + tree + "\nparent " + s.root + "\n" +
				"author " + evilName + " <evil\x1b[31m@example.invalid> 9999999999 +0000\n" +
				"committer Other\x1b[2J Person <o\x07@example.invalid> 4102444800 +0000\n" +
				"x-injected-header: yes\nencoding ISO-8859-1\n\n" +
				"message \x1b[31mred\x1b[0m \r\x08\x08 bell\x07 and a second line\n"
		})
		l.setRef(targetBranch, s.target)
		l.setRef(featureRef, s.feature)
		return s
	})
	_, cid := j.card2()
	merge := mergeOf(j.pendingOf(cid))
	raw := j.lab.git("", "cat-file", "commit", merge)
	head, msg, _ := strings.Cut(raw, "\n\n")
	wantHead := strings.Join([]string{
		"tree " + j.lab.git("", "rev-parse", merge+"^{tree}"),
		"parent " + j.scene.target,
		"parent " + j.scene.feature,
		fmt.Sprintf("author Host Identity <host@example.invalid> %d +0000", start.Unix()),
		fmt.Sprintf("committer Host Identity <host@example.invalid> %d +0000", start.Unix()),
	}, "\n")
	if head != wantHead {
		t.Errorf("the merge's headers\n%s\nwant\n%s", head, wantHead)
	}
	// The message is the host's: the feature's name, the target's and the
	// request's id, which the host checked.
	if want := "Merge feature/x into develop\n\nRequest-Id: rq-test-01"; strings.TrimSuffix(msg, "\n") != want {
		t.Errorf("the merge's message is %q, want %q", msg, want)
	}
	// What a terminal would be shown.
	for _, format := range []string{"--format=fuller", "--format=raw"} {
		out := j.lab.git("", "log", "-1", format, merge)
		for i := 0; i < len(out); i++ {
			if c := out[i]; c != '\n' && (c < 0x20 || c > 0x7e) {
				t.Errorf("git log %s prints the byte %#x", format, c)
				break
			}
		}
		for _, bad := range []string{"Evil", "pwn", "Other", "9999999999", "4102444800", "red", "bell", "x-injected"} {
			if strings.Contains(out, bad) {
				t.Errorf("git log %s prints %q of the lead's", format, bad)
			}
		}
	}
	// The card does not show the lead's headers as the host's.
	o := j.decider()
	c, err := o.Open(cid)
	if err != nil {
		t.Fatal(err)
	}
	if c.Merge == nil || c.Merge.BuiltBy == "" {
		t.Errorf("the card does not say that the host built the merge: %+v", c.Merge)
	}
	if out, err := j.approve(o, cid); err != nil || out.State != record.StateApplied {
		t.Fatalf("approve: %+v, %v", out, err)
	}
}

// OC21: a restart of the console, of the daemon and of both, with a card
// pending and a request being verified. The pending card is listed again from
// the store with the same binding; a new console makes a new code; nothing was
// decided meanwhile. A verification that the stop interrupted leaves no card
// and no pin and the lead is told; asking again runs the verification from the
// start.
func TestOwnerChannelOC21RestartsOfTheConsoleAndTheDaemon(t *testing.T) {
	j := newJoined(t, execFeature)
	_, cid := j.card2()
	p := j.pendingOf(cid)
	o1 := j.decider()
	md1 := newModel(t, o1, j.clock, "7Q4K")
	md1.key("enter")
	if md1.code() != "7Q4K" {
		t.Fatalf("the first console's code is %q:\n%s", md1.code(), md1.screen())
	}
	nothingDecided := func(when string) {
		t.Helper()
		if got := j.state0(cid); got != record.StatePending {
			t.Errorf("%s: the door holds %s", when, got)
		}
		if got := j.target(); got != j.scene.target {
			t.Errorf("%s: the target moved to %s", when, got)
		}
		shown, err := j.stand.Show(context.Background(), cid)
		if err != nil || len(shown.Decision.Bytes) != 0 {
			t.Errorf("%s: the door holds a decision (%v)", when, err)
		}
	}
	listed := func(o interface {
		List() (card.Listing, error)
	}, when string) {
		t.Helper()
		l, err := o.List()
		if err != nil || len(l.Pending) != 1 || l.Pending[0].ID != cid {
			t.Fatalf("%s: the listing is %+v (%v)", when, l.Pending, err)
		}
	}

	// A console restart: a new client and a new model, a new code, the same card.
	_ = o1.Close()
	o2 := j.decider()
	listed(o2, "after a console restart")
	md2 := newModel(t, o2, j.clock, "ACDE")
	md2.key("enter")
	if md2.code() != "ACDE" {
		t.Errorf("the restarted console's code is %q, want a new one", md2.code())
	}
	c2, err := o2.Open(cid)
	if err != nil || c2.BindingSHA256 != p.BindingSHA256 {
		t.Errorf("the card after a console restart: %v, binding %s", err, c2.BindingSHA256)
	}
	nothingDecided("after a console restart")

	// A daemon restart.
	j.restartDaemon()
	o3 := j.decider()
	listed(o3, "after a daemon restart")
	md3 := newModel(t, o3, j.clock, "FGHJ")
	md3.key("enter")
	if md3.code() != "FGHJ" {
		t.Errorf("the code after a daemon restart is %q, want a new one", md3.code())
	}
	c3, err := o3.Open(cid)
	if err != nil || c3.BindingSHA256 != p.BindingSHA256 {
		t.Errorf("the card after a daemon restart: %v, binding %s", err, c3.BindingSHA256)
	}
	nothingDecided("after a daemon restart")

	// Both.
	_ = o3.Close()
	j.restartDaemon()
	o4 := j.decider()
	listed(o4, "after both")
	nothingDecided("after both")

	// A request that is being verified when the daemon stops. The suite is
	// held until the host cancels it.
	tip := j.addFeature("y", "y.txt", "y\n")
	started := make(chan struct{})
	j.suiteHook(func(ctx context.Context, req SuiteRequest) (SuiteResult, error) {
		close(started)
		<-ctx.Done()
		return SuiteResult{}, ctx.Err()
	})
	j.restartDaemon()
	runsBefore := j.suite.count()
	id, b := j.request("y", func(rq *record.Request) { rq.FeatureSHA = tip })
	j.dropFile(id, b)
	scanned := make(chan struct{})
	go func() {
		defer close(scanned)
		_, _ = j.o.Collector.Scan(context.Background())
	}()
	select {
	case <-started:
	case <-time.After(30 * time.Second):
		t.Fatal("the verification did not start")
	}
	j.stopDaemon()
	select {
	case <-scanned:
	case <-time.After(30 * time.Second):
		t.Fatal("the collector did not return after the daemon stopped")
	}
	if len(j.pins()) != 1 {
		t.Errorf("pins after the interrupted verification: %v, want only the pending card's", j.pins())
	}
	if rp := j.readReply(id); rp.Code != string(record.CodeRequestInvalid) || rp.State != "" {
		t.Errorf("the lead's reply to the interrupted request: %+v", rp)
	}
	if got := j.suite.count(); got != runsBefore {
		t.Errorf("the fake suite counted %d runs: the held suite is not the fake", got)
	}
	// Asking again, with a new id, runs the verification from the start.
	j.suiteHook(nil)
	j.startDaemon()
	listed(j.decider(), "after the interrupted verification")
	id2, card2 := j.askFor("y", func(rq *record.Request) { rq.FeatureSHA = tip })
	if card2 == "" {
		st, _ := j.o.Service.Status(id2)
		t.Fatalf("asking again made no card: %+v", st)
	}
	if got := j.suite.count(); got != runsBefore+1 {
		t.Errorf("the suite ran %d times for the second ask, want one run from the start", got-runsBefore)
	}
	nothingDecided("at the end")
}

// OC24: the journal and the door disagree. A decision the journal has and the
// door lacks is delivered at the restart; a decision the door holds and the
// journal lacks is refused: an alert is journaled, the approval is never used
// and the listing says so, while another card goes on and is applied.
func TestOwnerChannelOC24TheJournalAndTheDoorDisagree(t *testing.T) {
	ctx := context.Background()
	j := newJoined(t, nil)
	_, journaled := j.card2()
	tipY := j.addFeature("y", "y.txt", "y\n")
	_, forged := j.askFor("y", func(rq *record.Request) { rq.FeatureSHA = tipY })
	tipZ := j.addFeature("z", "z.txt", "z\n")
	_, other := j.askFor("z", func(rq *record.Request) { rq.FeatureSHA = tipZ })
	if journaled == "" || forged == "" || other == "" {
		t.Fatalf("cards: %q %q %q", journaled, forged, other)
	}
	o := j.decider()

	// A deny that the journal has and the door lacks.
	c, err := o.Open(journaled)
	if err != nil {
		t.Fatal(err)
	}
	deny, ok, why := c.Deny("no", j.clock.Now())
	if !ok {
		t.Fatalf("Deny: %s", why)
	}
	j.stopAt("decision-journaled")
	o.Activity(j.clock.Now())
	if out, err := o.Decide(deny); err == nil {
		t.Fatalf("the stop was not reached: %+v", out)
	}
	j.clearStop()
	// An approval the door holds and no journal entry explains, made at the
	// door directly, by no console.
	fp := j.pendingOf(forged)
	if _, err := j.stand.Decide(ctx, forgedApproval(t, j, fp)); err != nil {
		t.Fatal(err)
	}
	j.restartDaemon()

	if got := j.state0(journaled); got != record.StateDenied {
		t.Errorf("the journaled decision's card is %s at the door, want denied (delivered at the restart)", got)
	}
	if got := j.state0(forged); got != record.StateApproved {
		t.Errorf("the forged approval's card is %s", got)
	}
	if got := j.target(); got != j.scene.target {
		t.Errorf("the forged approval was used: the target is at %s", got)
	}
	o2 := j.decider()
	l, err := o2.List()
	if err != nil {
		t.Fatal(err)
	}
	line := ""
	for _, r := range l.Recent {
		if r.ID == forged {
			line = r.Line
		}
	}
	if !strings.Contains(line, "refused") {
		t.Errorf("the listing's line for the forged approval is %q", line)
	}
	if len(j.noticed()) == 0 {
		t.Errorf("no notice was raised for the disagreement")
	}
	// Others go on: the third card is approved and applied.
	if out, err := j.approve(o2, other); err != nil || out.State != record.StateApplied {
		t.Fatalf("the other card: %+v, %v", out, err)
	}
	mergeOther := mergeOf(j.pendingOf(other))
	if got := j.target(); got != mergeOther {
		t.Errorf("the target is at %s, want the other card's merge %s", got, mergeOther)
	}
	if got := j.state0(forged); got != record.StateApproved {
		t.Errorf("the forged approval changed to %s", got)
	}
	// The alert is not repeated by another restart.
	j.restartDaemon()
	es := j.journalEntries()
	alerts := 0
	for _, e := range es {
		if e.Kind == ownerjournal.KindAlert && e.PendingID == forged {
			alerts++
		}
	}
	if alerts != 1 {
		t.Errorf("%d alerts for the forged approval, want 1", alerts)
	}
}

// forgedApproval makes an approval that no console sent, with every field the
// door checks.
func forgedApproval(t *testing.T, j *joined, p record.Pending) door.Record {
	t.Helper()
	d := record.Decision{
		Schema: record.SchemaDecision, ID: p.ID, Planner: p.Planner, Decision: record.DecisionApprove,
		BindingSHA256: p.BindingSHA256, Project: p.Binding.Project, Subject: p.Binding.Subject, PlanToken: p.Binding.PlanToken,
		SignedOff: []record.SignOff{}, Uses: 1,
		DecidedAt: record.FormatTime(j.clock.Now()), ExpiresAt: record.FormatTime(j.clock.Now().Add(30 * time.Minute)),
		Confirmed: record.ConfirmedCode,
		Console:   record.Console{PID: 1, TTY: "/dev/pts/9", Client: "forged", Decider: true, Surface: "console", Session: "1111111111111111"},
	}
	d.Subject.Holder = nil
	for _, f := range p.Binding.Summary.Flagged {
		d.SignedOff = append(d.SignedOff, record.SignOff{Kind: f.Kind, Path: f.Path})
	}
	b, err := record.EncodeDecision(d)
	if err != nil {
		t.Fatal(err)
	}
	return door.RecordOf(b)
}

// attrLines are the attributes of OC27: every one of them would change what a
// diff, a merge or a summary shows if the host read attributes from the
// tree it is judging.
const attrLines = "* -diff\n* binary\n* merge=union\n* merge=binary\n"

// binary5M is a 5 MiB blob with NULs.
func binary5M() string {
	b := make([]byte, 5<<20)
	for i := range b {
		b[i] = byte(i % 251)
	}
	return string(b)
}

// attrWorld is the history of one world of OC27.
type attrWorld struct {
	name string
	// upstream puts the attributes file in the root, so that the target has it;
	// added makes the feature add it.
	upstream, added bool
	// conflict makes both sides edit the same line of doc.txt.
	conflict bool
}

func (w attrWorld) scene(l *lab) scene {
	l.put("README.md", "readme\n")
	l.put("src/main.go", "package main\n")
	l.put("doc.txt", "l1\nl2\nl3\n")
	if w.upstream {
		l.put(".gitattributes", attrLines)
	}
	s := scene{root: l.commit("root\n")}
	l.put("target-side.txt", "target\n")
	if w.conflict {
		l.put("doc.txt", "l1\ntarget\nl3\n")
	}
	s.target = l.commit("target moves\n", s.root)
	l.load(s.root)
	l.put("src/main.go", "package main\n\nfunc main() {}\n")
	l.put("new.txt", "new\n")
	l.put("big.bin", binary5M())
	if w.conflict {
		l.put("doc.txt", "l1\nfeature\nl3\n")
	}
	if w.added {
		l.put(".gitattributes", attrLines)
	}
	s.feature = l.commit("feature\n", s.root)
	l.setRef(targetBranch, s.target)
	l.setRef(featureRef, s.feature)
	return s
}

// patchOf reads the whole patch of a card through the client and returns its
// lines with the pieces of a long line joined.
func patchOf(t *testing.T, o interface {
	Page(id string, kind card.PageKind, n int) (card.Page, error)
}, id string) []string {
	t.Helper()
	var lines []string
	cont := false
	for n := 0; ; n++ {
		pg, err := o.Page(id, card.PagePatch, n)
		if err != nil {
			t.Fatalf("patch page %d: %v", n, err)
		}
		for i, l := range pg.Patch {
			if cont && len(lines) > 0 {
				lines[len(lines)-1] += l
			} else {
				lines = append(lines, l)
			}
			cont = pg.Cont[i]
		}
		if n+1 >= pg.Pages {
			return lines
		}
	}
}

// withoutAttrs drops the sections of the attributes file from a patch, and
// the "index" lines, whose abbreviation does not matter here.
func withoutAttrs(lines []string) []string {
	var out []string
	skip := false
	for _, l := range lines {
		if strings.HasPrefix(l, "diff --git ") {
			skip = strings.HasSuffix(l, " b/.gitattributes")
		}
		if !skip && !strings.HasPrefix(l, "index ") {
			out = append(out, l)
		}
	}
	return out
}

// OC27: a .gitattributes with "* -diff", "* binary", "* merge=union" and
// "* merge=binary", once already on the target and once newly added by the
// feature, and a 5 MiB binary file. The summary, the patch and the merge are
// the same as for the same trees without the file; a change that would merge
// without conflict under the union driver is a merge-conflict, as it is
// without the file; a newly added attributes file is flagged; and the binary
// file is listed with its size and counted in the new bytes.
func TestOwnerChannelOC27AttributesOfTheTreeChangeNothing(t *testing.T) {
	type view struct {
		files, added, removed, newBytes int64
		paths                           []string
		binary                          []string
		flagged                         []string
		patch                           []string
		tree                            string
		bigBytes                        int64
	}
	read := func(w attrWorld) view {
		j := newJoinedScene(t, w.scene)
		_, cid := j.card2()
		o := j.decider()
		c, err := o.Open(cid)
		if err != nil {
			t.Fatal(err)
		}
		v := view{files: c.Diff.Files, added: c.Diff.Added, removed: c.Diff.Removed, newBytes: c.Diff.NewBytes}
		for _, r := range c.Diff.Rows {
			if lt(r.Path) == ".gitattributes" {
				continue
			}
			v.paths = append(v.paths, fmt.Sprintf("%s %s %s>%s +%d -%d %d", lt(r.Path), r.Status, r.OldMode, r.NewMode, r.Added, r.Removed, r.NewBytes))
			if lt(r.Path) == "big.bin" {
				v.bigBytes = r.NewBytes
			}
		}
		for _, l := range c.Lists {
			if l.Kind == card.ListBinary {
				for _, p := range l.Paths {
					v.binary = append(v.binary, lt(p))
				}
			}
		}
		for _, f := range c.Flagged {
			v.flagged = append(v.flagged, f.Kind+":"+lt(f.Path))
		}
		v.patch = withoutAttrs(patchOf(t, o, cid))
		merge := mergeOf(j.pendingOf(cid))
		var tree []string
		for _, l := range strings.Split(j.lab.git("", "ls-tree", "-r", merge), "\n") {
			if !strings.HasSuffix(l, "\t.gitattributes") {
				tree = append(tree, l)
			}
		}
		v.tree = strings.Join(tree, "\n")
		return v
	}
	control := read(attrWorld{name: "none"})
	if control.bigBytes != 5<<20 || !slices.Contains(control.binary, "big.bin") {
		t.Fatalf("the control does not list the 5 MiB binary with its size: %d bytes, binary list %v", control.bigBytes, control.binary)
	}
	if control.newBytes < 5<<20 {
		t.Errorf("the new bytes are %d, less than the binary's 5 MiB", control.newBytes)
	}
	for _, w := range []attrWorld{
		{name: "already on the target", upstream: true},
		{name: "newly added by the feature", added: true},
	} {
		t.Run(w.name, func(t *testing.T) {
			v := read(w)
			// The attributes file is one more path when the feature adds it.
			extra := int64(0)
			if w.added {
				extra = 1
			}
			if v.files != control.files+extra || v.removed != control.removed {
				t.Errorf("files %d (want %d), removed %d (want %d)", v.files, control.files+extra, v.removed, control.removed)
			}
			if w.added {
				if v.added != control.added+4 || v.newBytes != control.newBytes+int64(len(attrLines)) {
					t.Errorf("added %d and new bytes %d, want %d and %d", v.added, v.newBytes, control.added+4, control.newBytes+int64(len(attrLines)))
				}
			} else if v.added != control.added || v.newBytes != control.newBytes {
				t.Errorf("added %d and new bytes %d, want %d and %d", v.added, v.newBytes, control.added, control.newBytes)
			}
			if fmt.Sprint(v.paths) != fmt.Sprint(control.paths) {
				t.Errorf("paths\n got  %v\n want %v", v.paths, control.paths)
			}
			if fmt.Sprint(v.binary) != fmt.Sprint(control.binary) || v.bigBytes != control.bigBytes {
				t.Errorf("the binary list %v with %d bytes, want %v with %d", v.binary, v.bigBytes, control.binary, control.bigBytes)
			}
			if strings.Join(v.patch, "\n") != strings.Join(control.patch, "\n") {
				t.Errorf("the patch differs from the patch without the attributes file")
			}
			if v.tree != control.tree {
				t.Errorf("the merge's tree differs from the one without the attributes file")
			}
			hasAttrFlag := false
			for _, f := range v.flagged {
				hasAttrFlag = hasAttrFlag || strings.HasSuffix(f, ":.gitattributes")
			}
			if w.added && !hasAttrFlag {
				t.Errorf("a newly added attributes file is not flagged: %v", v.flagged)
			}
			if !w.added && fmt.Sprint(v.flagged) != fmt.Sprint(control.flagged) {
				t.Errorf("flags %v, want %v", v.flagged, control.flagged)
			}
		})
	}
	// A merge that the union driver would have made without a conflict.
	for _, w := range []attrWorld{{name: "no attributes", conflict: true}, {name: "on the target", upstream: true, conflict: true}, {name: "added by the feature", added: true, conflict: true}} {
		t.Run("a conflict with "+w.name, func(t *testing.T) {
			j := newJoinedScene(t, w.scene)
			id := j.ask()
			st, ok := j.o.Service.Status(id)
			if !ok || st.State != ownerchannel.RequestRefused || st.Code != record.CodeMergeConflict || st.PendingID != "" {
				t.Fatalf("status %+v, want refused merge-conflict", st)
			}
		})
	}
}

// OC31: a verification that fails, one that the time limit stops and one whose
// output is cut each refuse the request verification-failed with no card, no
// ref moved and no pin, and the lead is told; and a later failed run of the
// suite on a merge that has an approved, unused decision voids that approval,
// evidence-changed. The sandbox itself (a suite that reads the lead's home,
// reaches the network or writes outside its checkout) is not here: the suite
// is a fake, and the real suite's sandbox is for step 2b.
func TestOwnerChannelOC31VerificationOutcomes(t *testing.T) {
	cases := []struct {
		name string
		res  SuiteResult
		word string
	}{
		{"a failed suite", SuiteResult{Exit: 1, Output: []byte("FAIL\n")}, ownerchannel.VerifyFailed},
		{"a stop by the time limit", SuiteResult{Exit: -1, Stopped: "timeout"}, ownerchannel.VerifyStopped},
		{"output that was cut", SuiteResult{Passed: true, OutputCut: true}, ownerchannel.VerifyOutputCut},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			j := newJoined(t, execFeature)
			before := j.lab.refs()
			c.res.Started, c.res.Duration = start, time.Second
			j.suite.set(c.res, nil)
			id := j.ask()
			st, ok := j.o.Service.Status(id)
			if !ok || st.State != ownerchannel.RequestRefused || st.Code != record.CodeVerificationFailed || st.PendingID != "" ||
				!strings.HasSuffix(st.Detail, ": "+c.word) {
				t.Fatalf("status %+v, want refused verification-failed: %s", st, c.word)
			}
			if got := j.lab.refs(); got != before {
				t.Errorf("refs changed:\n%s\nwant\n%s", got, before)
			}
			if ids := j.pendingIDs(j.console()); len(ids) != 0 {
				t.Errorf("the console lists %v", ids)
			}
			if rp := j.readReply(id); rp.Code != string(record.CodeVerificationFailed) || rp.State != "" || !strings.Contains(rp.Line, c.word) {
				t.Errorf("the lead's reply: %+v", rp)
			}
		})
	}
	t.Run("a later failed run voids an approval that was not used", func(t *testing.T) {
		ctx := context.Background()
		j := newJoined(t, execFeature)
		reqID, cid := j.card2()
		merge := mergeOf(j.pendingOf(cid))
		o := j.decider()
		j.stopAt("delivered")
		if out, err := j.approve(o, cid); err == nil {
			t.Fatalf("the stop was not reached: %+v", out)
		}
		if got := j.state0(cid); got != record.StateApproved {
			t.Fatalf("the door holds %s, want approved and not used", got)
		}
		j.suite.set(SuiteResult{Exit: 1, Output: []byte("FAIL\n"), Started: start, Duration: time.Second}, nil)
		v, err := j.o.Host.Verify(ctx, ownerchannel.VerifyInput{Project: "project-a", RequestID: "rq-later-01", Merge: merge, Base: j.scene.target})
		if err != nil || v.Clean {
			t.Fatalf("the later run: %+v, %v", v, err)
		}
		j.clearStop()
		j.restartDaemon()
		shown, err := j.stand.Show(ctx, cid)
		if err != nil || shown.Entry.State != record.StateVoid || shown.Entry.ReasonCode != record.CodeEvidenceChanged {
			t.Fatalf("the door holds %+v (%v), want void for evidence-changed", shown.Entry, err)
		}
		if got := j.target(); got != j.scene.target {
			t.Errorf("the target moved to %s", got)
		}
		if reply := j.reply(reqID); !strings.Contains(reply, `"state":"void"`) {
			t.Errorf("the lead's reply: %s", reply)
		}
	})
}
