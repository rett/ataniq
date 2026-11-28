package leadwire

import (
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestConversationOperations(t *testing.T) {
	got := ConversationOperations()
	if !reflect.DeepEqual(got, []string{"conversation.read"}) {
		t.Fatalf("ConversationOperations() = %v", got)
	}
	got[0] = "x"
	if ConversationOperations()[0] != "conversation.read" {
		t.Error("ConversationOperations returned a shared slice")
	}
	for _, list := range [][]string{Operations(), ChatOperations()} {
		if slices.Contains(list, OpConversationRead) {
			t.Errorf("conversation.read is in %v", list)
		}
	}
	if Mutating(OpConversationRead) {
		t.Error("a read takes no command id and keeps no receipt")
	}
	if !reflect.DeepEqual(PageStates(), []string{"more", "end", "gap", "reset"}) {
		t.Errorf("PageStates() = %v", PageStates())
	}
	if !reflect.DeepEqual(EntryKinds(), []string{"reply", "instruction", "receipt", "system", "withheld"}) {
		t.Errorf("EntryKinds() = %v", EntryKinds())
	}
}

// The params and the result have exactly their keys, in their order, with
// empty values present.
func TestConversationJSON(t *testing.T) {
	b, err := json.Marshal(ConversationReadParams{Key: "alpha", Direction: ReadBackward})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"key":"alpha","direction":"backward","cursor":"","limit":0}`; string(b) != want {
		t.Errorf("params: %s, want %s", b, want)
	}
	b, err = json.Marshal(ConversationReadResult{Key: "alpha", State: PageStateEnd, Entries: []ConversationEntry{{
		ID: ConversationMessageID{"auth-a", "lead-a", "conv-a", 1, 2}, Kind: EntryKindReply, Text: "hi", At: "2026-11-01T12:00:00Z"}}})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"key":"alpha","state":"end","entries":[{"id":{"authority":"auth-a","lead":"lead-a","conversation":"conv-a","generation":1,"seq":2},"kind":"reply","text":"hi","at":"2026-11-01T12:00:00Z","command":null,"receipt":null,"status":null}],"older":"","newer":""}`
	if string(b) != want {
		t.Errorf("result:\n%s\nwant\n%s", b, want)
	}
}

const (
	okParams = `{"key":"alpha","direction":"backward","cursor":"","limit":10}`
	okEntry  = `{"id":{"authority":"auth-a","lead":"lead-a","conversation":"conv-a","generation":1,"seq":SEQ},"kind":"reply","text":"hi","at":"2026-11-01T12:00:00Z","command":null,"receipt":null,"status":null}`
)

func entryAt(seq string) string { return strings.Replace(okEntry, "SEQ", seq, 1) }

func TestDecodeConversationReadParams(t *testing.T) {
	got, err := DecodeConversationReadParams([]byte(okParams))
	if err != nil || got != (ConversationReadParams{"alpha", "backward", "", 10}) {
		t.Fatalf("%+v, %v", got, err)
	}
	if _, err := DecodeConversationReadParams([]byte(` {"key":"alpha","direction":"forward","cursor":"abc","limit":0}` + "\n")); err != nil {
		t.Errorf("whitespace around a forward read: %v", err)
	}
	for name, in := range map[string]string{
		"empty":             ``,
		"not an object":     `[]`,
		"null":              `null`,
		"missing key":       `{"key":"alpha","direction":"backward","cursor":""}`,
		"missing key again": `{"direction":"backward","cursor":"","limit":1}`,
		"unknown member":    `{"key":"alpha","direction":"backward","cursor":"","limit":1,"x":1}`,
		"repeated member":   `{"key":"alpha","key":"alpha","direction":"backward","cursor":"","limit":1}`,
		"escaped repeat":    `{"key":"alpha","key":"b","direction":"backward","cursor":"","limit":1}`,
		"trailing data":     okParams + ` {}`,
		"trailing garbage":  okParams + `x`,
		"wrong type":        `{"key":1,"direction":"backward","cursor":"","limit":1}`,
		"null member":       `{"key":null,"direction":"backward","cursor":"","limit":1}`,
		"fractional limit":  `{"key":"alpha","direction":"backward","cursor":"","limit":1.5}`,
		"no key":            `{"key":"","direction":"backward","cursor":"","limit":1}`,
		"long key":          `{"key":"` + strings.Repeat("k", MaxKey+1) + `","direction":"backward","cursor":"","limit":1}`,
		"direction":         `{"key":"alpha","direction":"up","cursor":"","limit":1}`,
		"forward no cursor": `{"key":"alpha","direction":"forward","cursor":"","limit":1}`,
		"long cursor":       `{"key":"alpha","direction":"backward","cursor":"` + strings.Repeat("c", MaxCursorBytes+1) + `","limit":1}`,
		"negative limit":    `{"key":"alpha","direction":"backward","cursor":"","limit":-1}`,
		"large limit":       `{"key":"alpha","direction":"backward","cursor":"","limit":201}`,
		"too large":         `{"key":"alpha","direction":"backward","cursor":"` + strings.Repeat(" ", MaxConversationParamsBytes) + `","limit":1}`,
	} {
		if p, err := DecodeConversationReadParams([]byte(in)); err == nil {
			t.Errorf("%s: accepted as %+v", name, p)
		}
	}
}

func TestDecodeConversationReadResult(t *testing.T) {
	page := func(state, entries, older, newer string) string {
		return `{"key":"alpha","state":"` + state + `","entries":[` + entries + `],"older":"` + older + `","newer":"` + newer + `"}`
	}
	const id1 = `{"authority":"auth-a","lead":"lead-a","conversation":"conv-a","generation":1,"seq":1}`
	digest := strings.Repeat("ab", 32)
	receiptJSON := `{"digest":"` + digest + `","admitted_revision":"r7","cursor":"hist"}`
	good := map[string]string{
		"more":        page("more", entryAt("1")+","+entryAt("2"), "o", "n"),
		"end":         page("end", entryAt("1"), "", "n"),
		"older page":  page("end", entryAt("1"), "", ""),
		"gap empty":   page("gap", "", "", "n"),
		"reset":       page("reset", "", "", ""),
		"withheld":    page("end", `{"id":`+id1+`,"kind":"withheld","text":"","at":"","command":null,"receipt":null,"status":null}`, "", "n"),
		"instruction": page("end", `{"id":`+id1+`,"kind":"instruction","text":"go","at":"2026-11-01T12:00:00.5Z","command":{"authority":"auth-a","client":"c","command":"d"},"receipt":null,"status":{"revision":"s1","state":"queued"}}`, "", "n"),
		"receipt":     page("end", `{"id":`+id1+`,"kind":"receipt","text":"","at":"2026-11-01T12:00:00Z","command":{"authority":"auth-a","client":"c","command":"d"},"receipt":`+receiptJSON+`,"status":null}`, "", "n"),
	}
	for name, in := range good {
		if _, err := DecodeConversationReadResult([]byte(in)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	r, err := DecodeConversationReadResult([]byte(good["more"]))
	if err != nil || len(r.Entries) != 2 || r.Entries[1].ID.Seq != 2 || r.Older != "o" {
		t.Fatalf("%+v, %v", r, err)
	}

	bad := map[string]string{
		"empty":                `{}`,
		"null entries":         `{"key":"alpha","state":"end","entries":null,"older":"","newer":"n"}`,
		"missing newer":        `{"key":"alpha","state":"end","entries":[],"older":""}`,
		"unknown member":       strings.Replace(good["end"], `"older"`, `"extra":1,"older"`, 1),
		"unknown entry member": strings.Replace(good["end"], `"kind"`, `"extra":1,"kind"`, 1),
		"missing id member":    strings.Replace(good["end"], `"lead":"lead-a",`, ``, 1),
		"repeated id member":   strings.Replace(good["end"], `"lead":"lead-a",`, `"lead":"lead-a","lead":"x",`, 1),
		"trailing data":        good["end"] + `1`,
		"unknown state":        page("live", "", "", "n"),
		"more without older":   page("more", entryAt("1"), "", "n"),
		"end with older":       page("end", entryAt("1"), "o", "n"),
		"gap with older":       page("gap", "", "o", "n"),
		"reset with entries":   page("reset", entryAt("1"), "", ""),
		"reset with cursor":    page("reset", "", "", "n"),
		"unordered":            page("end", entryAt("2")+","+entryAt("1"), "", "n"),
		"repeated":             page("end", entryAt("1")+","+entryAt("1"), "", "n"),
		"mixed segments":       page("end", entryAt("1")+","+strings.Replace(entryAt("2"), `"generation":1`, `"generation":2`, 1), "", "n"),
		"mixed leads":          page("end", entryAt("1")+","+strings.Replace(entryAt("2"), `"lead-a"`, `"lead-b"`, 1), "", "n"),
		"zero seq":             page("end", entryAt("0"), "", "n"),
		"zero generation":      page("end", strings.Replace(entryAt("1"), `"generation":1`, `"generation":0`, 1), "", "n"),
		"huge seq":             page("end", entryAt("9223372036854775808"), "", "n"),
		"bad id word":          page("end", strings.Replace(entryAt("1"), `"conv-a"`, `"conv/a"`, 1), "", "n"),
		"unknown kind":         page("end", strings.Replace(entryAt("1"), `"reply"`, `"note"`, 1), "", "n"),
		"bad time":             page("end", strings.Replace(entryAt("1"), `2026-11-01T12:00:00Z`, `yesterday`, 1), "", "n"),
		"no time":              page("end", strings.Replace(entryAt("1"), `2026-11-01T12:00:00Z`, ``, 1), "", "n"),
		"withheld with text":   strings.Replace(good["withheld"], `"text":""`, `"text":"leak"`, 1),
		"withheld with time":   strings.Replace(good["withheld"], `"at":""`, `"at":"2026-11-01T12:00:00Z"`, 1),
		"reply with command":   page("end", strings.Replace(entryAt("1"), `"command":null`, `"command":{"authority":"a","client":"c","command":"d"}`, 1), "", "n"),
		"instruction no key":   page("end", strings.Replace(entryAt("1"), `"reply"`, `"instruction"`, 1), "", "n"),
		"receipt no receipt":   strings.Replace(good["receipt"], `"receipt":`+receiptJSON, `"receipt":null`, 1),
		"upper digest":         strings.Replace(good["receipt"], digest, strings.ToUpper(digest), 1),
		"short digest":         strings.Replace(good["receipt"], digest, "abcd", 1),
		"status state":         strings.Replace(good["instruction"], `"queued"`, `"running"`, 1),
		"status revision":      strings.Replace(good["instruction"], `"s1"`, `""`, 1),
		"bad command word":     strings.Replace(good["instruction"], `"client":"c"`, `"client":"c d"`, 1),
		"invalid utf-8":        strings.Replace(entryAt("1"), `"hi"`, "\"\xff\"", 1),
		"too many entries":     page("end", strings.TrimSuffix(strings.Repeat(entryAt("1")+",", MaxConversationLimit+1), ","), "", "n"),
	}
	for name, in := range bad {
		if r, err := DecodeConversationReadResult([]byte(in)); err == nil {
			t.Errorf("%s: accepted as %+v", name, r)
		}
	}
}

// The message ID vectors of internal/appmodel are shared: each tuple has its
// exact JSON form here, strictly decodable, so that desk and seat reproduce
// the same whole ID.
func TestConversationMessageIDVectors(t *testing.T) {
	data, err := os.ReadFile("../appmodel/testdata/message-ids-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Valid []struct {
			Name string
			ID   ConversationMessageID
			Text string
		}
	}
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Valid) < 5 {
		t.Fatalf("read %d vectors", len(v.Valid))
	}
	for _, c := range v.Valid {
		b, err := json.Marshal(c.ID)
		if err != nil {
			t.Fatal(err)
		}
		var back ConversationMessageID
		if err := decodeStrict(b, &back); err != nil || back != c.ID {
			t.Errorf("%s: %s -> %+v, %v", c.Name, b, back, err)
		}
		entry := ConversationEntry{ID: c.ID, Kind: EntryKindWithheld}
		if err := entry.check(); err != nil {
			t.Errorf("%s: %v", c.Name, err)
		}
		if parts := strings.Split(c.Text, "/"); len(parts) != 6 || parts[1] != c.ID.Authority || parts[2] != c.ID.Lead || parts[3] != c.ID.Conversation {
			t.Errorf("%s: text %q does not carry the tuple", c.Name, c.Text)
		}
	}
}
