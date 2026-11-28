package thread

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/genesis/brief"
)

// A full interview of the web app path: start, six rounds, the summary, the
// finish with its digest, and a frozen thread.
func TestAFullInterviewOfTheWebAppPath(t *testing.T) {
	w := newWorld(t)
	first := w.ok(w.s.Start(ctx))
	if first.State != ownerwire.GenesisStateQuestion || first.Round != 1 || first.Of != 6 || !reflect.DeepEqual(nodesOf(first), []string{"kind"}) {
		t.Fatalf("the first round: %+v", first)
	}
	if first.Thread != "0000000000000001" || first.CatalogDigest != v1(t).Digest() || first.Progress.Filled != 0 || first.Progress.Total != 1 {
		t.Fatalf("the first round: %+v", first)
	}
	if again := w.ok(w.s.Round(ctx, first.Thread)); !reflect.DeepEqual(again, first) {
		t.Fatalf("round after start: %+v, want %+v", again, first)
	}
	second := w.ok(w.s.Answer(ctx, first.Thread, 1, webApp[0]))
	if got := nodesOf(second); !reflect.DeepEqual(got, []string{"software.sub_kind", "goal", "audience"}) {
		t.Fatalf("the second round asks %v", got)
	}
	sum := w.interview(first.Thread, 1)
	if sum.State != ownerwire.GenesisStateSummary || sum.Round != 6 || sum.Groups == nil || sum.Digest != "" || sum.Reset != 0 {
		t.Fatalf("the summary: %+v", sum)
	}
	if sum.Progress.Filled != sum.Progress.Total || sum.Progress.Total != 14 {
		t.Fatalf("the progress: %+v", sum.Progress)
	}
	// "you decide" leaves the field to its default, the skip likewise.
	var got []string
	for _, g := range [][]ownerwire.GenesisRow{sum.Groups.YouChose, sum.Groups.DecidedForYou, sum.Groups.Defaults} {
		got = append(got, "")
		for _, r := range g {
			got[len(got)-1] += r.Field + ";"
		}
	}
	if !strings.Contains(got[2], "schedule;") || !strings.Contains(got[1]+got[2], "software.data;") || !strings.Contains(got[0], "software.stack;") {
		t.Fatalf("the groups: %q", got)
	}
	// The summary and the round of a thread whose walk is done are the same.
	if r := w.ok(w.s.Round(ctx, first.Thread)); !reflect.DeepEqual(r, sum) {
		t.Fatalf("round: %+v, want %+v", r, sum)
	}
	if s := w.ok(w.s.Summary(ctx, first.Thread)); !reflect.DeepEqual(s, sum) {
		t.Fatalf("summary: %+v, want %+v", s, sum)
	}
	fin := w.ok(w.s.Finish(ctx, first.Thread))
	if fin.State != ownerwire.GenesisStateFinished || fin.Digest == "" || fin.Groups == nil || fin.Round != 6 {
		t.Fatalf("finish: %+v", fin)
	}
	// The digest is the brief's, and the file holds the same brief.
	r := w.s.threads[first.Thread]
	if d, err := brief.Digest(r.brief); err != nil || d != fin.Digest || r.frozen != d {
		t.Fatalf("the digest %s of the brief %s: %v", fin.Digest, d, err)
	}
	if a, err := w.s.Finish(ctx, first.Thread); err != nil || !reflect.DeepEqual(a, fin) {
		t.Fatalf("finishing again: %+v, %v", a, err)
	}
	// Frozen: no answer, no change, no discard, and the file stays as it
	// was finished.
	frozen := w.bytesOf(first.Thread)
	_, err := w.s.Answer(ctx, first.Thread, 1, webApp[0])
	refused(t, err, ownerwire.CodeGenesisDone)
	_, err = w.s.Change(ctx, first.Thread, "goal", ownerwire.GenesisPick{Kind: ownerwire.GenesisKindChoice, IDs: []string{"share"}})
	refused(t, err, ownerwire.CodeGenesisDone)
	_, err = w.s.Discard(ctx, first.Thread)
	refused(t, err, ownerwire.CodeGenesisDone)
	_, err = w.s.Genesis(ctx, ownerwire.SeatGenesisParams{Verb: ownerwire.GenesisVerbDiscard, Thread: first.Thread})
	refused(t, err, ownerwire.CodeGenesisDone)
	if !bytes.Equal(w.bytesOf(first.Thread), frozen) {
		t.Fatal("a refused call changed the finished thread's file")
	}
	if r := w.ok(w.s.Round(ctx, first.Thread)); !reflect.DeepEqual(r, fin) {
		t.Fatalf("round of a finished thread: %+v", r)
	}
	// What the log says holds no answer; the plan was stored at finish.
	if w.log.count("a thread was started") != 1 || w.log.count("a thread was finished") != 1 || w.log.count("a plan was drawn") != 1 || len(w.log.all()) != 3 {
		t.Fatalf("log %q", w.log.all())
	}
}

// A restart serves the same state, between two rounds as well as at the end.
func TestARestartServesTheSameThread(t *testing.T) {
	w := newWorld(t)
	st := w.ok(w.s.Start(ctx))
	id := st.Thread
	mid := w.ok(w.s.Answer(ctx, id, 1, webApp[0]))
	other := w.ok(w.s.Start(ctx))
	w.open()
	if got := w.ok(w.s.Round(ctx, id)); !reflect.DeepEqual(got, mid) {
		t.Fatalf("after the restart: %+v, want %+v", got, mid)
	}
	if got := w.ok(w.s.Round(ctx, other.Thread)); !reflect.DeepEqual(got, other) {
		t.Fatalf("after the restart, the other thread: %+v, want %+v", got, other)
	}
	sum := w.interview(id, 1)
	fin := w.ok(w.s.Finish(ctx, id))
	w.open()
	if got := w.ok(w.s.Round(ctx, id)); !reflect.DeepEqual(got, fin) {
		t.Fatalf("a finished thread after the restart: %+v, want %+v", got, fin)
	}
	if fin.Groups == nil || !reflect.DeepEqual(fin.Groups, sum.Groups) {
		t.Fatalf("the groups changed at finish")
	}
	if w.log.count("not served") != 0 {
		t.Fatalf("log %q", w.log.all())
	}
}

// A third open thread is genesis-busy; a thread that is finished or discarded
// makes room; and the refusal changes nothing.
func TestAThirdOpenThreadIsBusy(t *testing.T) {
	w := newWorld(t)
	a := w.ok(w.s.Start(ctx))
	b := w.ok(w.s.Start(ctx))
	before := w.files()
	_, err := w.s.Start(ctx)
	refused(t, err, ownerwire.CodeGenesisBusy)
	if !reflect.DeepEqual(w.files(), before) {
		t.Fatalf("a refused start wrote a file: %v", w.files())
	}
	w.ok(w.s.Discard(ctx, a.Thread))
	c := w.ok(w.s.Start(ctx))
	_, err = w.s.Start(ctx)
	refused(t, err, ownerwire.CodeGenesisBusy)
	w.ok(w.s.Finish(ctx, b.Thread))
	w.ok(w.s.Start(ctx))
	if c.Thread == a.Thread || c.Thread == b.Thread {
		t.Fatalf("an id was used twice: %s", c.Thread)
	}
	// Busy survives a restart.
	w.open()
	_, err = w.s.Start(ctx)
	refused(t, err, ownerwire.CodeGenesisBusy)
}

// An answer is refused, and nothing changes, when its round is one the thread
// has not reached, a node is not of the round, a form is not the question's,
// or one answer of several fails. A round already taken is stale (see
// TestAnAnswerSentTwiceIsAStaleRound).
func TestAnAnswerThatFailsChangesNothing(t *testing.T) {
	w := newWorld(t)
	id := w.ok(w.s.Start(ctx)).Thread
	w.ok(w.s.Answer(ctx, id, 1, webApp[0]))
	frozen := w.bytesOf(id)
	for name, c := range map[string]struct {
		round   int
		answers []ownerwire.GenesisAnswer
	}{
		"a later round":           {3, webApp[1]},
		"round zero":              {0, webApp[1]},
		"a node of another round": {2, []ownerwire.GenesisAnswer{choice("software.stack", "go")}},
		"an unknown node":         {2, []ownerwire.GenesisAnswer{choice("no.such.node", "go")}},
		"an option not offered":   {2, []ownerwire.GenesisAnswer{choice("goal", "nothing")}},
		"other text not taken":    {2, []ownerwire.GenesisAnswer{other("goal", "to see")}},
		"choices for a single":    {2, []ownerwire.GenesisAnswer{choices("goal", "try", "share")}},
		"a skip without default":  {2, []ownerwire.GenesisAnswer{skip("goal")}},
		"a node twice":            {2, []ownerwire.GenesisAnswer{choice("goal", "try"), choice("goal", "share")}},
		"the second answer bad":   {2, []ownerwire.GenesisAnswer{choice("goal", "try"), choice("audience", "everyone")}},
		"none":                    {2, nil},
		"four":                    {2, []ownerwire.GenesisAnswer{choice("goal", "try"), choice("audience", "me"), choice("software.sub_kind", "web_app"), choice("kind", "software")}},
	} {
		_, err := w.s.Answer(ctx, id, c.round, c.answers)
		if code, ok := RefusalCode(err); !ok || code != ownerwire.CodeGenesisBadAnswer {
			t.Errorf("%s: %v", name, err)
		}
		if !bytes.Equal(w.bytesOf(id), frozen) {
			t.Errorf("%s: the thread's file changed", name)
		}
	}
	// And the thread still takes its round.
	w.ok(w.s.Answer(ctx, id, 2, webApp[1]))
	// A round past the most a walk has is a bad answer; a thread whose
	// summary is ready takes no answer to any round of it: that is stale.
	sum := w.interview(id, 2)
	_, err := w.s.Answer(ctx, id, 7, webApp[0])
	refused(t, err, ownerwire.CodeGenesisBadAnswer)
	_, err = w.s.Answer(ctx, id, 6, webApp[5])
	refused(t, err, ownerwire.CodeGenesisStaleRound)
	if sum.State != ownerwire.GenesisStateSummary {
		t.Fatalf("state %s", sum.State)
	}
}

// An answer sent twice (a double tap, a second tab, a post the browser sent
// again) is refused genesis-stale-round the second time, and changes nothing:
// the round was taken. So is an answer to an earlier round, and an answer to
// any round once the walk is done. A stale round is the thread's state, not
// a bad answer.
func TestAnAnswerSentTwiceIsAStaleRound(t *testing.T) {
	w := newWorld(t)
	id := w.ok(w.s.Start(ctx)).Thread
	first := w.ok(w.s.Answer(ctx, id, 1, webApp[0]))
	frozen := w.bytesOf(id)
	_, err := w.s.Answer(ctx, id, 1, webApp[0])
	refused(t, err, ownerwire.CodeGenesisStaleRound)
	if !bytes.Equal(w.bytesOf(id), frozen) {
		t.Fatal("the stale answer changed the thread's file")
	}
	if now := w.ok(w.s.Round(ctx, id)); now.Round != first.Round || now.Round != 2 {
		t.Fatalf("the thread is at round %d after the stale answer, want 2", now.Round)
	}
	w.ok(w.s.Answer(ctx, id, 2, webApp[1]))
	for _, r := range []int{1, 2} {
		_, err := w.s.Answer(ctx, id, r, webApp[r-1])
		refused(t, err, ownerwire.CodeGenesisStaleRound)
	}
	// Once the walk is done, the last round sent again is stale too.
	sum := w.interview(id, 2)
	if sum.State != ownerwire.GenesisStateSummary {
		t.Fatalf("state %s", sum.State)
	}
	frozen = w.bytesOf(id)
	_, err = w.s.Answer(ctx, id, len(webApp), webApp[len(webApp)-1])
	refused(t, err, ownerwire.CodeGenesisStaleRound)
	if !bytes.Equal(w.bytesOf(id), frozen) {
		t.Fatal("a stale answer after the walk changed the thread's file")
	}
	// Through the wire's own verb the same.
	_, err = w.s.Genesis(ctx, ownerwire.SeatGenesisParams{Verb: ownerwire.GenesisVerbAnswer, Thread: id, Round: 1, Answers: webApp[0]})
	refused(t, err, ownerwire.CodeGenesisStaleRound)
}

// Text that looks like a secret is refused, by the word of the wire, however
// it is given, and the log counts it and holds none of it.
func TestSecretShapedTextIsRefused(t *testing.T) {
	w := newWorld(t)
	id := w.ok(w.s.Start(ctx)).Thread
	secret := "pass" + "word=hunter" + "2"
	frozen := w.bytesOf(id)
	for i := 1; i <= 2; i++ {
		_, err := w.s.Answer(ctx, id, 1, []ownerwire.GenesisAnswer{other("kind", secret)})
		refused(t, err, ownerwire.CodeGenesisSecretShaped)
		if !bytes.Equal(w.bytesOf(id), frozen) {
			t.Fatal("the thread's file changed")
		}
	}
	w.ok(w.s.Answer(ctx, id, 1, webApp[0]))
	w.ok(w.s.Answer(ctx, id, 2, webApp[1]))
	// An answer of several, the secret in the last: none is kept.
	frozen = w.bytesOf(id)
	_, err := w.s.Answer(ctx, id, 3, []ownerwire.GenesisAnswer{choice("software.auth", "none"), other("software.stack", "a "+"sk"+"-"+strings.Repeat("abcde", 4))})
	refused(t, err, ownerwire.CodeGenesisSecretShaped)
	if !bytes.Equal(w.bytesOf(id), frozen) {
		t.Fatal("the thread's file changed")
	}
	// A change takes the same check.
	w.interview(id, 2)
	_, err = w.s.Change(ctx, id, "software.stack", ownerwire.GenesisPick{Kind: ownerwire.GenesisKindOther, Text: secret})
	refused(t, err, ownerwire.CodeGenesisSecretShaped)
	if n := w.log.count("refused as secret-shaped"); n != 4 {
		t.Fatalf("%d lines about secrets: %q", n, w.log.all())
	}
	if !strings.Contains(strings.Join(w.log.all(), "\n"), "(4 since the store was opened)") {
		t.Fatalf("log %q", w.log.all())
	}
	for _, l := range w.log.all() {
		if strings.Contains(l, "hunter") || strings.Contains(l, "abcde") {
			t.Fatalf("the log holds the text: %q", l)
		}
	}
	if bytes.Contains(w.bytesOf(id), []byte("hunter")) {
		t.Fatal("the file holds the text")
	}
	// Plain text of the same question is taken, as untrusted data.
	res := w.ok(w.s.Change(ctx, id, "software.stack", ownerwire.GenesisPick{Kind: ownerwire.GenesisKindOther, Text: "rust, with a small web server"}))
	var row *ownerwire.GenesisRow
	for i, r := range res.Groups.YouChose {
		if r.Field == "software.stack" {
			row = &res.Groups.YouChose[i]
		}
	}
	if row == nil || !row.Untrusted || row.Value != "rust, with a small web server" {
		t.Fatalf("the row: %+v", row)
	}
}

// A change of an earlier answer clears what depended on it and says how many;
// the cleared values stay in the brief's history.
func TestAChangeResetsWhatDependedOnIt(t *testing.T) {
	w := newWorld(t)
	id := w.ok(w.s.Start(ctx)).Thread
	w.ok(w.s.Answer(ctx, id, 1, webApp[0]))
	w.ok(w.s.Answer(ctx, id, 2, webApp[1]))
	w.ok(w.s.Answer(ctx, id, 3, webApp[2]))
	// The same answer again changes nothing.
	same := w.ok(w.s.Change(ctx, id, "software.stack", ownerwire.GenesisPick{Kind: ownerwire.GenesisKindChoice, IDs: []string{"go"}}))
	if same.Reset != 0 {
		t.Fatalf("a change to the same answer reset %d", same.Reset)
	}
	// A sub kind that clears the four answers of software.
	res := w.ok(w.s.Change(ctx, id, "software.sub_kind", ownerwire.GenesisPick{Kind: ownerwire.GenesisKindChoice, IDs: []string{"web_tool"}}))
	if res.Reset != 3 || res.State != ownerwire.GenesisStateQuestion {
		t.Fatalf("the change: reset %d state %s", res.Reset, res.State)
	}
	if got := nodesOf(res); !reflect.DeepEqual(got, []string{"software.stack", "software.data", "software.auth"}) {
		t.Fatalf("after the change the round asks %v", got)
	}
	r := w.s.threads[id]
	invalidated := 0
	for _, h := range r.brief.History {
		if h.Reason == brief.ReasonInvalidated && h.Cause == "software.sub_kind" {
			invalidated++
		}
	}
	if invalidated != 3 {
		t.Fatalf("the history holds %d invalidated values: %+v", invalidated, r.brief.History)
	}
	// A change of the kind itself clears the whole branch.
	res = w.ok(w.s.Change(ctx, id, "kind", ownerwire.GenesisPick{Kind: ownerwire.GenesisKindChoice, IDs: []string{"publishing"}}))
	if res.Reset == 0 {
		t.Fatalf("a change of the kind reset nothing")
	}
	// The result of a change and the read after it say the same but the reset.
	again := w.ok(w.s.Round(ctx, id))
	res.Reset = 0
	if !reflect.DeepEqual(res, again) {
		t.Fatalf("round after a change: %+v, want %+v", again, res)
	}
	for name, c := range map[string]struct {
		node string
		pick ownerwire.GenesisPick
	}{
		"a node without an answer": {"deliverables", ownerwire.GenesisPick{Kind: ownerwire.GenesisKindChoices, IDs: []string{"plan"}}},
		"an unknown node":          {"no.such", ownerwire.GenesisPick{Kind: ownerwire.GenesisKindSkip}},
		"an option not offered":    {"goal", ownerwire.GenesisPick{Kind: ownerwire.GenesisKindChoice, IDs: []string{"nothing"}}},
		"a node of another branch": {"software.stack", ownerwire.GenesisPick{Kind: ownerwire.GenesisKindChoice, IDs: []string{"go"}}},
		"an unknown kind":          {"goal", ownerwire.GenesisPick{Kind: "other-kind"}},
	} {
		_, err := w.s.Change(ctx, id, c.node, c.pick)
		if code, ok := RefusalCode(err); !ok || code != ownerwire.CodeGenesisBadAnswer {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// A thread untouched for 14 days is discarded on the next access, and at
// open; it keeps no answer, and every call to it is unknown.
func TestAnUntouchedThreadExpires(t *testing.T) {
	w := newWorld(t)
	a := w.ok(w.s.Start(ctx)).Thread
	b := w.ok(w.s.Start(ctx)).Thread
	w.ok(w.s.Answer(ctx, a, 1, webApp[0]))
	w.clock.advance(Expiry - time.Second)
	w.ok(w.s.Round(ctx, a))
	// Reading does not keep a thread alive; one second more and it is gone.
	w.clock.advance(time.Second)
	_, err := w.s.Round(ctx, a)
	refused(t, err, ownerwire.CodeGenesisUnknownThread)
	if w.log.count("a thread expired") != 1 {
		t.Fatalf("log %q", w.log.all())
	}
	if d := w.s.threads[a]; d.state != StateDiscarded || len(d.brief.Fields) != 0 || len(d.brief.History) != 0 {
		t.Fatalf("the expired thread: %+v", d)
	}
	if bytes.Contains(w.bytesOf(a), []byte("software")) {
		t.Fatalf("the file of an expired thread holds an answer")
	}
	for _, call := range []func() error{
		func() error { _, err := w.s.Answer(ctx, a, 1, webApp[0]); return err },
		func() error { _, err := w.s.Summary(ctx, a); return err },
		func() error { _, err := w.s.Finish(ctx, a); return err },
		func() error { _, err := w.s.Discard(ctx, a); return err },
		func() error {
			_, err := w.s.Change(ctx, a, "kind", ownerwire.GenesisPick{Kind: ownerwire.GenesisKindSkip})
			return err
		},
	} {
		refused(t, call(), ownerwire.CodeGenesisUnknownThread)
	}
	// A start makes room by expiring: b expires too, so two starts pass.
	w.ok(w.s.Start(ctx))
	w.ok(w.s.Start(ctx))
	_, err = w.s.Round(ctx, b)
	refused(t, err, ownerwire.CodeGenesisUnknownThread)
}

func TestExpiryAtOpen(t *testing.T) {
	w := newWorld(t)
	a := w.ok(w.s.Start(ctx)).Thread
	w.ok(w.s.Answer(ctx, a, 1, webApp[0]))
	fin := w.ok(w.s.Start(ctx)).Thread
	w.ok(w.s.Finish(ctx, fin))
	w.clock.advance(Expiry)
	w.open()
	if w.log.count("a thread expired") != 1 {
		t.Fatalf("log %q", w.log.all())
	}
	_, err := w.s.Round(ctx, a)
	refused(t, err, ownerwire.CodeGenesisUnknownThread)
	// A finished thread does not expire.
	if r := w.ok(w.s.Round(ctx, fin)); r.State != ownerwire.GenesisStateFinished {
		t.Fatalf("%+v", r)
	}
	w.ok(w.s.Start(ctx))
	w.ok(w.s.Start(ctx))
}

// A discard drops the thread and its answers.
func TestDiscard(t *testing.T) {
	w := newWorld(t)
	id := w.ok(w.s.Start(ctx)).Thread
	w.ok(w.s.Answer(ctx, id, 1, webApp[0]))
	res := w.ok(w.s.Discard(ctx, id))
	if res.State != ownerwire.GenesisStateDiscarded || res.Thread != id || res.Round != 0 || res.Progress != (ownerwire.GenesisProgress{}) || res.Of != 6 {
		t.Fatalf("%+v", res)
	}
	_, err := w.s.Round(ctx, id)
	refused(t, err, ownerwire.CodeGenesisUnknownThread)
	if bytes.Contains(w.bytesOf(id), []byte("software")) || w.log.count("a thread was discarded") != 1 {
		t.Fatalf("the file holds an answer, or the log is %q", w.log.all())
	}
	w.open()
	_, err = w.s.Round(ctx, id)
	refused(t, err, ownerwire.CodeGenesisUnknownThread)
	// A thread that was never started, and an id of another form.
	_, err = w.s.Round(ctx, "ffffffffffffffff")
	refused(t, err, ownerwire.CodeGenesisUnknownThread)
	_, err = w.s.Round(ctx, "../../etc/passwd")
	refused(t, err, ownerwire.CodeGenesisUnknownThread)
	_, err = w.s.Round(ctx, "")
	refused(t, err, ownerwire.CodeGenesisUnknownThread)
}

// At most eight files are kept: the oldest finished or discarded go first, an
// open thread never.
func TestOnlyEightFilesAreKept(t *testing.T) {
	w := newWorld(t)
	keep := w.ok(w.s.Start(ctx)).Thread
	var ids []string
	for i := 0; i < 12; i++ {
		w.clock.advance(time.Minute)
		id := w.ok(w.s.Start(ctx)).Thread
		ids = append(ids, id)
		if i%2 == 0 {
			w.ok(w.s.Finish(ctx, id))
		} else {
			w.ok(w.s.Discard(ctx, id))
		}
		if n := len(w.files()); n > MaxFiles {
			t.Fatalf("%d files after %d threads", n, i+2)
		}
	}
	if got := len(w.files()); got != MaxFiles {
		t.Fatalf("%d files, want %d: %v", got, MaxFiles, w.files())
	}
	want := map[string]bool{fileOf(keep): true}
	for _, id := range ids[len(ids)-7:] {
		want[fileOf(id)] = true
	}
	for _, f := range w.files() {
		if !want[f] {
			t.Errorf("the file %s was kept; want the open thread and the seven newest", f)
		}
	}
	w.ok(w.s.Round(ctx, keep))
	// The same at open.
	w.open()
	if got := len(w.files()); got != MaxFiles {
		t.Fatalf("%d files after a restart", got)
	}
}

// The files are written whole: mode 0600, the folder 0700, no temporary file
// left, and a temporary file of a crashed write is removed at open.
func TestTheFilesAndTheFolder(t *testing.T) {
	w := newWorld(t)
	id := w.ok(w.s.Start(ctx)).Thread
	w.ok(w.s.Answer(ctx, id, 1, webApp[0]))
	fi, err := os.Stat(w.dir)
	if err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("the folder: %v %v", fi, err)
	}
	fi, err = os.Stat(filepath.Join(w.dir, fileOf(id)))
	if err != nil || fi.Mode().Perm() != 0o600 || !fi.Mode().IsRegular() {
		t.Fatalf("the file: %v %v", fi, err)
	}
	if !reflect.DeepEqual(w.files(), []string{fileOf(id)}) {
		t.Fatalf("files %v", w.files())
	}
	stale := filepath.Join(w.dir, tmpOf("00000000000000aa"))
	if err := os.WriteFile(stale, []byte("half"), 0o600); err != nil {
		t.Fatal(err)
	}
	w.open()
	if !reflect.DeepEqual(w.files(), []string{fileOf(id)}) {
		t.Fatalf("files %v", w.files())
	}
}

// A write that fails leaves the old thread, and the call fails with an error
// that is not a refusal.
func TestAFailedWriteChangesNothing(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("a folder is writable for root")
	}
	w := newWorld(t)
	id := w.ok(w.s.Start(ctx)).Thread
	before := w.ok(w.s.Round(ctx, id))
	if err := os.Chmod(w.dir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(w.dir, 0o700)
	_, err := w.s.Answer(ctx, id, 1, webApp[0])
	if err == nil {
		t.Fatal("the answer was taken with no write")
	}
	if _, ok := RefusalCode(err); ok || strings.Contains(err.Error(), w.dir) {
		t.Fatalf("error %v", err)
	}
	_, err = w.s.Start(ctx)
	if err == nil {
		t.Fatal("a start with no write")
	}
	os.Chmod(w.dir, 0o700)
	if got := w.ok(w.s.Round(ctx, id)); !reflect.DeepEqual(got, before) {
		t.Fatalf("after the failed write: %+v, want %+v", got, before)
	}
	if len(w.s.threads) != 1 {
		t.Fatalf("the store holds %d threads", len(w.s.threads))
	}
	// And once it can write again, it does.
	w.ok(w.s.Answer(ctx, id, 1, webApp[0]))
}

// The folder is refused when it is a link, another's or open to writing.
func TestTheFolderIsChecked(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(link, Options{}); err == nil || !strings.Contains(err.Error(), "link") {
		t.Fatalf("a link: %v", err)
	}
	for _, mode := range []os.FileMode{0o770, 0o707, 0o722} {
		d := filepath.Join(base, "m"+mode.String())
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(d, mode); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(d, Options{}); err == nil {
			t.Errorf("a folder of mode %v was taken", mode)
		}
	}
	file := filepath.Join(base, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(file, Options{}); err == nil {
		t.Error("a file was taken for the folder")
	}
	if _, err := Open("", Options{}); err == nil {
		t.Error("no folder was taken")
	}
	if _, err := Open(filepath.Join(base, "no", "such", "parent"), Options{}); err == nil {
		t.Error("a folder in a folder that is not there was made")
	}
	// A folder that is there and of 0700 or 0750 is taken, and is not changed.
	ok := filepath.Join(base, "ok")
	if err := os.Mkdir(ok, 0o750); err != nil {
		t.Fatal(err)
	}
	s, err := Open(ok, Options{})
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	// None of the errors holds the path.
	_, err = Open(link, Options{})
	if strings.Contains(err.Error(), base) {
		t.Fatalf("the error holds the path: %v", err)
	}
	// Each error is an OpenError with the reason word of what failed, for
	// the daemon's log line.
	wide := filepath.Join(base, "wide")
	if err := os.Mkdir(wide, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(wide, 0o777); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{link: "folder-link", wide: "folder-mode", file: "folder-not-folder", "": "no-folder",
		filepath.Join(base, "no", "such", "parent"): "folder-make"} {
		_, err := Open(path, Options{})
		var oe *OpenError
		if !errors.As(err, &oe) || oe.Reason != want {
			t.Errorf("%q: %v, want the reason %s", path, err, want)
		}
	}
}

// A store does not follow a link in its folder: a file that is a link is not
// served, whatever it points at.
func TestALinkedThreadFileIsNotServed(t *testing.T) {
	w := newWorld(t)
	id := w.ok(w.s.Start(ctx)).Thread
	w.s.Close()
	outside := filepath.Join(t.TempDir(), "outside.json")
	if err := os.Rename(filepath.Join(w.dir, fileOf(id)), outside); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(w.dir, fileOf(id))); err != nil {
		t.Fatal(err)
	}
	w.s = nil
	w.open()
	_, err := w.s.Round(ctx, id)
	refused(t, err, ownerwire.CodeGenesisUnknownThread)
	if w.log.count("is not served: the file is not a regular file") != 1 {
		t.Fatalf("log %q", w.log.all())
	}
	// A hard link of a thread file elsewhere is no matter, but a file of
	// another mode is not served.
}

// A file that fails its checks is not served, is logged once and is never
// repaired; the others are served.
func TestAFileThatFailsItsChecksIsNotServed(t *testing.T) {
	good := func(w *world) (id string, data []byte) {
		id = w.ok(w.s.Start(ctx)).Thread
		w.ok(w.s.Answer(ctx, id, 1, webApp[0]))
		return id, w.bytesOf(id)
	}
	flip := func(data []byte, old, new string) []byte {
		if !bytes.Contains(data, []byte(old)) {
			t.Fatalf("no %q in %s", old, data)
		}
		return bytes.Replace(data, []byte(old), []byte(new), 1)
	}
	cases := map[string]struct {
		edit func(w *world, id string, data []byte)
		why  string
	}{
		"a changed answer": {func(w *world, id string, data []byte) {
			os.WriteFile(filepath.Join(w.dir, fileOf(id)), flip(data, `"value":"software"`, `"value":"publishing"`), 0o600)
		}, "does not pass its own checks"},
		"a changed time": {func(w *world, id string, data []byte) {
			os.WriteFile(filepath.Join(w.dir, fileOf(id)), flip(data, `"created":"2026-11-23`, `"created":"2026-11-22`), 0o600)
		}, "does not match"},
		"a changed state": {func(w *world, id string, data []byte) {
			os.WriteFile(filepath.Join(w.dir, fileOf(id)), flip(data, `"state":"open"`, `"state":"discarded"`), 0o600)
		}, "does not match"},
		"whitespace": {func(w *world, id string, data []byte) {
			os.WriteFile(filepath.Join(w.dir, fileOf(id)), append([]byte(" "), data...), 0o600)
		}, "canonical"},
		"a trailing newline": {func(w *world, id string, data []byte) {
			os.WriteFile(filepath.Join(w.dir, fileOf(id)), append(data, '\n'), 0o600)
		}, "canonical"},
		"not json": {func(w *world, id string, data []byte) {
			os.WriteFile(filepath.Join(w.dir, fileOf(id)), []byte("not json"), 0o600)
		}, "not strict JSON"},
		"empty": {func(w *world, id string, data []byte) {
			os.WriteFile(filepath.Join(w.dir, fileOf(id)), nil, 0o600)
		}, "not strict JSON"},
		"too large": {func(w *world, id string, data []byte) {
			os.WriteFile(filepath.Join(w.dir, fileOf(id)), bytes.Repeat([]byte(" "), maxFileBytes+1), 0o600)
		}, "larger"},
		"an unknown member": {func(w *world, id string, data []byte) {
			os.WriteFile(filepath.Join(w.dir, fileOf(id)), flip(data, `{"brief"`, `{"agent":"x","brief"`), 0o600)
		}, "thread has a field"},
		"another id": {func(w *world, id string, data []byte) {
			os.WriteFile(filepath.Join(w.dir, fileOf(id)), flip(data, `"id":"`+id+`"`, `"id":"ffffffffffffffff"`), 0o600)
		}, "id is not the file"},
		"mode 0644": {func(w *world, id string, data []byte) { os.Chmod(filepath.Join(w.dir, fileOf(id)), 0o644) }, "open to group or other"},
		"mode 0666": {func(w *world, id string, data []byte) { os.Chmod(filepath.Join(w.dir, fileOf(id)), 0o666) }, "open to group or other"},
		"a folder": {func(w *world, id string, data []byte) {
			p := filepath.Join(w.dir, fileOf(id))
			os.Remove(p)
			os.Mkdir(p, 0o700)
		}, "not a regular file"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			id, data := good(w)
			otherID, _ := good(w)
			c.edit(w, id, data)
			edited, _ := os.ReadFile(filepath.Join(w.dir, fileOf(id)))
			w.open()
			if n := w.log.count(fileOf(id) + " is not served"); n != 1 || !strings.Contains(strings.Join(w.log.all(), "\n"), c.why) {
				t.Fatalf("log %q, want one line with %q", w.log.all(), c.why)
			}
			_, err := w.s.Round(ctx, id)
			refused(t, err, ownerwire.CodeGenesisUnknownThread)
			if _, err := w.s.Round(ctx, otherID); err != nil {
				t.Fatalf("the other thread: %v", err)
			}
			// Never repaired and never overwritten: another thread gets another id,
			// and a restart says it once more.
			if after, _ := os.ReadFile(filepath.Join(w.dir, fileOf(id))); !bytes.Equal(after, edited) {
				t.Fatal("the file was changed")
			}
			for i := 0; i < 20; i++ {
				w.s.Discard(ctx, otherID)
				if r, err := w.s.Start(ctx); err == nil {
					if r.Thread == id {
						t.Fatal("the id of a file that is not served was used again")
					}
					w.s.Discard(ctx, r.Thread)
				}
			}
			if w.log.count(fileOf(id)+" is not served") != 1 {
				t.Fatalf("log %q", w.log.all())
			}
			for _, l := range w.log.all() {
				if strings.Contains(l, "software") || strings.Contains(l, w.dir) {
					t.Fatalf("the log holds content or a path: %q", l)
				}
			}
		})
	}
}

// A file of another catalog than the store's is not served.
func TestAThreadOfAnotherCatalogIsNotServed(t *testing.T) {
	w := newWorld(t)
	id := w.ok(w.s.Start(ctx)).Thread
	r := w.s.threads[id]
	r.catalog = strings.Repeat("ab", 32)
	r.brief.Catalog = r.catalog
	data, err := encodeRecord(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w.dir, fileOf(id)), data, 0o600); err != nil {
		t.Fatal(err)
	}
	w.open()
	_, err = w.s.Round(ctx, id)
	refused(t, err, ownerwire.CodeGenesisUnknownThread)
	if w.log.count("another catalog") != 1 {
		t.Fatalf("log %q", w.log.all())
	}
}

// Anything in the folder that is not a thread file is left alone and does not
// count; a folder with too many files is not a store.
func TestOtherFilesInTheFolder(t *testing.T) {
	w := newWorld(t)
	w.ok(w.s.Start(ctx))
	w.s.Close()
	for _, n := range []string{"notes.txt", "0123456789abcdeg.json", "0123456789abcde.json", ".hidden"} {
		if err := os.WriteFile(filepath.Join(w.dir, n), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	w.s = nil
	w.open()
	if len(w.files()) != 5 || len(w.s.threads) != 1 || w.log.count("not served") != 0 {
		t.Fatalf("files %v log %q", w.files(), w.log.all())
	}
	w.s.Close()
	for i := 0; i < maxEntries; i++ {
		os.WriteFile(filepath.Join(w.dir, strings.Repeat("x", 3)+string(rune('a'+i%26))+string(rune('a'+i/26))), nil, 0o600)
	}
	if _, err := Open(w.dir, Options{}); err == nil {
		t.Fatal("a folder with more than the cap of files was opened")
	}
}

// The ids are 16 lower-case hexadecimal digits from crypto/rand by default, and
// differ.
func TestTheIDsAreRandom(t *testing.T) {
	dir := filepath.Join(t.TempDir(), DirName)
	s, err := Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seen := map[string]bool{}
	for i := 0; i < 6; i++ {
		r, err := s.Start(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !isHex(r.Thread, 16) || seen[r.Thread] {
			t.Fatalf("id %q", r.Thread)
		}
		seen[r.Thread] = true
		s.Discard(ctx, r.Thread)
	}
}

// A call after Close is refused; a cancelled context is refused before the
// store is touched.
func TestClosedAndCancelled(t *testing.T) {
	w := newWorld(t)
	id := w.ok(w.s.Start(ctx)).Thread
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := w.s.Round(cctx, id); !errors.Is(err, context.Canceled) {
		t.Fatalf("%v", err)
	}
	if err := w.s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := w.s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.Round(ctx, id); !errors.Is(err, ErrClosed) {
		t.Fatalf("%v", err)
	}
	if _, ok := RefusalCode(ErrClosed); ok {
		t.Fatal("closed is a refusal")
	}
}

// Concurrent calls are serialised: two seats' worth of calls leave a store whose
// threads are exactly what was answered, and the race detector sees nothing.
func TestConcurrentCalls(t *testing.T) {
	w := newWorld(t)
	a := w.ok(w.s.Start(ctx)).Thread
	b := w.ok(w.s.Start(ctx)).Thread
	var wg sync.WaitGroup
	busy := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, id := range []string{a, b} {
				w.s.Round(ctx, id)
				w.s.Summary(ctx, id)
				w.s.Answer(ctx, id, 1, webApp[0])
			}
			_, err := w.s.Start(ctx)
			busy <- err
		}()
	}
	wg.Wait()
	close(busy)
	for err := range busy {
		refused(t, err, ownerwire.CodeGenesisBusy)
	}
	for _, id := range []string{a, b} {
		if r := w.ok(w.s.Round(ctx, id)); r.Round != 2 {
			t.Fatalf("thread %s at round %d", id, r.Round)
		}
	}
	w.open()
	for _, id := range []string{a, b} {
		if r := w.ok(w.s.Round(ctx, id)); r.Round != 2 {
			t.Fatalf("after a restart, thread %s at round %d", id, r.Round)
		}
	}
}

// Genesis dispatches each verb of the wire's union, and refuses params the wire's
// form refuses.
func TestGenesisDispatchesTheWireVerbs(t *testing.T) {
	w := newWorld(t)
	do := func(p ownerwire.SeatGenesisParams) ownerwire.SeatGenesisResult {
		t.Helper()
		return w.ok(w.s.Genesis(ctx, p))
	}
	st := do(ownerwire.SeatGenesisParams{Verb: ownerwire.GenesisVerbStart})
	id := st.Thread
	if r := do(ownerwire.SeatGenesisParams{Verb: ownerwire.GenesisVerbRound, Thread: id}); !reflect.DeepEqual(r, st) {
		t.Fatalf("round %+v", r)
	}
	do(ownerwire.SeatGenesisParams{Verb: ownerwire.GenesisVerbAnswer, Thread: id, Round: 1, Answers: webApp[0]})
	do(ownerwire.SeatGenesisParams{Verb: ownerwire.GenesisVerbAnswer, Thread: id, Round: 2, Answers: webApp[1]})
	ch := do(ownerwire.SeatGenesisParams{Verb: ownerwire.GenesisVerbChange, Thread: id, Node: "goal", Answer: &ownerwire.GenesisPick{Kind: ownerwire.GenesisKindChoice, IDs: []string{"share"}}})
	if ch.Round != 3 {
		t.Fatalf("change %+v", ch)
	}
	if r := do(ownerwire.SeatGenesisParams{Verb: ownerwire.GenesisVerbSummary, Thread: id}); r.State != ownerwire.GenesisStateSummary || r.Groups == nil {
		t.Fatalf("summary %+v", r)
	}
	// The discard of an open thread (the finished one refuses it).
	other := do(ownerwire.SeatGenesisParams{Verb: ownerwire.GenesisVerbStart}).Thread
	if r := do(ownerwire.SeatGenesisParams{Verb: ownerwire.GenesisVerbDiscard, Thread: other}); r.State != ownerwire.GenesisStateDiscarded {
		t.Fatalf("discard %+v", r)
	}
	if r := do(ownerwire.SeatGenesisParams{Verb: ownerwire.GenesisVerbFinish, Thread: id}); r.State != ownerwire.GenesisStateFinished {
		t.Fatalf("finish %+v", r)
	}
	for _, p := range []ownerwire.SeatGenesisParams{
		{},
		{Verb: "start", Thread: id},
		{Verb: "round"},
		{Verb: "answer", Thread: id, Round: 1},
		{Verb: "change", Thread: id, Node: "goal"},
		{Verb: "round", Thread: "XYZ"},
	} {
		_, err := w.s.Genesis(ctx, p)
		refused(t, err, CodeBadParams)
	}
}

// Every result of the store passes the wire for each walk of the interview and
// every state, which the helper checks; and the refusals carry only gate words.
func TestRefusalsAreGateWords(t *testing.T) {
	for code := range map[string]bool{
		ownerwire.CodeGenesisBusy: true, ownerwire.CodeGenesisUnknownThread: true, ownerwire.CodeGenesisBadAnswer: true,
		ownerwire.CodeGenesisOverCap: true, ownerwire.CodeGenesisSecretShaped: true, ownerwire.CodeGenesisDone: true, CodeBadParams: true,
		ownerwire.CodeGenesisStaleRound: true, ownerwire.CodeGenesisNotReady: true, ownerwire.CodeGenesisNoTemplate: true,
	} {
		err := refuse(code)
		if got, ok := RefusalCode(fmt.Errorf("wrapped: %w", err)); !ok || got != code {
			t.Errorf("%s: %q %v", code, got, ok)
		}
		var g interface{ GateCode() string }
		if !errors.As(err, &g) || g.GateCode() != code || err.Error() != "genesis: "+code {
			t.Errorf("%s: %v", code, err)
		}
	}
	if _, ok := RefusalCode(errors.New("x")); ok {
		t.Fatal("a plain error is a refusal")
	}
}

// A folder may hold more open threads than the store would open (files
// restored by hand): List still answers, with the two oldest, in the wire's
// form; the third is served by id; and Start is refused busy.
func TestListNeverFailsOnMoreOpenThreadsThanItShows(t *testing.T) {
	w := newWorld(t)
	a := w.ok(w.s.Start(ctx)).Thread
	w.clock.advance(time.Minute)
	b := w.ok(w.s.Start(ctx)).Thread
	// A third open thread, from another folder whose ids start elsewhere,
	// started an hour before the two: the oldest of the three.
	other := newWorld(t)
	other.rand.n = 100
	other.clock.advance(-time.Hour)
	c := other.ok(other.s.Start(ctx)).Thread
	other.ok(other.s.Answer(ctx, c, 1, webApp[0]))
	other.s.Close()
	data, err := os.ReadFile(filepath.Join(other.dir, c+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w.dir, c+".json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	w.open()
	list := w.ok(w.s.List(ctx))
	if len(list.Threads) != 2 || list.Threads[0].Thread != c || list.Threads[1].Thread != a || list.Threads[0].Round != 2 {
		t.Fatalf("the list of three open: %+v", list.Threads)
	}
	if got := w.ok(w.s.Genesis(ctx, ownerwire.SeatGenesisParams{Verb: ownerwire.GenesisVerbList})); len(got.Threads) != 2 {
		t.Fatalf("the list through the wire's verb: %+v", got.Threads)
	}
	if r := w.ok(w.s.Round(ctx, b)); r.Thread != b {
		t.Fatalf("the newest is not served by id: %+v", r)
	}
	_, err = w.s.Start(ctx)
	refused(t, err, ownerwire.CodeGenesisBusy)
	// One discarded: the list shows the other two, and a start is still
	// refused while two are open.
	w.ok(w.s.Discard(ctx, c))
	if l := w.ok(w.s.List(ctx)); len(l.Threads) != 2 || l.Threads[0].Thread != a || l.Threads[1].Thread != b {
		t.Fatalf("after a discard: %+v", l.Threads)
	}
	_, err = w.s.Start(ctx)
	refused(t, err, ownerwire.CodeGenesisBusy)
}

// List answers the open threads, oldest first, each where it stands and with
// nothing of its answers; a finished, discarded or expired thread is not
// listed; it touches no thread; and a restart lists the same.
func TestListAnswersTheOpenThreads(t *testing.T) {
	w := newWorld(t)
	empty := w.ok(w.s.List(ctx))
	if empty.State != ownerwire.GenesisStateThreads || empty.Threads == nil || len(empty.Threads) != 0 || empty.Thread != "" || empty.CatalogDigest != "" {
		t.Fatalf("an empty store lists %+v", empty)
	}
	a := w.ok(w.s.Start(ctx)).Thread
	w.clock.advance(time.Minute)
	b := w.ok(w.s.Start(ctx)).Thread
	// b is the newer, though its id would sort first were it the older.
	w.ok(w.s.Answer(ctx, b, 1, webApp[0]))
	want := []ownerwire.GenesisThread{
		{Thread: a, State: ownerwire.GenesisStateQuestion, Round: 1, Of: 6, Filled: 0, Total: 1},
		{Thread: b, State: ownerwire.GenesisStateQuestion, Round: 2, Of: 6, Filled: 1, Total: 14},
	}
	got := w.ok(w.s.List(ctx))
	if !reflect.DeepEqual(got.Threads, want) {
		t.Fatalf("the list %+v, want %+v", got.Threads, want)
	}
	// Each entry is where its thread stands, as round says.
	for _, e := range got.Threads {
		r := w.ok(w.s.Round(ctx, e.Thread))
		if r.State != e.State || r.Round != e.Round || r.Of != e.Of || r.Progress.Filled != e.Filled || r.Progress.Total != e.Total {
			t.Fatalf("%s: listed %+v, round answers %+v", e.Thread, e, r)
		}
	}
	// Nothing of an answer is in it.
	if s := js(got); strings.Contains(s, "software") || strings.Contains(s, "kind") {
		t.Fatalf("the list holds an answer: %s", s)
	}
	// A thread whose walk is done is listed as a summary; a restart lists
	// the same.
	sum := w.interview(b, 1)
	got = w.ok(w.s.List(ctx))
	if len(got.Threads) != 2 || got.Threads[1] != (ownerwire.GenesisThread{Thread: b, State: ownerwire.GenesisStateSummary, Round: 6, Of: 6,
		Filled: sum.Progress.Filled, Total: sum.Progress.Total}) {
		t.Fatalf("the list with a summary: %+v", got.Threads)
	}
	w.open()
	if again := w.ok(w.s.List(ctx)); !reflect.DeepEqual(again, got) {
		t.Fatalf("after a restart: %+v, want %+v", again, got)
	}
	// A list touches nothing: a's clock still runs from its start.
	files := w.files()
	w.clock.advance(Expiry - time.Minute - time.Second)
	w.ok(w.s.List(ctx))
	if !reflect.DeepEqual(w.files(), files) {
		t.Fatalf("a list wrote a file: %v", w.files())
	}
	// Finished, discarded and expired threads are not listed.
	w.ok(w.s.Finish(ctx, b))
	if got := w.ok(w.s.List(ctx)); len(got.Threads) != 1 || got.Threads[0].Thread != a {
		t.Fatalf("after a finish: %+v", got.Threads)
	}
	w.clock.advance(time.Second)
	if got := w.ok(w.s.List(ctx)); len(got.Threads) != 0 || w.log.count("a thread expired") != 1 {
		t.Fatalf("after the expiry: %+v, log %q", got.Threads, w.log.all())
	}
	c := w.ok(w.s.Start(ctx)).Thread
	w.ok(w.s.Discard(ctx, c))
	if got := w.ok(w.s.List(ctx)); len(got.Threads) != 0 {
		t.Fatalf("after a discard: %+v", got.Threads)
	}
	// Through the wire's verb, which takes no member.
	if r := w.ok(w.s.Genesis(ctx, ownerwire.SeatGenesisParams{Verb: ownerwire.GenesisVerbList})); r.State != ownerwire.GenesisStateThreads {
		t.Fatalf("the list verb: %+v", r)
	}
	_, err := w.s.Genesis(ctx, ownerwire.SeatGenesisParams{Verb: ownerwire.GenesisVerbList, Thread: c})
	refused(t, err, CodeBadParams)
}
