package plan

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"ataniq/internal/genesis/brief"
	"ataniq/internal/genesis/catalog"
)

// sample is a compiled plan with notes, for the codec's tests.
func sample(t testing.TB) Plan {
	t.Helper()
	c := v1(t)
	b := walk(t, c, map[string]catalog.Answer{
		"kind": pickOne("software"), "software.sub_kind": pickOne("web_app"), "software.data": pickOne("accounts"),
		"software.stack": pickOther("the language my club already uses"), "software.deploy_kind": pickOne("hosting"),
	}, defaults)
	must(t, b.AddNote("The club meets on Tuesdays; bookings open a week ahead."))
	return compile(t, c, b)
}

func TestRoundTrip(t *testing.T) {
	p := sample(t)
	out := encode(t, p)
	back, err := Decode(out)
	must(t, err)
	if !reflect.DeepEqual(back, p) {
		t.Fatalf("the decoded plan differs:\n%#v\n%#v", back, p)
	}
	if again := encode(t, back); !bytes.Equal(again, out) {
		t.Fatal("the canonical form is not stable")
	}
	// A pretty spelling decodes to the same plan.
	var pretty bytes.Buffer
	must(t, json.Indent(&pretty, out, " ", "\t"))
	pb, err := Decode(pretty.Bytes())
	must(t, err)
	if !reflect.DeepEqual(pb, p) {
		t.Fatal("a pretty spelling decodes to another plan")
	}
}

// A plan of a template version that is no longer embedded, with areas and
// checks no template names today, still decodes: Decode holds a plan to its
// own rules, never to the templates of the build.
func TestAPlanOfAnOldTemplateDecodes(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "software-web-app-defaults.json"))
	must(t, err)
	p, err := Decode(data)
	must(t, err)
	old := strings.Repeat("5a", 32)
	ts, err := Templates()
	must(t, err)
	for _, tp := range ts {
		if tp.Digest() == old {
			t.Fatal("the fixture's digest is embedded")
		}
	}
	s := strings.Replace(string(data), p.TemplateDigest, old, 1)
	s = strings.Replace(s, `"version":"v1"`, `"version":"v0.9"`, 1)
	s = strings.Replace(s, `"scope":["repo:build"]`, `"scope":["repo:old-layout"]`, 1)
	s = strings.Replace(s, `"checks":["lint"]`, `"checks":["old-check"]`, 1)
	q, err := Decode([]byte(s))
	must(t, err)
	if q.TemplateDigest != old || q.Template.Version != "v0.9" {
		t.Fatalf("decoded %+v", q.Template)
	}
	if got := encode(t, q); string(got) != s {
		t.Fatal("the old plan does not encode to itself")
	}
}

// Decode refuses what is not the canonical record's form, member by member.
func TestDecodeRefuses(t *testing.T) {
	v := string(encode(t, sample(t)))
	replace := func(old, new string) string {
		t.Helper()
		if !strings.Contains(v, old) {
			t.Fatalf("the sample does not hold %s", old)
		}
		return strings.Replace(v, old, new, 1)
	}
	digest := strings.Repeat("ab", 32)
	cases := map[string]struct {
		data string
		code brief.Code
	}{
		"an unknown member":         {replace(`{"brief_digest"`, `{"extra":"x","brief_digest"`), brief.CodePlanInvalid},
		"a repeated member":         {replace(`{"brief_digest"`, `{"title":"A","brief_digest"`), brief.CodePlanInvalid},
		"a compile time":            {replace(`{"brief_digest"`, `{"compiled_at":"2026-11-23T10:00:00Z","brief_digest"`), brief.CodePlanInvalid},
		"a never-list":              {replace(`{"brief_digest"`, `{"never":["publish"],"brief_digest"`), brief.CodePlanInvalid},
		"a float":                   {replace(`"plan":1`, `"plan":1.0`), brief.CodePlanInvalid},
		"a null":                    {replace(`"hosts":"own_server"`, `"hosts":null`), brief.CodePlanInvalid},
		"revision 0":                {replace(`"plan":1`, `"plan":0`), brief.CodePlanInvalid},
		"revision 2 without prev":   {replace(`"plan":1`, `"plan":2`), brief.CodePlanInvalid},
		"revision 1 with a prev":    {replace(`"prev_digest":""`, `"prev_digest":"`+digest+`"`), brief.CodePlanInvalid},
		"a prev that is not hex":    {replace(`"plan":1,"prev_digest":""`, `"plan":2,"prev_digest":"x"`), brief.CodePlanInvalid},
		"a schema":                  {replace(`"ataniq.project-plan/1"`, `"ataniq.project-plan/2"`), brief.CodePlanInvalid},
		"an estimate not marked":    {replace(`"estimate":true`, `"estimate":false`), brief.CodePlanInvalid},
		"another caveat":            {replace(EstimateCaveat, "a promise"), brief.CodePlanInvalid},
		"another label":             {replace(`"label":"`, `"label":"x`), brief.CodePlanInvalid},
		"a budget enforced":         {replace(`"enforcement":"advisory"`, `"enforcement":"enforced"`), brief.CodePlanInvalid},
		"a budget number":           {replace(`"id":"none"`, `"id":"50"`), brief.CodePlanInvalid},
		"a note not untrusted":      {replace(`"untrusted":true`, `"untrusted":false`), brief.CodePlanInvalid},
		"a host":                    {replace(`"hosts":"own_server"`, `"hosts":"cloud"`), brief.CodePlanInvalid},
		"a service":                 {replace(`"external_services":["hosting service"]`, `"external_services":["payments"]`), brief.CodePlanInvalid},
		"a warning":                 {replace(`"settle-at-desk"`, `"all-clear"`), brief.CodePlanInvalid},
		"a check id":                {replace(`"gates":["lint","unit-tests"]`, `"gates":["Lint","unit-tests"]`), brief.CodePlanInvalid},
		"no gate":                   {replace(`"gates":["lint","unit-tests"]`, `"gates":[]`), brief.CodePlanInvalid},
		"gates unsorted":            {replace(`"gates":["lint","unit-tests"]`, `"gates":["unit-tests","lint"]`), brief.CodePlanInvalid},
		"a kind":                    {replace(`"kind":"build"`, `"kind":"deploy"`), brief.CodePlanInvalid},
		"a size":                    {replace(`"size":"small"`, `"size":"opus"`), brief.CodePlanInvalid},
		"a tier":                    {replace(`"size":"small"`, `"tier":"small"`), brief.CodePlanInvalid},
		"an area":                   {replace(`"scope":["repo:build"]`, `"scope":["cloud:build"]`), brief.CodePlanInvalid},
		"a task id":                 {replace(`"id":"t2"`, `"id":"t02"`), brief.CodePlanInvalid},
		"a repeated task id":        {replace(`"id":"t3"`, `"id":"t2"`), brief.CodePlanInvalid},
		"a milestone id":            {replace(`"id":"m1"`, `"id":"x1"`), brief.CodePlanInvalid},
		"a dependency unknown":      {replace(`"depends_on":["t2"]`, `"depends_on":["t24"]`), brief.CodePlanInvalid},
		"a dependency on itself":    {replace(`"depends_on":["t2"],"id":"t3"`, `"depends_on":["t3"],"id":"t3"`), brief.CodePlanInvalid},
		"a dependency on a later":   {replace(`"depends_on":["t2"],"id":"t3"`, `"depends_on":["t4"],"id":"t3"`), brief.CodePlanInvalid},
		"dependencies out of order": {replace(`"depends_on":["t3","t4"]`, `"depends_on":["t4","t3"]`), brief.CodePlanInvalid},
		"a title with markup":       {replace(`"title":"A full web app"`, `"title":"<b>A full web app</b>"`), brief.CodePlanInvalid},
		"a title with an address":   {replace(`"title":"A full web app"`, `"title":"See www.example.com"`), brief.CodePlanInvalid},
		"a note's field":            {replace(`"field":"software.stack"`, `"field":"stack"`), brief.CodePlanInvalid},
		"a note shaped like a key":  {replace(`"text":"the language my club already uses"`, `"text":"`+keyShaped()+`"`), brief.CodeSecretShaped},
		"a blank note":              {replace(`"text":"the language my club already uses"`, `"text":"   "`), brief.CodePlanInvalid},
		"a long note":               {replace(`"text":"the language my club already uses"`, `"text":"`+strings.Repeat("a", MaxNoteText+1)+`"`), brief.CodeTooLarge},
		"notes dropped with room":   {replace(`"notes_dropped":0`, `"notes_dropped":1`), brief.CodePlanInvalid},
		"a template version":        {replace(`"version":"v1"`, `"version":"V1"`), brief.CodePlanInvalid},
		"a template digest":         {replace(`"template_digest":"`, `"template_digest":"x`), brief.CodePlanInvalid},
		"a template digest inside":  {replace(`"version":"v1"`, `"version":"v1","digest":"`+digest+`"`), brief.CodePlanInvalid},
		"text after the value":      {v + "x", brief.CodePlanInvalid},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Decode([]byte(c.data))
			wantCode(t, err, c.code)
		})
	}
	_, err := Decode(bytes.Repeat([]byte(" "), MaxPlanBytes+1))
	wantCode(t, err, brief.CodeTooLarge)
}

func task(id string) Task {
	return Task{ID: id, Title: "More", Kind: KindBuild, Size: SizeSmall, DependsOn: []string{}, Scope: []string{"repo:src"}, Checks: []string{}}
}

// Check refuses a dependency on a task that is not before it, and every
// cap.
func TestCheckRefuses(t *testing.T) {
	base := sample(t)
	cases := map[string]func(p *Plan){
		"a cycle": func(p *Plan) {
			ts := p.Milestones[0].Tasks
			ts[0].DependsOn = []string{ts[1].ID}
			ts[1].DependsOn = []string{ts[0].ID}
		},
		"five milestones": func(p *Plan) {
			p.Milestones = p.Milestones[:1]
			p.Milestones[0].Tasks = p.Milestones[0].Tasks[:1]
			p.Milestones[0].Tasks[0].DependsOn = []string{}
			for i := 2; i <= MaxMilestones+1; i++ {
				p.Milestones = append(p.Milestones, Milestone{ID: "m" + strconv.Itoa(i), Title: "More", Goal: "More", Tasks: []Task{task("t" + strconv.Itoa(20+i))}})
			}
		},
		"seven tasks in a milestone": func(p *Plan) {
			p.Milestones = p.Milestones[:1]
			m := &p.Milestones[0]
			for i := len(m.Tasks); i < MaxTasksPerMilestone+1; i++ {
				m.Tasks = append(m.Tasks, task("t"+strconv.Itoa(30+i)))
			}
		},
		"thirteen tasks": func(p *Plan) {
			m := &p.Milestones[len(p.Milestones)-1]
			for n := len(p.Tasks()); n <= MaxTasks; n++ {
				m.Tasks = append(m.Tasks, task("t"+strconv.Itoa(50+n)))
			}
		},
		"no milestone":       func(p *Plan) { p.Milestones = nil },
		"an empty milestone": func(p *Plan) { p.Milestones[2].Tasks = nil },
		"no scope":           func(p *Plan) { p.Milestones[0].Tasks[0].Scope = nil },
		"a long title":       func(p *Plan) { p.Title = strings.Repeat("a", MaxTitle+1) },
		"a long goal":        func(p *Plan) { p.Milestones[0].Goal = strings.Repeat("a", MaxGoal+1) },
		"a long task title":  func(p *Plan) { p.Milestones[0].Tasks[0].Title = strings.Repeat("a", MaxTaskTitle+1) },
		"a long demo":        func(p *Plan) { p.FirstDemo = strings.Repeat("a", MaxDemo+1) },
		"a spaced title":     func(p *Plan) { p.Title = " A web app" },
		"nine notes": func(p *Plan) {
			for len(p.Notes) <= MaxNotes {
				p.Notes = append(p.Notes, Note{Field: FieldNotes, Text: "one more"})
			}
		},
		"no review point":  func(p *Plan) { p.ReviewPoints = nil },
		"a review point":   func(p *Plan) { p.ReviewPoints = []string{"never"} },
		"an estimate band": func(p *Plan) { p.Estimate.Band = "month" },
		"a budget band":    func(p *Plan) { p.BudgetBand = "high" },
		"warnings twice":   func(p *Plan) { p.Warnings = []string{WarnDesk, WarnDesk} },
		"a brief digest":   func(p *Plan) { p.BriefDigest = strings.ToUpper(p.BriefDigest) },
		"a catalog digest": func(p *Plan) { p.CatalogDigest = "" },
		"a template id":    func(p *Plan) { p.Template.ID = "Software" },
		"a revision":       func(p *Plan) { p.Revision = MaxRevision + 1 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			p := base.Clone()
			mutate(&p)
			err := Check(p)
			if _, ok := brief.CodeOf(err); !ok {
				t.Fatalf("Check passed or failed without a code: %v", err)
			}
			if _, err := Encode(p); err == nil {
				t.Fatal("Encode wrote a plan Check refuses")
			}
		})
	}
	// Twelve tasks in all is the cap: four milestones of three pass.
	p := base.Clone()
	p.Milestones = nil
	n := 0
	for i := 1; i <= MaxMilestones; i++ {
		m := Milestone{ID: "m" + strconv.Itoa(i), Title: "Step", Goal: "A step"}
		for j := 0; j < 3; j++ {
			n++
			m.Tasks = append(m.Tasks, task("t"+strconv.Itoa(n)))
		}
		p.Milestones = append(p.Milestones, m)
	}
	must(t, Check(p))
}

// keyShaped returns a text the brief's detector refuses, built at run time
// so that no file holds a key-shaped literal.
func keyShaped() string { return "gh" + "p_" + strings.Repeat("aB3dE5", 6) }
