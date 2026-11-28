package wire_test

import (
	"bytes"
	"errors"
	"math/rand"
	"testing"

	"ataniq/internal/leadholder/wire"
)

func historyRow(n int, fill byte) wire.HistoryEntry {
	return wire.HistoryEntry{Row: bytes.Repeat([]byte{fill}, n)}
}

func TestCF04Recent(t *testing.T) {
	t.Run("FrameCappedAndVersioned", func(t *testing.T) {
		h := wire.History{
			Position: 1 << 40,
			Oldest:   7,
			Entries: []wire.HistoryEntry{
				historyRow(2, 'a'), {Omitted: 3}, historyRow(40, 'b'), historyRow(1, 'c'), {Omitted: 1},
			},
		}
		enc, err := wire.EncodeHistory(h)
		if err != nil {
			t.Fatal(err)
		}
		if enc[0] != wire.HistoryVersion || wire.HistoryVersion != 1 {
			t.Fatalf("version byte %d", enc[0])
		}
		got, err := wire.DecodeHistory(enc)
		if err != nil || got.Position != h.Position || got.Oldest != 7 || got.Approximate || len(got.Entries) != 5 || got.Rows() != 7 {
			t.Fatalf("round trip: %+v %v", got, err)
		}
		for i := range h.Entries {
			if !bytes.Equal(got.Entries[i].Row, h.Entries[i].Row) || got.Entries[i].Omitted != h.Entries[i].Omitted {
				t.Fatalf("entry %d changed", i)
			}
		}
		// Encoding is deterministic, and the flag is carried.
		h.Approximate = true
		enc2, _ := wire.EncodeHistory(h)
		if d, err := wire.DecodeHistory(enc2); err != nil || !d.Approximate || bytes.Equal(enc, enc2) {
			t.Fatalf("approximate flag: %+v %v", d, err)
		}
		// An empty ring is a frame of its own, and is not an interval.
		empty, err := wire.EncodeHistory(wire.History{Position: 5})
		if err != nil || len(empty) != 22 {
			t.Fatalf("empty: %d %v", len(empty), err)
		}
		if d, err := wire.DecodeHistory(empty); err != nil || len(d.Entries) != 0 || d.Rows() != 0 {
			t.Fatalf("empty decode: %+v %v", d, err)
		}

		// The version is the first byte, and another one is its own error.
		bad := bytes.Clone(enc)
		bad[0] = 2
		if _, err := wire.DecodeHistory(bad); !errors.Is(err, wire.ErrHistoryVersion) || !errors.Is(err, wire.ErrInvalid) {
			t.Fatalf("version 2: %v", err)
		}
		bad[0] = 0
		if _, err := wire.DecodeHistory(bad); !errors.Is(err, wire.ErrHistoryVersion) {
			t.Fatalf("version 0: %v", err)
		}
		// Every undefined flag, every cut and any byte left over is refused.
		for f := 2; f < 256; f <<= 1 {
			bad = bytes.Clone(enc)
			bad[1] = byte(f)
			if _, err := wire.DecodeHistory(bad); !errors.Is(err, wire.ErrInvalid) {
				t.Fatalf("flag %#x: %v", f, err)
			}
		}
		for i := range enc {
			if _, err := wire.DecodeHistory(enc[:i]); err == nil {
				t.Fatalf("a payload cut at %d decoded", i)
			}
		}
		if _, err := wire.DecodeHistory(append(bytes.Clone(enc), 0)); !errors.Is(err, wire.ErrInvalid) {
			t.Fatalf("left over: %v", err)
		}
		// A count that the bytes cannot back is refused before any allocation.
		bad = bytes.Clone(enc[:22])
		bad[18], bad[19], bad[20], bad[21] = 0, 0, 0x40, 0
		if _, err := wire.DecodeHistory(bad); !errors.Is(err, wire.ErrTooLarge) && !errors.Is(err, wire.ErrInvalid) {
			t.Fatalf("count: %v", err)
		}
		bad[18] = 0xff
		if _, err := wire.DecodeHistory(bad); !errors.Is(err, wire.ErrTooLarge) {
			t.Fatalf("huge count: %v", err)
		}

		// Rules of the entries, on encode and on decode.
		for name, h := range map[string]wire.History{
			"empty entry":      {Entries: []wire.HistoryEntry{{}}},
			"row and interval": {Entries: []wire.HistoryEntry{{Row: []byte{1, 2}, Omitted: 1}}},
			"row over the cap": {Entries: []wire.HistoryEntry{historyRow(wire.MaxHistoryRowBytes+1, 'x')}},
			"interval over":    {Entries: []wire.HistoryEntry{{Omitted: wire.MaxHistoryRows + 1}}},
			"two intervals":    {Entries: []wire.HistoryEntry{{Omitted: 1}, {Omitted: 1}}},
			"numbers wrap":     {Oldest: ^uint64(0), Entries: []wire.HistoryEntry{historyRow(2, 'a')}},
		} {
			if _, err := wire.EncodeHistory(h); !errors.Is(err, wire.ErrInvalid) && !errors.Is(err, wire.ErrTooLarge) {
				t.Errorf("%s: encoded (%v)", name, err)
			}
		}
		// Caps: rows, entries and bytes. Encoding refuses, Fit cuts.
		many := wire.History{Position: 9, Oldest: 100}
		for range wire.MaxHistoryEntries + 5 {
			many.Entries = append(many.Entries, historyRow(2, 'r'))
		}
		if _, err := wire.EncodeHistory(many); !errors.Is(err, wire.ErrTooLarge) {
			t.Fatalf("too many entries: %v", err)
		}
		fit := many.Fit()
		if len(fit.Entries) != wire.MaxHistoryEntries || fit.Oldest != 105 || fit.Position != 9 || len(many.Entries) != wire.MaxHistoryEntries+5 {
			t.Fatalf("fit: %d entries, oldest %d", len(fit.Entries), fit.Oldest)
		}
		if _, err := wire.EncodeHistory(fit); err != nil {
			t.Fatalf("a fitted history: %v", err)
		}
		heavy := wire.History{Oldest: 1}
		for i := 0; i < 200; i++ {
			heavy.Entries = append(heavy.Entries, historyRow(wire.MaxHistoryRowBytes, byte(i)))
		}
		if _, err := wire.EncodeHistory(heavy); !errors.Is(err, wire.ErrTooLarge) {
			t.Fatalf("too many bytes: %v", err)
		}
		hf := heavy.Fit()
		enc3, err := wire.EncodeHistory(hf)
		if err != nil || len(enc3) > wire.MaxHistoryPayload || hf.Oldest != 1+uint64(200-len(hf.Entries)) ||
			hf.Entries[len(hf.Entries)-1].Row[0] != 199 {
			t.Fatalf("fit of heavy rows: %d entries, %d bytes, oldest %d, %v", len(hf.Entries), len(enc3), hf.Oldest, err)
		}
		// An interval that is too long is shortened from its old end.
		big := wire.History{Oldest: 4, Entries: []wire.HistoryEntry{{Omitted: wire.MaxHistoryRows}, historyRow(2, 'z')}}
		if bf := big.Fit(); len(bf.Entries) != 2 || bf.Entries[0].Omitted != wire.MaxHistoryRows-1 || bf.Oldest != 5 {
			t.Fatalf("interval fit: %+v", bf)
		}
		// A history that fits is returned as it is.
		if f := h.Fit(); len(f.Entries) != len(h.Entries) {
			t.Fatal("a history that fits was cut")
		}

		// As a frame: its direction is the client's, and its cap the frame's.
		var buf bytes.Buffer
		if err := wire.Write(&buf, wire.ToClient, wire.TypeHistory, enc); err != nil {
			t.Fatal(err)
		}
		f, err := wire.NewReader(bytes.NewReader(buf.Bytes()), wire.ToClient).Next()
		if err != nil || f.Type != wire.TypeHistory || !bytes.Equal(f.Payload, enc) {
			t.Fatalf("frame: %v", err)
		}
		if wire.TypeName(wire.TypeHistory) != "history" {
			t.Fatal("name")
		}
		if err := wire.Write(&buf, wire.ToHolder, wire.TypeHistory, enc); !errors.Is(err, wire.ErrUnknownType) {
			t.Fatalf("a client sent a history: %v", err)
		}
		if err := wire.Write(&buf, wire.ToClient, wire.TypeHistory, make([]byte, wire.MaxHistoryPayload+1)); !errors.Is(err, wire.ErrTooLarge) {
			t.Fatalf("over the frame cap: %v", err)
		}
		if err := wire.Write(&buf, wire.ToClient, wire.TypeHistory, enc[:21]); !errors.Is(err, wire.ErrTooSmall) {
			t.Fatalf("under the least: %v", err)
		}

		// Garbage never panics and a payload that decodes encodes back.
		rng := rand.New(rand.NewSource(4))
		for range 3000 {
			p := bytes.Clone(enc)
			for range 1 + rng.Intn(4) {
				p[rng.Intn(len(p))] = byte(rng.Intn(256))
			}
			if rng.Intn(3) == 0 {
				p = p[:rng.Intn(len(p))]
			}
			if d, err := wire.DecodeHistory(p); err == nil {
				if again, err := wire.EncodeHistory(d); err != nil || !bytes.Equal(again, p) {
					t.Fatalf("decoded payload does not encode back: %v", err)
				}
			}
		}
	})
}
