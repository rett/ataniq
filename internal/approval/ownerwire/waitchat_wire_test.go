package ownerwire

import (
	"crypto/sha256"
	"strconv"
	"strings"
	"testing"

	"ataniq/internal/approval/record"
)

func cursorOf(v int64) *int64 { return &v }

func sampleSeatWaitChatParams() SeatWaitParams {
	return SeatWaitParams{Since: waitVersionTwo, Chat: cursorOf(12345)}
}

func sampleSeatWaitChatResult() SeatWaitResult {
	r := sampleSeatWaitResult()
	r.Chat = cursorOf(67890)
	return r
}

// A legacy caller meets exactly the shapes it always did: the plain encoders
// write no chat member, and the plain decoders refuse one, in the params and
// in the result, as a gate or a seat that does not know the extension does.
func TestSeatWaitPlainFormRefusesChat(t *testing.T) {
	out, err := EncodeSeatWaitParams(SeatWaitParams{Since: waitVersionTwo})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"since":"` + waitVersionTwo + `"}`; string(out) != want {
		t.Errorf("the plain params are %s, want %s", out, want)
	}
	_, err = EncodeSeatWaitParams(sampleSeatWaitChatParams())
	refused(t, err, record.CodeRecordInvalid, "plain params with chat")
	for _, in := range []string{
		`{"chat":0,"since":""}`,
		`{"chat":1,"since":"` + waitVersionTwo + `"}`,
	} {
		_, err := DecodeSeatWaitParams([]byte(in))
		refused(t, err, record.CodeRecordInvalid, "plain params: "+in)
	}

	plain, err := EncodeSeatWaitResult(sampleSeatWaitResult())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(plain), "chat") {
		t.Errorf("the plain result names chat: %s", plain)
	}
	_, err = EncodeSeatWaitResult(sampleSeatWaitChatResult())
	refused(t, err, record.CodeRecordInvalid, "plain result with chat")
	ext, err := EncodeSeatWaitChatResult(sampleSeatWaitChatResult())
	if err != nil {
		t.Fatal(err)
	}
	_, err = DecodeSeatWaitResult(ext)
	refused(t, err, record.CodeRecordInvalid, "plain decoder of an extended result")
}

// The extended params are a distinct form: chat must be given, 0 to 2^53-1,
// and the plain params, without it, are not of it.
func TestSeatWaitChatParamsContent(t *testing.T) {
	for name, v := range map[string]int64{"none yet": 0, "one": 1, "the largest": MaxChatSeq} {
		p := SeatWaitParams{Since: waitVersionTwo, Chat: cursorOf(v)}
		out, err := EncodeSeatWaitChatParams(p)
		if err != nil {
			t.Errorf("%s: refused: %v", name, err)
			continue
		}
		if want := `{"chat":` + strconv.FormatInt(v, 10) + `,"since":"` + waitVersionTwo + `"}`; string(out) != want {
			t.Errorf("%s: written %s, want %s", name, out, want)
		}
		back, err := DecodeSeatWaitChatParams(out)
		if err != nil || back.Since != p.Since || back.Chat == nil || *back.Chat != v {
			t.Errorf("%s: reads back as %+v (%v)", name, back, err)
		}
	}
	for name, p := range map[string]SeatWaitParams{
		"no chat":          {Since: waitVersionTwo},
		"a negative chat":  {Since: waitVersionTwo, Chat: cursorOf(-1)},
		"past 2^53-1":      {Since: waitVersionTwo, Chat: cursorOf(MaxChatSeq + 1)},
		"a bad since":      {Since: "x", Chat: cursorOf(1)},
		"since in capital": {Since: strings.ToUpper(waitVersionTwo), Chat: cursorOf(1)},
	} {
		_, err := EncodeSeatWaitChatParams(p)
		refused(t, err, record.CodeRecordInvalid, "extended params: "+name)
	}
	for _, in := range []string{
		`{"since":"` + waitVersionTwo + `"}`,
		`{"chat":1}`,
		`{"chat":null,"since":""}`,
		`{"chat":"1","since":""}`,
		`{"chat":1.5,"since":""}`,
		`{"chat":9007199254740992,"since":""}`,
		`{"chat":1,"since":"","lead":"daemon:a"}`,
		`{"chat":1,"chat":2,"since":""}`,
	} {
		_, err := DecodeSeatWaitChatParams([]byte(in))
		refused(t, err, record.CodeRecordInvalid, "extended params: "+in)
	}
}

// The extended result adds chat and nothing else; chat is 1 to 2^53-1, or
// absent when the daemon had no cursor to give, and every rule of the plain
// result holds.
func TestSeatWaitChatResultContent(t *testing.T) {
	plain, err := EncodeSeatWaitResult(sampleSeatWaitResult())
	if err != nil {
		t.Fatal(err)
	}
	r, err := DecodeSeatWaitChatResult(plain)
	if err != nil || r.Chat != nil {
		t.Errorf("an extended result without chat: %+v (%v)", r, err)
	}
	for _, v := range []int64{1, MaxChatSeq} {
		res := sampleSeatWaitResult()
		res.Chat = cursorOf(v)
		out, err := EncodeSeatWaitChatResult(res)
		if err != nil {
			t.Errorf("chat %d refused: %v", v, err)
			continue
		}
		back, err := DecodeSeatWaitChatResult(out)
		if err != nil || back.Chat == nil || *back.Chat != v || back.Version != res.Version {
			t.Errorf("chat %d reads back as %+v (%v)", v, back, err)
		}
	}
	for name, change := range map[string]func(*SeatWaitResult){
		"chat 0":              func(r *SeatWaitResult) { r.Chat = cursorOf(0) },
		"a negative chat":     func(r *SeatWaitResult) { r.Chat = cursorOf(-3) },
		"chat past 2^53-1":    func(r *SeatWaitResult) { r.Chat = cursorOf(MaxChatSeq + 1) },
		"a bad version":       func(r *SeatWaitResult) { r.Version = "" },
		"more said wrongly":   func(r *SeatWaitResult) { r.More = true },
		"fewer than listed":   func(r *SeatWaitResult) { r.Waiting = 1 },
		"a card without id":   func(r *SeatWaitResult) { r.Cards[0].ID = "" },
		"a card's bad kind":   func(r *SeatWaitResult) { r.Cards[0].Kind = "Approval" },
		"a card's bad digest": func(r *SeatWaitResult) { r.Cards[0].BindingSHA256 = "x" },
	} {
		res := sampleSeatWaitChatResult()
		change(&res)
		_, err := EncodeSeatWaitChatResult(res)
		refused(t, err, record.CodeRecordInvalid, "extended result: "+name)
	}
	const v = `"version":"` + waitVersionTwo + `"`
	for _, in := range []string{
		`{"cards":[],"chat":1,"lead":"daemon:a","more":false,` + v + `,"waiting":0}`,
		`{"cards":[],"chat":1,"head":4,"more":false,` + v + `,"waiting":0}`,
		`{"cards":[],"chat":null,"more":false,` + v + `,"waiting":0}`,
		`{"cards":[],"chat":"7","more":false,` + v + `,"waiting":0}`,
	} {
		_, err := DecodeSeatWaitChatResult([]byte(in))
		refused(t, err, record.CodeRecordInvalid, "extended result: "+in)
	}
}

// The cursor's vectors: they pin the encoding, so that a seat and a daemon
// of different builds agree.
func TestChatCursorVectors(t *testing.T) {
	a := ChatCursorLead{Lead: "daemon:alpha", Gen: 1, Head: 7}
	b := ChatCursorLead{Lead: "daemon:bravo", Gen: 2, Head: 0}
	vectors := map[string]struct {
		set  []ChatCursorLead
		want int64
	}{
		"no lead":   {nil, chatCursorVector(t, nil)},
		"one lead":  {[]ChatCursorLead{a}, chatCursorVector(t, []ChatCursorLead{a})},
		"two leads": {[]ChatCursorLead{b, a}, chatCursorVector(t, []ChatCursorLead{a, b})},
	}
	for name, v := range vectors {
		got := ChatCursor(v.set)
		if got != v.want {
			t.Errorf("%s: %d, want %d", name, got, v.want)
		}
		if got < 1 || got > MaxChatSeq {
			t.Errorf("%s: %d is not 1 to 2^53-1", name, got)
		}
	}
	if got, want := ChatCursor(nil), int64(2554866884011498); got != want {
		t.Errorf("the empty set's cursor is %d, want %d: the encoding changed", got, want)
	}
}

// chatCursorVector computes the cursor of a set already in order, with the
// encoding written out here once more, byte by byte.
func chatCursorVector(t *testing.T, sorted []ChatCursorLead) int64 {
	t.Helper()
	var b []byte
	str := func(s string) {
		b = append(b, byte(len(s)>>24), byte(len(s)>>16), byte(len(s)>>8), byte(len(s)))
		b = append(b, s...)
	}
	num := func(v int64) {
		for i := 7; i >= 0; i-- {
			b = append(b, byte(uint64(v)>>(8*i)))
		}
	}
	str("ataniq.seat-wait.chat/1")
	n := len(sorted)
	b = append(b, byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
	for _, l := range sorted {
		str(l.Lead)
		num(l.Gen)
		num(l.Head)
	}
	sum := sha256.Sum256(b)
	var v uint64
	for _, c := range sum[:8] {
		v = v<<8 | uint64(c)
	}
	v &= 1<<53 - 1
	if v == 0 {
		v = 1
	}
	return int64(v)
}

// The cursor moves with what the seat may read and with nothing else: a
// head, a generation, a lead enabled or disabled. The order of the items does
// not matter, and the argument is not changed.
func TestChatCursorChanges(t *testing.T) {
	base := []ChatCursorLead{
		{Lead: "daemon:alpha", Gen: 3, Head: 10},
		{Lead: "daemon:bravo", Gen: 1, Head: 0},
		{Lead: "daemon:charlie", Gen: 7, Head: 2},
	}
	want := ChatCursor(base)
	kept := append([]ChatCursorLead(nil), base...)
	reversed := []ChatCursorLead{base[2], base[1], base[0]}
	if ChatCursor(reversed) != want {
		t.Error("the order of the leads changes the cursor")
	}
	for i := range base {
		if base[i] != kept[i] {
			t.Fatal("the argument was changed")
		}
	}
	seen := map[int64]string{want: "the base set"}
	for name, change := range map[string]func([]ChatCursorLead) []ChatCursorLead{
		"a head moved":      func(s []ChatCursorLead) []ChatCursorLead { s[0].Head++; return s },
		"a segment reset":   func(s []ChatCursorLead) []ChatCursorLead { s[1].Gen++; return s },
		"a lead disabled":   func(s []ChatCursorLead) []ChatCursorLead { return s[1:] },
		"a lead enabled":    func(s []ChatCursorLead) []ChatCursorLead { return append(s, ChatCursorLead{Lead: "daemon:delta"}) },
		"no lead enabled":   func(s []ChatCursorLead) []ChatCursorLead { return nil },
		"a lead renamed":    func(s []ChatCursorLead) []ChatCursorLead { s[2].Lead = "daemon:charly"; return s },
		"a lead twice":      func(s []ChatCursorLead) []ChatCursorLead { return append(s, s[0]) },
		"head and gen swap": func(s []ChatCursorLead) []ChatCursorLead { s[0].Gen, s[0].Head = s[0].Head, s[0].Gen; return s },
	} {
		got := ChatCursor(change(append([]ChatCursorLead(nil), base...)))
		if got == want {
			t.Errorf("%s does not change the cursor", name)
		}
		if other, dup := seen[got]; dup {
			t.Errorf("%s and %s have the same cursor", name, other)
		}
		seen[got] = name
		if got < 1 || got > MaxChatSeq {
			t.Errorf("%s: %d is not 1 to 2^53-1", name, got)
		}
	}
}

// The identifier is length-prefixed, so two sets that would write the same
// bytes without the prefix give different cursors.
func TestChatCursorIsNotACatenation(t *testing.T) {
	sets := map[string][]ChatCursorLead{
		"one lead":            {{Lead: "daemon:ab"}},
		"one lead, longer":    {{Lead: "daemon:abc"}},
		"two leads":           {{Lead: "daemon:a"}, {Lead: "daemon:b"}},
		"one lead with gen 1": {{Lead: "daemon:a", Gen: 1}},
		"one lead with head":  {{Lead: "daemon:a", Head: 1}},
		"empty identifier":    {{}},
		"none":                nil,
	}
	seen := map[int64]string{}
	for name, set := range sets {
		v := ChatCursor(set)
		if other, dup := seen[v]; dup {
			t.Errorf("%s and %s have the same cursor %d", name, other, v)
		}
		seen[v] = name
	}
}
