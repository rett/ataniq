package thread

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/genesis/brief"
	"ataniq/internal/genesis/catalog"
	"ataniq/internal/genesis/plan"
)

// walk answers the thread's rounds to the summary: each question skipped to
// its default, or given its first option.
func (w *world) walk(id string) ownerwire.SeatGenesisResult {
	w.t.Helper()
	res := w.ok(w.s.Round(ctx, id))
	for i := 0; res.State == ownerwire.GenesisStateQuestion; i++ {
		if i > catalog.MaxRounds {
			w.t.Fatal("the walk does not end")
		}
		var answers []ownerwire.GenesisAnswer
		for _, n := range res.Nodes {
			switch {
			case n.CanSkip:
				answers = append(answers, skip(n.ID))
			case n.Type == ownerwire.GenesisTypeMulti:
				answers = append(answers, choices(n.ID, n.Options[0].ID))
			case n.Type == ownerwire.GenesisTypeScale:
				answers = append(answers, choice(n.ID, "3"))
			default:
				answers = append(answers, choice(n.ID, n.Options[0].ID))
			}
		}
		res = w.ok(w.s.Answer(ctx, id, res.Round, answers))
	}
	return res
}

// askPlan asks the plan of the thread through the wire's verb.
func (w *world) askPlan(id string) (ownerwire.SeatGenesisResult, error) {
	return w.s.Genesis(ctx, ownerwire.SeatGenesisParams{Verb: ownerwire.GenesisVerbPlan, Thread: id})
}

// compiled is the plan the compiler gives the thread's brief now, and its
// digest.
func (w *world) compiled(id string) (plan.Plan, string) {
	w.t.Helper()
	p, err := plan.Compile(w.s.threads[id].brief, v1(w.t))
	if err != nil {
		w.t.Fatal(err)
	}
	d, err := plan.Digest(p)
	if err != nil {
		w.t.Fatal(err)
	}
	return p, d
}

// The plan of a thread at its summary is a preview, compiled on read and kept
// nowhere; finish stores it, in the write that freezes the brief; the plan of
// the finished thread is that one, stored, the same after a restart.
func TestThePlanIsPreviewedThenStoredAtFinish(t *testing.T) {
	w := newWorld(t)
	id := w.ok(w.s.Start(ctx)).Thread
	sum := w.interview(id, 0)
	if sum.State != ownerwire.GenesisStateSummary || !sum.Progress.PlanReady {
		t.Fatalf("the summary is not ready for a plan: %+v", sum.Progress)
	}
	before := w.bytesOf(id)
	pre := w.ok(w.askPlan(id))
	p, d := w.compiled(id)
	if pre.State != ownerwire.GenesisStatePlan || pre.Stored || pre.Plan == nil || pre.Plan.Revision != 1 || pre.Plan.PrevDigest != "" ||
		pre.PlanDigest != d || pre.Thread != id || pre.Round != sum.Round || pre.Progress != sum.Progress || pre.Plan.Title != p.Title {
		t.Fatalf("the preview: %+v", pre)
	}
	if again := w.ok(w.askPlan(id)); !reflect.DeepEqual(again, pre) {
		t.Fatalf("the preview asked again: %+v, want %+v", again, pre)
	}
	if !bytes.Equal(w.bytesOf(id), before) || w.s.threads[id].plan != nil {
		t.Fatal("a preview was stored")
	}
	if strings.Contains(string(before), `"plan":{`) || strings.Contains(string(before), `"plan_digest"`) {
		t.Fatal("an open thread's file holds a plan")
	}
	// A preview is a read: it does not hold the thread off its expiry.
	if w.s.threads[id].updated != w.clock.now() {
		t.Fatal("the clock moved")
	}

	fin := w.ok(w.s.Finish(ctx, id))
	if fin.State != ownerwire.GenesisStateFinished || fin.Plan != nil || !fin.Progress.PlanReady {
		t.Fatalf("finish: %+v", fin)
	}
	r := w.s.threads[id]
	if r.plan == nil || r.planDigest != d || !reflect.DeepEqual(*r.plan, p) {
		t.Fatalf("finish stored %+v", r.plan)
	}
	data := w.bytesOf(id)
	canonical, err := plan.Encode(p)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, append(append([]byte(`"plan":`), canonical...), ',')) || !bytes.Contains(data, []byte(`"plan_digest":"`+d+`"`)) {
		t.Fatalf("the file does not hold the plan and its digest: %s", data)
	}
	stored := w.ok(w.askPlan(id))
	want := pre
	want.Stored = true
	if !reflect.DeepEqual(stored, want) {
		t.Fatalf("the stored plan: %+v, want the preview stored: %+v", stored, want)
	}
	// Reads and a finish again change nothing.
	w.ok(w.askPlan(id))
	w.ok(w.s.Finish(ctx, id))
	if !bytes.Equal(w.bytesOf(id), data) || w.log.count("a plan was drawn") != 1 {
		t.Fatalf("a read or a second finish wrote the thread: %q", w.log.all())
	}
	// A restart serves the same plan, and the same finished thread.
	w.open()
	if got := w.ok(w.askPlan(id)); !reflect.DeepEqual(got, stored) {
		t.Fatalf("after the restart: %+v, want %+v", got, stored)
	}
	if got := w.ok(w.s.Round(ctx, id)); !reflect.DeepEqual(got, fin) {
		t.Fatalf("the finished thread after the restart: %+v", got)
	}
	if w.log.count("not served") != 0 {
		t.Fatalf("log %q", w.log.all())
	}
	// The log holds fixed texts only: nothing of the plan.
	for _, l := range w.log.all() {
		if strings.Contains(l, p.Title) || strings.Contains(l, d) || strings.Contains(l, p.Milestones[0].Title) || strings.Contains(l, id) {
			t.Fatalf("the log holds the plan: %q", l)
		}
	}
}

// "You decide" on the language, the sign-in and where the app runs leaves
// three answers inferred for the owner, not confirmed. The interview is still
// ready for a plan: its progress says every required answer is filled, in the
// same notion as plan_ready, and the preview is drawn, with the warning
// inferred-answers, and stores nothing; the open thread's brief keeps the
// three answers as they were, not confirmed. Finishing stores the plan, with
// the warning.
func TestInferredAnswersAreReadyForAPlan(t *testing.T) {
	w := newWorld(t)
	id := w.ok(w.s.Start(ctx)).Thread
	sum := w.answerAll(id, youDecideWebApp)
	if sum.State != ownerwire.GenesisStateSummary || sum.Progress.Filled != sum.Progress.Total || sum.Progress.Total != 14 || !sum.Progress.PlanReady {
		t.Fatalf("the summary: %+v", sum.Progress)
	}
	if sum.Groups == nil || len(sum.Groups.DecidedForYou) != len(inferredFields) {
		t.Fatalf("decided for you: %+v", sum.Groups)
	}
	b := w.s.threads[id].brief
	for _, name := range inferredFields {
		if f, _ := b.Get(name); f.Provenance != brief.SupervisorInferred || f.Confirmed {
			t.Fatalf("%s: %+v", name, f)
		}
	}
	if f, n := catalog.Readiness(v1(t), b); f != n-len(inferredFields) {
		t.Fatalf("readiness %d of %d: the inferred answers count as confirmed", f, n)
	}
	// The list's progress is the same.
	list := w.ok(w.s.List(ctx))
	if len(list.Threads) != 1 || list.Threads[0].Filled != sum.Progress.Filled || list.Threads[0].Total != sum.Progress.Total {
		t.Fatalf("the list: %+v", list.Threads)
	}

	before := w.bytesOf(id)
	pre := w.ok(w.askPlan(id))
	p, d := w.compiled(id)
	if pre.Stored || pre.Plan == nil || pre.PlanDigest != d || pre.Progress != sum.Progress || !slicesContain(pre.Plan.Warnings, plan.WarnInferred) {
		t.Fatalf("the preview: %+v", pre)
	}
	if !bytes.Equal(w.bytesOf(id), before) || w.s.threads[id].plan != nil || !reflect.DeepEqual(w.s.threads[id].brief, b) {
		t.Fatal("a preview was stored, or touched the brief")
	}

	fin := w.ok(w.s.Finish(ctx, id))
	if fin.State != ownerwire.GenesisStateFinished || !fin.Progress.PlanReady || fin.Progress.Filled != fin.Progress.Total {
		t.Fatalf("finish: %+v", fin)
	}
	r := w.s.threads[id]
	if r.plan == nil || !slicesContain(r.plan.Warnings, plan.WarnInferred) || !plan.ContentEqual(*r.plan, p) {
		t.Fatalf("finish stored %+v, want the preview's content", r.plan)
	}
	stored := w.ok(w.askPlan(id))
	if !stored.Stored || stored.PlanDigest != r.planDigest || !slicesContain(stored.Plan.Warnings, plan.WarnInferred) {
		t.Fatalf("the stored plan: %+v", stored)
	}
	if w.log.count("a plan was drawn") != 1 {
		t.Fatalf("log %q", w.log.all())
	}
}

func slicesContain(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// A brief that is not complete has no plan: genesis-not-ready, on an open
// thread and on one finished early, which finish freezes as it is, without
// a plan; the verb writes nothing.
func TestAPlanOfABriefNotCompleteIsNotReady(t *testing.T) {
	w := newWorld(t)
	id := w.ok(w.s.Start(ctx)).Thread
	_, err := w.askPlan(id)
	refused(t, err, ownerwire.CodeGenesisNotReady)
	res := w.ok(w.s.Answer(ctx, id, 1, webApp[0]))
	if res.Progress.PlanReady {
		t.Fatalf("a question round is ready for a plan: %+v", res.Progress)
	}
	w.ok(w.s.Answer(ctx, id, 2, webApp[1]))
	_, err = w.askPlan(id)
	refused(t, err, ownerwire.CodeGenesisNotReady)
	fin := w.ok(w.s.Finish(ctx, id))
	if fin.State != ownerwire.GenesisStateFinished || fin.Progress.PlanReady || w.s.threads[id].plan != nil {
		t.Fatalf("an early finish: %+v", fin)
	}
	data := w.bytesOf(id)
	if bytes.Contains(data, []byte(`"plan`)) {
		t.Fatal("an early finish stored a plan")
	}
	_, err = w.askPlan(id)
	refused(t, err, ownerwire.CodeGenesisNotReady)
	if !bytes.Equal(w.bytesOf(id), data) || w.log.count("plan") != 0 {
		t.Fatalf("a refused plan wrote or logged: %q", w.log.all())
	}
}

// A kind no template serves, the kind in the owner's own words, has no plan:
// genesis-no-template, before the brief is complete and after, open and
// finished; finish freezes it as it is.
func TestAPlanOfAKindWithoutATemplate(t *testing.T) {
	w := newWorld(t)
	id := w.ok(w.s.Start(ctx)).Thread
	w.ok(w.s.Answer(ctx, id, 1, []ownerwire.GenesisAnswer{other("kind", "a board game for two players")}))
	_, err := w.askPlan(id)
	refused(t, err, ownerwire.CodeGenesisNoTemplate)
	sum := w.walk(id)
	if sum.State != ownerwire.GenesisStateSummary || sum.Progress.Filled != sum.Progress.Total || sum.Progress.PlanReady {
		t.Fatalf("the summary of a kind of the owner's: %+v", sum.Progress)
	}
	_, err = w.askPlan(id)
	refused(t, err, ownerwire.CodeGenesisNoTemplate)
	w.ok(w.s.Finish(ctx, id))
	if w.s.threads[id].plan != nil || bytes.Contains(w.bytesOf(id), []byte(`"plan`)) {
		t.Fatal("a plan was stored for a kind no template serves")
	}
	_, err = w.askPlan(id)
	refused(t, err, ownerwire.CodeGenesisNoTemplate)
	if w.log.count("plan") != 0 {
		t.Fatalf("log %q", w.log.all())
	}
}

// A discarded or unknown thread has no plan.
func TestAPlanOfAThreadThatIsNot(t *testing.T) {
	w := newWorld(t)
	id := w.ok(w.s.Start(ctx)).Thread
	w.interview(id, 0)
	w.ok(w.s.Discard(ctx, id))
	_, err := w.askPlan(id)
	refused(t, err, ownerwire.CodeGenesisUnknownThread)
	_, err = w.askPlan("ffffffffffffffff")
	refused(t, err, ownerwire.CodeGenesisUnknownThread)
	_, err = w.s.Genesis(ctx, ownerwire.SeatGenesisParams{Verb: ownerwire.GenesisVerbPlan})
	refused(t, err, CodeBadParams)
}

// writeRecord writes a thread's file as the store would, for a record the
// test made.
func (w *world) writeRecord(r record) {
	w.t.Helper()
	data, err := encodeRecord(r)
	if err != nil {
		w.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w.dir, fileOf(r.id)), data, 0o600); err != nil {
		w.t.Fatal(err)
	}
}

// A thread finished before plans were stored is a file without one, which is
// valid: it is served as before, and the first plan asked of it is drawn
// then, stored once in the store's atomic write and logged; after that it is
// the stored one.
func TestAThreadFinishedBeforePlansGetsItsPlanOnce(t *testing.T) {
	w := newWorld(t)
	id := w.ok(w.s.Start(ctx)).Thread
	w.interview(id, 0)
	fin := w.ok(w.s.Finish(ctx, id))
	r := w.s.threads[id]
	r.plan, r.planDigest = nil, ""
	w.writeRecord(r)
	old := w.bytesOf(id)
	w.open()
	if got := w.ok(w.s.Round(ctx, id)); !reflect.DeepEqual(got, fin) {
		t.Fatalf("the old finished thread: %+v, want %+v", got, fin)
	}
	if !fin.Progress.PlanReady {
		t.Fatal("an old finished thread whose brief is complete is not ready for a plan")
	}
	drawn := w.log.count("a plan was drawn")
	got := w.ok(w.askPlan(id))
	_, d := w.compiled(id)
	if !got.Stored || got.PlanDigest != d || got.Plan.Revision != 1 {
		t.Fatalf("the plan drawn late: %+v", got)
	}
	if w.log.count("a plan was drawn") != drawn+1 || bytes.Equal(w.bytesOf(id), old) || w.s.threads[id].planDigest != d {
		t.Fatalf("the plan was not stored once: %q", w.log.all())
	}
	if !w.s.threads[id].updated.Equal(r.updated) {
		t.Fatal("drawing the plan touched the thread")
	}
	data := w.bytesOf(id)
	if again := w.ok(w.askPlan(id)); !reflect.DeepEqual(again, got) || !bytes.Equal(w.bytesOf(id), data) || w.log.count("a plan was drawn") != drawn+1 {
		t.Fatalf("the plan asked again: %+v", again)
	}
	w.open()
	if again := w.ok(w.askPlan(id)); !reflect.DeepEqual(again, got) {
		t.Fatalf("after the restart: %+v", again)
	}
}

// A plan drawn late is kept only when it is the plan of the thread's frozen
// brief. A compiler that answers a plan of another brief (forged here through
// the store's test seam) is not kept and not served: the call fails with the
// typed error, the log says fixed words and nothing of the plan, the file is
// as it was, and the next ask, with the compiler in order, draws the plan.
func TestAPlanOfAnotherBriefIsNotKeptWhenDrawnLate(t *testing.T) {
	w := newWorld(t)
	id := w.ok(w.s.Start(ctx)).Thread
	w.interview(id, 0)
	w.ok(w.s.Finish(ctx, id))
	r := w.s.threads[id]
	r.plan, r.planDigest = nil, ""
	w.writeRecord(r)
	w.open()
	before := w.bytesOf(id)
	drawn := w.log.count("a plan was drawn")
	w.s.compiler = func(b brief.Brief) (plan.Plan, string, error) {
		p, err := plan.Compile(b, v1(t))
		if err != nil {
			return plan.Plan{}, "", err
		}
		p.BriefDigest = strings.Repeat("9e", 32)
		d, err := plan.Digest(p)
		return p, d, err
	}
	res, err := w.askPlan(id)
	if !errors.Is(err, ErrPlanBrief) || res.Plan != nil || res.PlanDigest != "" {
		t.Fatalf("a plan of another brief: %+v, %v", res, err)
	}
	if _, refused := RefusalCode(err); refused {
		t.Fatal("a forged plan is refused as the owner's mistake")
	}
	if !bytes.Equal(w.bytesOf(id), before) || w.s.threads[id].plan != nil || w.log.count("a plan was drawn") != drawn {
		t.Fatalf("the forged plan was kept: %q", w.log.all())
	}
	if w.log.count("did not match the thread's brief and was not kept") != 1 || strings.Contains(strings.Join(w.log.all(), "\n"), "9e9e") {
		t.Fatalf("the log: %q", w.log.all())
	}
	// With the compiler in order the next ask draws the plan and keeps it.
	w.s.compiler = nil
	got := w.ok(w.askPlan(id))
	if _, d := w.compiled(id); !got.Stored || got.PlanDigest != d {
		t.Fatalf("the plan drawn after the fault: %+v", got)
	}
}

// A stored plan is never compiled again: a plan whose template digest is that
// of no template embedded now (a template changed since it was stored) is
// served as it was stored, digest and all, and is not replaced.
func TestAStoredPlanIsServedAsStored(t *testing.T) {
	w := newWorld(t)
	id := w.ok(w.s.Start(ctx)).Thread
	w.interview(id, 0)
	w.ok(w.s.Finish(ctx, id))
	r := w.s.threads[id]
	p := r.plan.Clone()
	p.TemplateDigest = strings.Repeat("0f", 32)
	p.Template.Version = "v0"
	p.Milestones[0].Title = "A milestone of an older template"
	ts, err := plan.Templates()
	if err != nil {
		t.Fatal(err)
	}
	for _, tpl := range ts {
		if tpl.Digest() == p.TemplateDigest {
			t.Fatal("the hand-written digest is a template's")
		}
	}
	d, err := plan.Digest(p)
	if err != nil {
		t.Fatal(err)
	}
	r.plan, r.planDigest = &p, d
	w.writeRecord(r)
	data := w.bytesOf(id)
	w.open()
	drawn := w.log.count("plan")
	got := w.ok(w.askPlan(id))
	if !got.Stored || got.PlanDigest != d || got.Plan.TemplateDigest != p.TemplateDigest || got.Plan.Template.Version != "v0" ||
		got.Plan.Milestones[0].Title != "A milestone of an older template" {
		t.Fatalf("the stored plan was not served as stored: %+v", got)
	}
	if _, now := w.compiled(id); now == d {
		t.Fatal("the plan compiled now is the stored one")
	}
	if !bytes.Equal(w.bytesOf(id), data) || w.log.count("plan") != drawn {
		t.Fatalf("the stored plan was replaced: %q", w.log.all())
	}
}

// A thread file whose plan member does not pass its checks is not served,
// like any file that fails a check: logged once by name, never repaired.
// A file without a plan stays valid.
func TestAFileWithABadPlanIsNotServed(t *testing.T) {
	finished := func(w *world) record {
		id := w.ok(w.s.Start(ctx)).Thread
		w.interview(id, 0)
		w.ok(w.s.Finish(ctx, id))
		return w.s.threads[id]
	}
	digestOf := func(t *testing.T, p plan.Plan) string {
		d, err := plan.Digest(p)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	cases := map[string]struct {
		edit func(t *testing.T, w *world, r record)
		why  string
	}{
		"a plan with another digest": {func(t *testing.T, w *world, r record) {
			r.planDigest = strings.Repeat("ab", 32)
			w.writeRecord(r)
		}, "does not match its digest"},
		"a plan of another brief": {func(t *testing.T, w *world, r record) {
			p := r.plan.Clone()
			p.BriefDigest = strings.Repeat("ab", 32)
			r.plan, r.planDigest = &p, digestOf(t, p)
			w.writeRecord(r)
		}, "not drawn from the thread's brief"},
		"a plan of another catalog": {func(t *testing.T, w *world, r record) {
			p := r.plan.Clone()
			p.CatalogDigest = strings.Repeat("ab", 32)
			r.plan, r.planDigest = &p, digestOf(t, p)
			w.writeRecord(r)
		}, "not drawn from the thread's brief"},
		"a plan on an open thread": {func(t *testing.T, w *world, r record) {
			r.state, r.frozen = StateOpen, ""
			w.writeRecord(r)
		}, "not finished has a plan"},
		"a plan without its digest": {func(t *testing.T, w *world, r record) {
			data := w.bytesOf(r.id)
			bad := bytes.Replace(data, []byte(`"plan_digest":"`+r.planDigest+`",`), nil, 1)
			if bytes.Equal(bad, data) {
				t.Fatal("the file holds no plan digest")
			}
			if err := os.WriteFile(filepath.Join(w.dir, fileOf(r.id)), bad, 0o600); err != nil {
				t.Fatal(err)
			}
		}, "not both in the file"},
		"a plan that breaks its rules": {func(t *testing.T, w *world, r record) {
			// Written by hand: the record's encoder refuses such a plan.
			data := w.bytesOf(r.id)
			bad := bytes.Replace(data, []byte(`"enforcement":"advisory"`), []byte(`"enforcement":"enforced"`), 1)
			if bytes.Equal(bad, data) {
				t.Fatal("the file holds no budget band")
			}
			if err := os.WriteFile(filepath.Join(w.dir, fileOf(r.id)), bad, 0o600); err != nil {
				t.Fatal(err)
			}
		}, "the plan does not pass its own checks"},
		"a plan with a member it lacks": {func(t *testing.T, w *world, r record) {
			data := w.bytesOf(r.id)
			bad := bytes.Replace(data, []byte(`"notes_dropped":0`), []byte(`"notes_dropped":0,"owner":"x"`), 1)
			if bytes.Equal(bad, data) {
				t.Fatal("the file holds no plan")
			}
			if err := os.WriteFile(filepath.Join(w.dir, fileOf(r.id)), bad, 0o600); err != nil {
				t.Fatal(err)
			}
		}, "the plan does not pass its own checks"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			r := finished(w)
			c.edit(t, w, r)
			edited := w.bytesOf(r.id)
			w.open()
			if n := w.log.count(fileOf(r.id) + " is not served"); n != 1 || !strings.Contains(strings.Join(w.log.all(), "\n"), c.why) {
				t.Fatalf("log %q, want one line with %q", w.log.all(), c.why)
			}
			_, err := w.askPlan(r.id)
			refused(t, err, ownerwire.CodeGenesisUnknownThread)
			if !bytes.Equal(w.bytesOf(r.id), edited) {
				t.Fatal("the file was changed")
			}
		})
	}
	// A finished thread's file holds its plan; the file of a thread finished
	// early holds none; both are served.
	w := newWorld(t)
	full := finished(w)
	early := w.ok(w.s.Start(ctx)).Thread
	w.ok(w.s.Finish(ctx, early))
	w.open()
	if w.log.count("not served") != 0 || w.s.threads[full.id].plan == nil || w.s.threads[early].plan != nil {
		t.Fatalf("log %q", w.log.all())
	}
}

// A finished thread's plan goes with its file when the eight-file cap prunes
// it.
func TestAPrunedThreadTakesItsPlan(t *testing.T) {
	w := newWorld(t)
	first := w.ok(w.s.Start(ctx)).Thread
	w.interview(first, 0)
	w.ok(w.s.Finish(ctx, first))
	for i := 0; i < MaxFiles; i++ {
		w.clock.advance(1)
		id := w.ok(w.s.Start(ctx)).Thread
		w.ok(w.s.Discard(ctx, id))
	}
	_, err := w.askPlan(first)
	refused(t, err, ownerwire.CodeGenesisUnknownThread)
	if _, err := os.Stat(filepath.Join(w.dir, fileOf(first))); !os.IsNotExist(err) {
		t.Fatalf("the pruned thread's file: %v", err)
	}
}
