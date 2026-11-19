//go:build linux && ataniq_canary

package standin

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

type evs []event

func (e evs) String() string {
	var b strings.Builder
	for _, x := range e {
		b.WriteString([]string{"key", "cr", "paste", "7501", "da1"}[x.kind])
		b.WriteString("(" + string(x.data) + ") ")
	}
	return b.String()
}

func TestScanner(t *testing.T) {
	for _, c := range []struct {
		name string
		in   []string
		want evs
	}{
		{"typed keys", []string{"abc"}, evs{{evKey, []byte("abc")}}},
		{"a carriage return", []string{"a\rb"}, evs{{evKey, []byte("a")}, {evCR, nil}, {evKey, []byte("b")}}},
		{"a paste", []string{pasteOf("one\rtwo")}, evs{{evPaste, []byte("one\rtwo")}}},
		{"a paste then a CR", []string{pasteOf("x") + "\r"}, evs{{evPaste, []byte("x")}, {evCR, nil}}},
		{"a CR inside a paste is text", []string{pasteOf("a\rb\r")}, evs{{evPaste, []byte("a\rb\r")}}},
		{"an empty paste", []string{pasteOf("")}, evs{{evPaste, []byte{}}}},
		{"the reply to the query, ST", []string{"\x1b]7501;?\x1b\\"}, evs{{evReply7501, nil}}},
		{"the reply to the query, BEL", []string{"\x1b]7501;?\a"}, evs{{evReply7501, nil}}},
		{"a report is no reply", []string{"\x1b]7501;state=idle\x1b\\"}, nil},
		{"another OSC is dropped", []string{"\x1b]11;rgb:0000/0000/0000\a"}, nil},
		{"a DA1 reply", []string{"\x1b[?62;4c"}, evs{{evReplyDA1, nil}}},
		{"both replies in order", []string{"\x1b]7501;?\x1b\\\x1b[?1;2c"}, evs{{evReply7501, nil}, {evReplyDA1, nil}}},
		{"an arrow key is typed", []string{"\x1b[A"}, evs{{evKey, []byte("\x1b[A")}}},
		{"a plain CSI c is no DA1 reply", []string{"\x1b[c"}, evs{{evKey, []byte("\x1b[c")}}},
		{"alt and a key", []string{"\x1bx"}, evs{{evKey, []byte("\x1bx")}}},
		{"a paste start inside a paste is text", []string{pasteBegin + "a" + pasteBegin + "b" + pasteClose}, evs{{evPaste, []byte("a" + pasteBegin + "b")}}},
		{"an end marker alone is typed bytes", []string{pasteClose + "k"}, evs{{evKey, []byte(pasteClose + "k")}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			var s scanner
			var got evs
			for _, in := range c.in {
				got = append(got, s.feed([]byte(in))...)
			}
			// An empty slice and nil are the same here.
			for i := range got {
				if len(got[i].data) == 0 {
					got[i].data = nil
					if got[i].kind == evPaste {
						got[i].data = []byte{}
					}
				}
			}
			if !reflect.DeepEqual(got, c.want) && !(len(got) == 0 && len(c.want) == 0) {
				t.Fatalf("got %v, want %v", got, c.want)
			}
		})
	}
}

// Every way of cutting the input into reads gives the same events.
func TestScannerSplitAtEveryByte(t *testing.T) {
	in := "ab\x1b]7501;?\x1b\\\x1b[?62c" + pasteOf("canary-token: "+testToken+"\r\nmore") + "\rz"
	var whole scanner
	want := joinKeys(whole.feed([]byte(in)))
	if len(want) != 6 {
		t.Fatalf("whole input gives %v", evs(want))
	}
	for k := 0; k <= len(in); k++ {
		var s scanner
		got := joinKeys(append(s.feed([]byte(in[:k])), s.feed([]byte(in[k:]))...))
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("split at %d: got %v, want %v", k, evs(got), evs(want))
		}
	}
	var s scanner
	var got []event
	for i := 0; i < len(in); i++ {
		got = append(got, s.feed([]byte{in[i]})...)
	}
	if got = joinKeys(got); !reflect.DeepEqual(got, want) {
		t.Fatalf("byte at a time: got %v, want %v", evs(got), evs(want))
	}
}

// joinKeys merges adjacent key events, which a split may separate.
func joinKeys(in []event) []event {
	var out []event
	for _, e := range in {
		if n := len(out); n > 0 && e.kind == evKey && out[n-1].kind == evKey {
			out[n-1].data = append(out[n-1].data, e.data...)
			continue
		}
		out = append(out, event{e.kind, append([]byte(nil), e.data...)})
	}
	return out
}

func TestScannerBoundsAPaste(t *testing.T) {
	var s scanner
	big := bytes.Repeat([]byte("x"), maxPaste+5000)
	got := s.feed([]byte(pasteBegin + string(big) + pasteClose + "k"))
	if len(got) != 2 || got[0].kind != evPaste || got[1].kind != evKey {
		t.Fatalf("events %v", evs(got))
	}
	if len(got[0].data) > maxPaste {
		t.Fatalf("paste kept %d bytes, the cap is %d", len(got[0].data), maxPaste)
	}
	if s.state != stGround {
		t.Fatalf("the scanner is not back in the ground state")
	}
}

func TestScannerBoundsASequence(t *testing.T) {
	var s scanner
	for _, in := range []string{"\x1b[" + strings.Repeat("1", 1000), "\x1b]" + strings.Repeat("x", 1000)} {
		s = scanner{}
		s.feed([]byte(in))
		if len(s.seq) > maxSequence+1 {
			t.Fatalf("kept %d bytes of a sequence", len(s.seq))
		}
		// And the scanner reads on.
		if got := s.feed([]byte("\x1b]7501;?\a")); len(got) != 1 || got[0].kind != evReply7501 {
			t.Fatalf("after an overlong sequence: %v", evs(got))
		}
	}
}
