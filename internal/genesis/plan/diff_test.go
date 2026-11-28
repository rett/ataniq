package plan

import (
	"bytes"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"ataniq/internal/genesis/catalog"
)

// A changed answer gives a revision whose diff names the tasks added,
// removed and changed by id, and the fields changed by name, with short
// texts: here the owner moved from a web app with nothing saved to one that
// keeps each person's data and hosts it.
func TestDiffOfAChangedBrief(t *testing.T) {
	c := v1(t)
	before := walk(t, c, map[string]catalog.Answer{"kind": pickOne("software"), "software.sub_kind": pickOne("web_app")}, defaults)
	after := walk(t, c, map[string]catalog.Answer{
		"kind": pickOne("software"), "software.sub_kind": pickOne("web_app"), "software.data": pickOne("accounts"),
		"software.deploy_kind": pickOne("hosting"), "budget_band": pickOne("low"),
	}, defaults)
	p1 := compile(t, c, before)
	p2, err := Next(p1, after, c)
	must(t, err)
	d := Diff(p1, p2)
	if d.From != 1 || d.To != 2 || d.FromDigest == "" || d.ToDigest == "" || d.More != 0 {
		t.Fatalf("diff %+v", d)
	}
	fields := map[string]FieldChange{}
	for _, f := range d.Fields {
		fields[f.Name] = f
	}
	if f := fields["budget_band"]; f.Old != "none" || f.New != "low" {
		t.Errorf("budget band %+v", f)
	}
	if f := fields["external_services"]; f.Old != "" || f.New != "hosting service" {
		t.Errorf("services %+v", f)
	}
	for _, name := range []string{"title", "template", "template_digest", "gates", "first_demo", "brief_digest", "prev_digest", "plan"} {
		if _, ok := fields[name]; ok {
			t.Errorf("%s changed", name)
		}
	}
	have := titles(p2)
	storage := have["Keep each person's data on the server, apart from everyone else's"]
	hosting := have["Prepare the set-up for the hosting service, without deploying"]
	ops := map[string]ItemChange{}
	for _, it := range d.Tasks {
		ops[it.ID] = it
	}
	if it := ops[storage.ID]; it.Op != Added || it.New != storage.Title || it.Old != "" {
		t.Errorf("storage %+v", it)
	}
	if it := ops[hosting.ID]; it.Op != Added {
		t.Errorf("hosting %+v", it)
	}
	tests := have["Write tests for the flow and the ways it can fail"]
	if it := ops[tests.ID]; it.Op != Changed || !reflect.DeepEqual(it.Members, []string{"depends_on"}) {
		t.Errorf("tests %+v", it)
	}
	for _, it := range d.Milestones {
		if it.Op != Changed || !reflect.DeepEqual(it.Members, []string{"tasks"}) {
			t.Errorf("milestone %+v", it)
		}
	}
	out, err := EncodeDiff(d)
	must(t, err)
	again, err := EncodeDiff(Diff(p1, p2))
	must(t, err)
	if !bytes.Equal(out, again) {
		t.Fatal("the diff's canonical form is not stable")
	}
	golden(t, "diff-web-app-accounts.json", out)
}

// Another sub-kind changes the title, the demo and the page sketch; the
// diff goes both ways.
func TestDiffOfASubKind(t *testing.T) {
	c := v1(t)
	app := compile(t, c, walk(t, c, map[string]catalog.Answer{"kind": pickOne("software"), "software.sub_kind": pickOne("web_app")}, defaults))
	tool := compile(t, c, walk(t, c, map[string]catalog.Answer{"kind": pickOne("software"), "software.sub_kind": pickOne("web_tool")}, defaults))
	d := Diff(app, tool)
	back := Diff(tool, app)
	if len(d.Tasks) != len(back.Tasks) || len(d.Fields) != len(back.Fields) {
		t.Fatalf("the diff is not symmetric: %+v %+v", d, back)
	}
	for i, it := range d.Tasks {
		r := back.Tasks[i]
		if it.ID != r.ID || it.Old != r.New || it.New != r.Old {
			t.Errorf("%+v against %+v", it, r)
		}
	}
	got := map[string]FieldChange{}
	for _, f := range d.Fields {
		got[f.Name] = f
	}
	if got["title"].Old != "A full web app" || got["title"].New != "A one-page tool" {
		t.Errorf("title %+v", got["title"])
	}
	if !strings.HasPrefix(got["first_demo"].New, "It runs on this computer and its one job") {
		t.Errorf("demo %+v", got["first_demo"])
	}
	removed := false
	for _, it := range d.Tasks {
		if it.Op == Removed && it.Old == "Sketch the pages and how one leads to the next" {
			removed = true
		}
	}
	if !removed {
		t.Error("the page sketch is not removed")
	}
	if !Diff(app, app).Empty() {
		t.Error("a plan differs from itself")
	}
}

// The owner's notes are counted in a diff, never quoted.
func TestDiffNeverQuotesTheOwner(t *testing.T) {
	c := v1(t)
	picks := func(text string) map[string]catalog.Answer {
		return map[string]catalog.Answer{"kind": pickOne("software"), "software.sub_kind": pickOne("web_app"), "software.stack": pickOther(text)}
	}
	a := compile(t, c, walk(t, c, picks("first words of mine"), defaults))
	b := compile(t, c, walk(t, c, picks("second words of mine"), defaults))
	d := Diff(a, b)
	out, err := EncodeDiff(d)
	must(t, err)
	if strings.Contains(string(out), "words of mine") {
		t.Fatal("the diff quotes the owner")
	}
	var notes FieldChange
	for _, f := range d.Fields {
		if f.Name == "notes" {
			notes = f
		}
	}
	if notes.Old != "1 note" || notes.New != "1 note, changed" {
		t.Fatalf("notes %+v", notes)
	}
	if len(d.Tasks) != 0 || len(d.Milestones) != 0 {
		t.Fatalf("other words changed tasks: %+v", d)
	}
}

// big returns a plan of eight milestones of six tasks, whose titles carry
// the word given: over the record's caps, so that the diff's own cap is
// reached (a plan within the caps has fewer changes than it).
func big(t testing.TB, word string) Plan {
	t.Helper()
	p := sample(t)
	p.Milestones = nil
	n := 0
	for i := 1; i <= 8; i++ {
		m := Milestone{ID: "m" + strconv.Itoa(i), Title: "Step " + word, Goal: "A step"}
		for j := 0; j < 6; j++ {
			n++
			m.Tasks = append(m.Tasks, Task{ID: "t" + strconv.Itoa(n), Title: strings.TrimSpace(strings.Repeat(word+" é ", 10)), Kind: KindWrite, Size: SizeSmall, DependsOn: []string{}, Scope: []string{"repo:docs"}, Checks: []string{}})
		}
		p.Milestones = append(p.Milestones, m)
	}
	return p
}

// A diff lists at most 48 changes, fields first, then milestones, then
// tasks, and counts the rest; a short text is cut on a character boundary.
func TestDiffCaps(t *testing.T) {
	a, b := big(t, "alpha"), big(t, "omega")
	b.BudgetBand = "medium"
	d := Diff(a, b)
	n := len(d.Fields) + len(d.Milestones) + len(d.Tasks)
	if n != MaxDiffEntries || d.More != 1+8+48-MaxDiffEntries || d.FromDigest != "" {
		t.Fatalf("%d listed, %d more", n, d.More)
	}
	if len(d.Fields) != 1 || len(d.Milestones) != 8 || d.Tasks[0].ID != "t1" || d.Tasks[len(d.Tasks)-1].ID != "t39" {
		t.Fatalf("the order is not fields, milestones, tasks: %d %d", len(d.Fields), len(d.Milestones))
	}
	for _, it := range d.Tasks {
		if len(it.Old) > MaxShort || !utf8.ValidString(it.Old) || !strings.HasSuffix(it.Old, "...") {
			t.Fatalf("short text %q", it.Old)
		}
	}
	// Ids sort by number: t9 before t10.
	ids := unionIDs(map[string]int{"t10": 1, "t9": 1, "t1": 1}, map[string]int{"t2": 1})
	if !reflect.DeepEqual(ids, []string{"t1", "t2", "t9", "t10"}) {
		t.Fatalf("ids %v", ids)
	}
}
