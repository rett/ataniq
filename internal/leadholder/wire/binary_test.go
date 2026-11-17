package wire_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"testing"

	"ataniq/internal/leadholder/wire"
)

func TestOutputAndSnapshotHelpers(t *testing.T) {
	type pair struct {
		name   string
		typ    byte
		encode func(uint64, []byte) ([]byte, error)
		decode func([]byte) (uint64, []byte, error)
		max    int
	}
	for _, c := range []pair{
		{"output", wire.TypeOutput, wire.EncodeOutput, wire.DecodeOutput, wire.MaxOutputData},
		{"snapshot", wire.TypeSnapshot, wire.EncodeSnapshot, wire.DecodeSnapshot, wire.MaxSnapshotData},
	} {
		t.Run(c.name, func(t *testing.T) {
			for _, pos := range []uint64{0, 1, 1 << 32, 1<<64 - 1} {
				for _, data := range [][]byte{nil, {}, []byte("\x1b[2Jhello"), {0, 1, 2, 0xff}} {
					p, err := c.encode(pos, data)
					if err != nil {
						t.Fatal(err)
					}
					if len(p) != 8+len(data) || binary.BigEndian.Uint64(p) != pos {
						t.Fatalf("payload % x", p)
					}
					gotPos, gotData, err := c.decode(p)
					if err != nil || gotPos != pos || !bytes.Equal(gotData, data) {
						t.Fatalf("pos %d data %q: got %d %q %v", pos, data, gotPos, gotData, err)
					}
					// It passes through a frame of its own type.
					var buf bytes.Buffer
					if err := wire.Write(&buf, c.typ, p); err != nil {
						t.Fatal(err)
					}
					f, err := wire.NewReader(&buf, wire.ToClient).Next()
					if err != nil || !bytes.Equal(f.Payload, p) {
						t.Fatal(err)
					}
				}
			}
			// The cap on the data is the cap on the frame less the position.
			p, err := c.encode(5, make([]byte, c.max))
			if err != nil {
				t.Fatalf("data at the cap: %v", err)
			}
			if err := wire.Write(io.Discard, c.typ, p); err != nil {
				t.Fatalf("frame at the cap: %v", err)
			}
			if _, err := c.encode(5, make([]byte, c.max+1)); !errors.Is(err, wire.ErrTooLarge) {
				t.Fatalf("data over the cap: %v", err)
			}
			if _, _, err := c.decode(make([]byte, c.max+9)); !errors.Is(err, wire.ErrTooLarge) {
				t.Fatalf("payload over the cap: %v", err)
			}
			for n := 0; n < 8; n++ {
				if _, _, err := c.decode(make([]byte, n)); !errors.Is(err, wire.ErrTooSmall) {
					t.Fatalf("%d byte payload: %v", n, err)
				}
			}
			if _, _, err := c.decode(nil); !errors.Is(err, wire.ErrTooSmall) {
				t.Fatalf("nil payload: %v", err)
			}
		})
	}
	// The data is a view of the payload, not a copy.
	p, _ := wire.EncodeOutput(1, []byte("abc"))
	_, data, _ := wire.DecodeOutput(p)
	data[0] = 'X'
	if p[8] != 'X' {
		t.Error("DecodeOutput copied")
	}
	if wire.MaxOutputData != wire.MaxHolderPayload-8 || wire.MaxSnapshotData != wire.MaxSnapshotPayload-8 {
		t.Error("data caps")
	}
}

func TestPaste(t *testing.T) {
	for _, s := range []string{"", "text", "line\nline\ttab", "h\u00e9llo \U0001F600", "\x1b[201~"} {
		p, err := wire.EncodePaste(s)
		if err != nil {
			t.Fatalf("%q: %v", s, err)
		}
		got, err := wire.DecodePaste(p)
		if err != nil || got != s {
			t.Fatalf("%q: %q %v", s, got, err)
		}
	}
	if _, err := wire.EncodePaste(strings.Repeat("a", wire.MaxClientPayload)); err != nil {
		t.Errorf("paste at the cap: %v", err)
	}
	if _, err := wire.EncodePaste(strings.Repeat("a", wire.MaxClientPayload+1)); !errors.Is(err, wire.ErrTooLarge) {
		t.Errorf("paste over the cap: %v", err)
	}
	if _, err := wire.EncodePaste("a\xffb"); !errors.Is(err, wire.ErrInvalid) {
		t.Errorf("invalid UTF-8: %v", err)
	}
	if _, err := wire.DecodePaste([]byte("a\xc3")); !errors.Is(err, wire.ErrInvalid) {
		t.Errorf("truncated UTF-8: %v", err)
	}
	if _, err := wire.DecodePaste(make([]byte, wire.MaxClientPayload+1)); !errors.Is(err, wire.ErrTooLarge) {
		t.Errorf("payload over the cap: %v", err)
	}
}

func TestInputIsRawBytes(t *testing.T) {
	raw := []byte{0x1b, '[', 'A', 0xff, 0x00, 0x80}
	var buf bytes.Buffer
	if err := wire.Write(&buf, wire.TypeInput, raw); err != nil {
		t.Fatal(err)
	}
	f, err := wire.NewReader(&buf, wire.ToHolder).Next()
	if err != nil || f.Type != wire.TypeInput || !bytes.Equal(f.Payload, raw) {
		t.Fatalf("%v %v", f, err)
	}
}

type fixedReader byte

func (b fixedReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(b)
	}
	return len(p), nil
}

func TestTicket(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		tk, err := wire.NewTicket()
		if err != nil {
			t.Fatal(err)
		}
		if err := wire.CheckTicket(tk); err != nil || len(tk) != wire.TicketLen || wire.TicketLen != 64 {
			t.Fatalf("%q: %v", tk, err)
		}
		if seen[tk] {
			t.Fatalf("ticket %q twice", tk)
		}
		seen[tk] = true
	}
	tk, err := wire.NewTicketFrom(fixedReader(0xab))
	if err != nil || tk != strings.Repeat("ab", 32) {
		t.Fatalf("%q %v", tk, err)
	}
	if _, err := wire.NewTicketFrom(io.LimitReader(fixedReader(1), 31)); err == nil {
		t.Fatal("a short source made a ticket")
	}
	for _, bad := range []string{"", "abc", strings.Repeat("a", 63), strings.Repeat("a", 65), strings.Repeat("A", 64), strings.Repeat("z", 64), strings.Repeat("a", 63) + " ", strings.Repeat("a", 63) + "\n"} {
		if err := wire.CheckTicket(bad); !errors.Is(err, wire.ErrInvalid) {
			t.Errorf("CheckTicket(%q) = %v", bad, err)
		}
	}
}
