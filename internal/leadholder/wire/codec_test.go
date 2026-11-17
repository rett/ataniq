package wire_test

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"ataniq/internal/leadholder/wire"
)

func intp(n int) *int { return &n }

// ticket64 is a well-formed ticket.
var ticket64 = strings.Repeat("0123456789abcdef", 4)

var sampleFacts = wire.Facts{StreamEnded: wire.FactNo, TUIExited: wire.FactUnknown, TreeGone: wire.FactYes}

// codec is one structured payload: its sample value, the type's frame, and a
// function that decodes a payload and encodes the result again.
type codec struct {
	name   string
	typ    byte
	sample any
	// encode and decode work on the sample's own type.
	encode func(any) ([]byte, error)
	decode func([]byte) (any, error)
}

func wrapCodec[T any](name string, typ byte, sample T, enc func(T) ([]byte, error), dec func([]byte) (T, error)) codec {
	return codec{
		name: name, typ: typ, sample: sample,
		encode: func(v any) ([]byte, error) { return enc(v.(T)) },
		decode: func(b []byte) (any, error) { return dec(b) },
	}
}

var sampleStatus = wire.Status{
	Lead: "project/lead", Epoch: 7, Runtime: "claude",
	Size: wire.Size{Columns: 120, Rows: 40}, Caps: wire.DefaultCaps(),
	WriterPresent: true, ViewerPresent: false,
	Counters: map[string]uint64{"osc52_dropped": 3, "link_dropped": 0},
	Facts:    sampleFacts,
}

var codecs = []codec{
	wrapCodec("hello", wire.TypeHello, wire.Hello{
		Version: 1, Ticket: ticket64, Name: "console", Columns: 120, Rows: 40,
		Colors: wire.Colors{Foreground: "#c0c0c0", Background: "#000000", Cursor: "#FFFFFF"},
	}, wire.EncodeHello, wire.DecodeHello),
	wrapCodec("welcome", wire.TypeWelcome, wire.Welcome{
		Version: 1, Role: wire.RoleWriter, Lead: "project/lead", Epoch: 4,
		Runtime: "codex", RuntimeVersion: "1.2.3", Caps: wire.DefaultCaps(),
	}, wire.EncodeWelcome, wire.DecodeWelcome),
	wrapCodec("resize", wire.TypeResize, wire.Size{Columns: 80, Rows: 24}, wire.EncodeResize, wire.DecodeResize),
	wrapCodec("size", wire.TypeSize, wire.Size{Columns: 500, Rows: 200}, wire.EncodeSize, wire.DecodeSize),
	wrapCodec("modes", wire.TypeModes, wire.Modes{DEC: []int{1, 25, 1004, 2004, 2026}, ANSI: []int{4}}, wire.EncodeModes, wire.DecodeModes),
	wrapCodec("state", wire.TypeState, wire.State{
		Facts: sampleFacts, ExitStatus: intp(3), Idle: wire.IdleUnknown,
		IdleSignals: []string{"spinner-visible", "prompt-visible"}, Title: "claude — work",
	}, wire.EncodeState, wire.DecodeState),
	wrapCodec("role", wire.TypeRole, wire.RoleInfo{WriterName: "console", WriterPID: 4242, Role: wire.RoleViewer}, wire.EncodeRole, wire.DecodeRole),
	wrapCodec("check", wire.TypeCheck, wire.Check{Position: 1 << 40, Digest: strings.Repeat("ab", 32)}, wire.EncodeCheck, wire.DecodeCheck),
	wrapCodec("notice", wire.TypeNotice, wire.Notice{Reason: wire.ReasonTooSlow, Text: "you were too slow"}, wire.EncodeNotice, wire.DecodeNotice),
	wrapCodec("exit", wire.TypeExit, wire.Exit{
		Facts: sampleFacts, ExitStatus: intp(0),
		Counters:   map[string]uint64{"cluster_cut": 18446744073709551615, "a": 1},
		LastScreen: "line one\nline <two> & \"three\"\n",
	}, wire.EncodeExit, wire.DecodeExit),
	wrapCodec("request", wire.TypeRequest, wire.Request{Op: wire.OpTicket, Epoch: 9, Role: wire.RoleWriter}, wire.EncodeRequest, wire.DecodeRequest),
	wrapCodec("response ticket", wire.TypeResponse, wire.Response{OK: true, Ticket: ticket64}, wire.EncodeResponse, wire.DecodeResponse),
	wrapCodec("response status", wire.TypeResponse, wire.Response{OK: true, Status: &sampleStatus}, wire.EncodeResponse, wire.DecodeResponse),
	wrapCodec("response refusal", wire.TypeResponse, wire.Response{Code: wire.CodeDisabled, Text: "inject is disabled"}, wire.EncodeResponse, wire.DecodeResponse),
}

func TestRoundTripEveryPayload(t *testing.T) {
	for _, c := range codecs {
		t.Run(c.name, func(t *testing.T) {
			p, err := c.encode(c.sample)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.HasSuffix(p, []byte("\n")) {
				t.Error("payload ends in a newline")
			}
			got, err := c.decode(p)
			if err != nil {
				t.Fatalf("decode %s: %v", p, err)
			}
			again, err := c.encode(got)
			if err != nil || !bytes.Equal(again, p) {
				t.Fatalf("not stable: %s then %s (%v)", p, again, err)
			}
			if !reflect.DeepEqual(got, c.sample) {
				t.Errorf("got %+v, want %+v", got, c.sample)
			}
			// It also goes through a frame of its own type.
			var buf bytes.Buffer
			if err := wire.Write(&buf, c.typ, p); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRoundTripValuesCompare(t *testing.T) {
	// Spot checks that compare fields, not just the encoding.
	p, _ := wire.EncodeExit(wire.Exit{Facts: sampleFacts, Counters: map[string]uint64{"x": 5}, LastScreen: "a"})
	e, err := wire.DecodeExit(p)
	if err != nil || e.ExitStatus != nil || e.Counters["x"] != 5 || e.LastScreen != "a" || e.Facts != sampleFacts {
		t.Fatalf("%+v %v", e, err)
	}
	p, _ = wire.EncodeResponse(wire.Response{OK: true, Status: &sampleStatus})
	r, err := wire.DecodeResponse(p)
	if err != nil || r.Status == nil || r.Status.Epoch != 7 || r.Status.Counters["osc52_dropped"] != 3 ||
		!r.Status.WriterPresent || r.Status.Size.Rows != 40 || r.Status.Caps != wire.DefaultCaps() {
		t.Fatalf("%+v %v", r, err)
	}
	p, _ = wire.EncodeModes(wire.Modes{})
	if string(p) != `{"dec":[],"ansi":[]}` {
		t.Fatalf("empty modes: %s", p)
	}
	p, _ = wire.EncodeModes(wire.Modes{DEC: []int{1, 2}, ANSI: []int{4}})
	if string(p) != `{"dec":[1,2],"ansi":[4]}` {
		t.Fatalf("modes: %s", p)
	}
	m, err := wire.DecodeModes([]byte(`{}`))
	if err != nil || len(m.DEC) != 0 || len(m.ANSI) != 0 {
		t.Fatalf("modes from {}: %+v %v", m, err)
	}
	if got := wire.SortModes([]int{2026, 1, 25, 1, 2026}); fmt.Sprint(got) != "[1 25 2026]" {
		t.Fatalf("SortModes: %v", got)
	}
}

func TestEncodingDoesNotEscapeHTML(t *testing.T) {
	p, err := wire.EncodeNotice(wire.Notice{Reason: wire.ReasonResync, Text: "a<b>&c"})
	if err != nil || !strings.Contains(string(p), "a<b>&c") {
		t.Fatalf("%s %v", p, err)
	}
}

func TestStrictDecoding(t *testing.T) {
	good := `{"columns":80,"rows":24}`
	if _, err := wire.DecodeSize([]byte(good)); err != nil {
		t.Fatal(err)
	}
	if _, err := wire.DecodeSize([]byte("  \n" + good + " \r\n\t")); err != nil {
		t.Fatalf("white space around the object: %v", err)
	}
	for name, p := range map[string]string{
		"empty":            ``,
		"not an object":    `[80,24]`,
		"null":             `null`,
		"number":           `80`,
		"unknown field":    `{"columns":80,"rows":24,"extra":1}`,
		"wrong case":       `{"Columns":80,"rows":24}`,
		"upper case":       `{"COLUMNS":80,"ROWS":24}`,
		"repeated key":     `{"columns":80,"rows":24,"rows":25}`,
		"trailing object":  good + `{}`,
		"trailing garbage": good + ` x`,
		"trailing comma":   `{"columns":80,"rows":24,}`,
		"truncated":        `{"columns":80,"rows":`,
		"string for int":   `{"columns":"80","rows":24}`,
		"float for int":    `{"columns":80.5,"rows":24}`,
		"zero":             `{"columns":0,"rows":24}`,
		"negative":         `{"columns":80,"rows":-1}`,
		"too big":          `{"columns":80,"rows":65536}`,
		"missing":          `{"columns":80}`,
		"deep":             `{"columns":80,"rows":24,"x":` + strings.Repeat(`[`, 100) + strings.Repeat(`]`, 100) + `}`,
		"NUL":              "{\"columns\":80,\"rows\":24}\x00",
	} {
		if _, err := wire.DecodeSize([]byte(p)); !errors.Is(err, wire.ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	// A repeated key and a mis-cased key are refused at a depth too.
	for name, p := range map[string]string{
		"nested repeat": `{"version":1,"ticket":"` + ticket64 + `","name":"n","columns":80,"rows":24,"colors":{"cursor":"#000000","cursor":"#111111"}}`,
		"nested case":   `{"version":1,"ticket":"` + ticket64 + `","name":"n","columns":80,"rows":24,"colors":{"Cursor":"#000000"}}`,
		"nested extra":  `{"version":1,"ticket":"` + ticket64 + `","name":"n","columns":80,"rows":24,"colors":{"x":"#000000"}}`,
	} {
		if _, err := wire.DecodeHello([]byte(p)); !errors.Is(err, wire.ErrInvalid) {
			t.Errorf("hello %s: err = %v", name, err)
		}
	}
	if _, err := wire.DecodeExit([]byte(`{"facts":{"stream_ended":"yes","tui_exited":"yes","tree_gone":"yes"},"counters":{"a":1,"A":2},"last_screen":""}`)); !errors.Is(err, wire.ErrInvalid) {
		t.Errorf("counter name with a capital: %v", err)
	}
	if _, err := wire.DecodeExit([]byte(`{"facts":{"stream_ended":"yes","tui_exited":"yes","tree_gone":"yes"},"counters":{"a":1,"a":2},"last_screen":""}`)); !errors.Is(err, wire.ErrInvalid) {
		t.Errorf("repeated counter: %v", err)
	}
}

func TestStrictDecodingEveryDecoder(t *testing.T) {
	for _, c := range codecs {
		p, err := c.encode(c.sample)
		if err != nil {
			t.Fatal(err)
		}
		bad := map[string][]byte{
			"unknown field": append(append([]byte{}, p[:len(p)-1]...), []byte(`,"zzz":1}`)...),
			"trailing data": append(append([]byte{}, p...), '{', '}'),
			"truncated":     p[:len(p)-1],
			"array":         []byte(`[]`),
			"empty":         nil,
		}
		for name, b := range bad {
			if _, err := c.decode(b); !errors.Is(err, wire.ErrInvalid) {
				t.Errorf("%s %s: err = %v", c.name, name, err)
			}
		}
	}
}

// field replaces the value of a JSON field in a sample payload.
func repl(t *testing.T, p []byte, old, new string) []byte {
	t.Helper()
	if !bytes.Contains(p, []byte(old)) {
		t.Fatalf("%s lacks %s", p, old)
	}
	return bytes.Replace(p, []byte(old), []byte(new), 1)
}

func TestFieldRules(t *testing.T) {
	hello := wire.Hello{Version: 1, Ticket: ticket64, Name: "c", Columns: 80, Rows: 24}
	for name, mod := range map[string]func(*wire.Hello){
		"empty name":          func(h *wire.Hello) { h.Name = "" },
		"65 byte name":        func(h *wire.Hello) { h.Name = strings.Repeat("n", 65) },
		"name with ESC":       func(h *wire.Hello) { h.Name = "a\x1b[31m" },
		"name with newline":   func(h *wire.Hello) { h.Name = "a\nb" },
		"name with C1":        func(h *wire.Hello) { h.Name = "a\u009cb" },
		"name with DEL":       func(h *wire.Hello) { h.Name = "a\x7f" },
		"name not UTF-8":      func(h *wire.Hello) { h.Name = "a\xffb" },
		"short ticket":        func(h *wire.Hello) { h.Ticket = ticket64[:63] },
		"long ticket":         func(h *wire.Hello) { h.Ticket = ticket64 + "0" },
		"upper-case ticket":   func(h *wire.Hello) { h.Ticket = strings.ToUpper(ticket64) },
		"non-hex ticket":      func(h *wire.Hello) { h.Ticket = strings.Repeat("g", 64) },
		"no ticket":           func(h *wire.Hello) { h.Ticket = "" },
		"version zero":        func(h *wire.Hello) { h.Version = 0 },
		"bad colour":          func(h *wire.Hello) { h.Colors.Foreground = "red" },
		"short colour":        func(h *wire.Hello) { h.Colors.Background = "#fff" },
		"colour without hash": func(h *wire.Hello) { h.Colors.Cursor = "0123456" },
		"colour not hex":      func(h *wire.Hello) { h.Colors.Cursor = "#12345g" },
		"zero columns":        func(h *wire.Hello) { h.Columns = 0 },
		"huge rows":           func(h *wire.Hello) { h.Rows = 1 << 20 },
	} {
		h := hello
		mod(&h)
		if _, err := wire.EncodeHello(h); !errors.Is(err, wire.ErrInvalid) {
			t.Errorf("hello %s: Encode err = %v", name, err)
		}
	}
	// A name of exactly 64 bytes and a version other than ours are fine: the
	// holder refuses a version with a Notice.
	h := hello
	h.Name = strings.Repeat("é", 32)
	h.Version = 2
	p, err := wire.EncodeHello(h)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := wire.DecodeHello(p); err != nil || got.Version != 2 {
		t.Fatalf("%+v %v", got, err)
	}
	// The same rules apply to what a peer sends, not only to Encode.
	good, _ := wire.EncodeHello(hello)
	for name, bad := range map[string][]byte{
		"decode long name":      repl(t, good, `"name":"c"`, `"name":"`+strings.Repeat("n", 65)+`"`),
		"decode escaped ESC":    repl(t, good, `"name":"c"`, `"name":"\u001b[31m"`),
		"decode short ticket":   repl(t, good, ticket64, ticket64[:63]),
		"decode escaped C1":     repl(t, good, `"name":"c"`, `"name":"\u009c"`),
		"decode a null ticket":  repl(t, good, `"`+ticket64+`"`, `null`),
		"decode a number name":  repl(t, good, `"name":"c"`, `"name":5`),
		"decode a string width": repl(t, good, `"columns":80`, `"columns":"80"`),
	} {
		if _, err := wire.DecodeHello(bad); !errors.Is(err, wire.ErrInvalid) {
			t.Errorf("%s: err = %v", name, err)
		}
	}

	// Notice and title: the length caps are in bytes.
	if _, err := wire.EncodeNotice(wire.Notice{Reason: wire.ReasonResync, Text: strings.Repeat("t", 256)}); err != nil {
		t.Errorf("256 byte notice: %v", err)
	}
	for name, n := range map[string]wire.Notice{
		"257 byte notice":        {Reason: wire.ReasonResync, Text: strings.Repeat("t", 257)},
		"notice of 129 two-byte": {Reason: wire.ReasonResync, Text: strings.Repeat("é", 129)},
		"notice with ESC":        {Reason: wire.ReasonResync, Text: "\x1b]52;c;x\a"},
		"no reason":              {Text: "x"},
		"reason with capital":    {Reason: "Resync", Text: "x"},
		"reason with space":      {Reason: "re sync", Text: "x"},
		"long reason":            {Reason: wire.Reason(strings.Repeat("r", 65)), Text: "x"},
	} {
		if _, err := wire.EncodeNotice(n); !errors.Is(err, wire.ErrInvalid) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if wire.Reason("future-reason").Known() || !wire.ReasonTooSlow.Known() {
		t.Error("Reason.Known")
	}
	if _, err := wire.EncodeNotice(wire.Notice{Reason: "future-reason", Text: "x"}); err != nil {
		t.Errorf("a well-formed unknown reason: %v", err)
	}
	st := wire.State{Facts: sampleFacts, Idle: "unknown", Title: strings.Repeat("t", 256)}
	if _, err := wire.EncodeState(st); err != nil {
		t.Errorf("256 byte title: %v", err)
	}
	for name, mod := range map[string]func(*wire.State){
		"257 byte title": func(s *wire.State) { s.Title = strings.Repeat("t", 257) },
		"title with BEL": func(s *wire.State) { s.Title = "a\ab" },
		"bad fact":       func(s *wire.State) { s.Facts.TUIExited = "maybe" },
		"no fact":        func(s *wire.State) { s.Facts.TreeGone = "" },
		"status 256":     func(s *wire.State) { s.ExitStatus = intp(256) },
		"status -1":      func(s *wire.State) { s.ExitStatus = intp(-1) },
		"no idle":        func(s *wire.State) { s.Idle = "" },
		"idle capital":   func(s *wire.State) { s.Idle = "Unknown" },
		"17 signals": func(s *wire.State) {
			s.IdleSignals = make([]string, 17)
			for i := range s.IdleSignals {
				s.IdleSignals[i] = "s"
			}
		},
		"empty signal": func(s *wire.State) { s.IdleSignals = []string{""} },
	} {
		s := st
		mod(&s)
		if _, err := wire.EncodeState(s); !errors.Is(err, wire.ErrInvalid) {
			t.Errorf("state %s: err = %v", name, err)
		}
	}

	// Check digests.
	for _, d := range []string{"", strings.Repeat("a", 63), strings.Repeat("a", 65), strings.Repeat("A", 64), strings.Repeat("g", 64)} {
		if _, err := wire.EncodeCheck(wire.Check{Digest: d}); !errors.Is(err, wire.ErrInvalid) {
			t.Errorf("digest %q: %v", d, err)
		}
	}
	var sum [32]byte
	sum[0] = 0xde
	if d := wire.DigestHex(sum); len(d) != wire.DigestLen || !strings.HasPrefix(d, "de00") {
		t.Errorf("DigestHex: %s", d)
	}

	// Role.
	for name, r := range map[string]wire.RoleInfo{
		"no role":         {},
		"role owner":      {Role: "owner"},
		"pid without who": {WriterPID: 5, Role: wire.RoleViewer},
		"negative pid":    {WriterName: "c", WriterPID: -1, Role: wire.RoleViewer},
		"long name":       {WriterName: strings.Repeat("c", 65), WriterPID: 1, Role: wire.RoleViewer},
	} {
		if _, err := wire.EncodeRole(r); !errors.Is(err, wire.ErrInvalid) {
			t.Errorf("role %s: err = %v", name, err)
		}
	}
	if _, err := wire.EncodeRole(wire.RoleInfo{Role: wire.RoleViewer}); err != nil {
		t.Errorf("no writer: %v", err)
	}

	// Welcome.
	w := wire.Welcome{Version: 1, Role: wire.RoleViewer, Lead: "l", Epoch: 1, Runtime: "claude", Caps: wire.DefaultCaps()}
	for name, mod := range map[string]func(*wire.Welcome){
		"bad role":          func(w *wire.Welcome) { w.Role = "admin" },
		"no lead":           func(w *wire.Welcome) { w.Lead = "" },
		"no runtime":        func(w *wire.Welcome) { w.Runtime = "" },
		"min above max":     func(w *wire.Welcome) { w.Caps.MinColumns = 501 },
		"cap above the cap": func(w *wire.Welcome) { w.Caps.ClientPayload = wire.MaxClientPayload + 1 },
		"snapshot cap":      func(w *wire.Welcome) { w.Caps.SnapshotPayload = wire.MaxSnapshotPayload + 1 },
		"zero cap":          func(w *wire.Welcome) { w.Caps.HolderPayload = 0 },
		"version zero":      func(w *wire.Welcome) { w.Version = 0 },
		"long version":      func(w *wire.Welcome) { w.RuntimeVersion = strings.Repeat("1", 65) },
	} {
		x := w
		mod(&x)
		if _, err := wire.EncodeWelcome(x); !errors.Is(err, wire.ErrInvalid) {
			t.Errorf("welcome %s: err = %v", name, err)
		}
	}

	// Modes.
	many := func(n int) []int {
		l := make([]int, n)
		for i := range l {
			l[i] = i + 1
		}
		return l
	}
	if _, err := wire.EncodeModes(wire.Modes{DEC: many(64), ANSI: many(64)}); err != nil {
		t.Errorf("64 modes: %v", err)
	}
	for name, m := range map[string]wire.Modes{
		"65 dec":    {DEC: many(65)},
		"65 ansi":   {ANSI: many(65)},
		"unsorted":  {DEC: []int{5, 4}},
		"repeat":    {ANSI: []int{4, 4}},
		"zero":      {DEC: []int{0}},
		"negative":  {DEC: []int{-1}},
		"too large": {DEC: []int{65536}},
	} {
		if _, err := wire.EncodeModes(m); !errors.Is(err, wire.ErrInvalid) {
			t.Errorf("modes %s: err = %v", name, err)
		}
	}
	if _, err := wire.DecodeModes([]byte(`{"dec":[3,2],"ansi":[]}`)); !errors.Is(err, wire.ErrInvalid) {
		t.Errorf("decoded unsorted modes: %v", err)
	}
	if _, err := wire.DecodeModes([]byte(`{"dec":[1.5],"ansi":[]}`)); !errors.Is(err, wire.ErrInvalid) {
		t.Errorf("decoded fractional mode: %v", err)
	}
}

func TestExitRules(t *testing.T) {
	e := wire.Exit{Facts: sampleFacts}
	// The largest last screen, made of the characters that JSON expands most,
	// still fits in a frame.
	for _, ch := range []string{"\n", `"`, `\`, " ", "x"} {
		e.LastScreen = strings.Repeat(ch, wire.MaxLastScreen/len(ch))
		p, err := wire.EncodeExit(e)
		if err != nil {
			if ch == " " || ch == `"` || ch == `\` {
				// not an error of the cap: U+2028 is not a control character here
				t.Fatalf("%q: %v", ch, err)
			}
			t.Fatalf("%q: %v", ch, err)
		}
		if len(p) > wire.MaxHolderPayload {
			t.Fatalf("%q: payload of %d bytes", ch, len(p))
		}
		if _, err := wire.DecodeExit(p); err != nil {
			t.Fatalf("%q: %v", ch, err)
		}
	}
	e.LastScreen = strings.Repeat("x", wire.MaxLastScreen+1)
	if _, err := wire.EncodeExit(e); !errors.Is(err, wire.ErrInvalid) {
		t.Errorf("last screen over the cap: %v", err)
	}
	for name, s := range map[string]string{"ESC": "a\x1b[2J", "tab": "a\tb", "NUL": "a\x00", "not UTF-8": "\xff"} {
		e.LastScreen = s
		if _, err := wire.EncodeExit(e); !errors.Is(err, wire.ErrInvalid) {
			t.Errorf("last screen with %s: %v", name, err)
		}
	}
	e.LastScreen = ""
	e.Counters = map[string]uint64{}
	for i := 0; i < 257; i++ {
		e.Counters[fmt.Sprintf("c%d", i)] = 1
	}
	if _, err := wire.EncodeExit(e); !errors.Is(err, wire.ErrInvalid) {
		t.Errorf("257 counters: %v", err)
	}
	delete(e.Counters, "c0")
	if _, err := wire.EncodeExit(e); err != nil {
		t.Errorf("256 counters: %v", err)
	}
	for _, name := range []string{"", "Bad", "a b", strings.Repeat("a", 65), "é"} {
		e.Counters = map[string]uint64{name: 1}
		if _, err := wire.EncodeExit(e); !errors.Is(err, wire.ErrInvalid) {
			t.Errorf("counter name %q: %v", name, err)
		}
	}
	e.Counters = nil
	e.ExitStatus = intp(256)
	if _, err := wire.EncodeExit(e); !errors.Is(err, wire.ErrInvalid) {
		t.Errorf("status 256: %v", err)
	}
	// A counter is an unsigned number.
	if _, err := wire.DecodeExit([]byte(`{"facts":{"stream_ended":"yes","tui_exited":"yes","tree_gone":"yes"},"counters":{"a":-1},"last_screen":""}`)); !errors.Is(err, wire.ErrInvalid) {
		t.Errorf("negative counter: %v", err)
	}
}

func TestControlRules(t *testing.T) {
	for _, op := range []wire.Operation{wire.OpStatus, wire.OpStop, wire.OpReleaseWriter, wire.OpInject} {
		if _, err := wire.EncodeRequest(wire.Request{Op: op, Epoch: 1}); err != nil {
			t.Errorf("%s: %v", op, err)
		}
		if _, err := wire.EncodeRequest(wire.Request{Op: op, Epoch: 1, Role: wire.RoleWriter}); !errors.Is(err, wire.ErrInvalid) {
			t.Errorf("%s with a role: %v", op, err)
		}
	}
	for name, r := range map[string]wire.Request{
		"no op":              {Epoch: 1},
		"unknown op":         {Op: "restart", Epoch: 1},
		"ticket without":     {Op: wire.OpTicket, Epoch: 1},
		"ticket bad role":    {Op: wire.OpTicket, Epoch: 1, Role: "root"},
		"capitalised op":     {Op: "Status", Epoch: 1},
		"op with whitespace": {Op: "stop ", Epoch: 1},
	} {
		if _, err := wire.EncodeRequest(r); !errors.Is(err, wire.ErrInvalid) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	for name, r := range map[string]wire.Response{
		"ok with code":        {OK: true, Code: "x"},
		"ok with text":        {OK: true, Text: "x"},
		"ok bad ticket":       {OK: true, Ticket: "abc"},
		"ticket and status":   {OK: true, Ticket: ticket64, Status: &sampleStatus},
		"refusal no code":     {Text: "x"},
		"refusal with ticket": {Code: "x", Ticket: ticket64},
		"refusal with status": {Code: "x", Status: &sampleStatus},
		"refusal long text":   {Code: "x", Text: strings.Repeat("t", 257)},
		"refusal bad code":    {Code: "Not OK"},
		"bad status":          {OK: true, Status: &wire.Status{}},
	} {
		if _, err := wire.EncodeResponse(r); !errors.Is(err, wire.ErrInvalid) {
			t.Errorf("response %s: err = %v", name, err)
		}
	}
	if _, err := wire.EncodeResponse(wire.Response{OK: true}); err != nil {
		t.Errorf("a bare ok: %v", err)
	}
	st := sampleStatus
	st.Counters = nil
	p, err := wire.EncodeResponse(wire.Response{OK: true, Status: &st})
	if err != nil || !strings.Contains(string(p), `"counters":{}`) {
		t.Errorf("nil counters: %s %v", p, err)
	}
	if st.Counters != nil {
		t.Error("Encode changed the caller's status")
	}
}

func TestSanitize(t *testing.T) {
	for _, c := range []struct {
		in   string
		max  int
		nl   bool
		want string
	}{
		{"plain", 10, false, "plain"},
		{"a\x1b[31mb", 20, false, "a�[31mb"},
		{"a\nb", 20, false, "a�b"},
		{"a\nb", 20, true, "a\nb"},
		{"a\xffb", 20, false, "a�b"},
		{"a\u009cb", 20, false, "a�b"},
		{"héllo", 2, false, "h"},
		{"héllo", 3, false, "hé"},
		{"\x1b\x1b\x1b", 7, false, "��"},
		{"", 5, false, ""},
		{"abc", 0, false, ""},
	} {
		got := wire.Sanitize(c.in, c.max, c.nl)
		if got != c.want {
			t.Errorf("Sanitize(%q, %d, %v) = %q, want %q", c.in, c.max, c.nl, got, c.want)
		}
		if len(got) > c.max {
			t.Errorf("Sanitize(%q, %d) is %d bytes", c.in, c.max, len(got))
		}
		if _, err := wire.EncodeNotice(wire.Notice{Reason: wire.ReasonResync, Text: wire.Sanitize(c.in, 256, false)}); err != nil {
			t.Errorf("sanitized %q is refused: %v", c.in, err)
		}
	}
}
