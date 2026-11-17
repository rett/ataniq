package ansi

import (
	"testing"

	"github.com/charmbracelet/x/ansi/parser"
)

// TestStringDataUtf8NotST checks that a 0x9C byte inside a UTF-8 character in
// string data is data, while ESC \, BEL (OSC only), 8-bit ST after a complete
// character, CAN and SUB still end the string. Each input is also fed in two
// parts, split at every byte.
func TestStringDataUtf8NotST(t *testing.T) {
	const (
		sparkle = "✳"          // E2 9C B3
		check   = "✅"          // E2 9C 85
		ideo    = "末"          // E6 9C AB
		emoji   = "\U0001F71C" // F0 9F 9C 9C: 0x9C twice
	)
	osc := func(s string) []any { return []any{[]byte(s)} }
	oscEsc := func(s string) []any { return []any{[]byte(s), Cmd('\\')} }
	cases := []testCase{
		{"osc0_sparkle_bel", "\x1b]0;" + sparkle + " Claude Code\x07", osc("0;" + sparkle + " Claude Code")},
		{"osc2_sparkle_bel", "\x1b]2;" + sparkle + " Claude Code\x07", osc("2;" + sparkle + " Claude Code")},
		{"osc0_check_esc_st", "\x1b]0;" + check + " done\x1b\\", oscEsc("0;" + check + " done")},
		{"osc2_check_esc_st", "\x1b]2;" + check + " done\x1b\\", oscEsc("2;" + check + " done")},
		{"osc2_title_ends_in_sparkle_bel", "\x1b]2;x " + sparkle + "\x07", osc("2;x " + sparkle)},
		{"osc2_title_ends_in_check_esc_st", "\x1b]2;x " + check + "\x1b\\", oscEsc("2;x " + check)},
		{"osc2_both_c1_st", "\x1b]2;" + sparkle + check + "\x9c", osc("2;" + sparkle + check)},
		{"osc2_four_byte_two_9c", "\x1b]2;" + emoji + "\x07", osc("2;" + emoji)},
		{"osc2_ideograph", "\x1b]2;" + ideo + "\x07", osc("2;" + ideo)},
		{"osc2_two_byte", "\x1b]2;\u00dcber \u041c\x07", osc("2;\u00dcber \u041c")}, // C3 9C, D0 9C
		{"osc2_third_byte", "\x1b]2;\u305c\x1b\\", oscEsc("2;\u305c")},              // E3 81 9C
		{"c1_osc_c1_st", "\x9d2;" + sparkle + "\x9c", osc("2;" + sparkle)},
		{"osc_8bit_st_after_ascii", "\x1b]2;abc\x9cZ", append(osc("2;abc"), 'Z')},
		{"osc_8bit_st_after_char", "\x1b]2;" + check + "\x9cZ", append(osc("2;"+check), 'Z')},
		{"osc_can_cancels", "\x1b]2;" + sparkle + "\x18Z", []any{'Z'}},
		{"osc_sub_cancels", "\x1b]2;" + sparkle + "\x1aZ", []any{'Z'}},
		{"osc_followed_by_print", "\x1b]0;" + sparkle + " t\x07" + sparkle, []any{[]byte("0;" + sparkle + " t"), '✳'}},
		{"dcs_sparkle_esc_st", "\x1bP1$q" + sparkle + "\x1b\\", []any{
			dcsSequence{Cmd: 'q' | '$'<<parser.IntermedShift, Params: Params{1}, Data: []byte(sparkle)}, Cmd('\\'),
		}},
		{"dcs_8bit_st_after_char", "\x1bP1$q" + check + "\x9c", []any{
			dcsSequence{Cmd: 'q' | '$'<<parser.IntermedShift, Params: Params{1}, Data: []byte(check)},
		}},
		{"apc_sparkle_esc_st", "\x1b_G" + sparkle + "\x1b\\", oscEsc("G" + sparkle)},
		{"apc_check_8bit_st", "\x1b_G" + check + "\x9c", osc("G" + check)},
		{"apc_c1_apc", "\x9fG" + check + "\x9c", osc("G" + check)},
	}

	run := func(t *testing.T, c testCase, parts ...string) {
		t.Helper()
		d := &testDispatcher{}
		p := testParser(d)
		for _, part := range parts {
			p.Parse([]byte(part))
		}
		if len(d.dispatched) != len(c.expected) {
			t.Fatalf("parts %q: got %d items %#v, want %#v", parts, len(d.dispatched), d.dispatched, c.expected)
		}
		assertEqual(t, c.expected, d.dispatched)
		if p.State() != parser.GroundState {
			t.Fatalf("parts %q: parser left in %s", parts, p.StateName())
		}
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			run(t, c, c.input)
			for i := 1; i < len(c.input); i++ {
				run(t, c, c.input[:i], c.input[i:])
			}
			bytes := make([]string, len(c.input))
			for i := 0; i < len(c.input); i++ { // bytes, not runes
				bytes[i] = c.input[i : i+1]
			}
			run(t, c, bytes...)
		})
	}
}

// TestStringDataUtf8NotSTReset checks that an unfinished UTF-8 character at
// the end of one string does not carry over to the next string.
func TestStringDataUtf8NotSTReset(t *testing.T) {
	d := &testDispatcher{}
	p := testParser(d)
	// The first OSC ends right after a lead byte (cut off by ESC \). The next
	// OSC must still end on a raw 0x9C after ASCII.
	p.Parse([]byte("\x1b]2;a\xe2\x1b\\\x1b]2;b\x9c"))
	assertEqual(t, []any{[]byte("2;a\xe2"), Cmd('\\'), []byte("2;b")}, d.dispatched)

	d.dispatched = nil
	p.Parse([]byte("\x1b]2;a\xe2"))
	p.Reset()
	p.Parse([]byte("\x1b]2;b\x9c"))
	assertEqual(t, []any{[]byte("2;b")}, d.dispatched)
}
