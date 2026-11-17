package tests

import (
	"fmt"
	"testing"

	"github.com/charmbracelet/x/vt"
)

// The family emoji, in the form a program writes it: man, ZWJ, woman, ZWJ, girl.
const zwjFamily = "\U0001F468\u200d\U0001F469\u200d\U0001F467"

// A program does not choose where its output is split. A ZWJ emoji family that
// arrives in two writes must still be one wide cell pair, as it is when it
// arrives in one. Unpatched x/vt v0.1.0 commits each half as its own cluster,
// so this test fails when the replace lines in go.mod are removed: it is the
// proof that the patched copy in third_party/patched/x-vt is the one in use.
func TestVTSplitCluster(t *testing.T) {
	input := "A" + zwjFamily + "B"
	check := func(t *testing.T, parts ...string) {
		t.Helper()
		term := vt.NewEmulator(8, 2)
		defer term.Close()
		for _, p := range parts {
			if _, err := term.WriteString(p); err != nil {
				t.Fatal(err)
			}
		}
		// A, the family in two columns, B: the cursor ends at column 4, not 6.
		for x, want := range []struct {
			content string
			width   int
		}{{"A", 1}, {zwjFamily, 2}, {"", 0}, {"B", 1}} {
			cell := term.CellAt(x, 0)
			if cell == nil || cell.Content != want.content || (want.content != "" && cell.Width != want.width) {
				t.Fatalf("writes %q: cell %d is %+v, want %q of width %d", parts, x, cell, want.content, want.width)
			}
		}
		if pos := term.CursorPosition(); pos.X != 4 || pos.Y != 0 {
			t.Fatalf("writes %q: cursor at %v, want (4, 0)", parts, pos)
		}
	}
	t.Run("one write", func(t *testing.T) { check(t, input) })
	t.Run("after the first ZWJ", func(t *testing.T) { check(t, "A\U0001F468\u200d", "\U0001F469\u200d\U0001F467B") })
	t.Run("between ZWJ and the next person", func(t *testing.T) { check(t, "A\U0001F468\u200d\U0001F469\u200d", "\U0001F467B") })
	// Every byte boundary, which includes the ones inside a UTF-8 sequence.
	for i := 1; i < len(input); i++ {
		t.Run(fmt.Sprintf("split at byte %d", i), func(t *testing.T) { check(t, input[:i], input[i:]) })
	}
}
