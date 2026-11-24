package ownerwire

import (
	"bytes"
	"encoding/base64"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"ataniq/internal/approval/record"
)

// The samples of seat.chat.
const (
	chatAlpha = "daemon:alpha"
	chatAt    = "2026-11-23T10:00:00Z"
)

func n64(n int64) *int64 { return &n }

func sampleSeatChatParams() SeatChatParams {
	return SeatChatParams{Verb: ChatVerbRead, Lead: chatAlpha, After: 4, Limit: 8}
}

func sampleChatLeads() SeatChatResult {
	return SeatChatResult{State: ChatStateLeads, StandIn: true, Leads: []ChatLeadRow{
		{Lead: chatAlpha, Chat: ChatRead, State: ChatLeadRunning, Epoch: n64(3), Head: n64(10)},
		{Lead: "daemon:bravo", Chat: ChatSend, State: ChatLeadStopped, Epoch: n64(0), Head: n64(0)},
		{Lead: "daemon:charlie", Chat: ChatOff},
	}}
}

func sampleSeatChatRead() SeatChatResult {
	return SeatChatResult{State: ChatStateRead, StandIn: true, Lead: chatAlpha, Epoch: 3, Head: 10, More: true, Entries: []ChatEntry{
		{Seq: 5, Who: ChatWhoSystem, At: chatAt, Text: "Lead restarted, epoch 3"},
		{Seq: 6, Who: ChatWhoLead, At: chatAt, Text: "Two lines\nof <b>text</b> & \"quotes\"", Cut: true},
	}}
}

// The operation is the gate's alone, the last of the seven, and its name has
// the form of the others.
func TestSeatChatIsAGateOperation(t *testing.T) {
	if OpSeatChat != "seat.chat" {
		t.Fatalf("the operation is %q", OpSeatChat)
	}
	gate := GateOperations()
	if len(gate) != 7 || gate[6] != OpSeatChat {
		t.Fatalf("the gate operations are %v, want seat.chat last of seven", gate)
	}
	for _, op := range Operations() {
		if op == OpSeatChat {
			t.Errorf("%s is in Operations(), the list the Unix socket serves and announces", op)
		}
	}
	if !regexp.MustCompile(`^seat\.[a-z0-9-]+$`).MatchString(OpSeatChat) || strings.Count(OpSeatChat, ".") != 1 {
		t.Errorf("%q is not seat. and one part of lower-case letters, digits and dashes", OpSeatChat)
	}
}

// The three words are of the gate's grammar, each its own and none another
// gate word.
func TestChatCodesAreWords(t *testing.T) {
	codes := map[string]string{CodeChatOff: "chat-off", CodeChatUnknownLead: "chat-unknown-lead", CodeChatBusy: "chat-busy"}
	if len(codes) != 3 {
		t.Fatalf("%d codes with different words, want 3", len(codes))
	}
	grammar := regexp.MustCompile(`^[a-z0-9-]{1,40}$`)
	for code, word := range codes {
		if code != word || !grammar.MatchString(code) {
			t.Errorf("the code %s is written %q", word, code)
		}
	}
	others := []string{CodeChallengeStale, CodeChallengesFull, CodeAssertionInvalid, CodeKeyRevoked, CodeKindRefused, CodeWaitFull,
		CodeGenesisBusy, CodeGenesisUnknownThread, CodeGenesisBadAnswer, CodeGenesisOverCap, CodeGenesisSecretShaped,
		CodeGenesisDone, CodeGenesisStaleRound, CodeGenesisNotReady, CodeGenesisNoTemplate}
	for _, o := range others {
		if _, ok := codes[o]; ok {
			t.Errorf("%q is both a chat code and another gate code", o)
		}
	}
}

// The bounds are the ones the task fixes.
func TestChatBounds(t *testing.T) {
	got := []int{MaxSeatChatParamsBytes, MaxSeatChatResultBytes, MaxChatLeads, MaxChatReadLimit, MaxChatText, MaxChatKeyBytes}
	want := []int{4096, 16384, 8, 16, 2000, 32}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("the bounds are %v, want %v", got, want)
	}
	if MaxChatSeq != 9007199254740991 {
		t.Errorf("MaxChatSeq is %d", MaxChatSeq)
	}
}

// The params of each verb, as the one canonical spelling.
func TestSeatChatParamsVectors(t *testing.T) {
	dec, enc := DecodeSeatChatParams, EncodeSeatChatParams
	vector(t, "leads", `{"verb":"leads"}`, SeatChatParams{Verb: ChatVerbLeads}, dec, enc)
	vector(t, "read", `{"after":4,"lead":"daemon:alpha","limit":8,"verb":"read"}`, sampleSeatChatParams(), dec, enc)
	vector(t, "read from the start", `{"lead":"daemon:alpha","limit":16,"verb":"read"}`,
		SeatChatParams{Verb: ChatVerbRead, Lead: chatAlpha, Limit: 16}, dec, enc)
	vector(t, "read at the largest cursor", `{"after":9007199254740991,"lead":"daemon:0-1","limit":1,"verb":"read"}`,
		SeatChatParams{Verb: ChatVerbRead, Lead: "daemon:0-1", After: MaxChatSeq, Limit: 1}, dec, enc)
	// An explicit 0 is another spelling of the start.
	if p, err := dec([]byte(`{"after":0,"lead":"daemon:alpha","limit":2,"verb":"read"}`)); err != nil || p.After != 0 {
		t.Errorf("after 0: %+v %v", p, err)
	}
}

// A verb takes exactly its members, there is no verb that sends, and a lead
// identifier is of one form.
func TestSeatChatParamsAreAClosedUnion(t *testing.T) {
	for name, in := range map[string]string{
		"no verb":                  `{}`,
		"a send":                   `{"lead":"daemon:alpha","verb":"send"}`,
		"a send with a body":       `{"body":"hi","lead":"daemon:alpha","verb":"send"}`,
		"a read with a body":       `{"body":"hi","lead":"daemon:alpha","limit":1,"verb":"read"}`,
		"a verb in capitals":       `{"verb":"Leads"}`,
		"a verb that is a number":  `{"verb":1}`,
		"an empty verb":            `{"verb":""}`,
		"a leads with a lead":      `{"lead":"daemon:alpha","verb":"leads"}`,
		"a leads with after":       `{"after":1,"verb":"leads"}`,
		"a leads with a limit":     `{"limit":1,"verb":"leads"}`,
		"a read without a lead":    `{"limit":1,"verb":"read"}`,
		"a read without a limit":   `{"lead":"daemon:alpha","verb":"read"}`,
		"a limit of 0":             `{"lead":"daemon:alpha","limit":0,"verb":"read"}`,
		"a limit of 17":            `{"lead":"daemon:alpha","limit":17,"verb":"read"}`,
		"a negative limit":         `{"lead":"daemon:alpha","limit":-1,"verb":"read"}`,
		"a negative after":         `{"after":-1,"lead":"daemon:alpha","limit":1,"verb":"read"}`,
		"an after past 2^53-1":     `{"after":9007199254740992,"lead":"daemon:alpha","limit":1,"verb":"read"}`,
		"an after that is a float": `{"after":1.5,"lead":"daemon:alpha","limit":1,"verb":"read"}`,
		"an empty lead":            `{"lead":"","limit":1,"verb":"read"}`,
		"a lead of another kind":   `{"lead":"tmux:alpha","limit":1,"verb":"read"}`,
		"a lead without a kind":    `{"lead":"alpha","limit":1,"verb":"read"}`,
		"a lead with no key":       `{"lead":"daemon:","limit":1,"verb":"read"}`,
		"a key starting with -":    `{"lead":"daemon:-alpha","limit":1,"verb":"read"}`,
		"a key in capitals":        `{"lead":"daemon:Alpha","limit":1,"verb":"read"}`,
		"a key with _":             `{"lead":"daemon:al_pha","limit":1,"verb":"read"}`,
		"a key with a colon":       `{"lead":"daemon:al:pha","limit":1,"verb":"read"}`,
		"a key with a space":       `{"lead":"daemon:al pha","limit":1,"verb":"read"}`,
		"a key of 33":              `{"lead":"daemon:` + strings.Repeat("a", 33) + `","limit":1,"verb":"read"}`,
		"a repeated key":           `{"verb":"leads","verb":"leads"}`,
		"a null lead":              `{"lead":null,"limit":1,"verb":"read"}`,
		"an array":                 `[]`,
	} {
		_, err := DecodeSeatChatParams([]byte(in))
		refused(t, err, record.CodeRecordInvalid, name)
	}
	// The encoder refuses the same.
	for name, p := range map[string]SeatChatParams{
		"a send":              {Verb: "send", Lead: chatAlpha},
		"a leads with a lead": {Verb: ChatVerbLeads, Lead: chatAlpha},
		"a read of no lead":   {Verb: ChatVerbRead, Limit: 1},
		"a read of no limit":  {Verb: ChatVerbRead, Lead: chatAlpha},
	} {
		_, err := EncodeSeatChatParams(p)
		refused(t, err, record.CodeRecordInvalid, "encode: "+name)
	}
	if !ValidChatLead("daemon:" + strings.Repeat("a", MaxChatKeyBytes)) {
		t.Error("a key of 32 is refused")
	}
}

// The result of each state, as the one canonical spelling. A text of two
// lines travels as b64.
func TestSeatChatResultVectors(t *testing.T) {
	dec, enc := DecodeSeatChatResult, EncodeSeatChatResult
	vector(t, "leads", `{"leads":[`+
		`{"chat":"read","epoch":3,"head":10,"lead":"daemon:alpha","state":"running"},`+
		`{"chat":"send","epoch":0,"head":0,"lead":"daemon:bravo","state":"stopped"},`+
		`{"chat":"off","lead":"daemon:charlie"}],"stand_in":true,"state":"leads"}`, sampleChatLeads(), dec, enc)
	vector(t, "no leads", `{"leads":[],"state":"leads"}`, SeatChatResult{State: ChatStateLeads, Leads: []ChatLeadRow{}}, dec, enc)
	two := base64.StdEncoding.EncodeToString([]byte("Two lines\nof <b>text</b> & \"quotes\""))
	vector(t, "read", `{"entries":[`+
		`{"at":"`+chatAt+`","cut":false,"seq":5,"text":"Lead restarted, epoch 3","who":"system"},`+
		`{"at":"`+chatAt+`","cut":true,"seq":6,"text":{"b64":"`+two+`"},"who":"lead"}],`+
		`"epoch":3,"head":10,"lead":"daemon:alpha","more":true,"stand_in":true,"state":"read"}`, sampleSeatChatRead(), dec, enc)
	vector(t, "an empty page after a reset", `{"entries":[],"lead":"daemon:alpha","reset":true,"state":"read"}`,
		SeatChatResult{State: ChatStateRead, Lead: chatAlpha, Entries: []ChatEntry{}, Reset: true}, dec, enc)
}

// An off row carries its lead and its switch and nothing else: a state, an
// epoch or a head on it is refused, even an epoch or a head of 0; every other
// row carries all three.
func TestAnOffRowCarriesOnlyItsLeadAndSwitch(t *testing.T) {
	row := func(r string) string { return `{"leads":[` + r + `],"state":"leads"}` }
	if _, err := DecodeSeatChatResult([]byte(row(`{"chat":"off","lead":"daemon:charlie"}`))); err != nil {
		t.Fatalf("the bare off row: %v", err)
	}
	for name, r := range map[string]string{
		"a state":           `{"chat":"off","lead":"daemon:charlie","state":"stopped"}`,
		"an epoch":          `{"chat":"off","epoch":3,"lead":"daemon:charlie"}`,
		"an epoch of 0":     `{"chat":"off","epoch":0,"lead":"daemon:charlie"}`,
		"a head":            `{"chat":"off","head":7,"lead":"daemon:charlie"}`,
		"a head of 0":       `{"chat":"off","head":0,"lead":"daemon:charlie"}`,
		"all three":         `{"chat":"off","epoch":0,"head":0,"lead":"daemon:charlie","state":"unknown"}`,
		"an empty state":    `{"chat":"off","lead":"daemon:charlie","state":""}`,
		"a member it lacks": `{"chat":"off","lead":"daemon:charlie","text":"x"}`,
	} {
		_, err := DecodeSeatChatResult([]byte(row(r)))
		refused(t, err, record.CodeRecordInvalid, "an off row with "+name)
	}
	for name, change := range map[string]func(*ChatLeadRow){
		"a state":  func(r *ChatLeadRow) { r.State = ChatLeadRunning },
		"an epoch": func(r *ChatLeadRow) { r.Epoch = n64(0) },
		"a head":   func(r *ChatLeadRow) { r.Head = n64(0) },
	} {
		res := sampleChatLeads()
		change(&res.Leads[2])
		_, err := EncodeSeatChatResult(res)
		refused(t, err, record.CodeRecordInvalid, "encode an off row with "+name)
	}
	for name, change := range map[string]func(*ChatLeadRow){
		"no state":                   func(r *ChatLeadRow) { r.State = "" },
		"no epoch":                   func(r *ChatLeadRow) { r.Epoch = nil },
		"no head":                    func(r *ChatLeadRow) { r.Head = nil },
		"a state not in the list":    func(r *ChatLeadRow) { r.State = "paused" },
		"a negative epoch":           func(r *ChatLeadRow) { r.Epoch = n64(-1) },
		"a head past 2^53-1":         func(r *ChatLeadRow) { r.Head = n64(MaxChatSeq + 1) },
		"a switch not in the list":   func(r *ChatLeadRow) { r.Chat = "on" },
		"a switch in capitals":       func(r *ChatLeadRow) { r.Chat = "Read" },
		"a lead of another kind":     func(r *ChatLeadRow) { r.Lead = "rc:alpha" },
		"the lead of the next row":   func(r *ChatLeadRow) { r.Lead = "daemon:bravo" },
		"a lead identifier too long": func(r *ChatLeadRow) { r.Lead = "daemon:" + strings.Repeat("x", 33) },
	} {
		res := sampleChatLeads()
		change(&res.Leads[0])
		_, err := EncodeSeatChatResult(res)
		refused(t, err, record.CodeRecordInvalid, "a read row with "+name)
	}
}

// What a result may hold, state by state.
func TestSeatChatResultIsAClosedUnion(t *testing.T) {
	leads := map[string]func(*SeatChatResult){
		"a lead":       func(r *SeatChatResult) { r.Lead = chatAlpha },
		"an epoch":     func(r *SeatChatResult) { r.Epoch = 1 },
		"a head":       func(r *SeatChatResult) { r.Head = 1 },
		"entries":      func(r *SeatChatResult) { r.Entries = []ChatEntry{} },
		"more":         func(r *SeatChatResult) { r.More = true },
		"a reset":      func(r *SeatChatResult) { r.Reset = true },
		"no list":      func(r *SeatChatResult) { r.Leads = nil },
		"nine leads":   func(r *SeatChatResult) { r.Leads = nineRows() },
		"a state word": func(r *SeatChatResult) { r.State = "lead" },
	}
	for name, change := range leads {
		r := sampleChatLeads()
		change(&r)
		_, err := EncodeSeatChatResult(r)
		refused(t, err, record.CodeRecordInvalid, "leads with "+name)
	}
	reads := map[string]func(*SeatChatResult){
		"a list":                  func(r *SeatChatResult) { r.Leads = []ChatLeadRow{} },
		"no lead":                 func(r *SeatChatResult) { r.Lead = "" },
		"no entries":              func(r *SeatChatResult) { r.Entries = nil },
		"more with no entry":      func(r *SeatChatResult) { r.Entries = []ChatEntry{} },
		"a negative epoch":        func(r *SeatChatResult) { r.Epoch = -1 },
		"a head below an entry":   func(r *SeatChatResult) { r.Head = 5 },
		"seventeen entries":       func(r *SeatChatResult) { r.Entries = manyEntries(17, "x"); r.Head = 100 },
		"a seq of 0":              func(r *SeatChatResult) { r.Entries[0].Seq = 0 },
		"a seq twice":             func(r *SeatChatResult) { r.Entries[1].Seq = 5 },
		"seqs out of order":       func(r *SeatChatResult) { r.Entries[0].Seq = 7; r.Entries[1].Seq = 6 },
		"a seq past 2^53-1":       func(r *SeatChatResult) { r.Entries[1].Seq = MaxChatSeq + 1; r.Head = MaxChatSeq + 1 },
		"a who of the owner":      func(r *SeatChatResult) { r.Entries[0].Who = "owner" },
		"a who of the seat":       func(r *SeatChatResult) { r.Entries[0].Who = "seat" },
		"a who of the desk":       func(r *SeatChatResult) { r.Entries[0].Who = "desk" },
		"a time with an offset":   func(r *SeatChatResult) { r.Entries[0].At = "2026-11-23T10:00:00+01:00" },
		"a time with a fraction":  func(r *SeatChatResult) { r.Entries[0].At = "2026-11-23T10:00:00.5Z" },
		"no time":                 func(r *SeatChatResult) { r.Entries[0].At = "" },
		"no text":                 func(r *SeatChatResult) { r.Entries[0].Text = "" },
		"a text over its cap":     func(r *SeatChatResult) { r.Entries[0].Text = record.Text(strings.Repeat("x", MaxChatText+1)) },
		"a text of spaces":        func(r *SeatChatResult) { r.Entries[0].Text = " \n " },
		"a bidi override":         func(r *SeatChatResult) { r.Entries[0].Text = "abc\u202edef" },
		"a lead of another kind":  func(r *SeatChatResult) { r.Lead = "tmux:alpha" },
		"a state not in the list": func(r *SeatChatResult) { r.State = "page" },
	}
	for name, change := range reads {
		r := sampleSeatChatRead()
		change(&r)
		_, err := EncodeSeatChatResult(r)
		refused(t, err, record.CodeRecordInvalid, "read with "+name)
	}
	// The decoder refuses the same, from the JSON.
	for name, in := range map[string]string{
		"a read with a list":    `{"entries":[],"lead":"daemon:alpha","leads":[],"state":"read"}`,
		"a read with no list":   `{"lead":"daemon:alpha","state":"read"}`,
		"a read with null":      `{"entries":null,"lead":"daemon:alpha","state":"read"}`,
		"a leads with entries":  `{"entries":[],"leads":[],"state":"leads"}`,
		"a leads with no list":  `{"state":"leads"}`,
		"an entry it lacks":     `{"entries":[{"at":"` + chatAt + `","cut":false,"seq":1,"text":"x","who":"lead","to":"x"}],"head":1,"lead":"daemon:alpha","state":"read"}`,
		"an entry with no cut":  `{"entries":[{"at":"` + chatAt + `","seq":1,"text":"x","who":"lead"}],"head":1,"lead":"daemon:alpha","state":"read"}`,
		"an empty stand_in":     `{"leads":[],"stand_in":"","state":"leads"}`,
		"a member of no state":  `{"leads":[],"state":"leads","decision":"approve"}`,
		"no state":              `{"leads":[]}`,
		"a state in capitals":   `{"leads":[],"state":"Leads"}`,
		"an entry of no who":    `{"entries":[{"at":"` + chatAt + `","cut":false,"seq":1,"text":"x"}],"head":1,"lead":"daemon:alpha","state":"read"}`,
		"a stand_in of no bool": `{"leads":[],"stand_in":1,"state":"leads"}`,
	} {
		_, err := DecodeSeatChatResult([]byte(in))
		refused(t, err, record.CodeRecordInvalid, name)
	}
}

func nineRows() []ChatLeadRow {
	rows := make([]ChatLeadRow, MaxChatLeads+1)
	for i := range rows {
		rows[i] = ChatLeadRow{Lead: "daemon:l" + strconv.Itoa(i), Chat: ChatOff}
	}
	return rows
}

// manyEntries are n entries of the text, numbered from 1.
func manyEntries(n int, text string) []ChatEntry {
	es := make([]ChatEntry, n)
	for i := range es {
		es[i] = ChatEntry{Seq: int64(i + 1), Who: ChatWhoLead, At: chatAt, Text: record.Text(text)}
	}
	return es
}

// The text of an entry is plain text: line feeds are taken, and travel as
// b64; every other control character, the line and paragraph separators,
// non-ASCII spaces and the bidirectional and other format controls are
// refused, whether the text came as a string or as b64. Markup is text.
func TestChatTextIsPlain(t *testing.T) {
	good := []string{
		"plain", "two\nlines", "\nled by a line feed", "<b>bold</b> & \"quotes\" \\ <script>", "café 東京 ✓",
		strings.Repeat("x", MaxChatText), strings.Repeat("é", MaxChatText/2),
	}
	for _, s := range good {
		if !ValidChatText(s) {
			t.Errorf("%q is refused", s)
		}
		r := sampleSeatChatRead()
		r.Entries[0].Text = record.Text(s)
		out, err := EncodeSeatChatResult(r)
		if err != nil {
			t.Errorf("%q does not encode: %v", s, err)
			continue
		}
		back, err := DecodeSeatChatResult(out)
		if err != nil || string(back.Entries[0].Text) != s {
			t.Errorf("%q does not come back: %v", s, err)
		}
	}
	bad := map[string]string{
		"empty": "", "spaces": "   ", "line feeds": "\n\n", "a tab": "a\tb", "a carriage return": "a\r\nb",
		"ESC": "a\x1b[31mb", "a NUL": "a\x00b", "DEL": "a\x7fb", "a C1 control": "a\u0085b",
		"a right-to-left override": "a\u202eb", "a left-to-right mark": "a\u200eb", "an isolate": "a\u2066b\u2069",
		"a zero-width space": "a\u200bb", "a zero-width joiner": "a\u200db", "a byte order mark": "\ufeffa",
		"a line separator": "a\u2028b", "a paragraph separator": "a\u2029b", "a no-break space": "a\u00a0b",
		"an ideographic space": "a\u3000b", "a soft hyphen": "a\u00adb", "bytes that are not UTF-8": "a\xffb",
		"a lone surrogate": "a\xed\xa0\x80b", "one byte over": strings.Repeat("x", MaxChatText+1),
		"a private use rune": "a\ue000b",
	}
	for name, s := range bad {
		if ValidChatText(s) {
			t.Errorf("%s is taken", name)
		}
		// As b64, which the codec takes for any bytes, the check refuses it.
		body := `{"entries":[{"at":"` + chatAt + `","cut":false,"seq":1,"text":{"b64":"` + base64.StdEncoding.EncodeToString([]byte(s)) + `"},"who":"lead"}],"head":1,"lead":"daemon:alpha","state":"read"}`
		_, err := DecodeSeatChatResult([]byte(body))
		if err == nil {
			t.Errorf("%s as b64 is taken", name)
		}
	}
	// As a JSON string, an escaped control character is refused by the codec.
	for _, esc := range []string{`a\u001bb`, `a\tb`, `a\nb`, `a\u0085b`} {
		body := `{"entries":[{"at":"` + chatAt + `","cut":false,"seq":1,"text":"` + esc + `","who":"lead"}],"head":1,"lead":"daemon:alpha","state":"read"}`
		_, err := DecodeSeatChatResult([]byte(body))
		refused(t, err, record.CodeRecordInvalid, esc+" as a string")
	}
	// A bidi override written as a JSON escape is the same rune, refused.
	body := `{"entries":[{"at":"` + chatAt + `","cut":false,"seq":1,"text":"a\u202eb","who":"lead"}],"head":1,"lead":"daemon:alpha","state":"read"}`
	_, err := DecodeSeatChatResult([]byte(body))
	refused(t, err, record.CodeRecordInvalid, "a bidi override as an escape")
}

// worstText is the text of the most encoded bytes: MaxChatText quotes, each
// written \" in a string.
var worstText = record.Text(strings.Repeat(`"`, MaxChatText))

// The caps hold the largest payloads: the largest params, the largest list,
// and a page of one entry of the worst text at the largest numbers. A page
// of sixteen such entries does not fit, and FillSeatChatRead pages them by
// encoded bytes, with a cursor that always moves on.
func TestSeatChatCapsHoldTheLargestPayloads(t *testing.T) {
	longLead := "daemon:" + strings.Repeat("k", MaxChatKeyBytes)
	p := SeatChatParams{Verb: ChatVerbRead, Lead: longLead, After: MaxChatSeq, Limit: MaxChatReadLimit}
	out, err := EncodeSeatChatParams(p)
	if err != nil || len(out) > 256 {
		t.Fatalf("the largest params: %d bytes, %v", len(out), err)
	}

	rows := make([]ChatLeadRow, MaxChatLeads)
	for i := range rows {
		rows[i] = ChatLeadRow{Lead: "daemon:" + strings.Repeat("k", MaxChatKeyBytes-1) + strconv.Itoa(i), Chat: ChatSend,
			State: ChatLeadStarting, Epoch: n64(MaxChatSeq), Head: n64(MaxChatSeq)}
	}
	out, err = EncodeSeatChatResult(SeatChatResult{State: ChatStateLeads, StandIn: true, Leads: rows})
	if err != nil {
		t.Fatalf("the largest list does not fit: %v", err)
	}
	t.Logf("largest list: %d bytes of %d", len(out), MaxSeatChatResultBytes)

	head := SeatChatResult{State: ChatStateRead, StandIn: true, Lead: longLead, Epoch: MaxChatSeq, Head: MaxChatSeq, Reset: true}
	for name, text := range map[string]record.Text{
		"quotes":           worstText,
		"backslashes":      record.Text(strings.Repeat(`\`, MaxChatText)),
		"lines, as b64":    record.Text(strings.Repeat("\n\"", MaxChatText/2)),
		"three-byte runes": record.Text(strings.Repeat("東", MaxChatText/3)),
	} {
		one := head
		one.More = true
		one.Entries = []ChatEntry{{Seq: MaxChatSeq, Who: ChatWhoLead, At: chatAt, Text: text, Cut: true}}
		out, err := EncodeSeatChatResult(one)
		if err != nil {
			t.Fatalf("one entry of %s does not fit: %v", name, err)
		}
		t.Logf("one entry of %s: %d bytes of %d", name, len(out), MaxSeatChatResultBytes)
		if _, err := DecodeSeatChatResult(out); err != nil {
			t.Errorf("one entry of %s does not decode: %v", name, err)
		}
	}

	// Sixteen of the worst do not fit in one page.
	worst := manyEntries(MaxChatReadLimit, string(worstText))
	full := head
	full.Head, full.Entries = 100, worst
	_, err = EncodeSeatChatResult(full)
	refused(t, err, record.CodeRecordTooLarge, "sixteen entries of the worst text")

	// Paged by encoded bytes, every page fits, holds at least one entry, and
	// the cursor moves on until the conversation is read.
	page := head
	page.Head, page.Reset = 100, false
	var got []ChatEntry
	rest := worst
	for pages := 0; len(rest) > 0; pages++ {
		if pages > MaxChatReadLimit {
			t.Fatal("the cursor does not move on")
		}
		r, err := FillSeatChatRead(page, rest, MaxChatReadLimit)
		if err != nil {
			t.Fatal(err)
		}
		out, err := EncodeSeatChatResult(r)
		if err != nil || len(out) > MaxSeatChatResultBytes || len(r.Entries) == 0 {
			t.Fatalf("a page of %d entries: %d bytes, %v", len(r.Entries), len(out), err)
		}
		if r.More != (len(r.Entries) < len(rest)) {
			t.Fatalf("more is %v with %d of %d entries", r.More, len(r.Entries), len(rest))
		}
		got = append(got, r.Entries...)
		rest = rest[len(r.Entries):]
	}
	if !reflect.DeepEqual(got, worst) {
		t.Fatal("the pages do not hold the conversation")
	}

	// The cap is exactly 16 KiB, and a result over it is not read.
	big, err := EncodeSeatChatResult(sampleSeatChatRead())
	if err != nil {
		t.Fatal(err)
	}
	over := append(append([]byte(nil), big...), bytes.Repeat([]byte(" "), MaxSeatChatResultBytes)...)
	_, err = DecodeSeatChatResult(over)
	refused(t, err, record.CodeRecordTooLarge, "a result over its cap")
	pover := append([]byte(`{"verb":"leads"}`), bytes.Repeat([]byte(" "), MaxSeatChatParamsBytes)...)
	_, err = DecodeSeatChatParams(pover)
	refused(t, err, record.CodeRecordTooLarge, "params over their cap")
}

// FillSeatChatRead keeps to the limit and the order, says more when it left
// entries out, gives an empty page with no more for nothing to read, and
// refuses a page the encoder refuses.
func TestFillSeatChatRead(t *testing.T) {
	head := SeatChatResult{State: ChatStateRead, Lead: chatAlpha, Epoch: 2, Head: 20}
	es := manyEntries(20, "short")
	r, err := FillSeatChatRead(head, es, 5)
	if err != nil || len(r.Entries) != 5 || !r.More || r.Entries[4].Seq != 5 {
		t.Fatalf("a limit of 5: %+v %v", r, err)
	}
	r, err = FillSeatChatRead(head, es[15:], 16)
	if err != nil || len(r.Entries) != 5 || r.More || r.Entries[0].Seq != 16 {
		t.Fatalf("the last five: %+v %v", r, err)
	}
	r, err = FillSeatChatRead(head, nil, 16)
	if err != nil || r.Entries == nil || len(r.Entries) != 0 || r.More {
		t.Fatalf("nothing to read: %+v %v", r, err)
	}
	if _, err := EncodeSeatChatResult(r); err != nil {
		t.Fatalf("the empty page does not encode: %v", err)
	}
	// A limit out of its bounds is taken as the nearest bound.
	if r, err := FillSeatChatRead(head, es, 99); err != nil || len(r.Entries) != MaxChatReadLimit || !r.More {
		t.Fatalf("a limit of 99: %d entries, %v", len(r.Entries), err)
	}
	if r, err := FillSeatChatRead(head, es, 0); err != nil || len(r.Entries) != 1 || !r.More {
		t.Fatalf("a limit of 0: %d entries, %v", len(r.Entries), err)
	}
	// What the encoder refuses is not paged over.
	bad := manyEntries(2, "short")
	bad[1].Text = "a\u202eb"
	if _, err := FillSeatChatRead(head, bad, 16); err == nil {
		t.Fatal("a bidi override was paged")
	}
	if _, err := FillSeatChatRead(SeatChatResult{State: ChatStateRead, Lead: "tmux:x"}, es, 16); err == nil {
		t.Fatal("a head of another lead kind was paged")
	}
	// An entry that cannot fit even alone is refused, not skipped.
	huge := head
	huge.Lead = "daemon:" + strings.Repeat("k", MaxChatKeyBytes)
	if _, err := FillSeatChatRead(huge, []ChatEntry{{Seq: 1, Who: ChatWhoLead, At: chatAt, Text: record.Text(strings.Repeat("x", MaxChatText+1))}}, 1); err == nil {
		t.Fatal("an entry over its text cap was paged")
	}
}
