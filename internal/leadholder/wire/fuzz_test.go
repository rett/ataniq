package wire_test

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"ataniq/internal/leadholder/wire"
)

// FuzzReader reads arbitrary bytes as every direction. Whatever it is given,
// a Reader returns frames of its direction within the caps of their types,
// stops at its first error and never reads more than the stream holds.
func FuzzReader(f *testing.F) {
	for _, ft := range frameTypes {
		var buf bytes.Buffer
		_ = wire.Write(&buf, dirOf(ft.typ), ft.typ, bytes.Repeat([]byte{'{', '}'}, 5))
		f.Add(buf.Bytes())
		f.Add(hdr(ft.typ, uint32(ft.max)+1))
		f.Add(hdr(ft.typ, 0xffffffff))
		f.Add(hdr(ft.typ, 20))
	}
	f.Add([]byte{})
	f.Add([]byte{'H'})
	f.Add([]byte{'X', 0, 0, 0, 0})
	for _, c := range codecs {
		p, _ := c.encode(c.sample)
		var buf bytes.Buffer
		_ = wire.Write(&buf, dirOf(c.typ), c.typ, p)
		_ = wire.Write(&buf, wire.ToHolder, wire.TypeDetach, nil)
		f.Add(buf.Bytes())
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		for _, dir := range directions {
			src := &countReader{r: bytes.NewReader(data)}
			rd := wire.NewReader(src, dir)
			total := 0
			var first error
			for i := 0; i < len(data)/wire.HeaderLen+2; i++ {
				fr, err := rd.Next()
				if err != nil {
					first = err
					break
				}
				total += wire.HeaderLen + len(fr.Payload)
				max, ok := wire.Cap(fr.Type)
				if !ok || len(fr.Payload) > max {
					t.Fatalf("%s: frame %q of %d bytes", dir, fr.Type, len(fr.Payload))
				}
				if !frameOfDirection(fr.Type, dir) {
					t.Fatalf("%s: frame %q", dir, fr.Type)
				}
			}
			if first == nil {
				t.Fatalf("%s: no end of stream", dir)
			}
			if total > len(data) || src.n > len(data) {
				t.Fatalf("%s: frames of %d bytes from %d", dir, total, len(data))
			}
			if _, err := rd.Next(); err != first {
				t.Fatalf("%s: reader went on after %v: %v", dir, first, err)
			}
			if first != io.EOF && !errors.Is(first, io.ErrUnexpectedEOF) &&
				!errors.Is(first, wire.ErrUnknownType) && !errors.Is(first, wire.ErrTooLarge) && !errors.Is(first, wire.ErrTooSmall) {
				t.Fatalf("%s: error %v", dir, first)
			}
		}
	})
}

func frameOfDirection(typ byte, dir wire.Direction) bool {
	for _, ft := range frameTypes {
		if ft.typ == typ {
			return ft.dir == dir
		}
	}
	return false
}

// FuzzDecoders feeds arbitrary bytes to every decoder. A payload that decodes
// must encode again, and that encoding must decode and encode to itself: a
// decoder never accepts what the encoder would refuse, and neither panics.
func FuzzDecoders(f *testing.F) {
	for _, c := range codecs {
		p, _ := c.encode(c.sample)
		f.Add(p)
		f.Add(append([]byte{}, p[:len(p)/2]...))
		f.Add(append(append([]byte{}, p...), ' ', '{'))
	}
	for _, s := range []string{
		``, `{}`, `null`, `[]`, `{"columns":80,"rows":24}`, `{"Columns":80,"rows":24}`, `{"columns":80,"columns":80}`,
		`{"dec":[1,2,3],"ansi":null}`, `{"dec":[3,2]}`, `{"op":"ticket","epoch":1,"role":"viewer"}`,
		`{"ok":false,"code":"disabled","text":"x"}`, "{\"name\":\"\\u001b\"}",
		`{"facts":{"stream_ended":"yes","tui_exited":"no","tree_gone":"unknown"},"idle":"unknown","idle_signals":null,"title":""}`,
		`{"facts":{"stream_ended":"yes","tui_exited":"no","tree_gone":"unknown"},"counters":{"a":1},"last_screen":"x\n"}`,
		`{"name":"\ud800"}`, `{"name":"\ud83d\ude00"}`, "{\"name\":\"\xff\"}",
		"\xff\xfe", `{"a":` + string(bytes.Repeat([]byte("["), 50)),
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		for _, c := range codecs {
			v, err := c.decode(data)
			if err != nil {
				if !errors.Is(err, wire.ErrInvalid) {
					t.Fatalf("%s: error %v is not ErrInvalid", c.name, err)
				}
				continue
			}
			e1, err := c.encode(v)
			if err != nil {
				t.Fatalf("%s: decoded %q but cannot encode it: %v", c.name, data, err)
			}
			v2, err := c.decode(e1)
			if err != nil {
				t.Fatalf("%s: own encoding %q does not decode: %v", c.name, e1, err)
			}
			e2, err := c.encode(v2)
			if err != nil || !bytes.Equal(e1, e2) {
				t.Fatalf("%s: encoding not stable: %q then %q (%v)", c.name, e1, e2, err)
			}
			if max, _ := wire.Cap(c.typ); len(e1) > max {
				t.Fatalf("%s: encoding of %d bytes over the cap", c.name, len(e1))
			}
		}
	})
}

// FuzzBinary feeds arbitrary bytes to the binary helpers.
func FuzzBinary(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0, 0, 0, 0, 0, 0, 0, 1, 'a'})
	f.Add([]byte("h\u00e9llo"))
	f.Add([]byte{0xff, 0xfe, 0xc3})
	f.Fuzz(func(t *testing.T, data []byte) {
		for _, pair := range []struct {
			enc func(uint64, []byte) ([]byte, error)
			dec func([]byte) (uint64, []byte, error)
		}{
			{wire.EncodeOutput, wire.DecodeOutput},
			{wire.EncodeSnapshot, wire.DecodeSnapshot},
		} {
			pos, rest, err := pair.dec(data)
			if err != nil {
				if !errors.Is(err, wire.ErrTooSmall) && !errors.Is(err, wire.ErrTooLarge) {
					t.Fatal(err)
				}
				continue
			}
			again, err := pair.enc(pos, rest)
			if err != nil || !bytes.Equal(again, data) {
				t.Fatalf("not stable: %v", err)
			}
		}
		text, err := wire.DecodePaste(data)
		if err != nil {
			if !errors.Is(err, wire.ErrInvalid) {
				t.Fatal(err)
			}
			return
		}
		back, err := wire.EncodePaste(text)
		if err != nil || !bytes.Equal(back, data) {
			t.Fatalf("paste not stable: %v", err)
		}
	})
}

// FuzzSanitize checks that sanitised text always passes the field rules.
func FuzzSanitize(f *testing.F) {
	for _, s := range []string{"", "plain", "a\x1b]0;x\a", "\xff\xfe", "h\u00e9\u009c", "a\nb"} {
		f.Add(s, 8)
	}
	f.Fuzz(func(t *testing.T, s string, max int) {
		if max < 0 || max > 4096 {
			return
		}
		out := wire.Sanitize(s, max, false)
		if len(out) > max {
			t.Fatalf("%d bytes over %d", len(out), max)
		}
		if max > wire.MaxNoticeText {
			return
		}
		if _, err := wire.EncodeNotice(wire.Notice{Reason: wire.ReasonResync, Text: out}); err != nil {
			t.Fatalf("%q: %v", out, err)
		}
		if again := wire.Sanitize(out, max, false); again != out {
			t.Fatalf("not idempotent: %q then %q", out, again)
		}
	})
}

// FuzzVersion checks that the lenient version helpers never panic, only ever
// report a version in range, and agree with the strict decoders on what they
// both accept.
func FuzzVersion(f *testing.F) {
	for _, s := range []string{
		``, `{}`, `{"version":1}`, `{"version":2,"x":[1,{"y":2}]}`, `{"version":1,"version":1}`,
		`{"version":"1"}`, `{"x":` + string(bytes.Repeat([]byte("["), 200)),
	} {
		f.Add([]byte(s))
	}
	for _, c := range codecs {
		if c.name == "hello" || c.name == "welcome" {
			p, _ := c.encode(c.sample)
			f.Add(p)
		}
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if v, ok := wire.HelloVersion(data); ok && (v < 1 || v > wire.MaxDimension) {
			t.Fatalf("hello version %d", v)
		}
		v, ok := wire.WelcomeVersion(data)
		if ok && (v < 1 || v > wire.MaxDimension) {
			t.Fatalf("welcome version %d", v)
		}
		if w, err := wire.DecodeWelcome(data); err == nil {
			if !ok || v != w.Version {
				t.Fatalf("decoded version %d, helper %d %v for %q", w.Version, v, ok, data)
			}
		}
		if h, err := wire.DecodeHello(data); err == nil {
			if hv, hok := wire.HelloVersion(data); !hok || hv != h.Version {
				t.Fatalf("decoded version %d, helper %d %v for %q", h.Version, hv, hok, data)
			}
		}
	})
}
