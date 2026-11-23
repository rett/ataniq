package brief

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// testCatalog is a neutral catalog digest for the tests.
const testCatalog = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

var t0 = time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)

func at(minutes int) time.Time { return t0.Add(time.Duration(minutes) * time.Minute) }

func must(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func wantCode(t testing.TB, err error, code Code) {
	t.Helper()
	got, ok := CodeOf(err)
	if !ok || got != code {
		t.Fatalf("got %v, want the code %s", err, code)
	}
}

// sample builds the brief of the vector: every form of value, every
// provenance, a note, and a history with each reason.
func sample(t testing.TB) Brief {
	t.Helper()
	b := New(testCatalog)
	must(t, b.Set("kind", One("software"), OwnerChosen, "kind", 1, at(0)))
	must(t, b.Set("software.sub_kind", One("web_app"), OwnerChosen, "software.sub_kind", 2, at(1)))
	must(t, b.Set("software.stack", One("go"), OwnerChosen, "software.stack", 3, at(2)))
	must(t, b.Set("software.data", One("local"), OwnerChosen, "software.data", 3, at(2)))
	must(t, b.Set("goal", One("try"), OwnerChosen, "goal", 2, at(1)))
	must(t, b.Set("goal", One("share"), OwnerChosen, "goal", 3, at(3)))
	must(t, b.Set("audience", One("few"), OwnerChosen, "audience", 2, at(1)))
	must(t, b.Set("review_points", Many("publish", "demo"), Default, "review_points", 3, at(4)))
	must(t, b.Set("software.auth", OtherText("a sign-in link sent by email"), OwnerChosen, "software.auth", 3, at(4)))
	must(t, b.Set("software.deploy_kind", One("local"), SupervisorInferred, "software.deploy_kind", 3, at(5)))
	must(t, b.Set("hosts", One("own_server"), SupervisorInferred, "hosts", 3, at(5)))
	must(t, b.Confirm("hosts"))
	n, err := b.ClearClosure([]string{"software.stack", "software.data", "software.auth"}, "software.sub_kind", at(6))
	must(t, err)
	if n != 3 {
		t.Fatalf("cleared %d fields, want 3", n)
	}
	must(t, b.Clear("audience", at(7)))
	must(t, b.AddNote("The club meets on Tuesdays; bookings open a week ahead."))
	return b
}

func TestRoundTrip(t *testing.T) {
	b := sample(t)
	out, err := Encode(b)
	must(t, err)
	back, err := Decode(out)
	must(t, err)
	if !reflect.DeepEqual(back, b) {
		t.Fatalf("the decoded brief differs:\n%#v\n%#v", back, b)
	}
	again, err := Encode(back)
	must(t, err)
	if !bytes.Equal(again, out) {
		t.Fatal("the canonical form is not stable")
	}
}

// The vector: a fixed brief has fixed canonical bytes and a fixed digest.
// A pretty spelling of it, with whitespace, decodes to the same
// brief.
func TestVector(t *testing.T) {
	want, err := os.ReadFile(filepath.Join("testdata", "brief-valid.json"))
	must(t, err)
	out, err := Encode(sample(t))
	must(t, err)
	if !bytes.Equal(out, want) {
		t.Fatalf("the canonical form changed:\n%s", out)
	}
	d, err := Digest(sample(t))
	must(t, err)
	if d != vectorDigest {
		t.Fatalf("the digest is %s, want %s", d, vectorDigest)
	}
	pretty, err := os.ReadFile(filepath.Join("testdata", "brief-valid.pretty.json"))
	must(t, err)
	b, err := Decode(pretty)
	must(t, err)
	if !reflect.DeepEqual(b, sample(t)) {
		t.Fatal("the pretty vector decodes to another brief")
	}
}

const vectorDigest = "c1b78259e8100dba068ba7befa72cd993b90881c792e51ac25d487c728392bcb"

func TestNewBriefEncodes(t *testing.T) {
	out, err := Encode(New(testCatalog))
	must(t, err)
	want := `{"catalog":"` + testCatalog + `","fields":{},"history":[],"history_dropped":0,"kind":"","notes":[],"rounds":0,"schema":"ataniq.project-brief/1"}`
	if string(out) != want {
		t.Fatalf("got %s", out)
	}
	b, err := Decode(out)
	must(t, err)
	if !reflect.DeepEqual(b, New(testCatalog)) {
		t.Fatal("an empty brief does not round trip")
	}
}

// valid is the canonical vector as a string, for the refusal table.
func valid(t testing.TB) string {
	t.Helper()
	out, err := Encode(sample(t))
	must(t, err)
	return string(out)
}

func TestDecodeRefuses(t *testing.T) {
	v := valid(t)
	field := `"goal":{"at":"2026-11-23T10:03:00Z","node":"goal","provenance":"owner-chosen","round":3,"value":"share"}`
	if !strings.Contains(v, field) {
		t.Fatalf("the vector does not hold the goal field: %s", v)
	}
	replace := func(old, new string) string {
		t.Helper()
		if !strings.Contains(v, old) {
			t.Fatalf("the vector does not hold %s", old)
		}
		return strings.Replace(v, old, new, 1)
	}
	goal := func(new string) string { return replace(field, `"goal":`+new) }
	cases := []struct {
		name string
		data string
		code Code
	}{
		{"unknown top field", replace(`"history_dropped":`, `"extra":1,"history_dropped":`), CodeBriefInvalid},
		{"unknown field key", goal(`{"at":"2026-11-23T10:03:00Z","extra":1,"node":"goal","provenance":"owner-chosen","round":3,"value":"share"}`), CodeBriefInvalid},
		{"missing top field", replace(`"rounds":3,`, ``), CodeBriefInvalid},
		{"key in another case", replace(`"rounds":`, `"Rounds":`), CodeBriefInvalid},
		{"repeated key", replace(`"rounds":3`, `"rounds":3,"rounds":3`), CodeBriefInvalid},
		{"repeated field", replace(field, field+","+field), CodeBriefInvalid},
		{"float", replace(`"rounds":3`, `"rounds":3.0`), CodeBriefInvalid},
		{"null", replace(`"rounds":3`, `"rounds":null`), CodeBriefInvalid},
		{"trailing data", v + "{}", CodeBriefInvalid},
		{"control character", goal(`{"at":"2026-11-23T10:03:00Z","node":"goal","provenance":"owner-chosen","round":3,"value":{"other":"a\u0007b"}}`), CodeBriefInvalid},
		{"schema", replace(Schema, "ataniq.project-brief/2"), CodeBriefInvalid},
		{"catalog digest", replace(testCatalog, strings.ToUpper(testCatalog)), CodeBriefInvalid},
		{"kind not derived", replace(`"kind":"software"`, `"kind":"media"`), CodeBriefInvalid},
		{"field name", replace(`"goal":`, `"Goal":`), CodeFieldName},
		{"unknown top-level name", replace(`"goal":`, `"goals":`), CodeFieldName},
		{"extension of another kind", replace(`"software.sub_kind":`, `"media.sub_kind":`), CodeDomainMismatch},
		{"provenance", goal(`{"at":"2026-11-23T10:03:00Z","node":"goal","provenance":"owner","round":3,"value":"share"}`), CodeValueInvalid},
		{"confirmed on chosen", goal(`{"at":"2026-11-23T10:03:00Z","confirmed":true,"node":"goal","provenance":"owner-chosen","round":3,"value":"share"}`), CodeBriefInvalid},
		{"inferred without confirmed", goal(`{"at":"2026-11-23T10:03:00Z","node":"goal","provenance":"supervisor-inferred","round":3,"value":"share"}`), CodeBriefInvalid},
		{"node id", goal(`{"at":"2026-11-23T10:03:00Z","node":"Goal","provenance":"owner-chosen","round":3,"value":"share"}`), CodeValueInvalid},
		{"round zero", goal(`{"at":"2026-11-23T10:03:00Z","node":"goal","provenance":"owner-chosen","round":0,"value":"share"}`), CodeBriefInvalid},
		{"round past rounds", goal(`{"at":"2026-11-23T10:03:00Z","node":"goal","provenance":"owner-chosen","round":4,"value":"share"}`), CodeBriefInvalid},
		{"time with offset", goal(`{"at":"2026-11-23T10:03:00+01:00","node":"goal","provenance":"owner-chosen","round":3,"value":"share"}`), CodeValueInvalid},
		{"time with fraction", goal(`{"at":"2026-11-23T10:03:00.5Z","node":"goal","provenance":"owner-chosen","round":3,"value":"share"}`), CodeValueInvalid},
		{"choice id", goal(`{"at":"2026-11-23T10:03:00Z","node":"goal","provenance":"owner-chosen","round":3,"value":"Share"}`), CodeValueInvalid},
		{"empty choice", goal(`{"at":"2026-11-23T10:03:00Z","node":"goal","provenance":"owner-chosen","round":3,"value":""}`), CodeValueInvalid},
		{"choices unsorted", goal(`{"at":"2026-11-23T10:03:00Z","node":"goal","provenance":"owner-chosen","round":3,"value":["b","a"]}`), CodeValueInvalid},
		{"choices repeated", goal(`{"at":"2026-11-23T10:03:00Z","node":"goal","provenance":"owner-chosen","round":3,"value":["a","a"]}`), CodeValueInvalid},
		{"no choices", goal(`{"at":"2026-11-23T10:03:00Z","node":"goal","provenance":"owner-chosen","round":3,"value":[]}`), CodeValueInvalid},
		{"too many choices", goal(`{"at":"2026-11-23T10:03:00Z","node":"goal","provenance":"owner-chosen","round":3,"value":["a","b","c","d","e","f","g","h","i"]}`), CodeValueInvalid},
		{"empty other", goal(`{"at":"2026-11-23T10:03:00Z","node":"goal","provenance":"owner-chosen","round":3,"value":{"other":" "}}`), CodeValueInvalid},
		{"long other", goal(`{"at":"2026-11-23T10:03:00Z","node":"goal","provenance":"owner-chosen","round":3,"value":{"other":"` + strings.Repeat("a ", 101) + `"}}`), CodeTooLarge},
		{"other with another key", goal(`{"at":"2026-11-23T10:03:00Z","node":"goal","provenance":"owner-chosen","round":3,"value":{"other":"x","more":"y"}}`), CodeBriefInvalid},
		{"value a number", goal(`{"at":"2026-11-23T10:03:00Z","node":"goal","provenance":"owner-chosen","round":3,"value":3}`), CodeValueInvalid},
		{"secret in other", goal(`{"at":"2026-11-23T10:03:00Z","node":"goal","provenance":"owner-chosen","round":3,"value":{"other":"use ghp_aB3dE5gH7jK9mN1pQ3sT5vX7zA9cD1fG3hJ5"}}`), CodeSecretShaped},
		{"secret in a choice", goal(`{"at":"2026-11-23T10:03:00Z","node":"goal","provenance":"owner-chosen","round":3,"value":"0123456789abcdef0123456789abcdef"}`), CodeSecretShaped},
		{"secret in a note", replace(`"notes":[`, `"notes":["password=hunter2",`), CodeSecretShaped},
		{"empty note", replace(`"notes":[`, `"notes":["",`), CodeValueInvalid},
		{"nine notes", replace(`"notes":[`, `"notes":["a","b","c","d","e","f","g","h",`), CodeTooLarge},
		{"reason", replace(`"reason":"cleared"`, `"reason":"removed"`), CodeBriefInvalid},
		{"cause on cleared", replace(`"reason":"cleared"`, `"cause":"goal","reason":"cleared"`), CodeBriefInvalid},
		{"empty cause", replace(`"cause":"software.sub_kind"`, `"cause":""`), CodeBriefInvalid},
		{"history_dropped negative", replace(`"history_dropped":0`, `"history_dropped":-1`), CodeBriefInvalid},
		{"too large", v + strings.Repeat(" ", MaxBriefBytes), CodeTooLarge},
		{"not an object", `[]`, CodeBriefInvalid},
		{"not utf8", "\xff", CodeBriefInvalid},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Decode([]byte(c.data))
			wantCode(t, err, c.code)
			checkRefusal(t, err)
		})
	}
}

func TestDecodeRefusesOverCaps(t *testing.T) {
	// 65 fields.
	b := New(testCatalog)
	must(t, b.Set("kind", One("software"), OwnerChosen, "kind", 1, t0))
	for i := 0; i < MaxFields-1; i++ {
		must(t, b.Set(fmt.Sprintf("software.f%d", i), One("x"), OwnerChosen, "n", 1, t0))
	}
	err := b.Set("software.one_more", One("x"), OwnerChosen, "n", 1, t0)
	wantCode(t, err, CodeTooLarge)
	out, err := Encode(b)
	must(t, err)
	over := strings.Replace(string(out), `"fields":{`, `"fields":{"software.one_more":{"at":"2026-11-23T10:00:00Z","node":"n","provenance":"owner-chosen","round":1,"value":"x"},`, 1)
	_, err = Decode([]byte(over))
	wantCode(t, err, CodeTooLarge)

	// 33 history entries.
	h := `{"at":"2026-11-23T10:00:00Z","field":"goal","old":{"at":"2026-11-23T10:00:00Z","node":"n","provenance":"owner-chosen","round":1,"value":"x"},"reason":"cleared"}`
	many := strings.Repeat(h+",", MaxHistory) + h
	data := `{"catalog":"` + testCatalog + `","fields":{},"history":[` + many + `],"history_dropped":0,"kind":"","notes":[],"rounds":1,"schema":"ataniq.project-brief/1"}`
	_, err = Decode([]byte(data))
	wantCode(t, err, CodeTooLarge)
}

// The largest field a brief can hold is well under the per-field cap: a
// node id of 48 bytes, 8 choices of 32 bytes, and the longest round and
// time.
func TestLargestFieldIsUnderItsCap(t *testing.T) {
	ids := make([]string, MaxChoices)
	for i := range ids {
		ids[i] = fmt.Sprintf("%c%s", 'a'+i, strings.Repeat("z", MaxChoiceLen-1))
	}
	f := Field{Value: Many(ids...), Provenance: SupervisorInferred, Node: "n" + strings.Repeat("a", 47), Round: MaxRound, At: t0}
	must(t, f.check("f"))
	b := New(testCatalog)
	b.Fields["goal"] = f
	b.Rounds = MaxRound
	must(t, b.Validate())
	out, err := encode(b)
	must(t, err)
	if len(out) > MaxFieldBytes {
		t.Fatalf("a brief of one field is %d bytes", len(out))
	}
}

func TestSetRefusesAndLeavesTheBrief(t *testing.T) {
	b := sample(t)
	before, err := Encode(b)
	must(t, err)
	cases := []struct {
		name  string
		field string
		v     Value
		p     Provenance
		node  string
		round int
		code  Code
	}{
		{"field name", "Goal", One("x"), OwnerChosen, "goal", 3, CodeFieldName},
		{"unknown name", "colour", One("x"), OwnerChosen, "goal", 3, CodeFieldName},
		{"extension of another kind", "media.format", One("x"), OwnerChosen, "media.format", 3, CodeDomainMismatch},
		{"change of kind with old fields", "kind", One("media"), OwnerChosen, "kind", 3, CodeDomainMismatch},
		{"several kinds", "kind", Many("media", "software"), OwnerChosen, "kind", 3, CodeValueInvalid},
		{"no value", "goal", Value{}, OwnerChosen, "goal", 3, CodeValueInvalid},
		{"provenance", "goal", One("x"), "owner", "goal", 3, CodeValueInvalid},
		{"node", "goal", One("x"), OwnerChosen, "", 3, CodeValueInvalid},
		{"round zero", "goal", One("x"), OwnerChosen, "goal", 0, CodeValueInvalid},
		{"round too high", "goal", One("x"), OwnerChosen, "goal", MaxRound + 1, CodeValueInvalid},
		{"repeated choice", "review_points", Many("a", "a"), OwnerChosen, "review_points", 3, CodeValueInvalid},
		{"secret", "goal", OtherText("token=abc123"), OwnerChosen, "goal", 3, CodeSecretShaped},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := b.Set(c.field, c.v, c.p, c.node, c.round, at(9))
			wantCode(t, err, c.code)
			checkRefusal(t, err)
			after, err := Encode(b)
			must(t, err)
			if !bytes.Equal(after, before) {
				t.Fatal("a refused Set changed the brief")
			}
		})
	}
	wantCode(t, b.Confirm("goal"), CodeNotInferred)
	wantCode(t, b.Confirm("budget_band"), CodeNotInferred)
	wantCode(t, b.Clear("Goal", t0), CodeFieldName)
	_, err = b.ClearClosure([]string{"Goal"}, "kind", t0)
	wantCode(t, err, CodeFieldName)
	_, err = b.ClearClosure([]string{"goal"}, "", t0)
	wantCode(t, err, CodeFieldName)
	after, err := Encode(b)
	must(t, err)
	if !bytes.Equal(after, before) {
		t.Fatal("a refused operation changed the brief")
	}
}

func TestSetStampsAndCounts(t *testing.T) {
	b := New(testCatalog)
	local := time.Date(2026, 11, 23, 1, 2, 3, 456, time.FixedZone("x", -9*3600))
	must(t, b.Set("goal", One("try"), OwnerChosen, "goal", 2, local))
	f, _ := b.Get("goal")
	if !f.At.Equal(local.Truncate(time.Second)) || f.At.Location() != time.UTC || f.At.Nanosecond() != 0 {
		t.Fatalf("the time is %v", f.At)
	}
	if b.Rounds != 2 {
		t.Fatalf("rounds is %d", b.Rounds)
	}
	// Setting the same value again keeps no history.
	must(t, b.Set("goal", One("try"), Default, "goal", 2, local))
	if len(b.History) != 0 {
		t.Fatal("setting the same value kept history")
	}
	must(t, b.Set("goal", One("earn"), OwnerChosen, "goal", 2, local))
	if len(b.History) != 1 || b.History[0].Reason != ReasonReplaced || !b.History[0].Old.Value.Equal(One("try")) {
		t.Fatalf("the history is %+v", b.History)
	}
}

func TestKindAndDomain(t *testing.T) {
	b := New(testCatalog)
	must(t, b.Set("kind", OtherText("a board game"), OwnerChosen, "kind", 1, t0))
	if b.Kind != DomainOther {
		t.Fatalf("the kind is %q", b.Kind)
	}
	wantCode(t, b.Set("software.stack", One("go"), OwnerChosen, "software.stack", 1, t0), CodeDomainMismatch)
	must(t, b.Set("kind", One("software"), OwnerChosen, "kind", 1, t0))
	must(t, b.Set("software.stack", One("go"), OwnerChosen, "software.stack", 1, t0))
	wantCode(t, b.Set("kind", One("publishing"), OwnerChosen, "kind", 1, t0), CodeDomainMismatch)
	wantCode(t, b.Clear("kind", t0), CodeDomainMismatch)
	n, err := b.ClearClosure([]string{"software.stack", "kind"}, "kind", t0)
	must(t, err)
	if n != 2 || b.Kind != "" {
		t.Fatalf("cleared %d, kind %q", n, b.Kind)
	}
	must(t, b.Set("kind", One("publishing"), OwnerChosen, "kind", 1, t0))
	if b.Kind != DomainPublishing {
		t.Fatalf("the kind is %q", b.Kind)
	}
	// An unknown choice for the kind is the generic branch.
	must(t, b.Set("kind", One("research"), OwnerChosen, "kind", 1, t0))
	if b.Kind != DomainOther {
		t.Fatalf("the kind is %q", b.Kind)
	}
}

func TestClearClosureKeepsHistory(t *testing.T) {
	b := New(testCatalog)
	must(t, b.Set("kind", One("media"), OwnerChosen, "kind", 1, t0))
	must(t, b.Set("media.sub_kind", One("podcast"), OwnerChosen, "media.sub_kind", 2, at(1)))
	must(t, b.Set("media.format", One("audio"), OwnerChosen, "media.format.podcast", 3, at(2)))
	must(t, b.Set("media.length", One("short"), Default, "media.length", 3, at(2)))
	must(t, b.Set("audience", One("public"), OwnerChosen, "audience", 2, at(1)))
	n, err := b.ClearClosure([]string{"media.format", "media.length", "media.sources", "media.format"}, "media.sub_kind", at(5))
	must(t, err)
	if n != 2 {
		t.Fatalf("cleared %d fields, want 2 (one not set, one named twice)", n)
	}
	if _, ok := b.Get("media.format"); ok {
		t.Fatal("a cleared field is still live")
	}
	if _, ok := b.Get("audience"); !ok {
		t.Fatal("a field outside the closure was cleared")
	}
	if len(b.History) != 2 {
		t.Fatalf("the history has %d entries", len(b.History))
	}
	for i, want := range []struct {
		field string
		value Value
		p     Provenance
	}{{"media.format", One("audio"), OwnerChosen}, {"media.length", One("short"), Default}} {
		h := b.History[i]
		if h.Field != want.field || h.Reason != ReasonInvalidated || h.Cause != "media.sub_kind" ||
			!h.Old.Value.Equal(want.value) || h.Old.Provenance != want.p || !h.At.Equal(at(5)) || h.Old.Node == "" {
			t.Fatalf("history[%d] is %+v", i, h)
		}
	}
	// Nothing to clear is no change.
	n, err = b.ClearClosure([]string{"media.format"}, "media.sub_kind", at(6))
	must(t, err)
	if n != 0 || len(b.History) != 2 {
		t.Fatal("clearing nothing changed the brief")
	}
	// The history survives a round trip.
	out, err := Encode(b)
	must(t, err)
	back, err := Decode(out)
	must(t, err)
	if !reflect.DeepEqual(back.History, b.History) {
		t.Fatal("the history does not round trip")
	}
}

func TestHistoryIsCapped(t *testing.T) {
	b := New(testCatalog)
	for i := 0; i < MaxHistory+5; i++ {
		must(t, b.Set("goal", One(fmt.Sprintf("v%d", i)), OwnerChosen, "goal", 1, at(i)))
	}
	if len(b.History) != MaxHistory || b.HistoryDropped != 4 {
		t.Fatalf("%d entries, %d dropped", len(b.History), b.HistoryDropped)
	}
	if b.History[0].Old.Value.One() != "v4" {
		t.Fatalf("the oldest kept entry is %v", b.History[0].Old.Value.One())
	}
	// Under the size cap as well: long other texts.
	b = New(testCatalog)
	for i := 0; i < 200; i++ {
		text := Untrusted(fmt.Sprintf("answer %d %s", i, strings.Repeat("words and more words ", 8)))
		must(t, b.Set("goal", OtherText(text), OwnerChosen, "goal", 1, at(i)))
	}
	out, err := Encode(b)
	must(t, err)
	if len(out) > MaxBriefBytes || b.HistoryDropped == 0 {
		t.Fatalf("%d bytes, %d dropped", len(out), b.HistoryDropped)
	}
}

func TestNotes(t *testing.T) {
	b := New(testCatalog)
	for i := 0; i < MaxNotes; i++ {
		must(t, b.AddNote(Untrusted(fmt.Sprintf("note %d", i))))
	}
	wantCode(t, b.AddNote("one more"), CodeTooLarge)
	b = New(testCatalog)
	wantCode(t, b.AddNote(Untrusted(strings.Repeat("a", MaxFreeText+1))), CodeTooLarge)
	wantCode(t, b.AddNote("line\nbreak"), CodeValueInvalid)
	wantCode(t, b.AddNote("-----BEGIN OPENSSH PRIVATE KEY-----"), CodeSecretShaped)
	must(t, b.AddNote(Untrusted(strings.Repeat("é", MaxFreeText/2))))
}

func TestReadiness(t *testing.T) {
	b := New(testCatalog)
	must(t, b.Set("goal", One("try"), OwnerChosen, "goal", 1, t0))
	must(t, b.Set("audience", One("me"), Default, "audience", 1, t0))
	must(t, b.Set("hosts", One("own_server"), SupervisorInferred, "hosts", 1, t0))
	must(t, b.Set("schedule", One("week"), SupervisorInferred, "schedule", 1, t0))
	must(t, b.Confirm("schedule"))
	cases := []struct {
		required      []string
		filled, total int
	}{
		{nil, 0, 0},
		{[]string{"goal"}, 1, 1},
		{[]string{"audience"}, 1, 1},
		{[]string{"hosts"}, 0, 1},
		{[]string{"schedule"}, 1, 1},
		{[]string{"budget_band"}, 0, 1},
		{[]string{"goal", "goal"}, 1, 1},
		{[]string{"goal", "audience", "hosts", "schedule", "budget_band"}, 3, 5},
	}
	for _, c := range cases {
		f, n := b.Readiness(c.required)
		if f != c.filled || n != c.total {
			t.Errorf("%v: %d of %d, want %d of %d", c.required, f, n, c.filled, c.total)
		}
	}
}

// A decoder never upgrades a provenance: an inferred field stays inferred
// and unconfirmed, and does not count as filled.
func TestDecodeDoesNotUpgrade(t *testing.T) {
	b := New(testCatalog)
	must(t, b.Set("hosts", One("own_server"), SupervisorInferred, "hosts", 1, t0))
	out, err := Encode(b)
	must(t, err)
	back, err := Decode(out)
	must(t, err)
	f, _ := back.Get("hosts")
	if f.Provenance != SupervisorInferred || f.Confirmed || f.Filled() {
		t.Fatalf("the decoded field is %+v", f)
	}
	if !strings.Contains(string(out), `"confirmed":false`) {
		t.Fatal("an inferred field is written without its confirmation")
	}
}

func TestSecretShaped(t *testing.T) {
	refused := []string{
		"-----BEGIN RSA PRIVATE KEY-----",
		"my key is sk-abcdefghijklmnop1234",
		"sk_live_51HxAbCdEfGhIjKlMn",
		"ghp_aB3dE5gH7jK9mN1pQ3sT5vX7zA9cD1fG3hJ5",
		"github_pat_11ABCDEFG0123456789",
		"xoxb-123456789012-abcdefghij",
		"AKIAIOSFODNN7EXAMPLE",
		"password=hunter2",
		"use Token=abc",
		"api_key=xyz",
		"0123456789abcdef0123456789abcdef",
		"commit d670460b4b4aece5915caf5c68d12f560a9fe3e4 is it",
		"wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		"key: Zm9vYmFyYmF6cXV4MTIzNDU2Nzg5MEFCQ0RFRkdISUpL",
	}
	for _, s := range refused {
		if !SecretShaped(s) {
			t.Errorf("not refused: %q", s)
		}
	}
	passed := []string{
		"A booking page for the tennis club, with a waiting list.",
		"https://example.org/docs/2024/getting-started-with-the-thing",
		"the brand colour is #ff8800 and the accent #0a0a0a",
		"risk-free trial for the first month",
		"ask-me-anything sessions every month",
		"internationalization-and-localization-of-everything",
		"CamelCaseIdentifierNameForSomethingLong",
		"123e4567-e89b-12d3-a456-426614174000",
		"the password is shared at the desk",
		"a token of thanks",
		"bypass=on",
		"sk-",
		"deadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
		"",
	}
	for _, s := range passed {
		if SecretShaped(s) {
			t.Errorf("refused: %q", s)
		}
	}
}

func TestValidFieldName(t *testing.T) {
	for _, n := range append([]string{"software.stack", "media.rights", "publishing.style_guide", "software.a1_b"}, TopFields...) {
		if !ValidFieldName(n) {
			t.Errorf("refused %q", n)
		}
	}
	for _, n := range []string{"", "Goal", "goals", "software.", "software.Stack", "software.1a", "software.a.b",
		"music.format", "software", ".stack", "software.a-b", "software." + strings.Repeat("a", 33)} {
		if ValidFieldName(n) {
			t.Errorf("accepted %q", n)
		}
	}
}
