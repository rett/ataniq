package ownerwire

import (
	"bytes"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"ataniq/internal/approval/record"
)

// The samples of seat.genesis. The thread id and the digests are fixed words
// of the vectors below.
const (
	genThread  = "0123456789abcdef"
	genCatalog = "5d41402abc4b2a76b9719d911017c5925d41402abc4b2a76b9719d911017c592"
	genBrief   = "7d793037a0760186574b0282f2f435e77d793037a0760186574b0282f2f435e7"
)

func sampleSeatGenesisParams() SeatGenesisParams {
	return SeatGenesisParams{Verb: GenesisVerbAnswer, Thread: genThread, Round: 2, Answers: []GenesisAnswer{
		{Node: "goal", Kind: GenesisKindChoice, IDs: []string{"share"}},
		{Node: "formats", Kind: GenesisKindChoices, IDs: []string{"plan", "web"}},
		{Node: "software.stack", Kind: GenesisKindOther, Text: "Go and a small database"},
	}}
}

func genesisNodes() []GenesisNode {
	return []GenesisNode{
		{ID: "goal", Type: GenesisTypeSingle, Prompt: "What is the project for?", Topic: "purpose",
			Options: []GenesisOption{
				{ID: "try", Label: "To try an idea", Example: "A weekend experiment"},
				{ID: "share", Label: "To share with others", Example: "A club booking page"},
			}, AllowOther: true, AllowYouDecide: true, CanSkip: true},
		{ID: "media.rights", Type: GenesisTypeYesNo, Prompt: "Do you hold the rights to everything you will use?", Topic: "purpose",
			Options: []GenesisOption{
				{ID: "yes", Label: "Yes", Example: "All of it is mine or licensed"},
				{ID: "no", Label: "No", Example: "Some of it belongs to others"},
			}},
		{ID: "schedule", Type: GenesisTypeScale, Prompt: "How soon do you need it?", Topic: "purpose", Options: []GenesisOption{}, CanSkip: true},
	}
}

func sampleSeatGenesisResult() SeatGenesisResult {
	return SeatGenesisResult{State: GenesisStateQuestion, Thread: genThread, Round: 2, Of: 6,
		Progress: GenesisProgress{Filled: 1, Total: 7}, Nodes: genesisNodes(), Reset: 0, CatalogDigest: genCatalog}
}

func genesisRows() *GenesisGroups {
	return &GenesisGroups{
		YouChose: []GenesisRow{
			{Node: "goal", Field: "goal", Label: "What is the project for?", Value: "To share with others"},
			{Node: "software.stack", Field: "software.stack", Label: "What will it be built with?", Value: "Go and a small database", Untrusted: true},
		},
		DecidedForYou: []GenesisRow{},
		Defaults:      []GenesisRow{{Node: "schedule", Field: "schedule", Label: "How soon do you need it?", Value: "No hurry"}},
	}
}

// The operation is the gate's alone, the last of the six, and its name has the
// form of the others.
func TestSeatGenesisIsAGateOperation(t *testing.T) {
	if OpSeatGenesis != "seat.genesis" {
		t.Fatalf("the operation is %q", OpSeatGenesis)
	}
	gate := GateOperations()
	if len(gate) != 6 || gate[5] != OpSeatGenesis {
		t.Fatalf("the gate operations are %v, want seat.genesis last of six", gate)
	}
	for _, op := range Operations() {
		if op == OpSeatGenesis {
			t.Errorf("%s is in Operations(), the list the Unix socket serves and announces", op)
		}
	}
	if !regexp.MustCompile(`^seat\.[a-z0-9-]+$`).MatchString(OpSeatGenesis) || strings.Count(OpSeatGenesis, ".") != 1 {
		t.Errorf("%q is not seat. and one part of lower-case letters, digits and dashes", OpSeatGenesis)
	}
}

// The six words are of the gate's grammar, each its own.
func TestGenesisCodesAreWords(t *testing.T) {
	codes := map[string]string{
		CodeGenesisBusy: "genesis-busy", CodeGenesisUnknownThread: "genesis-unknown-thread",
		CodeGenesisBadAnswer: "genesis-bad-answer", CodeGenesisOverCap: "genesis-over-cap",
		CodeGenesisSecretShaped: "genesis-secret-shaped", CodeGenesisDone: "genesis-done",
	}
	if len(codes) != 6 {
		t.Fatalf("%d codes with different words, want 6", len(codes))
	}
	grammar := regexp.MustCompile(`^[a-z0-9-]{1,40}$`)
	for code, word := range codes {
		if code != word || !grammar.MatchString(code) {
			t.Errorf("the code %s is written %q", word, code)
		}
	}
	// None is another word of the table: not a record code, nor a gate word.
	others := []string{CodeChallengeStale, CodeChallengesFull, CodeAssertionInvalid, CodeKeyRevoked, CodeKindRefused, CodeWaitFull}
	for _, o := range others {
		if _, ok := codes[o]; ok {
			t.Errorf("%q is both a genesis code and another gate code", o)
		}
	}
}

// The bounds are the ones the plan fixes.
func TestGenesisBoundsAreThePlans(t *testing.T) {
	got := []int{MaxGenesisThreads, MaxGenesisRounds, MaxGenesisNodes, MaxGenesisAnswers, MaxGenesisOptions, MaxGenesisRows,
		MaxGenesisPrompt, MaxGenesisLabel, MaxGenesisExample, MaxGenesisOther, GenesisThreadLength, MaxSeatGenesisResultBytes}
	want := []int{2, 6, 3, 3, 8, 64, 160, 60, 100, 200, 16, 16384}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("the bounds are %v, want %v", got, want)
	}
}

// vector holds one fixed spelling of a payload: it decodes to the value, and
// the value encodes to exactly those bytes.
func vector[T any](t *testing.T, name, js string, want T, dec func([]byte) (T, error), enc func(T) ([]byte, error)) {
	t.Helper()
	got, err := dec([]byte(js))
	if err != nil {
		t.Errorf("%s: the vector is refused: %v", name, err)
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s: the vector decodes to %+v, want %+v", name, got, want)
	}
	out, err := enc(want)
	if err != nil {
		t.Errorf("%s: the value does not encode: %v", name, err)
		return
	}
	if string(out) != js {
		t.Errorf("%s: the value encodes to\n%s\nwant\n%s", name, out, js)
	}
}

// The params of each of the seven verbs, as the one canonical spelling.
func TestSeatGenesisParamsVectors(t *testing.T) {
	dec, enc := DecodeSeatGenesisParams, EncodeSeatGenesisParams
	vector(t, "start", `{"verb":"start"}`, SeatGenesisParams{Verb: GenesisVerbStart}, dec, enc)
	for _, verb := range []string{GenesisVerbRound, GenesisVerbSummary, GenesisVerbFinish, GenesisVerbDiscard} {
		vector(t, verb, `{"thread":"`+genThread+`","verb":"`+verb+`"}`, SeatGenesisParams{Verb: verb, Thread: genThread}, dec, enc)
	}
	vector(t, "answer", `{"answers":[`+
		`{"ids":["share"],"kind":"choice","node":"goal"},`+
		`{"ids":["plan","web"],"kind":"choices","node":"formats"},`+
		`{"kind":"other","node":"software.stack","text":"Go and a small database"}],`+
		`"round":2,"thread":"`+genThread+`","verb":"answer"}`, sampleSeatGenesisParams(), dec, enc)
	vector(t, "answer, you decide and skip", `{"answers":[`+
		`{"kind":"you_decide","node":"hosts"},{"kind":"skip","node":"schedule"}],`+
		`"round":6,"thread":"`+genThread+`","verb":"answer"}`, SeatGenesisParams{Verb: GenesisVerbAnswer, Thread: genThread, Round: 6,
		Answers: []GenesisAnswer{{Node: "hosts", Kind: GenesisKindYouDecide}, {Node: "schedule", Kind: GenesisKindSkip}}}, dec, enc)
	vector(t, "change", `{"answer":{"ids":["try"],"kind":"choice"},"node":"goal","thread":"`+genThread+`","verb":"change"}`,
		SeatGenesisParams{Verb: GenesisVerbChange, Thread: genThread, Node: "goal", Answer: &GenesisPick{Kind: GenesisKindChoice, IDs: []string{"try"}}}, dec, enc)
	vector(t, "change to other", `{"answer":{"kind":"other","text":"a family cookbook"},"node":"goal","thread":"`+genThread+`","verb":"change"}`,
		SeatGenesisParams{Verb: GenesisVerbChange, Thread: genThread, Node: "goal", Answer: &GenesisPick{Kind: GenesisKindOther, Text: "a family cookbook"}}, dec, enc)
}

// The result of each of the four states, as the one canonical spelling.
func TestSeatGenesisResultVectors(t *testing.T) {
	dec, enc := DecodeSeatGenesisResult, EncodeSeatGenesisResult
	vector(t, "question", `{"catalog_digest":"`+genCatalog+`","nodes":[`+
		`{"allow_other":true,"allow_you_decide":true,"can_skip":true,"id":"goal","options":[`+
		`{"example":"A weekend experiment","id":"try","label":"To try an idea"},`+
		`{"example":"A club booking page","id":"share","label":"To share with others"}],`+
		`"prompt":"What is the project for?","topic":"purpose","type":"single"},`+
		`{"allow_other":false,"allow_you_decide":false,"can_skip":false,"id":"media.rights","options":[`+
		`{"example":"All of it is mine or licensed","id":"yes","label":"Yes"},`+
		`{"example":"Some of it belongs to others","id":"no","label":"No"}],`+
		`"prompt":"Do you hold the rights to everything you will use?","topic":"purpose","type":"yesno"},`+
		`{"allow_other":false,"allow_you_decide":false,"can_skip":true,"id":"schedule","options":[],`+
		`"prompt":"How soon do you need it?","topic":"purpose","type":"scale"}],`+
		`"of":6,"progress":{"filled":1,"total":7},"reset":0,"round":2,"state":"question","thread":"`+genThread+`"}`,
		sampleSeatGenesisResult(), dec, enc)

	groups := `{"decided_for_you":[],"defaults":[{"field":"schedule","label":"How soon do you need it?","node":"schedule","untrusted":false,"value":"No hurry"}],` +
		`"you_chose":[{"field":"goal","label":"What is the project for?","node":"goal","untrusted":false,"value":"To share with others"},` +
		`{"field":"software.stack","label":"What will it be built with?","node":"software.stack","untrusted":true,"value":"Go and a small database"}]}`
	summary := SeatGenesisResult{State: GenesisStateSummary, Thread: genThread, Round: 3, Of: 6, Progress: GenesisProgress{Filled: 3, Total: 7},
		Nodes: []GenesisNode{}, Groups: genesisRows(), Reset: 2, CatalogDigest: genCatalog}
	vector(t, "summary", `{"catalog_digest":"`+genCatalog+`","groups":`+groups+`,"nodes":[],"of":6,"progress":{"filled":3,"total":7},"reset":2,"round":3,"state":"summary","thread":"`+genThread+`"}`,
		summary, dec, enc)

	finished := summary
	finished.State, finished.Reset, finished.Digest = GenesisStateFinished, 0, genBrief
	vector(t, "finished", `{"catalog_digest":"`+genCatalog+`","digest":"`+genBrief+`","groups":`+groups+`,"nodes":[],"of":6,"progress":{"filled":3,"total":7},"reset":0,"round":3,"state":"finished","thread":"`+genThread+`"}`,
		finished, dec, enc)

	discarded := SeatGenesisResult{State: GenesisStateDiscarded, Thread: genThread, Of: 6, Nodes: []GenesisNode{}, CatalogDigest: genCatalog}
	vector(t, "discarded", `{"catalog_digest":"`+genCatalog+`","nodes":[],"of":6,"progress":{"filled":0,"total":0},"reset":0,"round":0,"state":"discarded","thread":"`+genThread+`"}`,
		discarded, dec, enc)
}

// A verb takes exactly its members: each member of another verb is refused,
// and each member of its own is required.
func TestSeatGenesisParamsAreAClosedUnion(t *testing.T) {
	answers := sampleSeatGenesisParams().Answers
	pick := &GenesisPick{Kind: GenesisKindSkip}
	type members struct {
		thread, round, answers, node, answer bool
	}
	takes := map[string]members{
		GenesisVerbStart:   {},
		GenesisVerbRound:   {thread: true},
		GenesisVerbAnswer:  {thread: true, round: true, answers: true},
		GenesisVerbChange:  {thread: true, node: true, answer: true},
		GenesisVerbSummary: {thread: true},
		GenesisVerbFinish:  {thread: true},
		GenesisVerbDiscard: {thread: true},
	}
	build := func(verb string, m members) SeatGenesisParams {
		p := SeatGenesisParams{Verb: verb}
		if m.thread {
			p.Thread = genThread
		}
		if m.round {
			p.Round = 1
		}
		if m.answers {
			p.Answers = answers
		}
		if m.node {
			p.Node = "goal"
		}
		if m.answer {
			p.Answer = pick
		}
		return p
	}
	for verb, m := range takes {
		if _, err := EncodeSeatGenesisParams(build(verb, m)); err != nil {
			t.Errorf("%s: its own members are refused: %v", verb, err)
		}
		// One member short.
		for _, short := range []struct {
			name string
			m    members
		}{
			{"thread", members{false, m.round, m.answers, m.node, m.answer}},
			{"round", members{m.thread, false, m.answers, m.node, m.answer}},
			{"answers", members{m.thread, m.round, false, m.node, m.answer}},
			{"node", members{m.thread, m.round, m.answers, false, m.answer}},
			{"answer", members{m.thread, m.round, m.answers, m.node, false}},
		} {
			if short.m == m {
				continue // the verb does not take it
			}
			_, err := EncodeSeatGenesisParams(build(verb, short.m))
			refused(t, err, record.CodeRecordInvalid, verb+" without "+short.name)
		}
		// One member too many.
		for _, extra := range []struct {
			name string
			m    members
		}{
			{"thread", members{true, m.round, m.answers, m.node, m.answer}},
			{"round", members{m.thread, true, m.answers, m.node, m.answer}},
			{"answers", members{m.thread, m.round, true, m.node, m.answer}},
			{"node", members{m.thread, m.round, m.answers, true, m.answer}},
			{"answer", members{m.thread, m.round, m.answers, m.node, true}},
		} {
			if extra.m == m {
				continue // the verb takes it
			}
			_, err := EncodeSeatGenesisParams(build(verb, extra.m))
			refused(t, err, record.CodeRecordInvalid, verb+" with "+extra.name)
		}
	}
	// An empty list is not a missing one, and a verb is a closed word.
	_, err := EncodeSeatGenesisParams(SeatGenesisParams{Verb: GenesisVerbRound, Thread: genThread, Answers: []GenesisAnswer{}})
	refused(t, err, record.CodeRecordInvalid, "an empty list of answers for a round")
	for _, verb := range []string{"", "Start", "begin", "start ", "answers", "answer\n", "restart", "decide", "approve"} {
		_, err := EncodeSeatGenesisParams(SeatGenesisParams{Verb: verb})
		refused(t, err, record.CodeRecordInvalid, "the verb "+strconv.Quote(verb))
		_, err = DecodeSeatGenesisParams([]byte(`{"verb":` + strconv.Quote(verb) + `}`))
		refused(t, err, record.CodeRecordInvalid, "decoding the verb "+strconv.Quote(verb))
	}
	// The decoder refuses the same, from the JSON.
	for name, in := range map[string]string{
		"start with a thread":       `{"thread":"` + genThread + `","verb":"start"}`,
		"round without a thread":    `{"verb":"round"}`,
		"round with a round":        `{"round":1,"thread":"` + genThread + `","verb":"round"}`,
		"a thread that is null":     `{"thread":null,"verb":"round"}`,
		"an empty thread":           `{"thread":"","verb":"round"}`,
		"summary with a node":       `{"node":"goal","thread":"` + genThread + `","verb":"summary"}`,
		"finish with answers":       `{"answers":[],"thread":"` + genThread + `","verb":"finish"}`,
		"discard with an answer":    `{"answer":{"kind":"skip"},"thread":"` + genThread + `","verb":"discard"}`,
		"change without an answer":  `{"node":"goal","thread":"` + genThread + `","verb":"change"}`,
		"change with a node in it":  `{"answer":{"kind":"skip","node":"goal"},"node":"goal","thread":"` + genThread + `","verb":"change"}`,
		"answer without a round":    `{"answers":[{"kind":"skip","node":"goal"}],"thread":"` + genThread + `","verb":"answer"}`,
		"answer with round zero":    `{"answers":[{"kind":"skip","node":"goal"}],"round":0,"thread":"` + genThread + `","verb":"answer"}`,
		"an answer that is a list":  `{"answer":[],"node":"goal","thread":"` + genThread + `","verb":"change"}`,
		"no verb":                   `{"thread":"` + genThread + `"}`,
		"a verb that is a number":   `{"verb":1}`,
		"two verbs":                 `{"verb":"start","verb":"start"}`,
		"a member the form lacks":   `{"verb":"start","kind":"seat"}`,
		"a member in capitals":      `{"Verb":"start"}`,
		"a nested member of a pick": `{"answer":{"kind":"skip","extra":1},"node":"goal","thread":"` + genThread + `","verb":"change"}`,
	} {
		_, err := DecodeSeatGenesisParams([]byte(in))
		refused(t, err, record.CodeRecordInvalid, name)
	}
}

// What an answer may hold, kind by kind.
func TestSeatGenesisAnswersAreHeldToTheirKind(t *testing.T) {
	long := strings.Repeat("a", MaxGenesisOther+1)
	nine := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"}
	cases := map[string]GenesisPick{
		"a choice without an id":          {Kind: GenesisKindChoice},
		"a choice with two ids":           {Kind: GenesisKindChoice, IDs: []string{"a", "b"}},
		"a choice with text":              {Kind: GenesisKindChoice, IDs: []string{"a"}, Text: "x"},
		"choices without an id":           {Kind: GenesisKindChoices},
		"choices with nine ids":           {Kind: GenesisKindChoices, IDs: nine},
		"choices with an id twice":        {Kind: GenesisKindChoices, IDs: []string{"a", "a"}},
		"choices with text":               {Kind: GenesisKindChoices, IDs: []string{"a"}, Text: "x"},
		"an id in capitals":               {Kind: GenesisKindChoice, IDs: []string{"Share"}},
		"an id with a space":              {Kind: GenesisKindChoice, IDs: []string{"a b"}},
		"an id that starts with a dash":   {Kind: GenesisKindChoice, IDs: []string{"-a"}},
		"an id that starts with a score":  {Kind: GenesisKindChoice, IDs: []string{"_a"}},
		"an empty id":                     {Kind: GenesisKindChoice, IDs: []string{""}},
		"an id of 33 bytes":               {Kind: GenesisKindChoice, IDs: []string{strings.Repeat("a", 33)}},
		"an id with a dot":                {Kind: GenesisKindChoice, IDs: []string{"a.b"}},
		"an id that is not ASCII":         {Kind: GenesisKindChoice, IDs: []string{"café"}},
		"other without text":              {Kind: GenesisKindOther},
		"other with blank text":           {Kind: GenesisKindOther, Text: " \t "},
		"other over 200 bytes":            {Kind: GenesisKindOther, Text: long},
		"other with ids":                  {Kind: GenesisKindOther, Text: "x", IDs: []string{"a"}},
		"you decide with an id":           {Kind: GenesisKindYouDecide, IDs: []string{"a"}},
		"you decide with text":            {Kind: GenesisKindYouDecide, Text: "x"},
		"skip with an id":                 {Kind: GenesisKindSkip, IDs: []string{"a"}},
		"skip with text":                  {Kind: GenesisKindSkip, Text: "x"},
		"no kind":                         {},
		"a kind in capitals":              {Kind: "Skip"},
		"a kind of the verbs":             {Kind: "answer"},
		"a kind that is not one":          {Kind: "approve"},
		"an empty list of ids for a skip": {Kind: GenesisKindSkip, IDs: []string{}},
	}
	for name, pick := range cases {
		p := pick
		_, err := EncodeSeatGenesisParams(SeatGenesisParams{Verb: GenesisVerbChange, Thread: genThread, Node: "goal", Answer: &p})
		refused(t, err, record.CodeRecordInvalid, "a change to "+name)
		_, err = EncodeSeatGenesisParams(SeatGenesisParams{Verb: GenesisVerbAnswer, Thread: genThread, Round: 1,
			Answers: []GenesisAnswer{{Node: "goal", Kind: p.Kind, IDs: p.IDs, Text: p.Text}}})
		refused(t, err, record.CodeRecordInvalid, "an answer of "+name)
	}
	// The longest of each kind is fine.
	for name, pick := range map[string]GenesisPick{
		"eight ids":               {Kind: GenesisKindChoices, IDs: []string{"a", "b", "c", "d", "e", "f", "g", "h"}},
		"an id of 32 bytes":       {Kind: GenesisKindChoice, IDs: []string{"a" + strings.Repeat("-_9", 10) + "z"}},
		"a scale step":            {Kind: GenesisKindChoice, IDs: []string{"5"}},
		"200 bytes of text":       {Kind: GenesisKindOther, Text: strings.Repeat("é", 100)},
		"text with quotes and \\": {Kind: GenesisKindOther, Text: `say "hi" \ <b>`},
		"text that is no secret":  {Kind: GenesisKindOther, Text: "https://example.org/a-b"},
	} {
		p := pick
		if _, err := EncodeSeatGenesisParams(SeatGenesisParams{Verb: GenesisVerbChange, Thread: genThread, Node: "goal", Answer: &p}); err != nil {
			t.Errorf("%s: refused: %v", name, err)
		}
	}
	// Answers: one to three, each to another node, with node ids of the grammar.
	one := GenesisAnswer{Node: "goal", Kind: GenesisKindSkip}
	two := GenesisAnswer{Node: "hosts", Kind: GenesisKindSkip}
	three := GenesisAnswer{Node: "schedule", Kind: GenesisKindSkip}
	four := GenesisAnswer{Node: "formats", Kind: GenesisKindSkip}
	for name, answers := range map[string][]GenesisAnswer{
		"none":                {},
		"four":                {one, two, three, four},
		"a node twice":        {one, one},
		"a node twice, apart": {one, two, one},
		"no node":             {{Kind: GenesisKindSkip}},
		"a node in capitals":  {{Node: "Goal", Kind: GenesisKindSkip}},
		"a node with a dash":  {{Node: "go-al", Kind: GenesisKindSkip}},
		"a node of 49 bytes":  {{Node: strings.Repeat("a", 49), Kind: GenesisKindSkip}},
		"a node of a digit":   {{Node: "1goal", Kind: GenesisKindSkip}},
	} {
		_, err := EncodeSeatGenesisParams(SeatGenesisParams{Verb: GenesisVerbAnswer, Thread: genThread, Round: 1, Answers: answers})
		refused(t, err, record.CodeRecordInvalid, "answers: "+name)
	}
	for name, answers := range map[string][]GenesisAnswer{
		"one": {one}, "three": {one, two, three}, "a node with dots": {{Node: "media.format.podcast", Kind: GenesisKindSkip}},
		"a node of 48 bytes": {{Node: strings.Repeat("a", 48), Kind: GenesisKindSkip}},
	} {
		if _, err := EncodeSeatGenesisParams(SeatGenesisParams{Verb: GenesisVerbAnswer, Thread: genThread, Round: 6, Answers: answers}); err != nil {
			t.Errorf("answers: %s: refused: %v", name, err)
		}
	}
	// The thread and the round.
	for name, thread := range map[string]string{"short": genThread[:15], "long": genThread + "0", "capitals": strings.ToUpper("abcdef0123456789"),
		"not hex": "0123456789abcdeg", "a space": " 123456789abcdef", "empty": ""} {
		_, err := EncodeSeatGenesisParams(SeatGenesisParams{Verb: GenesisVerbRound, Thread: thread})
		refused(t, err, record.CodeRecordInvalid, "a thread id: "+name)
	}
	for _, round := range []int{-1, 0, 7, 100} {
		p := sampleSeatGenesisParams()
		p.Round = round
		_, err := EncodeSeatGenesisParams(p)
		refused(t, err, record.CodeRecordInvalid, "round "+strconv.Itoa(round))
	}
}

// Text the owner gave is read as text: the codec refuses what is not plain,
// and an escape makes no difference.
func TestSeatGenesisTextIsPlain(t *testing.T) {
	base := `{"answer":{"kind":"other","text":"%s"},"node":"goal","thread":"` + genThread + `","verb":"change"}`
	in := func(text string) []byte { return []byte(strings.Replace(base, "%s", text, 1)) }
	for name, text := range map[string]string{"a control character": `a\u0007b`, "a newline": `a\nb`, "a tab": `a\tb`, "a lone surrogate": `\ud800`, "a NUL": `\u0000`} {
		_, err := DecodeSeatGenesisParams(in(text))
		refused(t, err, record.CodeRecordInvalid, name)
	}
	_, err := DecodeSeatGenesisParams(append(in("a"), 0xff))
	refused(t, err, record.CodeRecordInvalid, "bytes that are not UTF-8")
	_, err = DecodeSeatGenesisParams([]byte(strings.Replace(string(in("a")), `"a"`, "\"\xff\"", 1)))
	refused(t, err, record.CodeRecordInvalid, "text that is not UTF-8")
	p, err := DecodeSeatGenesisParams(in(`café \"q\" \\`))
	if err != nil || p.Answer.Text != "café \"q\" \\" {
		t.Errorf("an escaped text reads as %+v (%v)", p.Answer, err)
	}
}

// What a result may hold, state by state.
func TestSeatGenesisResultIsAClosedUnion(t *testing.T) {
	summary := func() SeatGenesisResult {
		return SeatGenesisResult{State: GenesisStateSummary, Thread: genThread, Round: 3, Of: 6, Progress: GenesisProgress{Filled: 3, Total: 7},
			Nodes: []GenesisNode{}, Groups: genesisRows(), Reset: 2, CatalogDigest: genCatalog}
	}
	finished := func() SeatGenesisResult {
		r := summary()
		r.State, r.Reset, r.Digest = GenesisStateFinished, 0, genBrief
		return r
	}
	discarded := func() SeatGenesisResult {
		return SeatGenesisResult{State: GenesisStateDiscarded, Thread: genThread, Of: 6, Nodes: []GenesisNode{}, CatalogDigest: genCatalog}
	}
	for name, r := range map[string]SeatGenesisResult{"question": sampleSeatGenesisResult(), "summary": summary(), "finished": finished(), "discarded": discarded()} {
		if _, err := EncodeSeatGenesisResult(r); err != nil {
			t.Fatalf("the %s sample is refused: %v", name, err)
		}
	}
	rows := func(n int) []GenesisRow {
		out := make([]GenesisRow, n)
		for i := range out {
			out[i] = GenesisRow{Node: "goal", Field: "software.f" + strconv.Itoa(i), Label: "Q", Value: "V"}
		}
		return out
	}
	str := strings.Repeat
	cases := map[string]SeatGenesisResult{}
	add := func(name string, base SeatGenesisResult, change func(*SeatGenesisResult)) {
		change(&base)
		cases[name] = base
	}
	q := sampleSeatGenesisResult
	// The members every state has.
	for _, base := range []struct {
		name string
		r    SeatGenesisResult
	}{{"question", q()}, {"summary", summary()}, {"finished", finished()}, {"discarded", discarded()}} {
		add(base.name+": no state", base.r, func(r *SeatGenesisResult) { r.State = "" })
		add(base.name+": a thread in capitals", base.r, func(r *SeatGenesisResult) { r.Thread = strings.ToUpper("abcdef0123456789") })
		add(base.name+": a short thread", base.r, func(r *SeatGenesisResult) { r.Thread = genThread[:8] })
		add(base.name+": no catalog digest", base.r, func(r *SeatGenesisResult) { r.CatalogDigest = "" })
		add(base.name+": a short catalog digest", base.r, func(r *SeatGenesisResult) { r.CatalogDigest = genCatalog[:63] })
		add(base.name+": a catalog digest in capitals", base.r, func(r *SeatGenesisResult) { r.CatalogDigest = strings.ToUpper(genCatalog) })
		add(base.name+": of zero", base.r, func(r *SeatGenesisResult) { r.Of = 0 })
		add(base.name+": of seven", base.r, func(r *SeatGenesisResult) { r.Of = 7 })
		add(base.name+": filled over total", base.r, func(r *SeatGenesisResult) { r.Progress = GenesisProgress{Filled: 4, Total: 3} })
		add(base.name+": a negative fill", base.r, func(r *SeatGenesisResult) { r.Progress = GenesisProgress{Filled: -1, Total: 3} })
		add(base.name+": a total over 64", base.r, func(r *SeatGenesisResult) { r.Progress = GenesisProgress{Filled: 1, Total: 65} })
		add(base.name+": a negative reset", base.r, func(r *SeatGenesisResult) { r.Reset = -1 })
		add(base.name+": a round over of", base.r, func(r *SeatGenesisResult) { r.Round = r.Of + 1 })
		add(base.name+": a negative round", base.r, func(r *SeatGenesisResult) { r.Round = -1 })
	}
	// A question.
	add("question: round zero", q(), func(r *SeatGenesisResult) { r.Round = 0 })
	add("question: no nodes", q(), func(r *SeatGenesisResult) { r.Nodes = []GenesisNode{} })
	add("question: four nodes", q(), func(r *SeatGenesisResult) {
		r.Nodes = append(r.Nodes, GenesisNode{ID: "hosts", Type: GenesisTypeScale, Prompt: "p", Topic: "t", Options: []GenesisOption{}})
	})
	add("question: a node twice", q(), func(r *SeatGenesisResult) { r.Nodes[1].ID = r.Nodes[0].ID })
	add("question: groups", q(), func(r *SeatGenesisResult) { r.Groups = genesisRows() })
	add("question: a digest", q(), func(r *SeatGenesisResult) { r.Digest = genBrief })
	add("question: a node id in capitals", q(), func(r *SeatGenesisResult) { r.Nodes[0].ID = "Goal" })
	add("question: no node id", q(), func(r *SeatGenesisResult) { r.Nodes[0].ID = "" })
	add("question: a type that is not one", q(), func(r *SeatGenesisResult) { r.Nodes[0].Type = "text" })
	add("question: a type in capitals", q(), func(r *SeatGenesisResult) { r.Nodes[0].Type = "Single" })
	add("question: no prompt", q(), func(r *SeatGenesisResult) { r.Nodes[0].Prompt = "" })
	add("question: a blank prompt", q(), func(r *SeatGenesisResult) { r.Nodes[0].Prompt = "  " })
	add("question: a prompt of 161 bytes", q(), func(r *SeatGenesisResult) { r.Nodes[0].Prompt = str("p", 161) })
	add("question: no topic", q(), func(r *SeatGenesisResult) { r.Nodes[0].Topic = "" })
	add("question: a topic of 17 bytes", q(), func(r *SeatGenesisResult) { r.Nodes[0].Topic = str("t", 17) })
	add("question: a topic with a capital", q(), func(r *SeatGenesisResult) { r.Nodes[0].Topic = "Purpose" })
	add("question: one option for a single choice", q(), func(r *SeatGenesisResult) { r.Nodes[0].Options = r.Nodes[0].Options[:1] })
	add("question: nine options", q(), func(r *SeatGenesisResult) {
		r.Nodes[0].Options = nil
		for i := 0; i < 9; i++ {
			r.Nodes[0].Options = append(r.Nodes[0].Options, GenesisOption{ID: "o" + strconv.Itoa(i), Label: "l", Example: "e"})
		}
	})
	add("question: three options for a yes-or-no", q(), func(r *SeatGenesisResult) {
		r.Nodes[1].Options = append(r.Nodes[1].Options, GenesisOption{ID: "maybe", Label: "l", Example: "e"})
	})
	add("question: options for a scale", q(), func(r *SeatGenesisResult) { r.Nodes[2].Options = genesisNodes()[0].Options })
	add("question: an option twice", q(), func(r *SeatGenesisResult) { r.Nodes[0].Options[1].ID = r.Nodes[0].Options[0].ID })
	add("question: an option id in capitals", q(), func(r *SeatGenesisResult) { r.Nodes[0].Options[0].ID = "Try" })
	add("question: no label", q(), func(r *SeatGenesisResult) { r.Nodes[0].Options[0].Label = "" })
	add("question: a label of 61 bytes", q(), func(r *SeatGenesisResult) { r.Nodes[0].Options[0].Label = str("l", 61) })
	add("question: no example", q(), func(r *SeatGenesisResult) { r.Nodes[0].Options[0].Example = "" })
	add("question: an example of 101 bytes", q(), func(r *SeatGenesisResult) { r.Nodes[0].Options[0].Example = str("e", 101) })
	// A summary and a finished thread.
	add("summary: no groups", summary(), func(r *SeatGenesisResult) { r.Groups = nil })
	add("summary: nodes", summary(), func(r *SeatGenesisResult) { r.Nodes = genesisNodes()[:1] })
	add("summary: a digest", summary(), func(r *SeatGenesisResult) { r.Digest = genBrief })
	add("summary: a field twice, in two groups", summary(), func(r *SeatGenesisResult) {
		r.Groups.DecidedForYou = []GenesisRow{r.Groups.YouChose[0]}
	})
	add("summary: a field twice, in a group", summary(), func(r *SeatGenesisResult) {
		r.Groups.YouChose = append(r.Groups.YouChose, r.Groups.YouChose[0])
	})
	add("summary: 65 rows", summary(), func(r *SeatGenesisResult) {
		r.Groups = &GenesisGroups{YouChose: rows(65), DecidedForYou: []GenesisRow{}, Defaults: []GenesisRow{}}
	})
	add("summary: 65 rows in three groups", summary(), func(r *SeatGenesisResult) {
		more := rows(65)
		r.Groups = &GenesisGroups{YouChose: more[:30], DecidedForYou: more[30:55], Defaults: more[55:]}
	})
	add("summary: a row without a node", summary(), func(r *SeatGenesisResult) { r.Groups.YouChose[0].Node = "" })
	add("summary: a row field that is not one", summary(), func(r *SeatGenesisResult) { r.Groups.YouChose[0].Field = "colour" })
	add("summary: a row field of another domain", summary(), func(r *SeatGenesisResult) { r.Groups.YouChose[0].Field = "music.key" })
	add("summary: a row field in capitals", summary(), func(r *SeatGenesisResult) { r.Groups.YouChose[0].Field = "Goal" })
	add("summary: a row field of 42 bytes", summary(), func(r *SeatGenesisResult) { r.Groups.YouChose[0].Field = "software." + str("a", 33) })
	add("summary: a row field with a digit first", summary(), func(r *SeatGenesisResult) { r.Groups.YouChose[0].Field = "software.1a" })
	add("summary: a row field with two dots", summary(), func(r *SeatGenesisResult) { r.Groups.YouChose[0].Field = "software.a.b" })
	add("summary: a row without a label", summary(), func(r *SeatGenesisResult) { r.Groups.YouChose[0].Label = "" })
	add("summary: a row label of 161 bytes", summary(), func(r *SeatGenesisResult) { r.Groups.YouChose[0].Label = str("l", 161) })
	add("summary: a row without a value", summary(), func(r *SeatGenesisResult) { r.Groups.YouChose[0].Value = "" })
	add("summary: a row value of 513 bytes", summary(), func(r *SeatGenesisResult) { r.Groups.YouChose[0].Value = str("v", 513) })
	add("summary: an untrusted value of 201 bytes", summary(), func(r *SeatGenesisResult) {
		r.Groups.YouChose[1].Value = str("v", MaxGenesisOther+1)
	})
	add("finished: no digest", finished(), func(r *SeatGenesisResult) { r.Digest = "" })
	add("finished: a short digest", finished(), func(r *SeatGenesisResult) { r.Digest = genBrief[:63] })
	add("finished: a digest in capitals", finished(), func(r *SeatGenesisResult) { r.Digest = strings.ToUpper(genBrief) })
	add("finished: no groups", finished(), func(r *SeatGenesisResult) { r.Groups = nil })
	add("finished: a reset", finished(), func(r *SeatGenesisResult) { r.Reset = 1 })
	add("finished: nodes", finished(), func(r *SeatGenesisResult) { r.Nodes = genesisNodes()[:1] })
	// A discarded thread.
	add("discarded: a round", discarded(), func(r *SeatGenesisResult) { r.Round = 1 })
	add("discarded: progress", discarded(), func(r *SeatGenesisResult) { r.Progress = GenesisProgress{Filled: 1, Total: 2} })
	add("discarded: groups", discarded(), func(r *SeatGenesisResult) { r.Groups = genesisRows() })
	add("discarded: a digest", discarded(), func(r *SeatGenesisResult) { r.Digest = genBrief })
	add("discarded: a reset", discarded(), func(r *SeatGenesisResult) { r.Reset = 1 })
	add("discarded: nodes", discarded(), func(r *SeatGenesisResult) { r.Nodes = genesisNodes()[:1] })
	for name, r := range cases {
		_, err := EncodeSeatGenesisResult(r)
		refused(t, err, record.CodeRecordInvalid, "result: "+name)
	}
	// The members are all required but groups and digest.
	sample, _ := EncodeSeatGenesisResult(finished())
	for _, key := range []string{`"state"`, `"thread"`, `"round"`, `"of"`, `"progress"`, `"filled"`, `"total"`, `"nodes"`, `"reset"`, `"catalog_digest"`,
		`"you_chose"`, `"decided_for_you"`, `"defaults"`, `"untrusted"`, `"field"`, `"label"`, `"value"`} {
		in := bytes.Replace(sample, []byte(key), []byte(`"x`+key[1:]), 1)
		_, err := DecodeSeatGenesisResult(in)
		refused(t, err, record.CodeRecordInvalid, "result without "+key)
	}
	// A result with another member is refused, at every level.
	for name, in := range map[string]string{
		"a member of the result": `"extra":1,`, "a member of a row": `"node":"goal","extra":1,`,
	} {
		out := strings.Replace(string(sample), `{"field"`, `{`+strings.Replace(in, `"node":"goal",`, "", 1)+`"field"`, 1)
		if name == "a member of the result" {
			out = `{` + in + string(sample[1:])
		}
		_, err := DecodeSeatGenesisResult([]byte(out))
		refused(t, err, record.CodeRecordInvalid, name)
	}
}

// The caps hold the largest payloads, and the result cap is the binding one.
func TestSeatGenesisCapsHoldTheLargestPayloads(t *testing.T) {
	// The most an answer call holds: three answers, each of the largest kind.
	ids := make([]string, MaxGenesisOptions)
	for i := range ids {
		ids[i] = strings.Repeat("a", MaxGenesisChoiceBytes-1) + strconv.Itoa(i)
	}
	p := SeatGenesisParams{Verb: GenesisVerbAnswer, Thread: genThread, Round: MaxGenesisRounds, Answers: []GenesisAnswer{
		{Node: strings.Repeat("n", MaxGenesisNodeBytes), Kind: GenesisKindChoices, IDs: ids},
		{Node: strings.Repeat("m", MaxGenesisNodeBytes), Kind: GenesisKindChoices, IDs: ids},
		{Node: strings.Repeat("o", MaxGenesisNodeBytes), Kind: GenesisKindOther, Text: strings.Repeat(`"`, MaxGenesisOther)},
	}}
	out, err := EncodeSeatGenesisParams(p)
	if err != nil {
		t.Fatalf("the largest params do not fit: %v", err)
	}
	t.Logf("largest params: %d bytes of %d", len(out), MaxSeatGenesisParamsBytes)
	if _, err := DecodeSeatGenesisParams(out); err != nil {
		t.Errorf("the largest params do not decode: %v", err)
	}

	// The most a round holds: three questions of eight options, each text at
	// its cap, each id at its cap.
	var nodes []GenesisNode
	for i := 0; i < MaxGenesisNodes; i++ {
		n := GenesisNode{ID: strings.Repeat("q", MaxGenesisNodeBytes-1) + strconv.Itoa(i), Type: GenesisTypeMulti,
			Prompt: strings.Repeat("p", MaxGenesisPrompt), Topic: strings.Repeat("t", MaxGenesisTopicBytes),
			AllowOther: true, AllowYouDecide: true, CanSkip: true}
		for j := 0; j < MaxGenesisOptions; j++ {
			n.Options = append(n.Options, GenesisOption{ID: strings.Repeat("o", MaxGenesisChoiceBytes-1) + strconv.Itoa(j),
				Label: strings.Repeat("l", MaxGenesisLabel), Example: strings.Repeat("e", MaxGenesisExample)})
		}
		nodes = append(nodes, n)
	}
	q := SeatGenesisResult{State: GenesisStateQuestion, Thread: genThread, Round: 6, Of: 6, Progress: GenesisProgress{Filled: 20, Total: 21},
		Nodes: nodes, CatalogDigest: genCatalog}
	out, err = EncodeSeatGenesisResult(q)
	if err != nil {
		t.Fatalf("the largest round does not fit: %v", err)
	}
	t.Logf("largest round: %d bytes of %d", len(out), MaxSeatGenesisResultBytes)
	if _, err := DecodeSeatGenesisResult(out); err != nil {
		t.Errorf("the largest round does not decode: %v", err)
	}

	// A summary of 64 rows of short texts fits; one of 64 rows with every text
	// at its cap does not, and is refused record-too-large: the result cap is
	// the limit, and a host with more to say shortens it.
	groups := func(node, field, label, value string) *GenesisGroups {
		var g GenesisGroups
		g.DecidedForYou, g.Defaults = []GenesisRow{}, []GenesisRow{}
		for i := 0; i < MaxGenesisRows; i++ {
			g.YouChose = append(g.YouChose, GenesisRow{Node: node, Field: "software.f" + strconv.Itoa(i) + field, Label: label, Value: value})
		}
		return &g
	}
	s := SeatGenesisResult{State: GenesisStateSummary, Thread: genThread, Round: 6, Of: 6, Progress: GenesisProgress{Filled: 64, Total: 64},
		Nodes: []GenesisNode{}, CatalogDigest: genCatalog}
	s.Groups = groups("goal", "", "What is the project for?", "To share with others")
	out, err = EncodeSeatGenesisResult(s)
	if err != nil {
		t.Fatalf("a summary of 64 short rows does not fit: %v", err)
	}
	t.Logf("64 short rows: %d bytes of %d", len(out), MaxSeatGenesisResultBytes)
	s.Groups = groups(strings.Repeat("n", MaxGenesisNodeBytes), "", strings.Repeat("l", MaxGenesisPrompt), strings.Repeat("v", MaxGenesisValue))
	_, err = EncodeSeatGenesisResult(s)
	refused(t, err, record.CodeRecordTooLarge, "64 rows at their caps")

	// The cap is exactly 16 KiB, and a result over it is not read.
	big, err := EncodeSeatGenesisResult(sampleSeatGenesisResult())
	if err != nil {
		t.Fatal(err)
	}
	over := append(append([]byte(nil), big...), bytes.Repeat([]byte(" "), MaxSeatGenesisResultBytes)...)
	_, err = DecodeSeatGenesisResult(over)
	refused(t, err, record.CodeRecordTooLarge, "a result over its cap")
	pover := append([]byte(`{"verb":"start"}`), bytes.Repeat([]byte(" "), MaxSeatGenesisParamsBytes)...)
	_, err = DecodeSeatGenesisParams(pover)
	refused(t, err, record.CodeRecordTooLarge, "params over their cap")
}
