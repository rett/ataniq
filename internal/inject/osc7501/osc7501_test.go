package osc7501

import (
	"bufio"
	"encoding/base64"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

// recorded reads testdata/reports.txt: tab separated source, raw payload,
// state, app, kind, msg, title; "-" is an empty field.
type recordedLine struct {
	source, payload string
	want            Report
}

func recorded(t *testing.T) []recordedLine {
	t.Helper()
	f, err := os.Open("testdata/reports.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []recordedLine
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		c := strings.Split(line, "\t")
		if len(c) != 7 {
			t.Fatalf("testdata line has %d columns, want 7: %q", len(c), line)
		}
		dash := func(s string) string {
			if s == "-" {
				return ""
			}
			return s
		}
		text := func(s string) string {
			if s == "-" {
				return ""
			}
			u, err := strconv.Unquote(s)
			if err != nil {
				t.Fatalf("testdata text %q: %v", s, err)
			}
			return u
		}
		out = append(out, recordedLine{c[0], c[1], Report{
			State: State(c[2]), App: dash(c[3]), Kind: dash(c[4]), Msg: text(c[5]), Title: text(c[6]),
		}})
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestRecordedReports(t *testing.T) {
	lines := recorded(t)
	if len(lines) < 9 {
		t.Fatalf("only %d recorded payloads", len(lines))
	}
	seen := map[State]bool{}
	for _, l := range lines {
		t.Run(l.source+"/"+l.payload, func(t *testing.T) {
			if IsQuery([]byte(l.payload)) {
				t.Fatal("a report is not a query")
			}
			got, err := Parse([]byte(l.payload))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if got != l.want {
				t.Fatalf("Parse = %+v, want %+v", got, l.want)
			}
			// The emulator may pass the data with its command number.
			again, err := Parse([]byte("7501;" + l.payload))
			if err != nil || again != got {
				t.Fatalf("with prefix: %+v, %v", again, err)
			}
		})
		seen[l.want.State] = true
	}
	for _, s := range []State{StateIdle, StateWorking, StateDone, StateBlocked, StateClear} {
		if !seen[s] {
			t.Errorf("no recorded payload for state %q", s)
		}
	}
}

// TestRecordedScrubbed keeps real home paths out of the recorded data: a msg
// that holds a home path may only hold the placeholder.
func TestRecordedScrubbed(t *testing.T) {
	for _, l := range recorded(t) {
		for _, text := range []string{l.want.Msg, l.want.Title} {
			for rest := text; ; {
				_, after, ok := strings.Cut(rest, homePrefix)
				if !ok {
					break
				}
				if !strings.HasPrefix(after, "user/") {
					t.Errorf("%q holds a home path other than /home/user", text)
				}
				rest = after
			}
		}
	}
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func TestParseForms(t *testing.T) {
	tests := []struct {
		name, payload string
		want          Report
	}{
		{"claude error", "state=error:app=claude-code", Report{State: StateError, App: "claude-code"}},
		{"claude question", "state=blocked:app=claude-code:kind=question", Report{State: StateBlocked, App: "claude-code", Kind: "question"}},
		{"claude auth", "state=blocked:app=claude-code:kind=auth", Report{State: StateBlocked, App: "claude-code", Kind: "auth"}},
		{"title", "state=working:app=claude-code:title=" + b64("fix the build"), Report{State: StateWorking, App: "claude-code", Title: "fix the build"}},
		{"msg and title", "state=blocked:app=claude-code:kind=permission:msg=" + b64("ok?") + ":title=" + b64("t"), Report{State: StateBlocked, App: "claude-code", Kind: "permission", Msg: "ok?", Title: "t"}},
		{"empty msg", "state=idle:msg=", Report{State: StateIdle}},
		{"state only", "state=idle", Report{State: StateIdle}},
		{"any order", "app=codex:state=done", Report{State: StateDone, App: "codex"}},
		{"unknown keys counted", "state=idle:x=1:y=:app=codex:z=a=b", Report{State: StateIdle, App: "codex", Unknown: 3}},
		{"padding kept", "state=idle:msg=" + b64("a"), Report{State: StateIdle, Msg: "a"}},
		{"non-ASCII text", "state=idle:title=" + b64("Größe ✳ 日本"), Report{State: StateIdle, Title: "Größe ✳ 日本"}},
		{"control characters replaced", "state=idle:msg=" + b64("a\x1b[31m\nb\x7f\u0085c‮d "), Report{State: StateIdle, Msg: "a�[31m�b��c�d�"}},
		{"app at the limit", "state=idle:app=" + strings.Repeat("a", 64), Report{State: StateIdle, App: strings.Repeat("a", 64)}},
		{"payload at the limit", "state=idle:" + "k=" + strings.Repeat("v", MaxPayload-len("state=idle:k=")), Report{State: StateIdle, Unknown: 1}},
		{"text cut at a character", "state=idle:msg=" + b64(strings.Repeat("é", 700)), Report{State: StateIdle, Msg: strings.Repeat("é", 512)}},
		{"text cut after replacement", "state=idle:msg=" + b64(strings.Repeat("\x01", 2000)), Report{State: StateIdle, Msg: strings.Repeat("�", 341)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse([]byte(tt.payload))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if got != tt.want {
				t.Fatalf("Parse = %+v, want %+v", got, tt.want)
			}
			if !utf8.ValidString(got.Msg) || !utf8.ValidString(got.Title) || len(got.Msg) > MaxText || len(got.Title) > MaxText {
				t.Fatalf("text out of bounds: %+v", got)
			}
		})
	}
}

func TestParseHostile(t *testing.T) {
	many := make([]string, 10000)
	for i := range many {
		many[i] = "k" + strconv.Itoa(i) + "=1"
	}
	manyShort := strings.Repeat("a=1:", 1000)
	tests := []struct {
		name, payload string
		err           error
	}{
		{"empty", "", ErrEmpty},
		{"only the prefix", "7501;", ErrEmpty},
		{"query", "?", ErrField},
		{"over-long", "state=idle:k=" + strings.Repeat("v", MaxPayload), ErrTooLong},
		{"one byte over", "state=idle:k=" + strings.Repeat("v", MaxPayload-len("state=idle:k=")+1), ErrTooLong},
		{"bad base64", "state=idle:msg=!!!!", ErrText},
		{"unpadded base64", "state=idle:msg=YQ", ErrText},
		{"url base64", "state=idle:msg=_-_-", ErrText},
		{"invalid UTF-8 in msg", "state=idle:msg=" + base64.StdEncoding.EncodeToString([]byte{'a', 0xff, 0xfe}), ErrText},
		{"invalid UTF-8 in title", "state=idle:title=" + base64.StdEncoding.EncodeToString([]byte{0xc3}), ErrText},
		{"unknown state", "state=sleeping", ErrState},
		{"state in capitals", "state=IDLE", ErrState},
		{"empty state", "state=", ErrState},
		{"missing state", "app=codex", ErrState},
		{"duplicate state", "state=idle:state=working", ErrDuplicate},
		{"duplicate unknown key", "state=idle:x=1:x=2", ErrDuplicate},
		{"duplicate msg", "state=idle:msg=:msg=", ErrDuplicate},
		{"control character in app", "state=idle:app=co\x1bdex", ErrToken},
		{"newline in app", "state=idle:app=a\nb", ErrToken},
		{"space in app", "state=idle:app=a b", ErrToken},
		{"empty app", "state=idle:app=", ErrToken},
		{"non-ASCII app", "state=idle:app=cödex", ErrToken},
		{"long app", "state=idle:app=" + strings.Repeat("a", 65), ErrToken},
		{"punctuation in kind", "state=blocked:kind=a.b", ErrToken},
		{"field without value", "state=idle:oops", ErrField},
		{"empty key", "state=idle:=1", ErrField},
		{"empty field", "state=idle::app=codex", ErrField},
		{"trailing colon", "state=idle:", ErrField},
		{"control character in key", "state=idle:a\x00b=1", ErrField},
		{"10000 fields", strings.Join(many, ":"), ErrTooLong},
		{"many short fields", manyShort, ErrDuplicate},
		{"33 fields", "state=idle" + func() string {
			var b strings.Builder
			for i := 0; i < 32; i++ {
				b.WriteString(":k" + strconv.Itoa(i) + "=1")
			}
			return b.String()
		}(), ErrField},
		{"NUL bytes", "\x00\x00\x00", ErrField},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse([]byte(tt.payload))
			if !errors.Is(err, tt.err) {
				t.Fatalf("Parse error = %v, want %v", err, tt.err)
			}
			if got != (Report{}) {
				t.Fatalf("an error returned %+v, want the zero Report", got)
			}
		})
	}
}

func TestParseFieldLimit(t *testing.T) {
	var b strings.Builder
	b.WriteString("state=idle")
	for i := 0; i < MaxFields-1; i++ {
		b.WriteString(":k" + strconv.Itoa(i) + "=1")
	}
	got, err := Parse([]byte(b.String()))
	if err != nil || got.Unknown != MaxFields-1 {
		t.Fatalf("%d fields: %+v, %v", MaxFields, got, err)
	}
}

func TestParseDoesNotAliasInput(t *testing.T) {
	in := []byte("state=idle:app=codex")
	got, err := Parse(in)
	if err != nil {
		t.Fatal(err)
	}
	for i := range in {
		in[i] = 'x'
	}
	if got.App != "codex" || got.State != StateIdle {
		t.Fatalf("report changed with its input: %+v", got)
	}
}

func TestQuery(t *testing.T) {
	for _, p := range []string{"?", "7501;?", "?extra", "7501;?:x=1"} {
		if !IsQuery([]byte(p)) {
			t.Errorf("IsQuery(%q) = false", p)
		}
		r, ok := Reply([]byte(p))
		if !ok || string(r) != QueryReply {
			t.Errorf("Reply(%q) = %q, %v", p, r, ok)
		}
	}
	for _, p := range []string{"", "7501;", "state=idle", "7501;7501;?", " ?", "state=?"} {
		if IsQuery([]byte(p)) {
			t.Errorf("IsQuery(%q) = true", p)
		}
		if r, ok := Reply([]byte(p)); ok || r != nil {
			t.Errorf("Reply(%q) = %q, %v", p, r, ok)
		}
	}
	if QueryReply != "\x1b]7501;?\x1b\\" {
		t.Errorf("QueryReply = %q", QueryReply)
	}
}

func TestReplyIsACopy(t *testing.T) {
	a, _ := Reply([]byte("?"))
	a[0] = 'X'
	b, _ := Reply([]byte("?"))
	if string(b) != QueryReply {
		t.Fatalf("a reply was shared: %q", b)
	}
}

func FuzzParse(f *testing.F) {
	for _, l := range []string{
		"", "?", "7501;", "state=idle", "state=idle:state=idle", "state=idle:msg=%%%", "state=idle:app=\x1b",
		"state=blocked:app=claude-code:kind=permission:msg=YXBwcm92ZQ==:title=YQ==", "=:=:=", "::::", "state=idle:msg=/w==",
	} {
		f.Add([]byte(l))
	}
	f.Fuzz(func(t *testing.T, payload []byte) {
		r, err := Parse(payload)
		if err != nil && r != (Report{}) {
			t.Fatalf("error %v with a non-zero report %+v", err, r)
		}
	})
}

// homePrefix starts a home path. It is split in two so that a grep for home
// paths in the tree finds only real ones.
const homePrefix = "/home" + "/"
