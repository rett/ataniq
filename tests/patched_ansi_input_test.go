package tests

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// decodeInput feeds input to the reader Bubble Tea uses for terminal input
// (ultraviolet's TerminalReader) and returns the events it produced. On Unix
// the reader scans input itself with x/ansi's constants; the tests that use it
// check that the console's input still decodes with the patched x/ansi linked
// in, not that they exercise the patch.
func decodeInput(t *testing.T, input string) []uv.Event {
	t.Helper()
	reader := uv.NewTerminalReader(strings.NewReader(input), "xterm-256color")
	events := make(chan uv.Event)
	done := make(chan []uv.Event)
	go func() {
		var got []uv.Event
		for e := range events {
			got = append(got, e)
		}
		done <- got
	}()
	if err := reader.StreamEvents(context.Background(), events); err != nil {
		t.Fatalf("decode %q: %v", input, err)
	}
	close(events)
	return <-done
}

// keyStrings returns the Bubble Tea names of the key presses among events.
func keyStrings(events []uv.Event) []string {
	var keys []string
	for _, e := range events {
		if k, ok := e.(uv.KeyPressEvent); ok {
			keys = append(keys, tea.KeyPressMsg(k).String())
		}
	}
	return keys
}

// The console's planned return key, Ctrl+\, arrives as 0x1c in the legacy
// encoding and as CSI 92;5u in the kitty encoding. Ctrl+4 in the kitty
// encoding, CSI 52;5u, is a different key and must not decode as Ctrl+\.
func TestPatchedANSIReturnKeyDecoding(t *testing.T) {
	for _, tc := range []struct{ name, input, want string }{
		{"legacy 0x1c", "\x1c", "ctrl+\\"},
		{"kitty CSI 92;5u", "\x1b[92;5u", "ctrl+\\"},
		{"kitty CSI 52;5u (Ctrl+4)", "\x1b[52;5u", "ctrl+4"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			keys := keyStrings(decodeInput(t, tc.input))
			if len(keys) != 1 || keys[0] != tc.want {
				t.Fatalf("%q decoded as %q, want [%s]", tc.input, keys, tc.want)
			}
		})
	}
}

// A bracketed paste whose text holds characters with a 0x9C byte in their
// UTF-8 encoding (U+2733 is E2 9C B3, U+2705 is E2 9C 85) arrives whole.
func TestPatchedANSIBracketedPaste(t *testing.T) {
	const text = "✳ Claude ✅ done\nsecond line ✳"
	events := decodeInput(t, "\x1b[200~"+text+"\x1b[201~"+"q")
	var pasted string
	var starts, ends int
	var keys []string
	for _, e := range events {
		switch e := e.(type) {
		case uv.PasteStartEvent:
			starts++
		case uv.PasteEvent:
			pasted += e.Content
		case uv.PasteEndEvent:
			ends++
		case uv.KeyPressEvent:
			keys = append(keys, tea.KeyPressMsg(e).String())
		}
	}
	if pasted != text || starts != 1 || ends != 1 {
		t.Errorf("paste %q (starts %d, ends %d) from events %#v, want %q", pasted, starts, ends, events, text)
	}
	if len(keys) != 1 || keys[0] != "q" {
		t.Errorf("input after the paste decoded as %q, want [q]", keys)
	}
	// Bubble Tea passes the same events on as its own message types.
	for _, e := range events {
		if p, ok := e.(uv.PasteEvent); ok && tea.PasteMsg(p).Content != p.Content {
			t.Errorf("tea.PasteMsg changes the content: %q", tea.PasteMsg(p).Content)
		}
	}
}

// An SGR mouse report decodes to a click with the cell and button, and the
// release that follows decodes as a release.
func TestPatchedANSISGRMouse(t *testing.T) {
	events := decodeInput(t, "\x1b[<0;5;3M\x1b[<0;5;3m")
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2: %#v", len(events), events)
	}
	click, ok := events[0].(uv.MouseClickEvent)
	if !ok || click.X != 4 || click.Y != 2 || click.Button != uv.MouseLeft {
		t.Errorf("click: %#v", events[0])
	}
	if tea.MouseClickMsg(click).String() != "left" {
		t.Errorf("tea click message: %q", tea.MouseClickMsg(click).String())
	}
	if release, ok := events[1].(uv.MouseReleaseEvent); !ok || release.X != 4 || release.Y != 2 || release.Button != uv.MouseLeft {
		t.Errorf("release: %#v", events[1])
	}
}

// The patched parser keeps the character U+2733 inside an OSC title while the
// 8-bit ST (a 0x9C byte after a complete character) and BEL still end it. This
// is what the x/ansi patches add, so the test fails with the unpatched module.
func TestPatchedANSIStringTerminatorInsideCharacter(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		osc         []string
		printed     string
	}{
		{"BEL after a title with U+2733", "\x1b]0;✳ x\a", []string{"0;✳ x"}, ""},
		{"ESC ST after a title ending in U+2705", "\x1b]2;done ✅\x1b\\", []string{"2;done ✅"}, ""},
		{"8-bit ST after ASCII still ends it", "\x1b]0;a\x9cb", []string{"0;a"}, "b"},
		{"8-bit ST after a character still ends it", "\x1b]0;✳\x9cb", []string{"0;✳"}, "b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var osc []string
			var printed strings.Builder
			p := ansi.NewParser()
			p.SetHandler(ansi.Handler{
				HandleOsc: func(cmd int, data []byte) { osc = append(osc, string(data)) },
				Print:     func(r rune) { printed.WriteRune(r) },
			})
			p.Parse([]byte(tc.input))
			if len(osc) != len(tc.osc) || (len(osc) == 1 && osc[0] != tc.osc[0]) || printed.String() != tc.printed {
				t.Errorf("OSC data %q and printed %q, want %q and %q", osc, printed.String(), tc.osc, tc.printed)
			}
		})
	}
	// DecodeSequence applies the same rule.
	in := []byte("\x1b]0;✳ x\a")
	seq, _, n, _ := ansi.DecodeSequence(in, ansi.NormalState, nil)
	if n != len(in) || string(seq) != string(in) {
		t.Errorf("DecodeSequence ended the title early: %q (%d of %d bytes)", seq, n, len(in))
	}
}
