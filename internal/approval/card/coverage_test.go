package card

import (
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"ataniq/internal/approval/record"
)

// This file holds the two directions of the card's promise: everything
// under the binding's digest is on the model, and nothing is on the model
// beyond it except the three named exceptions.
//
// The first direction walks record.Binding by reflection. It fills every
// field with a value of its own, builds the model, and then changes one
// field at a time: the model must change with it. A field added to the
// binding later is walked too, and fails the test until the card shows it.

// leaf is one field of the binding that holds a value: a string, a number
// or a flag.
type leaf struct {
	path string
	kind reflect.Kind
}

// fillBinding returns a binding in which every leaf holds a value made
// from its own path, and the leaf at alter holds another one. Pointers are
// set and every list has two elements, so every field is reached. It also
// returns the leaves, in the order of the walk.
func fillBinding(alter string) (record.Binding, []leaf) {
	var b record.Binding
	var leaves []leaf
	n := 0
	var fill func(v reflect.Value, path string)
	fill = func(v reflect.Value, path string) {
		switch v.Kind() {
		case reflect.Pointer:
			v.Set(reflect.New(v.Type().Elem()))
			fill(v.Elem(), path)
		case reflect.Struct:
			for i := 0; i < v.NumField(); i++ {
				fill(v.Field(i), path+"."+v.Type().Field(i).Name)
			}
		case reflect.Slice:
			v.Set(reflect.MakeSlice(v.Type(), 2, 2))
			for i := 0; i < 2; i++ {
				fill(v.Index(i), path+"["+strconv.Itoa(i)+"]")
			}
		case reflect.String:
			n++
			leaves = append(leaves, leaf{path, reflect.String})
			v.SetString(leafString(path, path == alter))
		case reflect.Int64:
			n++
			leaves = append(leaves, leaf{path, reflect.Int64})
			value := int64(100000 + n)
			if path == alter {
				value += 500000
			}
			v.SetInt(value)
		case reflect.Bool:
			n++
			leaves = append(leaves, leaf{path, reflect.Bool})
			v.SetBool(path == alter)
		default:
			panic("the binding has a field of a kind this test does not know: " + path + " (" + v.Kind().String() + ")")
		}
	}
	fill(reflect.ValueOf(&b).Elem(), "Binding")
	return b, leaves
}

// leafString is the value of a string leaf. Two fields cannot hold any
// text: the verb chooses the card's shape, and a time must be one.
func leafString(path string, altered bool) string {
	switch path {
	case "Binding.Verb":
		if altered {
			return record.VerbBranchPrune
		}
		return record.VerbFeatureFinish
	case "Binding.RequestedAt", "Binding.PendingExpiresAt":
		if altered {
			return "2031-01-02T03:04:05Z"
		}
		if path == "Binding.RequestedAt" {
			return "2026-11-20T14:02:04Z"
		}
		return "2026-11-21T14:02:04Z"
	}
	if altered {
		return "other:" + path
	}
	return "value:" + path
}

// filledPending wraps a filled binding in a record. The record's own
// fields are fixed, so only the binding moves the model.
func filledPending(b record.Binding) record.Pending {
	return record.Pending{ID: "ap-filled", Binding: b, BindingSHA256: "the digest", PlanText: "the plan", Note: "the note"}
}

func TestEveryFieldOfTheBindingIsOnTheModel(t *testing.T) {
	base, leaves := fillBinding("")
	if len(leaves) < 50 {
		t.Fatalf("the walk found %d fields of the binding, want all of them", len(leaves))
	}
	baseDump := dump(build(filledPending(base)))
	values := make(map[string]bool, len(baseDump))
	for _, v := range baseDump {
		values[v] = true
	}

	for _, l := range leaves {
		altered, _ := fillBinding(l.path)
		diff := changed(baseDump, dump(build(filledPending(altered))))
		if len(diff) == 0 {
			t.Errorf("%s is under the binding's digest and not on the model: changing it changes nothing", l.path)
			continue
		}
		// And the value itself is there to read, not only something
		// made from it.
		v := reflect.ValueOf(base)
		switch l.kind {
		case reflect.String:
			want := leafString(l.path, false)
			if l.path == "Binding.RequestedAt" || l.path == "Binding.PendingExpiresAt" {
				if !values["time:"+want] {
					t.Errorf("%s: the time %s is nowhere on the model", l.path, want)
				}
				continue
			}
			found := false
			for value := range values {
				// The target is its ref without the prefix, and
				// a feature finish joins its argv, so the value
				// may stand inside a longer one.
				if strings.Contains(value, want) {
					found = true
				}
			}
			if !found {
				t.Errorf("%s: the text %q is nowhere on the model", l.path, want)
			}
		case reflect.Int64:
			want := "int:" + strconv.FormatInt(fieldAt(t, v, l.path).Int(), 10)
			if !values[want] {
				t.Errorf("%s: the number %s is nowhere on the model", l.path, want)
			}
		}
	}

	// A list with one element more shows one row more.
	for _, grow := range []func(b *record.Binding){
		func(b *record.Binding) { b.Effects = append(b.Effects, b.Effects[0]) },
		func(b *record.Binding) { b.Evidence = append(b.Evidence, b.Evidence[0]) },
		func(b *record.Binding) { b.Argv = append(b.Argv, "more") },
		func(b *record.Binding) { b.Merge.Parents = append(b.Merge.Parents, "more") },
		func(b *record.Binding) { b.Summary.Paths = append(b.Summary.Paths, b.Summary.Paths[0]) },
		func(b *record.Binding) { b.Summary.Flagged = append(b.Summary.Flagged, b.Summary.Flagged[0]) },
		func(b *record.Binding) { b.Summary.Binary = append(b.Summary.Binary, "more") },
		func(b *record.Binding) { b.Summary.Symlinks = append(b.Summary.Symlinks, "more") },
		func(b *record.Binding) { b.Summary.ModeChanges = append(b.Summary.ModeChanges, "more") },
		func(b *record.Binding) { b.Summary.Submodules = append(b.Summary.Submodules, "more") },
	} {
		grown, _ := fillBinding("")
		grow(&grown)
		if len(changed(baseDump, dump(build(filledPending(grown))))) == 0 {
			t.Error("a list of the binding grew and the model did not change")
		}
	}
	// The walk above grows every list the binding has: a list added to
	// the binding later must be added there as well.
	lists := 0
	var count func(typ reflect.Type)
	count = func(typ reflect.Type) {
		switch typ.Kind() {
		case reflect.Pointer:
			count(typ.Elem())
		case reflect.Slice:
			lists++
			count(typ.Elem())
		case reflect.Struct:
			for i := 0; i < typ.NumField(); i++ {
				count(typ.Field(i).Type)
			}
		}
	}
	count(reflect.TypeOf(record.Binding{}))
	if lists != 10 {
		t.Errorf("the binding has %d lists, and this test grows 10", lists)
	}
}

// fieldAt returns the field of v that a path of the walk names.
func fieldAt(t *testing.T, v reflect.Value, path string) reflect.Value {
	t.Helper()
	for _, part := range strings.Split(strings.TrimPrefix(path, "Binding."), ".") {
		name, index, indexed := strings.Cut(part, "[")
		for v.Kind() == reflect.Pointer {
			v = v.Elem()
		}
		v = v.FieldByName(name)
		if indexed {
			i, err := strconv.Atoi(strings.TrimSuffix(index, "]"))
			if err != nil {
				t.Fatalf("bad path %q", path)
			}
			v = v.Index(i)
		}
	}
	for v.Kind() == reflect.Pointer {
		v = v.Elem()
	}
	return v
}

// The second direction. The fields of the card are sorted into what the
// binding covers and the three exceptions, by name, so that a field added
// to the card later fails the test until it is sorted into one of them.
var (
	// boundFields are the card's sections that come from the binding.
	boundFields = []string{"Title", "Verb", "Plan", "Refs", "Merge", "Diff", "Lists", "Flagged", "Evidence", "BindingSHA256"}
	// exceptionFields are the fields beyond the binding, each with the
	// label a surface draws beside it.
	exceptionFields = map[string]string{
		"Note":     NoteLabel,
		"Differs":  DiffersLabel,
		"After":    AfterLabel,
		"Verdicts": AfterLabel,
		// The card's own record of a refused earlier card; it stands
		// where the block would.
		"DiffersRefused": DiffersLabel,
	}
)

func TestTheThreeExceptionsAreNamedAndLabelled(t *testing.T) {
	typ := reflect.TypeOf(Card{})
	sorted := make(map[string]bool)
	for _, name := range boundFields {
		sorted[name] = true
	}
	for name := range exceptionFields {
		if sorted[name] {
			t.Errorf("%s is sorted twice", name)
		}
		sorted[name] = true
	}
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		if !sorted[name] {
			t.Errorf("Card.%s is neither a section the binding covers nor one of the three exceptions", name)
		}
		delete(sorted, name)
	}
	for name := range sorted {
		t.Errorf("Card has no field %s", name)
	}

	// The labels are the words the surfaces share.
	for got, want := range map[string]string{
		NoteLabel:    "lead-stated, not checked",
		DiffersLabel: "from an earlier card",
		HeldLabel:    "decided past the limit",
		AfterLabel:   "after the decision",
	} {
		if got != want {
			t.Errorf("a label is %q, want %q", got, want)
		}
	}
	// The exceptions have the types the note fixes.
	for name, want := range map[string]reflect.Type{
		"Note":           reflect.TypeOf(LeadText{}),
		"Differs":        reflect.TypeOf(&Differs{}),
		"After":          reflect.TypeOf(After{}),
		"Verdicts":       reflect.TypeOf(&VerdictBlock{}),
		"DiffersRefused": reflect.TypeOf(""),
	} {
		f, _ := typ.FieldByName(name)
		if f.Type != want {
			t.Errorf("Card.%s is a %s, want a %s", name, f.Type, want)
		}
	}
	// After holds the state, the outcome line and the held mark, and
	// nothing else.
	var after []string
	for i := 0; i < reflect.TypeOf(After{}).NumField(); i++ {
		after = append(after, reflect.TypeOf(After{}).Field(i).Name)
	}
	if got := strings.Join(after, " "); got != "State Line Held" {
		t.Errorf("After has the fields %q", got)
	}
}

// boundPart keeps the entries of a dump that belong to the sections the
// binding covers.
func boundPart(d map[string]string) map[string]string {
	out := make(map[string]string)
	for path, value := range d {
		for _, name := range boundFields {
			if path == name || strings.HasPrefix(path, name+".") || strings.HasPrefix(path, name+"[") {
				out[path] = value
			}
		}
	}
	return out
}

// index matches the index of a list in a dump's path.
var index = regexp.MustCompile(`\[\d+\]`)

func TestNothingIsOnTheModelBeyondTheBinding(t *testing.T) {
	// What stands in the bound sections is made from the binding, the
	// plan text (which the binding's plan digest covers) and the record's
	// id (the card's name). Nothing else of the record or of the inputs
	// moves them, and each such thing moves its own exception only.
	p := validPending(t)
	base := dump(mustCard(t, p, Inputs{}))

	outside := []struct {
		name   string
		change func(p *record.Pending, in *Inputs)
		moves  []string // prefixes of the paths that may change
	}{
		{"the note", func(p *record.Pending, in *Inputs) { p.Note = "another note" }, []string{"Note"}},
		{"the planner", func(p *record.Pending, in *Inputs) { p.Planner = "planner-b" }, nil},
		{"the request id", func(p *record.Pending, in *Inputs) { p.RequestID = "rq-another" }, nil},
		{"the request digest", func(p *record.Pending, in *Inputs) { p.RequestSHA256 = record.Digest([]byte("another")) }, nil},
		{"the held mark", func(p *record.Pending, in *Inputs) { in.Held = true }, []string{"After.Held"}},
		{"the state", func(p *record.Pending, in *Inputs) { in.State = record.StateWithdrawn }, []string{"After.State"}},
		{"the outcome line", func(p *record.Pending, in *Inputs) { in.Line = "ended" }, []string{"After.Line"}},
		{"the verdicts", func(p *record.Pending, in *Inputs) {
			in.State = record.StateApplied
			in.Verdicts = &VerdictBlock{}
		}, []string{"After.State", "Verdicts"}},
		{"an earlier card", func(p *record.Pending, in *Inputs) {
			earlier := seal(t, laterPending())
			in.Earlier = &Earlier{Record: earlier, ReadAt: asked}
		}, []string{"Differs"}},
		{"an earlier card that is refused", func(p *record.Pending, in *Inputs) {
			earlier := rawPending()
			earlier.Schema = "other/1"
			in.Earlier = &Earlier{Record: seal(t, earlier), ReadAt: asked}
		}, []string{"DiffersRefused"}},
		{"the id", func(p *record.Pending, in *Inputs) { p.ID = "ap-another" }, []string{"Title.ID"}},
	}
	for _, tc := range outside {
		q, in := p, Inputs{}
		tc.change(&q, &in)
		got := dump(mustCard(t, q, in))
		for _, path := range changed(base, got) {
			allowed := false
			for _, prefix := range tc.moves {
				if path == prefix || strings.HasPrefix(path, prefix+".") || strings.HasPrefix(path, prefix+"[") {
					allowed = true
				}
			}
			if !allowed {
				t.Errorf("%s changed %s", tc.name, path)
			}
		}
		if len(tc.moves) > 0 && len(changed(base, got)) == 0 {
			t.Errorf("%s changed nothing on the card", tc.name)
		}
		if tc.name != "the id" && len(changed(boundPart(base), boundPart(got))) != 0 {
			t.Errorf("%s changed a section that the binding covers", tc.name)
		}
	}

	// And every value in the bound sections moves with some field of
	// the binding, with the plan text, with the id or with the digest: a
	// value that nothing of those moves would be content from nowhere.
	// Three values are made from their neighbours and never move on
	// their own; each has its own test.
	derived := map[string]string{
		"Lists[].Kind":     "the list's place among the four (TestTheFourListsAreShownEvenWhenEmpty)",
		"Flagged[].Reason": "the host's words for the flag's kind (TestFlagReasons)",
		"Refs[].Deletes":   "whether the new id is all zeros (TestPruneCard)",
	}
	filled, leaves := fillBinding("")
	fp := filledPending(filled)
	filledDump := dump(build(fp))
	moved := make(map[string]bool)
	note := func(d map[string]string) {
		for _, path := range changed(filledDump, d) {
			moved[index.ReplaceAllString(path, "[]")] = true
		}
	}
	for _, l := range leaves {
		altered, _ := fillBinding(l.path)
		note(dump(build(filledPending(altered))))
	}
	for _, change := range []func(p *record.Pending){
		func(p *record.Pending) { p.PlanText = "another plan\nof two lines" },
		func(p *record.Pending) { p.ID = "ap-another" },
		func(p *record.Pending) { p.BindingSHA256 = "another digest" },
	} {
		q := fp
		change(&q)
		note(dump(build(q)))
	}
	var paths []string
	for path := range boundPart(filledDump) {
		// The length of a list is not a value of its own; the lists
		// are grown in TestEveryFieldOfTheBindingIsOnTheModel.
		if !strings.HasSuffix(path, ".len") {
			paths = append(paths, index.ReplaceAllString(path, "[]"))
		}
	}
	sort.Strings(paths)
	for _, path := range paths {
		if _, ok := derived[path]; ok {
			if moved[path] {
				t.Errorf("%s moves with the binding, and this test calls it derived", path)
			}
			continue
		}
		if !moved[path] {
			t.Errorf("%s is on the model, and no field of the binding, the plan text or the id moves it", path)
		}
	}
	for path := range derived {
		found := false
		for _, have := range paths {
			if have == path {
				found = true
			}
		}
		if !found {
			t.Errorf("the derived value %s is not on the model", path)
		}
	}

	// The exceptions hold nothing of the binding that the bound sections
	// lack: a card without inputs has an empty After, no Differs and no
	// Verdicts, and its note is the record's note.
	c := mustCard(t, p, Inputs{})
	if c.After != (After{}) || c.Differs != nil || c.DiffersRefused != "" || c.Verdicts != nil || !c.Note.Equal(NewLeadText(string(p.Note))) {
		t.Errorf("a card without inputs has %+v, %v, %v", c.After, c.Differs, c.Verdicts)
	}
}
