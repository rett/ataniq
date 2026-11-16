package ansi

import (
	"slices"
	"testing"

	"github.com/charmbracelet/x/ansi/parser"
)

type decodedSeq struct {
	seq   string
	width int
	cmd   int
	data  string
}

// decodeAll decodes b the way a caller loops over DecodeSequence, carrying
// the state, and returns the sequences and the final state.
func decodeAll(b []byte) ([]decodedSeq, byte) {
	p := NewParser()
	var out []decodedSeq
	var state byte
	for len(b) > 0 {
		seq, width, n, newState := DecodeSequence(b, state, p)
		out = append(out, decodedSeq{string(seq), width, p.Command(), string(p.Data())})
		state = newState
		b = b[n:]
	}
	return out, state
}

// TestDecodeSequenceUtf8NotST checks that DecodeSequence keeps a 0x9C byte
// inside a UTF-8 character as string data, and still ends the string on ESC
// \, BEL (OSC), and 8-bit ST after a complete character. Each input is also
// cut at every byte: a prefix must decode as an unfinished string, not as a
// sequence ended inside a character. (A prefix that ends on the ESC of ESC \
// is left out: DecodeSequence cancels the string there, as before.)
func TestDecodeSequenceUtf8NotST(t *testing.T) {
	const (
		sparkle = "✳" // E2 9C B3
		check   = "✅" // E2 9C 85
	)
	cases := []struct {
		name  string
		in    string
		start int // index of the first data byte
		cmd   int
		data  string
	}{
		{"osc0_title_bel", "\x1b]0;" + sparkle + " Claude Code\x07", 2, 0, "0;" + sparkle + " Claude Code"},
		{"osc2_title_esc_st", "\x1b]2;" + check + " done\x1b\\", 2, 2, "2;" + check + " done"},
		{"osc_title_report", "\x1b]l" + sparkle + check + "\x1b\\", 2, parser.MissingCommand, "l" + sparkle + check},
		{"osc52_reply", "\x1b]52;c;" + sparkle + check + "\x07", 2, 52, "52;c;" + sparkle + check},
		{"osc2_two_byte", "\x1b]2;Ü М\x07", 2, 2, "2;Ü М"},
		{"osc2_third_byte", "\x1b]2;ぜ\x07", 2, 2, "2;ぜ"},
		{"osc2_four_byte_two_9c", "\x1b]2;\U0001F71C\x07", 2, 2, "2;\U0001F71C"},
		{"osc_8bit_st_after_char", "\x1b]2;" + check + "\x9c", 2, 2, "2;" + check},
		{"osc_8bit_st_after_ascii", "\x1b]2;abc\x9c", 2, 2, "2;abc"},
		{"c1_osc_c1_st", "\x9d2;" + sparkle + "\x9c", 1, 2, "2;" + sparkle},
		{"dcs_reply", "\x1bP1$r" + sparkle + "\x1b\\", 6, 'r' | '$'<<parser.IntermedShift, sparkle},
		{"apc", "\x1b_G" + check + "\x1b\\", 2, parser.MissingCommand, "G" + check},
		{"sos", "\x1bX" + sparkle + "\x1b\\", 2, parser.MissingCommand, sparkle},
		{"pm", "\x1b^" + sparkle + "\x9c", 2, parser.MissingCommand, sparkle},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, state := decodeAll([]byte(c.in))
			want := []decodedSeq{{c.in, 0, c.cmd, c.data}}
			if !slices.Equal(got, want) || state != NormalState {
				t.Fatalf("whole: got %q state %d, want %q", got, state, want)
			}
			for k := c.start; k < len(c.in); k++ {
				if c.in[k-1] == ESC {
					continue
				}
				prefix := []byte(c.in[:k])
				seq, _, n, newState := DecodeSequence(prefix, NormalState, NewParser())
				if n != k || newState != StringState || string(seq) != c.in[:k] {
					t.Fatalf("prefix %q: got seq %q n=%d state %d, want an unfinished string", prefix, seq, n, newState)
				}
			}
		})
	}
}

// TestDecodeSequenceKeysUnchanged checks key, paste and mouse input, whole
// and cut at every byte: the sequences decoded from a prefix are the
// expected ones, the last possibly unfinished.
func TestDecodeSequenceKeysUnchanged(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"csi_u_ctrl_backslash", "\x1b[92;5u", []string{"\x1b[92;5u"}},
		{"c0_fs", "\x1c", []string{"\x1c"}},
		{"kitty_keys", "\x1b[97;5u\x1b[1;5A\x1b[57441;2u\x1b[13;2u", []string{"\x1b[97;5u", "\x1b[1;5A", "\x1b[57441;2u", "\x1b[13;2u"}},
		{"bracketed_paste", "\x1b[200~✳ ✅x\x1b[201~", []string{"\x1b[200~", "✳", " ", "✅", "x", "\x1b[201~"}},
		{"sgr_mouse", "\x1b[<0;10;5M\x1b[<0;10;5m\x1b[<64;3;4M", []string{"\x1b[<0;10;5M", "\x1b[<0;10;5m", "\x1b[<64;3;4M"}},
		{"text_with_9c", "✳aÜ", []string{"✳", "a", "Ü"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			seqs := func(b string) []string {
				got, _ := decodeAll([]byte(b))
				var s []string
				for _, g := range got {
					s = append(s, g.seq)
				}
				return s
			}
			if got := seqs(c.in); !slices.Equal(got, c.want) {
				t.Fatalf("whole: got %q, want %q", got, c.want)
			}
			for k := 1; k < len(c.in); k++ {
				got := seqs(c.in[:k])
				if len(got) > len(c.want) {
					t.Fatalf("prefix %q: got %q, want a prefix of %q", c.in[:k], got, c.want)
				}
				last := len(got) - 1
				if !slices.Equal(got[:last], c.want[:last]) || len(got[last]) > len(c.want[last]) || got[last] != c.want[last][:len(got[last])] {
					t.Fatalf("prefix %q: got %q, want a prefix of %q", c.in[:k], got, c.want)
				}
			}
		})
	}
}
