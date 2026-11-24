package plan

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

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
	want := p.Clone()
	want.CompiledAt = time.Time{}
	if !reflect.DeepEqual(back, want) {
		t.Fatalf("the decoded plan differs:\n%#v\n%#v", back, want)
	}
	if again := encode(t, back); !bytes.Equal(again, out) {
		t.Fatal("the canonical form is not stable")
	}
	// A pretty spelling decodes to the same plan.
	var pretty bytes.Buffer
	must(t, json.Indent(&pretty, out, " ", "\t"))
	pb, err := Decode(pretty.Bytes())
	must(t, err)
	if !reflect.DeepEqual(pb, want) {
		t.Fatal("a pretty spelling decodes to another plan")
	}
}

func TestEnvelope(t *testing.T) {
	p := sample(t)
	out, err := EncodeEnvelope(p)
	must(t, err)
	d, err := Digest(p)
	must(t, err)
	if !strings.HasPrefix(string(out), `{"compiled_at":"2026-11-23T10:00:00Z","digest":"`+d+`","plan":{`) {
		t.Fatalf("the envelope is %s", out[:120])
	}
	back, err := DecodeEnvelope(out)
	must(t, err)
	if !reflect.DeepEqual(back, p) {
		t.Fatal("the envelope does not round trip")
	}
	// A digest that is not the plan's, a time that is not UTC to the
	// second, and an unknown member are refused.
	for name, data := range map[string]string{
		"digest":   strings.Replace(string(out), d, strings.Repeat("0", 64), 1),
		"time":     strings.Replace(string(out), "2026-11-23T10:00:00Z", "2026-11-23T10:00:00+01:00", 1),
		"member":   strings.Replace(string(out), `{"compiled_at"`, `{"x":1,"compiled_at"`, 1),
		"revision": strings.Replace(string(out), `"plan":1,`, `"plan":2,`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := DecodeEnvelope([]byte(data))
			wantCode(t, err, brief.CodePlanInvalid)
		})
	}
	_, err = DecodeEnvelope(bytes.Repeat([]byte(" "), MaxEnvelopeBytes+1))
	wantCode(t, err, brief.CodeTooLarge)
	q := p.Clone()
	q.CompiledAt = time.Time{}.Add(time.Nanosecond)
	_, err = EncodeEnvelope(q)
	wantCode(t, err, brief.CodePlanInvalid)
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
	cases := map[string]struct {
		data string
		code brief.Code
	}{
		"an unknown member":        {replace(`{"brief_digest"`, `{"extra":"x","brief_digest"`), brief.CodePlanInvalid},
		"a repeated member":        {replace(`{"brief_digest"`, `{"title":"A","brief_digest"`), brief.CodePlanInvalid},
		"a float":                  {replace(`"plan":1`, `"plan":1.0`), brief.CodePlanInvalid},
		"a null":                   {replace(`"hosts":"own_server"`, `"hosts":null`), brief.CodePlanInvalid},
		"revision 0":               {replace(`"plan":1`, `"plan":0`), brief.CodePlanInvalid},
		"a schema":                 {replace(`"ataniq.project-plan/1"`, `"ataniq.project-plan/2"`), brief.CodePlanInvalid},
		"an estimate not marked":   {replace(`"estimate":true`, `"estimate":false`), brief.CodePlanInvalid},
		"another caveat":           {replace(EstimateCaveat, "a promise"), brief.CodePlanInvalid},
		"another label":            {replace(`"label":"`, `"label":"x`), brief.CodePlanInvalid},
		"a note not untrusted":     {replace(`"untrusted":true`, `"untrusted":false`), brief.CodePlanInvalid},
		"a never-list cut":         {replace(`"publish","release",`, `"release",`), brief.CodePlanInvalid},
		"a spend number":           {replace(`"spend":"none"`, `"spend":"50"`), brief.CodePlanInvalid},
		"a host":                   {replace(`"hosts":"own_server"`, `"hosts":"cloud"`), brief.CodePlanInvalid},
		"a service":                {replace(`"external_services":["hosting service"]`, `"external_services":["payments"]`), brief.CodePlanInvalid},
		"a gate":                   {replace(`"gates":["lint","unit-tests"]`, `"gates":["lint","deploy"]`), brief.CodePlanInvalid},
		"no gate":                  {replace(`"gates":["lint","unit-tests"]`, `"gates":[]`), brief.CodePlanInvalid},
		"gates unsorted":           {replace(`"gates":["lint","unit-tests"]`, `"gates":["unit-tests","lint"]`), brief.CodePlanInvalid},
		"a kind":                   {replace(`"kind":"build"`, `"kind":"deploy"`), brief.CodePlanInvalid},
		"a tier":                   {replace(`"tier":"fast"`, `"tier":"gpt"`), brief.CodePlanInvalid},
		"an area":                  {replace(`"scope":["repo:build"]`, `"scope":["repo:secrets"]`), brief.CodePlanInvalid},
		"a check":                  {replace(`"checks":["lint"]`, `"checks":["vibes"]`), brief.CodePlanInvalid},
		"a task id":                {replace(`"id":"t2"`, `"id":"t02"`), brief.CodePlanInvalid},
		"a repeated task id":       {replace(`"id":"t3"`, `"id":"t2"`), brief.CodePlanInvalid},
		"a milestone id":           {replace(`"id":"m1"`, `"id":"x1"`), brief.CodePlanInvalid},
		"a dependency unknown":     {replace(`"depends_on":["t2"]`, `"depends_on":["t39"]`), brief.CodePlanInvalid},
		"a dependency on itself":   {replace(`"depends_on":["t2"],"id":"t3"`, `"depends_on":["t3"],"id":"t3"`), brief.CodePlanInvalid},
		"dependencies unsorted":    {replace(`"depends_on":["t4","t5"]`, `"depends_on":["t5","t4"]`), brief.CodePlanInvalid},
		"a title with markup":      {replace(`"title":"A full web app"`, `"title":"<b>A full web app</b>"`), brief.CodePlanInvalid},
		"a title with an address":  {replace(`"title":"A full web app"`, `"title":"See www.example.com"`), brief.CodePlanInvalid},
		"a note's field":           {replace(`"field":"software.stack"`, `"field":"stack"`), brief.CodePlanInvalid},
		"a note shaped like a key": {replace(`"text":"the language my club already uses"`, `"text":"`+keyShaped()+`"`), brief.CodeSecretShaped},
		"a blank note":             {replace(`"text":"the language my club already uses"`, `"text":"   "`), brief.CodePlanInvalid},
		"a long note":              {replace(`"text":"the language my club already uses"`, `"text":"`+strings.Repeat("a", MaxNoteText+1)+`"`), brief.CodeTooLarge},
		"notes dropped with room":  {replace(`"notes_dropped":0`, `"notes_dropped":1`), brief.CodePlanInvalid},
		"a template version":       {replace(`"version":"v1"`, `"version":"V1"`), brief.CodePlanInvalid},
		"text after the value":     {v + "x", brief.CodePlanInvalid},
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

// Check refuses a cycle in the dependencies, and every cap.
func TestCheckRefuses(t *testing.T) {
	base := sample(t)
	cases := map[string]func(p *Plan){
		"a cycle": func(p *Plan) {
			ts := p.Milestones[0].Tasks
			ts[0].DependsOn = []string{ts[1].ID}
			ts[1].DependsOn = []string{ts[0].ID}
		},
		"nine milestones": func(p *Plan) {
			for i := len(p.Milestones); i < MaxMilestones+1; i++ {
				p.Milestones = append(p.Milestones, Milestone{ID: "m" + strconv.Itoa(10+i), Title: "More", Goal: "More",
					Tasks: []Task{{ID: "t" + strconv.Itoa(100+i), Title: "More", Kind: KindBuild, Tier: TierFast, DependsOn: []string{}, Scope: []string{"repo:src"}, Checks: []string{}}}})
			}
		},
		"seven tasks in a milestone": func(p *Plan) {
			m := &p.Milestones[len(p.Milestones)-1]
			for i := len(m.Tasks); i < MaxTasksPerMilestone+1; i++ {
				m.Tasks = append(m.Tasks, Task{ID: "t" + strconv.Itoa(100+i), Title: "More", Kind: KindBuild, Tier: TierFast, DependsOn: []string{}, Scope: []string{"repo:src"}, Checks: []string{}})
			}
		},
		"no milestone":       func(p *Plan) { p.Milestones = nil },
		"an empty milestone": func(p *Plan) { p.Milestones[0].Tasks = nil },
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
		"no review point":    func(p *Plan) { p.ReviewPoints = nil },
		"a review point":     func(p *Plan) { p.ReviewPoints = []string{"never"} },
		"an estimate band":   func(p *Plan) { p.Estimate.Band = "month" },
		"a never-list added": func(p *Plan) { p.Never = append(p.Never, "nothing") },
		"a never-list order": func(p *Plan) { p.Never[0], p.Never[1] = p.Never[1], p.Never[0] },
		"a brief digest":     func(p *Plan) { p.BriefDigest = strings.ToUpper(p.BriefDigest) },
		"a catalog digest":   func(p *Plan) { p.CatalogDigest = "" },
		"a template id":      func(p *Plan) { p.Template.ID = "Software" },
		"a revision":         func(p *Plan) { p.Revision = MaxRevision + 1 },
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
	// Forty tasks in all is the cap: eight milestones of five pass, and a
	// forty-first task does not.
	p := base.Clone()
	p.Milestones = nil
	n := 0
	for i := 1; i <= MaxMilestones; i++ {
		m := Milestone{ID: "m" + strconv.Itoa(i), Title: "Step", Goal: "A step"}
		for j := 0; j < 5; j++ {
			n++
			m.Tasks = append(m.Tasks, Task{ID: "t" + strconv.Itoa(n), Title: "Work", Kind: KindWrite, Tier: TierDeep, DependsOn: []string{}, Scope: []string{"repo:docs"}, Checks: []string{"spelling"}})
		}
		p.Milestones = append(p.Milestones, m)
	}
	must(t, Check(p))
	p.Milestones[7].Tasks = append(p.Milestones[7].Tasks, Task{ID: "t41", Title: "Work", Kind: KindWrite, Tier: TierDeep, DependsOn: []string{}, Scope: []string{"repo:docs"}, Checks: []string{}})
	wantCode(t, Check(p), brief.CodePlanInvalid)
}

// keyShaped returns a text the brief's detector refuses, built at run time
// so that no file holds a key-shaped literal.
func keyShaped() string { return "gh" + "p_" + strings.Repeat("aB3dE5", 6) }
